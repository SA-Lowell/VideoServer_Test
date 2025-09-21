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

package main

import (
    "bytes"
    "fmt"
    "log"
    "os"
    "path/filepath"
    "sort"
    "strconv"
    "strings"
    "sync"
    "time"

    "github.com/gin-contrib/cors"
    "github.com/gin-gonic/gin"
    "github.com/pion/webrtc/v3"
    "github.com/pion/webrtc/v3/pkg/media"
)

const (
    HlsDir      = "./webrtc_segments" // Your .h264 dir
    Port        = ":8081"
    ClockRate   = 90000               // RTP 90kHz clock
    // Precise 29.97 fps: numerator/denominator for frame duration calc
    FPSNum      = 30000
    FPSDen      = 1001
    AudioFrameMs = 20 // Opus frame duration in ms (matches ffmpeg -frame_duration)
)

var (
    segmentList []string
    mu          sync.Mutex
    cycleIndex  int
    spsPPS      [][]byte // Raw SPS + PPS
    trackVideo  *webrtc.TrackLocalStaticSample
    trackAudio  *webrtc.TrackLocalStaticSample
    fmtpLine    string // Dynamic fmtp from SPS
)

type segInfo struct {
    name string
    num  int
}

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

func scanSegments() {
    files, _ := filepath.Glob(filepath.Join(HlsDir, "*.h264"))
    var segInfos []segInfo
    for _, f := range files {
        base := filepath.Base(f)
        numStr := ""
        prefix := ""
        if strings.HasPrefix(base, "seg") {
            prefix = "seg"
            numStr = strings.TrimSuffix(strings.TrimPrefix(base, "seg"), ".h264")
        } else if strings.HasPrefix(base, "ad_") {
            prefix = "ad"
            numStr = strings.TrimSuffix(strings.TrimPrefix(base, "ad_"), ".h264")
        } else {
            continue
        }
        if num, err := strconv.Atoi(numStr); err == nil && num >= 0 {
            segInfos = append(segInfos, segInfo{base, num})
            log.Printf("Added %s segment %s (num %d)", prefix, base, num)
        }
    }
    sort.Slice(segInfos, func(i, j int) bool { return segInfos[i].num < segInfos[j].num })
    segmentList = nil
    for _, si := range segInfos {
        segmentList = append(segmentList, filepath.Join(HlsDir, si.name))
    }
    log.Printf("Loaded %d .h264 segments for WebRTC cycling: %v", len(segmentList), segmentList)

    // Extract raw SPS/PPS from any segment
    spsPPS = nil
    for _, segPath := range segmentList {
        data, err := os.ReadFile(segPath)
        if err == nil && len(data) > 0 {
            nalus := splitNALUs(data)
            for _, nalu := range nalus {
                if len(nalu) > 0 {
                    nalType := int(nalu[0] & 0x1F)
                    if nalType == 7 {
                        spsPPS = append(spsPPS, nalu)
                    } else if nalType == 8 {
                        spsPPS = append(spsPPS, nalu)
                        break
                    }
                }
            }
            if len(spsPPS) > 0 {
                log.Printf("Extracted %d config NALUs (SPS/PPS) from %s", len(spsPPS), segPath)
                break
            }
        }
    }

    // Parse SPS for profile-level-id (full 3 bytes: profile + constraints + level)
    if len(spsPPS) > 0 {
        sps := spsPPS[0]
        if len(sps) >= 4 {
            profileIDC := sps[1]
            constraints := sps[2]
            levelIDC := sps[3]
            profileLevelID := fmt.Sprintf("%02x%02x%02x", profileIDC, constraints, levelIDC)
            fmtpLine = fmt.Sprintf("level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=%s", profileLevelID)
            log.Printf("Detected H.264 profile-level-id: %s (fmtp: %s)", profileLevelID, fmtpLine)
        }
    }
    // Fallback to High Profile if parsing failed
    if fmtpLine == "" {
        fmtpLine = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=64001f"
        log.Printf("Using fallback High Profile fmtp: %s", fmtpLine)
    }
    // Force higher level for decoder flexibility
    fmtpLine = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640028"
    log.Printf("Forcing H.264 fmtp to higher level: %s", fmtpLine)
}

// NALU splitter (unchanged)
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

