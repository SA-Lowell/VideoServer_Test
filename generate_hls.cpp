#include <iostream>
#include <cstdlib>  // for std::system
#include <filesystem>  // C++17: for paths
#include <string>
#include <fstream>
#include <sstream>

namespace fs = std::filesystem;

// Trim leading and trailing whitespace
std::string trim(const std::string& str) {
    size_t first = str.find_first_not_of(" \t\r\n");
    if (first == std::string::npos) return "";
    size_t last = str.find_last_not_of(" \t\r\n");
    return str.substr(first, (last - first + 1));
}

// fixM3U8Paths ensures relative segment paths for VLC local playback
bool fixM3U8Paths(const std::string& m3u8Path, const std::string& hlsDir) {
    std::cout << "DEBUG: Starting fixM3U8Paths for: " << m3u8Path << " (dir: " << hlsDir << ")" << std::endl;
    std::ifstream inFile(m3u8Path);
    if (!inFile.is_open()) {
        std::cout << "DEBUG: Could not open m3u8 for reading: " << m3u8Path << std::endl;
        return false;
    }

    std::ofstream outFile(m3u8Path + ".tmp");
    if (!outFile.is_open()) {
        std::cout << "DEBUG: Could not open temp file for writing" << std::endl;
        inFile.close();
        return false;
    }

    std::string line;
    int lineCount = 0;
    int fixedCount = 0;
    while (std::getline(inFile, line)) {
        lineCount++;
        // Trim leading and trailing whitespace
        line = trim(line);
        std::cout << "DEBUG: Line " << lineCount << ": '" << line << "'" << std::endl;
        // Check if line is a segment reference (doesn't start with #, not empty, ends with .ts)
        if (!line.empty() && line[0] != '#' && line.size() >= 3 && line.substr(line.size() - 3) == ".ts") {
            // Extract just the filename for relative path
            std::string relativePath = fs::path(line).filename().string();
            outFile << relativePath << std::endl;
            std::cout << "DEBUG: Fixed to relative: '" << line << "' -> '" << relativePath << "'" << std::endl;
            fixedCount++;
        } else {
            outFile << line << std::endl;
        }
    }
    std::cout << "DEBUG: Processed " << lineCount << " lines, fixed " << fixedCount << " paths to relative" << std::endl;

    inFile.close();
    outFile.close();

    if (fs::exists(m3u8Path + ".tmp")) {
        fs::remove(m3u8Path);
        fs::rename(m3u8Path + ".tmp", m3u8Path);
        std::cout << "DEBUG: Fixed m3u8 saved: " << m3u8Path << std::endl;
        return true;
    }
    return false;
}

// generateHLS generates HLS segments for a file (duration in seconds)
bool generateHLS(const fs::path& inputFile, const fs::path& outputPlaylist, int segmentDur, double targetFPS) {
    std::cout << "DEBUG: generateHLS called: input=" << inputFile << " playlist=" << outputPlaylist << " segDur=" << segmentDur << " fps=" << targetFPS << std::endl;
    fs::create_directories(outputPlaylist.parent_path());
    std::cout << "DEBUG: Created dirs for HLS output" << std::endl;

    // Try copy first (fastest)
    std::string args = "-i \"" + inputFile.string() + "\" -r " + std::to_string(targetFPS) + " -c copy -start_number 0 -hls_time " + std::to_string(segmentDur) + " -hls_list_size 0 -hls_segment_type mpegts -f hls -movflags +faststart \"" + outputPlaylist.string() + "\"";
    std::string fullCmd = "ffmpeg " + args;
    std::cout << "DEBUG: Executing HLS copy: " << fullCmd << std::endl;  // Debug
    int result = std::system(fullCmd.c_str());
    if (result != 0) {
        std::cout << "DEBUG: HLS copy failed, falling back to re-encode: " << result << std::endl;
        // Fallback: Re-encode with veryfast
        args = "-i \"" + inputFile.string() + "\" -r " + std::to_string(targetFPS) + " -c:v libx264 -preset veryfast -crf 25 -c:a aac -b:a 128k -start_number 0 -hls_time " + std::to_string(segmentDur) + " -hls_list_size 0 -hls_segment_type mpegts -f hls -movflags +faststart \"" + outputPlaylist.string() + "\"";
        fullCmd = "ffmpeg " + args;
        std::cout << "DEBUG: Executing fallback HLS: " << fullCmd << std::endl;  // Debug
        result = std::system(fullCmd.c_str());
        if (result != 0) {
            std::cout << "DEBUG: HLS fallback failed: " << result << std::endl;
            return false;
        }
    }
    // Post-process m3u8 to use relative paths for local VLC playback
    std::string hlsDir = outputPlaylist.parent_path().string();
    if (!fixM3U8Paths(outputPlaylist.string(), hlsDir)) {
        std::cout << "DEBUG: Failed to fix m3u8 paths" << std::endl;
        return false;
    }

    std::cout << "DEBUG: Generated HLS with relative paths: " << outputPlaylist << std::endl;
    return true;
}

