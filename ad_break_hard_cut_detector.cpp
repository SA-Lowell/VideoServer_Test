#include <iostream>
#include <string>
#include <vector>
#include <sstream>
#include <iomanip>
#include <cstdlib>
#include <cstdio>
#include <algorithm>

struct Period
{
    double start;
    double end;
};

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

std::string secondsToMMSS(double seconds)
{
    int min = static_cast<int>(seconds / 60);
    double sec = seconds - min * 60;
    std::ostringstream oss;
    oss << std::setfill('0') << std::setw(2) << min << ":" << std::fixed << std::setprecision(21) << std::setw(24) << sec;

    return oss.str();
}

std::vector<Period> parseSilence(const std::string& output)
{
    std::vector<Period> periods;
    std::istringstream iss(output);
    std::string line;
    double current_start = -1.0;

    while(std::getline(iss, line))
    {
        size_t pos_start = line.find("[silencedetect @");

        if(pos_start == std::string::npos)continue;

        size_t pos_silence_start = line.find("silence_start: ");
        size_t pos_silence_end = line.find("silence_end: ");

        if(pos_silence_start != std::string::npos)
        {
            current_start = std::stod(line.substr(pos_silence_start + 15));
        }
        else if(pos_silence_end != std::string::npos && current_start >= 0.0)
        {
            size_t end_pos = pos_silence_end + 13;
            size_t space_pos = line.find(' ', end_pos);
            double end = std::stod(line.substr(end_pos, space_pos - end_pos));

            if(end - current_start >= 0.005)
            {
                periods.push_back({current_start, end});
            }

            current_start = -1.0;
        }
    }

    return periods;
}

std::vector<double> parseScenes(const std::string& output)
{
    std::vector<double> scenes;
    std::istringstream iss(output);
    std::string line;

    while(std::getline(iss, line))
    {
        size_t pos_showinfo = line.find("[Parsed_showinfo");

        if(pos_showinfo != std::string::npos)
        {
            size_t pos_pts_time = line.find("pts_time:");

            if(pos_pts_time != std::string::npos)
            {
                size_t start_pos = pos_pts_time + 9;
                size_t end_pos = line.find(' ', start_pos);
                if(end_pos == std::string::npos) end_pos = line.length();
                double time = std::stod(line.substr(start_pos, end_pos - start_pos));
                scenes.push_back(time);
            }
        }
    }

    return scenes;
}

std::vector<double> findSilentScenes(const std::vector<Period>& silences, const std::vector<double>& scenes, double min_silence_duration = 0.01)
{
    std::vector<double> silent_scenes;

    for(const auto& sc : scenes)
    {
        for(const auto& sil : silences)
        {
            if(sc >= sil.start && sc < sil.end && (sil.end - sil.start >= min_silence_duration))
            {
                silent_scenes.push_back(sc);
                break;
            }
        }
    }

    if(silent_scenes.empty()) return silent_scenes;

    std::sort(silent_scenes.begin(), silent_scenes.end());

    return silent_scenes;
}

int main(int argc, char* argv[])
{
    if(argc < 2)
    {
        std::cerr << "Usage: " << argv[0] << " <video_file> [--no-format] [--hide-decimal] [--hide-mmss] [--hide-start] [--hide-midpoint] [--hide-end] [--silence-db <db>] [--silence-dur <dur>] [--scene-thresh <thresh>]" << std::endl;
        return 1;
    }

    std::string video = argv[1];

    bool show_decimal = true;
    bool no_format = false;
    bool show_mmss = true;
    bool show_start = true;
    bool show_midpoint = true;
    bool show_end = true;
    double silence_db = -40.0;
    double silence_dur = 0.01;
    double scene_thresh = 0.2;

    for(int i = 2; i < argc; ++i)
    {
        std::string arg = argv[i];

        if(arg == "--hide-decimal")show_decimal = false;
        else if(arg == "--hide-mmss")show_mmss = false;
        else if(arg == "--hide-start")show_start = false;
        else if(arg == "--hide-midpoint")show_midpoint = false;
        else if(arg == "--hide-end")show_end = false;
        else if(arg == "--no-format")no_format = true;
        else if(arg == "--silence-db" && i + 1 < argc)
        {
            silence_db = std::stod(argv[++i]);
        }
        else if(arg == "--silence-dur" && i + 1 < argc)
        {
            silence_dur = std::stod(argv[++i]);
        }
        else if(arg == "--scene-thresh" && i + 1 < argc)
        {
            scene_thresh = std::stod(argv[++i]);
        }
        else
        {
            std::cerr << "Unknown option: " << arg << std::endl;
            return 1;
        }
    }

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

    if(!no_format)std::cout << "Video duration: " << std::fixed << std::setprecision(21) << video_duration << std::endl;

    std::ostringstream silence_cmd_ss;
    silence_cmd_ss << "ffmpeg -i \"" << video << "\" -af silencedetect=noise=" << silence_db << "dB:d=" << silence_dur << " -f null - 2>&1";
    std::string silence_output = exec(silence_cmd_ss.str());
    std::vector<Period> silences = parseSilence(silence_output);

    std::ostringstream scene_cmd_ss;
    scene_cmd_ss << "ffmpeg -i \"" << video << "\" -vf \"select=gt(scene\\," << scene_thresh << "),showinfo\" -f null - 2>&1";
    std::string scene_output = exec(scene_cmd_ss.str());
    std::vector<double> scenes = parseScenes(scene_output);

    std::vector<double> ad_points = findSilentScenes(silences, scenes, silence_dur);

    std::vector<double> filtered_points;
    const double epsilon = 1.0;

    for(const auto& p : ad_points)
    {
        if(p > epsilon && p < (video_duration - epsilon))
        {
            filtered_points.push_back(p);
        }
    }

    if(filtered_points.empty())
    {
        std::cout << "No suitable ad insertion points detected." << std::endl;
    }
    else
    {
        bool output_written = false;
        for(const auto& p : filtered_points)
        {
            double point = p;

            if(no_format)
            {
                if(show_decimal && show_midpoint)
                {
                    if(output_written)std::cout << ' ';
                    std::cout << std::fixed << std::setprecision(21) << point;
                    output_written = true;
                }
            }
            else
            {
                std::cout << "Potential ad insertion point:" << std::endl;

                if(show_decimal && show_midpoint)
                {
                    std::cout << "\tDecimal seconds:" << std::endl;
                    std::cout << "\t\tPoint: " << std::fixed << std::setprecision(21) << point << std::endl;
                    std::cout << std::endl;
                }

                if(show_mmss && show_midpoint)
                {
                    std::cout << "\tMM:SS.d" << std::endl;
                    std::cout << "\t\tPoint: " << secondsToMMSS(point) << std::endl;
                    std::cout << std::endl;
                }
            }
        }
        if(no_format && output_written) std::cout << std::endl;
    }

    return 0;
}