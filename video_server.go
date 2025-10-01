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

const (
    HlsDir                      = "./webrtc_segments"
    Port                        = ":8081"
    ClockRate                   = 90000
    AudioFrameMs                = 20
    DefaultFPSNum               = 30000
    DefaultFPSDen               = 1001
    DefaultDur                  = 0.0
    DefaultStation              = "default"
    AdInsertPath                = "./ad_insert.exe"
    DefaultVideoBaseDir         = "Z:/Videos"
    DefaultTempPrefix           = "ad_insert_"
    ChunkDuration               = 30.0  // Process 30-second chunks
    BufferThreshold             = 60.0  // Start processing more chunks when buffer < 60s
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
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("database connection is nil")
    }
    var segments []string
    var spsPPS [][]byte
    var fmtpLine string
    var uri string
    err := db.QueryRow(`SELECT uri FROM videos WHERE id = $1`, videoID).Scan(&uri)
    if err != nil {
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("failed to get URI for video %d: %v", videoID, err)
    }
    fullEpisodePath := filepath.Join(videoBaseDir, uri)
    if _, err := os.Stat(fullEpisodePath); err != nil {
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("episode file not found: %s", fullEpisodePath)
    }
    var duration sql.NullFloat64
    err = db.QueryRow(`SELECT duration FROM videos WHERE id = $1`, videoID).Scan(&duration)
    if err != nil {
        log.Printf("Station %s: Failed to get duration for video %d: %v", st.name, videoID, err)
    }
    adjustedChunkDur := chunkDur
    if duration.Valid && startTime+chunkDur > duration.Float64 {
        adjustedChunkDur = duration.Float64 - startTime
        if adjustedChunkDur <= 0 {
            return nil, nil, "", 0, fpsPair{}, fmt.Errorf("no remaining duration for video %d at start time %f", videoID, startTime)
        }
    }
    tempDir, err := os.MkdirTemp("", DefaultTempPrefix)
    if err != nil {
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("failed to create temp dir for video %d: %v", videoID, err)
    }
    defer os.RemoveAll(tempDir)
    if err := os.MkdirAll(HlsDir, 0755); err != nil {
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("failed to create webrtc_segments directory: %v", err)
    }
    safeStationName := strings.ReplaceAll(st.name, " ", "_")
    baseName := fmt.Sprintf("%s_vid%d_chunk_%.3f", safeStationName, videoID, startTime)
    segName := baseName + ".h264"
    fullSegPath := filepath.Join(HlsDir, segName)
    opusName := baseName + ".opus"
    opusPath := filepath.Join(HlsDir, opusName)
    // Process audio
    cmdProbe := exec.Command(
        "ffprobe",
        "-v", "error",
        "-select_streams", "a:0",
        "-show_entries", "stream=sample_rate,channels",
        "-of", "default=noprint_wrappers=1",
        fullEpisodePath,
    )
    outputProbe, err := cmdProbe.Output()
    if err == nil {
        log.Printf("Station %s: Input audio for %s: %s", st.name, fullEpisodePath, strings.TrimSpace(string(outputProbe)))
    } else {
        log.Printf("Station %s: Failed to probe input audio for %s: %v", st.name, fullEpisodePath, err)
    }
    args := []string{
        "-y",
        "-ss", fmt.Sprintf("%.3f", startTime),
        "-i", fullEpisodePath,
        "-t", fmt.Sprintf("%.3f", adjustedChunkDur),
        "-map", "0:a:0",
        "-c:a", "libopus",
        "-b:a", "192k",
        "-ar", "48000",
        "-ac", "2",
        "-frame_duration", "20",
        "-page_duration", "20000",
        "-application", "audio",
        "-avoid_negative_ts", "make_zero",
        "-fflags", "+genpts",
        "-f", "opus",
        opusPath,
    }
    cmdAudio := exec.Command("ffmpeg", args...)
    outputAudio, err := cmdAudio.CombinedOutput()
    if err != nil {
        log.Printf("Station %s: ffmpeg audio command failed: %v\nOutput: %s", st.name, err, string(outputAudio))
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("ffmpeg audio failed for video %d at %fs: %v", videoID, startTime, err)
    }
    log.Printf("Station %s: ffmpeg audio succeeded for %s", st.name, opusPath)
    audioData, err := os.ReadFile(opusPath)
    if err != nil || len(audioData) == 0 {
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("audio file %s is empty or unreadable: %v", opusPath, err)
    }
    log.Printf("Station %s: Read audio file %s, size: %d bytes", st.name, opusPath, len(audioData))
    // Process video with optimized settings
    args = []string{
        "-y",
        "-ss", fmt.Sprintf("%.3f", startTime),
        "-i", fullEpisodePath,
        "-t", fmt.Sprintf("%.3f", adjustedChunkDur),
        "-c:v", "libx264",
        "-preset", "ultrafast", // Revert to ultrafast for performance
        "-crf", "23", // Add CRF for quality control
        "-force_key_frames", "expr:gte(t,n_forced*2)", // Keyframe every 2 seconds
        "-sc_threshold", "0", // Disable scene change detection
        "-an",
        "-bsf:v", "h264_mp4toannexb",
        "-g", "60", // GOP size of 60 frames (~2 seconds at 30fps)
        fullSegPath,
    }
    cmdVideo := exec.Command("ffmpeg", args...)
    outputVideo, err := cmdVideo.CombinedOutput()
    if err != nil {
        log.Printf("Station %s: ffmpeg video command failed: %v\nOutput: %s", st.name, err, string(outputVideo))
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("ffmpeg video failed for video %d at %fs: %v", videoID, startTime, err)
    }
    segments = append(segments, fullSegPath)
    // Get FPS
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
        log.Printf("Station %s: ffprobe failed for original %s: %v", st.name, fullEpisodePath, err)
    }
    // Get actual duration
    cmdDur := exec.Command(
        "ffprobe",
        "-v", "error",
        "-select_streams", "v:0",
        "-count_frames",
        "-show_entries", "stream=nb_frames,r_frame_rate",
        "-of", "json",
        fullSegPath,
    )
    outputDur, err := cmdDur.Output()
    var actualDur float64
    if err == nil {
        var result struct {
            Streams []struct {
                NbFrames string `json:"nb_frames"`
                FrameRate string `json:"r_frame_rate"`
            } `json:"streams"`
        }
        if err := json.Unmarshal(outputDur, &result); err != nil {
            log.Printf("Station %s: Failed to parse ffprobe JSON for %s: %v, falling back to format duration", st.name, fullSegPath, err)
        } else if len(result.Streams) > 0 && result.Streams[0].NbFrames != "" {
            nbFrames, err := strconv.Atoi(result.Streams[0].NbFrames)
            if err != nil {
                log.Printf("Station %s: Failed to parse nb_frames '%s' for %s: %v, falling back to format duration", st.name, result.Streams[0].NbFrames, fullSegPath, err)
            } else {
                rate := result.Streams[0].FrameRate
                var fpsNum, fpsDen int
                if slash := strings.Index(rate, "/"); slash != -1 {
                    fpsNum, _ = strconv.Atoi(rate[:slash])
                    fpsDen, _ = strconv.Atoi(rate[slash+1:])
                } else {
                    fpsNum, _ = strconv.Atoi(rate)
                    fpsDen = 1
                }
                if fpsNum > 0 && fpsDen > 0 {
                    actualDur = float64(nbFrames) * float64(fpsDen) / float64(fpsNum)
                    log.Printf("Station %s: Video segment %s duration: %.3fs (calculated from %d frames at %s fps)", st.name, fullSegPath, actualDur, nbFrames, rate)
                    if actualDur < adjustedChunkDur*0.9 || actualDur > adjustedChunkDur*1.1 {
                        log.Printf("Station %s: Warning: Video duration %.3fs does not match expected %.3fs", st.name, actualDur, adjustedChunkDur)
                    }
                } else {
                    log.Printf("Station %s: Invalid frame rate %s for %s, falling back to format duration", st.name, rate, fullSegPath)
                }
            }
        } else {
            log.Printf("Station %s: No valid streams or nb_frames in ffprobe output for %s, falling back to format duration", st.name, fullSegPath)
        }
    }
    if actualDur == 0 {
        cmdDur = exec.Command(
            "ffprobe",
            "-v", "error",
            "-show_entries", "format=duration",
            "-of", "default=noprint_wrappers=1:nokey=1",
            fullSegPath,
        )
        outputDur, err = cmdDur.Output()
        if err == nil {
            durStr := strings.TrimSpace(string(outputDur))
            actualDur, err = strconv.ParseFloat(durStr, 64)
            if err != nil {
                log.Printf("Station %s: Failed to parse format duration for %s: %v, falling back to source duration", st.name, fullSegPath, err)
            } else {
                log.Printf("Station %s: Video segment %s duration: %.3fs (from format)", st.name, fullSegPath, actualDur)
                if actualDur < adjustedChunkDur*0.9 || actualDur > adjustedChunkDur*1.1 {
                    log.Printf("Station %s: Warning: Video duration %.3fs does not match expected %.3fs", st.name, actualDur, adjustedChunkDur)
                }
            }
        } else {
            log.Printf("Station %s: ffprobe format duration failed for %s: %v, falling back to source duration", st.name, fullSegPath, err)
        }
    }
    if actualDur == 0 {
        cmdDur = exec.Command(
            "ffprobe",
            "-v", "error",
            "-ss", fmt.Sprintf("%.3f", startTime),
            "-t", fmt.Sprintf("%.3f", adjustedChunkDur),
            "-show_entries", "format=duration",
            "-of", "default=noprint_wrappers=1:nokey=1",
            fullEpisodePath,
        )
        outputDur, err = cmdDur.Output()
        if err == nil {
            durStr := strings.TrimSpace(string(outputDur))
            actualDur, err = strconv.ParseFloat(durStr, 64)
            if err != nil {
                log.Printf("Station %s: Failed to parse source duration for %s at %.3fs: %v, using adjustedChunkDur %.3f", st.name, fullEpisodePath, startTime, err, adjustedChunkDur)
                actualDur = adjustedChunkDur
            } else {
                log.Printf("Station %s: Video segment %s duration: %.3fs (from source at %.3fs)", st.name, fullSegPath, actualDur, startTime)
                if actualDur < adjustedChunkDur*0.9 || actualDur > adjustedChunkDur*1.1 {
                    log.Printf("Station %s: Warning: Video duration %.3fs does not match expected %.3fs", st.name, actualDur, adjustedChunkDur)
                }
            }
        } else {
            log.Printf("Station %s: ffprobe source duration failed for %s at %.3fs: %v, using adjustedChunkDur %.3f", st.name, fullEpisodePath, startTime, err, adjustedChunkDur)
            actualDur = adjustedChunkDur
        }
    }
    // Check audio duration and re-encode only if significantly mismatched
    cmdAudioDur := exec.Command(
        "ffprobe",
        "-v", "error",
        "-show_entries", "format=duration",
        "-of", "default=noprint_wrappers=1:nokey=1",
        opusPath,
    )
    outputAudioDur, err := cmdAudioDur.Output()
    var audioDur float64
    if err == nil {
        durStr := strings.TrimSpace(string(outputAudioDur))
        audioDur, err = strconv.ParseFloat(durStr, 64)
        if err != nil {
            log.Printf("Station %s: Failed to parse audio duration for %s: %v, assuming video duration %.3fs", st.name, opusPath, err, actualDur)
            audioDur = actualDur
        } else {
            log.Printf("Station %s: Audio segment %s duration: %.3fs", st.name, opusPath, audioDur)
        }
    } else {
        log.Printf("Station %s: ffprobe audio duration failed for %s: %v, assuming video duration %.3fs", st.name, opusPath, err, actualDur)
        audioDur = actualDur
    }
    if math.Abs(audioDur-actualDur) > 0.5 { // Increased threshold to 0.5s
        log.Printf("Station %s: Audio duration %.3fs significantly differs from video duration %.3fs, re-encoding audio", st.name, audioDur, actualDur)
        args = []string{
            "-y",
            "-ss", fmt.Sprintf("%.3f", startTime),
            "-i", fullEpisodePath,
            "-t", fmt.Sprintf("%.3f", actualDur),
            "-map", "0:a:0",
            "-c:a", "libopus",
            "-b:a", "192k",
            "-ar", "48000",
            "-ac", "2",
            "-frame_duration", "20",
            "-page_duration", "20000",
            "-application", "audio",
            "-avoid_negative_ts", "make_zero",
            "-fflags", "+genpts",
            "-f", "opus",
            opusPath,
        }
        cmdAudio := exec.Command("ffmpeg", args...)
        outputAudio, err = cmdAudio.CombinedOutput()
        if err != nil {
            log.Printf("Station %s: ffmpeg audio re-encode failed: %v\nOutput: %s", st.name, err, string(outputAudio))
            return nil, nil, "", 0, fpsPair{}, fmt.Errorf("ffmpeg audio re-encode failed for video %d at %fs: %v", videoID, startTime, err)
        }
        log.Printf("Station %s: Re-encoded audio for %s to match video duration %.3fs", st.name, opusPath, actualDur)
    }
    data, err := os.ReadFile(fullSegPath)
    if err != nil || len(data) == 0 {
        log.Printf("Station %s: Failed to read video segment %s: %v", st.name, fullSegPath, err)
        return nil, nil, "", 0, fpsPair{}, fmt.Errorf("failed to read video segment %s: %v", fullSegPath, err)
    }
    nalus := splitNALUs(data)
    if len(nalus) == 0 {
        log.Printf("Station %s: No NALUs found in segment %s", st.name, fullSegPath)
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
    if !hasIDR {
        log.Printf("Station %s: No IDR frame in segment %s, attempting repair", st.name, fullSegPath)
        repairedSegPath := filepath.Join(tempDir, baseName+"_repaired.h264")
        args = []string{
            "-y",
            "-i", fullSegPath,
            "-c:v", "libx264",
            "-preset", "ultrafast",
            "-crf", "23",
            "-force_key_frames", "expr:gte(t,n_forced*2)",
            "-sc_threshold", "0",
            "-bsf:v", "h264_mp4toannexb",
            "-g", "60",
            "-f", "h264",
            repairedSegPath,
        }
        cmdRepair := exec.Command("ffmpeg", args...)
        outputRepair, err := cmdRepair.CombinedOutput()
        if err != nil {
            log.Printf("Station %s: Failed to repair segment %s: %v\nOutput: %s", st.name, fullSegPath, err, string(outputRepair))
            // Continue with original segment to avoid breaking pipeline
        } else {
            if err := os.Rename(repairedSegPath, fullSegPath); err != nil {
                log.Printf("Station %s: Failed to replace %s with repaired segment: %v", st.name, fullSegPath, err)
            } else {
                log.Printf("Station %s: Successfully repaired segment %s with IDR frames", st.name, fullSegPath)
                data, err = os.ReadFile(fullSegPath)
                if err != nil || len(data) == 0 {
                    log.Printf("Station %s: Failed to read repaired video segment %s: %v", st.name, fullSegPath, err)
                    return nil, nil, "", 0, fpsPair{}, fmt.Errorf("failed to read repaired video segment %s: %v", fullSegPath, err)
                }
                nalus = splitNALUs(data)
                if len(nalus) == 0 {
                    log.Printf("Station %s: No NALUs found in repaired segment %s", st.name, fullSegPath)
                    return nil, nil, "", 0, fpsPair{}, fmt.Errorf("no NALUs found in repaired segment %s", fullSegPath)
                }
                hasIDR = false
                for _, nalu := range nalus {
                    if len(nalu) > 0 && int(nalu[0]&0x1F) == 5 {
                        hasIDR = true
                        break
                    }
                }
                if !hasIDR {
                    log.Printf("Station %s: Still no IDR frame in repaired segment %s, playback may fail", st.name, fullSegPath)
                }
            }
        }
    }
    if len(spsPPS) > 0 {
        sps := spsPPS[0]
        if len(sps) >= 4 {
            profileIDC := sps[1]
            constraints := sps[2]
            levelIDC := sps[3]
            profileLevelID := fmt.Sprintf("%02x%02x%02x", profileIDC, constraints, levelIDC)
            fmtpLine = fmt.Sprintf("level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=%s", profileLevelID)
        }
    } else {
        log.Printf("Station %s: No SPS/PPS found in segment %s, using default", st.name, fullSegPath)
        fmtpLine = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f"
    }
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

func manageProcessing(st *Station, db *sql.DB) {
    for {
        select {
        case <-st.stopProcessing:
            log.Printf("Station %s: Stopping processing due to no viewers", st.name)
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
                st.mu.Unlock()
                time.Sleep(time.Second)
                continue
            }
            remainingDur := 0.0
            sumNonAd := 0.0
            for _, chunk := range st.segmentList {
                remainingDur += chunk.dur
                if !chunk.isAd {
                    sumNonAd += chunk.dur
                }
            }
            log.Printf("Station %s: Remaining buffer %.3fs, non-ad %.3fs, current video %d, offset %.3f", st.name, remainingDur, sumNonAd, st.currentVideo, st.currentOffset)
            for remainingDur < BufferThreshold {
                videoDur := getVideoDur(st.currentVideo, db)
                if videoDur <= 0 {
                    log.Printf("Station %s: Invalid duration for video %d, advancing to next video", st.name, st.currentVideo)
                    st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                    st.currentVideo = st.videoQueue[st.currentIndex]
                    st.currentOffset = 0.0
                    st.spsPPS = nil
                    st.fmtpLine = ""
                    sumNonAd = 0.0
                    continue
                }
                nextStart := st.currentOffset + sumNonAd
                if nextStart >= videoDur {
                    log.Printf("Station %s: Reached end of video %d (%.3fs >= %.3fs), advancing", st.name, st.currentVideo, nextStart, videoDur)
                    st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                    st.currentVideo = st.videoQueue[st.currentIndex]
                    st.currentOffset = 0.0
                    st.spsPPS = nil
                    st.fmtpLine = ""
                    sumNonAd = 0.0
                    continue
                }
                breaks, err := getBreakPoints(st.currentVideo, db)
                if err != nil {
                    log.Printf("Station %s: Failed to get break points for video %d: %v", st.name, st.currentVideo, err)
                    breaks = []float64{}
                }
                log.Printf("Station %s: Break points for video %d: %v", st.name, st.currentVideo, breaks)
                var nextBreak float64 = math.MaxFloat64
                for _, b := range breaks {
                    if b > nextStart {
                        nextBreak = b
                        break
                    }
                }
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
                if chunkDur > 0 {
                    segments, spsPPS, fmtpLine, actualDur, fps, err := processVideo(st, st.currentVideo, db, nextStart, chunkDur)
                    if err != nil {
                        log.Printf("Station %s: Failed to process episode chunk for video %d at %.3fs: %v", st.name, st.currentVideo, nextStart, err)
                        st.mu.Unlock()
                        time.Sleep(5 * time.Second)
                        continue
                    }
                    if len(st.spsPPS) == 0 {
                        st.spsPPS = spsPPS
                        st.fmtpLine = fmtpLine
                    }
                    newChunk := bufferedChunk{
                        segPath: segments[0],
                        dur:     actualDur,
                        isAd:    false,
                        videoID: st.currentVideo,
                        fps:     fps,
                    }
                    st.segmentList = append(st.segmentList, newChunk)
                    remainingDur += actualDur
                    sumNonAd += actualDur
                    log.Printf("Station %s: Queued episode chunk for video %d at %.3fs, duration %.3fs", st.name, st.currentVideo, nextStart, actualDur)
                }
                if nextBreak == chunkEnd && len(breaks) > 0 {
                    log.Printf("Station %s: Inserting ad break at %.3fs for video %d", st.name, nextBreak, st.currentVideo)
                    availableAds := make([]int64, len(adIDs))
                    copy(availableAds, adIDs)
                    for i := 0; i < 3 && len(availableAds) > 0; i++ {
                        idx := rand.Intn(len(availableAds))
                        adID := availableAds[idx]
                        adDur := getVideoDur(adID, db)
                        if adDur <= 0 {
                            log.Printf("Station %s: Invalid duration for ad %d, skipping", st.name, adID)
                            availableAds = append(availableAds[:idx], availableAds[idx+1:]...)
                            i--
                            continue
                        }
                        segments, spsPPS, fmtpLine, actualDur, fps, err := processVideo(st, adID, db, 0, adDur)
                        if err != nil {
                            log.Printf("Station %s: Failed to process ad %d: %v", st.name, adID, err)
                            availableAds = append(availableAds[:idx], availableAds[idx+1:]...)
                            i--
                            continue
                        }
                        if len(st.spsPPS) == 0 {
                            st.spsPPS = spsPPS
                            st.fmtpLine = fmtpLine
                        }
                        adChunk := bufferedChunk{
                            segPath: segments[0],
                            dur:     actualDur,
                            isAd:    true,
                            videoID: adID,
                            fps:     fps,
                        }
                        st.segmentList = append(st.segmentList, adChunk)
                        remainingDur += actualDur
                        log.Printf("Station %s: Queued ad %d with duration %.3fs at break %.3fs", st.name, adID, actualDur, nextBreak)
                        availableAds = append(availableAds[:idx], availableAds[idx+1:]...)
                    }
                }
            }
            st.mu.Unlock()
            time.Sleep(time.Second)
        }
    }
}