// probeVideoForFPS probes only for FPS (improved with targeted ffprobe output)
bool probeVideoForFPS(const std::string& filePath, double& fps) {
    std::cout << "DEBUG: probeVideoForFPS called for file: " << filePath << std::endl;
    if (!fs::exists(filePath)) {
        std::cout << "DEBUG: File does not exist: " << filePath << std::endl;
        fps = 30.0;
        return false;
    }

    std::string cmd = "ffprobe -v error -select_streams v:0 -show_entries stream=r_frame_rate -of csv=s=x:p=0 \"" + filePath + "\" 2>nul";
    std::cout << "DEBUG: Running ffprobe command: " << cmd << std::endl;
    FILE* pipe = _popen(cmd.c_str(), "r");  // Windows popen
    if (!pipe) {
        std::cout << "DEBUG: Failed to open pipe for ffprobe" << std::endl;
        fps = 30.0;
        return false;
    }

    std::string output;
    char buffer[1024];
    while (fgets(buffer, sizeof buffer, pipe) != NULL) {
        output += buffer;
    }
    _pclose(pipe);

    output = trim(output);
    if (output.empty()) {
        std::cout << "DEBUG: ffprobe output is empty" << std::endl;
        fps = 30.0;
        return false;
    }

    try {
        size_t slash = output.find('/');
        if (slash != std::string::npos) {
            double num = std::stod(output.substr(0, slash));
            double den = std::stod(output.substr(slash + 1));
            if (den != 0.0) {
                fps = num / den;
            } else {
                throw std::invalid_argument("Denominator is zero");
            }
        } else {
            fps = std::stod(output);
        }
        std::cout << "DEBUG: Parsed FPS: " << fps << std::endl;
        return true;
    } catch (...) {
        std::cout << "DEBUG: Failed to parse FPS from '" << output << "', using default" << std::endl;
        fps = 30.0;
        return true;
    }
}

int main(int argc, char* argv[]) {
    if (argc < 4) {
        std::cerr << "Usage: " << argv[0] << " <segmentDur> <input_file> <output_dir>" << std::endl;
        std::cerr << "All parameters are required." << std::endl;
        return 1;
    }

    int segmentDur = std::stoi(argv[1]);
    fs::path input_file(argv[2]);
    fs::path output_dir(argv[3]);
    std::cout << "DEBUG: Parsed args: segmentDur=" << segmentDur << ", input_file=" << input_file << ", output_dir=" << output_dir << std::endl;

    fs::create_directories(output_dir);
    std::cout << "DEBUG: Created output dir" << std::endl;

    if (!fs::exists(input_file)) {
        std::cout << "Input file not found: " << input_file << std::endl;
        return 1;
    }

    double targetFPS = 30.0;
    if (!probeVideoForFPS(input_file.string(), targetFPS)) {
        std::cout << "FPS probe failed, using default 30.0" << std::endl;
    }

    fs::path playlist = output_dir / "playlist.m3u8";
    std::cout << "Generating HLS..." << std::endl;
    if (!generateHLS(input_file, playlist, segmentDur, targetFPS)) {
        std::cout << "HLS generation failed!" << std::endl;
        return 1;
    } else {
        std::cout << "HLS playlist ready in " << playlist << std::endl;
    }
    return 0;
}