package main

import (
    "bytes"
    "database/sql"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "math"
    "math/rand"
    "os"
    "os/exec"
    "path/filepath"
    "runtime"
    "sort"
    "strconv"
    "strings"
    "sync"
    "time"
    _ "github.com/lib/pq"
    "github.com/gin-contrib/cors"
    "github.com/gin-gonic/gin"
    "github.com/pion/webrtc/v3"
    "github.com/pion/webrtc/v3/pkg/media"
    "github.com/pion/webrtc/v3/pkg/media/oggreader"
)

var errorLogger *log.Logger

const (
    HlsDir = "./webrtc_segments"
    Port = ":8081"
    ClockRate = 90000
    AudioFrameMs = 20
    DefaultFPSNum = 30000
    DefaultFPSDen = 1001
    DefaultDur = 0.0
    DefaultStation = "default"
    AdInsertPath = "./ad_insert.exe"
    DefaultVideoBaseDir = "Z:/Videos"
    DefaultTempPrefix = "ad_insert_"
    ChunkDuration = 30.0 // Process 30-second chunks
    BufferThreshold = 120.0 // Start processing more chunks when buffer < 120s
    maxAdRetries = 5 // Higher retry limit for ads
)

const dbConnString = "user=postgres password=aaaaaaaaaa dbname=webrtc_tv sslmode=disable host=localhost port=5432"

type fpsPair struct {
    num int
    den int
}

type BreakPoint struct {
	Time     float64
	IsFade   bool
	Color    string
	FadeOut  FadePhase
	FadeIn   FadePhase
}

type FadePhase struct {
	Video FadeRange
	Audio FadeRange
}

type FadeRange struct {
	Start float64
	End   float64
}

type bufferedChunk struct {
    segPath string
    dur float64
    isAd bool
    videoID int64
    fps fpsPair
    effective_advance float64
}

type bitReader struct {
    data []byte
    pos int
}

type LoudnormOutput struct {
    InputI string `json:"input_i"`
    InputLRA string `json:"input_lra"`
    InputTP string `json:"input_tp"`
    InputThresh string `json:"input_thresh"`
}

type Station struct {
    name string
    segmentList []bufferedChunk
    spsPPS [][]byte
    fmtpLine string
    trackVideo *webrtc.TrackLocalStaticSample
    trackAudio *webrtc.TrackLocalStaticSample
    videoQueue []int64
    currentVideo int64
    currentIndex int
    currentOffset float64
    viewers int
    processing bool
    stopCh chan struct{}
    adsEnabled bool
    mu sync.Mutex
    currentVideoRTPTS uint32
    currentAudioSamples uint32
}

var adIDs []int64
var videoBaseDir string
var stations = make(map[string]*Station)
var noAdsStations = make(map[string]*Station)
var mu sync.Mutex
var globalStart = time.Now()

func newBitReader(data []byte) *bitReader {
    return &bitReader{data: data, pos: 0}
}

func (br *bitReader) readBit() (uint, error) {
    if br.pos/8 >= len(br.data) {
        return 0, fmt.Errorf("EOF")
    }
    byteIdx := br.pos / 8
    bitIdx := 7 - (br.pos % 8) // Declare bitIdx here
    bit := uint((br.data[byteIdx] >> bitIdx) & 1)
    br.pos++
    return bit, nil
}

func (br *bitReader) readUe() (uint, error) {
    leadingZeros := 0
    for {
        bit, err := br.readBit()
        if err != nil {
            return 0, err
        }
        if bit == 1 {
            break
        }
        leadingZeros++
    }
    val := (uint(1) << leadingZeros) - 1
    for i := 0; i < leadingZeros; i++ {
        bit, err := br.readBit()
        if err != nil {
            return 0, err
        }
        val += bit << (leadingZeros - 1 - i)
    }
    return val, nil
}

func getFirstMbInSlice(nalu []byte) (uint, error) {
    if len(nalu) < 2 {
        return 0, fmt.Errorf("NALU too short")
    }
    ebsp := nalu[1:]
    rbsp := make([]byte, 0, len(ebsp))
    for i := 0; i < len(ebsp); {
        if i+2 < len(ebsp) && ebsp[i] == 0 && ebsp[i+1] == 0 && ebsp[i+2] == 3 {
            rbsp = append(rbsp, 0, 0)
            i += 3
        } else {
            rbsp = append(rbsp, ebsp[i])
            i++
        }
    }
    br := newBitReader(rbsp)
    return br.readUe()
}

func sanitizeTrackID(name string) string {
    return strings.ReplaceAll(strings.ReplaceAll(name, " ", "_"), "'", "")
}

