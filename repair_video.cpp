#include <iostream>
#include <string>
#include <cstdlib>
#include <cstdio>

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

    std::string input = argv[1];
    size_t dot_pos = input.find_last_of('.');
    std::string output;
    if (dot_pos != std::string::npos) {
        output = input.substr(0, dot_pos) + "_fixed" + input.substr(dot_pos);
    } else {
        output = input + "_fixed";
    }

    std::string cmd = "ffmpeg -i \"" + input + "\" -c copy \"" + output + "\" 2>&1";
    std::cout << "Executing command: " << cmd << std::endl;

    std::string ffmpeg_output = exec(cmd);

    std::cout << "FFmpeg output:\n" << ffmpeg_output << std::endl;

    if (ffmpeg_output.find("Output file is empty") != std::string::npos || ffmpeg_output.find("Error") != std::string::npos) {
        std::cerr << "Failed to fix the video file." << std::endl;
        return 1;
    }

    std::cout << "Fixed video saved as: " << output << std::endl;
    return 0;
}