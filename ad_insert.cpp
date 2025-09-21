#include <iostream>
#include <fstream>
#include <sstream>
#include <string>
#include <vector>
#include <map>
#include <set>
#include <algorithm>
#include <cstdlib> // for std::system
#include <filesystem> // C++17: for paths
#include <cctype> // for isspace, isdigit

namespace fs = std::filesystem;

// Break represents a commercial flag (in seconds for simplicity)
struct Break {
 double startSec;
 std::vector<std::string> ads; // List of ad filenames, e.g., {"ad.mp4"}
};

// Simple JSON structures for ffprobe (manual parsing below)
struct ProbeStream {
 std::string codecType;
 std::string codecName;
 int width = 0;
 int height = 0;
 std::string sampleAspectRatio = "1:1";
 std::string sampleRate;
 std::string bitRate;
 int channels = 0;
 std::string rFrameRate = "30/1"; // e.g., "30000/1001"
};

struct ProbeFormat {
 std::string bitRate; // Overall file bitrate (bps string)
 std::string duration; // In seconds (float string)
};

struct ProbeOutput {
 ProbeFormat format;
 std::vector<ProbeStream> streams;
};

// Helper to trim leading/trailing whitespace
std::string trim(const std::string& str) {
 size_t first = str.find_first_not_of(" \t\n\r\f\v");
 if (first == std::string::npos) return "";
 size_t last = str.find_last_not_of(" \t\n\r\f\v");
 return str.substr(first, (last - first + 1));
}

// Helper for quoted values like "key": "value"
std::string getQuotedValue(const std::string& line, size_t keyStart) {
 size_t colon = line.find(':', keyStart);
 if (colon == std::string::npos) return "";
 size_t quoteStart = line.find('"', colon + 1);
 if (quoteStart == std::string::npos) return "";
 size_t quoteEnd = line.find('"', quoteStart + 1);
 if (quoteEnd == std::string::npos) return "";
 return line.substr(quoteStart + 1, quoteEnd - quoteStart - 1);
}

// Improved helper for unquoted int values like "key": 1920,
int getIntValue(const std::string& line, size_t keyStart) {
 size_t colon = line.find(':', keyStart);
 if (colon == std::string::npos) return 0;
 size_t numStart = colon + 1;
 // Skip spaces
 while (numStart < line.length() && std::isspace(static_cast<unsigned char>(line[numStart]))) ++numStart;
 // Handle negative
 bool negative = false;
 if (numStart < line.length() && line[numStart] == '-') {
 negative = true;
 ++numStart;
 }
 // Skip spaces after -
 while (numStart < line.length() && std::isspace(static_cast<unsigned char>(line[numStart]))) ++numStart;
 size_t numEnd = numStart;
 while (numEnd < line.length() && std::isdigit(static_cast<unsigned char>(line[numEnd]))) ++numEnd;
 if (numEnd == numStart) return 0;
 std::string numStr = line.substr(numStart, numEnd - numStart);
 try {
 int val = std::stoi(numStr);
 return negative ? -val : val;
 } catch (...) {
 return 0;
 }
}

// parseBitrate extracts numeric bitrate in bps (handles "128k" or "95994")
int parseBitrate(const std::string& s) {
 std::string trimmed = trim(s);
 if (trimmed.empty()) return 0;
 if (trimmed.back() == 'k') {
 trimmed.pop_back();
 trimmed = trim(trimmed);
 try {
 int i = std::stoi(trimmed);
 return i * 1000;
 } catch (...) {
 return 0;
 }
 }
 try {
 return std::stoi(trimmed);
 } catch (...) {
 return 0;
 }
}

// parseFPS parses r_frame_rate (e.g., "30000/1001" -> 29.97)
double parseFPS(const std::string& s) {
 std::string trimmed = trim(s);
 if (trimmed.empty()) return 30.0;
 size_t slash = trimmed.find('/');
 if (slash != std::string::npos) {
 std::string numStr = trimmed.substr(0, slash);
 std::string denStr = trimmed.substr(slash + 1);
 try {
 double num = std::stod(numStr);
 double den = std::stod(denStr);
 if (den != 0.0) return num / den;
 } catch (...) {}
 }
 try {
 return std::stod(trimmed);
 } catch (...) {}
 return 30.0; // Default fallback
}

// buildAudioFilter constructs -af string for resampling/channels
std::string buildAudioFilter(int srcSR, int srcCh, int tgtSR, int tgtCh) {
 std::vector<std::string> filters;
 if (srcSR != tgtSR) {
 filters.push_back("aresample=" + std::to_string(tgtSR));
 }
 if(srcCh != tgtCh) {
 std::string layout = "mono";
 if (tgtCh == 2) layout = "stereo";
 else if (tgtCh > 2) layout = "stereo"; // Force downmix to stereo
 filters.push_back("aformat=channel_layouts=" + layout);
 }
 if (filters.empty()) return "";
 std::string result;
 for (size_t i = 0; i < filters.size(); ++i) {
 if (i > 0) result += ",";
 result += filters[i];
 }
 return result;
}

