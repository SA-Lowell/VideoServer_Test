// live_server.go
// WebRTC-based real-time TV: Cycles raw .h264 segments with sub-2s latency.
// Fixed: AddTrack AFTER setRemoteDescription to trigger ontrack properly (Chrome local loopback issue).
// Prefix SPS/PPS always. Log NAL types. Incremental timestamps/seqnums.
// Additional fixes: Global track for broadcasting; moved goroutine to main(); fixed FU indicator; same timestamp per frame/nalu; syntax errors resolved.
// Critical fix: Switched to TrackLocalStaticSample for Pion to handle RTP packetization/PT/fragmentation automatically.
// Dynamic profile-level-id extracted from SPS for fmtp compatibility.
// New fixes: Handle unbound track without exiting writer; correct profile-level-id parsing; group NALUs per frame for proper timestamps.
// Latest fix: Removed premature defer pc.Close() to keep connection open; added ICE state logging; commented NAT1To1 for local testing.
// Current fix: Adjusted segment parsing to handle "segN.h264" without underscore (unlike "ad_N.h264").
// Speed fix: Use precise 29.97 fps duration (1001/30000 seconds per frame) based on ffprobe.
// Pacing fix: Added per-frame sleep with drift compensation for real-time playback.
// Grouping fix: Parse first_mb_in_slice to detect new pictures even without non-VCL separators; handles multi-slice and long GOPs.
// SDP fix: Force higher level in fmtp lines to allow for decoder flexibility.
// Audio addition: Added support for paired .opus files; parses OggOpus packets and sends in parallel with video using absolute timing for sync.
// NEW FIX: Dynamically detect FPS from segments using ffprobe to handle varying episode frame rates (prevents speed/sync issues).
// UPDATE: Moved FPS probe per segment for robustness in case of mixed FPS.
// NEW OPTIMIZATION: Pre-probe all segment FPS and durations in parallel at startup, cache results for fast lookup during playback.
// NEW FEATURE: Support multiple TV stations, each with segments_{station}.txt (default: segments.txt for "default" station).
// NEW FEATURE: Wait mode when no viewers: simulate playback by sleeping segment durations without processing/sending.

package main

import (
    "bytes"
    "database/sql"  
    "fmt"
    "log"
    "os"
    "os/exec"
    "path/filepath"
    "strconv"
    "strings"
    "sync"
    "time"

    _ "github.com/lib/pq"  

    "github.com/gin-contrib/cors"
    "github.com/gin-gonic/gin"
    "github.com/pion/webrtc/v3"
    "github.com/pion/webrtc/v3/pkg/media"
)

const (
    HlsDir        = "./webrtc_segments" // Your .h264 dir
    Port          = ":8081"
    ClockRate     = 90000               // RTP 90kHz clock
    AudioFrameMs  = 20                  // Opus frame duration in ms (matches ffmpeg -frame_duration)
    DefaultFPSNum = 30000
    DefaultFPSDen = 1001                // Default if probe fails
    DefaultDur    = 0.0                 // Default duration if probe fails
    DefaultStation = "default"
)

const dbConnString = "user=postgres password=aaaaaaaaaa dbname=webrtc_tv sslmode=disable host=localhost port=5432"

type fpsPair struct {
    num int
    den int
}

type Station struct {
    name           string
    segmentList    []string
    spsPPS         [][]byte // Raw SPS + PPS
    fmtpLine       string   // Dynamic fmtp from SPS
    trackVideo     *webrtc.TrackLocalStaticSample
    trackAudio     *webrtc.TrackLocalStaticSample
    fpsCache       sync.Map // path -> fpsPair
    durationCache  sync.Map // path -> float64
}

var (
    stations    = make(map[string]*Station)
    mu          sync.Mutex
    globalStart = time.Now() // For potential future use
)

type bitReader struct {
    data []byte
    pos  int // bit position
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
    // Extract EBSP (slice data after NAL header)
    ebsp := nalu[1:]
    // Remove emulation prevention bytes to get RBSP
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
    // first_mb_in_slice ue(v)
    return br.readUe()
}