// Parse OggOpus to extract Opus packets (skips ID and comment headers)
func parseOpusPackets(data []byte) [][]byte {
    var packets [][]byte
    i := 0
    for i < len(data) {
        if i+4 >= len(data) || string(data[i:i+4]) != "OggS" {
            log.Printf("Invalid Ogg magic at %d", i)
            break
        }
        i += 4
        version := data[i]
        i++
        if version != 0 {
            log.Printf("Unsupported Ogg version %d", version)
            break
        }
        // headerType := data[i]
        i++
        // granule := binary.LittleEndian.Uint64(data[i : i+8])
        i += 8
        // serial := binary.LittleEndian.Uint32(data[i : i+4])
        i += 4
        // pageNum := binary.LittleEndian.Uint32(data[i : i+4])
        i += 4
        // checksum := binary.LittleEndian.Uint32(data[i : i+4])
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
            log.Printf("Ogg page data overflow")
            break
        }
        pageData := data[i : i+lacingSum]
        i += lacingSum
        // Collect packets from lacing
        var currentPacket []byte
        for _, lace := range segTable {
            if len(pageData) < lace {
                log.Printf("Ogg lacing overflow")
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
            // Packet spans to next page
            packets = append(packets, currentPacket)
        }
    }
    // Skip first two packets (OpusHead and OpusTags)
    if len(packets) >= 2 && string(packets[0][:8]) == "OpusHead" && string(packets[1][:8]) == "OpusTags" {
        packets = packets[2:]
    }
    return packets
}

func signalingHandler(c *gin.Context) {
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
    // Register H264 codec with dynamic fmtp
    if err := m.RegisterCodec(webrtc.RTPCodecParameters{
        RTPCodecCapability: webrtc.RTPCodecCapability{
            MimeType:     webrtc.MimeTypeH264,
            ClockRate:    90000,
            SDPFmtpLine:  fmtpLine,
            RTCPFeedback: []webrtc.RTCPFeedback{{"goog-remb", ""}, {"ccm", "fir"}, {"nack", ""}, {"nack", "pli"}},
        },
        PayloadType: 96,
    }, webrtc.RTPCodecTypeVideo); err != nil {
        log.Printf("RegisterCodec video error: %v", err)
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }
    // Register Opus codec for audio
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
    // s.SetNAT1To1IPs([]string{"68.200.110.199"}, webrtc.ICECandidateTypeSrflx) // Commented for local testing
    s.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeTCP4}) // Disable IPv6

    api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(s))
    pc, err := api.NewPeerConnection(webrtc.Configuration{
        ICEServers: []webrtc.ICEServer{
            {
                URLs: []string{"stun:stun.l.google.com:19302"},
            },
            {
                URLs:       []string{"turn:openrelay.metered.ca:80"},
                Username:   "openrelayproject",
                Credential: "openrelayproject",
            },
            {
                URLs:       []string{"turn:openrelay.metered.ca:443"},
                Username:   "openrelayproject",
                Credential: "openrelayproject",
            },
        },
    })
    if err != nil {
        log.Printf("NewPeerConnection error: %v", err)
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }

    // Log ICE connection state changes for debugging
    pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
        log.Printf("ICE Connection State changed: %s", state.String())
    })

    // Handle offer/answer
    if msg.Type == "offer" {
        offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: msg.SDP}
        if err := pc.SetRemoteDescription(offer); err != nil {
            log.Printf("SetRemoteDescription error: %v", err)
            c.JSON(500, gin.H{"error": err.Error()})
            pc.Close()
            return
        }

        // NOW add global tracks after setRemoteDescription
        if _, err = pc.AddTrack(trackVideo); err != nil {
            log.Printf("AddTrack video error: %v", err)
            c.JSON(500, gin.H{"error": err.Error()})
            pc.Close()
            return
        }
        if _, err = pc.AddTrack(trackAudio); err != nil {
            log.Printf("AddTrack audio error: %v", err)
            c.JSON(500, gin.H{"error": err.Error()})
            pc.Close()
            return
        }
        log.Printf("Tracks added after setRemoteDescription")

        answer, err := pc.CreateAnswer(nil)
        if err != nil {
            log.Printf("CreateAnswer error: %v", err)
            c.JSON(500, gin.H{"error": err.Error()})
            pc.Close()
            return
        }

        // Set local description and gather ICE candidates
        gatherComplete := webrtc.GatheringCompletePromise(pc)
        if err := pc.SetLocalDescription(answer); err != nil {
            log.Printf("SetLocalDescription error: %v", err)
            c.JSON(500, gin.H{"error": err.Error()})
            pc.Close()
            return
        }

        // Wait for ICE gathering to complete
        <-gatherComplete

        log.Printf("SDP Answer generated (check browser console for profile): %s", pc.LocalDescription().SDP)
        c.JSON(200, gin.H{"type": "answer", "sdp": pc.LocalDescription().SDP})
    }

    // Auto-close on failure/disconnect (optional, for resource management)
    pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
        log.Printf("Peer Connection State changed: %s", s.String())
        if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateDisconnected {
            if err := pc.Close(); err != nil {
                log.Printf("Failed to close PC: %v", err)
            }
        }
    })
}

// Helper to log NAL type
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