// Simple manual JSON parser for ffprobe output (searches for keys in {"key": "value"} format)
ProbeOutput parseProbeJson(const std::string& jsonStr) {
 std::cout << "DEBUG: parseProbeJson input length: " << jsonStr.length() << std::endl;
 std::cout << "DEBUG: parseProbeJson first 500 chars: " << jsonStr.substr(0, std::min<size_t>(500, jsonStr.length())) << "..." << std::endl;
 ProbeOutput probe;
 std::istringstream iss(jsonStr);
 std::string line;
 std::string currentSection = ""; // "format" or "streams"
 ProbeStream* currentStream = nullptr;
 size_t keyPos;
 int lineNum = 0;

 while (std::getline(iss, line)) {
 ++lineNum;
 std::string trimmedLine = trim(line);
 std::cout << "DEBUG: Line " << lineNum << ": " << line << std::endl;

 // Skip empty lines, braces, and commas
 if (trimmedLine.empty() || trimmedLine == "{" || trimmedLine == "}" || trimmedLine == ",") continue;

 // Detect format section
 if (line.find("\"format\":") != std::string::npos) {
 currentSection = "format";
 currentStream = nullptr;
 std::cout << "DEBUG: Entering format section" << std::endl;
 continue;
 }

 // Detect streams section
 if (line.find("\"streams\":") != std::string::npos) {
 currentSection = "streams";
 currentStream = nullptr;
 std::cout << "DEBUG: Entering streams section" << std::endl;
 continue;
 }

 if (currentSection == "format") {
 // Parse format keys
 if ((keyPos = line.find("\"bit_rate\":")) != std::string::npos) {
 probe.format.bitRate = getQuotedValue(line, keyPos);
 std::cout << "DEBUG: Parsed format bit_rate: " << probe.format.bitRate << std::endl;
 } else if ((keyPos = line.find("\"duration\":")) != std::string::npos) {
 probe.format.duration = getQuotedValue(line, keyPos);
 std::cout << "DEBUG: Parsed format duration: " << probe.format.duration << std::endl;
 }
 } else if (currentSection == "streams") {
 // Start new stream on "index"
 if ((keyPos = line.find("\"index\":")) != std::string::npos) {
 probe.streams.emplace_back();
 currentStream = &probe.streams.back();
 std::cout << "DEBUG: Starting new stream, total streams now: " << probe.streams.size() << std::endl;
 }
 }

 if (currentStream != nullptr) {
 if ((keyPos = line.find("\"codec_name\":")) != std::string::npos) {
 currentStream->codecName = getQuotedValue(line, keyPos);
 std::cout << "DEBUG: Stream codec_name: " << currentStream->codecName << std::endl;
 } else if ((keyPos = line.find("\"codec_type\":")) != std::string::npos) {
 currentStream->codecType = getQuotedValue(line, keyPos);
 std::cout << "DEBUG: Stream codec_type: " << currentStream->codecType << std::endl;
 } else if ((keyPos = line.find("\"width\":")) != std::string::npos) {
 currentStream->width = getIntValue(line, keyPos);
 std::cout << "DEBUG: Stream width: " << currentStream->width << std::endl;
 } else if ((keyPos = line.find("\"height\":")) != std::string::npos) {
 currentStream->height = getIntValue(line, keyPos);
 std::cout << "DEBUG: Stream height: " << currentStream->height << std::endl;
 } else if ((keyPos = line.find("\"r_frame_rate\":")) != std::string::npos) {
 currentStream->rFrameRate = getQuotedValue(line, keyPos);
 std::cout << "DEBUG: Stream r_frame_rate: " << currentStream->rFrameRate << std::endl;
 } else if ((keyPos = line.find("\"sample_aspect_ratio\":")) != std::string::npos) {
 currentStream->sampleAspectRatio = getQuotedValue(line, keyPos);
 std::cout << "DEBUG: Stream sample_aspect_ratio: " << currentStream->sampleAspectRatio << std::endl;
 } else if ((keyPos = line.find("\"sample_rate\":")) != std::string::npos) {
 currentStream->sampleRate = getQuotedValue(line, keyPos);
 std::cout << "DEBUG: Stream sample_rate: " << currentStream->sampleRate << std::endl;
 } else if ((keyPos = line.find("\"channels\":")) != std::string::npos) {
 currentStream->channels = getIntValue(line, keyPos);
 std::cout << "DEBUG: Stream channels: " << currentStream->channels << std::endl;
 } else if ((keyPos = line.find("\"bit_rate\":")) != std::string::npos) {
 currentStream->bitRate = getQuotedValue(line, keyPos);
 std::cout << "DEBUG: Stream bit_rate: " << currentStream->bitRate << std::endl;
 }
 }
 }
 std::cout << "DEBUG: parseProbeJson complete. Format: bitrate=" << probe.format.bitRate << ", duration=" << probe.format.duration << ". Streams count: " << probe.streams.size() << std::endl;
 for (size_t i = 0; i < probe.streams.size(); ++i) {
 const auto& s = probe.streams[i];
 std::cout << "DEBUG: Stream " << i << ": type=" << s.codecType << ", name=" << s.codecName << ", w/h=" << s.width << "/" << s.height << ", ch=" << s.channels << ", sr=" << s.sampleRate << ", br=" << s.bitRate << std::endl;
 }
 return probe;
}