func loadStation(stationName string, db *sql.DB) *Station {
    st := &Station{name: stationName}

    rows, err := db.Query(`
        SELECT s.segment_name 
        FROM segments s 
        JOIN stations st ON s.station_id = st.id 
        WHERE st.name = $1 
        ORDER BY s.order_num ASC`, stationName)
    if err != nil {
        log.Printf("Failed to query segments for station %s: %v", stationName, err)
        return nil
    }
    defer rows.Close()

    for rows.Next() {
        var segName string
        if err := rows.Scan(&segName); err != nil {
            log.Printf("Failed to scan segment for station %s: %v", stationName, err)
            continue
        }
        fullPath := filepath.Join(HlsDir, segName)
        if _, err := os.Stat(fullPath); err != nil {
            log.Printf("Segment %s not found for station %s: %v", segName, stationName, err)
            continue
        }
        st.segmentList = append(st.segmentList, fullPath)
    }
    if err := rows.Err(); err != nil {
        log.Printf("Error iterating segments for station %s: %v", stationName, err)
        return nil
    }
    log.Printf("Station %s: Loaded %d .h264 segments from DB: %v", stationName, len(st.segmentList), st.segmentList)

 // Pre-probe all segment FPS and durations in parallel
var wg sync.WaitGroup
for _, segPath := range st.segmentList {
    wg.Add(1)
    go func(path string) {
        defer wg.Done()
        // Probe FPS
        cmdFPS := exec.Command(
            "ffprobe",
            "-v", "error",
            "-select_streams", "v:0",
            "-show_entries", "stream=r_frame_rate",
            "-of", "default=noprint_wrappers=1:nokey=1",
            path,
        )
        outputFPS, errFPS := cmdFPS.Output()
        fpsNum := DefaultFPSNum
        fpsDen := DefaultFPSDen
        if errFPS == nil {
            rate := strings.TrimSpace(string(outputFPS))
            slash := strings.Index(rate, "/")
            if slash != -1 {
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
            log.Printf("Station %s: Pre-probed FPS for %s: %d/%d (%.2f fps)", stationName, path, fpsNum, fpsDen, float64(fpsNum)/float64(fpsDen))
        } else {
            log.Printf("Station %s: ffprobe FPS failed for %s: %v (default %d/%d)", stationName, path, errFPS, fpsNum, fpsDen)
        }
        st.fpsCache.Store(path, fpsPair{num: fpsNum, den: fpsDen})

        // Probe duration
        cmdDur := exec.Command(
            "ffprobe",
            "-v", "error",
            "-show_entries", "format=duration",
            "-of", "default=noprint_wrappers=1:nokey=1",
            path,
        )
        outputDur, errDur := cmdDur.Output()
        dur := DefaultDur
        if errDur == nil {
            durStr := strings.TrimSpace(string(outputDur))
            if d, err := strconv.ParseFloat(durStr, 64); err == nil {
                dur = d
            }
        }
        if dur <= 0 {
            log.Printf("Station %s: Invalid duration for %s, defaulting to 0 (will not sleep)", stationName, path)
        } else {
            log.Printf("Station %s: Pre-probed duration for %s: %.2fs", stationName, path, dur)
        }
        st.durationCache.Store(path, dur)
    }(segPath)
}
wg.Wait()
    log.Printf("Station %s: Pre-probed FPS/durations for all %d segments", stationName, len(st.segmentList))

// Extract raw SPS/PPS from any segment
for _, segPath := range st.segmentList {
    data, segErr := os.ReadFile(segPath)
    if segErr == nil && len(data) > 0 {
        nalus := splitNALUs(data)
        for _, nalu := range nalus {
            if len(nalu) > 0 {
                nalType := int(nalu[0] & 0x1F)
                if nalType == 7 {
                    st.spsPPS = append(st.spsPPS, nalu)
                } else if nalType == 8 {
                    st.spsPPS = append(st.spsPPS, nalu)
                    break
                }
            }
        }
        if len(st.spsPPS) > 0 {
            log.Printf("Station %s: Extracted %d config NALUs (SPS/PPS) from %s", stationName, len(st.spsPPS), segPath)
            break
        }
    }
}

    // Parse SPS for profile-level-id
    if len(st.spsPPS) > 0 {
        sps := st.spsPPS[0]
        if len(sps) >= 4 {
            profileIDC := sps[1]
            constraints := sps[2]
            levelIDC := sps[3]
            profileLevelID := fmt.Sprintf("%02x%02x%02x", profileIDC, constraints, levelIDC)
            st.fmtpLine = fmt.Sprintf("level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=%s", profileLevelID)
            log.Printf("Station %s: Detected H.264 profile-level-id: %s (fmtp: %s)", stationName, profileLevelID, st.fmtpLine)
        }
    }
    // Fallback
    if st.fmtpLine == "" {
        st.fmtpLine = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=64001f"
        log.Printf("Station %s: Using fallback High Profile fmtp: %s", stationName, st.fmtpLine)
    }
    // Force higher level
    st.fmtpLine = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640028"
    log.Printf("Station %s: Forcing H.264 fmtp to higher level: %s", stationName, st.fmtpLine)

    // Create tracks
    st.trackVideo, err = webrtc.NewTrackLocalStaticSample(
        webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
        "video_"+stationName,
        "pion",
    )
    if err != nil {
        log.Printf("Station %s: Failed to create video track: %v", stationName, err)
        return nil
    }
    st.trackAudio, err = webrtc.NewTrackLocalStaticSample(
        webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
        "audio_"+stationName,
        "pion",
    )
    if err != nil {
        log.Printf("Station %s: Failed to create audio track: %v", stationName, err)
        return nil
    }

    return st
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
        log.Fatal("No stations found in DB - populate the 'stations' and 'segments' tables")
    }
}

