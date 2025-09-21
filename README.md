To compile:
g++ -std=c++17 -O3 -march=native -o ad_insert ad_insert.cpp
g++ -std=c++17 -O3 -march=native -o generate_hls generate_hls.cpp

To run:
generate_hls <segmentDur> <input_file> <output_dir>
generate_hls 3 "./compiled_episode/full.mp4" "./hls/"

ad_insert <episode_file> <output_dir> <num_breaks> [for each break: <start_sec> <num_ads> <ad_file1> <ad_file2> ... ]
ad_insert "./media/episode1.mp4" "./compiled_episode" 2 15.5 1 "./media/ad1.mp4" 877 3 "./media/ad2.mp4" "./media/ad3.mp4" "./media/ad4.mp4"


run server:
go run live_server.go



./
├── live_server.go
├── go
├── main.streamHandler
├── go.sum
├── go.mod
├── generate_hls.cpp //NOt used for this
├── ad_insert.cpp //This takes a video, splits it into 2 parts, inserts otehr videos (ads) into those splits, and recombines the video. It also creates the h264 files
├── compiled_episode/
│   └── full.mp4 //Note: If I double click this to run it it runs fine in VLC.
├── video/
│   └── video.go
├── webrtc_segments/
│   ├── ad_1.h264
│   ├── ad_3.h264
│   ├── ad_4.h264
│   ├── ad_5.h264
│   ├── seg0.h264
│   ├── seg2.h264
│   └── seg6.h264
└── media/
    ├── episode1.mp4
    ├── ad1.mp4
    ├── ad2.mp4
    ├── ad3.mp4
    ├── ad4.mp4
    ├── ad5.mp4
    └── ad6.mp4

ffprobe -show_frames -select_streams v ad_1.h264 | find "pict_type=I"
ffprobe -show_frames -select_streams v ad_3.h264 | find "pict_type=I"
ffprobe -show_frames -select_streams v ad_4.h264 | find "pict_type=I"
ffprobe -show_frames -select_streams v ad_5.h264 | find "pict_type=I"
ffprobe -show_frames -select_streams v seg0.h264 | find "pict_type=I"
ffprobe -show_frames -select_streams v seg2.h264 | find "pict_type=I"
ffprobe -show_frames -select_streams v seg6.h264 | find "pict_type=I"


go run analyze_h264.go "./webrtc_segments/ad_1.h264"
go run analyze_h264.go "./webrtc_segments/ad_3.h264"
go run analyze_h264.go "./webrtc_segments/ad_4.h264"
go run analyze_h264.go "./webrtc_segments/ad_5.h264"
go run analyze_h264.go "./webrtc_segments/seg0.h264"
go run analyze_h264.go "./webrtc_segments/seg2.h264"
go run analyze_h264.go "./webrtc_segments/seg6.h264"




Install Go: Download from golang.org (v1.21+). Run go version to verify.
Install FFmpeg: macOS: brew install ffmpeg
Ubuntu: sudo apt update && sudo apt install ffmpeg
Windows: Download from ffmpeg.org and add to PATH.

Sample Media: Place a sample video (episode1.mp4, ~5-10min) and commercial (ad.mp4, ~2min) in a ./media folder.
Project Setup:

mkdir tv-station && cd tv-station
go mod init tv-station
go get github.com/gin-gonic/gin github.com/robfig/cron/v3

(No goav needed; we're using os/exec for FFmpeg.)



C++ version:
Install FFmpeg Development Libraries (for libav*):Ubuntu/Debian: sudo apt update && sudo apt install libavcodec-dev libavformat-dev libavutil-dev libswscale-dev libavfilter-dev pkg-config
macOS (Homebrew): brew install ffmpeg
Windows: Use vcpkg (vcpkg install ffmpeg) or MSYS2 (pacman -S mingw-w64-x86_64-ffmpeg).
Verify: pkg-config --cflags --libs libavformat should output paths/flags.

Compiler: g++ 11+ (C++17 standard).