func processVideo(st *Station, videoID int64, db *sql.DB, startTime, chunkDur float64, fadeType string, videoSt, videoD, audioSt, audioD float64, color string) ([]string, [][]byte, string, float64, fpsPair, error) {
    const durDiffThreshold = 0.001
    if startTime < 0 {
        adjust := -startTime
        chunkDur += adjust
        videoSt += adjust
        audioSt += adjust
        startTime = 0
        log.Printf("Station %s: Clamped negative startTime to 0, adjusted dur to %.3f, videoSt %.4f, audioSt %.4f", st.name, chunkDur, videoSt, audioSt)
    }
    if db == nil {
        errorLogger.Printf("Station %s: Database connection is nil for video %d", st.name, videoID)
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("database connection is nil")
    }
    var segments []string
    var spsPPS [][]byte
    var fmtpLine string
    var uri string
    var loudI, loudLRA, loudTP, loudThresh sql.NullFloat64
    err := db.QueryRow(`SELECT uri FROM videos WHERE id = $1`, videoID).Scan(&uri)
    if err != nil {
        errorLogger.Printf("Station %s: Failed to get URI for video %d: %v", st.name, videoID, err)
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("failed to get URI for video %d: %v", videoID, err)
    }
    originalPath := filepath.Join(videoBaseDir, uri)
    normalizedPath := filepath.Join("./normalized", filepath.Base(uri))
    fullEpisodePath := originalPath
    if _, statErr := os.Stat(normalizedPath); statErr == nil {
        fullEpisodePath = normalizedPath
        log.Printf("Station %s: Using normalized path for video %d: %s", st.name, videoID, normalizedPath)
    } else {
        log.Printf("Station %s: Normalized path not found for video %d, using original: %s", st.name, videoID, originalPath)
    }
    if _, err := os.Stat(fullEpisodePath); err != nil {
        errorLogger.Printf("Station %s: Episode file not found: %s", st.name, fullEpisodePath)
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("episode file not found: %s", fullEpisodePath)
    }
    var duration sql.NullFloat64
    err = db.QueryRow(`SELECT duration FROM videos WHERE id = $1`, videoID).Scan(&duration)
    if err != nil {
        errorLogger.Printf("Station %s: Failed to get duration for video %d: %v", st.name, videoID, err)
    }
    adjustedChunkDur := chunkDur
    isFinalChunk := false
    if duration.Valid && startTime+chunkDur > duration.Float64 {
        adjustedChunkDur = duration.Float64 - startTime
        isFinalChunk = true
        if adjustedChunkDur <= 0 {
            errorLogger.Printf("Station %s: No remaining duration for video %d at start time %.3f", st.name, videoID, startTime)
            return nil, nil, "", 0, fpsPair{}, fmt.Errorf("no remaining duration for video %d at start time %f", videoID, startTime)
        }
    }
    if adjustedChunkDur < 0.001 {
        log.Printf("Station %s: Negligible chunk duration %.3fs for video %d at %.3fs, skipping without loss assumption", st.name, adjustedChunkDur, videoID, startTime)
        return nil, nil, "", adjustedChunkDur, fpsPair{}, nil // Return dur>0 but no files, effective advance
    }
    tempDir := filepath.Join(".", "temp_encoded_segments")
    if err := os.MkdirAll(tempDir, 0755); err != nil {
        errorLogger.Printf("Station %s: Failed to create temp_encoded_segments directory for video %d: %v", st.name, videoID, err)
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("failed to create temp_encoded_segments directory for video %d: %v", videoID, err)
    }
    if err := os.MkdirAll(HlsDir, 0755); err != nil {
        errorLogger.Printf("Station %s: Failed to create webrtc_segments directory: %v", st.name, err)
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("failed to create webrtc_segments directory: %v", err)
    }
    safeStationName := strings.ReplaceAll(st.name, " ", "_")
    baseName := fmt.Sprintf("%s_vid%d_chunk_%.3f", safeStationName, videoID, startTime)
    segName := baseName + ".h264"
    fullSegPath := filepath.Join(HlsDir, segName)
    opusName := baseName + ".opus"
    opusPath := filepath.Join(HlsDir, opusName)
    tempDurMP4 := filepath.Join(tempDir, baseName+"_dur.mp4")
    tempMuxedPath := filepath.Join(tempDir, baseName+"_muxed.mp4")
    var audioData []byte
    var sampleRate, channels int
    var hasAudio bool
    cmdProbe := exec.Command(
        "ffprobe",
        "-v", "error",
        "-select_streams", "a:0",
        "-show_entries", "stream=sample_rate,channels",
        "-of", "json",
        fullEpisodePath,
    )
    outputProbe, err := cmdProbe.Output()
    if err == nil {
        var probeResult struct {
            Streams []struct {
                SampleRate string `json:"sample_rate"`
                Channels int `json:"channels"`
            } `json:"streams"`
        }
        if err := json.Unmarshal(outputProbe, &probeResult); err != nil {
            errorLogger.Printf("Station %s: Failed to parse audio probe JSON for %s: %v", st.name, fullEpisodePath, err)
        } else if len(probeResult.Streams) > 0 {
            hasAudio = true
            sampleRate, _ = strconv.Atoi(probeResult.Streams[0].SampleRate)
            channels = probeResult.Streams[0].Channels
            log.Printf("Station %s: Input audio for %s: sample_rate=%d, channels=%d", st.name, fullEpisodePath, sampleRate, channels)
        } else {
            log.Printf("Station %s: No audio stream found in %s", st.name, fullEpisodePath)
        }
    } else {
        errorLogger.Printf("Station %s: Failed to probe input audio for %s: %v", st.name, fullEpisodePath, err)
    }
    if sampleRate == 0 {
        sampleRate = 48000
    }
    if channels == 0 {
        channels = 2
    }
    fpsNum := DefaultFPSNum
    fpsDen := DefaultFPSDen
    cmdFPS := exec.Command(
        "ffprobe",
        "-v", "error",
        "-select_streams", "v:0",
        "-show_entries", "stream=r_frame_rate",
        "-of", "default=noprint_wrappers=1:nokey=1",
        fullEpisodePath,
    )
    outputFPS, err := cmdFPS.Output()
    if err == nil {
        rate := strings.TrimSpace(string(outputFPS))
        if rate == "0/0" || rate == "" {
            log.Printf("Station %s: Invalid FPS from original file %s, using default %d/%d", st.name, fullEpisodePath, fpsNum, fpsDen)
        } else {
            if slash := strings.Index(rate, "/"); slash != -1 {
                num, err1 := strconv.Atoi(rate[:slash])
                den, err2 := strconv.Atoi(rate[slash+1:])
                if err1 == nil && err2 == nil && num > 0 && den > 0 {
                    fpsNum = num
                    fpsDen = den
                }
            } else {
                num, err := strconv.Atoi(rate)
                if err == nil && num > 0 {
                    fpsNum = num
                    fpsDen = 1
                }
            }
        }
        log.Printf("Station %s: FPS for %s (from original): %d/%d", st.name, fullSegPath, fpsNum, fpsDen)
    } else {
        errorLogger.Printf("Station %s: ffprobe failed for original %s: %v", st.name, fullEpisodePath, err)
    }
    fps := float64(fpsNum) / float64(fpsDen)
    gopSize := int(math.Round(fps * 2))
    if isFinalChunk {
        gopSize = int(math.Round(fps * 0.5))
    }
    keyintMin := gopSize
    keyFrameParams := fmt.Sprintf("keyint=%d:min-keyint=1:scenecut=0", gopSize)
    err = db.QueryRow(
        "SELECT loudnorm_input_i, loudnorm_input_lra, loudnorm_input_tp, loudnorm_input_thresh FROM videos WHERE id = $1",
        videoID,
    ).Scan(&loudI, &loudLRA, &loudTP, &loudThresh)
    if err != nil {
        errorLogger.Printf("Station %s: Failed to query loudnorm measurements for video %d: %v", st.name, videoID, err)
    }
    // Build args for muxed encoding with exact duration
    argsMuxed := []string{
        "-y",
        "-err_detect", "ignore_err",
        "-analyzeduration", "100M",
        "-probesize", "100M",
    }
    if startTime > 0 {
        argsMuxed = append(argsMuxed, "-ss", fmt.Sprintf("%.3f", startTime))
    }
    argsMuxed = append(argsMuxed,
        "-i", fullEpisodePath,
        "-t", fmt.Sprintf("%.6f", adjustedChunkDur), // Precise duration
        "-c:v", "libx264",
        "-preset", "ultrafast",
        "-crf", "23",
        "-bf", "0",
        "-maxrate", "5M",
        "-bufsize", "10M",
        "-profile:v", "baseline",
        "-level", "5.2",
        "-pix_fmt", "yuv420p",
        "-force_fps",
        "-r", fmt.Sprintf("%d/%d", fpsNum, fpsDen),
        "-fps_mode", "cfr",
        "-force_key_frames", "expr:eq(n,0)",
        "-sc_threshold", "0",
        "-x264-params", keyFrameParams,
        "-c:a", "libopus",
        "-b:a", "128k",
        "-ar", "48000",
        "-ac", "2",
        "-frame_duration", "20",
        "-page_duration", "960",
        "-application", "audio",
        "-vbr", "on",
        "-avoid_negative_ts", "make_zero",
        "-fflags", "+genpts",
        "-async", "1",
        "-max_delay", "0",
        "-threads", "0",
        "-f", "mp4",
        "-shortest", // Ensure output stops at shortest stream (video)
        tempMuxedPath,
    )
    // Insert combined filters (loudnorm + fades + apad for exact duration)
    var combinedFilter string
    if loudI.Valid && fullEpisodePath == originalPath {
        combinedFilter = fmt.Sprintf(
            "loudnorm=I=-23:TP=-1.5:LRA=11:measured_I=%.2f:measured_LRA=%.2f:measured_TP=%.2f:measured_thresh=%.2f:offset=0:linear=true",
            loudI.Float64, loudLRA.Float64, loudTP.Float64, loudThresh.Float64,
        )
    }
    // Add short fades to prevent pops (20ms in/out)
    fadeDur := 0.02 // 20ms; test 0.01 for 10ms if needed
    if fadeType == "" && adjustedChunkDur >= 2*fadeDur {
        afade := fmt.Sprintf("afade=in:st=0:d=%.2f,afade=out:st=%.6f:d=%.2f", fadeDur, adjustedChunkDur-fadeDur, fadeDur)
        if combinedFilter != "" {
            combinedFilter += "," + afade
        } else {
            combinedFilter = afade
        }
    } else if fadeType == "" {
        log.Printf("Station %s: Skipping short fades for small chunk %.3fs", st.name, adjustedChunkDur)
    }
    if fadeType != "" && audioD > 0 {
        afadeType := "out"
        if fadeType == "in" {
            afadeType = "in"
        }
        afadeFilter := fmt.Sprintf("afade=t=%s:st=%.4f:d=%.4f", afadeType, audioSt, audioD)
        if combinedFilter != "" {
            combinedFilter += "," + afadeFilter
        } else {
            combinedFilter = afadeFilter
        }
    }
    // Always add apad to force exact audio duration
    apad := fmt.Sprintf("apad=whole_dur=%.6f", adjustedChunkDur)
    if combinedFilter != "" {
        combinedFilter += "," + apad
    } else {
        combinedFilter = apad
    }
    if combinedFilter != "" {
        insertIndex := -1
        for i, arg := range argsMuxed {
            if arg == "-c:a" {
                insertIndex = i + 2
                break
            }
        }
        if insertIndex != -1 {
            argsMuxed = append(argsMuxed[:insertIndex], append([]string{"-af", combinedFilter}, argsMuxed[insertIndex:]...)...)
        } else {
            argsMuxed = append(argsMuxed[:len(argsMuxed)-1], "-af", combinedFilter, tempMuxedPath)
        }
        log.Printf("Station %s: Inserted combined audio filter: %s", st.name, combinedFilter)
    }
    // Video fade if needed
    if fadeType != "" && videoD > 0 {
        vfadeType := "out"
        if fadeType == "in" {
            vfadeType = "in"
        }
        vfadeFilter := fmt.Sprintf("fade=t=%s:st=%.4f:d=%.4f:color=%s", vfadeType, videoSt, videoD, color)
        insertIndex := -1
        for i, arg := range argsMuxed {
            if arg == "-c:v" {
                insertIndex = i + 2
                break
            }
        }
        if insertIndex != -1 {
            argsMuxed = append(argsMuxed[:insertIndex], append([]string{"-vf", vfadeFilter}, argsMuxed[insertIndex:]...)...)
        } else {
            argsMuxed = append(argsMuxed, "-vf", vfadeFilter)
        }
        log.Printf("Station %s: Applied video fade %s: %s", st.name, fadeType, vfadeFilter)
    }
    // Run muxed encode
    cmdMuxed := exec.Command("ffmpeg", argsMuxed...)
    outputMuxed, err := cmdMuxed.CombinedOutput()
    log.Printf("Station %s: FFmpeg muxed output for %s: %s", st.name, tempMuxedPath, string(outputMuxed))
    if err != nil {
        errorLogger.Printf("Station %s: ffmpeg muxed command failed for %s: %v", st.name, tempMuxedPath, err)
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("ffmpeg muxed with loudnorm failed for video %d at %fs: %v", videoID, startTime, err)
    } else {
        log.Printf("Station %s: ffmpeg muxed succeeded for %s", st.name, tempMuxedPath)
    }
    // Extract video to H264
    cmdExtractVideo := exec.Command("ffmpeg", "-y", "-i", tempMuxedPath, "-c:v", "copy", "-bsf:v", "h264_mp4toannexb", fullSegPath)
    outputExtractV, err := cmdExtractVideo.CombinedOutput()
    log.Printf("Station %s: FFmpeg video extract output for %s: %s", st.name, fullSegPath, string(outputExtractV))
    if err != nil {
        errorLogger.Printf("Station %s: ffmpeg video extract failed for %s: %v", st.name, fullSegPath, err)
        os.Remove(tempMuxedPath)
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("ffmpeg video extract failed for video %d at %fs: %v", videoID, startTime, err)
    } else {
        log.Printf("Station %s: ffmpeg video succeeded for %s", st.name, fullSegPath)
    }
    // Extract audio to Opus (re-encode instead of copy for clean container)
    argsExtractA := []string{
        "-y",
        "-i", tempMuxedPath,
        "-c:a", "libopus",
        "-b:a", "128k",
        "-ar", "48000",
        "-ac", "2",
        "-frame_duration", "20",
        "-page_duration", "960",
        "-application", "audio",
        "-vbr", "on",
        "-avoid_negative_ts", "make_zero",
        "-fflags", "+genpts",
        "-async", "1",
        "-max_delay", "0",
        "-threads", "0",
        "-map_metadata", "-1",  // Strip metadata
        "-f", "opus",
        opusPath,
    }
    cmdExtractAudio := exec.Command("ffmpeg", argsExtractA...)
    outputExtractA, err := cmdExtractAudio.CombinedOutput()
    log.Printf("Station %s: FFmpeg audio extract output for %s: %s", st.name, opusPath, string(outputExtractA))
    if err != nil {
        errorLogger.Printf("Station %s: ffmpeg audio extract failed for %s: %v", st.name, opusPath, err)
        os.Remove(tempMuxedPath)
        os.Remove(fullSegPath)
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("ffmpeg audio extract failed for video %d at %fs: %v", videoID, startTime, err)
    } else {
        log.Printf("Station %s: Extracted audio to %s", st.name, opusPath)
    }
    // Clean up temp muxed file
    os.Remove(tempMuxedPath)
    // Append the segment path now that extraction is successful
    segments = append(segments, fullSegPath)
    audioData, err = os.ReadFile(opusPath)
    if err != nil || len(audioData) == 0 {
        errorLogger.Printf("Station %s: Audio file %s is empty or unreadable: %v, size=%d", st.name, opusPath, err, len(audioData))
        hasAudio = false
    }
    if !hasAudio || len(audioData) == 0 {
        log.Printf("Station %s: No valid audio for %s, generating silent Opus for %.3fs", st.name, opusPath, adjustedChunkDur)
        argsSilent := []string{
            "-y",
            "-f", "lavfi",
            "-i", fmt.Sprintf("anullsrc=r=48000:cl=stereo:d=%.3f", adjustedChunkDur),
            "-c:a", "libopus",
            "-b:a", "128k",
            "-ar", "48000",
            "-ac", "2",
            "-frame_duration", "20",
            "-page_duration", "960",
            "-application", "audio",
            "-vbr", "on",
            "-avoid_negative_ts", "make_zero",
            "-fflags", "+genpts",
            "-max_delay", "0",
            "-threads", "0",
            "-map_metadata", "-1",
            "-f", "opus",
            opusPath,
        }
        if fadeType != "" && audioD > 0 {
            afadeType := "out"
            if fadeType == "in" {
                afadeType = "in"
            }
            afadeFilter := fmt.Sprintf("afade=t=%s:st=%.4f:d=%.4f", afadeType, audioSt, audioD)
            argsSilent = append(argsSilent[:len(argsSilent)-1], "-af", afadeFilter, opusPath)
        }
        cmdSilent := exec.Command("ffmpeg", argsSilent...)
        outputSilent, err := cmdSilent.CombinedOutput()
        log.Printf("Station %s: FFmpeg silent audio output for %s: %s", st.name, opusPath, string(outputSilent))
        if err != nil {
            errorLogger.Printf("Station %s: Failed to generate silent audio for %s: %v", st.name, opusPath, err)
            return nil, nil, "", 0, fpsPair{}, fmt.Errorf("failed to generate silent audio for %s: %v", opusPath, err)
        }
        audioData, err = os.ReadFile(opusPath)
        if err != nil || len(audioData) == 0 {
            errorLogger.Printf("Station %s: Silent audio file %s is empty or unreadable: %v, size=%d", st.name, opusPath, err, len(audioData))
            return nil, nil, "", 0, fpsPair{}, fmt.Errorf("silent audio file %s is empty or unreadable: %v", opusPath, err)
        }
    }
    cmdAudioProbe := exec.Command(
        "ffprobe",
        "-v", "error",
        "-show_packets",
        opusPath,
    )
    outputAudioProbe, err := cmdAudioProbe.Output()
    if err != nil {
        errorLogger.Printf("Station %s: Failed to probe audio packets for %s: %v", st.name, opusPath, err)
        if len(audioData) > 0 {
            log.Printf("Station %s: Audio file %s probe failed but file size >0, assuming valid", st.name, opusPath)
        } else {
            errorLogger.Printf("Station %s: Audio file %s has zero size and probe failed", st.name, opusPath)
            return nil, nil, "", 0, fpsPair{}, fmt.Errorf("failed to probe audio packets for %s: %v", opusPath, err)
        }
    } else {
        outputStr := string(outputAudioProbe)
        packetLines := strings.Count(outputStr, "[PACKET]")
        if packetLines == 0 {
            errorLogger.Printf("Station %s: Audio file %s has 0 packets - invalid encoding", st.name, opusPath)
            return nil, nil, "", 0, fpsPair{}, fmt.Errorf("audio file %s has 0 packets", opusPath)
        }
        log.Printf("Station %s: Audio file %s has %d packets", st.name, opusPath, packetLines)
    }
    // Get actualDur using ffprobe on muxed mp4
    var actualDur float64
    cmdMux := exec.Command("ffmpeg", "-y", "-i", fullSegPath, "-c", "copy", tempDurMP4)
    outputMux, err := cmdMux.CombinedOutput()
    log.Printf("Station %s: FFmpeg mux output for duration check %s: %s", st.name, tempDurMP4, string(outputMux))
    if err != nil {
        errorLogger.Printf("Station %s: Mux to mp4 for duration failed for %s: %v", st.name, fullSegPath, err)
    } else {
        cmdDur := exec.Command(
            "ffprobe",
            "-v", "error",
            "-show_entries", "format=duration",
            "-of", "json",
            tempDurMP4,
        )
        outputDur, err := cmdDur.Output()
        if err == nil {
            var result struct {
                Format struct {
                    Duration string `json:"duration"`
                } `json:"format"`
            }
            if err := json.Unmarshal(outputDur, &result); err == nil && result.Format.Duration != "" {
                actualDur, err = strconv.ParseFloat(result.Format.Duration, 64)
                if err == nil {
                    log.Printf("Station %s: Actual duration from ffprobe on muxed mp4: %.3fs", st.name, actualDur)
                }
            }
        } else {
            errorLogger.Printf("Station %s: ffprobe failed on muxed mp4 %s: %v", st.name, tempDurMP4, err)
        }
        os.Remove(tempDurMP4)
    }
    if actualDur == 0 {
        // Fallback to parsing from FFmpeg output
        outputStr := string(outputMuxed)
        timeIndex := strings.LastIndex(outputStr, "time=")
        if timeIndex != -1 {
            timeStr := outputStr[timeIndex+5:]
            timeStr = timeStr[:strings.Index(timeStr, " ")]
            parts := strings.Split(timeStr, ":")
            if len(parts) == 3 {
                hh, _ := strconv.ParseFloat(parts[0], 64)
                mm, _ := strconv.ParseFloat(parts[1], 64)
                ss, _ := strconv.ParseFloat(parts[2], 64)
                actualDur = hh*3600 + mm*60 + ss
                log.Printf("Station %s: Parsed actualDur %.3fs from FFmpeg output for %s", st.name, actualDur, fullSegPath)
            }
        }
    }
    if actualDur == 0 {
        // Additional fallback to frame count
        cmdFrames := exec.Command(
            "ffprobe",
            "-v", "error",
            "-count_frames",
            "-select_streams", "v:0",
            "-show_entries", "stream=nb_read_frames",
            "-of", "default=noprint_wrappers=1:nokey=1",
            fullSegPath,
        )
        outputFrames, err := cmdFrames.Output()
        var numFrames int64
        if err == nil {
            framesStr := strings.TrimSpace(string(outputFrames))
            numFrames, err = strconv.ParseInt(framesStr, 10, 64)
            if err == nil && numFrames > 0 {
                actualDur = float64(numFrames) * float64(fpsDen) / float64(fpsNum)
                log.Printf("Station %s: Actual duration from frame count: %.3fs (%d frames at %d/%d fps)", st.name, actualDur, numFrames, fpsNum, fpsDen)
            }
        }
    }
    if actualDur == 0 {
        errorLogger.Printf("Station %s: All duration probes failed for %s, using adjustedChunkDur %.3f", st.name, fullSegPath, adjustedChunkDur)
        actualDur = adjustedChunkDur
    }
    var cmdAudioDur *exec.Cmd
    var outputAudioDur []byte
    cmdAudioDur = exec.Command(
        "ffprobe",
        "-v", "error",
        "-show_entries", "format=duration",
        "-of", "json",
        opusPath,
    )
    outputAudioDur, err = cmdAudioDur.Output()
    var audioDur float64
    if err == nil {
        var result struct {
            Format struct {
                Duration string `json:"duration"`
            } `json:"format"`
        }
        if err := json.Unmarshal(outputAudioDur, &result); err != nil {
            errorLogger.Printf("Station %s: Failed to parse audio duration JSON for %s: %v", st.name, opusPath, err)
            audioDur = actualDur
        } else if result.Format.Duration != "" {
            audioDur, err = strconv.ParseFloat(result.Format.Duration, 64)
            if err != nil {
                errorLogger.Printf("Station %s: Failed to parse audio duration for %s: %v", st.name, opusPath, err)
                audioDur = actualDur
            } else {
                log.Printf("Station %s: Audio segment %s duration: %.3fs", st.name, opusPath, audioDur)
            }
        }
    } else {
        errorLogger.Printf("Station %s: ffprobe audio duration failed for %s: %v, assuming video duration %.3fs", st.name, opusPath, err, actualDur)
        audioDur = actualDur
    }
    if math.Abs(audioDur-actualDur) > durDiffThreshold {
        log.Printf("Station %s: Audio duration %.3fs differs from video duration %.3fs, re-encoding audio", st.name, audioDur, actualDur)
        argsAudio := []string{
            "-y",
            "-err_detect", "ignore_err",
            "-analyzeduration", "100M",
            "-probesize", "100M",
        }
        if startTime > 0 {
            argsAudio = append(argsAudio, "-ss", fmt.Sprintf("%.3f", startTime))
        }
        argsAudio = append(argsAudio,
            "-i", fullEpisodePath,
            "-t", fmt.Sprintf("%.6f", actualDur), // Use more precision in -t
            "-map", "0:a:0",
            "-c:a", "libopus",
            "-b:a", "128k",
            "-ar", "48000",
            "-ac", "2",
            "-frame_duration", "20",
            "-page_duration", "960",
            "-application", "audio",
            "-vbr", "on",
            "-avoid_negative_ts", "make_zero",
            "-fflags", "+genpts",
            "-async", "1",
            "-max_delay", "0",
            "-threads", "0",
            "-f", "opus",
            opusPath,
        )
        var audioFilterReencode string
        if loudI.Valid && hasAudio && fullEpisodePath == originalPath {
            audioFilterReencode = fmt.Sprintf(
                "loudnorm=I=-23:TP=-1.5:LRA=11:measured_I=%.2f:measured_LRA=%.2f:measured_TP=%.2f:measured_thresh=%.2f:offset=0:linear=true",
                loudI.Float64, loudLRA.Float64, loudTP.Float64, loudThresh.Float64,
            )
            log.Printf("Station %s: Applying loudnorm filter to re-encoded audio for video %d: %s", st.name, videoID, audioFilterReencode)
        }
        if fadeType != "" && audioD > 0 {
            afadeType := "out"
            if fadeType == "in" {
                afadeType = "in"
            }
            afadeFilter := fmt.Sprintf("afade=t=%s:st=%.4f:d=%.4f", afadeType, audioSt, audioD)
            if audioFilterReencode != "" {
                audioFilterReencode += "," + afadeFilter
            } else {
                audioFilterReencode = afadeFilter
            }
        }
        // Add apad to force exact duration in re-encode
        padDur := actualDur - audioDur
        if padDur > 0 {
            apad := fmt.Sprintf("apad=pad_dur=%.6f", padDur)
            if audioFilterReencode != "" {
                audioFilterReencode += "," + apad
            } else {
                audioFilterReencode = apad
            }
        }
        if audioFilterReencode != "" {
            for i := len(argsAudio) - 1; i >= 0; i-- {
                if argsAudio[i] == "-c:a" {
                    argsAudio = append(argsAudio[:i], append([]string{"-af", audioFilterReencode}, argsAudio[i:]...)...)
                    break
                }
            }
        }
        cmdAudio := exec.Command("ffmpeg", argsAudio...)
        outputAudio, err := cmdAudio.CombinedOutput()
        log.Printf("Station %s: FFmpeg audio re-encode output for %s: %s", st.name, opusPath, string(outputAudio))
        if err != nil {
            errorLogger.Printf("Station %s: ffmpeg audio re-encode failed for %s: %v", st.name, opusPath, err)
            return nil, nil, "", 0, fpsPair{}, fmt.Errorf("ffmpeg audio re-encode failed for video %d at %fs: %v", videoID, startTime, err)
        }
        log.Printf("Station %s: Re-encoded audio for %s to match video duration %.3fs", st.name, opusPath, actualDur)
        audioData, err = os.ReadFile(opusPath)
        if err != nil || len(audioData) == 0 {
            errorLogger.Printf("Station %s: Re-encoded audio file %s is empty or unreadable: %v, size=%d", st.name, opusPath, err, len(audioData))
            return nil, nil, "", 0, fpsPair{}, fmt.Errorf("re-encoded audio file %s is empty or unreadable: %v", opusPath, err)
        }
    } else {
        log.Printf("Station %s: Ignoring small duration difference %.3fs for %s", st.name, math.Abs(audioDur-actualDur), opusPath)
    }
    var data []byte
    data, err = os.ReadFile(fullSegPath)
    if err != nil || len(data) == 0 {
        errorLogger.Printf("Station %s: Failed to read video segment %s: %v, size=%d", st.name, fullSegPath, err, len(data))
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("failed to read video segment %s: %v", fullSegPath, err)
    }
    nalus := splitNALUs(data)
    if len(nalus) == 0 {
        errorLogger.Printf("Station %s: No NALUs found in segment %s", st.name, fullSegPath)
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("no NALUs found in segment %s", fullSegPath)
    }
    hasIDR := false
    for _, nalu := range nalus {
        if len(nalu) > 0 && int(nalu[0]&0x1F) == 5 {
            hasIDR = true
            break
        }
    }
    spsPPS = nil
    for _, nalu := range nalus {
        if len(nalu) > 0 {
            nalType := int(nalu[0] & 0x1F)
            if nalType == 7 {
                spsPPS = append(spsPPS, nalu)
            } else if nalType == 8 && len(spsPPS) > 0 {
                spsPPS = append(spsPPS, nalu)
                break
            }
        }
    }
    if !hasIDR || (isFinalChunk && len(nalus) < int(fps*0.1)) {
        errorLogger.Printf("Station %s: %s segment %s has no IDR or insufficient frames (%d NALUs, expected ~%d), attempting repair", st.name, map[bool]string{true: "Final", false: "Segment"}[isFinalChunk], fullSegPath, len(nalus), int(fps*0.1))
        var cmdRepair *exec.Cmd
        var outputRepair []byte
        repairedSegPath := filepath.Join(tempDir, baseName+"_repaired.h264")
        keyFrameParamsRepair := fmt.Sprintf("keyint=%d:min-keyint=1:scenecut=0", gopSize)
        args := []string{
            "-y",
            "-err_detect", "ignore_err",
            "-analyzeduration", "100M",
            "-probesize", "100M",
            "-i", fullSegPath,
            "-c:v", "libx264",
            "-preset", "ultrafast",
            "-crf", "23",
            "-bf", "0",
            "-maxrate", "20M",
            "-bufsize", "40M",
            "-profile:v", "baseline",
            "-level", "5.2",
            "-pix_fmt", "yuv420p",
            "-force_fps",
            "-r", fmt.Sprintf("%d/%d", fpsNum, fpsDen),
            "-fps_mode", "cfr",
            "-force_key_frames", "expr:eq(n,0)",
            "-sc_threshold", "0",
            "-bsf:v", "h264_mp4toannexb",
            "-g", fmt.Sprintf("%d", gopSize),
            "-keyint_min", fmt.Sprintf("%d", keyintMin),
            "-x264-params", keyFrameParamsRepair,
            "-threads", "0",
            "-f", "h264",
            repairedSegPath,
        }
        if fadeType != "" && videoD > 0 {
            vfadeType := "out"
            if fadeType == "in" {
                vfadeType = "in"
            }
            vfadeFilter := fmt.Sprintf("fade=t=%s:st=%.4f:d=%.4f:color=%s", vfadeType, videoSt, videoD, color)
            insertIndex := -1
            for i, arg := range args {
                if arg == "-c:v" {
                    insertIndex = i + 2
                    break
                }
            }
            if insertIndex != -1 {
                args = append(args[:insertIndex], append([]string{"-vf", vfadeFilter}, args[insertIndex:]...)...)
            } else {
                args = append(args, "-vf", vfadeFilter)
            }
        }
        cmdRepair = exec.Command("ffmpeg", args...)
        outputRepair, err = cmdRepair.CombinedOutput()
        log.Printf("Station %s: FFmpeg repair output for %s: %s", st.name, repairedSegPath, string(outputRepair))
        if err != nil {
            errorLogger.Printf("Station %s: Failed to repair segment %s: %v", st.name, fullSegPath, err)
        } else {
            if err := os.Rename(repairedSegPath, fullSegPath); err != nil {
                errorLogger.Printf("Station %s: Failed to replace %s with repaired segment: %v", st.name, fullSegPath, err)
            } else {
                log.Printf("Station %s: Successfully repaired segment %s with IDR frames", st.name, fullSegPath)
                data, err = os.ReadFile(fullSegPath)
                if err != nil || len(data) == 0 {
                    errorLogger.Printf("Station %s: Failed to read repaired video segment %s: %v, size=%d", st.name, fullSegPath, err, len(data))
                    return nil, nil, "", 0, fpsPair{}, fmt.Errorf("failed to read repaired video segment %s: %v", fullSegPath, err)
                }
                nalus = splitNALUs(data)
                if len(nalus) == 0 {
                    errorLogger.Printf("Station %s: No NALUs found in repaired segment %s", st.name, fullSegPath)
                    return nil, nil, "", 0, fpsPair{}, fmt.Errorf("no NALUs found in repaired segment %s", fullSegPath)
                }
                hasIDR = false
                for _, nalu := range nalus {
                    if len(nalu) > 0 && int(nalu[0]&0x1F) == 5 {
                        hasIDR = true
                        break
                    }
                }
                if !hasIDR || (isFinalChunk && len(nalus) < int(fps*0.1)) {
                    errorLogger.Printf("Station %s: Repaired %s segment %s still invalid: hasIDR=%v, NALUs=%d", st.name, map[bool]string{true: "final", false: "segment"}[isFinalChunk], fullSegPath, hasIDR, len(nalus))
                    return nil, nil, "", 0, fpsPair{}, fmt.Errorf("repaired segment %s still invalid", fullSegPath)
                }
            }
        }
    }
    if len(nalus) > 0 && len(audioData) > 0 {
        log.Printf("Station %s: Chunk %s validated: %d NALUs, audio size %d bytes", st.name, fullSegPath, len(nalus), len(audioData))
    } else {
        errorLogger.Printf("Station %s: Warning: Chunk %s has %d NALUs, audio size %d bytes - proceeding but may cause issues", st.name, fullSegPath, len(nalus), len(audioData))
    }
    fmtpLine = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42c034"
    log.Printf("Station %s: Processed segment %s with %d NALUs, %d SPS/PPS, fmtp: %s, hasIDR: %v", st.name, fullSegPath, len(nalus), len(spsPPS), fmtpLine, hasIDR)
    return segments, spsPPS, fmtpLine, actualDur, fpsPair{num: fpsNum, den: fpsDen}, nil
}

