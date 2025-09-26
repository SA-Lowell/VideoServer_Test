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

            if(end - current_start >= 0.01)
            {
                periods.push_back({current_start, end});
            }

            current_start = -1.0;
        }
    }

    return periods;
}

std::vector<Period> parseBlack(const std::string& output)
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
            double start = std::stod(line.substr(pos_black_start + 12, pos_black_end - (pos_black_start + 12)));
            double end = std::stod(line.substr(pos_black_end + 11));

            if(end - start >= 0.01)
            {
                periods.push_back({start, end});
            }
        }
    }

    return periods;
}

bool overlaps(const Period& a, const Period& b, double& overlap_start, double& overlap_end)
{
    overlap_start = std::max(a.start, b.start);
    overlap_end = std::min(a.end, b.end);

    return overlap_start < overlap_end;
}

std::vector<Period> findOverlaps(const std::vector<Period>& silences, const std::vector<Period>& blacks, double min_duration = 0.1)
{
    std::vector<Period> overlap_periods;

    for (const auto& s : silences)
    {
        for (const auto& b : blacks)
        {
            double o_start, o_end;

            if (overlaps(s, b, o_start, o_end) && (o_end - o_start >= min_duration))
            {
                overlap_periods.push_back({o_start, o_end});
            }
        }
    }

    if(overlap_periods.empty())return overlap_periods;

    std::sort
    (
        overlap_periods.begin(),
        overlap_periods.end(),
        [](const Period& a, const Period& b)
        {
            return a.start < b.start;
        }
    );

    std::vector<Period> merged;
    Period current = overlap_periods[0];

    for(size_t i = 1; i < overlap_periods.size(); ++i)
    {
        if(current.end >= overlap_periods[i].start)
        {
            current.end = std::max(current.end, overlap_periods[i].end);
        }
        else
        {
            merged.push_back(current);
            current = overlap_periods[i];
        }
    }

    merged.push_back(current);

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

    bool show_decimal = true;
    bool no_format = false;
    bool show_mmss = true;
    bool show_start = true;
    bool show_midpoint = true;
    bool show_end = true;

    for(int i = 2; i < argc; ++i)
    {
        std::string arg = argv[i];

        if(arg == "--hide-decimal")show_decimal = false;
        else if(arg == "--hide-mmss")show_mmss = false;
        else if(arg == "--hide-start")show_start = false;
        else if(arg == "--hide-midpoint")show_midpoint = false;
        else if(arg == "--hide-end")show_end = false;
        else if(arg == "--no-format")no_format = true;
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

    std::string silence_cmd = "ffmpeg -i \"" + video + "\" -af silencedetect=noise=-35dB:d=0.03 -f null - 2>&1";
    std::string silence_output = exec(silence_cmd);
    std::vector<Period> silences = parseSilence(silence_output);

    std::string black_cmd = "ffmpeg -i \"" + video + "\" -vf blackdetect=d=0.1:pic_th=0.95:pix_th=0.10 -f null - 2>&1";
    std::string black_output = exec(black_cmd);
    std::vector<Period> blacks = parseBlack(black_output);

    std::vector<Period> ad_points = findOverlaps(silences, blacks, 0.03);

    std::vector<Period> filtered_points;
    const double epsilon = 1.0;

    for(const auto& p : ad_points)
    {
        double adjusted_start = std::max(p.start, 0.0);
        double adjusted_end = std::min(p.end, video_duration);

        if(adjusted_start > epsilon && adjusted_end < (video_duration - epsilon))
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
            double midpoint = p.start + ((p.end - p.start) / 2.0);


        if(no_format)
        {
            if(show_decimal && (show_start || show_midpoint || show_end))
            {
                if(show_start)
                {
                if(output_written)std::cout << ' ';
                    std::cout << std::fixed << std::setprecision(21) << p.start;
                    output_written = true;
                }
                if(show_midpoint)
                {
                if(output_written)std::cout << ' ';
                    std::cout << std::fixed << std::setprecision(21) << midpoint;
                    output_written = true;
                }
                if(show_end)
                {
                if(output_written)std::cout << ' ';
                    std::cout << std::fixed << std::setprecision(21) << p.end;
                    output_written = true;
                }
            }
        }
        else
        {
            std::cout << "Potential ad insertion period:" << std::endl;

            if(show_decimal && (show_start || show_midpoint || show_end))
            {
                if(show_start || show_midpoint || show_end)std::cout << "\tDecimal seconds:" << std::endl;

                if(show_start)std::cout << "\t\tStart:" << std::fixed << std::setprecision(21) << p.start << std::endl;
                if(show_midpoint)std::cout << "\t\tMidpoint:" << std::fixed << std::setprecision(21) << midpoint << std::endl;
                if(show_end)std::cout << "\t\tEnd:" << std::fixed << std::setprecision(21) << p.end << std::endl;

                if(show_start || show_midpoint || show_end)std::cout << std::endl;
            }

            if(show_mmss && (show_start || show_midpoint || show_end))
            {
                if(show_start || show_midpoint || show_end)std::cout << "\tMM:SS.d" << std::endl;

                if(show_start)std::cout << "\t\tStart:" << std::fixed << secondsToMMSS(p.start) << std::endl;
                if(show_midpoint)std::cout << "\t\tMidpoint:" << std::fixed << secondsToMMSS(midpoint) << std::endl;
                if(show_end)std::cout << "\t\tEnd:" << std::fixed << secondsToMMSS(p.end) << std::endl;

                if(show_start || show_midpoint || show_end)std::cout << std::endl;
            }
        }
        }
    }

    return 0;
}