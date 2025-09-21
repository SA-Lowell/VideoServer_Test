#include <iostream>
#include <string>
#include <vector>
#include <sstream>
#include <iomanip>
#include <cstdlib>
#include <cstdio> // for popen, pclose
#include <algorithm>

struct Period {
    double start;
    double end;
};

std::string exec(const std::string& cmd) {
    std::string result = "";
    char buffer[128];
    FILE* pipe = popen(cmd.c_str(), "r");
    if (!pipe) return "ERROR";
    while (!feof(pipe)) {
        if (fgets(buffer, 128, pipe) != NULL)
            result += buffer;
    }
    pclose(pipe);
    return result;
}

std::string secondsToMMSS(double seconds) {
    int min = static_cast<int>(seconds / 60);
    double sec = seconds - min * 60;
    std::ostringstream oss;
    oss << std::setfill('0') << std::setw(2) << min << ":" << std::fixed << std::setprecision(3) << std::setw(6) << sec;
    return oss.str();
}

std::vector<Period> parseSilence(const std::string& output) {
    std::vector<Period> periods;
    std::istringstream iss(output);
    std::string line;
    double current_start = -1.0;

    while (std::getline(iss, line)) {
        size_t pos_start = line.find("[silencedetect @");
        if (pos_start == std::string::npos) continue;

        size_t pos_silence_start = line.find("silence_start: ");
        size_t pos_silence_end = line.find("silence_end: ");

        if (pos_silence_start != std::string::npos) {
            current_start = std::stod(line.substr(pos_silence_start + 15));
        } else if (pos_silence_end != std::string::npos && current_start >= 0.0) {
            size_t end_pos = pos_silence_end + 13;
            size_t space_pos = line.find(' ', end_pos);
            double end = std::stod(line.substr(end_pos, space_pos - end_pos));
            if (end - current_start >= 1.0) { // Filter for at least 1 second silence
                periods.push_back({current_start, end});
            }
            current_start = -1.0;
        }
    }
    return periods;
}

std::vector<Period> parseBlack(const std::string& output) {
    std::vector<Period> periods;
    std::istringstream iss(output);
    std::string line;

    while (std::getline(iss, line)) {
        size_t pos_black_start = line.find("black_start:");
        size_t pos_black_end = line.find(" black_end:");

        if (pos_black_start != std::string::npos && pos_black_end != std::string::npos) {
            double start = std::stod(line.substr(pos_black_start + 12, pos_black_end - (pos_black_start + 12)));
            double end = std::stod(line.substr(pos_black_end + 11));
            if (end - start >= 0.5) { // Filter for at least 0.5 second black
                periods.push_back({start, end});
            }
        }
    }
    return periods;
}

bool overlaps(const Period& a, const Period& b, double& overlap_start, double& overlap_end) {
    overlap_start = std::max(a.start, b.start);
    overlap_end = std::min(a.end, b.end);
    return overlap_start < overlap_end;
}

std::vector<Period> findOverlaps(const std::vector<Period>& silences, const std::vector<Period>& blacks, double min_duration = 1.0) {
    std::vector<Period> overlap_periods;
    for (const auto& s : silences) {
        for (const auto& b : blacks) {
            double o_start, o_end;
            if (overlaps(s, b, o_start, o_end) && (o_end - o_start >= min_duration)) {
                overlap_periods.push_back({o_start, o_end});
            }
        }
    }
    // Sort and merge overlapping overlaps if necessary
    if (overlap_periods.empty()) return overlap_periods;
    std::sort(overlap_periods.begin(), overlap_periods.end(), [](const Period& a, const Period& b) { return a.start < b.start; });
    std::vector<Period> merged;
    Period current = overlap_periods[0];
    for (size_t i = 1; i < overlap_periods.size(); ++i) {
        if (current.end >= overlap_periods[i].start) {
            current.end = std::max(current.end, overlap_periods[i].end);
        } else {
            merged.push_back(current);
            current = overlap_periods[i];
        }
    }
    merged.push_back(current);
    return merged;
}

int main(int argc, char* argv[]) {
    if (argc != 2) {
        std::cerr << "Usage: " << argv[0] << " <video_file>" << std::endl;
        return 1;
    }

    std::string video = argv[1];

    // Detect silence: noise -30dB, min duration 1s
    std::string silence_cmd = "ffmpeg -i \"" + video + "\" -af silencedetect=noise=-30dB:d=1 -f null - 2>&1";
    std::string silence_output = exec(silence_cmd);
    std::vector<Period> silences = parseSilence(silence_output);

    // Detect black frames: duration 0.5s, pixel black threshold 0.1 (10%)
    std::string black_cmd = "ffmpeg -i \"" + video + "\" -vf blackdetect=d=0.5:pix_th=0.1 -f null - 2>&1";
    std::string black_output = exec(black_cmd);
    std::vector<Period> blacks = parseBlack(black_output);

    // Find overlaps of at least 1 second
    std::vector<Period> ad_points = findOverlaps(silences, blacks, 1.0);

    if (ad_points.empty()) {
        std::cout << "No suitable ad insertion points detected." << std::endl;
    } else {
        for (const auto& p : ad_points) {
			double midpoint = p.start + ((p.end - p.start) / 2.0);

            std::cout << "Potential ad insertion period:" << std::endl;
            std::cout << "  Decimal seconds - Start: " << std::fixed << std::setprecision(21) << p.start << ", End: " << p.end << std::endl;
            std::cout << "  Decimal seconds - Midpoint: " << std::fixed << std::setprecision(21) << midpoint << std::endl << std::endl;
            std::cout << "  MM:SS.d         - Start: " << secondsToMMSS(p.start) << ", End: " << secondsToMMSS(p.end) << std::endl;
            std::cout << "  MM:SS.d         - Midpoint: " << secondsToMMSS(midpoint) << std::endl << std::endl;
        }
    }

    return 0;
}