func indexHandler(c *gin.Context) {
    html := `<!DOCTYPE html>
<html><body>
<video id="video" autoplay controls width="640" height="360"></video>
<div id="status">Connecting...</div>
<script>
console.log('Starting WebRTC client');
const pc = new RTCPeerConnection();
const video = document.getElementById('video');
const status = document.getElementById('status');
video.addEventListener('loadedmetadata', () => { console.log('Metadata loaded'); status.textContent = 'Metadata OK'; });
video.addEventListener('canplay', () => { console.log('Can play'); status.textContent = 'Playing'; video.muted = false; });
video.addEventListener('error', (e) => { console.error('Video error:', e); status.textContent = 'Error: ' + e.message; });
video.addEventListener('stalled', () => console.log('Stalled - jitter buffer?'));
pc.oniceconnectionstatechange = () => console.log('ICE:', pc.iceConnectionState);
pc.ontrack = (e) => {
    console.log('Track received:', e.track.kind, e.track.readyState);
    if (!video.srcObject) {
        video.srcObject = e.streams[0];
    } else {
        video.srcObject.addTrack(e.track);
    }
    const track = e.track;
    track.addEventListener('ended', () => console.log('Track ended'));
    track.addEventListener('mute', () => console.log('Track muted - black screen?'));
};
async function start() {
    try {
        const offer = await pc.createOffer({offerToReceiveVideo: true, offerToReceiveAudio: true});
        await pc.setLocalDescription(offer);
        console.log('Local Offer SDP (check H.264 profile):', offer.sdp);
        const res = await fetch('/signal', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({type: 'offer', sdp: offer.sdp})
        });
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const answer = await res.json();
        console.log('Remote Answer SDP:', answer.sdp);
        await pc.setRemoteDescription(new RTCSessionDescription(answer));
        status.textContent = 'Connected - waiting for frames';
    } catch (err) {
        console.error('WebRTC failed:', err);
        status.textContent = 'Failed: ' + err.message;
    }
}
start();
</script></body></html>`
    c.Header("Content-Type", "text/html")
    c.String(200, html)
}

