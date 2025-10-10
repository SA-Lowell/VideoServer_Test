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
    HlsDir              = "./webrtc_segments"
    Port                = ":8081"
    ClockRate           = 90000
    AudioFrameMs        = 20
    DefaultFPSNum       = 30000
    DefaultFPSDen       = 1001
    DefaultDur          = 0.0
    DefaultStation      = "default"
    AdInsertPath        = "./ad_insert.exe"
    DefaultVideoBaseDir = "Z:/Videos"
    DefaultTempPrefix   = "ad_insert_"
    ChunkDuration       = 30.0 // Process 30-second chunks
    BufferThreshold     = 120.0 // Start processing more chunks when buffer < 120s
    maxAdRetries        = 5     // Higher retry limit for ads
)

const dbConnString = "user=postgres password=aaaaaaaaaa dbname=webrtc_tv sslmode=disable host=localhost port=5432"

type fpsPair struct {
    num int
    den int
}

type bufferedChunk struct {
    segPath string
    dur     float64
    isAd    bool
    videoID int64
    fps     fpsPair
}

type bitReader struct {
    data []byte
    pos  int
}

type LoudnormOutput struct {
    InputI      string `json:"input_i"`
    InputLRA    string `json:"input_lra"`
    InputTP     string `json:"input_tp"`
    InputThresh string `json:"input_thresh"`
}