// probeVideo gets video/audio properties using ffprobe with bitrate estimation
bool probeVideo(const std::string& filePath, int& width, int& height, int& sarNum, int& sarDen, int& sampleRate, int& channels, std::string& codecName, std::string& vBitRate, std::string& aBitRate, double& fps, double& duration) {
 std::cout << "DEBUG: probeVideo called for file: " << filePath << std::endl;
 if (!fs::exists(filePath)) {
 std::cout << "DEBUG: File does not exist: " << filePath << std::endl;
 return false;
 }
 std::cout << "DEBUG: File exists, size: " << fs::file_size(filePath) << " bytes" << std::endl;

 std::string cmd = "ffprobe -v quiet -print_format json -show_format -show_streams \"" + filePath + "\" 2>nul";
 std::cout << "DEBUG: Running ffprobe command: " << cmd << std::endl;
 FILE* pipe = _popen(cmd.c_str(), "r"); // Windows popen
 if (!pipe) {
 std::cout << "DEBUG: Failed to open pipe for ffprobe" << std::endl;
 return false;
 }

 std::string output;
 char buffer[1024];
 while (fgets(buffer, sizeof buffer, pipe) != NULL) {
 output += buffer;
 }
 int exitCode = _pclose(pipe);
 std::cout << "DEBUG: ffprobe exit code: " << exitCode << std::endl;
 std::cout << "DEBUG: ffprobe raw output length: " << output.length() << std::endl;
 std::cout << "DEBUG: ffprobe raw output:\n" << output << std::endl;

 if (output.empty()) {
 std::cout << "DEBUG: ffprobe output is empty" << std::endl;
 return false;
 }

 ProbeOutput probe = parseProbeJson(output); // Use manual parser

 // Parse duration (fallback to 0)
 try {
 duration = std::stod(probe.format.duration);
 std::cout << "DEBUG: Parsed duration: " << duration << "s" << std::endl;
 } catch (...) {
 std::cout << "DEBUG: Failed to parse duration from '" << probe.format.duration << "'" << std::endl;
 duration = 0.0;
 }
 if (duration == 0.0) {
 std::cout << "DEBUG: Duration is 0, probe failed" << std::endl;
 return false;
 }

 // File size for calc fallback
 uintmax_t info;
 try {
 info = fs::file_size(filePath);
 std::cout << "DEBUG: File size: " << info << " bytes" << std::endl;
 } catch (...) {
 std::cout << "DEBUG: Failed to get file size" << std::endl;
 return false;
 }
 if (info == static_cast<uintmax_t>(-1)) {
 std::cout << "DEBUG: Invalid file size" << std::endl;
 return false;
 }
 long long fileSizeBytes = static_cast<long long>(info);
 long long overallBpsFromSize = (fileSizeBytes * 8) / static_cast<long long>(duration);
 std::cout << "DEBUG: Calculated overall bps from size/duration: " << overallBpsFromSize << std::endl;

 // Overall bitrate from format (fallback to size calc if empty)
 int overallBps = 0;
 if (!probe.format.bitRate.empty()) {
 overallBps = parseBitrate(probe.format.bitRate);
 std::cout << "DEBUG: Using format bitrate: " << overallBps << " bps (parsed from '" << probe.format.bitRate << "')" << std::endl;
 } else {
 overallBps = static_cast<int>(overallBpsFromSize);
 std::cout << "DEBUG: Using calculated overall bitrate: " << overallBps << " bps" << std::endl;
 }

 // Stream parsing: find first video (width > 0) and first audio (channels > 0 or sampleRate)
 bool hasVideo = false;
 bool hasAudio = false;
 for (size_t i = 0; i < probe.streams.size(); ++i) {
 const auto& stream = probe.streams[i];
 std::cout << "DEBUG: Processing stream " << i << ": type=" << stream.codecType << ", width=" << stream.width << ", height=" << stream.height << ", channels=" << stream.channels << ", sampleRate='" << stream.sampleRate << "'" << std::endl;
 if (!hasVideo && stream.width > 0) {
 std::cout << "DEBUG: Found video stream" << std::endl;
 hasVideo = true;
 width = stream.width;
 height = stream.height;
 codecName = stream.codecName.empty() ? "h264" : stream.codecName;
 std::string rfr = stream.rFrameRate.empty() ? "30/1" : stream.rFrameRate;
 fps = parseFPS(rfr);
 std::cout << "DEBUG: Video FPS parsed: " << fps << " from '" << rfr << "'" << std::endl;
 if (!stream.bitRate.empty()) {
 vBitRate = stream.bitRate;
 std::cout << "DEBUG: Direct video bitrate: " << vBitRate << std::endl;
 } else {
 int estVBps = overallBps - 128000;
 if (estVBps < 500000) estVBps = 2000000;
 vBitRate = std::to_string(estVBps);
 std::cout << "DEBUG: Estimated video bitrate: " << vBitRate << " bps (overall - est audio)" << std::endl;
 }
 // SAR parse
 std::string sar = stream.sampleAspectRatio.empty() ? "1:1" : stream.sampleAspectRatio;
 size_t colon = sar.find(':');
 if (colon != std::string::npos) {
 try {
 sarNum = std::stoi(sar.substr(0, colon));
 sarDen = std::stoi(sar.substr(colon + 1));
 std::cout << "DEBUG: Parsed SAR: " << sarNum << ":" << sarDen << std::endl;
 } catch (...) {
 std::cout << "DEBUG: Failed to parse SAR '" << sar << "', defaulting to 1:1" << std::endl;
 sarNum = 1;
 sarDen = 1;
 }
 if (sarNum <= 0 || sarDen <= 0) {
 std::cout << "DEBUG: Invalid SAR values, defaulting to 1:1" << std::endl;
 sarNum = 1;
 sarDen = 1;
 }
 } else {
 std::cout << "DEBUG: No colon in SAR '" << sar << "', defaulting to 1:1" << std::endl;
 sarNum = 1;
 sarDen = 1;
 }
 } else if (!hasAudio && (stream.channels > 0 || !stream.sampleRate.empty())) {
 std::cout << "DEBUG: Found audio stream" << std::endl;
 hasAudio = true;
 if (!stream.sampleRate.empty()) {
 try {
 sampleRate = std::stoi(stream.sampleRate);
 std::cout << "DEBUG: Parsed sample rate: " << sampleRate << std::endl;
 } catch (...) {
 std::cout << "DEBUG: Failed to parse sample rate '" << stream.sampleRate << "', defaulting to 48000" << std::endl;
 sampleRate = 48000;
 }
 } else {
 std::cout << "DEBUG: No sample rate, defaulting to 48000" << std::endl;
 sampleRate = 48000;
 }
 if (!stream.bitRate.empty()) {
 aBitRate = stream.bitRate;
 std::cout << "DEBUG: Direct audio bitrate: " << aBitRate << std::endl;
 } else {
 aBitRate = "128000";
 std::cout << "DEBUG: Defaulted audio bitrate: " << aBitRate << " (no metadata)" << std::endl;
 }
 channels = stream.channels;
 std::cout << "DEBUG: Audio channels: " << channels << std::endl;
 }
 }
 std::cout << "DEBUG: Final hasVideo: " << (hasVideo ? "true" : "false") << ", hasAudio: " << (hasAudio ? "true" : "false") << std::endl;

 // Final checks/defaults
 if (vBitRate.empty()) {
 vBitRate = std::to_string(overallBps); // Total as video fallback
 std::cout << "DEBUG: vBitRate was empty, set to overall: " << vBitRate << std::endl;
 }
 if (aBitRate.empty()) {
 aBitRate = "128000";
 std::cout << "DEBUG: aBitRate was empty, defaulted to 128000" << std::endl;
 }
 if (!hasVideo || !hasAudio) {
 std::cout << "DEBUG: No valid video/audio stream found" << std::endl;
 return false;
 }

 std::cout << "DEBUG: probeVideo succeeded. Summary: w=" << width << " h=" << height << " sar=" << sarNum << ":" << sarDen << " sr=" << sampleRate << " ch=" << channels << " codec=" << codecName << " vbr=" << vBitRate << " abr=" << aBitRate << " fps=" << fps << " dur=" << duration << std::endl;
 return true;
}