// sender runs the cycling loop for a station
func sender(st *Station) {
    audioFrameDur := time.Duration(AudioFrameMs) * time.Millisecond
    cycleIndex := 0
    for {
        if len(st.segmentList) == 0 {
            time.Sleep(time.Second)
            continue
        }
        if cycleIndex >= len(st.segmentList) {
            cycleIndex = 0
        }
        segPath := st.segmentList[cycleIndex]
        cycleIndex++

        // Check if viewers (track bound)
        testSample := media.Sample{Data: []byte{}, Duration: time.Duration(0)}
        err := st.trackVideo.WriteSample(testSample)
        if err != nil && strings.Contains(err.Error(), "not bound") {
            // No viewers, simulate sleep
            val, ok := st.durationCache.Load(segPath)
            segDur := DefaultDur
            if ok {
                segDur = val.(float64)
            }
            if segDur > 0 {
                log.Printf("Station %s: No viewers, sleeping for segment %s duration %.2fs", st.name, segPath, segDur)
                time.Sleep(time.Duration(segDur * float64(time.Second)))
            } else {
                log.Printf("Station %s: No viewers, but invalid dur for %s, skipping sleep", st.name, segPath)
            }
            continue
        } else if err != nil {
            log.Printf("Station %s: Track test write error: %v (continuing)", st.name, err)
        }

        // Viewers present, process segment
        data, segErr := os.ReadFile(segPath)
        if segErr != nil {
            log.Printf("Station %s: Segment read error %s: %v", st.name, segPath, segErr)
            continue
        }
        if len(data) == 0 {
            log.Printf("Station %s: Empty segment %s, skipping", st.name, segPath)
            continue
        }

        log.Printf("Station %s: Processing segment %s (%d bytes)", st.name, segPath, len(data))

        // Get cached FPS
        fpsNum := DefaultFPSNum
        fpsDen := DefaultFPSDen
        if val, ok := st.fpsCache.Load(segPath); ok {
            pair := val.(fpsPair)
            fpsNum = pair.num
            fpsDen = pair.den
            log.Printf("Station %s: Cache hit FPS for %s: %d/%d (%.2f fps)", st.name, segPath, fpsNum, fpsDen, float64(fpsNum)/float64(fpsDen))
        } else {
            log.Printf("Station %s: Cache miss for %s, using default %d/%d", st.name, segPath, fpsNum, fpsDen)
        }
        frameDuration := time.Second * time.Duration(fpsDen) / time.Duration(fpsNum)

        nalus := splitNALUs(data)
        log.Printf("Station %s: Split %d NALUs from %s", st.name, len(nalus), segPath)

        // Prefix SPS/PPS
        allNALUs := nalus
        if len(st.spsPPS) > 0 {
            allNALUs = append(st.spsPPS, nalus...)
            log.Printf("Station %s: Prefixed %d config NALUs to segment", st.name, len(st.spsPPS))
        }

        // Load audio
        audioPath := strings.Replace(segPath, ".h264", ".opus", 1)
        var audioPackets [][]byte
        if _, err := os.Stat(audioPath); err == nil {
            audioData, err := os.ReadFile(audioPath)
            if err == nil && len(audioData) > 0 {
                audioPackets = parseOpusPackets(audioData)
                log.Printf("Station %s: Parsed %d Opus packets from %s", st.name, len(audioPackets), audioPath)
            } else {
                log.Printf("Station %s: Failed to read audio %s: %v", st.name, audioPath, err)
            }
        } else {
            log.Printf("Station %s: No audio file for %s", st.name, segPath)
        }

        // Start sending
        var wg sync.WaitGroup
        wg.Add(1) // Video

        // Audio goroutine
        if len(audioPackets) > 0 {
            wg.Add(1)
            go func(packets [][]byte) {
                defer wg.Done()
                audioStart := time.Now()
                audioTs := time.Duration(0)
                boundCheckedAudio := false
                for pktIdx, pkt := range packets {
                    targetTime := audioStart.Add(audioTs)
                    now := time.Now()
                    if now.Before(targetTime) {
                        time.Sleep(targetTime.Sub(now))
                    }
                    if !boundCheckedAudio {
                        testSample := media.Sample{Data: []byte{}, Duration: time.Duration(0)}
                        if err := st.trackAudio.WriteSample(testSample); err != nil && strings.Contains(err.Error(), "not bound") {
                            log.Printf("Station %s: Audio track not bound, skipping segment audio", st.name)
                            break
                        }
                        boundCheckedAudio = true
                    }
                    sample := media.Sample{Data: pkt, Duration: audioFrameDur}
                    if err := st.trackAudio.WriteSample(sample); err != nil {
                        log.Printf("Station %s: Audio sample %d write error: %v", st.name, pktIdx, err)
                        if strings.Contains(err.Error(), "not bound") {
                            break
                        }
                    }
                    audioTs += audioFrameDur
                }
                log.Printf("Station %s: Sent %d audio samples for %s", st.name, len(packets), audioPath)
            }(audioPackets)
        }

        // Video
        go func(nalus [][]byte) {
            defer wg.Done()
            videoStart := time.Now()
            videoTs := time.Duration(0)
            segmentSamples := 0
            idrSent := false
            var currentFrame [][]byte
            var hasVCL bool
            boundChecked := false
            for i, nalu := range nalus {
                if len(nalu) == 0 {
                    continue
                }
                nalType := int(nalu[0] & 0x1F)
                isVCL := nalType >= 1 && nalType <= 5
                if i%1000 == 0 || nalType == 5 || nalType == 7 || nalType == 8 {
                    //log.Printf("Station %s: NALU %d type %d (%s) size %d", st.name, i, nalType, nalTypeToString(nalType), len(nalu))
                }

                if hasVCL && !isVCL {
                    // Send frame
                    targetTime := videoStart.Add(videoTs)
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
                        testSample := media.Sample{Data: []byte{}, Duration: time.Duration(0)}
                        if err := st.trackVideo.WriteSample(testSample); err != nil && strings.Contains(err.Error(), "not bound") {
                            log.Printf("Station %s: Video track not bound, skipping segment video", st.name)
                            return
                        }
                        boundChecked = true
                    }

                    sample := media.Sample{
                        Data:     sampleData,
                        Duration: frameDuration,
                    }
                    if err := st.trackVideo.WriteSample(sample); err != nil {
                        log.Printf("Station %s: Video sample write error: %v", st.name, err)
                    }
                    segmentSamples++
                    videoTs += frameDuration

                    currentFrame = [][]byte{nalu}
                    hasVCL = false
                } else {
                    if isVCL {
                        firstMb, err := getFirstMbInSlice(nalu)
                        if err != nil {
                            continue
                        }
                        if firstMb == 0 && len(currentFrame) > 0 && hasVCL {
                            // Send frame
                            targetTime := videoStart.Add(videoTs)
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
                                testSample := media.Sample{Data: []byte{}, Duration: time.Duration(0)}
                                if err := st.trackVideo.WriteSample(testSample); err != nil && strings.Contains(err.Error(), "not bound") {
                                    log.Printf("Station %s: Video track not bound, skipping segment video", st.name)
                                    return
                                }
                                boundChecked = true
                            }

                            sample := media.Sample{
                                Data:     sampleData,
                                Duration: frameDuration,
                            }
                            if err := st.trackVideo.WriteSample(sample); err != nil {
                                log.Printf("Station %s: Video sample write error: %v", st.name, err)
                            }
                            segmentSamples++
                            videoTs += frameDuration

                            currentFrame = [][]byte{}
                            hasVCL = false
                        }
                        currentFrame = append(currentFrame, nalu)
                        hasVCL = true
                        if nalType == 5 {
                            idrSent = true
                        }
                    } else {
                        currentFrame = append(currentFrame, nalu)
                    }
                }
            }
            // Last frame
            if len(currentFrame) > 0 {
                targetTime := videoStart.Add(videoTs)
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
                    testSample := media.Sample{Data: []byte{}, Duration: time.Duration(0)}
                    if err := st.trackVideo.WriteSample(testSample); err != nil && strings.Contains(err.Error(), "not bound") {
                        log.Printf("Station %s: Video track not bound, skipping segment video", st.name)
                        return
                    }
                    boundChecked = true
                }

                sample := media.Sample{
                    Data:     sampleData,
                    Duration: frameDuration,
                }
                if err := st.trackVideo.WriteSample(sample); err != nil {
                    log.Printf("Station %s: Video sample write error: %v", st.name, err)
                }
                segmentSamples++
            }
            if !idrSent {
                log.Printf("Station %s: WARNING: No IDR in %s", st.name, segPath)
            }
            log.Printf("Station %s: Sent %d video frames for %s", st.name, segmentSamples, segPath)
        }(allNALUs)

        wg.Wait()
        time.Sleep(10 * time.Millisecond)
    }
}

