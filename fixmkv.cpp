#include <iostream>
#include <filesystem>
#include <fstream>
#include <string>
#include <vector>
#include <cstdlib>
#include <sstream>
#include <thread>
#include <mutex>
#include <queue>
#include <algorithm>

namespace fs = std::filesystem;

const std::string log_file = "tmp_video_output_directory/conversion_errors.log";
std::mutex log_mutex;

void log_message(const std::string& message, bool to_console = true) {
    std::lock_guard<std::mutex> lock(log_mutex);
    std::ofstream log(log_file, std::ios::app);
    log << message << "\n";
    if (to_console) std::cout << message << "\n";
}

double get_file_size(const fs::path& path) {
    try {
        return static_cast<double>(fs::file_size(path)) / (1024 * 1024); // MB
    } catch (...) {
        log_message("Failed to get file size for " + path.string());
        return 0.0;
    }
}

bool check_ffmpeg() {
    std::string cmd = "ffmpeg -version > tmp_ffmpeg_version.txt 2>&1";
    int ret = std::system(cmd.c_str());
    std::ifstream version_file("tmp_ffmpeg_version.txt");
    std::string version;
    std::getline(version_file, version);
    version_file.close();
    std::remove("tmp_ffmpeg_version.txt");
    if (ret != 0 || version.empty()) {
        log_message("Error: FFmpeg not found or failed to run. Ensure ffmpeg.exe is in PATH.");
        return false;
    }
    log_message("FFmpeg found: " + version);
    return true;
}

std::string sanitize_filename(const std::string& filename) {
    std::string safe = filename;
    std::replace(safe.begin(), safe.end(), '\'', '_');
    std::replace(safe.begin(), safe.end(), ' ', '_');
    return safe;
}

std::string escape_path(const std::string& path) {
#ifdef _WIN32
    return "\"" + path + "\""; // Simply wrap the path in double quotes
#else
    std::string escaped = path;
    std::string result;
    result.reserve(escaped.size() + 2);
    result += "\"";
    for (char c : escaped) {
        if (c == '\'') {
            result += "'\\''"; // Escape single quotes for Unix-like systems
        } else {
            result += c;
        }
    }
    result += "\"";
    return result;
#endif
}

bool is_scaling_needed(const std::string& input_path) {
    std::string safe_filename = sanitize_filename(fs::path(input_path).filename().string());
    std::string cmd = "ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of default=noprint_wrappers=1:nokey=1 " + escape_path(input_path) + " > tmp_resolution_" + safe_filename + ".txt 2>&1";
    int ret = std::system(cmd.c_str());
    if (ret != 0) {
        log_message("Failed to get resolution for " + input_path + ". Assuming scaling is needed.");
        return true;
    }
    std::ifstream res_file("tmp_resolution_" + safe_filename + ".txt");
    if (!res_file.is_open()) {
        log_message("Failed to open resolution file for " + input_path + ". Assuming scaling is needed.");
        return true;
    }
    std::string width_str, height_str;
    std::getline(res_file, width_str);
    std::getline(res_file, height_str);
    res_file.close();
    std::remove(("tmp_resolution_" + safe_filename + ".txt").c_str());
    try {
        int width = std::stoi(width_str);
        int height = std::stoi(height_str);
        if (width <= 1280 && height <= 720) {
            log_message("Input resolution is " + width_str + "x" + height_str + "; skipping scaling.");
            return false;
        }
        log_message("Input resolution is " + width_str + "x" + height_str + "; scaling to 1280x720.");
        return true;
    } catch (...) {
        log_message("Invalid resolution for " + input_path + ". Assuming scaling is needed.");
        return true;
    }
}