// extractSegment extracts a segment from startSec for durSec (if durSec <=0, skips)
bool extractSegment(const std::string& inputPath, const std::string& outputPath, double startSec, double durSec, bool reencodeEpisode, int targetWidth, int targetHeight, int targetSampleRate, int targetChannels, const std::string& targetVBitRate, const std::string& targetABitRate, double targetFPS, const std::string& targetVCodec, const std::string& sarStr, const std::string& epAudioFilter) {
 std::cout << "DEBUG: extractSegment called: input=" << inputPath << " output=" << outputPath << " ss=" << startSec << " t=" << durSec << " reencode=" << reencodeEpisode << std::endl;
 if (durSec <= 0) {
 std::cout << "DEBUG: Skipping extract due to durSec <=0" << std::endl;
 return true; // Skip empty
 }

 std::string args = "-ss " + std::to_string(startSec) + " ";
 if (durSec > 0) args += "-t " + std::to_string(durSec) + " ";
 args += "-i \"" + inputPath + "\" ";

 if (reencodeEpisode) {
 std::string scaleFilter = "scale=" + std::to_string(targetWidth) + ":" + std::to_string(targetHeight) + ":force_original_aspect_ratio=decrease,pad=" + std::to_string(targetWidth) + ":" + std::to_string(targetHeight) + ":(ow-iw)/2:(oh-ih)/2,setsar=" + sarStr;
 args += "-vf \"" + scaleFilter + "\" ";
 if (!epAudioFilter.empty()) args += "-af \"" + epAudioFilter + "\" ";
 args += "-r " + std::to_string(targetFPS) + " -c:v " + targetVCodec + " -preset veryfast -crf 23 -b:v " + targetVBitRate + " -c:a aac -b:a " + targetABitRate + " ";
 args += "-profile:v baseline -level 3.0 ";
 } else {
 args += "-c copy -r " + std::to_string(targetFPS) + " "; // Set FPS metadata
 }
 args += "-avoid_negative_ts make_zero \"" + outputPath + "\"";

 std::string fullCmd = "ffmpeg " + args;
 std::cout << "DEBUG: Executing extract: " << fullCmd << std::endl; // Debug print
 int result = std::system(fullCmd.c_str());
 if (result != 0) {
 std::cout << "DEBUG: segment extract failed (ss=" << startSec << " t=" << durSec << "): " << result << std::endl;
 return false;
 }
 std::cout << "DEBUG: Extracted segment: " << outputPath << " (ss=" << startSec << "s t=" << durSec << "s)" << std::endl;
 return true;
}

