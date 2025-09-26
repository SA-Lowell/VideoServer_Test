#include <iostream>
#include <string>
#include <sstream>
#include <iomanip>
#include <cstdlib>
#include <cstdio>
#include <algorithm>

std::string exec(const std::string& cmd)
{
    std::string result = "";
    char buffer[128];
    FILE* pipe = popen(cmd.c_str(), "r");

    if(!pipe)return "ERROR";

    while(!feof(pipe))
    {
        if(fgets(buffer, 128, pipe) != NULL)result += buffer;
    }

    pclose(pipe);

    return result;
}

int main(int argc, char* argv[])
{
    if(argc < 3)
    {
        std::cerr << "Usage: " << argv[0] << " <video_file> <timestamp_in_seconds> [output_prefix]" << std::endl;
        return 1;
    }

    std::string video = argv[1];
    double timestamp = std::stod(argv[2]);
    std::string prefix = (argc > 3) ? argv[3] : "";

    // Extract extension
    std::string ext = ".mp4"; // Default assumption
    size_t dot_pos = video.rfind('.');
    if(dot_pos != std::string::npos)
    {
        ext = video.substr(dot_pos);
    }

    // Get video duration
    std::string duration_cmd = "ffprobe -v error -show_entries format=duration -of default=noprint_wrappers=1:nokey=1 \"" + video + "\"";
    std::string duration_output = exec(duration_cmd);
    duration_output.erase(std::remove(duration_output.begin(), duration_output.end(), '\n'), duration_output.end());
    duration_output.erase(std::remove(duration_output.begin(), duration_output.end(), '\r'), duration_output.end());

    if(duration_output.empty() || duration_output == "ERROR")
    {
        std::cerr << "Failed to get video duration." << std::endl;
        return 1;
    }

    double video_duration = std::stod(duration_output);

    if(timestamp <= 0.0 || timestamp >= video_duration)
    {
        std::cerr << "Timestamp must be between 0 and " << video_duration << " seconds." << std::endl;
        return 1;
    }

    // Output file names
    std::string part1 = prefix + "part1" + ext;
    std::string part2 = prefix + "part2" + ext;

    // Command for part 1: from start to timestamp, stream copy
    std::ostringstream ts_ss;
    ts_ss << std::fixed << std::setprecision(21) << timestamp;
    std::string part1_cmd = "ffmpeg -i \"" + video + "\" -t " + ts_ss.str() + " -c copy -y \"" + part1 + "\"";

    // Command for part 2: from timestamp to end, stream copy (fast seek)
    std::string part2_cmd = "ffmpeg -ss " + ts_ss.str() + " -i \"" + video + "\" -c copy -y \"" + part2 + "\"";

    std::cout << "Splitting video at " << std::fixed << std::setprecision(21) << timestamp << " seconds." << std::endl;

    std::string out1 = exec(part1_cmd);
    if(out1 == "ERROR")
    {
        std::cerr << "Failed to create part 1." << std::endl;
        return 1;
    }

    std::string out2 = exec(part2_cmd);
    if(out2 == "ERROR")
    {
        std::cerr << "Failed to create part 2." << std::endl;
        return 1;
    }

    std::cout << "Created " << part1 << " (0 to " << timestamp << " seconds)" << std::endl;
    std::cout << "Created " << part2 << " (" << timestamp << " seconds to end)" << std::endl;
    std::cout << "Note: Using stream copy preserves original quality exactly, but the split may not be frame-accurate for the second part if the timestamp is not at a keyframe. For frame-accurate splits, re-encoding would be needed, which may slightly degrade quality." << std::endl;

    return 0;
}