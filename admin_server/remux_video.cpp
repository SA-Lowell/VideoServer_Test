#include <iostream>
#include <string>
#include <cstdlib>
#include <cstdio>
#include <filesystem>

std::string exec(const std::string& cmd) {
    std::string result = "";
    char buffer[128];
    FILE* pipe = popen(cmd.c_str(), "r");
    if (!pipe) return "ERROR";
    while (!feof(pipe)) {
        if (fgets(buffer, 128, pipe) != NULL) result += buffer;
    }
    pclose(pipe);
    return result;
}

int main(int argc, char* argv[]) {
    if (argc < 2) {
        std::cerr << "Usage: " << argv[0] << " <input_video_file>" << std::endl;
        return 1;
    }

    std::filesystem::path input_path = argv[1];
    std::filesystem::path parent_dir = input_path.parent_path();
    std::filesystem::path filename = input_path.filename();
    std::filesystem::path old_version_dir = parent_dir / "Old Versions (Delete This)";
    std::filesystem::path old_file_path = old_version_dir / filename;
    std::filesystem::path output_path = parent_dir / filename;

    // Create the "Old Versions (Delete This)" directory if it doesn't exist
    try {
        std::filesystem::create_directory(old_version_dir);
    } catch (const std::filesystem::filesystem_error& e) {
        std::cerr << "Failed to create directory " << old_version_dir << ": " << e.what() << std::endl;
        return 1;
    }

    // Move the original file to the subfolder
    try {
        std::filesystem::rename(input_path, old_file_path);
    } catch (const std::filesystem::filesystem_error& e) {
        std::cerr << "Failed to move original file to " << old_version_dir << ": " << e.what() << std::endl;
        return 1;
    }

    // Construct the ffmpeg command with platform-independent paths
    std::string cmd = "ffmpeg -i \"" + old_file_path.string() + "\" -c copy \"" + output_path.string() + "\" 2>&1";
    std::cout << "Executing command: " << cmd << std::endl;

    std::string ffmpeg_output = exec(cmd);

    std::cout << "FFmpeg output:\n" << ffmpeg_output << std::endl;

    if (ffmpeg_output.find("Output file is empty") != std::string::npos || ffmpeg_output.find("Error") != std::string::npos) {
        std::cerr << "Failed to fix the video file." << std::endl;
        return 1;
    }

    std::cout << "Fixed video saved as: " << output_path.string() << std::endl;
    return 0;
}