// insertBreak creates a seamless concatenated video with multiple ad insertions (outputs full.mp4)
bool insertBreak(const std::string& episodePath, const std::string& outputDir, const std::vector<Break>& brks) {
 std::cout << "DEBUG: insertBreak called: episode=" << episodePath << " outputDir=" << outputDir << " numBreaks=" << brks.size() << std::endl;
 std::cout << "DEBUG: Episode path: " << episodePath << std::endl;
 std::cout << "DEBUG: Number of breaks: " << brks.size() << std::endl;
 for (size_t i = 0; i < brks.size(); ++i) {
 const auto& b = brks[i];
 std::cout << "DEBUG: Break " << i << ": start=" << b.startSec << "s, ads count=" << b.ads.size() << std::endl;
 for (const auto& ad : b.ads) {
 std::cout << "DEBUG: - ad: " << ad << std::endl;
 }
 }

 // If no breaks, just re-encode the whole episode to baseline profile
 if (brks.empty()) {
 std::cout << "DEBUG: No breaks, re-encoding whole episode to baseline profile to full.mp4" << std::endl;
 std::string fullFile = outputDir + "/full.mp4";
 std::string cmd = "ffmpeg -i \"" + episodePath + "\" -c:v libx264 -profile:v baseline -level 3.0 -preset veryfast -crf 23 -movflags +faststart \"" + fullFile + "\"";
 int result = std::system(cmd.c_str());
 if (result == 0) {
 std::cout << "Re-encoded episode to: " << fullFile << std::endl;
 }
 return result == 0;
 }

 // Probe episode
 int epWidth, epHeight, epSarNum, epSarDen, epSampleRate, epChannels;
 std::string epVCodec, epVBitRate, epABitRate;
 double epFPS, epDuration;
 std::cout << "DEBUG: Probing episode..." << std::endl;
 bool epProbeOk = probeVideo(episodePath, epWidth, epHeight, epSarNum, epSarDen, epSampleRate, epChannels, epVCodec, epVBitRate, epABitRate, epFPS, epDuration);
 if (!epProbeOk) {
 std::cout << "DEBUG: Episode probe failed" << std::endl;
 return false;
 }
 std::cout << "DEBUG: Episode props: " << epWidth << "x" << epHeight << " SAR " << epSarNum << ":" << epSarDen << " SR " << epSampleRate << "Hz ch " << epChannels << " vcodec " << epVCodec << " vbr " << epVBitRate << " abr " << epABitRate << " fps " << epFPS << " dur " << epDuration << "s" << std::endl;

 // Collect unique ads from all breaks
 std::set<std::string> uniqueAds;
 for (const auto& brk : brks) {
 for (const auto& ad : brk.ads) {
 uniqueAds.insert(ad);
 }
 }
 std::cout << "DEBUG: Unique ads count: " << uniqueAds.size() << std::endl;
 for (const auto& ad : uniqueAds) {
 std::cout << "DEBUG: Unique ad: " << ad << std::endl;
 }
 if (uniqueAds.empty()) {
 std::cout << "DEBUG: No unique ads, failing" << std::endl;
 return false;
 }

 // Probe all unique ads and determine max target specs
 struct AdInfo {
 int width, height, sampleRate, channels;
 std::string vBitRate, aBitRate;
 double fps, duration;
 };
 std::map<std::string, AdInfo> adInfos;
 int targetHeight = epHeight;
 int targetWidth = epWidth;
 int targetSampleRate = epSampleRate;
 int targetChannels = epChannels;
 std::string targetVBitRate = epVBitRate;
 std::string targetABitRate = epABitRate;
 double targetFPS = epFPS;
 std::cout << "DEBUG: Initial targets from episode: " << targetWidth << "x" << targetHeight << " SR " << targetSampleRate << "Hz ch " << targetChannels << " vbr " << targetVBitRate << " abr " << targetABitRate << " fps " << targetFPS << std::endl;
 for (const auto& adName : uniqueAds) {
 std::cout << "DEBUG: Probing ad: " << adName << std::endl;
 int adWidth, adHeight, dummySarNum, dummySarDen, adSampleRate, adChannels;
 std::string dummyVCodec, adVBitRate, adABitRate;
 double adFPS, adDuration;
 bool adProbeOk = probeVideo(adName, adWidth, adHeight, dummySarNum, dummySarDen, adSampleRate, adChannels, dummyVCodec, adVBitRate, adABitRate, adFPS, adDuration);
 if (!adProbeOk) {
 std::cout << "DEBUG: Ad probe failed for " << adName << std::endl;
 return false;
 }
 adInfos[adName] = {adWidth, adHeight, adSampleRate, adChannels, adVBitRate, adABitRate, adFPS, adDuration};
 std::cout << "DEBUG: Ad " << adName << " props: " << adWidth << "x" << adHeight << " SR " << adSampleRate << "Hz ch " << adChannels << " vbr " << adVBitRate << " abr " << adABitRate << " fps " << adFPS << " dur " << adDuration << "s" << std::endl;

 // Update targets to max (episode base, upgrade if ad higher)
 if (adHeight > targetHeight) {
 targetHeight = adHeight;
 targetWidth = adWidth;
 std::cout << "DEBUG: Using ad " << adName << "'s higher res: " << targetWidth << "x" << targetHeight << std::endl;
 }
 if (adSampleRate > targetSampleRate) {
 targetSampleRate = adSampleRate;
 std::cout << "DEBUG: Using ad " << adName << "'s higher SR: " << targetSampleRate << "Hz" << std::endl;
 }
 if (adChannels > targetChannels) {
 targetChannels = adChannels;
 std::cout << "DEBUG: Using ad " << adName << "'s higher channels: " << targetChannels << std::endl;
 }
 int adABps = parseBitrate(adABitRate);
 int tgtABps = parseBitrate(targetABitRate);
 if (adABps > tgtABps) {
 targetABitRate = adABitRate;
 std::cout << "DEBUG: Using ad " << adName << "'s higher abr: " << targetABitRate << " (" << adABps << " bps)" << std::endl;
 }
 int adVBps = parseBitrate(adVBitRate);
 int tgtVBps = parseBitrate(targetVBitRate);
 if (adVBps > tgtVBps) {
 targetVBitRate = adVBitRate;
 std::cout << "DEBUG: Using ad " << adName << "'s higher vbr: " << targetVBitRate << " (" << adVBps << " bps)" << std::endl;
 }
 }
 // Force stereo for WebRTC Opus compatibility
 targetChannels = 2;
 std::cout << "DEBUG: Forced target channels to 2 for Opus" << std::endl;
 std::cout << "DEBUG: Final targets: " << targetWidth << "x" << targetHeight << " SR " << targetSampleRate << "Hz ch " << targetChannels << " vbr " << targetVBitRate << " abr " << targetABitRate << " fps " << targetFPS << std::endl;

 std::string targetVCodec = "libx264";
 std::string sarStr = std::to_string(epSarNum) + "/" + std::to_string(epSarDen);
 std::cout << "DEBUG: Target VCodec: " << targetVCodec << " SAR str: " << sarStr << std::endl;

 bool reencodeEpisode = true; // Force re-encode for baseline
 std::cout << "DEBUG: Reencode episode: " << reencodeEpisode << std::endl;

 std::string epAudioFilter = buildAudioFilter(epSampleRate, epChannels, targetSampleRate, targetChannels);
 std::cout << "DEBUG: Episode audio filter: '" << epAudioFilter << "'" << std::endl;

 // Sort breaks by StartSec (with overlap check)
 std::vector<Break> sortedBrks = brks;
 std::sort(sortedBrks.begin(), sortedBrks.end(), [](const Break& a, const Break& b) { return a.startSec < b.startSec; });
 std::cout << "DEBUG: Sorted breaks by startSec" << std::endl;
 for (size_t i = 1; i < sortedBrks.size(); ++i) {
 if (sortedBrks[i].startSec <= sortedBrks[i - 1].startSec + 1e-6) { // Allow tiny epsilon
 std::cout << "DEBUG: Overlap detected between break " << (i-1) << " and " << i << std::endl;
 return false;
 }
 }

 // Build concat parts and temps
 std::vector<std::string> concatParts;
 std::vector<std::string> tempFiles;
 double currentSecFloat = 0.0;
 int segIndex = 0;

 // Loop over breaks
 for (size_t i = 0; i < sortedBrks.size(); ++i) {
 const auto& brk = sortedBrks[i];
 double segStart = currentSecFloat;
 double segDur = brk.startSec - currentSecFloat;
 std::cout << "DEBUG: Processing break " << i << ": current=" << currentSecFloat << " start=" << brk.startSec << " segDur=" << segDur << std::endl;
 if (segDur > 0) {
 std::string segFile = outputDir + "/seg" + std::to_string(segIndex) + ".mp4";
 std::cout << "DEBUG: Extracting pre-break segment: " << segFile << std::endl;
 if (!extractSegment(episodePath, segFile, segStart, segDur, reencodeEpisode, targetWidth, targetHeight, targetSampleRate, targetChannels, targetVBitRate, targetABitRate, targetFPS, targetVCodec, sarStr, epAudioFilter)) {
 return false;
 }
 concatParts.push_back("seg" + std::to_string(segIndex) + ".mp4");
 tempFiles.push_back(segFile);
 ++segIndex;
 } else if (segDur < -1e-6) { // Epsilon
 std::cout << "DEBUG: break " << i << " start (" << brk.startSec << ") before current (" << currentSecFloat << "): overlap" << std::endl;
 return false;
 }

 // Insert ads for this break (re-encode each to temp)
 std::cout << "DEBUG: Inserting " << brk.ads.size() << " ads for break " << i << std::endl;
 for (size_t adIdx = 0; adIdx < brk.ads.size(); ++adIdx) {
 const std::string& adName = brk.ads[adIdx];
 auto adIt = adInfos.find(adName);
 if (adIt == adInfos.end()) {
 std::cout << "DEBUG: AdInfo not found for " << adName << std::endl;
 return false;
 }
 const AdInfo& info = adIt->second;
 std::string adTemp = outputDir + "/ad_temp_" + std::to_string(i) + "_" + std::to_string(adIdx) + "_" + fs::path(adName).filename().string();
 std::cout << "DEBUG: Re-encoding ad " << adName << " to " << adTemp << " (dur=" << info.duration << ")" << std::endl;
 // Re-encode ad
 std::string adArgs = "-i \"" + adName + "\" ";
 std::string adScaleFilter;
 if (info.height != targetHeight || info.width != targetWidth) {
 adScaleFilter = "scale=" + std::to_string(targetWidth) + ":" + std::to_string(targetHeight) + ":force_original_aspect_ratio=decrease,pad=" + std::to_string(targetWidth) + ":" + std::to_string(targetHeight) + ":(ow-iw)/2:(oh-ih)/2,setsar=" + sarStr;
 adArgs += "-vf \"" + adScaleFilter + "\" ";
 std::cout << "DEBUG: Ad scale filter: '" << adScaleFilter << "'" << std::endl;
 }
 std::string adAudioFilter = buildAudioFilter(info.sampleRate, info.channels, targetSampleRate, targetChannels);
 if (!adAudioFilter.empty()) {
 adArgs += "-af \"" + adAudioFilter + "\" ";
 std::cout << "DEBUG: Ad audio filter: '" << adAudioFilter << "'" << std::endl;
 }
 adArgs += "-r " + std::to_string(targetFPS) + " -c:v libx264 -preset ultrafast -crf 23 -b:v " + targetVBitRate + " -c:a aac -b:a " + targetABitRate + " ";
 adArgs += "-profile:v baseline -level 3.0 ";
 adArgs += "-x264-params keyint=1:min-keyint=1:scenecut=-1 "; // Force IDR every frame
 adArgs += "-avoid_negative_ts make_zero \"" + adTemp + "\"";
 std::string adFullCmd = "ffmpeg " + adArgs;
 std::cout << "DEBUG: Executing ad re-encode: " << adFullCmd << std::endl; // Debug
 int adResult = std::system(adFullCmd.c_str());
 if (adResult != 0) {
 std::cout << "DEBUG: ad " << adName << " re-encode failed: " << adResult << std::endl;
 return false;
 }
 concatParts.push_back(fs::path(adTemp).filename().string());
 tempFiles.push_back(adTemp);
 }

 currentSecFloat = brk.startSec; // Actually advance by ad duration, not just start
 std::cout << "DEBUG: Advanced currentSec to " << currentSecFloat << std::endl;
 }

 // Final segment (after last break)
 double finalStart = currentSecFloat;
 double finalDur = epDuration - finalStart;
 std::cout << "DEBUG: Final segment: start=" << finalStart << " dur=" << finalDur << std::endl;
 if (finalDur > 0) {
 std::string finalFile = outputDir + "/seg" + std::to_string(segIndex) + ".mp4";
 std::cout << "DEBUG: Extracting final segment: " << finalFile << std::endl;
 if (!extractSegment(episodePath, finalFile, finalStart, finalDur, reencodeEpisode, targetWidth, targetHeight, targetSampleRate, targetChannels, targetVBitRate, targetABitRate, targetFPS, targetVCodec, sarStr, epAudioFilter)) {
 return false;
 }
 concatParts.push_back("seg" + std::to_string(segIndex) + ".mp4");
 tempFiles.push_back(finalFile);
 } else if (finalDur < -1e-6) {
 std::cout << "DEBUG: breaks overrun episode: current=" << currentSecFloat << " > " << epDuration << std::endl;
 return false;
 }

 if (concatParts.empty()) {
 std::cout << "DEBUG: No concat parts, failing" << std::endl;
 return false;
 }
 std::cout << "DEBUG: Concat parts count: " << concatParts.size() << std::endl;
 for (size_t i = 0; i < concatParts.size(); ++i) {
 std::cout << "DEBUG: Concat part " << i << ": " << concatParts[i] << std::endl;
 }

 // Build concat list
 std::string concatLocal = "concat.txt";
 std::string concatListFile = outputDir + "/" + concatLocal;
 std::ofstream concatFile(concatListFile);
 if (!concatFile) {
 std::cout << "DEBUG: Failed to open concat list file: " << concatListFile << std::endl;
 return false;
 }
 for (const auto& part : concatParts) {
 concatFile << "file '" << part << "'\n";
 }
 concatFile.close();
 std::cout << "DEBUG: Wrote concat list: " << concatListFile << std::endl;

 // Concat
 std::string fullFile = outputDir + "/full.mp4";
 std::string concatArgs = "-fflags +genpts -f concat -safe 0 -i \"" + concatListFile + "\" -c copy -r " + std::to_string(targetFPS) + " -movflags +faststart \"" + fullFile + "\"";
 std::string concatFullCmd = "ffmpeg " + concatArgs;
 std::cout << "DEBUG: Executing concat: " << concatFullCmd << std::endl; // Debug
 int concatResult = std::system(concatFullCmd.c_str());
 if (concatResult != 0) {
 std::cout << "DEBUG: concat failed: " << concatResult << std::endl;
 return false;
 }
 std::cout << "DEBUG: Concatenated full: " << fullFile << " (" << concatParts.size() << " parts) at target " << targetWidth << "x" << targetHeight << " SR " << targetSampleRate << "Hz FPS " << targetFPS << std::endl;

 // Extract to .h264 and .opus segments for WebRTC server (instead of .mp4)
 std::string segmentsDir = "./webrtc_segments";
 fs::create_directories(segmentsDir);
 std::cout << "DEBUG: Created/verified segments dir: " << segmentsDir << std::endl;
 int segmentIndex = 0;
 for (const auto& tempFile : tempFiles) {
     std::string base = fs::path(tempFile).filename().string();
     std::string prefix;
     if (base.find("seg") == 0) {
         prefix = "seg";
     } else {
         prefix = "ad_";
     }
     std::string indexStr = std::to_string(segmentIndex);
     std::string tempMp4 = segmentsDir + "/temp_" + prefix + indexStr + ".mp4";
     fs::rename(tempFile, tempMp4);
     std::string h264File = segmentsDir + "/" + prefix + indexStr + ".h264";
     std::string opusFile = segmentsDir + "/" + prefix + indexStr + ".opus";
     std::string cmdV = "ffmpeg -y -i \"" + tempMp4 + "\" -c:v copy -bsf:v h264_mp4toannexb \"" + h264File + "\"";
     int resV = std::system(cmdV.c_str());
     if (resV != 0) {
         std::cout << "Failed to extract h264 from " << tempMp4 << ": " << resV << std::endl;
     }
     std::string cmdA = "ffmpeg -y -i \"" + tempMp4 + "\" -vn -c:a libopus -b:a 64k -frame_duration 20 -application audio \"" + opusFile + "\"";
     int resA = std::system(cmdA.c_str());
     if (resA != 0) {
         std::cout << "Failed to extract opus from " << tempMp4 << ": " << resA << std::endl;
     }
     fs::remove(tempMp4);
     std::cout << "Extracted " << h264File << " and " << opusFile << std::endl;
     ++segmentIndex;
 }
 std::cout << "DEBUG: Extracted " << tempFiles.size() << " .h264/.opus pairs" << std::endl;

 // Cleanup only concat list (temps are now final segments)
 std::cout << "DEBUG: Cleaning up concat list" << std::endl;
 if (fs::exists(concatListFile)) {
     fs::remove(concatListFile);
     std::cout << "DEBUG: Removed concat list" << std::endl;
 }

 std::cout << "Merged file ready: " << fullFile << std::endl;
 std::cout << "WebRTC-ready .h264 and .opus segments generated in: " << segmentsDir << std::endl;
 return true;
}