func getBreakPoints(videoID int64, db *sql.DB) ([]BreakPoint, error) {
	rows, err := db.Query("SELECT value FROM video_metadata WHERE video_id = $1 AND metadata_type_id = 1", videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bps []BreakPoint
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var val interface{}
		if err := json.Unmarshal(raw, &val); err != nil {
			return nil, err
		}
		switch v := val.(type) {
		case float64:
			bps = append(bps, BreakPoint{Time: v, IsFade: false})
		case map[string]interface{}:
			if typ, ok := v["type"].(string); ok && typ == "fade" {
				timeVal, ok := v["time"].(float64)
				if !ok {
					continue // invalid
				}
				color, _ := v["color"].(string) // default ""
				fadeOutMap, _ := v["fade_out"].(map[string]interface{})
				fadeOutVideoMap, _ := fadeOutMap["video"].(map[string]interface{})
				fadeOutAudioMap, _ := fadeOutMap["audio"].(map[string]interface{})
				fadeInMap, _ := v["fade_in"].(map[string]interface{})
				fadeInVideoMap, _ := fadeInMap["video"].(map[string]interface{})
				fadeInAudioMap, _ := fadeInMap["audio"].(map[string]interface{})

				fadeOutVideoStart, _ := fadeOutVideoMap["start"].(float64)
				fadeOutVideoEnd, _ := fadeOutVideoMap["end"].(float64)
				fadeOutAudioStart, _ := fadeOutAudioMap["start"].(float64)
				fadeOutAudioEnd, _ := fadeOutAudioMap["end"].(float64)
				fadeInVideoStart, _ := fadeInVideoMap["start"].(float64)
				fadeInVideoEnd, _ := fadeInVideoMap["end"].(float64)
				fadeInAudioStart, _ := fadeInAudioMap["start"].(float64)
				fadeInAudioEnd, _ := fadeInAudioMap["end"].(float64)

				bp := BreakPoint{
					Time:   timeVal,
					IsFade: true,
					Color:  color,
					FadeOut: FadePhase{
						Video: FadeRange{Start: fadeOutVideoStart, End: fadeOutVideoEnd},
						Audio: FadeRange{Start: fadeOutAudioStart, End: fadeOutAudioEnd},
					},
					FadeIn: FadePhase{
						Video: FadeRange{Start: fadeInVideoStart, End: fadeInVideoEnd},
						Audio: FadeRange{Start: fadeInAudioStart, End: fadeInAudioEnd},
					},
				}
				bps = append(bps, bp)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(bps, func(i, j int) bool {
		return bps[i].Time < bps[j].Time
	})
	return bps, nil
}

func getVideoDur(videoID int64, db *sql.DB) float64 {
    var dur sql.NullFloat64
    err := db.QueryRow("SELECT duration FROM videos WHERE id = $1", videoID).Scan(&dur)
    if err != nil || !dur.Valid {
        log.Printf("Failed to get duration for video %d: %v", videoID, err)
        return 0
    }
    return dur.Float64
}

func loadStation(stationName string, db *sql.DB, adsEnabled bool, originalSt *Station) *Station {
    st := &Station{
        name:        stationName,
        currentIndex: 0,
        viewers:     0,
        stopCh:      make(chan struct{}),
        adsEnabled:  adsEnabled,
    }
    var unixStart int64
    var videoIds []int64
    var currentVideoID int64
    var currentVideoIndex int
    var currentOffset float64
    err := db.QueryRow("SELECT unix_start FROM stations WHERE name = $1", stationName).Scan(&unixStart)
    if err != nil {
        log.Printf("Failed to get unix_start for station %s: %v", stationName, err)
        return nil
    }
    rows, err := db.Query(
        "SELECT sv.video_id FROM station_videos sv JOIN stations s ON sv.station_id = s.id WHERE s.name = $1 ORDER BY sv.id ASC",
        stationName)
    if err != nil {
        log.Printf("Failed to query station_videos for station %s: %v", stationName, err)
        return nil
    }
    defer rows.Close()
    if originalSt != nil {
        videoIds = make([]int64, len(originalSt.videoQueue))
        copy(videoIds, originalSt.videoQueue)
        currentVideoID = originalSt.currentVideo
        currentVideoIndex = originalSt.currentIndex
        currentOffset = originalSt.currentOffset
    } else {
        videoIds = nil
        for rows.Next() {
            var vid int64
            if err := rows.Scan(&vid); err != nil {
                log.Printf("Failed to scan video_id for station %s: %v", stationName, err)
                continue
            }
            videoIds = append(videoIds, vid)
        }
        if err := rows.Err(); err != nil {
            log.Printf("Error iterating station_videos: %v", err)
            return nil
        }
        if len(videoIds) == 0 {
            log.Printf("No videos found for station %s", stationName)
            return nil
        }
        currentTime := time.Now().Unix()
        elapsedSeconds := float64(currentTime - unixStart)
        totalQueueDuration, err := getQueueDuration(videoIds, db)
        if err != nil || totalQueueDuration <= 0 {
            log.Printf("Failed to get total queue duration for station %s: %v", stationName, err)
            currentVideoIndex = 0
            currentVideoID = videoIds[0]
            currentOffset = 0.0
        } else {
            loops := int(elapsedSeconds / totalQueueDuration)
            remainingSeconds := math.Mod(elapsedSeconds, totalQueueDuration)
            currentOffset = remainingSeconds
            log.Printf("Station %s: Elapsed %f seconds, %d loops, remaining %f seconds", stationName, elapsedSeconds, loops, remainingSeconds)
            for i, vid := range videoIds {
                var hasCommercialTag bool
                err = db.QueryRow("SELECT EXISTS (SELECT 1 FROM video_tags vt WHERE vt.video_id = $1 AND vt.tag_id = 4)", vid).Scan(&hasCommercialTag)
                if err != nil {
                    log.Printf("Failed to check commercial tag for video %d: %v", vid, err)
                    continue
                }
                if hasCommercialTag {
                    continue
                }
                var duration sql.NullFloat64
                err = db.QueryRow("SELECT duration FROM videos WHERE id = $1", vid).Scan(&duration)
                if err != nil {
                    log.Printf("Failed to get duration for video %d: %v", vid, err)
                    continue
                }
                if duration.Valid && currentOffset >= duration.Float64 {
                    currentOffset -= duration.Float64
                    continue
                }
                if duration.Valid {
                    currentVideoIndex = i
                    currentVideoID = vid
                    break
                }
            }
            if currentVideoID == 0 {
                log.Printf("Station %s: No valid video found, defaulting to first video", stationName)
                currentVideoIndex = 0
                currentVideoID = videoIds[0]
                currentOffset = 0.0
            }
        }
    }
    st.videoQueue = videoIds
    st.currentVideo = currentVideoID
    st.currentIndex = currentVideoIndex
    st.currentOffset = currentOffset
    st.trackVideo, err = webrtc.NewTrackLocalStaticSample(
        webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
        fmt.Sprintf("video_%s_%t", sanitizeTrackID(stationName), adsEnabled),
        "pion",
    )
    if err != nil {
        log.Printf("Station %s: Failed to create video track: %v", stationName, err)
        return nil
    }
    st.trackAudio, err = webrtc.NewTrackLocalStaticSample(
        webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
        fmt.Sprintf("audio_%s_%t", sanitizeTrackID(stationName), adsEnabled),
        "pion",
    )
    if err != nil {
        log.Printf("Station %s: Failed to create audio track: %v", stationName, err)
        return nil
    }
    log.Printf("Station %s: Initialized at video %d (index %d) with offset %f seconds, adsEnabled: %v", stationName, currentVideoID, currentVideoIndex, currentOffset, adsEnabled)
    return st
}

func getQueueDuration(videoIDs []int64, db *sql.DB) (float64, error) {
    totalDuration := 0.0
    for _, vid := range videoIDs {
        var hasCommercialTag bool
        err := db.QueryRow(
            "SELECT EXISTS (SELECT 1 FROM video_tags vt WHERE vt.video_id = $1 AND vt.tag_id = 4)",
            vid).Scan(&hasCommercialTag)
        if err != nil {
            log.Printf("Failed to check commercial tag for video %d: %v", vid, err)
            continue
        }
        if hasCommercialTag {
            continue
        }
        var duration sql.NullFloat64
        err = db.QueryRow("SELECT duration FROM videos WHERE id = $1", vid).Scan(&duration)
        if err != nil {
            log.Printf("Failed to get duration for video %d: %v", vid, err)
            continue
        }
        if duration.Valid {
            totalDuration += duration.Float64
        } else {
            log.Printf("Video %d has null duration, skipping in duration calculation", vid)
        }
    }
    return totalDuration, nil
}

func manageProcessing(st *Station, db *sql.DB) {
    const maxRetries = 3
    const maxAdRetries = 5
    const maxFinalChunkRetries = 5
    const minChunkDur = 0.05
    const minFinalChunkDur = 0.05
    const maxChunkDur = 60.0 // New constant to cap chunk size
    for {
        select {
        case <-st.stopCh:
            log.Printf("Station %s (adsEnabled: %v): Stopping processing due to no viewers", st.name, st.adsEnabled)
            st.mu.Lock()
            st.processing = false
            for _, chunk := range st.segmentList {
                os.Remove(chunk.segPath)
                os.Remove(strings.Replace(chunk.segPath, ".h264", ".opus", 1))
            }
            st.segmentList = nil
            st.spsPPS = nil
            st.fmtpLine = ""
            st.mu.Unlock()
            return
        default:
            st.mu.Lock()
            if st.viewers == 0 {
                log.Printf("Station %s (adsEnabled: %v): No viewers, waiting", st.name, st.adsEnabled)
                st.mu.Unlock()
                time.Sleep(time.Second)
                continue
            }
            // Remove stale chunks
            for i := 0; i < len(st.segmentList); i++ {
                chunk := st.segmentList[i]
                if !chunk.isAd && chunk.videoID != st.currentVideo {
                    found := false
                    for _, vid := range st.videoQueue {
                        if vid == chunk.videoID {
                            found = true
                            break
                        }
                    }
                    if !found {
                        log.Printf("Station %s (adsEnabled: %v): Removing stale chunk %s from video %d", st.name, st.adsEnabled, chunk.segPath, chunk.videoID)
                        os.Remove(chunk.segPath)
                        os.Remove(strings.Replace(chunk.segPath, ".h264", ".opus", 1))
                        st.segmentList = append(st.segmentList[:i], st.segmentList[i+1:]...)
                        i--
                    }
                }
            }
            remainingDur := 0.0
            sumNonAd := 0.0
            for _, chunk := range st.segmentList {
                remainingDur += chunk.dur
                if !chunk.isAd && chunk.videoID == st.currentVideo {
                    sumNonAd += chunk.effective_advance
                }
            }
            if remainingDur >= BufferThreshold && len(st.segmentList) >= 4 {
                st.mu.Unlock()
                time.Sleep(time.Millisecond * 500)
                continue
            }
            log.Printf("Station %s (adsEnabled: %v): Remaining buffer %.3fs, non-ad %.3fs, current video %d, offset %.3fs", st.name, st.adsEnabled, remainingDur, sumNonAd, st.currentVideo, st.currentOffset)
            videoDur := getVideoDur(st.currentVideo, db)
            if videoDur <= 0 {
                errorLogger.Printf("Station %s: Invalid duration for video %d, advancing", st.name, st.currentVideo)
                st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                st.currentVideo = st.videoQueue[st.currentIndex]
                st.currentOffset = 0.0
                st.spsPPS = nil
                st.fmtpLine = ""
                st.currentVideoRTPTS = 0
                st.currentAudioSamples = 0
                log.Printf("Station %s (adsEnabled: %v): Transitioned to video %d with offset 0.0s due to invalid duration", st.name, st.adsEnabled, st.currentVideo)
                st.mu.Unlock()
                continue
            }
            nextStart := st.currentOffset + sumNonAd
            if nextStart >= videoDur {
                log.Printf("Station %s (adsEnabled: %v): Reached end of video %d (%.3fs >= %.3fs), advancing", st.name, st.adsEnabled, st.currentVideo, nextStart, videoDur)
                st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                st.currentVideo = st.videoQueue[st.currentIndex]
                st.currentOffset = 0.0
                st.spsPPS = nil
                st.fmtpLine = ""
                st.currentVideoRTPTS = 0
                st.currentAudioSamples = 0
                log.Printf("Station %s (adsEnabled: %v): Transitioned to video %d with offset 0.0s", st.name, st.adsEnabled, st.currentVideo)
                st.mu.Unlock()
                continue
            }
            breaks, getBreakPointsErr := getBreakPoints(st.currentVideo, db)
            if getBreakPointsErr != nil {
                errorLogger.Printf("Station %s (adsEnabled: %v): Failed to get break points for video %d: %v", st.name, st.adsEnabled, st.currentVideo, getBreakPointsErr)
                breaks = []BreakPoint{}
            }
            log.Printf("Station %s (adsEnabled: %v): Break points for video %d: %v", st.name, st.adsEnabled, st.currentVideo, breaks)
            var nextBreak *BreakPoint
            for i := range breaks {
                if breaks[i].Time > nextStart {
                    nextBreak = &breaks[i]
                    break
                }
            }
            var fadeOutStart float64 = math.MaxFloat64
            if nextBreak != nil {
                outVideoStart := nextBreak.FadeOut.Video.Start
                outAudioStart := nextBreak.FadeOut.Audio.Start
                outStartMin := math.Min(outVideoStart, outAudioStart)
                fadeOutStart = nextBreak.Time + outStartMin
            }
            distance := fadeOutStart - nextStart
            var adDurTotal float64
            if distance <= 0 && nextBreak != nil {
                // Insert break
                log.Printf("Station %s (adsEnabled: %v): Inserting ad break at %.3fs for video %d", st.name, st.adsEnabled, nextBreak.Time, st.currentVideo)
                outVideoStart := nextBreak.FadeOut.Video.Start
                outVideoEnd := nextBreak.FadeOut.Video.End
                outAudioStart := nextBreak.FadeOut.Audio.Start
                outAudioEnd := nextBreak.FadeOut.Audio.End
                outStartMin := math.Min(outVideoStart, outAudioStart)
                outEndMax := math.Max(outVideoEnd, outAudioEnd)
                outDur := outEndMax - outStartMin
                var requested_dur float64
                requested_dur = outDur
                if outDur > 0 {
                    var videoSt, videoD, audioSt, audioD float64
                    videoSt = outVideoStart - outStartMin
                    videoD = outVideoEnd - outVideoStart
                    audioSt = outAudioStart - outStartMin
                    audioD = outAudioEnd - outAudioStart
                    fadeOutStart = nextBreak.Time + outStartMin
                    adjust := 0.0
                    if fadeOutStart < nextStart {
                        adjust = nextStart - fadeOutStart
                        fadeOutStart = nextStart
                        outDur -= adjust
                        videoSt += adjust
                        audioSt += adjust
                    }
                    if fadeOutStart < 0 {
                        adjust = -fadeOutStart
                        fadeOutStart = 0
                        outDur -= adjust
                        videoSt += adjust
                        audioSt += adjust
                    }
                    if outDur <= 0 {
                        log.Printf("Station %s (adsEnabled: %v): Skipping fade out with non-positive duration after adjust (%.3fs)", st.name, st.adsEnabled, outDur)
                    } else {
                        if videoSt + videoD > outDur {
                            videoD = outDur - videoSt
                        }
                        if audioSt + audioD > outDur {
                            audioD = outDur - audioSt
                        }
                        var segments []string
                        var spsPPS [][]byte
                        var fmtpLine string
                        var actualDur float64
                        var fps fpsPair
                        var err error
                        segments, spsPPS, fmtpLine, actualDur, fps, err = processVideo(st, st.currentVideo, db, fadeOutStart, outDur, "out", videoSt, videoD, audioSt, audioD, nextBreak.Color)
                        if err != nil {
                            errorLogger.Printf("Station %s (adsEnabled: %v): Failed to process fade_out chunk: %v", st.name, st.adsEnabled, err)
                        } else if actualDur > 0 {
                            if len(segments) > 0 {
                                if len(st.spsPPS) == 0 {
                                    st.spsPPS = spsPPS
                                    st.fmtpLine = fmtpLine
                                }
                                net := outEndMax - outStartMin
                                net = math.Max(0, net)
                                effective := net
                                if requested_dur > 0 {
                                    effective = net * (actualDur / requested_dur)
                                }
                                newChunk := bufferedChunk{
                                    segPath: segments[0],
                                    dur: actualDur,
                                    isAd: false,
                                    videoID: st.currentVideo,
                                    fps: fps,
                                    effective_advance: effective,
                                }
                                st.segmentList = append(st.segmentList, newChunk)
                                remainingDur += actualDur
                                sumNonAd += effective
                                log.Printf("Station %s (adsEnabled: %v): Queued fade_out chunk at %.3fs, duration %.3fs, effective advance %.3fs", st.name, st.adsEnabled, fadeOutStart, actualDur, effective)
                            } else {
                                st.currentOffset += actualDur
                                sumNonAd += actualDur
                                log.Printf("Station %s (adsEnabled: %v): Advanced offset by negligible fade_out %.3fs without queuing", st.name, st.adsEnabled, actualDur)
                            }
                        }
                    }
                }
                if len(adIDs) == 0 {
                    errorLogger.Printf("Station %s (adsEnabled: %v): No adIDs available, skipping ad break", st.name, st.adsEnabled)
                } else {
                    availableAds := make([]int64, len(adIDs))
                    copy(availableAds, adIDs)
                    adDurTotal = 0.0
                    for i := 0; i < 3 && len(availableAds) > 0; i++ {
                        idx := rand.Intn(len(availableAds))
                        adID := availableAds[idx]
                        adDur := getVideoDur(adID, db)
                        if adDur <= 0 {
                            errorLogger.Printf("Station %s (adsEnabled: %v): Invalid duration for ad %d, skipping", st.name, st.adsEnabled, adID)
                            availableAds = append(availableAds[:idx], availableAds[idx+1:]...)
                            continue
                        }
                        var adRetryCount int
                        for adRetryCount = 0; adRetryCount < maxAdRetries; adRetryCount++ {
                            var segments []string
                            var spsPPS [][]byte
                            var fmtpLine string
                            var actualDur float64
                            var fps fpsPair
                            var err error
                            segments, spsPPS, fmtpLine, actualDur, fps, err = processVideo(st, adID, db, 0, adDur, "", 0, 0, 0, 0, "")
                            if err != nil {
                                errorLogger.Printf("Station %s (adsEnabled: %v): Failed to process ad %d (retry %d/%d): %v", st.name, st.adsEnabled, adID, adRetryCount+1, maxAdRetries, err)
                                if segments != nil && len(segments) > 0 {
                                    os.Remove(segments[0])
                                    os.Remove(strings.Replace(segments[0], ".h264", ".opus", 1))
                                }
                                time.Sleep(time.Millisecond * 500)
                                continue
                            }
                            if actualDur <= 0 {
                                errorLogger.Printf("Station %s (adsEnabled: %v): Invalid duration (%.3fs) for ad %d, retrying", st.name, st.adsEnabled, actualDur, adID)
                                if segments != nil && len(segments) > 0 {
                                    os.Remove(segments[0])
                                    os.Remove(strings.Replace(segments[0], ".h264", ".opus", 1))
                                }
                                continue
                            }
                            if len(segments) > 0 {
                                if len(st.spsPPS) == 0 {
                                    st.spsPPS = spsPPS
                                    st.fmtpLine = fmtpLine
                                }
                                adChunk := bufferedChunk{
                                    segPath: segments[0],
                                    dur: actualDur,
                                    isAd: true,
                                    videoID: adID,
                                    fps: fps,
                                    effective_advance: 0,
                                }
                                st.segmentList = append(st.segmentList, adChunk)
                                remainingDur += actualDur
                                adDurTotal += actualDur
                                log.Printf("Station %s (adsEnabled: %v): Queued ad %d with duration %.3fs at break %.3fs", st.name, st.adsEnabled, adID, actualDur, nextBreak.Time)
                            } else {
                                log.Printf("Station %s (adsEnabled: %v): Negligible ad duration %.3fs for ad %d, skipping", st.name, st.adsEnabled, actualDur, adID)
                            }
                            break
                        }
                        if adRetryCount == maxAdRetries {
                            errorLogger.Printf("Station %s (adsEnabled: %v): All %d retries failed for ad %d, skipping", st.name, st.adsEnabled, maxAdRetries, adID)
                            availableAds = append(availableAds[:idx], availableAds[idx+1:]...)
                            continue
                        }
                        availableAds = append(availableAds[:idx], availableAds[idx+1:]...)
                    }
                }
                resumePoint := nextBreak.Time + outEndMax
                inVideoStart := nextBreak.FadeIn.Video.Start
                inVideoEnd := nextBreak.FadeIn.Video.End
                inAudioStart := nextBreak.FadeIn.Audio.Start
                inAudioEnd := nextBreak.FadeIn.Audio.End
                inStartMin := math.Min(inVideoStart, inAudioStart)
                inEndMax := math.Max(inVideoEnd, inAudioEnd)
                inDur := inEndMax - inStartMin
                requested_dur = inDur
                if inDur > 0 {
                    var videoSt, videoD, audioSt, audioD float64
                    videoSt = inVideoStart - inStartMin
                    videoD = inVideoEnd - inVideoStart
                    audioSt = inAudioStart - inStartMin
                    audioD = inAudioEnd - inAudioStart
                    fadeInStart := nextBreak.Time + inStartMin
                    adjust := 0.0
                    overlap := inStartMin < outEndMax
                    if !overlap {
                        if fadeInStart < resumePoint {
                            adjust = resumePoint - fadeInStart
                            fadeInStart += adjust
                            inDur -= adjust
                            videoSt += adjust
                            audioSt += adjust
                        }
                    } // else, no adjust, duplicate content
                    if fadeInStart < 0 {
                        adjust = -fadeInStart
                        fadeInStart = 0
                        inDur -= adjust
                        videoSt += adjust
                        audioSt += adjust
                    }
                    if inDur <= 0 {
                        log.Printf("Station %s (adsEnabled: %v): Skipping fade in with non-positive duration after adjust (%.3fs)", st.name, st.adsEnabled, inDur)
                    } else {
                        if videoSt + videoD > inDur {
                            videoD = inDur - videoSt
                        }
                        if audioSt + audioD > inDur {
                            audioD = inDur - audioSt
                        }
                        var segments []string
                        var spsPPS [][]byte
                        var fmtpLine string
                        var actualDur float64
                        var fps fpsPair
                        var err error
                        segments, spsPPS, fmtpLine, actualDur, fps, err = processVideo(st, st.currentVideo, db, fadeInStart, inDur, "in", videoSt, videoD, audioSt, audioD, nextBreak.Color)
                        if err != nil {
                            errorLogger.Printf("Station %s (adsEnabled: %v): Failed to process fade_in chunk: %v", st.name, st.adsEnabled, err)
                        } else if actualDur > 0 {
                            if len(segments) > 0 {
                                if len(st.spsPPS) == 0 {
                                    st.spsPPS = spsPPS
                                    st.fmtpLine = fmtpLine
                                }
                                maxStart := math.Max(outEndMax, inStartMin)
                                net := inEndMax - maxStart
                                net = math.Max(0, net)
                                effective := net
                                if requested_dur > 0 {
                                    effective = net * (actualDur / requested_dur)
                                }
                                newChunk := bufferedChunk{
                                    segPath: segments[0],
                                    dur: actualDur,
                                    isAd: false,
                                    videoID: st.currentVideo,
                                    fps: fps,
                                    effective_advance: effective,
                                }
                                st.segmentList = append(st.segmentList, newChunk)
                                remainingDur += actualDur
                                sumNonAd += effective
                                log.Printf("Station %s (adsEnabled: %v): Queued fade_in chunk at %.3fs, duration %.3fs, effective advance %.3fs", st.name, st.adsEnabled, fadeInStart, actualDur, effective)
                            } else {
                                st.currentOffset += actualDur
                                sumNonAd += actualDur
                                log.Printf("Station %s (adsEnabled: %v): Advanced offset by negligible fade_in %.3fs without queuing", st.name, st.adsEnabled, actualDur)
                            }
                        }
                    }
                }
                if adDurTotal > 0 {
                    sumNonAd = 0.0
                    for _, c := range st.segmentList {
                        if !c.isAd && c.videoID == st.currentVideo {
                            sumNonAd += c.effective_advance
                        }
                    }
                    nextStart = st.currentOffset + sumNonAd
                }
            } else {
                // Process regular chunk
                chunkDur := ChunkDuration
                isFinalChunk := false
                if nextBreak == nil {
                    if nextStart + chunkDur >= videoDur {
                        chunkDur = videoDur - nextStart
                        isFinalChunk = true
                    }
                } else {
                    chunkDur = math.Min(chunkDur, distance)
                }
                chunkDur = math.Min(maxChunkDur, chunkDur)
                if chunkDur <= 0 {
                    st.mu.Unlock()
                    continue
                }
                if chunkDur < minChunkDur {
                    st.currentOffset += chunkDur
                    sumNonAd += chunkDur
                    log.Printf("Station %s (adsEnabled: %v): Skipped small regular chunk %.3fs for video %d at %.3fs", st.name, st.adsEnabled, chunkDur, st.currentVideo, nextStart)
                    st.mu.Unlock()
                    continue
                }
                if isFinalChunk && chunkDur < minFinalChunkDur {
                    log.Printf("Station %s (adsEnabled: %v): Skipping small final chunk (%.3fs) for video %d", st.name, st.adsEnabled, chunkDur, st.currentVideo)
                    st.currentOffset += chunkDur
                    if st.currentOffset + sumNonAd >= videoDur {
                        st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                        st.currentVideo = st.videoQueue[st.currentIndex]
                        st.currentOffset = 0.0
                        st.spsPPS = nil
                        st.fmtpLine = ""
                        st.currentVideoRTPTS = 0
                        st.currentAudioSamples = 0
                        log.Printf("Station %s (adsEnabled: %v): Transitioned to video %d with offset 0.0s due to small final skip", st.name, st.adsEnabled, st.currentVideo)
                    }
                    st.mu.Unlock()
                    continue
                }
                var retryLimit int = maxRetries
                if isFinalChunk {
                    retryLimit = maxFinalChunkRetries
                }
                var segments []string
                var spsPPS [][]byte
                var fmtpLine string
                var actualDur float64
                var fps fpsPair
                var err error
                var retryCount int
                for retryCount = 0; retryCount < retryLimit; retryCount++ {
                    segments, spsPPS, fmtpLine, actualDur, fps, err = processVideo(st, st.currentVideo, db, nextStart, chunkDur, "", 0, 0, 0, 0, "")
                    if err != nil {
                        errorLogger.Printf("Station %s (adsEnabled: %v): Failed to process %s chunk for video %d at %.3fs (retry %d/%d): %v", st.name, st.adsEnabled, map[bool]string{true: "final", false: "episode"}[isFinalChunk], st.currentVideo, nextStart, retryCount+1, retryLimit, err)
                        if segments != nil && len(segments) > 0 {
                            os.Remove(segments[0])
                            os.Remove(strings.Replace(segments[0], ".h264", ".opus", 1))
                        }
                        time.Sleep(time.Millisecond * 500)
                        continue
                    }
                    if actualDur <= 0 {
                        errorLogger.Printf("Station %s (adsEnabled: %v): Invalid duration (%.3fs) for chunk %s, retrying", st.name, st.adsEnabled, actualDur, segments[0])
                        if segments != nil && len(segments) > 0 {
                            os.Remove(segments[0])
                            os.Remove(strings.Replace(segments[0], ".h264", ".opus", 1))
                        }
                        continue
                    }
                    break
                }
                if retryCount == retryLimit {
                    errorLogger.Printf("Station %s (adsEnabled: %v): Max retries (%d) failed for chunk at %.3fs, advancing video", st.name, st.adsEnabled, retryLimit, nextStart)
                    st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                    st.currentVideo = st.videoQueue[st.currentIndex]
                    st.currentOffset = 0.0
                    st.spsPPS = nil
                    st.fmtpLine = ""
                    st.currentVideoRTPTS = 0
                    st.currentAudioSamples = 0
                    log.Printf("Station %s (adsEnabled: %v): Transitioned to video %d with offset 0.0s due to failed chunk", st.name, st.adsEnabled, st.currentVideo)
                    st.mu.Unlock()
                    continue
                }
                if err == nil && actualDur > 0 {
                    if len(segments) > 0 {
                        if len(st.spsPPS) == 0 {
                            st.spsPPS = spsPPS
                            st.fmtpLine = fmtpLine
                        }
                        newChunk := bufferedChunk{
                            segPath: segments[0],
                            dur: actualDur,
                            isAd: false,
                            videoID: st.currentVideo,
                            fps: fps,
                            effective_advance: actualDur,
                        }
                        st.segmentList = append(st.segmentList, newChunk)
                        remainingDur += actualDur
                        sumNonAd += actualDur
                        log.Printf("Station %s (adsEnabled: %v): Queued %s chunk for video %d at %.3fs, duration %.3fs", st.name, st.adsEnabled, map[bool]string{true: "final", false: "episode"}[isFinalChunk], st.currentVideo, nextStart, actualDur)
                    } else {
                        st.currentOffset += actualDur
                        sumNonAd += actualDur
                        log.Printf("Station %s (adsEnabled: %v): Advanced offset by negligible %.3fs without queuing chunk for video %d at %.3fs", st.name, st.adsEnabled, actualDur, st.currentVideo, nextStart)
                        if st.currentOffset >= videoDur {
                            st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                            st.currentVideo = st.videoQueue[st.currentIndex]
                            st.currentOffset = 0.0
                            st.spsPPS = nil
                            st.fmtpLine = ""
                            st.currentVideoRTPTS = 0
                            st.currentAudioSamples = 0
                            log.Printf("Station %s (adsEnabled: %v): Transitioned to video %d with offset 0.0s due to negligible advance", st.name, st.adsEnabled, st.currentVideo)
                        }
                    }
                }
            }
            log.Printf("Station %s (adsEnabled: %v): Buffer check complete, remainingDur %.3fs, segmentList: %v", st.name, st.adsEnabled, remainingDur, st.segmentList)
            st.mu.Unlock()
            time.Sleep(time.Millisecond * 500)
        }
    }
}

func sender(st *Station, db *sql.DB) {
    st.mu.Lock()
    currentVideoTS := st.currentVideoRTPTS
    currentAudioTS := st.currentAudioSamples
    st.mu.Unlock()
    for {
        select {
        case <-st.stopCh:
            log.Printf("Station %s (adsEnabled: %v): Stopping sender due to no viewers", st.name, st.adsEnabled)
            return
        default:
            st.mu.Lock()
            if st.viewers == 0 || len(st.segmentList) == 0 {
                log.Printf("Station %s (adsEnabled: %v): No viewers or empty segment list, waiting (segmentList: %v)", st.name, st.adsEnabled, st.segmentList)
                st.mu.Unlock()
                time.Sleep(time.Second)
                continue
            }
            // Ensure stopCh is not closed
            select {
            case <-st.stopCh:
                st.stopCh = make(chan struct{})
                log.Printf("Station %s (adsEnabled: %v): Reinitialized stopCh due to previous closure", st.name, st.adsEnabled)
            default:
            }
            chunk := st.segmentList[0]
            videoDur := getVideoDur(st.currentVideo, db)
            sumNonAd := 0.0
            for _, c := range st.segmentList {
                if !c.isAd && c.videoID == st.currentVideo {
                    sumNonAd += c.effective_advance
                }
            }
            isFinalChunk := !chunk.isAd && (st.currentOffset+sumNonAd >= videoDur || math.Abs(st.currentOffset+sumNonAd-videoDur) < 0.001)
            if !chunk.isAd && chunk.videoID != st.currentVideo {
                found := false
                for _, vid := range st.videoQueue {
                    if vid == chunk.videoID {
                        found = true
                        break
                    }
                }
                if !found {
                    errorLogger.Printf("Station %s (adsEnabled: %v): Discarding stale non-ad chunk from video %d (current video %d, segment: %s)", st.name, st.adsEnabled, chunk.videoID, st.currentVideo, chunk.segPath)
                    os.Remove(chunk.segPath)
                    os.Remove(strings.Replace(chunk.segPath, ".h264", ".opus", 1))
                    st.segmentList = st.segmentList[1:]
                    log.Printf("Station %s (adsEnabled: %v): Removed stale chunk %s, new segmentList: %v", st.name, st.adsEnabled, chunk.segPath, st.segmentList)
                    st.mu.Unlock()
                    continue
                }
            }
            segPath := chunk.segPath
            fpsNum := chunk.fps.num
            fpsDen := chunk.fps.den
            fps := float64(fpsNum) / float64(fpsDen)
            log.Printf("Station %s (adsEnabled: %v): Processing chunk %d/%d: segPath=%s, videoID=%d, isAd=%v, dur=%.3fs, effective_advance=%.3fs, fps=%d/%d", st.name, st.adsEnabled, 1, len(st.segmentList), chunk.segPath, chunk.videoID, chunk.isAd, chunk.dur, chunk.effective_advance, fpsNum, fpsDen)
            st.mu.Unlock()
            data, err := os.ReadFile(segPath)
            if err != nil || len(data) == 0 {
                errorLogger.Printf("Station %s (adsEnabled: %v): %s segment %s read error: %v", st.name, st.adsEnabled, map[bool]string{true: "Final", false: "Segment"}[isFinalChunk], segPath, err)
                st.mu.Lock()
                os.Remove(segPath) // Clean up even if missing
                os.Remove(strings.Replace(segPath, ".h264", ".opus", 1))
                st.segmentList = st.segmentList[1:]
                st.mu.Unlock()
                continue
            }
            nalus := splitNALUs(data)
            if len(nalus) == 0 {
                errorLogger.Printf("Station %s (adsEnabled: %v): No NALUs found in segment %s", st.name, st.adsEnabled, segPath)
                st.mu.Lock()
                os.Remove(segPath)
                os.Remove(strings.Replace(segPath, ".h264", ".opus", 1))
                st.segmentList = st.segmentList[1:]
                st.mu.Unlock()
                continue
            }
            chunkSpsPPS := [][]byte{}
            for _, nalu := range nalus {
                if len(nalu) > 0 {
                    nalType := int(nalu[0] & 0x1F)
                    if nalType == 7 || nalType == 8 {
                        chunkSpsPPS = append(chunkSpsPPS, nalu)
                        if nalType == 8 && len(chunkSpsPPS) >= 2 {
                            break
                        }
                    }
                }
            }
            if len(chunkSpsPPS) < 2 {
                chunkSpsPPS = st.spsPPS
                log.Printf("Station %s (adsEnabled: %v): Using station SPS/PPS for chunk %s", st.name, st.adsEnabled, segPath)
            }
            testSample := media.Sample{Data: []byte{}, Duration: time.Duration(0)}
            if err := st.trackVideo.WriteSample(testSample); err != nil {
                if strings.Contains(err.Error(), "not bound") {
                    errorLogger.Printf("Station %s (adsEnabled: %v): Track not bound, waiting for negotiation...", st.name, st.adsEnabled)
                    time.Sleep(500 * time.Millisecond)
                    continue
                } else {
                    errorLogger.Printf("Station %s (adsEnabled: %v): Video track write test error for %s: %v", st.name, st.adsEnabled, segPath, err)
                    st.mu.Lock()
                    if st.viewers == 0 {
                        close(st.stopCh)
                        st.stopCh = make(chan struct{})
                    }
                    newTrackVideo, err2 := webrtc.NewTrackLocalStaticSample(
                        webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
                        fmt.Sprintf("video_%s_%t", sanitizeTrackID(st.name), st.adsEnabled),
                        "pion",
                    )
                    if err2 != nil {
                        errorLogger.Printf("Station %s (adsEnabled: %v): Failed to reinitialize video track: %v", st.name, st.adsEnabled, err2)
                        st.viewers = 0
                        close(st.stopCh)
                        st.stopCh = make(chan struct{})
                        st.mu.Unlock()
                        return
                    }
                    st.trackVideo = newTrackVideo
                    log.Printf("Station %s (adsEnabled: %v): Reinitialized video track", st.name, st.adsEnabled)
                    st.mu.Unlock()
                    continue // Continue to try sending the chunk with new track
                }
            }
            audioPath := strings.Replace(segPath, ".h264", ".opus", 1)
            audioData, err := os.ReadFile(audioPath)
            if err != nil {
                errorLogger.Printf("Station %s (adsEnabled: %v): Failed to read audio %s: %v", st.name, st.adsEnabled, audioPath, err)
                st.mu.Lock()
                os.Remove(segPath)
                os.Remove(audioPath)
                st.segmentList = st.segmentList[1:]
                st.mu.Unlock()
                continue
            }
            var transmissionWG sync.WaitGroup
            transmissionWG.Add(2)
            go func(nalus [][]byte, startTS uint32) {
                defer transmissionWG.Done()
                var allNALUs [][]byte
                if len(chunkSpsPPS) > 0 {
                    allNALUs = append(chunkSpsPPS, nalus...)
                    log.Printf("Station %s (adsEnabled: %v): Prefixed %d SPS/PPS NALUs to %s", st.name, st.adsEnabled, len(chunkSpsPPS), segPath)
                } else {
                    allNALUs = nalus
                }
                var frames [][]byte
                var currentFrame [][]byte
                var hasVCL bool
                for _, nalu := range allNALUs {
                    if len(nalu) == 0 {
                        continue
                    }
                    nalType := int(nalu[0] & 0x1F)
                    isVCL := nalType >= 1 && nalType <= 5
                    if hasVCL && !isVCL {
                        var frameData bytes.Buffer
                        for _, n := range currentFrame {
                            frameData.Write([]byte{0x00, 0x00, 0x00, 0x01})
                            frameData.Write(n)
                        }
                        frames = append(frames, frameData.Bytes())
                        currentFrame = [][]byte{nalu}
                        hasVCL = false
                    } else {
                        if isVCL {
                            firstMb, err := getFirstMbInSlice(nalu)
                            if err != nil {
                                errorLogger.Printf("Station %s (adsEnabled: %v): Failed to parse first_mb_in_slice for NALU in %s: %v", st.name, st.adsEnabled, segPath, err)
                                continue
                            }
                            if firstMb == 0 && len(currentFrame) > 0 && hasVCL {
                                var frameData bytes.Buffer
                                for _, n := range currentFrame {
                                    frameData.Write([]byte{0x00, 0x00, 0x00, 0x01})
                                    frameData.Write(n)
                                }
                                frames = append(frames, frameData.Bytes())
                                currentFrame = nil
                                hasVCL = false
                            }
                            currentFrame = append(currentFrame, nalu)
                            hasVCL = true
                        } else {
                            currentFrame = append(currentFrame, nalu)
                        }
                    }
                }
                if len(currentFrame) > 0 {
                    var frameData bytes.Buffer
                    for _, n := range currentFrame {
                        frameData.Write([]byte{0x00, 0x00, 0x00, 0x01})
                        frameData.Write(n)
                    }
                    frames = append(frames, frameData.Bytes())
                }
                if len(frames) == 0 {
                    errorLogger.Printf("Station %s (adsEnabled: %v): No frames in segment %s", st.name, st.adsEnabled, segPath)
                    st.mu.Lock()
                    st.currentVideoRTPTS = currentVideoTS // Maintain continuity
                    st.mu.Unlock()
                    return
                }
                expectedFrames := int(math.Round(chunk.dur * fps))
                if expectedFrames == 0 {
                    expectedFrames = 1
                }
                actualFrames := len(frames)
                frameIntervalSeconds := chunk.dur / float64(max(1, actualFrames))
                frameInterval := time.Duration(frameIntervalSeconds * float64(time.Second))
                log.Printf("Station %s (adsEnabled: %v): %s segment %s, expected %d frames, actual %d frames, interval %.3fs", st.name, st.adsEnabled, map[bool]string{true: "Final", false: "Segment"}[isFinalChunk], segPath, expectedFrames, actualFrames, frameIntervalSeconds)
                videoTimestamp := startTS
                const videoClockRate = 90000
                boundChecked := false
                frameIdx := 0
                expectedSamples := uint32(chunk.dur * float64(videoClockRate))
                if actualFrames > 0 {
                    if !boundChecked {
                        if err := st.trackVideo.WriteSample(testSample); err != nil {
                            errorLogger.Printf("Station %s (adsEnabled: %v): Video track not bound for frame %d in %s: %v", st.name, st.adsEnabled, frameIdx, segPath, err)
                            st.mu.Lock()
                            st.currentVideoRTPTS = currentVideoTS
                            st.mu.Unlock()
                            return
                        }
                        boundChecked = true
                    }
                    sample := media.Sample{
                        Data: frames[frameIdx],
                        Duration: frameInterval,
                        PacketTimestamp: videoTimestamp,
                    }
                    if err := st.trackVideo.WriteSample(sample); err != nil {
                        errorLogger.Printf("Station %s (adsEnabled: %v): Video sample %d write error for %s: %v", st.name, st.adsEnabled, frameIdx, segPath, err)
                        st.mu.Lock()
                        st.currentVideoRTPTS = currentVideoTS
                        st.mu.Unlock()
                        return
                    }
                    videoTimestamp += uint32(frameIntervalSeconds * float64(videoClockRate))
                    frameIdx++
                    log.Printf("Station %s (adsEnabled: %v): Sent first video frame immediately for %s", st.name, st.adsEnabled, segPath)
                }
                if frameIdx < actualFrames {
                    ticker := time.NewTicker(frameInterval)
                    defer ticker.Stop()
                    for range ticker.C {
                        if frameIdx >= actualFrames {
                            break
                        }
                        sample := media.Sample{
                            Data: frames[frameIdx],
                            Duration: frameInterval,
                            PacketTimestamp: videoTimestamp,
                        }
                        if err := st.trackVideo.WriteSample(sample); err != nil {
                            errorLogger.Printf("Station %s (adsEnabled: %v): Video sample %d write error for %s: %v", st.name, st.adsEnabled, frameIdx, segPath, err)
                            st.mu.Lock()
                            st.currentVideoRTPTS = currentVideoTS
                            st.mu.Unlock()
                            return
                        }
                        videoTimestamp += uint32(frameIntervalSeconds * float64(videoClockRate))
                        frameIdx++
                        if isFinalChunk && videoTimestamp >= expectedSamples {
                            break
                        }
                    }
                }
                st.mu.Lock()
                st.currentVideoRTPTS = videoTimestamp
                st.mu.Unlock()
                log.Printf("Station %s (adsEnabled: %v): Completed video transmission for %s, final videoTS=%d", st.name, st.adsEnabled, segPath, videoTimestamp)
            }(nalus, currentVideoTS)
            go func(audioData []byte, startTS uint32) {
                defer transmissionWG.Done()
                const sampleRate = 48000
                audioTimestamp := startTS
                if len(audioData) == 0 {
                    log.Printf("Station %s (adsEnabled: %v): No audio data for %s, skipping audio transmission", st.name, st.adsEnabled, segPath)
                    return
                }
                reader := bytes.NewReader(audioData)
                ogg, _, err := oggreader.NewWith(reader)
                if err != nil {
                    errorLogger.Printf("Station %s (adsEnabled: %v): Failed to create ogg reader for %s: %v", st.name, st.adsEnabled, segPath, err)
                    return
                }
                var prevGranule uint64 = 0
                boundChecked := false
                testSample := media.Sample{Data: []byte{}, Duration: time.Duration(0)}
                packetIdx := 0
                expectedSamples := uint32(chunk.dur * float64(sampleRate))
                cumulTime := time.Duration(0)
                startTime := time.Now()
                for {
                    payload, header, err := ogg.ParseNextPage()
                    if err == io.EOF {
                        break
                    }
                    if err != nil {
                        errorLogger.Printf("Station %s (adsEnabled: %v): Failed to parse ogg page for %s: %v", st.name, st.adsEnabled, segPath, err)
                        return
                    }
                    if len(payload) < 1 || (len(payload) >= 8 && (string(payload[:8]) == "OpusHead" || string(payload[:8]) == "OpusTags")) {
                        log.Printf("Station %s (adsEnabled: %v): Skipping header packet: %s", st.name, st.adsEnabled, string(payload[:8]))
                        continue
                    }
                    durSamples := header.GranulePosition - prevGranule
                    if durSamples == 0 {
                        log.Printf("Station %s (adsEnabled: %v): Zero-duration audio packet %d for %s, skipping", st.name, st.adsEnabled, packetIdx, segPath)
                        packetIdx++
                        continue
                    }
                    pktDur := time.Duration(durSamples * 1000000000 / uint64(sampleRate)) * time.Nanosecond
                    // Pace: sleep until target time for this packet's start
                    targetTime := startTime.Add(cumulTime)
                    for time.Now().Before(targetTime) {
                        time.Sleep(targetTime.Sub(time.Now()))
                    }
                    if !boundChecked {
                        if err := st.trackAudio.WriteSample(testSample); err != nil {
                            errorLogger.Printf("Station %s (adsEnabled: %v): Audio track not bound for sample %d in %s: %v", st.name, st.adsEnabled, packetIdx, segPath, err)
                            return
                        }
                        boundChecked = true
                    }
                    sample := media.Sample{
                        Data: payload,
                        Duration: pktDur,
                        PacketTimestamp: audioTimestamp,
                    }
                    if err := st.trackAudio.WriteSample(sample); err != nil {
                        errorLogger.Printf("Station %s (adsEnabled: %v): Audio sample %d write error for %s: %v", st.name, st.adsEnabled, packetIdx, segPath, err)
                        st.mu.Lock()
                        st.currentAudioSamples = audioTimestamp
                        st.mu.Unlock()
                        return
                    }
                    audioTimestamp += uint32(durSamples)
                    prevGranule = header.GranulePosition
                    cumulTime += pktDur
                    packetIdx++
                    if isFinalChunk && audioTimestamp >= expectedSamples {
                        break
                    }
                }
                st.mu.Lock()
                st.currentAudioSamples = audioTimestamp
                st.mu.Unlock()
                log.Printf("Station %s (adsEnabled: %v): Completed audio transmission for %s, final audioTS=%d", st.name, st.adsEnabled, segPath, audioTimestamp)
            }(audioData, currentAudioTS)
            // Monitor with timeout
            doneCh := make(chan struct{})
            go func() {
                transmissionWG.Wait()
                close(doneCh)
            }()
            select {
            case <-doneCh:
                log.Printf("Station %s (adsEnabled: %v): Transmission completed normally for %s", st.name, st.adsEnabled, segPath)
            case <-time.After(time.Duration(chunk.dur*float64(time.Second)) + 20*time.Second):
                errorLogger.Printf("Station %s (adsEnabled: %v): Timeout waiting for audio/video transmission for %s", st.name, st.adsEnabled, segPath)
            }
            st.mu.Lock()
            os.Remove(segPath)
            os.Remove(audioPath)
            if !chunk.isAd && chunk.videoID == st.currentVideo {
                st.currentOffset += chunk.effective_advance
                log.Printf("Station %s (adsEnabled: %v): Updated offset to %.3fs for video %d after successful transmission (effective advance %.3fs)", st.name, st.adsEnabled, st.currentOffset, st.currentVideo, chunk.effective_advance)
                if videoDur > 0 && (st.currentOffset >= videoDur || math.Abs(st.currentOffset-videoDur) < 0.001) {
                    log.Printf("Station %s (adsEnabled: %v): Completed video %d, advancing to next", st.name, st.adsEnabled, st.currentVideo)
                    st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                    st.currentVideo = st.videoQueue[st.currentIndex]
                    st.currentOffset = 0.0
                    st.spsPPS = nil
                    st.fmtpLine = ""
                    st.segmentList = []bufferedChunk{}
                    log.Printf("Station %s (adsEnabled: %v): Transitioned to video %d with offset 0.0s", st.name, st.adsEnabled, st.currentVideo)
                }
            }
            st.segmentList = st.segmentList[1:]
            log.Printf("Station %s (adsEnabled: %v): Removed %s chunk %s, new segmentList: %v", st.name, st.adsEnabled, map[bool]string{true: "final", false: "chunk"}[isFinalChunk], segPath, st.segmentList)
            currentVideoTS = st.currentVideoRTPTS
            currentAudioTS = st.currentAudioSamples
            st.mu.Unlock()
        }
    }
}

func min(a, b int) int {
    if a < b {
        return a
    }
    return b
}

func splitNALUs(data []byte) [][]byte {
    if len(data) == 0 {
        return nil
    }
    var nalus [][]byte
    start := 0
    i := 0
    for i < len(data) {
        if i+3 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
            if start < i {
                nalus = append(nalus, data[start:i])
            }
            start = i + 4
            i += 4
            continue
        }
        if i+2 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
            if start < i {
                nalus = append(nalus, data[start:i])
            }
            start = i + 3
            i += 3
            continue
        }
        i++
    }
    if start < len(data) {
        nalus = append(nalus, data[start:])
    }
    return nalus
}

func signalingHandler(db *sql.DB, c *gin.Context) {
    stationName := c.Query("station")
    if stationName == "" {
        stationName = DefaultStation
    }
    adsEnabled := c.Query("adsEnabled") != "false"
    mu.Lock()
    var st *Station
    var ok bool
    if adsEnabled {
        st, ok = stations[stationName]
        if !ok {
            st = loadStation(stationName, db, true, nil)
            if st == nil {
                mu.Unlock()
                c.JSON(400, gin.H{"error": "Invalid station"})
                return
            }
            stations[stationName] = st
        }
    } else {
        st, ok = noAdsStations[stationName]
        if !ok {
            originalSt, origOk := stations[stationName]
            if !origOk {
                originalSt = loadStation(stationName, db, true, nil)
                if originalSt == nil {
                    mu.Unlock()
                    c.JSON(400, gin.H{"error": "Base station does not exist"})
                    return
                }
                stations[stationName] = originalSt
            }
            st = loadStation(stationName, db, false, originalSt)
            if st == nil {
                mu.Unlock()
                c.JSON(400, gin.H{"error": "Failed to create no-ads station"})
                return
            }
            noAdsStations[stationName] = st
            log.Printf("Created no-ads station for %s", stationName)
        }
    }
    mu.Unlock()
    log.Printf("Signaling for station %s, adsEnabled: %v", stationName, adsEnabled)
    var msg struct {
        Type string `json:"type"`
        SDP string `json:"sdp,omitempty"`
    }
    if err := c.BindJSON(&msg); err != nil {
        log.Printf("JSON bind error: %v", err)
        c.JSON(400, gin.H{"error": err.Error()})
        return
    }
    m := &webrtc.MediaEngine{}
    if err := m.RegisterCodec(webrtc.RTPCodecParameters{
        RTPCodecCapability: webrtc.RTPCodecCapability{
            MimeType: webrtc.MimeTypeH264,
            ClockRate: 90000,
            SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42c034",
            RTCPFeedback: []webrtc.RTCPFeedback{{"goog-remb", ""}, {"ccm", "fir"}, {"nack", ""}, {"nack", "pli"}},
        },
        PayloadType: 96,
    }, webrtc.RTPCodecTypeVideo); err != nil {
        log.Printf("RegisterCodec video error: %v", err)
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }
    if err := m.RegisterCodec(webrtc.RTPCodecParameters{
        RTPCodecCapability: webrtc.RTPCodecCapability{
            MimeType: webrtc.MimeTypeOpus,
            ClockRate: 48000,
            Channels: 2,
            SDPFmtpLine: "minptime=10;useinbandfec=1;stereo=1",
        },
        PayloadType: 111,
    }, webrtc.RTPCodecTypeAudio); err != nil {
        log.Printf("RegisterCodec audio error: %v", err)
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }
    s := webrtc.SettingEngine{}
    s.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeTCP4})
    api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(s))
    pc, err := api.NewPeerConnection(webrtc.Configuration{
        ICEServers: []webrtc.ICEServer{
            {URLs: []string{"stun:stun.l.google.com:19302"}},
            {URLs: []string{"turn:openrelay.metered.ca:80"}, Username: "openrelayproject", Credential: "openrelayproject"},
            {URLs: []string{"turn:openrelay.metered.ca:443"}, Username: "openrelayproject", Credential: "openrelayproject"},
        },
    })
    if err != nil {
        log.Printf("NewPeerConnection error: %v", err)
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }
    st.mu.Lock()
    st.viewers++
    startGoroutines := false
    if st.viewers == 1 {
        startGoroutines = true
        st.stopCh = make(chan struct{})
        st.processing = true
        if err := os.MkdirAll(HlsDir, 0755); err != nil {
            log.Printf("Station %s: Failed to create webrtc_segments directory: %v", st.name, err)
            st.viewers--
            st.mu.Unlock()
            if startGoroutines {
                go manageProcessing(st, db)
                go sender(st, db)
            }
            c.JSON(500, gin.H{"error": "Failed to create webrtc_segments directory"})
            pc.Close()
            return
        }
        // Removed synchronous initial build loop; manageProcessing will handle it asynchronously
    }
    st.mu.Unlock()
    if startGoroutines {
        go manageProcessing(st, db)
        go sender(st, db)
    }
    pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
        log.Printf("Station %s: ICE state: %s", stationName, state.String())
    })
    if msg.Type == "offer" {
        offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: msg.SDP}
        if err := pc.SetRemoteDescription(offer); err != nil {
            log.Printf("SetRemoteDescription error: %v", err)
            st.mu.Lock()
            st.viewers--
            if st.viewers == 0 {
                close(st.stopCh)
                st.stopCh = make(chan struct{})
            }
            st.mu.Unlock()
            c.JSON(500, gin.H{"error": err.Error()})
            pc.Close()
            return
        }
        if _, err = pc.AddTrack(st.trackVideo); err != nil {
            log.Printf("AddTrack video error: %v", err)
            st.mu.Lock()
            st.viewers--
            if st.viewers == 0 {
                close(st.stopCh)
                st.stopCh = make(chan struct{})
            }
            st.mu.Unlock()
            c.JSON(500, gin.H{"error": err.Error()})
            pc.Close()
            return
        }
        if _, err = pc.AddTrack(st.trackAudio); err != nil {
            log.Printf("AddTrack audio error: %v", err)
            st.mu.Lock()
            st.viewers--
            if st.viewers == 0 {
                close(st.stopCh)
                st.stopCh = make(chan struct{})
            }
            st.mu.Unlock()
            c.JSON(500, gin.H{"error": err.Error()})
            pc.Close()
            return
        }
        log.Printf("Station %s: Tracks added", stationName)
        answer, err := pc.CreateAnswer(nil)
        if err != nil {
            log.Printf("CreateAnswer error: %v", err)
            st.mu.Lock()
            st.viewers--
            if st.viewers == 0 {
                close(st.stopCh)
                st.stopCh = make(chan struct{})
            }
            st.mu.Unlock()
            c.JSON(500, gin.H{"error": err.Error()})
            pc.Close()
            return
        }
        gatherComplete := webrtc.GatheringCompletePromise(pc)
        if err := pc.SetLocalDescription(answer); err != nil {
            log.Printf("SetLocalDescription error: %v", err)
            st.mu.Lock()
            st.viewers--
            if st.viewers == 0 {
                close(st.stopCh)
                st.stopCh = make(chan struct{})
            }
            st.mu.Unlock()
            c.JSON(500, gin.H{"error": err.Error()})
            pc.Close()
            return
        }
        <-gatherComplete
        log.Printf("Station %s: SDP Answer: %s", stationName, pc.LocalDescription().SDP)
        c.JSON(200, gin.H{"type": "answer", "sdp": pc.LocalDescription().SDP})
    }
    pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
        log.Printf("Station %s: PC state: %s", stationName, s.String())
        if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateDisconnected {
            st.mu.Lock()
            st.viewers--
            if st.viewers == 0 {
                close(st.stopCh)
                st.stopCh = make(chan struct{})
                mu.Lock()
                if !st.adsEnabled {
                    delete(noAdsStations, stationName)
                    log.Printf("Removed no-ads station %s due to no viewers", stationName)
                } else {
                    delete(stations, stationName)
                    log.Printf("Removed station %s due to no viewers", stationName)
                }
                mu.Unlock()
            }
            st.mu.Unlock()
            if err := pc.Close(); err != nil {
                log.Printf("Failed to close PC: %v", err)
            }
        }
    })
}