func main() {
    if err := os.MkdirAll(HlsDir, 0755); err != nil {
        log.Fatal(err)
    }
    scanSegments()

    // Create global video track as StaticSample - Pion handles RTP/PT/fragmentation
    var err error
    trackVideo, err = webrtc.NewTrackLocalStaticSample(
        webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
        "video",
        "pion",
    )
    if err != nil {
        log.Fatal(err)
    }
    // Create global audio track for Opus
    trackAudio, err = webrtc.NewTrackLocalStaticSample(
        webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
        "audio",
        "pion",
    )
    if err != nil {
        log.Fatal(err)
    }

    // Background cycling - send grouped frame NALUs as samples with frame duration
    go func() {
        frameDuration := time.Second * time.Duration(FPSDen) / time.Duration(FPSNum)
        audioFrameDur := time.Duration(AudioFrameMs) * time.Millisecond
        for {
            mu.Lock()
            if len(segmentList) == 0 {
                mu.Unlock()
                time.Sleep(time.Second)
                continue
            }
            if cycleIndex >= len(segmentList) {
                cycleIndex = 0
            }
            segPath := segmentList[cycleIndex]
            cycleIndex++
            mu.Unlock()

            data, err := os.ReadFile(segPath)
            if err != nil {
                log.Printf("Segment read error %s: %v", segPath, err)
                continue
            }
            if len(data) == 0 {
                log.Printf("Empty segment %s, skipping", segPath)
                continue
            }

            log.Printf("Processing segment %s (%d bytes)", segPath, len(data))

            nalus := splitNALUs(data)
            log.Printf("Split %d NALUs from %s", len(nalus), segPath)

            // Always prefix SPS/PPS for loop compatibility
            allNALUs := nalus
            if len(spsPPS) > 0 {
                allNALUs = append(spsPPS, nalus...)
                log.Printf("Prefixed %d config NALUs to segment", len(spsPPS))
            }

            // Load corresponding audio
            audioPath := strings.Replace(segPath, ".h264", ".opus", 1)
            var audioPackets [][]byte
            if _, err := os.Stat(audioPath); err == nil {
                audioData, err := os.ReadFile(audioPath)
                if err == nil && len(audioData) > 0 {
                    audioPackets = parseOpusPackets(audioData)
                    log.Printf("Parsed %d Opus packets from %s", len(audioPackets), audioPath)
                } else {
                    log.Printf("Failed to read audio %s: %v", audioPath, err)
                }
            } else {
                log.Printf("No audio file for %s", segPath)
            }

            // Start video and audio sending with sync
            var wg sync.WaitGroup
            wg.Add(1) // For video

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
                        // Check bound on first write
                        if !boundCheckedAudio {
                            testSample := media.Sample{Data: []byte{}, Duration: time.Duration(0)}
                            if err := trackAudio.WriteSample(testSample); err != nil && strings.Contains(err.Error(), "not bound") {
                                log.Printf("Audio track not bound yet, skipping segment audio")
                                break
                            }
                            boundCheckedAudio = true
                        }
                        sample := media.Sample{Data: pkt, Duration: audioFrameDur}
                        if err := trackAudio.WriteSample(sample); err != nil {
                            log.Printf("Audio sample %d write error: %v", pktIdx, err)
                            if strings.Contains(err.Error(), "not bound") {
                                break
                            }
                        }
                        audioTs += audioFrameDur
                    }
                    log.Printf("Sent %d audio samples for %s", len(packets), audioPath)
                }(audioPackets)
            }

            // Video sending (modified for absolute pacing)
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
                        //log.Printf("NALU %d type %d (%s) size %d", i, nalType, nalTypeToString(nalType), len(nalu))
                    }

                    if hasVCL && !isVCL {
                        // Non-VCL after VCL: Send current access unit
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

                        // Check bound status on first write attempt
                        if !boundChecked {
                            testSample := media.Sample{Data: []byte{}, Duration: time.Duration(0)}
                            if err := trackVideo.WriteSample(testSample); err != nil && strings.Contains(err.Error(), "not bound") {
                                log.Printf("Video track not bound yet, skipping segment")
                                return
                            }
                            boundChecked = true
                        }

                        // Write the real frame sample
                        sample := media.Sample{
                            Data:     sampleData,
                            Duration: frameDuration,
                        }
                        if err := trackVideo.WriteSample(sample); err != nil {
                            log.Printf("Video sample write error: %v", err)
                            if strings.Contains(err.Error(), "not bound") {
                                return
                            }
                        }
                        segmentSamples++
                        videoTs += frameDuration

                        // Start new access unit with this non-VCL
                        currentFrame = [][]byte{nalu}
                        hasVCL = false
                    } else {
                        if isVCL {
                            firstMb, err := getFirstMbInSlice(nalu)
                            if err != nil {
                                //log.Printf("first_mb_in_slice parse error for NALU %d: %v", i, err)
                                continue
                            }
                            if firstMb == 0 && len(currentFrame) > 0 && hasVCL {
                                // New picture starts: Send previous access unit
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
                                    if err := trackVideo.WriteSample(testSample); err != nil && strings.Contains(err.Error(), "not bound") {
                                        log.Printf("Video track not bound yet, skipping segment")
                                        return
                                    }
                                    boundChecked = true
                                }

                                sample := media.Sample{
                                    Data:     sampleData,
                                    Duration: frameDuration,
                                }
                                if err := trackVideo.WriteSample(sample); err != nil {
                                    log.Printf("Video sample write error: %v", err)
                                    if strings.Contains(err.Error(), "not bound") {
                                        return
                                    }
                                }
                                segmentSamples++
                                videoTs += frameDuration

                                // Start new access unit
                                currentFrame = [][]byte{}
                                hasVCL = false
                            }
                            // Add this VCL to current (first or continuation slice)
                            currentFrame = append(currentFrame, nalu)
                            hasVCL = true
                            if nalType == 5 {
                                idrSent = true
                                //log.Printf("IDR keyframe detected")
                            }
                        } else {
                            // Non-VCL before any VCL: Add to current
                            currentFrame = append(currentFrame, nalu)
                        }
                    }
                }
                // Send any remaining access unit
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
                        if err := trackVideo.WriteSample(testSample); err != nil && strings.Contains(err.Error(), "not bound") {
                            log.Printf("Video track not bound yet, skipping segment")
                            return
                        }
                        boundChecked = true
                    }

                    sample := media.Sample{
                        Data:     sampleData,
                        Duration: frameDuration,
                    }
                    if err := trackVideo.WriteSample(sample); err != nil {
                        log.Printf("Video sample write error: %v", err)
                        if strings.Contains(err.Error(), "not bound") {
                            return
                        }
                    }
                    segmentSamples++
                    videoTs += frameDuration
                }
                if !idrSent {
                    log.Printf("WARNING: No IDR sent in %s - decoder may stall", segPath)
                }
                log.Printf("Sent %d video frame samples for %s", segmentSamples, segPath)
            }(allNALUs)

            // Wait for both video and audio to finish before next segment
            wg.Wait()
            time.Sleep(10 * time.Millisecond) // Small segment gap to avoid overload
        }
    }()

    r := gin.Default()

    // Add CORS middleware
    r.Use(cors.Default())

    r.POST("/signal", signalingHandler)
    r.GET("/", indexHandler)
    r.GET("/hls/*path", func(c *gin.Context) { c.String(404, "Use WebRTC") })

    log.Printf("WebRTC TV server on %s (StaticSample for H264+Opus). Open http://localhost%s/", Port, Port)
    log.Fatal(r.Run(Port))
}