func loadStation(stationName string, db *sql.DB) *Station {
    st := &Station{
        name:           stationName,
        currentIndex:   0,
        viewers:        0,
        stopProcessing: make(chan struct{}),
    }
    var unixStart int64
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
    var videoIds []int64
    for rows.Next() {
        var vid int64
        if err := rows.Scan(&vid); err != nil {
            log.Printf("Failed to scan video_id for station %s: %v", stationName, err)
            continue
        }
        videoIds = append(videoIds, vid)
    }
    if len(videoIds) == 0 {
        log.Printf("No videos found for station %s", stationName)
        return nil
    }
    st.videoQueue = videoIds
    currentTime := time.Now().Unix()
    elapsedSeconds := float64(currentTime - unixStart)
    totalQueueDuration, err := getQueueDuration(videoIds, db)
    if err != nil || totalQueueDuration <= 0 {
        log.Printf("Failed to calculate queue duration for station %s: %v", stationName, err)
        return nil
    }
    loops := int(elapsedSeconds / totalQueueDuration)
    remainingSeconds := math.Mod(elapsedSeconds, totalQueueDuration)
    log.Printf("Station %s: Elapsed %f seconds, %d loops, remaining %f seconds", stationName, elapsedSeconds, loops, remainingSeconds)
    currentOffset := remainingSeconds
    currentVideoIndex := 0
    var currentVideoID int64
    for i, vid := range videoIds {
        var hasCommercialTag bool
        err = db.QueryRow(
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
        log.Printf("No valid non-ad video found for station %s, defaulting to first video", stationName)
        currentVideoID = videoIds[0]
        currentOffset = 0
    }
    st.currentVideo = currentVideoID
    st.currentIndex = currentVideoIndex
    st.currentOffset = currentOffset
    st.trackVideo, err = webrtc.NewTrackLocalStaticSample(
        webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
        "video_"+sanitizeTrackID(stationName),
        "pion",
    )
    if err != nil {
        log.Printf("Station %s: Failed to create video track: %v", stationName, err)
        return nil
    }
    st.trackAudio, err = webrtc.NewTrackLocalStaticSample(
        webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
        "audio_"+sanitizeTrackID(stationName),
        "pion",
    )
    if err != nil {
        log.Printf("Station %s: Failed to create audio track: %v", stationName, err)
        return nil
    }
    log.Printf("Station %s: Initialized at video %d (index %d) with offset %f seconds", stationName, currentVideoID, currentVideoIndex, currentOffset)
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

func sender(st *Station, db *sql.DB) {
    for {
        st.mu.Lock()
        if st.viewers == 0 || len(st.segmentList) == 0 {
            log.Printf("Station %s: No viewers or empty segment list, waiting", st.name)
            st.mu.Unlock()
            time.Sleep(500 * time.Millisecond)
            continue
        }
        chunk := st.segmentList[0]
        segPath := chunk.segPath
        log.Printf("Station %s: Sending chunk %s (video %d, isAd: %v, duration: %.3fs)", st.name, segPath, chunk.videoID, chunk.isAd, chunk.dur)
        st.mu.Unlock()
        testSample := media.Sample{Data: []byte{}, Duration: time.Duration(0)}
        err := st.trackVideo.WriteSample(testSample)
        if err != nil {
            log.Printf("Station %s: Video track write test error: %v", st.name, err)
            if strings.Contains(err.Error(), "not bound") {
                log.Printf("Station %s: Video track not bound, checking viewers", st.name)
                st.mu.Lock()
                if st.viewers == 0 {
                    close(st.stopProcessing)
                    st.stopProcessing = make(chan struct{})
                }
                st.mu.Unlock()
                time.Sleep(500 * time.Millisecond)
                continue
            }
        }
        data, err := os.ReadFile(segPath)
        if err != nil || len(data) == 0 {
            log.Printf("Station %s: Segment %s read error or empty: %v", st.name, segPath, err)
            st.mu.Lock()
            st.segmentList = st.segmentList[1:]
            st.mu.Unlock()
            os.Remove(segPath)
            opusSeg := strings.Replace(segPath, ".h264", ".opus", 1)
            os.Remove(opusSeg)
            continue
        }
        if chunk.dur <= 0 {
            log.Printf("Station %s: Skipping chunk %s with zero duration", st.name, segPath)
            st.mu.Lock()
            st.segmentList = st.segmentList[1:]
            st.mu.Unlock()
            os.Remove(segPath)
            opusSeg := strings.Replace(segPath, ".h264", ".opus", 1)
            os.Remove(opusSeg)
            continue
        }
        fpsNum := chunk.fps.num
        fpsDen := chunk.fps.den
        nalus := splitNALUs(data)
        if len(nalus) == 0 {
            log.Printf("Station %s: No NALUs found in segment %s", st.name, segPath)
            st.mu.Lock()
            st.segmentList = st.segmentList[1:]
            st.mu.Unlock()
            os.Remove(segPath)
            opusSeg := strings.Replace(segPath, ".h264", ".opus", 1)
            os.Remove(opusSeg)
            continue
        }
        audioPath := strings.Replace(segPath, ".h264", ".opus", 1)
        var audioPackets [][]byte
        if audioData, err := os.ReadFile(audioPath); err == nil && len(audioData) > 0 {
            reader := bytes.NewReader(audioData)
            ogg, _, err := oggreader.NewWith(reader)
            if err != nil {
                log.Printf("Station %s: Failed to create ogg reader for %s: %v", st.name, audioPath, err)
            } else {
                for {
                    payload, _, err := ogg.ParseNextPage()
                    if err == io.EOF {
                        break
                    }
                    if err != nil {
                        log.Printf("Station %s: Failed to parse ogg page for %s: %v", st.name, audioPath, err)
                        continue
                    }
                    if len(payload) < 1 {
                        log.Printf("Station %s: Empty ogg payload for %s, skipping", st.name, audioPath)
                        continue
                    }
                    if len(payload) >= 8 && (string(payload[:8]) == "OpusHead" || string(payload[:8]) == "OpusTags") {
                        log.Printf("Station %s: Skipping header packet: %s", st.name, string(payload[:8]))
                        continue
                    }
                    audioPackets = append(audioPackets, payload)
                }
                log.Printf("Station %s: Parsed %d audio packets for %s", st.name, len(audioPackets), audioPath)
            }
        } else {
            log.Printf("Station %s: Failed to read audio %s: %v", st.name, audioPath, err)
        }
        var wg sync.WaitGroup
        if len(audioPackets) > 0 {
            wg.Add(1)
            go func(packets [][]byte) {
                defer wg.Done()
                audioStart := time.Now()
                boundChecked := false
                audioTimestamp := uint32(0)
                const sampleRate = 48000
                log.Printf("Station %s: Starting audio transmission for %s, %d packets", st.name, audioPath, len(packets))
                for i, pkt := range packets {
                    if len(pkt) < 1 {
                        log.Printf("Station %s: Empty audio packet %d, skipping", st.name, i)
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
                    samples := (baseFrameDurationMs * sampleRate) / 1000
                    if !boundChecked {
                        if err := st.trackAudio.WriteSample(testSample); err != nil {
                            log.Printf("Station %s: Audio track not bound for sample %d: %v", st.name, i, err)
                            break
                        }
                        boundChecked = true
                    }
                    sample := media.Sample{
                        Data: pkt,
                        Duration: time.Duration(baseFrameDurationMs) * time.Millisecond,
                        PacketTimestamp: audioTimestamp,
                    }
                    if err := st.trackAudio.WriteSample(sample); err != nil {
                        log.Printf("Station %s: Audio sample %d write error: %v, packet size: %d", st.name, i, err, len(pkt))
                        break
                    }
                    audioTimestamp += uint32(samples)
                    targetTime := audioStart.Add(time.Duration(audioTimestamp*1000/sampleRate) * time.Millisecond)
                    now := time.Now()
                    if now.Before(targetTime) {
                        time.Sleep(targetTime.Sub(now))
                    }
                }
                log.Printf("Station %s: Sent %d audio packets for %s, total timestamp: %d samples", st.name, len(packets), audioPath, audioTimestamp)
            }(audioPackets)
        } else {
            log.Printf("Station %s: No audio packets to send for %s", st.name, audioPath)
        }
        wg.Add(1)
        go func(nalus [][]byte) {
            defer wg.Done()
            var allNALUs [][]byte
            if len(st.spsPPS) > 0 {
                allNALUs = append(st.spsPPS, nalus...)
                log.Printf("Station %s: Prefixed %d config NALUs to segment %s", st.name, len(st.spsPPS), segPath)
            } else {
                allNALUs = nalus
            }
            videoStart := time.Now()
            videoTimestamp := uint32(0)
            const videoClockRate = 90000
            segmentSamples := 0
            var currentFrame [][]byte
            var hasVCL bool
            boundChecked := false
            // Estimate number of frames for smoother timing
            expectedFrames := int(math.Ceil(chunk.dur * float64(fpsNum) / float64(fpsDen)))
            if expectedFrames == 0 {
                expectedFrames = 1
            }
            frameInterval := time.Duration(float64(chunk.dur) / float64(expectedFrames) * float64(time.Second))
            log.Printf("Station %s: Sending %s with %d expected frames, frame interval %v", st.name, segPath, expectedFrames, frameInterval)
            for _, nalu := range allNALUs {
                if len(nalu) == 0 {
                    log.Printf("Station %s: Empty NALU in segment %s, skipping", st.name, segPath)
                    continue
                }
                nalType := int(nalu[0] & 0x1F)
                isVCL := nalType >= 1 && nalType <= 5
                if hasVCL && !isVCL {
                    targetTime := videoStart.Add(time.Duration(videoTimestamp*1000/videoClockRate) * time.Millisecond)
                    now := time.Now()
                    if now.Before(targetTime) {
                        time.Sleep(targetTime.Sub(now))
                    }
                    var frameData bytes.Buffer
                    for _, n := range currentFrame {
                        frameData.Write([]byte{0x00, 0x00, 0x00, 0x01})
                        frameData.Write(n)
                    }
                    sampleData := frameData.Bytes()
                    if !boundChecked {
                        if err := st.trackVideo.WriteSample(testSample); err != nil {
                            log.Printf("Station %s: Video track not bound: %v", st.name, err)
                            return
                        }
                        boundChecked = true
                    }
                    sample := media.Sample{
                        Data: sampleData,
                        Duration: frameInterval,
                        PacketTimestamp: videoTimestamp,
                    }
                    if err := st.trackVideo.WriteSample(sample); err != nil {
                        log.Printf("Station %s: Video sample write error: %v", st.name, err)
                        return
                    }
                    segmentSamples++
                    videoTimestamp += uint32((frameInterval * videoClockRate) / time.Second)
                    currentFrame = [][]byte{nalu}
                    hasVCL = false
                } else {
                    if isVCL {
                        firstMb, err := getFirstMbInSlice(nalu)
                        if err != nil {
                            log.Printf("Station %s: Failed to parse first_mb_in_slice for NALU in %s: %v", st.name, segPath, err)
                            continue
                        }
                        if firstMb == 0 && len(currentFrame) > 0 && hasVCL {
                            targetTime := videoStart.Add(time.Duration(videoTimestamp*1000/videoClockRate) * time.Millisecond)
                            now := time.Now()
                            if now.Before(targetTime) {
                                time.Sleep(targetTime.Sub(now))
                            }
                            var frameData bytes.Buffer
                            for _, n := range currentFrame {
                                frameData.Write([]byte{0x00, 0x00, 0x00, 0x01})
                                frameData.Write(n)
                            }
                            sampleData := frameData.Bytes()
                            if !boundChecked {
                                if err := st.trackVideo.WriteSample(testSample); err != nil {
                                    log.Printf("Station %s: Video track not bound: %v", st.name, err)
                                    return
                                }
                                boundChecked = true
                            }
                            sample := media.Sample{
                                Data: sampleData,
                                Duration: frameInterval,
                                PacketTimestamp: videoTimestamp,
                            }
                            if err := st.trackVideo.WriteSample(sample); err != nil {
                                log.Printf("Station %s: Video sample write error: %v", st.name, err)
                                return
                            }
                            segmentSamples++
                            videoTimestamp += uint32((frameInterval * videoClockRate) / time.Second)
                            currentFrame = [][]byte{}
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
                targetTime := videoStart.Add(time.Duration(videoTimestamp*1000/videoClockRate) * time.Millisecond)
                now := time.Now()
                if now.Before(targetTime) {
                    time.Sleep(targetTime.Sub(now))
                }
                var frameData bytes.Buffer
                for _, n := range currentFrame {
                    frameData.Write([]byte{0x00, 0x00, 0x00, 0x01})
                    frameData.Write(n)
                }
                sampleData := frameData.Bytes()
                if !boundChecked {
                    if err := st.trackVideo.WriteSample(testSample); err != nil {
                        log.Printf("Station %s: Video track not bound: %v", st.name, err)
                        return
                    }
                    boundChecked = true
                }
                sample := media.Sample{
                    Data: sampleData,
                    Duration: frameInterval,
                    PacketTimestamp: videoTimestamp,
                }
                if err := st.trackVideo.WriteSample(sample); err != nil {
                    log.Printf("Station %s: Video sample write error: %v", st.name, err)
                    return
                }
                segmentSamples++
            }
            log.Printf("Station %s: Sent %d video frames for %s", st.name, segmentSamples, segPath)
        }(nalus)
        wg.Wait()
        os.Remove(segPath)
        opusSeg := strings.Replace(segPath, ".h264", ".opus", 1)
        os.Remove(opusSeg)
        st.mu.Lock()
        if !chunk.isAd {
            st.currentOffset += chunk.dur
            log.Printf("Station %s: Updated offset to %.3fs for video %d", st.name, st.currentOffset, st.currentVideo)
            videoDur := getVideoDur(st.currentVideo, db)
            if videoDur > 0 && st.currentOffset >= videoDur {
                log.Printf("Station %s: Completed video %d, advancing to next", st.name, st.currentVideo)
                st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
                st.currentVideo = st.videoQueue[st.currentIndex]
                st.currentOffset = 0.0
                st.spsPPS = nil
                st.fmtpLine = ""
            }
        }
        st.segmentList = st.segmentList[1:]
        log.Printf("Station %s: Removed chunk %s from segment list, %d chunks remain", st.name, segPath, len(st.segmentList))
        st.mu.Unlock()
        time.Sleep(time.Millisecond)
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
    st, ok := stations[stationName]
    if !ok {
        c.JSON(400, gin.H{"error": "Invalid station"})
        return
    }
    log.Printf("Signaling for station %s", stationName)
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
            SDPFmtpLine:  st.fmtpLine,
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
            MimeType:    webrtc.MimeTypeOpus,
            ClockRate:   48000,
            Channels:    2,
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
        var nextBreak float64 = math.MaxFloat64
        for _, b := range breaks {
            if b > nextStart {
                nextBreak = b
                break
            }
        }
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
        segments, spsPPS, fmtpLine, actualDur, fps, err := processVideo(st, st.currentVideo, db, nextStart, chunkDur)
        if err != nil {
            log.Printf("Station %s: Failed to process initial chunk for video %d: %v", st.name, st.currentVideo, err)
            st.viewers--
            st.mu.Unlock()
            c.JSON(500, gin.H{"error": "Failed to process initial video chunk"})
            pc.Close()
            return
        }
        if len(st.spsPPS) == 0 { // Update codec configuration
            st.spsPPS = spsPPS
            st.fmtpLine = fmtpLine
        }
        st.segmentList = []bufferedChunk{{
            segPath: segments[0],
            dur:     actualDur,
            isAd:    false,
            videoID: st.currentVideo,
            fps:     fps,
        }}
        log.Printf("Station %s: Queued initial chunk for video %d at %.3fs, duration %.3fs", st.name, st.currentVideo, nextStart, actualDur)
        audioPath := strings.Replace(segments[0], ".h264", ".opus", 1)
        if _, err := os.Stat(audioPath); os.IsNotExist(err) {
            log.Printf("Station %s: Audio file %s not found", st.name, audioPath)
            st.viewers--
            st.mu.Unlock()
            c.JSON(500, gin.H{"error": "Failed to process initial audio chunk"})
            pc.Close()
            return
        }
        // Queue second chunk or ads
        nextStart += actualDur
        if nextBreak == chunkEnd && len(breaks) > 0 {
            log.Printf("Station %s: Inserting initial ad break at %.3fs for video %d", st.name, nextBreak, st.currentVideo)
            for i := 0; i < 3; i++ {
                if len(adIDs) == 0 {
                    log.Printf("Station %s: No ads available for initial break at %.3fs", st.name, nextBreak)
                    break
                }
adID := adIDs[rand.Intn(len(adIDs))]
//adID := int64(64)//Debug with FFX ad
                adDur := getVideoDur(adID, db)
                if adDur <= 0 {
                    log.Printf("Station %s: Invalid duration for ad %d, skipping", st.name, adID)
                    continue
                }
                segments, spsPPS, fmtpLine, actualDur, fps, err := processVideo(st, adID, db, 0, adDur)
                if err != nil {
                    log.Printf("Station %s: Failed to process initial ad %d: %v", st.name, adID, err)
                    continue
                }
                if len(st.spsPPS) == 0 { // Update codec configuration
                    st.spsPPS = spsPPS
                    st.fmtpLine = fmtpLine
                }
                adChunk := bufferedChunk{
                    segPath: segments[0],
                    dur:     actualDur,
                    isAd:    true,
                    videoID: adID,
                    fps:     fps,
                }
                st.segmentList = append(st.segmentList, adChunk)
                log.Printf("Station %s: Queued initial ad %d with duration %.3fs at break %.3fs", st.name, adID, actualDur, nextBreak)
            }
        } else if nextStart < videoDur {
            segments, spsPPS, fmtpLine, actualDur, fps, err := processVideo(st, st.currentVideo, db, nextStart, ChunkDuration)
            if err != nil {
                log.Printf("Station %s: Failed to process second initial chunk for video %d: %v", st.name, st.currentVideo, err)
            } else {
                if len(st.spsPPS) == 0 { // Update codec configuration
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
                log.Printf("Station %s: Queued second initial chunk for video %d at %.3fs, duration %.3fs", st.name, st.currentVideo, nextStart, actualDur)
            }
        }
        go manageProcessing(st, db)
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
                close(st.stopProcessing)
                st.stopProcessing = make(chan struct{})
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
                close(st.stopProcessing)
                st.stopProcessing = make(chan struct{})
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
                close(st.stopProcessing)
                st.stopProcessing = make(chan struct{})
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
                close(st.stopProcessing)
                st.stopProcessing = make(chan struct{})
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
                close(st.stopProcessing)
                st.stopProcessing = make(chan struct{})
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
                close(st.stopProcessing)
                st.stopProcessing = make(chan struct{})
            }
            st.mu.Unlock()
            if err := pc.Close(); err != nil {
                log.Printf("Failed to close PC: %v", err)
            }
        }
    })
}

func indexHandler(c *gin.Context) {
    html := `
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>WebRTC Video Receiver</title>
<style>
video {
width: 640px;
height: 480px;
background-color: black;
}
</style>
</head>
<body>
<h1>WebRTC Video Receiver</h1>
<video id="remoteVideo" autoplay playsinline controls></video>
<button id="startButton">Start Connection</button>
<label for="serverUrl">Go Server Base URL (e.g., http://192.168.0.60:8081/signal):</label>
<input id="serverUrl" type="text" value="http://192.168.0.60:8081/signal">
<label for="station">Station (e.g., Bob's Burgers):</label>
<input id="station" type="text" value="Bob's Burgers" style="width: 100px;">
<button id="sendOfferButton" disabled>Send Offer to Server</button>
<button id="restartIceButton" disabled>Restart ICE</button>
<pre id="log"></pre>
<script>
function log(message) {
    console.log(message);
    const logElement = document.getElementById('log');
    logElement.textContent += message + '\n';
}
const configuration = {
    iceServers: [
        { urls: 'stun:stun.l.google.com:19302' },
        { urls: 'turn:openrelay.metered.ca:80', username: 'openrelayproject', credential: 'openrelayproject' },
        { urls: 'turn:openrelay.metered.ca:443', username: 'openrelayproject', credential: 'openrelayproject' },
    ]
};
let pc = null;
let remoteStream = null;
let analyser = null;
let hasAudio = false;
let hasVideo = false;
pc = new RTCPeerConnection(configuration);
pc.ontrack = event => {
    const track = event.track;
    log('Track received: kind=' + track.kind + ', id=' + track.id + ', readyState=' + track.readyState + ', enabled=' + track.enabled + ', muted=' + track.muted);
   
    if (!remoteStream) {
        remoteStream = event.streams[0];
        log('Initialized remoteStream with first track');
    }
    if (track.kind === 'video') {
        hasVideo = true;
        stopStaticAudio();
        log('Stopping TV static effect');
        log('Video started playing');
        track.onunmute = () => log('Video unmuted - media flowing');
    }
   
    if (track.kind === 'audio') {
        hasAudio = true;
        const audioContext = new AudioContext({ sampleRate: 48000 }); // Explicit 48kHz
        log('AudioContext created with sampleRate=' + audioContext.sampleRate);
        const source = audioContext.createMediaStreamSource(remoteStream);
        analyser = audioContext.createAnalyser();
        source.connect(analyser);
        analyser.connect(audioContext.destination);
        log('Connected audio track to AudioContext, channels=' + source.channelCount);
        setInterval(() => {
            if (analyser) {
                const data = new Float32Array(analyser.fftSize);
                analyser.getFloatTimeDomainData(data);
                const level = data.reduce((sum, val) => sum + Math.abs(val), 0) / data.length;
                log('Audio level: ' + level.toFixed(4) + ', sample: ' + data.slice(0, 10).join(', '));
            }
        }, 1000);
        track.onmute = () => {
            log('Track muted: kind=' + track.kind + ', id=' + track.id + ' - possible silence or no media flow');
        };
        track.onunmute = () => {
            log('Track unmuted: kind=' + track.kind + ', id=' + track.id + ' - media flowing');
            const data = new Float32Array(analyser.fftSize);
            analyser.getFloatTimeDomainData(data);
            log('Audio data sample on unmute: ' + data.slice(0, 10).join(', '));
        };
        track.onended = () => {
            log('Track ended: kind=' + track.kind + ', id=' + track.id);
        };
    }
    if (hasAudio && hasVideo && !videoElement.srcObject) {
        videoElement.srcObject = remoteStream;
        log('Both tracks received - assigned stream to video element');
        videoElement.play().then(() => {
            log('Audio and video playback started');
            if (analyser) {
                const data = new Float32Array(analyser.fftSize);
                analyser.getFloatTimeDomainData(data);
                log('Audio data sample: ' + data.slice(0, 10).join(', '));
            }
        }).catch(err => {
            log('Audio and video playback error: ' + err);
        });
    }
};
pc.oniceconnectionstatechange = () => {
    log('ICE connection state: ' + pc.iceConnectionState);
    if (pc.iceConnectionState === 'disconnected') {
        log('ICE disconnected - attempting automatic restart in 5 seconds...');
        setTimeout(restartIce, 5000);
    }
    if (pc.iceConnectionState === 'failed') {
        log('ICE failed - manual restart may be needed.');
        document.getElementById('restartIceButton').disabled = false;
    }
    if (pc.iceConnectionState === 'connected' || pc.iceConnectionState === 'completed') {
        log('ICE connected - assigned stream to video element.');
    }
};
pc.onconnectionstatechange = () => {
    log('Peer connection state: ' + pc.connectionState);
};
pc.onicecandidate = (event) => {
    if (event.candidate) {
        log('New ICE candidate: ' + JSON.stringify(event.candidate));
    } else {
        log('All ICE candidates gathered (end-of-candidates)');
    }
};
let staticAudio = null;
function startStaticAudio() {
    const ctx = new AudioContext();
    const bufferSize = 4096;
    const noise = ctx.createScriptProcessor(bufferSize, 1, 1);
    noise.onaudioprocess = (e) => {
        const output = e.outputBuffer.getChannelData(0);
        for (let i = 0; i < bufferSize; i++) {
            output[i] = (Math.random() * 2 - 1) * 0.05;
        }
    };
    const gain = ctx.createGain();
    gain.gain.value = 0.1;
    noise.connect(gain);
    gain.connect(ctx.destination);
    staticAudio = noise;
    log('Starting static audio');
}
function stopStaticAudio() {
    if (staticAudio) {
        staticAudio.disconnect();
        staticAudio = null;
        log('Stopping static audio');
    }
}
startStaticAudio();
async function createOffer() {
    const offer = await pc.createOffer({ offerToReceiveVideo: true, offerToReceiveAudio: true });
    await pc.setLocalDescription(offer);
    log('Local Offer SDP: ' + offer.sdp);
    document.getElementById('sendOfferButton').disabled = false;
}
async function sendOfferToServer() {
    let baseUrl = document.getElementById('serverUrl').value;
    const station = document.getElementById('station').value;
    const serverUrl = baseUrl + (baseUrl.includes('?') ? '&' : '?') + 'station=' + encodeURIComponent(station);
    try {
        const response = await fetch(serverUrl, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ sdp: pc.localDescription.sdp, type: pc.localDescription.type })
        });
        if (!response.ok) {
            throw new Error('HTTP error: ' + response.status);
        }
        const answerJson = await response.json();
        const answer = new RTCSessionDescription(answerJson);
        await pc.setRemoteDescription(answer);
        log('Remote Answer SDP: ' + answer.sdp);
    } catch (error) {
        log('Error sending offer: ' + error);
    }
}
async function restartIce() {
    log('Restarting ICE...');
    try {
        const newOffer = await pc.createOffer({ iceRestart: true });
        await pc.setLocalDescription(newOffer);
        log('New offer with ICE restart: ' + newOffer.sdp);
        sendOfferToServer();
    } catch (error) {
        log('ICE restart failed: ' + error);
    }
}
function startStatsLogging() {
    setInterval(async () => {
        if (!pc) return;
        try {
            const stats = await pc.getStats();
            stats.forEach(report => {
                if (report.type === 'inbound-rtp' && report.kind === 'video') {
                    log('Video inbound: packetsReceived=' + (report.packetsReceived || 0) +
                        ', bytesReceived=' + (report.bytesReceived || 0) +
                        ', packetsLost=' + (report.packetsLost || 0) +
                        ', jitter=' + (report.jitter || 0) +
                        ', frameRate=' + (report.frameRate || 0));
                }
                if (report.type === 'inbound-rtp' && report.kind === 'audio') {
                    log('Audio inbound: packetsReceived=' + (report.packetsReceived || 0) +
                        ', bytesReceived=' + (report.bytesReceived || 0) +
                        ', packetsLost=' + (report.packetsLost || 0) +
                        ', jitter=' + (report.jitter || 0) +
                        ', audioLevel=' + (report.audioLevel || 0) +
                        ', totalAudioEnergy=' + (report.totalAudioEnergy || 0) +
                        ', totalSamplesReceived=' + (report.totalSamplesReceived || 0));
                }
                if (report.type === 'candidate-pair' && report.state === 'succeeded') {
                    log('Active candidate pair: localType=' + report.localCandidateId +
                        ', remoteType=' + report.remoteCandidateId +
                        ', bytesSent=' + report.bytesSent +
                        ', bytesReceived=' + report.bytesReceived);
                }
            });
        } catch (error) {
            log('getStats error: ' + error);
        }
    }, 2000);
}
document.getElementById('startButton').addEventListener('click', async () => {
    await createOffer();
    startStatsLogging();
    document.getElementById('sendOfferButton').disabled = false;
    document.getElementById('restartIceButton').disabled = false;
    log('Connection started. Enter server URL and station, then click "Send Offer to Server".');
});
document.getElementById('sendOfferButton').addEventListener('click', sendOfferToServer);
document.getElementById('restartIceButton').addEventListener('click', restartIce);
</script>
</body>
</html>`
    c.Header("Content-Type", "text/html")
    c.String(200, html)
}

func updateVideoDurations(db *sql.DB) error {
    if videoBaseDir == "" {
        return fmt.Errorf("videoBaseDir is not set, cannot process video files")
    }
    rows, err := db.Query("SELECT id, uri FROM videos WHERE duration IS NULL")
    if err != nil {
        return fmt.Errorf("failed to query videos with NULL duration: %v", err)
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
        log.Println("No videos with NULL duration found")
        return nil
    }
    log.Printf("Found %d videos with NULL duration, calculating durations", len(videoIDs))
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
            duration, err := strconv.ParseFloat(durationStr, 64)
            if err != nil {
                mu.Lock()
                errors = append(errors, fmt.Errorf("failed to parse duration for video %d (%s): %v", id, fullPath, err))
                mu.Unlock()
                return
            }
            _, err = db.Exec("UPDATE videos SET duration = $1 WHERE id = $2", duration, id)
            if err != nil {
                mu.Lock()
                errors = append(errors, fmt.Errorf("failed to update duration for video %d (%s): %v", id, fullPath, err))
                mu.Unlock()
                return
            }
            log.Printf("Updated duration for video %d (%s) to %.2f seconds", id, fullPath, duration)
        }(videoID, uris[i])
    }
    wg.Wait()
    if len(errors) > 0 {
        return fmt.Errorf("encountered %d errors during duration updates: %v", len(errors), errors)
    }
    log.Printf("Successfully updated durations for %d videos", len(videoIDs))
    return nil
}

var adIDs []int64
var videoBaseDir string
var stations = make(map[string]*Station)
var mu sync.Mutex
var globalStart = time.Now()

type Station struct {
    name            string
    segmentList     []bufferedChunk
    spsPPS          [][]byte
    fmtpLine        string
    trackVideo      *webrtc.TrackLocalStaticSample
    trackAudio      *webrtc.TrackLocalStaticSample
    videoQueue      []int64
    currentVideo    int64
    currentIndex    int
    currentOffset   float64
    viewers         int
    processing      bool
    stopProcessing  chan struct{}
    mu              sync.Mutex
}

func discoverStations(db *sql.DB) {
    rows, err := db.Query("SELECT name FROM stations")
    if err != nil {
        log.Fatalf("Failed to query stations from DB: %v", err)
    }
    defer rows.Close()
    for rows.Next() {
        var stationName string
        if err := rows.Scan(&stationName); err != nil {
            log.Printf("Failed to scan station: %v", err)
            continue
        }
        st := loadStation(stationName, db)
        if st != nil {
            stations[stationName] = st
        }
    }
    if err := rows.Err(); err != nil {
        log.Fatalf("Error iterating stations: %v", err)
    }
    log.Printf("Discovered %d stations from DB: %v", len(stations), stations)
    if len(stations) == 0 {
        log.Fatal("No stations found in DB - populate the 'stations' and 'station_videos' tables")
    }
}

func main() {
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
//adIDs = []int64{int64(64)}//Debug with FFX ad
    }
    if err := rows.Err(); err != nil {
        log.Fatalf("Error iterating ad IDs: %v", err)
    }
    log.Printf("Loaded %d commercials", len(adIDs))
    discoverStations(db)
    for _, st := range stations {
        go sender(st, db)
    }
    r := gin.Default()
    r.Use(cors.Default())
    r.POST("/signal", func(c *gin.Context) { signalingHandler(db, c) })
    r.GET("/", indexHandler)
    r.GET("/hls/*path", func(c *gin.Context) {
        c.String(404, "Use WebRTC")
    })
    log.Printf("WebRTC TV server on %s. Stations: %v", Port, stations)
    log.Fatal(r.Run(Port))
}