type Station struct {
    name                string
    segmentList         []bufferedChunk
    spsPPS              [][]byte
    fmtpLine            string
    trackVideo          *webrtc.TrackLocalStaticSample
    trackAudio          *webrtc.TrackLocalStaticSample
    videoQueue          []int64
    currentVideo        int64
    currentIndex        int
    currentOffset       float64
    viewers             int
    processing          bool
    stopCh              chan struct{}
    adsEnabled          bool
    mu                  sync.Mutex
    currentVideoRTPTS   uint32
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
    bitIdx := 7 - (br.pos % 8)
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

func processVideo(st *Station, videoID int64, db *sql.DB, startTime, chunkDur float64) ([]string, [][]byte, string, float64, fpsPair, error) {
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
    fullEpisodePath := filepath.Join(videoBaseDir, uri)
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
    tempMP4Path := filepath.Join(tempDir, baseName+".mp4")
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
    var cmdAudio *exec.Cmd
    var outputAudio []byte
    var audioData []byte
    if hasAudio {
        argsAudio := []string{
            "-y",
            "-ss", fmt.Sprintf("%.3f", startTime),
            "-i", fullEpisodePath,
            "-t", fmt.Sprintf("%.3f", adjustedChunkDur),
            "-map", "0:a:0",
            "-c:a", "libopus",
            "-b:a", "128k",
            "-ar", "48000",
            "-ac", "2",
            "-frame_duration", "20",
            "-page_duration", "20000",
            "-application", "audio",
            "-vbr", "on",
            "-avoid_negative_ts", "make_zero",
            "-fflags", "+genpts",
            "-async", "1",
            "-max_delay", "0", // Added to minimize sync issues
            "-threads", "0",
            "-f", "opus",
            opusPath,
        }
        if loudI.Valid {
            loudnormFilter := fmt.Sprintf(
                "loudnorm=I=-23:TP=-1.5:LRA=11:measured_I=%.2f:measured_LRA=%.2f:measured_TP=%.2f:measured_thresh=%.2f:offset=0:linear=true",
                loudI.Float64, loudLRA.Float64, loudTP.Float64, loudThresh.Float64,
            )
            for i := len(argsAudio) - 1; i >= 0; i-- {
                if argsAudio[i] == "-c:a" {
                    argsAudio = append(argsAudio[:i], append([]string{"-af", loudnormFilter}, argsAudio[i:]...)...)
                    break
                }
            }
            log.Printf("Station %s: Applying loudnorm filter to audio for video %d: %s", st.name, videoID, loudnormFilter)
        }
        cmdAudio = exec.Command("ffmpeg", argsAudio...)
        outputAudio, err = cmdAudio.CombinedOutput()
        log.Printf("Station %s: FFmpeg audio output for %s: %s", st.name, opusPath, string(outputAudio))
        if err != nil {
            errorLogger.Printf("Station %s: ffmpeg audio command failed for %s: %v", st.name, opusPath, err)
            argsAudio = []string{
                "-y",
                "-ss", fmt.Sprintf("%.3f", startTime),
                "-i", fullEpisodePath,
                "-t", fmt.Sprintf("%.3f", adjustedChunkDur),
                "-map", "0:a:0",
                "-c:a", "libopus",
                "-b:a", "128k",
                "-ar", "48000",
                "-ac", "2",
                "-frame_duration", "20",
                "-page_duration", "20000",
                "-application", "audio",
                "-vbr", "on",
                "-avoid_negative_ts", "make_zero",
                "-fflags", "+genpts",
                "-async", "1",
                "-max_delay", "0", // Added to minimize sync issues
                "-threads", "0",
                "-f", "opus",
                opusPath,
            }
            log.Printf("Station %s: Retrying audio encode without loudnorm for %s", st.name, opusPath)
            cmdAudio = exec.Command("ffmpeg", argsAudio...)
            outputAudio, err = cmdAudio.CombinedOutput()
            log.Printf("Station %s: FFmpeg audio retry output for %s: %s", st.name, opusPath, string(outputAudio))
            if err != nil {
                errorLogger.Printf("Station %s: ffmpeg audio retry failed for %s: %v", st.name, opusPath, err)
            } else {
                log.Printf("Station %s: ffmpeg audio retry succeeded for %s", st.name, opusPath)
            }
        } else {
            log.Printf("Station %s: ffmpeg audio succeeded for %s", st.name, opusPath)
        }
        audioData, err = os.ReadFile(opusPath)
        if err != nil || len(audioData) == 0 {
            errorLogger.Printf("Station %s: Audio file %s is empty or unreadable after encoding: %v, size=%d", st.name, opusPath, err, len(audioData))
            hasAudio = false
        }
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
            "-page_duration", "20000",
            "-application", "audio",
            "-vbr", "on",
            "-avoid_negative_ts", "make_zero",
            "-fflags", "+genpts",
            "-max_delay", "0", // Added for silent audio
            "-threads", "0",
            "-f", "opus",
            opusPath,
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
    var cmdVideo *exec.Cmd
    var outputVideo []byte
    argsVideo := []string{
        "-y",
        "-ss", fmt.Sprintf("%.3f", startTime),
        "-i", fullEpisodePath,
        "-t", fmt.Sprintf("%.3f", adjustedChunkDur),
        "-c:v", "libx264",
        "-preset", "ultrafast", // Changed to ultrafast
        "-crf", "23",
        "-bf", "0",
        "-maxrate", "20M",
        "-bufsize", "40M",
        "-profile:v", "high",
        "-level", "5.2",
        "-pix_fmt", "yuv420p",
        "-force_fps",
        "-r", fmt.Sprintf("%d/%d", fpsNum, fpsDen),
        "-fps_mode", "cfr",
        "-force_key_frames", "0",
        "-sc_threshold", "0",
        "-an",
        "-bsf:v", "h264_mp4toannexb",
        "-g", fmt.Sprintf("%d", gopSize),
        "-keyint_min", fmt.Sprintf("%d", keyintMin),
        "-x264-params", keyFrameParams,
        "-threads", "0",
        fullSegPath,
    }
    if isFinalChunk {
        insertIndex := len(argsVideo) - 7 // Before -g
        argsVideo = append(argsVideo[:insertIndex], append([]string{"-force_key_frames", "0:expr=eq(n,0)"}, argsVideo[insertIndex:]...)...)
        log.Printf("Station %s: Forcing IDR frame at start for final chunk %s", st.name, fullSegPath)
    }
    cmdVideo = exec.Command("ffmpeg", argsVideo...)
    outputVideo, err = cmdVideo.CombinedOutput()
    log.Printf("Station %s: FFmpeg video output for %s: %s", st.name, fullSegPath, string(outputVideo))
    if err != nil {
        errorLogger.Printf("Station %s: ffmpeg video command failed for %s: %v", st.name, fullSegPath, err)
    } else {
        log.Printf("Station %s: ffmpeg video succeeded for %s", st.name, fullSegPath)
    }
    if err != nil {
        log.Printf("Station %s: Initial encoding failed for video %d at %.3fs, performing full re-encode", st.name, videoID, startTime)
        var cmdReencode *exec.Cmd
        var outputReencode []byte
        gopSizeReencode := int(math.Round(fps * 2))
        if isFinalChunk {
            gopSizeReencode = int(math.Round(fps * 0.5))
        }
        keyintMinReencode := gopSizeReencode
        keyFrameParamsReencode := fmt.Sprintf("keyint=%d:min-keyint=1:scenecut=0", gopSizeReencode)
        tempMP4Args := []string{
            "-y",
            "-ss", fmt.Sprintf("%.3f", startTime),
            "-i", fullEpisodePath,
            "-t", fmt.Sprintf("%.3f", adjustedChunkDur),
            "-c:v", "libx264",
            "-preset", "ultrafast", // Changed to ultrafast
            "-crf", "23",
            "-maxrate", "20M",
            "-bufsize", "40M",
            "-profile:v", "high",
            "-level", "5.2",
            "-pix_fmt", "yuv420p",
            "-force_fps",
            "-r", fmt.Sprintf("%d/%d", fpsNum, fpsDen),
            "-fps_mode", "cfr",
            "-force_key_frames", "0",
            "-sc_threshold", "0",
            "-g", fmt.Sprintf("%d", gopSizeReencode),
            "-keyint_min", fmt.Sprintf("%d", keyintMinReencode),
            "-bf", "0",
            "-x264-params", keyFrameParamsReencode,
            "-threads", "0",
            "-c:a", "libopus",
            "-b:a", "128k",
            "-ar", "48000",
            "-ac", "2",
            "-async", "1",
            "-strict", "experimental",
            "-f", "mp4",
            tempMP4Path,
        }
        if isFinalChunk {
            insertIndex := len(tempMP4Args) - 7
            tempMP4Args = append(tempMP4Args[:insertIndex], append([]string{"-force_key_frames", "0:expr=eq(n,0)"}, tempMP4Args[insertIndex:]...)...)
        }
        if !hasAudio {
            tempMP4Args = tempMP4Args[:len(tempMP4Args)-7]
            tempMP4Args = append(tempMP4Args, "-an", "-f", "mp4", tempMP4Path)
        }
        cmdReencode = exec.Command("ffmpeg", tempMP4Args...)
        outputReencode, err = cmdReencode.CombinedOutput()
        log.Printf("Station %s: FFmpeg re-encode output for %s: %s", st.name, tempMP4Path, string(outputReencode))
        if err != nil {
            errorLogger.Printf("Station %s: Full re-encode failed for video %d at %.3fs: %v", st.name, videoID, startTime, err)
            return nil, nil, "", 0, fpsPair{}, fmt.Errorf("full re-encode failed for video %d at %.3fs: %v", videoID, startTime, err)
        }
        log.Printf("Station %s: Full re-encode succeeded, creating segments from %s", st.name, tempMP4Path)
        var cmdReencodeVideo *exec.Cmd
        var outputReencodeVideo []byte
        argsReencodeVideo := []string{
            "-y",
            "-i", tempMP4Path,
            "-c:v", "libx264",
            "-preset", "ultrafast", // Changed to ultrafast
            "-crf", "23",
            "-maxrate", "20M",
            "-bufsize", "40M",
            "-profile:v", "high",
            "-level", "5.2",
            "-pix_fmt", "yuv420p",
            "-force_fps",
            "-r", fmt.Sprintf("%d/%d", fpsNum, fpsDen),
            "-fps_mode", "cfr",
            "-force_key_frames", "0",
            "-g", fmt.Sprintf("%d", gopSizeReencode),
            "-keyint_min", fmt.Sprintf("%d", keyintMinReencode),
            "-bf", "0",
            "-x264-params", keyFrameParamsReencode,
            "-threads", "0",
            "-an",
            "-bsf:v", "h264_mp4toannexb",
            "-f", "h264",
            fullSegPath,
        }
        if isFinalChunk {
            insertIndex := len(argsReencodeVideo) - 7
            argsReencodeVideo = append(argsReencodeVideo[:insertIndex], append([]string{"-force_key_frames", "0:expr=eq(n,0)"}, argsReencodeVideo[insertIndex:]...)...)
        }
        cmdReencodeVideo = exec.Command("ffmpeg", argsReencodeVideo...)
        outputReencodeVideo, err = cmdReencodeVideo.CombinedOutput()
        log.Printf("Station %s: FFmpeg re-encode video output for %s: %s", st.name, fullSegPath, string(outputReencodeVideo))
        if err != nil {
            errorLogger.Printf("Station %s: Re-encoding video failed for %s: %v", st.name, fullSegPath, err)
            os.Remove(tempMP4Path)
            return nil, nil, "", 0, fpsPair{}, fmt.Errorf("re-encoding video failed for video %d: %v", videoID, err)
        }
        segments = append(segments, fullSegPath)
        if hasAudio {
            var cmdReencodeAudio *exec.Cmd
            var outputReencodeAudio []byte
            argsReencodeAudio := []string{
                "-y",
                "-i", tempMP4Path,
                "-c:a", "libopus",
                "-b:a", "128k",
                "-ar", "48000",
                "-ac", "2",
                "-async", "1",
                "-max_delay", "0", // Added to minimize sync issues
                "-threads", "0",
                "-vn",
                "-f", "opus",
                opusPath,
            }
            if loudI.Valid {
                loudnormFilter := fmt.Sprintf(
                    "loudnorm=I=-23:TP=-1.5:LRA=11:measured_I=%.2f:measured_LRA=%.2f:measured_TP=%.2f:measured_thresh=%.2f:offset=0:linear=true",
                    loudI.Float64, loudLRA.Float64, loudTP.Float64, loudThresh.Float64,
                )
                for i := len(argsReencodeAudio) - 1; i >= 0; i-- {
                    if argsReencodeAudio[i] == "-c:a" {
                        argsReencodeAudio = append(argsReencodeAudio[:i], append([]string{"-af", loudnormFilter}, argsReencodeAudio[i:]...)...)
                        break
                    }
                }
            }
            cmdReencodeAudio = exec.Command("ffmpeg", argsReencodeAudio...)
            outputReencodeAudio, err = cmdReencodeAudio.CombinedOutput()
            log.Printf("Station %s: FFmpeg re-encode audio output for %s: %s", st.name, opusPath, string(outputReencodeAudio))
            if err != nil {
                errorLogger.Printf("Station %s: Re-encoding audio failed for %s: %v", st.name, opusPath, err)
                argsReencodeAudio = []string{
                    "-y",
                    "-i", tempMP4Path,
                    "-c:a", "libopus",
                    "-b:a", "128k",
                    "-ar", "48000",
                    "-ac", "2",
                    "-async", "1",
                    "-max_delay", "0", // Added to minimize sync issues
                    "-threads", "0",
                    "-vn",
                    "-f", "opus",
                    opusPath,
                }
                cmdReencodeAudio = exec.Command("ffmpeg", argsReencodeAudio...)
                outputReencodeAudio, err = cmdReencodeAudio.CombinedOutput()
                log.Printf("Station %s: FFmpeg re-encode audio retry output for %s: %s", st.name, opusPath, string(outputReencodeAudio))
                if err != nil {
                    errorLogger.Printf("Station %s: Re-encoding audio retry failed for %s: %v", st.name, opusPath, err)
                    os.Remove(fullSegPath)
                    os.Remove(tempMP4Path)
                    return nil, nil, "", 0, fpsPair{}, fmt.Errorf("re-encoding audio failed for video %d: %v", videoID, err)
                }
            }
            audioData, err = os.ReadFile(opusPath)
            if err != nil || len(audioData) == 0 {
                errorLogger.Printf("Station %s: Re-encoded audio file %s is empty or unreadable: %v, size=%d", st.name, opusPath, err, len(audioData))
                os.Remove(fullSegPath)
                os.Remove(tempMP4Path)
                return nil, nil, "", 0, fpsPair{}, fmt.Errorf("re-encoded audio file %s is empty or unreadable: %v", opusPath, err)
            }
            cmdAudioProbe = exec.Command(
                "ffprobe",
                "-v", "error",
                "-show_packets",
                opusPath,
            )
            outputAudioProbe, err = cmdAudioProbe.Output()
            if err != nil {
                errorLogger.Printf("Station %s: Failed to probe re-encoded audio packets for %s: %v", st.name, opusPath, err)
                os.Remove(fullSegPath)
                os.Remove(tempMP4Path)
                return nil, nil, "", 0, fpsPair{}, fmt.Errorf("failed to probe re-encoded audio packets for %s: %v", opusPath, err)
            } else {
                outputStr := string(outputAudioProbe)
                packetLines := strings.Count(outputStr, "[PACKET]")
                if packetLines == 0 {
                    errorLogger.Printf("Station %s: Re-encoded audio file %s has 0 packets - invalid encoding", st.name, opusPath)
                    os.Remove(fullSegPath)
                    os.Remove(tempMP4Path)
                    return nil, nil, "", 0, fpsPair{}, fmt.Errorf("re-encoded audio file %s has 0 packets", opusPath)
                }
                log.Printf("Station %s: Re-encoded audio file %s has %d packets", st.name, opusPath, packetLines)
            }
        }
        os.Remove(tempMP4Path)
    } else {
        segments = append(segments, fullSegPath)
    }
    var cmdDur *exec.Cmd
    var outputDur []byte
    cmdDur = exec.Command(
        "ffprobe",
        "-v", "error",
        "-show_entries", "format=duration",
        "-of", "json",
        fullSegPath,
    )
    outputDur, err = cmdDur.Output()
    var actualDur float64
    if err == nil {
        var result struct {
            Format struct {
                Duration string `json:"duration"`
            } `json:"format"`
        }
        if err := json.Unmarshal(outputDur, &result); err != nil {
            errorLogger.Printf("Station %s: Failed to parse ffprobe JSON for %s: %v", st.name, fullSegPath, err)
        } else if result.Format.Duration != "" {
            actualDur, err = strconv.ParseFloat(result.Format.Duration, 64)
            if err != nil {
                errorLogger.Printf("Station %s: Failed to parse duration for %s: %v", st.name, fullSegPath, err)
            } else {
                log.Printf("Station %s: Video segment %s duration: %.3fs", st.name, fullSegPath, actualDur)
            }
        }
    }
    if actualDur == 0 {
        cmdDur = exec.Command(
            "ffprobe",
            "-v", "error",
            "-show_entries", "format=duration",
            "-of", "json",
            fullEpisodePath,
        )
        outputDur, err = cmdDur.Output()
        if err == nil {
            var result struct {
                Format struct {
                    Duration string `json:"duration"`
                } `json:"format"`
            }
            if err := json.Unmarshal(outputDur, &result); err != nil {
                errorLogger.Printf("Station %s: Failed to parse source ffprobe JSON for %s: %v", st.name, fullEpisodePath, err)
            } else if result.Format.Duration != "" {
                actualDur, err = strconv.ParseFloat(result.Format.Duration, 64)
                if err != nil {
                    errorLogger.Printf("Station %s: Failed to parse source duration for %s: %v", st.name, fullEpisodePath, err)
                } else {
                    actualDur = math.Min(actualDur, adjustedChunkDur)
                    log.Printf("Station %s: Video segment %s duration from source: %.3fs", st.name, fullSegPath, actualDur)
                }
            }
        }
        if actualDur == 0 {
            errorLogger.Printf("Station %s: All duration probes failed for %s, using adjustedChunkDur %.3f", st.name, fullSegPath, adjustedChunkDur)
            actualDur = adjustedChunkDur
        }
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
    if math.Abs(audioDur-actualDur) > 0.1 {
        log.Printf("Station %s: Audio duration %.3fs differs from video duration %.3fs, re-encoding audio", st.name, audioDur, actualDur)
        argsAudio := []string{
            "-y",
            "-ss", fmt.Sprintf("%.3f", startTime),
            "-i", fullEpisodePath,
            "-t", fmt.Sprintf("%.3f", actualDur),
            "-map", "0:a:0",
            "-c:a", "libopus",
            "-b:a", "128k",
            "-ar", "48000",
            "-ac", "2",
            "-frame_duration", "20",
            "-page_duration", "20000",
            "-application", "audio",
            "-vbr", "on",
            "-avoid_negative_ts", "make_zero",
            "-fflags", "+genpts",
            "-async", "1",
            "-max_delay", "0", // Added to minimize sync issues
            "-threads", "0",
            "-f", "opus",
            opusPath,
        }
        if loudI.Valid && hasAudio {
            loudnormFilter := fmt.Sprintf(
                "loudnorm=I=-23:TP=-1.5:LRA=11:measured_I=%.2f:measured_LRA=%.2f:measured_TP=%.2f:measured_thresh=%.2f:offset=0:linear=true",
                loudI.Float64, loudLRA.Float64, loudTP.Float64, loudThresh.Float64,
            )
            for i := len(argsAudio) - 1; i >= 0; i-- {
                if argsAudio[i] == "-c:a" {
                    argsAudio = append(argsAudio[:i], append([]string{"-af", loudnormFilter}, argsAudio[i:]...)...)
                    break
                }
            }
            log.Printf("Station %s: Applying loudnorm filter to re-encoded audio for video %d: %s", st.name, videoID, loudnormFilter)
        }
        if !hasAudio {
            argsAudio = []string{
                "-y",
                "-f", "lavfi",
                "-i", fmt.Sprintf("anullsrc=r=48000:cl=stereo:d=%.3f", actualDur),
                "-c:a", "libopus",
                "-b:a", "128k",
                "-ar", "48000",
                "-ac", "2",
                "-frame_duration", "20",
                "-page_duration", "20000",
                "-application", "audio",
                "-vbr", "on",
                "-avoid_negative_ts", "make_zero",
                "-fflags", "+genpts",
                "-max_delay", "0", // Added to minimize sync issues
                "-threads", "0",
                "-f", "opus",
                opusPath,
            }
        }
        cmdAudio = exec.Command("ffmpeg", argsAudio...)
        outputAudio, err = cmdAudio.CombinedOutput()
        log.Printf("Station %s: FFmpeg audio re-encode output for %s: %s", st.name, opusPath, string(outputAudio))
        if err != nil {
            errorLogger.Printf("Station %s: ffmpeg audio re-encode failed for %s: %v", st.name, opusPath, err)
            if hasAudio {
                argsAudio = []string{
                    "-y",
                    "-ss", fmt.Sprintf("%.3f", startTime),
                    "-i", fullEpisodePath,
                    "-t", fmt.Sprintf("%.3f", actualDur),
                    "-map", "0:a:0",
                    "-c:a", "libopus",
                    "-b:a", "128k",
                    "-ar", "48000",
                    "-ac", "2",
                    "-frame_duration", "20",
                    "-page_duration", "20000",
                    "-application", "audio",
                    "-vbr", "on",
                    "-avoid_negative_ts", "make_zero",
                    "-fflags", "+genpts",
                    "-async", "1",
                    "-max_delay", "0", // Added to minimize sync issues
                    "-threads", "0",
                    "-f", "opus",
                    opusPath,
                }
                cmdAudio = exec.Command("ffmpeg", argsAudio...)
                outputAudio, err = cmdAudio.CombinedOutput()
                log.Printf("Station %s: FFmpeg audio re-encode retry output for %s: %s", st.name, opusPath, string(outputAudio))
                if err != nil {
                    errorLogger.Printf("Station %s: ffmpeg audio re-encode retry failed for %s: %v", st.name, opusPath, err)
                    return nil, nil, "", 0, fpsPair{}, fmt.Errorf("ffmpeg audio re-encode failed for video %d at %fs: %v", videoID, startTime, err)
                }
            } else {
                errorLogger.Printf("Station %s: ffmpeg audio re-encode failed for %s: %v", st.name, opusPath, err)
                return nil, nil, "", 0, fpsPair{}, fmt.Errorf("ffmpeg audio re-encode failed for video %d at %fs: %v", videoID, startTime, err)
            }
        }
        log.Printf("Station %s: Re-encoded audio for %s to match video duration %.3fs", st.name, opusPath, actualDur)
    }
    data, err := os.ReadFile(fullSegPath)
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
            "-i", fullSegPath,
            "-c:v", "libx264",
            "-preset", "ultrafast", // Changed to ultrafast
            "-crf", "23",
            "-bf", "0",
            "-maxrate", "20M",
            "-bufsize", "40M",
            "-profile:v", "high",
            "-level", "5.2",
            "-pix_fmt", "yuv420p",
            "-force_fps",
            "-r", fmt.Sprintf("%d/%d", fpsNum, fpsDen),
            "-fps_mode", "cfr",
            "-force_key_frames", "0:expr=eq(n,0)",
            "-sc_threshold", "0",
            "-bsf:v", "h264_mp4toannexb",
            "-g", fmt.Sprintf("%d", gopSize),
            "-keyint_min", fmt.Sprintf("%d", keyintMin),
            "-x264-params", keyFrameParamsRepair,
            "-threads", "0",
            "-f", "h264",
            repairedSegPath,
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
    fmtpLine = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640034"
    log.Printf("Station %s: Processed segment %s with %d NALUs, %d SPS/PPS, fmtp: %s, hasIDR: %v", st.name, fullSegPath, len(nalus), len(spsPPS), fmtpLine, hasIDR)
    return segments, spsPPS, fmtpLine, actualDur, fpsPair{num: fpsNum, den: fpsDen}, nil
}

func getBreakPoints(videoID int64, db *sql.DB) ([]float64, error) {
    rows, err := db.Query("SELECT value FROM video_metadata WHERE video_id = $1 AND metadata_type_id = 1", videoID)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var bps []float64
    for rows.Next() {
        var raw json.RawMessage
        if err := rows.Scan(&raw); err != nil {
            return nil, err
        }
        var bp float64
        if err := json.Unmarshal(raw, &bp); err != nil {
            return nil, err
        }
        bps = append(bps, bp)
    }
    if err := rows.Err(); err != nil {
        return nil, err
    }
    sort.Float64s(bps)
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
    const maxFinalChunkRetries = 5
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
            // Resource monitoring
            var memStats runtime.MemStats
            runtime.ReadMemStats(&memStats)
            if memStats.Alloc > 4*1024*1024*1024 { // 4GB
                errorLogger.Printf("Station %s: High memory usage (%d bytes), pausing processing", st.name, memStats.Alloc)
                st.mu.Unlock()
                time.Sleep(5 * time.Second)
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
                    sumNonAd += chunk.dur
                }
            }
            log.Printf("Station %s (adsEnabled: %v): Remaining buffer %.3fs, non-ad %.3fs, current video %d, offset %.3fs", st.name, st.adsEnabled, remainingDur, sumNonAd, st.currentVideo, st.currentOffset)
            if remainingDur < BufferThreshold || len(st.segmentList) < 4 { // Changed to 4 chunks
                videoDur := getVideoDur(st.currentVideo, db)
                if videoDur <= 0 {
                    errorLogger.Printf("Station %s (adsEnabled: %v): Invalid duration for video %d, advancing", st.name, st.adsEnabled, st.currentVideo)
                    st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                    st.currentVideo = st.videoQueue[st.currentIndex]
                    st.currentOffset = 0.0
                    st.spsPPS = nil
                    st.fmtpLine = ""
                    st.currentVideoRTPTS = st.currentVideoRTPTS // Maintain continuity
                    st.currentAudioSamples = st.currentAudioSamples
                    log.Printf("Station %s (adsEnabled: %v): Transitioned to video %d with offset 0.0s", st.name, st.adsEnabled, st.currentVideo)
                    st.mu.Unlock()
                    continue
                }
                nextStart := st.currentOffset + sumNonAd
                if nextStart >= videoDur || math.Abs(nextStart-videoDur) < 0.001 {
                    log.Printf("Station %s (adsEnabled: %v): Reached end of video %d (%.3fs >= %.3fs), advancing", st.name, st.adsEnabled, st.currentVideo, nextStart, videoDur)
                    st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                    st.currentVideo = st.videoQueue[st.currentIndex]
                    st.currentOffset = 0.0
                    st.spsPPS = nil
                    st.fmtpLine = ""
                    st.currentVideoRTPTS = st.currentVideoRTPTS // Maintain continuity
                    st.currentAudioSamples = st.currentAudioSamples
                    log.Printf("Station %s (adsEnabled: %v): Transitioned to video %d with offset 0.0s", st.name, st.adsEnabled, st.currentVideo)
                    st.mu.Unlock()
                    continue
                }
                breaks, err := getBreakPoints(st.currentVideo, db)
                if err != nil {
                    errorLogger.Printf("Station %s (adsEnabled: %v): Failed to get break points for video %d: %v", st.name, st.adsEnabled, st.currentVideo, err)
                    breaks = []float64{}
                }
                log.Printf("Station %s (adsEnabled: %v): Break points for video %d: %v", st.name, st.adsEnabled, st.currentVideo, breaks)
                var nextBreak float64 = math.MaxFloat64
                for _, b := range breaks {
                    if b > nextStart {
                        nextBreak = b
                        break
                    }
                }
                chunkDur := ChunkDuration
                chunkEnd := nextStart + chunkDur
                isFinalChunk := chunkEnd >= videoDur || math.Abs(chunkEnd-videoDur) < 0.001
                if isFinalChunk {
                    chunkEnd = videoDur
                    chunkDur = videoDur - nextStart
                } else if nextBreak < chunkEnd {
                    chunkEnd = nextBreak
                    chunkDur = nextBreak - nextStart
                }
                if chunkDur <= 0 {
                    errorLogger.Printf("Station %s (adsEnabled: %v): Invalid chunk duration (%.3fs) at %.3fs for video %d, advancing", st.name, st.adsEnabled, chunkDur, nextStart, st.currentVideo)
                    st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                    st.currentVideo = st.videoQueue[st.currentIndex]
                    st.currentOffset = 0.0
                    st.spsPPS = nil
                    st.fmtpLine = ""
                    st.currentVideoRTPTS = st.currentVideoRTPTS
                    st.currentAudioSamples = st.currentAudioSamples
                    log.Printf("Station %s (adsEnabled: %v): Transitioned to video %d with offset 0.0s", st.name, st.adsEnabled, st.currentVideo)
                    st.mu.Unlock()
                    continue
                }
                retryLimit := maxRetries
                if isFinalChunk {
                    retryLimit = maxFinalChunkRetries
                    log.Printf("Station %s (adsEnabled: %v): Processing final chunk for video %d at %.3fs, duration %.3fs", st.name, st.adsEnabled, st.currentVideo, nextStart, chunkDur)
                }
                var segments []string
                var spsPPS [][]byte
                var fmtpLine string
                var actualDur float64
                var fps fpsPair
                var retryCount int // Explicit declaration
                for retryCount = 0; retryCount < retryLimit; retryCount++ {
                    segments, spsPPS, fmtpLine, actualDur, fps, err = processVideo(st, st.currentVideo, db, nextStart, chunkDur)
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
                    }
                    st.segmentList = append(st.segmentList, newChunk)
                    remainingDur += actualDur
                    sumNonAd += actualDur
                    log.Printf("Station %s (adsEnabled: %v): Queued %s chunk for video %d at %.3fs, duration %.3fs", st.name, st.adsEnabled, map[bool]string{true: "final", false: "episode"}[isFinalChunk], st.currentVideo, nextStart, actualDur)
                    break
                }
                if retryCount == retryLimit {
                    errorLogger.Printf("Station %s (adsEnabled: %v): Max retries (%d) failed for chunk at %.3fs, advancing video", st.name, st.adsEnabled, retryLimit, nextStart)
                    st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                    st.currentVideo = st.videoQueue[st.currentIndex]
                    st.currentOffset = 0.0
                    st.spsPPS = nil
                    st.fmtpLine = ""
                    st.currentVideoRTPTS = st.currentVideoRTPTS
                    st.currentAudioSamples = st.currentAudioSamples
                    log.Printf("Station %s (adsEnabled: %v): Transitioned to video %d with offset 0.0s", st.name, st.adsEnabled, st.currentVideo)
                    st.mu.Unlock()
                    continue
                }
                if st.adsEnabled && nextBreak == chunkEnd && len(breaks) > 0 {
                    log.Printf("Station %s (adsEnabled: %v): Inserting ad break at %.3fs for video %d", st.name, st.adsEnabled, nextBreak, st.currentVideo)
                    if len(adIDs) == 0 {
                        errorLogger.Printf("Station %s (adsEnabled: %v): No adIDs available, skipping ad break", st.name, st.adsEnabled)
                    } else {
                        availableAds := make([]int64, len(adIDs))
                        copy(availableAds, adIDs)
                        adDurTotal := 0.0
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
                                segments, spsPPS, fmtpLine, actualDur, fps, err = processVideo(st, adID, db, 0, adDur)
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
                                }
                                st.segmentList = append(st.segmentList, adChunk)
                                remainingDur += actualDur
                                adDurTotal += actualDur
                                log.Printf("Station %s (adsEnabled: %v): Queued ad %d with duration %.3fs at break %.3fs", st.name, st.adsEnabled, adID, actualDur, nextBreak)
                                break
                            }
                            if adRetryCount == maxAdRetries {
                                errorLogger.Printf("Station %s (adsEnabled: %v): All %d retries failed for ad %d, skipping", st.name, st.adsEnabled, maxAdRetries, adID)
                                availableAds = append(availableAds[:idx], availableAds[idx+1:]...)
                                continue
                            }
                            availableAds = append(availableAds[:idx], availableAds[idx+1:]...)
                        }
                        if adDurTotal > 0 {
                            sumNonAd = 0.0
                            for _, c := range st.segmentList {
                                if !c.isAd && c.videoID == st.currentVideo {
                                    sumNonAd += c.dur
                                }
                            }
                            nextStart = st.currentOffset + sumNonAd
                            if nextStart < videoDur {
                                retryLimit := maxRetries
                                isPostAdFinalChunk := nextStart+ChunkDuration >= videoDur || math.Abs(nextStart+ChunkDuration-videoDur) < 0.001
                                if isPostAdFinalChunk {
                                    retryLimit = maxFinalChunkRetries
                                    log.Printf("Station %s (adsEnabled: %v): Processing final chunk after ads for video %d at %.3fs", st.name, st.adsEnabled, st.currentVideo, nextStart)
                                }
                                var postAdRetryCount int // Explicit declaration
                                for postAdRetryCount = 0; postAdRetryCount < retryLimit; postAdRetryCount++ {
                                    segments, spsPPS, fmtpLine, actualDur, fps, err = processVideo(st, st.currentVideo, db, nextStart, ChunkDuration)
                                    if err != nil {
                                        errorLogger.Printf("Station %s (adsEnabled: %v): Failed to pre-queue %s chunk after ads for video %d at %.3fs (retry %d/%d): %v", st.name, st.adsEnabled, map[bool]string{true: "final", false: "episode"}[isPostAdFinalChunk], st.currentVideo, nextStart, postAdRetryCount+1, retryLimit, err)
                                        if segments != nil && len(segments) > 0 {
                                            os.Remove(segments[0])
                                            os.Remove(strings.Replace(segments[0], ".h264", ".opus", 1))
                                        }
                                        time.Sleep(time.Millisecond * 500)
                                        continue
                                    }
                                    if actualDur <= 0 {
                                        errorLogger.Printf("Station %s (adsEnabled: %v): Invalid duration (%.3fs) for chunk after ads at %.3fs, retrying", st.name, st.adsEnabled, actualDur, nextStart)
                                        if segments != nil && len(segments) > 0 {
                                            os.Remove(segments[0])
                                            os.Remove(strings.Replace(segments[0], ".h264", ".opus", 1))
                                        }
                                        continue
                                    }
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
                                    }
                                    st.segmentList = append(st.segmentList, newChunk)
                                    remainingDur += actualDur
                                    sumNonAd += actualDur
                                    log.Printf("Station %s (adsEnabled: %v): Pre-queued %s chunk after ads for video %d at %.3fs, duration %.3fs", st.name, st.adsEnabled, map[bool]string{true: "final", false: "episode"}[isPostAdFinalChunk], st.currentVideo, nextStart, actualDur)
                                    break
                                }
                                if postAdRetryCount == retryLimit && !isPostAdFinalChunk {
                                    errorLogger.Printf("Station %s (adsEnabled: %v): Max retries failed for chunk after ads at %.3fs, advancing video", st.name, st.adsEnabled, nextStart)
                                    st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                                    st.currentVideo = st.videoQueue[st.currentIndex]
                                    st.currentOffset = 0.0
                                    st.spsPPS = nil
                                    st.fmtpLine = ""
                                    st.currentVideoRTPTS = st.currentVideoRTPTS
                                    st.currentAudioSamples = st.currentAudioSamples
                                    log.Printf("Station %s (adsEnabled: %v): Transitioned to video %d with offset 0.0s", st.name, st.adsEnabled, st.currentVideo)
                                }
                            }
                        }
                    }
                }
            }
            log.Printf("Station %s (adsEnabled: %v): Buffer check complete, remainingDur %.3fs, segmentList: %v", st.name, st.adsEnabled, remainingDur, st.segmentList)
            st.mu.Unlock()
            time.Sleep(time.Millisecond * 500) // Reduced for faster processing
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
                    sumNonAd += c.dur
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
            log.Printf("Station %s (adsEnabled: %v): Processing chunk %d/%d: segPath=%s, videoID=%d, isAd=%v, dur=%.3fs, fps=%d/%d", st.name, st.adsEnabled, 1, len(st.segmentList), chunk.segPath, chunk.videoID, chunk.isAd, chunk.dur, fpsNum, fpsDen)
            // Load chunk-specific SPS/PPS
            data, err := os.ReadFile(segPath)
            if err != nil {
                errorLogger.Printf("Station %s (adsEnabled: %v): %s segment %s read error: %v", st.name, st.adsEnabled, map[bool]string{true: "Final", false: "Segment"}[isFinalChunk], segPath, err)
                st.mu.Unlock()
                continue
            }
            nalus := splitNALUs(data)
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
            st.mu.Unlock()
            testSample := media.Sample{Data: []byte{}, Duration: time.Duration(0)}
            if err := st.trackVideo.WriteSample(testSample); err != nil {
                errorLogger.Printf("Station %s (adsEnabled: %v): Video track write test error for %s: %v", st.name, st.adsEnabled, segPath, err)
                if strings.Contains(err.Error(), "not bound") {
                    st.mu.Lock()
                    if st.viewers == 0 {
                        close(st.stopCh)
                        st.stopCh = make(chan struct{})
                        st.mu.Unlock()
                        return
                    }
                    newTrackVideo, err := webrtc.NewTrackLocalStaticSample(
                        webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
                        fmt.Sprintf("video_%s_%t", sanitizeTrackID(st.name), st.adsEnabled),
                        "pion",
                    )
                    if err != nil {
                        errorLogger.Printf("Station %s (adsEnabled: %v): Failed to reinitialize video track: %v", st.name, st.adsEnabled, err)
                        st.viewers = 0
                        close(st.stopCh)
                        st.stopCh = make(chan struct{})
                        st.mu.Unlock()
                        return
                    }
                    st.trackVideo = newTrackVideo
                    log.Printf("Station %s (adsEnabled: %v): Reinitialized video track", st.name, st.adsEnabled)
                    st.mu.Unlock()
                    time.Sleep(time.Second)
                    continue
                }
            }
            audioPath := strings.Replace(segPath, ".h264", ".opus", 1)
            audioData, err := os.ReadFile(audioPath)
            if err != nil {
                errorLogger.Printf("Station %s (adsEnabled: %v): Failed to read audio %s: %v", st.name, st.adsEnabled, audioPath, err)
            }
            var audioPackets [][]byte
            if len(audioData) > 0 {
                reader := bytes.NewReader(audioData)
                ogg, _, err := oggreader.NewWith(reader)
                if err != nil {
                    errorLogger.Printf("Station %s (adsEnabled: %v): Failed to create ogg reader for %s: %v", st.name, st.adsEnabled, audioPath, err)
                } else {
                    for {
                        payload, _, err := ogg.ParseNextPage()
                        if err == io.EOF {
                            break
                        }
                        if err != nil {
                            errorLogger.Printf("Station %s (adsEnabled: %v): Failed to parse ogg page for %s: %v", st.name, st.adsEnabled, audioPath, err)
                            continue
                        }
                        if len(payload) < 1 || (len(payload) >= 8 && (string(payload[:8]) == "OpusHead" || string(payload[:8]) == "OpusTags")) {
                            log.Printf("Station %s (adsEnabled: %v): Skipping header packet: %s", st.name, st.adsEnabled, string(payload[:8]))
                            continue
                        }
                        audioPackets = append(audioPackets, payload)
                    }
                    log.Printf("Station %s (adsEnabled: %v): Parsed %d audio packets for %s", st.name, st.adsEnabled, len(audioPackets), audioPath)
                }
            }
            if len(nalus) == 0 || len(audioData) == 0 {
                errorLogger.Printf("Station %s (adsEnabled: %v): %s chunk %s is empty or has no NALUs/audio, advancing", st.name, st.adsEnabled, map[bool]string{true: "Final", false: "Segment"}[isFinalChunk], segPath)
                st.mu.Lock()
                os.Remove(segPath)
                os.Remove(audioPath)
                if !chunk.isAd {
                    st.currentOffset += chunk.dur
                    if videoDur > 0 && (st.currentOffset >= videoDur || math.Abs(st.currentOffset-videoDur) < 0.001) {
                        st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                        st.currentVideo = st.videoQueue[st.currentIndex]
                        st.currentOffset = 0.0
                        st.spsPPS = nil
                        st.fmtpLine = ""
                        // Maintain RTP timestamps
                        log.Printf("Station %s (adsEnabled: %v): Transitioned to video %d with offset 0.0s, maintaining videoTS=%d, audioTS=%d", st.name, st.adsEnabled, st.currentVideo, currentVideoTS, currentAudioTS)
                    }
                }
                st.segmentList = st.segmentList[1:]
                st.mu.Unlock()
                continue
            }
            var wg sync.WaitGroup
            done := make(chan struct{})
            wg.Add(2)
            go func(nalus [][]byte, startTS uint32) {
                defer wg.Done()
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
                    select {
                    case done <- struct{}{}:
                    default:
                    }
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
                ticker := time.NewTicker(frameInterval)
                defer ticker.Stop()
                frameIdx := 0
                expectedSamples := uint32(chunk.dur * float64(videoClockRate))
                for range ticker.C {
                    if frameIdx >= actualFrames {
                        break
                    }
                    if !boundChecked {
                        if err := st.trackVideo.WriteSample(testSample); err != nil {
                            errorLogger.Printf("Station %s (adsEnabled: %v): Video track not bound for frame %d in %s: %v", st.name, st.adsEnabled, frameIdx, segPath, err)
                            st.mu.Lock()
                            st.currentVideoRTPTS = currentVideoTS
                            newTrackVideo, err := webrtc.NewTrackLocalStaticSample(
                                webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
                                fmt.Sprintf("video_%s_%t", sanitizeTrackID(st.name), st.adsEnabled),
                                "pion",
                            )
                            if err != nil {
                                errorLogger.Printf("Station %s (adsEnabled: %v): Failed to reinitialize video track: %v", st.name, st.adsEnabled, err)
                                st.viewers = 0
                                close(st.stopCh)
                                st.stopCh = make(chan struct{})
                                st.mu.Unlock()
                                select {
                                case done <- struct{}{}:
                                default:
                                }
                                return
                            }
                            st.trackVideo = newTrackVideo
                            log.Printf("Station %s (adsEnabled: %v): Reinitialized video track", st.name, st.adsEnabled)
                            st.mu.Unlock()
                            select {
                            case done <- struct{}{}:
                            default:
                            }
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
                        select {
                        case done <- struct{}{}:
                        default:
                        }
                        return
                    }
                    videoTimestamp += uint32(frameIntervalSeconds * float64(videoClockRate))
                    frameIdx++
                    if isFinalChunk && videoTimestamp >= expectedSamples {
                        break
                    }
                }
                st.mu.Lock()
                st.currentVideoRTPTS = videoTimestamp
                st.mu.Unlock()
                log.Printf("Station %s (adsEnabled: %v): Completed video transmission for %s, final videoTS=%d", st.name, st.adsEnabled, segPath, videoTimestamp)
                select {
                case done <- struct{}{}:
                default:
                }
            }(nalus, currentVideoTS)
            go func(packets [][]byte, startTS uint32) {
                defer wg.Done()
                const sampleRate = 48000
                log.Printf("Station %s (adsEnabled: %v): Starting audio transmission for %s, %d packets, startTS=%d", st.name, st.adsEnabled, audioPath, len(packets), startTS)
                audioTimestamp := startTS
                boundChecked := false
                ticker := time.NewTicker(time.Millisecond * 20)
                defer ticker.Stop()
                packetIdx := 0
                expectedSamples := uint32(chunk.dur * float64(sampleRate))
                for range ticker.C {
                    if packetIdx >= len(packets) {
                        break
                    }
                    pkt := packets[packetIdx]
                    if len(pkt) < 1 {
                        errorLogger.Printf("Station %s (adsEnabled: %v): Empty audio packet %d for %s, skipping", st.name, st.adsEnabled, packetIdx, audioPath)
                        packetIdx++
                        continue
                    }
                    toc := pkt[0]
                    config := (toc >> 3) & 0x1F
                    baseFrameDurationMs := 20
                    switch config {
                    case 0, 1, 2, 3, 16, 17, 18, 19:
                        baseFrameDurationMs = 10
                    case 4, 5, 6, 7, 20, 21, 22, 23:
                        baseFrameDurationMs = 20
                    case 8, 9, 10, 11:
                        baseFrameDurationMs = 40
                    case 12, 13, 14, 15:
                        baseFrameDurationMs = 60
                    }
                    samples := uint32((baseFrameDurationMs * sampleRate) / 1000)
                    if !boundChecked {
                        if err := st.trackAudio.WriteSample(testSample); err != nil {
                            errorLogger.Printf("Station %s (adsEnabled: %v): Audio track not bound for sample %d in %s: %v", st.name, st.adsEnabled, packetIdx, audioPath, err)
                            st.mu.Lock()
                            newTrackAudio, err := webrtc.NewTrackLocalStaticSample(
                                webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
                                fmt.Sprintf("audio_%s_%t", sanitizeTrackID(st.name), st.adsEnabled),
                                "pion",
                            )
                            if err != nil {
                                errorLogger.Printf("Station %s (adsEnabled: %v): Failed to reinitialize audio track: %v", st.name, st.adsEnabled, err)
                                st.viewers = 0
                                close(st.stopCh)
                                st.stopCh = make(chan struct{})
                                st.mu.Unlock()
                                select {
                                case done <- struct{}{}:
                                default:
                                }
                                return
                            }
                            st.trackAudio = newTrackAudio
                            log.Printf("Station %s (adsEnabled: %v): Reinitialized audio track", st.name, st.adsEnabled)
                            st.mu.Unlock()
                            select {
                            case done <- struct{}{}:
                            default:
                            }
                            return
                        }
                        boundChecked = true
                    }
                    sample := media.Sample{
                        Data: pkt,
                        Duration: time.Duration(baseFrameDurationMs) * time.Millisecond,
                        PacketTimestamp: audioTimestamp,
                    }
                    if err := st.trackAudio.WriteSample(sample); err != nil {
                        errorLogger.Printf("Station %s (adsEnabled: %v): Audio sample %d write error for %s: %v", st.name, st.adsEnabled, packetIdx, audioPath, err)
                        st.mu.Lock()
                        st.currentAudioSamples = audioTimestamp
                        st.mu.Unlock()
                        select {
                        case done <- struct{}{}:
                        default:
                        }
                        return
                    }
                    audioTimestamp += samples
                    packetIdx++
                    if isFinalChunk && audioTimestamp >= expectedSamples {
                        break
                    }
                }
                st.mu.Lock()
                st.currentAudioSamples = audioTimestamp
                st.mu.Unlock()
                log.Printf("Station %s (adsEnabled: %v): Completed audio transmission for %s, final audioTS=%d", st.name, st.adsEnabled, audioPath, audioTimestamp)
                select {
                case done <- struct{}{}:
                default:
                }
            }(audioPackets, currentAudioTS)
            go func() {
                for i := 0; i < 2; i++ {
                    select {
                    case <-done:
                    case <-time.After(time.Duration(chunk.dur*float64(time.Second)) + 5*time.Second):
                        errorLogger.Printf("Station %s (adsEnabled: %v): Timeout waiting for audio/video transmission for %s", st.name, st.adsEnabled, segPath)
                    }
                }
                close(done)
            }()
            select {
            case <-done:
            case <-time.After(time.Duration(chunk.dur*float64(time.Second)) + 5*time.Second):
                errorLogger.Printf("Station %s (adsEnabled: %v): Timeout waiting for audio/video transmission completion for %s", st.name, st.adsEnabled, segPath)
            }
            st.mu.Lock()
            os.Remove(segPath)
            os.Remove(audioPath)
            if !chunk.isAd {
                st.currentOffset += chunk.dur
                log.Printf("Station %s (adsEnabled: %v): Updated offset to %.3fs for video %d after successful transmission", st.name, st.adsEnabled, st.currentOffset, st.currentVideo)
                if videoDur > 0 && (st.currentOffset >= videoDur || math.Abs(st.currentOffset-videoDur) < 0.001) {
                    log.Printf("Station %s (adsEnabled: %v): Completed video %d, advancing to next", st.name, st.adsEnabled, st.currentVideo)
                    st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                    st.currentVideo = st.videoQueue[st.currentIndex]
                    st.currentOffset = 0.0
                    st.spsPPS = nil
                    st.fmtpLine = ""
                    // Maintain RTP timestamps
                    log.Printf("Station %s (adsEnabled: %v): Transitioned to video %d with offset 0.0s, maintaining videoTS=%d, audioTS=%d", st.name, st.adsEnabled, st.currentVideo, currentVideoTS, currentAudioTS)
                }
            }
            st.segmentList = st.segmentList[1:]
            log.Printf("Station %s (adsEnabled: %v): Removed %s chunk %s, new segmentList: %v", st.name, st.adsEnabled, map[bool]string{true: "final", false: "chunk"}[isFinalChunk], segPath, st.segmentList)
            currentVideoTS = st.currentVideoRTPTS
            currentAudioTS = st.currentAudioSamples
            st.mu.Unlock()
            wg.Wait()
            time.Sleep(time.Millisecond * 20) // Reduced delay for smoother transitions
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
        SDP  string `json:"sdp,omitempty"`
    }
    if err := c.BindJSON(&msg); err != nil {
        log.Printf("JSON bind error: %v", err)
        c.JSON(400, gin.H{"error": err.Error()})
        return
    }
    m := &webrtc.MediaEngine{}
    if err := m.RegisterCodec(webrtc.RTPCodecParameters{
        RTPCodecCapability: webrtc.RTPCodecCapability{
            MimeType:     webrtc.MimeTypeH264,
            ClockRate:    90000,
            SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640034",
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
            MimeType:     webrtc.MimeTypeOpus,
            ClockRate:    48000,
            Channels:     2,
            SDPFmtpLine:  "minptime=10;useinbandfec=1;stereo=1",
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
    if !st.processing {
        st.processing = true
        if err := os.MkdirAll(HlsDir, 0755); err != nil {
            log.Printf("Station %s: Failed to create webrtc_segments directory: %v", st.name, err)
            st.viewers--
            st.mu.Unlock()
            c.JSON(500, gin.H{"error": "Failed to create webrtc_segments directory"})
            pc.Close()
            return
        }
        videoDur := getVideoDur(st.currentVideo, db)
        if videoDur <= 0 {
            log.Printf("Station %s: Invalid duration for initial video %d, advancing", st.name, st.currentVideo)
            st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
            st.currentVideo = st.videoQueue[st.currentIndex]
            st.currentOffset = 0.0
            videoDur = getVideoDur(st.currentVideo, db)
        }
        breaks, err := getBreakPoints(st.currentVideo, db)
        if err != nil {
            log.Printf("Station %s: Failed to get break points for initial video %d: %v", st.name, st.currentVideo, err)
            breaks = []float64{}
        }
        log.Printf("Station %s: Initial break points for video %d: %v", st.name, st.currentVideo, breaks)
        nextStart := st.currentOffset
        st.segmentList = nil // Initialize empty segmentList
        totalDur := 0.0
        const maxRetries = 3
        var nextBreak float64 = math.MaxFloat64
        for _, b := range breaks {
            if b > nextStart {
                nextBreak = b
                break
            }
        }
        for totalDur < BufferThreshold && nextStart < videoDur {
            chunkDur := ChunkDuration
            chunkEnd := nextStart + chunkDur
            if chunkEnd > videoDur {
                chunkEnd = videoDur
                chunkDur = videoDur - nextStart
            }
            if nextBreak < chunkEnd {
                chunkEnd = nextBreak
                chunkDur = nextBreak - nextStart
            }
            if chunkDur <= 0 {
                log.Printf("Station %s: Zero chunk duration at %.3fs, breaking", st.name, nextStart)
                break
            }
            var retryCount int
            var segments []string
            var spsPPS [][]byte
            var fmtpLine string
            var actualDur float64
            var fps fpsPair
            for retryCount < maxRetries {
                segments, spsPPS, fmtpLine, actualDur, fps, err = processVideo(st, st.currentVideo, db, nextStart, chunkDur)
                if err != nil {
                    log.Printf("Station %s: Failed to process initial chunk for video %d at %.3fs (retry %d/%d): %v", st.name, st.currentVideo, nextStart, retryCount+1, maxRetries, err)
                    if segments != nil && len(segments) > 0 {
                        os.Remove(segments[0])
                        os.Remove(strings.Replace(segments[0], ".h264", ".opus", 1))
                    }
                    retryCount++
                    if retryCount == maxRetries {
                        log.Printf("Station %s: Max retries failed for initial chunk at %.3fs, advancing video", st.name, nextStart)
                        st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                        st.currentVideo = st.videoQueue[st.currentIndex]
                        st.currentOffset = 0.0
                        st.spsPPS = nil
                        st.fmtpLine = ""
                        st.currentVideoRTPTS = 0
                        st.currentAudioSamples = 0
                        st.viewers--
                        st.mu.Unlock()
                        c.JSON(500, gin.H{"error": "Failed to process initial video chunk after retries"})
                        pc.Close()
                        return
                    }
                    continue
                }
                // Skip strict validation; trust processVideo output
                if len(st.spsPPS) == 0 {
                    st.spsPPS = spsPPS
                    st.fmtpLine = fmtpLine
                }
                st.segmentList = append(st.segmentList, bufferedChunk{
                    segPath: segments[0],
                    dur:     actualDur,
                    isAd:    false,
                    videoID: st.currentVideo,
                    fps:     fps,
                })
                log.Printf("Station %s: Queued initial chunk for video %d at %.3fs, duration %.3fs", st.name, st.currentVideo, nextStart, actualDur)
                totalDur += actualDur
                nextStart += actualDur
                // Update nextBreak for the next iteration
                nextBreak = math.MaxFloat64
                for _, b := range breaks {
                    if b > nextStart {
                        nextBreak = b
                        break
                    }
                }
                break
            }
            if retryCount == maxRetries {
                break
            }
        }
        if st.adsEnabled && totalDur < BufferThreshold && nextStart >= nextBreak && len(breaks) > 0 {
            log.Printf("Station %s: Inserting initial ad break at %.3fs for video %d", st.name, nextBreak, st.currentVideo)
            availableAds := make([]int64, len(adIDs))
            copy(availableAds, adIDs)
            adDurTotal := 0.0
            for i := 0; i < 3 && totalDur < BufferThreshold; i++ {
                if len(availableAds) == 0 {
                    log.Printf("Station %s: No more available ads, stopping at %d of 3", st.name, i)
                    break
                }
                idx := rand.Intn(len(availableAds))
                adID := availableAds[idx]
                adDur := getVideoDur(adID, db)
                if adDur <= 0 {
                    errorLogger.Printf("Station %s: Invalid duration for ad %d, skipping", st.name, adID)
                    availableAds = append(availableAds[:idx], availableAds[idx+1:]...)
                    continue
                }
                var retryCount int
                for retryCount < maxAdRetries {
                    segments, spsPPS, fmtpLine, actualDur, fps, err := processVideo(st, adID, db, 0, adDur)
                    if err != nil {
                        log.Printf("Station %s: Failed to process initial ad %d (retry %d/%d): %v", st.name, adID, retryCount+1, maxAdRetries, err)
                        if segments != nil && len(segments) > 0 {
                            os.Remove(segments[0])
                            os.Remove(strings.Replace(segments[0], ".h264", ".opus", 1))
                        }
                        retryCount++
                        if retryCount == maxAdRetries {
                            errorLogger.Printf("Station %s: All %d retries failed for ad %d, skipping", st.name, maxAdRetries, adID)
                            break
                        }
                        continue
                    }
                    data, err := os.ReadFile(segments[0])
                    if err != nil || len(data) == 0 {
                        log.Printf("Station %s: Invalid ad segment %s: read error or empty (retry %d/%d): %v", st.name, segments[0], retryCount+1, maxAdRetries, err)
                        os.Remove(segments[0])
                        os.Remove(strings.Replace(segments[0], ".h264", ".opus", 1))
                        retryCount++
                        if retryCount == maxAdRetries {
                            errorLogger.Printf("Station %s: All %d retries failed for ad %d due to invalid segment, skipping", st.name, maxAdRetries, adID)
                            break
                        }
                        continue
                    }
                    nalus := splitNALUs(data)
                    if len(nalus) == 0 {
                        log.Printf("Station %s: No NALUs in ad segment %s (retry %d/%d)", st.name, segments[0], retryCount+1, maxAdRetries)
                        os.Remove(segments[0])
                        os.Remove(strings.Replace(segments[0], ".h264", ".opus", 1))
                        retryCount++
                        if retryCount == maxAdRetries {
                            errorLogger.Printf("Station %s: All %d retries failed for ad %d due to no NALUs, skipping", st.name, maxAdRetries, adID)
                            break
                        }
                        continue
                    }
                    audioPath := strings.Replace(segments[0], ".h264", ".opus", 1)
                    if _, err := os.Stat(audioPath); os.IsNotExist(err) {
                        log.Printf("Station %s: Ad audio file %s not found (retry %d/%d)", st.name, audioPath, retryCount+1, maxAdRetries)
                        os.Remove(segments[0])
                        os.Remove(audioPath)
                        retryCount++
                        if retryCount == maxAdRetries {
                            errorLogger.Printf("Station %s: All %d retries failed for ad %d due to missing audio, skipping", st.name, maxAdRetries, adID)
                            break
                        }
                        continue
                    }
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
                    }
                    st.segmentList = append(st.segmentList, adChunk)
                    totalDur += actualDur
                    adDurTotal += actualDur
                    log.Printf("Station %s: Queued initial ad %d with duration %.3fs at break %.3fs", st.name, adID, actualDur, nextBreak)
                    availableAds = append(availableAds[:idx], availableAds[idx+1:]...)
                    break
                }
            }
            if adDurTotal > 0 {
                log.Printf("Station %s: Queued %f seconds of ads at break %.3fs", st.name, adDurTotal, nextBreak)
            } else {
                log.Printf("Station %s: Failed to queue any initial ads at break %.3fs", st.name, nextBreak)
            }
        }
        log.Printf("Station %s: Initial segmentList: %v", st.name, st.segmentList)
        go manageProcessing(st, db)
        go sender(st, db)
    }
    st.mu.Unlock()
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
    rows, err := db.Query("SELECT id, uri FROM videos WHERE duration IS NULL OR loudnorm_input_i IS NULL")
    if err != nil {
        return fmt.Errorf("failed to query videos with NULL duration or loudnorm: %v", err)
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
        log.Println("No videos with NULL duration or loudnorm found")
        return nil
    }
    log.Printf("Found %d videos with NULL duration or loudnorm, calculating metadata", len(videoIDs))
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
            if err != nil || !duration.Valid {
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
            err = db.QueryRow("SELECT loudnorm_input_i IS NULL FROM videos WHERE id = $1", id).Scan(&needsLoudnorm)
            if err != nil {
                mu.Lock()
                errors = append(errors, fmt.Errorf("failed to check loudnorm for video %d (%s): %v", id, fullPath, err))
                mu.Unlock()
                return
            }
            if needsLoudnorm {
                cmdLoudnorm := exec.Command(
                    "ffmpeg",
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
                jsonStart := strings.LastIndex(outputStr, "{")
                if jsonStart == -1 {
                    mu.Lock()
                    errors = append(errors, fmt.Errorf("no JSON found in loudnorm output for video %d (%s)", id, fullPath))
                    mu.Unlock()
                    return
                }
                jsonStr := outputStr[jsonStart:]
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
            }
        }(videoID, uris[i])
    }
    wg.Wait()
    if len(errors) > 0 {
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