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

bool check_nvenc_support() {
    std::string cmd = "ffmpeg -encoders > tmp_encoders.txt 2>&1";
    int ret = std::system(cmd.c_str());
    if (ret != 0) {
        log_message("Error: Failed to check encoder support. Assuming no NVENC.");
        return false;
    }
    std::ifstream encoders_file("tmp_encoders.txt");
    std::string line;
    bool nvenc_supported = false;
    while (std::getline(encoders_file, line)) {
        if (line.find("h264_nvenc") != std::string::npos) {
            nvenc_supported = true;
            break;
        }
    }
    encoders_file.close();
    std::remove("tmp_encoders.txt");
    if (!nvenc_supported) {
        log_message("NVENC not supported. Falling back to libx264.");
    }
    return nvenc_supported;
}

std::string escape_path(const std::string& path) {
#ifdef _WIN32
    return "\"" + path + "\"";
#else
    std::string escaped = path;
    return "\"" + escaped + "\"";
#endif
}

void process_file(const std::string& input_path) {
    std::string output_path = "tmp_video_output_directory/" + fs::path(input_path).stem().string() + ".mkv";

    // Choose encoder
    std::string video_encoder = "h264_nvenc";
    int crf_value = 20;

    bool use_nvenc = check_nvenc_support();
    if (!use_nvenc) {
        video_encoder = "libx264";
        crf_value = 18;
    }

    log_message("Using CRF " + std::to_string(crf_value) + " with " + video_encoder + " for " + input_path);

    // Assume scaling to 720p (simplified due to ffprobe issues)
    std::string video_filter = "-vf \"scale=1280:720,setsar=1:1\"";

    // Encoding command
    std::string preset = use_nvenc ? "-preset p7" : "-preset fast";
    std::string log_file_name = "tmp_video_output_directory/ffmpeg_log_" + fs::path(input_path).filename().string() + ".txt";
    std::string cmd_crf = "ffmpeg -y -i " + escape_path(input_path) + " -c:v " + video_encoder + " -crf " + std::to_string(crf_value) + " " + preset + " " + video_filter + " -pix_fmt yuv420p -c:a copy -map 0 -map_metadata -1 -f matroska " + escape_path(output_path) + " > " + escape_path(log_file_name) + " 2>&1";

    log_message("Running encoding for " + input_path + " with command: " + cmd_crf);
    int ret = std::system(cmd_crf.c_str());
    if (ret != 0) {
        std::ifstream log_file(log_file_name);
        std::stringstream log_content;
        std::string line;
        while (std::getline(log_file, line)) {
            log_content << line << "\n";
        }
        log_file.close();
        log_message("FFmpeg error output:\n" + log_content.str());
        log_message("Failed to convert " + input_path + ". Check " + log_file_name);
        return;
    }

    log_message("Successfully converted " + input_path + " to " + output_path);

    // Log file sizes
    double input_size = get_file_size(input_path);
    double output_size = get_file_size(output_path);
    log_message("Input file size for " + input_path + ": " + std::to_string(input_size) + " MB");
    log_message("Output file size for " + output_path + ": " + std::to_string(output_size) + " MB");
    log_message("Size difference: " + std::to_string(output_size - input_size) + " MB (" + 
                std::to_string((output_size - input_size) / input_size * 100) + "%)");
}

int main(int argc, char* argv[]) {
    // Check for FFmpeg
    if (!check_ffmpeg()) {
        return 1;
    }

    // Check for command-line arguments
    if (argc < 2) {
        log_message("Usage: " + std::string(argv[0]) + " <input1.mkv> [<input2.mkv> ...]");
        return 1;
    }

    // Create output directory
    fs::create_directory("tmp_video_output_directory");
    log_message("Starting conversion for " + std::to_string(argc - 1) + " files");

    // Collect input files
    std::vector<std::string> input_files;
    for (int i = 1; i < argc; ++i) {
        fs::path input_path = argv[i];
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

    if (input_files.empty()) {
        log_message("No valid .mkv files provided.");
        return 1;
    }

    // Set max parallel jobs
    const size_t max_parallel_jobs = std::min<size_t>(2, std::thread::hardware_concurrency() / 2);
    log_message("Running up to " + std::to_string(max_parallel_jobs) + " parallel jobs");

    // Process files in parallel
    std::vector<std::thread> threads;
    std::queue<std::string> file_queue;
    for (const auto& file : input_files) {
        file_queue.push(file);
    }

    while (!file_queue.empty()) {
        while (!file_queue.empty() && threads.size() < max_parallel_jobs) {
            std::string file = file_queue.front();
            file_queue.pop();
            threads.emplace_back(process_file, file);
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