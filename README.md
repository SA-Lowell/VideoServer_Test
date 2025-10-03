Final Fantasy X Promo Trailer 2 has an issue where it goes to black screen. FIX IT

ffprobe -i "Z:\Videos\Commercials\PS2\Final Fantasy X\Final Fantasy X Promo Trailer 2.mp4" -show_streams -show_format

Start Postgres server:
	set PGDATA=C:\Program Files\PostgreSQL\17\data
	pg_ctl start

To compile:
g++ -std=c++17 -O3 -march=native -o ad_insert ad_insert.cpp
g++ -std=c++17 -O3 -march=native -o generate_hls generate_hls.cpp
g++ -std=c++17 -O3 -march=native -o ad_break_detector ad_break_detector.cpp

To run:
generate_hls <segmentDur> <input_file> <output_dir>
generate_hls 3 "./compiled_episode/full.mp4" "./hls/"

ad_insert <episode_file> <temp_dir> <full_mp4_path> <segments_dir> <num_breaks> [for each break: <start_sec> <num_ads> <ad_file1> <ad_file2> ... ] [ <custom_base_name1> <custom_base_name2> ... ]
ad_insert "./media/episode1.mp4" "./temp_video_directory" "./full_compiled_video/full_video.mp4" "./webrtc_segments" 2 15.5 1 "./media/ad1.mp4" 877 5 "./media/ad2.mp4" "./media/ad3.mp4" "./media/ad4.mp4" "./media/ad5.mp4" "./media/ad6.mp4" "seg0" "ad_1" "seg2" "ad_3" "ad_4" "ad_5" "ad_6" "ad_7" "seg8"

ad_break_detector <video_file> [--hide-decimal] [--hide-mmss] [--hide-start] [--hide-midpoint] [--hide-end]
ad_break_detector "episode1.mp4" --hide-mmss --hide-decimal --hide-start --hide-end

run server:
go run video_server.go

./
├── video_server.go
├── admin_server.go
├── go
├── main.streamHandler
├── go.sum
├── go.mod
├── generate_hls.cpp //NOt used for this
├── ad_insert.cpp //This takes a video, splits it into 2 parts, inserts otehr videos (ads) into those splits, and recombines the video. It also creates the h264 files
├── ad_break_detector.cpp // This is just used fror detecting potential ad breaks in a file. It does nothing more than output timestamps so I can then manually investigate those times.
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
│   ├── seg6.h264
│   ├── ad_1.opus
│   ├── ad_3.opus
│   ├── ad_4.opus
│   ├── ad_5.opus
│   ├── seg0.opus
│   ├── seg2.opus
│   └── seg6.opus
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













GROK:
Other Types of "Commercials" and Promotional Content to Account ForYes, commercials and promos come in many flavors, especially in media/TV/gaming contexts like yours. Your schema's metadata abstraction (e.g., 'media_type' = "commercial" with subtypes or details in JSONB) handles this well without new tables—use consistent naming conventions in metadata to categorize. Here's a breakdown of common types beyond basic ads, with suggestions for your DB:Standard TV/Radio Spots: Short (15-60s) ads for broadcast. E.g., a 30s RE2 TV commercial. Name convention: Use 'media_type' = "commercial", with 'subtype' = "tv_spot" or JSONB: {"type": "commercial", "subtype": "tv_spot", "duration": "00:00:30"}. Account for regional variants (e.g., 'country' = "US" vs. "JP" for localized voiceovers/subtitles).
Trailers/Teasers: Longer previews (1-3min) showing gameplay/footage. E.g., RE2 E3 teaser. Convention: 'media_type' = "trailer" or "teaser" (distinguish: teaser = short/hype-focused, trailer = story-revealing). Add 'event' = "E3 1997" or 'version' = "red_band" for mature cuts.
Sneak Peeks/Previews: Early/incomplete glimpses, often exclusive (e.g., magazine DVD extras). E.g., RE2 beta preview. Convention: 'media_type' = "sneak_peak" or "preview", with 'stage' = "beta" or "alpha". Useful for games with dev cycles.
Behind-the-Scenes (BTS)/Making-Of: Featurettes on production. E.g., RE2 dev diary. Convention: 'media_type' = "bts", with 'focus' = "development" or "voice_acting".
Interviews/Panels: Talent discussions (e.g., RE2 director interview). Convention: 'media_type' = "interview", with 'person' = "Shinji Mikami" or 'event' = "Comic-Con".
Viral/User-Generated Promos: Modern social media clips (e.g., fan-made RE2 hype video, if curated). Convention: 'media_type' = "viral_promo", with 'source' = "user_generated" to flag non-official.
Cross-Promos/Tie-Ins: Ads linking to other media (e.g., RE2 comic tie-in ad). Convention: Add 'linked_title_id' metadata (or column) referencing another title.
Other Edge Cases: PSAs (public service ads, if thematic), bumpers (short channel IDs), or infomercials (long-form sales). Convention: 'media_type' = "psa" or "infomercial". For non-ads (e.g., full episodes as "promo" clips), use tags like "excerpt".

Naming Conventions Tips: Standardize metadata keys (e.g., always 'subtype' for variants, 'country' as ISO codes like "US"). Document in a schema note or app enum. For inserts, use JSONB objects for multi-values (e.g., {"countries": ["US", "JP"]}). If types proliferate, add a promo-specific metadata_type cluster (e.g., prefix "promo_"). This keeps your DB extensible without changes.If adding many, consider a 'promo_type' column in videos for quick filtering, but your metadata works fine now.