func indexHandler(c *gin.Context) {
    var html string = ""
    c.Header("Content-Type", "text/html")
    c.String(200, html)
}

func updateVideoDurations(db *sql.DB) error {
    if videoBaseDir == "" {
        return fmt.Errorf("videoBaseDir is not set, cannot process video files")
    }
    normalizedDir := "./normalized"
    if err := os.MkdirAll(normalizedDir, 0755); err != nil {
        return fmt.Errorf("failed to create normalized directory: %v", err)
    }
    rows, err := db.Query(`
        SELECT id, uri FROM videos 
        WHERE (duration IS NULL OR duration = 0 OR loudnorm_input_i IS NULL OR loudnorm_input_i = 0)
    `)
    if err != nil {
        return fmt.Errorf("failed to query videos with NULL or 0 duration or loudnorm: %v", err)
    }
    defer rows.Close()
    var videoIDs []int64
    var uris []string
    for rows.Next() {
        var id int64
        var uri string
        if err := rows.Scan(&id, &uri); err != nil {
            log.Printf("Failed to scan video ID and URI: %v", err)
            continue
        }
        videoIDs = append(videoIDs, id)
        uris = append(uris, uri)
    }
    if err := rows.Err(); err != nil {
        return fmt.Errorf("error iterating videos: %v", err)
    }
    if len(videoIDs) == 0 {
        log.Println("No videos with NULL/0 duration or loudnorm found")
        return nil
    }
    log.Printf("Found %d videos with NULL/0 duration or loudnorm, calculating metadata", len(videoIDs))
    const maxConcurrent = 5
    semaphore := make(chan struct{}, maxConcurrent)
    var wg sync.WaitGroup
    var mu sync.Mutex
    var errors []error
    for i, videoID := range videoIDs {
        wg.Add(1)
        semaphore <- struct{}{}
        go func(id int64, uri string) {
            defer wg.Done()
            defer func() { <-semaphore }()
            fullPath := filepath.Join(videoBaseDir, uri)
            if _, err := os.Stat(fullPath); err != nil {
                mu.Lock()
                errors = append(errors, fmt.Errorf("video %d file not found at %s: %v", id, fullPath, err))
                mu.Unlock()
                return
            }
            var duration sql.NullFloat64
            err := db.QueryRow("SELECT duration FROM videos WHERE id = $1", id).Scan(&duration)
            if err != nil || !duration.Valid || duration.Float64 == 0 {
                cmd := exec.Command(
                    "ffprobe",
                    "-v", "error",
                    "-show_entries", "format=duration",
                    "-of", "default=noprint_wrappers=1:nokey=1",
                    fullPath,
                )
                output, err := cmd.Output()
                if err != nil {
                    mu.Lock()
                    errors = append(errors, fmt.Errorf("ffprobe failed for video %d (%s): %v", id, fullPath, err))
                    mu.Unlock()
                    return
                }
                durationStr := strings.TrimSpace(string(output))
                durationFloat, err := strconv.ParseFloat(durationStr, 64)
                if err != nil {
                    mu.Lock()
                    errors = append(errors, fmt.Errorf("failed to parse duration for video %d (%s): %v", id, fullPath, err))
                    mu.Unlock()
                    return
                }
                _, err = db.Exec("UPDATE videos SET duration = $1 WHERE id = $2", durationFloat, id)
                if err != nil {
                    mu.Lock()
                    errors = append(errors, fmt.Errorf("failed to update duration for video %d (%s): %v", id, fullPath, err))
                    mu.Unlock()
                    return
                }
                log.Printf("Updated duration for video %d (%s) to %.2f seconds", id, fullPath, durationFloat)
            }
            var needsLoudnorm bool
            err = db.QueryRow("SELECT loudnorm_input_i IS NULL OR loudnorm_input_i = 0 FROM videos WHERE id = $1", id).Scan(&needsLoudnorm)
            if err != nil {
                mu.Lock()
                errors = append(errors, fmt.Errorf("failed to check loudnorm for video %d (%s): %v", id, fullPath, err))
                mu.Unlock()
                return
            }
            if needsLoudnorm {
                // Check for audio stream
                cmdProbe := exec.Command(
                    "ffprobe",
                    "-v", "error",
                    "-select_streams", "a:0",
                    "-show_entries", "stream=index",
                    "-of", "default=noprint_wrappers=1:nokey=1",
                    fullPath,
                )
                outputProbe, err := cmdProbe.CombinedOutput()
                outputStrProbe := strings.TrimSpace(string(outputProbe))
                log.Printf("ffprobe output for video %d (%s): %q, error: %v", id, fullPath, outputStrProbe, err)
                if err != nil || outputStrProbe == "" {
                    log.Printf("No audio stream in video %d (%s), setting sentinel loudnorm values", id, fullPath)
                    _, err = db.Exec(
                        "UPDATE videos SET loudnorm_input_i = 0, loudnorm_input_lra = 0, loudnorm_input_tp = 0, loudnorm_input_thresh = 0 WHERE id = $1",
                        id,
                    )
                    if err != nil {
                        mu.Lock()
                        errors = append(errors, fmt.Errorf("failed to update sentinel loudnorm for video %d (%s): %v", id, fullPath, err))
                        mu.Unlock()
                    } else {
                        log.Printf("Set sentinel loudnorm values (0) for video %d (%s)", id, fullPath)
                    }
                    return
                }
                log.Printf("Audio stream detected in video %d (%s), calculating loudnorm", id, fullPath)
                cmdLoudnorm := exec.Command(
                    "ffmpeg",
                    "-err_detect", "ignore_err",
                    "-analyzeduration", "100M",
                    "-probesize", "100M",
                    "-i", fullPath,
                    "-af", "loudnorm=print_format=json",
                    "-vn", "-f", "null", "-",
                )
                outputLoudnorm, err := cmdLoudnorm.CombinedOutput()
                if err != nil {
                    mu.Lock()
                    errors = append(errors, fmt.Errorf("ffmpeg loudnorm failed for video %d (%s): %v\nOutput: %s", id, fullPath, err, string(outputLoudnorm)))
                    mu.Unlock()
                    return
                }
                outputStr := string(outputLoudnorm)
                log.Printf("Raw loudnorm output for video %d (%s):\n%s", id, fullPath, outputStr)
                jsonStart := strings.LastIndex(outputStr, "{")
                if jsonStart == -1 {
                    mu.Lock()
                    errors = append(errors, fmt.Errorf("no JSON found in loudnorm output for video %d (%s)", id, fullPath))
                    mu.Unlock()
                    return
                }
                jsonEnd := jsonStart
                braceCount := 1
                for i := jsonStart + 1; i < len(outputStr); i++ {
                    if outputStr[i] == '{' {
                        braceCount++
                    } else if outputStr[i] == '}' {
                        braceCount--
                        if braceCount == 0 {
                            jsonEnd = i + 1
                            break
                        }
                    }
                }
                if braceCount != 0 {
                    mu.Lock()
                    errors = append(errors, fmt.Errorf("unmatched braces in loudnorm JSON for video %d (%s)", id, fullPath))
                    mu.Unlock()
                    return
                }
                jsonStr := outputStr[jsonStart:jsonEnd]
                log.Printf("Extracted JSON for video %d (%s):\n%s", id, fullPath, jsonStr)
                var loudnorm LoudnormOutput
                if err := json.Unmarshal([]byte(jsonStr), &loudnorm); err != nil {
                    mu.Lock()
                    errors = append(errors, fmt.Errorf("failed to parse loudnorm JSON for video %d (%s): %v\nJSON: %s", id, fullPath, err, jsonStr))
                    mu.Unlock()
                    return
                }
                inputI, err := strconv.ParseFloat(loudnorm.InputI, 64)
                if err != nil {
                    mu.Lock()
                    errors = append(errors, fmt.Errorf("failed to parse loudnorm input_i for video %d (%s): %v", id, fullPath, err))
                    mu.Unlock()
                    return
                }
                inputLRA, err := strconv.ParseFloat(loudnorm.InputLRA, 64)
                if err != nil {
                    mu.Lock()
                    errors = append(errors, fmt.Errorf("failed to parse loudnorm input_lra for video %d (%s): %v", id, fullPath, err))
                    mu.Unlock()
                    return
                }
                inputTP, err := strconv.ParseFloat(loudnorm.InputTP, 64)
                if err != nil {
                    mu.Lock()
                    errors = append(errors, fmt.Errorf("failed to parse loudnorm input_tp for video %d (%s): %v", id, fullPath, err))
                    mu.Unlock()
                    return
                }
                inputThresh, err := strconv.ParseFloat(loudnorm.InputThresh, 64)
                if err != nil {
                    mu.Lock()
                    errors = append(errors, fmt.Errorf("failed to parse loudnorm input_thresh for video %d (%s): %v", id, fullPath, err))
                    mu.Unlock()
                    return
                }
                log.Printf("Attempting to update loudnorm for video %d (%s): I=%.2f, LRA=%.2f, TP=%.2f, Thresh=%.2f", id, fullPath, inputI, inputLRA, inputTP, inputThresh)
                _, err = db.Exec(
                    "UPDATE videos SET loudnorm_input_i = $1, loudnorm_input_lra = $2, loudnorm_input_tp = $3, loudnorm_input_thresh = $4 WHERE id = $5",
                    inputI, inputLRA, inputTP, inputThresh, id,
                )
                if err != nil {
                    mu.Lock()
                    errors = append(errors, fmt.Errorf("failed to update loudnorm for video %d (%s): %v", id, fullPath, err))
                    mu.Unlock()
                    return
                }
                log.Printf("Updated loudnorm measurements for video %d (%s): I=%.2f, LRA=%.2f, TP=%.2f, Thresh=%.2f", id, fullPath, inputI, inputLRA, inputTP, inputThresh)

                // Normalize the full video
                normalizedPath := filepath.Join(normalizedDir, filepath.Base(uri))
                if _, statErr := os.Stat(normalizedPath); statErr == nil {
                    log.Printf("Normalized file already exists for video %d (%s), skipping normalization", id, fullPath)
                    return
                }
                loudnormFilter := fmt.Sprintf(
                    "loudnorm=I=-23:TP=-1.5:LRA=11:measured_I=%.2f:measured_LRA=%.2f:measured_TP=%.2f:measured_thresh=%.2f:offset=0:linear=true",
                    inputI, inputLRA, inputTP, inputThresh,
                )
                cmdNormalize := exec.Command(
                    "ffmpeg",
                    "-err_detect", "ignore_err",
                    "-analyzeduration", "100M",
                    "-probesize", "100M",
                    "-y",
                    "-i", fullPath,
                    "-c:v", "copy",
                    "-c:a", "aac",
                    "-af", loudnormFilter,
                    normalizedPath,
                )
                outputNormalize, err := cmdNormalize.CombinedOutput()
                if err != nil {
                    mu.Lock()
                    errors = append(errors, fmt.Errorf("ffmpeg normalization failed for video %d (%s): %v\nOutput: %s", id, fullPath, err, string(outputNormalize)))
                    mu.Unlock()
                    return
                }
                log.Printf("Created normalized video for %d at %s", id, normalizedPath)
            } else {
                log.Printf("Loudnorm already set for video %d (%s), skipping", id, fullPath)
            }
        }(videoID, uris[i])
    }
    wg.Wait()
    if len(errors) > 0 {
        errorLogger.Printf("Encountered %d errors during duration and loudnorm updates: %v", len(errors), errors)
        return fmt.Errorf("encountered %d errors during duration and loudnorm updates: %v", len(errors), errors)
    }
    log.Printf("Successfully updated metadata for %d videos", len(videoIDs))
    return nil
}