func nalTypeToString(t int) string {
    switch t {
    case 1:
        return "Non-IDR"
    case 5:
        return "IDR"
    case 6:
        return "SEI"
    case 7:
        return "SPS"
    case 8:
        return "PPS"
    default:
        return "Other"
    }
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

func parseOpusPackets(data []byte) [][]byte {
    var packets [][]byte
    i := 0
    for i < len(data) {
        if i+4 >= len(data) || string(data[i:i+4]) != "OggS" {
            break
        }
        i += 4
        version := data[i]
        i++
        if version != 0 {
            break
        }
        i++
        i += 8
        i += 4
        i += 4
        i += 4
        segCount := int(data[i])
        i++
        var segTable []int
        lacingSum := 0
        for j := 0; j < segCount; j++ {
            seg := int(data[i])
            segTable = append(segTable, seg)
            lacingSum += seg
            i++
        }
        if i+lacingSum > len(data) {
            break
        }
        pageData := data[i : i+lacingSum]
        i += lacingSum
        var currentPacket []byte
        for _, lace := range segTable {
            if len(pageData) < lace {
                return packets
            }
            currentPacket = append(currentPacket, pageData[:lace]...)
            pageData = pageData[lace:]
            if lace < 255 {
                packets = append(packets, currentPacket)
                currentPacket = nil
            }
        }
        if currentPacket != nil {
            packets = append(packets, currentPacket)
        }
    }
    if len(packets) >= 2 && string(packets[0][:8]) == "OpusHead" && string(packets[1][:8]) == "OpusTags" {
        packets = packets[2:]
    }
    return packets
}

func signalingHandler(c *gin.Context) {
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
    // Register H264 with station's fmtp
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
    // Register Opus
    if err := m.RegisterCodec(webrtc.RTPCodecParameters{
        RTPCodecCapability: webrtc.RTPCodecCapability{
            MimeType:     webrtc.MimeTypeOpus,
            ClockRate:    48000,
            Channels:     2,
            SDPFmtpLine:  "minptime=10;useinbandfec=1",
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

    pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
        log.Printf("Station %s: ICE state: %s", stationName, state.String())
    })

    if msg.Type == "offer" {
        offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: msg.SDP}
        if err := pc.SetRemoteDescription(offer); err != nil {
            log.Printf("SetRemoteDescription error: %v", err)
            c.JSON(500, gin.H{"error": err.Error()})
            pc.Close()
            return
        }

        // Add station's tracks
        if _, err = pc.AddTrack(st.trackVideo); err != nil {
            log.Printf("AddTrack video error: %v", err)
            c.JSON(500, gin.H{"error": err.Error()})
            pc.Close()
            return
        }
        if _, err = pc.AddTrack(st.trackAudio); err != nil {
            log.Printf("AddTrack audio error: %v", err)
            c.JSON(500, gin.H{"error": err.Error()})
            pc.Close()
            return
        }
        log.Printf("Station %s: Tracks added", stationName)

        answer, err := pc.CreateAnswer(nil)
        if err != nil {
            log.Printf("CreateAnswer error: %v", err)
            c.JSON(500, gin.H{"error": err.Error()})
            pc.Close()
            return
        }

        gatherComplete := webrtc.GatheringCompletePromise(pc)
        if err := pc.SetLocalDescription(answer); err != nil {
            log.Printf("SetLocalDescription error: %v", err)
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
            if err := pc.Close(); err != nil {
                log.Printf("Failed to close PC: %v", err)
            }
        }
    })
}

