#include <iostream>
#include <filesystem>
#include <fstream>
#include <string>
#include <vector>
#include <cstdlib>
#include <sstream>

namespace fs = std::filesystem;

const std::string log_file = "tmp_video_output_directory/conversion_errors.log";

void log_message(const std::string& message, bool to_console = true) {
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

double get_duration(const std::string& input_path) {
    std::string cmd = "ffprobe -v error -show_entries format=duration -of default=noprint_wrappers=1:nokey=1 \"" + input_path + "\" > tmp_duration.txt 2>&1";
    int ret = std::system(cmd.c_str());
    if (ret != 0) {
        log_message("Failed to get duration for " + input_path + ".");
        return 0.0;
    }
    std::ifstream dur_file("tmp_duration.txt");
    std::string duration_str;
    std::getline(dur_file, duration_str);
    dur_file.close();
    std::remove("tmp_duration.txt");
    try {
        return std::stod(duration_str);
    } catch (...) {
        log_message("Invalid duration for " + input_path + ".");
        return 0.0;
    }
}

std::string get_input_codec(const std::string& input_path) {
    std::string cmd = "ffprobe -v error -select_streams v:0 -show_entries stream=codec_name -of default=noprint_wrappers=1:nokey=1 \"" + input_path + "\" > tmp_codec.txt 2>&1";
    int ret = std::system(cmd.c_str());
    if (ret != 0) {
        log_message("Failed to get input codec for " + input_path + ". Defaulting to libx264.");
        return "unknown";
    }
    std::ifstream codec_file("tmp_codec.txt");
    std::string codec;
    std::getline(codec_file, codec);
    codec_file.close();
    std::remove("tmp_codec.txt");
    return codec;
}

int main(int argc, char* argv[]) {
    // Check for command-line argument
    if (argc != 2) {
        log_message("Usage: " + std::string(argv[0]) + " <input.mkv>");
        return 1;
    }

    fs::path input_path = argv[1];
    if (!fs::exists(input_path) || input_path.extension() != ".mkv") {
        log_message("Error: Provided file does not exist or is not an .mkv file.");
        return 1;
    }

    // Create output directory
    fs::create_directory("tmp_video_output_directory");
    log_message("Starting conversion for " + input_path.string());

    std::string output_path = "tmp_video_output_directory/" + input_path.stem().string() + ".mkv";

    // Detect input codec and choose encoder
    std::string input_codec = get_input_codec(input_path.string());
    log_message("Input video codec: " + input_codec);
    std::string video_encoder = "libx264";
    std::string profile = "-profile:v main";
    int crf_value = 20; // Default for libx264; close to original quality

    if (input_codec == "hevc") {
        video_encoder = "libx265";
        profile = "-profile:v main"; // For libx265
        crf_value = 26; // Adjusted for libx265 (roughly equivalent to libx264 CRF 20)
        log_message("Input is HEVC; using libx265 for better compression efficiency.");
    }

    log_message("Using CRF " + std::to_string(crf_value) + " with " + video_encoder + " for quality preservation and size reduction.");

    // CRF-based encoding command
    std::string cmd_crf = "ffmpeg -y -i \"" + input_path.string() + "\" -c:v " + video_encoder + " -crf " + std::to_string(crf_value) + " -preset veryslow " + profile + " -level 4.0 -vf \"scale=1280:720,setsar=1:1\" -pix_fmt yuv420p -vsync 2 -c:a copy -strict -2 -map 0 -map_metadata -1 -f matroska \"" + output_path + "\" >> tmp_video_output_directory/ffmpeg_log.txt 2>&1";

    log_message("Running CRF encoding for " + input_path.string());
    int ret = std::system(cmd_crf.c_str());
    if (ret != 0) {
        log_message("Failed to convert " + input_path.string() + ". Check tmp_video_output_directory/ffmpeg_log.txt");
        return 1;
    }

    log_message("Successfully converted " + input_path.string() + " to " + output_path);

    // Log file sizes
    double input_size = get_file_size(input_path);
    double output_size = get_file_size(output_path);
    log_message("Input file size: " + std::to_string(input_size) + " MB");
    log_message("Output file size: " + std::to_string(output_size) + " MB");
    log_message("Size difference: " + std::to_string(output_size - input_size) + " MB (" + 
                std::to_string((output_size - input_size) / input_size * 100) + "%)");

    log_message("Conversion complete. File saved to " + output_path);
    return 0;
}