int main(int argc, char* argv[]) {
 if (argc < 4) {
 std::cerr << "Usage: " << argv[0] << " <episode_file> <output_dir> <num_breaks> [for each break: <start_sec> <num_ads> <ad_file1> <ad_file2> ... ]" << std::endl;
 std::cerr << "All parameters are required. For 0 breaks, provide just num_breaks=0." << std::endl;
 return 1;
 }

 std::string episode_file = argv[1];
 std::string output_dir = argv[2];
 int num_breaks;
 try {
 num_breaks = std::stoi(argv[3]);
 } catch (...) {
 std::cerr << "Invalid num_breaks: " << argv[3] << std::endl;
 return 1;
 }

 std::vector<Break> brks;
 size_t arg_idx = 4;
 for (int i = 0; i < num_breaks; ++i) {
 if (arg_idx + 1 >= argc) {
 std::cerr << "Insufficient arguments for break " << i << std::endl;
 return 1;
 }
 double start_sec;
 try {
 start_sec = std::stod(argv[arg_idx++]);
 } catch (...) {
 std::cerr << "Invalid start_sec: " << argv[arg_idx-1] << std::endl;
 return 1;
 }

 int num_ads;
 try {
 num_ads = std::stoi(argv[arg_idx++]);
 } catch (...) {
 std::cerr << "Invalid num_ads: " << argv[arg_idx-1] << std::endl;
 return 1;
 }

 if (arg_idx + num_ads > argc) {
 std::cerr << "Insufficient ad files for break " << i << " (expected " << num_ads << ")" << std::endl;
 return 1;
 }

 std::vector<std::string> ads;
 for (int j = 0; j < num_ads; ++j) {
 ads.push_back(argv[arg_idx++]);
 }
 brks.push_back({start_sec, ads});
 }

 if (arg_idx != static_cast<size_t>(argc)) {
 std::cerr << "Extra arguments provided after breaks." << std::endl;
 return 1;
 }

 std::cout << "Current working directory: " << fs::current_path() << std::endl; // Debug
 std::cout << "DEBUG: Parsed args: episode=" << episode_file << " output_dir=" << output_dir << " num_breaks=" << num_breaks << std::endl;

 fs::create_directories(output_dir);
 std::cout << "DEBUG: Created output dir" << std::endl;

 std::cout << "Inserting ads and merging video..." << std::endl;
 if (!insertBreak(episode_file, output_dir, brks)) {
 std::cout << "Ad insertion failed!" << std::endl;
 return 1;
 } else {
 std::cout << "Merged video ready in " << output_dir << "/full.mp4" << std::endl;
 std::cout << "WebRTC-ready .h264 and .opus segments generated in ./webrtc_segments/" << std::endl;
 }
 return 0;
}