#include <iostream>
#include <string>
#include <vector>
#include <sstream>
#include <iomanip>
#include <cstdlib>
#include <cstdio>
#include <algorithm>
#include <cctype>
struct Period
{
    double start;
    double end;
};
struct FrameData
{
    double timestamp;
    int black_percentage = 0;
    bool is_scene_change = false;
    double scene_score = 0.0;
    double rms_level = 0.0;
};
std::string exec(const std::string& cmd)
{
    std::string result = "";
    char buffer[128];
    FILE* pipe = popen(cmd.c_str(), "r");
    if(!pipe) return "ERROR";
    while(!feof(pipe))
    {
        if(fgets(buffer, 128, pipe) != NULL) result += buffer;
    }
    pclose(pipe);
    return result;
}
std::string secondsToMMSS(double seconds)
{
    int min = static_cast<int>(seconds / 60);
    double sec = seconds - min * 60;
    std::ostringstream oss;
    oss << std::setfill('0') << std::setw(2) << min << ":" << std::fixed << std::setprecision(3) << std::setw(6) << sec;
    return oss.str();
}
std::vector<Period> parseSilence(const std::string& output, double start_time)
{
    std::vector<Period> periods;
    std::istringstream iss(output);
    std::string line;
    double current_start = -1.0;
    while(std::getline(iss, line))
    {
        size_t pos_start = line.find("[silencedetect @");
        if(pos_start == std::string::npos) continue;
        size_t pos_silence_start = line.find("silence_start: ");
        size_t pos_silence_end = line.find("silence_end: ");
        if(pos_silence_start != std::string::npos)
        {
            size_t num_pos = pos_silence_start + 15;
            std::string num_str;
            while(num_pos < line.size() && (std::isdigit(line[num_pos]) || line[num_pos] == '.' || line[num_pos] == '-'))
            {
                num_str += line[num_pos];
                ++num_pos;
            }
            try {
                current_start = start_time + std::stod(num_str);
            } catch (...) {
                continue;
            }
        }
        else if(pos_silence_end != std::string::npos && current_start >= start_time)
        {
            size_t end_pos = pos_silence_end + 13;
            std::string end_str;
            while(end_pos < line.size() && (std::isdigit(line[end_pos]) || line[end_pos] == '.' || line[end_pos] == '-'))
            {
                end_str += line[end_pos];
                ++end_pos;
            }
            double end;
            try {
                end = start_time + std::stod(end_str);
            } catch (...) {
                continue;
            }
            if(end - current_start >= 0.005)
            {
                periods.push_back({current_start, end});
            }
            current_start = -1.0;
        }
    }
    return periods;
}
std::vector<Period> parseBlack(const std::string& output, double start_time)
{
    std::vector<Period> periods;
    std::istringstream iss(output);
    std::string line;
    while(std::getline(iss, line))
    {
        size_t pos_black_start = line.find("black_start:");
        size_t pos_black_end = line.find(" black_end:");
        if(pos_black_start != std::string::npos && pos_black_end != std::string::npos)
        {
            double start, end;
            try {
                start = start_time + std::stod(line.substr(pos_black_start + 12, pos_black_end - (pos_black_start + 12)));
                end = start_time + std::stod(line.substr(pos_black_end + 11));
            } catch (...) {
                continue;
            }
            if(end - start >= 0.005)
            {
                periods.push_back({start, end});
            }
        }
    }
    return periods;
}
std::vector<FrameData> parseFrameData(const std::string& output, double start_time)
{
    std::vector<FrameData> frame_data;
    std::istringstream iss(output);
    std::string line;
    double current_scene_score = 0.0;
    FrameData current_frame;
    while(std::getline(iss, line))
    {
        size_t pos_metadata = line.find("lavfi.scene_score=");
        if(pos_metadata != std::string::npos)
        {
            try {
                current_scene_score = std::stod(line.substr(pos_metadata + 18));
                current_frame.scene_score = current_scene_score;
                current_frame.is_scene_change = (current_scene_score > 0.3);
            } catch (...) {
                current_scene_score = 0.0;
            }
        }
        size_t pos_blackframe = line.find("[blackframe @");
        if(pos_blackframe != std::string::npos)
        {
            size_t pos_pblack = line.find(" pblack:");
            if(pos_pblack != std::string::npos)
            {
                try {
                    current_frame.black_percentage = std::stoi(line.substr(pos_pblack + 8));
                } catch (...) {
                    continue;
                }
            }
        }
        size_t pos_showinfo = line.find("[showinfo @");
        if(pos_showinfo != std::string::npos)
        {
            size_t pos_pts_time = line.find("pts_time:");
            if(pos_pts_time != std::string::npos)
            {
                size_t time_pos = pos_pts_time + 9;
                std::string time_str;
                while(time_pos < line.size() && (std::isdigit(line[time_pos]) || line[time_pos] == '.' || line[time_pos] == '-'))
                {
                    time_str += line[time_pos];
                    ++time_pos;
                }
                try {
                    double relative_time = std::stod(time_str);
                    current_frame.timestamp = start_time + relative_time;
                    frame_data.push_back(current_frame);
                    current_frame = FrameData();
                    current_scene_score = 0.0;
                } catch (...) {
                    continue;
                }
            }
        }
        size_t pos_astats = line.find("[astats @");
        if(pos_astats != std::string::npos)
        {
            size_t pos_rms = line.find("RMS level dB:");
            size_t pos_pts_time = line.find("pts_time:");
            if(pos_rms != std::string::npos && pos_pts_time != std::string::npos)
            {
                size_t time_pos = pos_pts_time + 9;
                std::string time_str;
                while(time_pos < line.size() && (std::isdigit(line[time_pos]) || line[time_pos] == '.' || line[time_pos] == '-'))
                {
                    time_str += line[time_pos];
                    ++time_pos;
                }
                try {
                    double relative_time = std::stod(time_str);
                    double absolute_time = start_time + relative_time;
                    double rms = std::stod(line.substr(pos_rms + 13));
                    for(auto& frame : frame_data)
                    {
                        if(std::abs(frame.timestamp - absolute_time) < 0.02)
                        {
                            frame.rms_level = rms;
                            break;
                        }
                    }
                } catch (...) {
                    continue;
                }
            }
        }
    }
    std::sort(frame_data.begin(), frame_data.end(), [](const FrameData& a, const FrameData& b) {
        return a.timestamp < b.timestamp;
    });
    return frame_data;
}
bool overlaps(const Period& a, const Period& b, double& overlap_start, double& overlap_end)
{
    overlap_start = std::max(a.start, b.start);
    overlap_end = std::min(a.end, b.end);
    return overlap_start < overlap_end;
}
std::vector<Period> findOverlaps(const std::vector<Period>& silences, const std::vector<FrameData>& frame_data, const std::vector<Period>& blacks, double min_duration = 0.1)
{
    std::vector<Period> periods;
    for (const auto& s : silences)
    {
        for (const auto& b : blacks)
        {
            double overlap_start, overlap_end;
            if (overlaps(s, b, overlap_start, overlap_end) && (overlap_end - overlap_start >= min_duration))
            {
                periods.push_back({overlap_start, overlap_end});
            }
        }
    }
    if (periods.empty()) return periods;
    std::sort(periods.begin(), periods.end(), [](const Period& a, const Period& b) {
        return a.start < b.start;
    });
    std::vector<Period> merged;
    if (!periods.empty())
    {
        Period current = periods[0];
        double gap = 1.0;
        for (size_t i = 1; i < periods.size(); ++i)
        {
            if (current.end + gap >= periods[i].start)
            {
                current.end = std::max(current.end, periods[i].end);
            }
            else
            {
                merged.push_back(current);
                current = periods[i];
            }
        }
        merged.push_back(current);
    }
    return merged;
}
int main(int argc, char* argv[])
{
    if(argc < 2)
    {
        std::cerr << "Usage: " << argv[0] << " <video_file> [--no-format] [--hide-decimal] [--hide-mmss] [--hide-start] [--hide-midpoint] [--hide-end]" << std::endl;
        return 1;
    }
    std::string video = argv[1];
    const double start_time = 0.0;
    bool show_decimal = true;
    bool no_format = false;
    bool show_mmss = true;
    bool show_start = true;
    bool show_midpoint = true;
    bool show_end = true;
    for(int i = 2; i < argc; ++i)
    {
        std::string arg = argv[i];
        if(arg == "--hide-decimal") show_decimal = false;
        else if(arg == "--hide-mmss") show_mmss = false;
        else if(arg == "--hide-start") show_start = false;
        else if(arg == "--hide-midpoint") show_midpoint = false;
        else if(arg == "--hide-end") show_end = false;
        else if(arg == "--no-format") no_format = true;
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
    if(!no_format) std::cout << "Video duration: " << std::fixed << std::setprecision(3) << video_duration << std::endl;
    std::string silence_cmd = "ffmpeg -i \"" + video + "\" -af silencedetect=noise=-45dB:d=0.1 -f null - 2>&1";
    std::string silence_output = exec(silence_cmd);
    std::vector<Period> silences = parseSilence(silence_output, start_time);
    std::string black_cmd = "ffmpeg -i \"" + video + "\" -vf blackdetect=d=0.1:pic_th=0.98:pix_th=0.12 -f null - 2>&1";
    std::string black_output = exec(black_cmd);
    std::vector<Period> blacks = parseBlack(black_output, start_time);
    std::string frame_cmd = "ffmpeg -i \"" + video + "\" -vf \"setpts=PTS-STARTPTS,select='gt(scene\\,-1)',metadata=print,blackframe=amount=0:threshold=60,showinfo\" -af astats=metadata=1:reset=1 -f null - 2>&1";
    std::string frame_output = exec(frame_cmd);
    std::vector<FrameData> frame_data = parseFrameData(frame_output, start_time);
    std::vector<Period> ad_points = findOverlaps(silences, frame_data, blacks, 0.1);
    std::vector<Period> filtered_points;
    for(const auto& p : ad_points)
    {
        double adjusted_start = std::max(p.start, 0.0);
        double adjusted_end = std::min(p.end, video_duration);
        if(adjusted_start > 1.0 && adjusted_end < (video_duration - 1.0))
        {
            filtered_points.push_back({adjusted_start, adjusted_end});
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
            double midpoint = p.start + ((p.end - p.start) / 2.0);
            if(no_format)
            {
                if(show_decimal && (show_start || show_midpoint || show_end))
                {
                    if(show_start)
                    {
                        if(output_written) std::cout << ' ';
                        std::cout << std::fixed << std::setprecision(3) << p.start;
                        output_written = true;
                    }
                    if(show_midpoint)
                    {
                        if(output_written) std::cout << ' ';
                        std::cout << std::fixed << std::setprecision(3) << midpoint;
                        output_written = true;
                    }
                    if(show_end)
                    {
                        if(output_written) std::cout << ' ';
                        std::cout << std::fixed << std::setprecision(3) << p.end;
                        output_written = true;
                    }
                }
            }
            else
            {
                std::cout << "Potential ad insertion period:" << std::endl;
                if(show_decimal && (show_start || show_midpoint || show_end))
                {
                    if(show_start || show_midpoint || show_end) std::cout << "\tDecimal seconds:" << std::endl;
                    if(show_start) std::cout << "\t\tStart: " << std::fixed << std::setprecision(3) << p.start << std::endl;
                    if(show_midpoint) std::cout << "\t\tMidpoint: " << std::fixed << std::setprecision(3) << midpoint << std::endl;
                    if(show_end) std::cout << "\t\tEnd: " << std::fixed << std::setprecision(3) << p.end << std::endl;
                    if(show_start || show_midpoint || show_end) std::cout << std::endl;
                }
                if(show_mmss && (show_start || show_midpoint || show_end))
                {
                    if(show_start || show_midpoint || show_end) std::cout << "\tMM:SS.d" << std::endl;
                    if(show_start) std::cout << "\t\tStart: " << secondsToMMSS(p.start) << std::endl;
                    if(show_midpoint) std::cout << "\t\tMidpoint: " << secondsToMMSS(midpoint) << std::endl;
                    if(show_end) std::cout << "\t\tEnd: " << secondsToMMSS(p.end) << std::endl;
                    if(show_start || show_midpoint || show_end) std::cout << std::endl;
                }
            }
        }
    }
    return 0;
}