func main() {
    errorLogFile, err := os.OpenFile("error.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
    if err != nil {
        log.Fatal("Failed to open error.log: ", err)
    }
    defer errorLogFile.Close()
    errorLogger = log.New(errorLogFile, "", log.LstdFlags)
    runtime.GOMAXPROCS(runtime.NumCPU())
    rand.Seed(time.Now().UnixNano())
    if err := os.MkdirAll(HlsDir, 0755); err != nil {
        log.Fatal(err)
    }
    db, err := sql.Open("postgres", dbConnString)
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()
    if err := db.Ping(); err != nil {
        log.Fatal("DB ping failed: ", err)
    }
    log.Println("Connected to PostgreSQL DB")
    videoBaseDir = os.Getenv("VIDEO_BASE_DIR")
    if videoBaseDir == "" {
        videoBaseDir = DefaultVideoBaseDir
    }
    log.Printf("Using video base directory: %s", videoBaseDir)
    if err := updateVideoDurations(db); err != nil {
        log.Printf("Failed to update video durations: %v", err)
    }
    rows, err := db.Query("SELECT v.id FROM videos v JOIN video_tags vt ON v.id = vt.video_id WHERE vt.tag_id = 4")
    if err != nil {
        log.Fatalf("Failed to load ad IDs: %v", err)
    }
    defer rows.Close()
    for rows.Next() {
        var id int64
        if err := rows.Scan(&id); err != nil {
            log.Printf("Failed to scan ad ID: %v", err)
            continue
        }
        adIDs = append(adIDs, id)
    }
    if err := rows.Err(); err != nil {
        log.Fatalf("Error iterating ad IDs: %v", err)
    }
    log.Printf("Loaded %d commercials", len(adIDs))
    r := gin.Default()
    r.Use(cors.Default())
    r.POST("/signal", func(c *gin.Context) { signalingHandler(db, c) })
    r.GET("/", indexHandler)
    r.GET("/hls/*path", func(c *gin.Context) {
        c.String(404, "Use WebRTC")
    })
    log.Printf("WebRTC TV server on %s. Stations will be loaded on demand.", Port)
    log.Fatal(r.Run(Port))
}