func indexHandler(c *gin.Context) {
    html := `<!DOCTYPE html>
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
    <label for="serverUrl">Go Server Base URL (e.g., http://localhost:8081/signal):</label>
    <input id="serverUrl" type="text" value="http://localhost:8081/signal" style="width: 300px;">
    <label for="station">Station (e.g., default, 1, 2):</label>
    <input id="station" type="text" value="default" style="width: 100px;">
    <button id="sendOfferButton" disabled>Send Offer to Server</button>
    <button id="restartIceButton" disabled>Restart ICE</button>
    <pre id="log"></pre>

    <script>
        // Utility to log messages to the console and the page
        function log(message) {
            console.log(message);
            const logElement = document.getElementById('log');
            logElement.textContent += message + '\n';
        }

        // Configuration for ICE servers
        const configuration = {
            iceServers: [
                { urls: 'stun:stun.l.google.com:19302' },
                { urls: 'turn:openrelay.metered.ca:80', username: 'openrelayproject', credential: 'openrelayproject' },
                { urls: 'turn:openrelay.metered.ca:443', username: 'openrelayproject', credential: 'openrelayproject' },
            ]
        };

        // Variables
        let pc = null;
        let remoteStream = null;
        let offer = null;
        let candidates = [];

        // Function to create the local offer (recvonly video and audio)
        async function createOffer() {
            pc = new RTCPeerConnection(configuration);

            // Event listeners for debugging
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
                    const videoElement = document.getElementById('remoteVideo');
                    videoElement.srcObject = remoteStream;
                    log('ICE connected - assigned stream to video element.');
                }
            };

            pc.onconnectionstatechange = () => {
                log('Peer connection state: ' + pc.connectionState);
            };

            pc.onicecandidate = (event) => {
                if (event.candidate) {
                    log('New ICE candidate: ' + JSON.stringify(event.candidate));
                    candidates.push(event.candidate);
                } else {
                    log('All ICE candidates gathered (end-of-candidates)');
                }
            };

            pc.ontrack = (event) => {
                log('Track received: ' + event.track.kind + ' ' + event.track.readyState);
                if (!remoteStream) {
                    remoteStream = new MediaStream();
                }
                remoteStream.addTrack(event.track);
                event.track.onmute = () => log('Track muted - possible black screen or no media flow');
                event.track.onunmute = () => log('Track unmuted - media flowing');
            };

            // Create offer
            offer = await pc.createOffer({
                offerToReceiveVideo: true,
                offerToReceiveAudio: true
            });
            await pc.setLocalDescription(offer);
            log('Local Offer SDP: ' + offer.sdp);
        }

        // Function to send offer to Go server via fetch
        async function sendOfferToServer() {
            let baseUrl = document.getElementById('serverUrl').value;
            const station = document.getElementById('station').value;
            const serverUrl = baseUrl + (baseUrl.includes('?') ? '&' : '?') + 'station=' + encodeURIComponent(station);
            if (!offer) {
                log('No offer generated yet - start connection first.');
                return;
            }

            try {
                const response = await fetch(serverUrl, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ sdp: offer.sdp, type: offer.type })
                });
                if (!response.ok) {
                    throw new Error('HTTP error: ' + response.status);
                }
                const answerJson = await response.json();
                const answer = new RTCSessionDescription(answerJson);
                await pc.setRemoteDescription(answer);
                log('Remote Answer SDP set: ' + answer.sdp);
            } catch (error) {
                log('Error sending offer: ' + error);
            }
        }

        // Function for ICE restart
        async function restartIce() {
            log('Restarting ICE...');
            try {
                const newOffer = await pc.createOffer({ iceRestart: true });
                await pc.setLocalDescription(newOffer);
                log('New offer with ICE restart: ' + newOffer.sdp);
                offer = newOffer;
            } catch (error) {
                log('ICE restart failed: ' + error);
            }
        }

        // Periodically get stats for debugging
        function startStatsLogging() {
            setInterval(async () => {
                if (!pc) return;
                try {
                    const stats = await pc.getStats();
                    stats.forEach(report => {
                        if (report.type === 'inbound-rtp' && report.kind === 'video') {
                            log('Video inbound: packetsReceived=' + (report.packetsReceived || 0) + ', bytesReceived=' + (report.bytesReceived || 0) + ', packetsLost=' + (report.packetsLost || 0) + ', jitter=' + (report.jitter || 0) + ', frameRate=' + (report.frameRate || 0));
                        }
                        if (report.type === 'inbound-rtp' && report.kind === 'audio') {
                            log('Audio inbound: packetsReceived=' + (report.packetsReceived || 0) + ', bytesReceived=' + (report.bytesReceived || 0) + ', packetsLost=' + (report.packetsLost || 0) + ', jitter=' + (report.jitter || 0));
                        }
                        if (report.type === 'candidate-pair' && report.state === 'succeeded') {
                            log('Active candidate pair: localType=' + report.localCandidateId + ', remoteType=' + report.remoteCandidateId + ', bytesSent=' + report.bytesSent + ', bytesReceived=' + report.bytesReceived);
                        }
                    });
                } catch (error) {
                    log('getStats error: ' + error);
                }
            }, 5000);
        }

        // Button handlers
        document.getElementById('startButton').addEventListener('click', async () => {
            await createOffer();
            startStatsLogging();
            document.getElementById('sendOfferButton').disabled = false;
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

func main() {
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

    discoverStations(db)  

    // Launch senders for each station
    for _, st := range stations {
        go sender(st)
    }

    r := gin.Default()
    r.Use(cors.Default())
    r.POST("/signal", signalingHandler)
    r.GET("/", indexHandler)
    r.GET("/hls/*path", func(c *gin.Context) { c.String(404, "Use WebRTC") })

    log.Printf("WebRTC TV server on %s. Stations: %v", Port, stations)
    log.Fatal(r.Run(Port))
}