void process_file(const std::string& input_path, double duration = 0.0) {
    std::string output_path = "tmp_video_output_directory/" + fs::path(input_path).stem().string() + (duration > 0 ? "_test.mkv" : ".mkv");
    std::string video_encoder = "libx264";
    int crf_value = 24;
    log_message("Using CRF " + std::to_string(crf_value) + " with " + video_encoder + " for " + input_path);
    std::string video_filter = is_scaling_needed(input_path) ? "-vf \"scale=1280:720,setsar=1:1\"" : "";
    std::string preset = "-preset veryslow";
    std::string duration_str = (duration > 0) ? "-t " + std::to_string(duration) + " " : "";
    std::string safe_filename = sanitize_filename(fs::path(input_path).filename().string());
    std::string cmd_crf = "ffmpeg -y -i " + escape_path(input_path) + " " + duration_str + "-c:v " + video_encoder + " -crf " + std::to_string(crf_value) + " " + preset + " -profile:v main -pix_fmt yuv420p " + video_filter + " -c:a copy -map 0 -map_metadata -1 -f matroska " + escape_path(output_path) + " > tmp_video_output_directory/ffmpeg_log_" + safe_filename + (duration > 0 ? "_test.txt" : ".txt") + " 2>&1";

    log_message("Running encoding for " + input_path + " with command: " + cmd_crf);
    int ret = std::system(cmd_crf.c_str());
    if (ret != 0) {
        log_message("Failed to execute command: " + cmd_crf);
        std::ifstream log_file("tmp_video_output_directory/ffmpeg_log_" + safe_filename + (duration > 0 ? "_test.txt" : ".txt"));
        std::stringstream log_content;
        std::string line;
        while (std::getline(log_file, line)) {
            log_content << line << "\n";
        }
        log_file.close();
        log_message("FFmpeg error output:\n" + log_content.str());
        log_message("Failed to convert " + input_path + ". Check tmp_video_output_directory/ffmpeg_log_" + safe_filename + (duration > 0 ? "_test.txt" : ".txt"));
        return;
    }

    log_message("Successfully converted " + input_path + " to " + output_path);
    double input_size = get_file_size(input_path);
    double output_size = get_file_size(output_path);
    log_message("Input file size for " + input_path + ": " + std::to_string(input_size) + " MB");
    log_message("Output file size for " + output_path + ": " + std::to_string(output_size) + " MB");
    log_message("Size difference: " + std::to_string(output_size - input_size) + " MB (" + 
                std::to_string((output_size - input_size) / input_size * 100) + "%)");
}

int main(int argc, char* argv[]) {
    if (!check_ffmpeg()) {
        return 1;
    }

    if (argc < 2) {
        log_message("Usage: " + std::string(argv[0]) + " <input1.mkv> [<input2.mkv> ...] [-t duration_in_seconds]");
        return 1;
    }

    fs::create_directory("tmp_video_output_directory");
    log_message("Starting conversion for " + std::to_string(argc - 1) + " files");

    double duration = 0.0;
    std::vector<std::string> input_files;
    for (int i = 1; i < argc; ++i) {
        std::string arg = argv[i];
        if (arg == "-t" && i + 1 < argc) {
            try {
                duration = std::stod(argv[++i]);
                if (duration <= 0) {
                    log_message("Error: Duration must be positive. Using full file.");
                    duration = 0.0;
                }
            } catch (...) {
                log_message("Error: Invalid duration. Using full file.");
                duration = 0.0;
            }
        } else {
            fs::path input_path = arg;
            if (!fs::exists(input_path)) {
                log_message("Error: " + input_path.string() + " does not exist.");
                continue;
            }
            if (input_path.extension() != ".mkv") {
                log_message("Error: " + input_path.string() + " is not an .mkv file.");
                continue;
            }
            input_files.push_back(input_path.string());
        }
    }

    if (input_files.empty()) {
        log_message("No valid .mkv files provided.");
        return 1;
    }

    log_message("Using duration of " + std::to_string(duration) + " seconds (0 means full file).");

    const size_t max_parallel_jobs = std::min<size_t>(2, std::thread::hardware_concurrency() / 2);
    log_message("Running up to " + std::to_string(max_parallel_jobs) + " parallel jobs");

    std::vector<std::thread> threads;
    std::queue<std::string> file_queue;
    for (const auto& file : input_files) {
        file_queue.push(file);
    }

    while (!file_queue.empty()) {
        while (!file_queue.empty() && threads.size() < max_parallel_jobs) {
            std::string file = file_queue.front();
            file_queue.pop();
            threads.emplace_back(process_file, file, duration);
        }

        for (auto& t : threads) {
            if (t.joinable()) {
                t.join();
            }
        }
        threads.erase(std::remove_if(threads.begin(), threads.end(), 
            [](const std::thread& t) { return !t.joinable(); }), threads.end());
    }

    for (auto& t : threads) {
        if (t.joinable()) {
            t.join();
        }
    }

    log_message("Conversion complete. Files saved to tmp_video_output_directory");
    return 0;
}