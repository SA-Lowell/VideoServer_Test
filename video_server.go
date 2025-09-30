package main

import (
    "bytes"
    "database/sql"
    "fmt"
    "log"
    "math"
    "math/rand"
    "os"
    "os/exec"
    "path/filepath"
    "strconv"
    "strings"
    "sync"
    "time"
    "io"

    _ "github.com/lib/pq"
    "github.com/gin-contrib/cors"
    "github.com/gin-gonic/gin"
    "github.com/pion/webrtc/v3"
    "github.com/pion/webrtc/v3/pkg/media"
    "github.com/pion/webrtc/v3/pkg/media/oggreader"
)

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
	BufferThreshold     = 60.0 // Start processing more chunks when buffer < 60s
)

const dbConnString = "user=postgres password=aaaaaaaaaa dbname=webrtc_tv sslmode=disable host=localhost port=5432"

type fpsPair struct {
	num int
	den int
}

type PreprocessedVideo struct {
	videoID   int64
	segments  []string
	spsPPS    [][]byte
	fmtpLine  string
	startTime float64 // Start time of this chunk in the video (seconds)
	endTime   float64 // End time of this chunk
}

type Station struct {
	name            string
	segmentList     []string
	spsPPS          [][]byte
	fmtpLine        string
	trackVideo      *webrtc.TrackLocalStaticSample
	trackAudio      *webrtc.TrackLocalStaticSample
	fpsCache        sync.Map
	durationCache   sync.Map
	videoQueue      []int64
	currentVideo    int64
	currentIndex    int
	currentOffset   float64
	viewers         int
	processing      bool
	stopProcessing  chan struct{}
	mu              sync.Mutex
	lastQueuedStart float64
}

var (
	stations     = make(map[string]*Station)
	mu           sync.Mutex
	globalStart  = time.Now()
	adFullPaths  []string
	videoBaseDir string
)

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

func getOpusHead(fullEpisodePath string) ([]byte, error) {
	cmd := exec.Command(
		"ffmpeg",
		"-y",
		"-i", fullEpisodePath,
		"-t", "0.1",
		"-c:a", "copy",
		"-vn",
		"-f", "data",
		"-map", "0:a:0",
		"pipe:",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Printf("getOpusHead stderr: %s", stderr.String())
		return nil, fmt.Errorf("failed to extract OpusHead: %v", err)
	}
	data := out.Bytes()
	if len(data) < 19 || string(data[:8]) != "OpusHead" {
		log.Printf("Invalid OpusHead, size: %d, first 8 bytes: %x", len(data), data[:min(8, len(data))])
		return nil, fmt.Errorf("invalid OpusHead extracted")
	}
	log.Printf("Extracted OpusHead, size: %d bytes, first 19 bytes: %x", len(data), data[:min(19, len(data))])
	return data, nil
}

func processVideo(st *Station, videoID int64, db *sql.DB, startTime, chunkDur float64) ([]string, [][]byte, string, error) {
    if db == nil {
        return nil, nil, "", fmt.Errorf("database connection is nil")
    }
    var segments []string
    var spsPPS [][]byte
    var fmtpLine string
    var uri string
    err := db.QueryRow(`SELECT uri FROM videos WHERE id = $1`, videoID).Scan(&uri)
    if err != nil {
        return nil, nil, "", fmt.Errorf("failed to get URI for video %d: %v", videoID, err)
    }
    fullEpisodePath := filepath.Join(videoBaseDir, uri)
    if _, err := os.Stat(fullEpisodePath); err != nil {
        return nil, nil, "", fmt.Errorf("episode file not found: %s", fullEpisodePath)
    }
    var duration sql.NullFloat64
    err = db.QueryRow(`SELECT duration FROM videos WHERE id = $1`, videoID).Scan(&duration)
    if err != nil {
        log.Printf("Failed to get duration for video %d: %v", videoID, err)
    }
    adjustedChunkDur := chunkDur
    if duration.Valid && startTime+chunkDur > duration.Float64 {
        adjustedChunkDur = duration.Float64 - startTime
        if adjustedChunkDur <= 0 {
            return nil, nil, "", fmt.Errorf("no remaining duration for video %d at start time %f", videoID, startTime)
        }
    }
    tempDir, err := os.MkdirTemp("", DefaultTempPrefix)
    if err != nil {
        return nil, nil, "", fmt.Errorf("failed to create temp dir for video %d: %v", videoID, err)
    }
    defer os.RemoveAll(tempDir)
    if err := os.MkdirAll(HlsDir, 0755); err != nil {
        return nil, nil, "", fmt.Errorf("failed to create webrtc_segments directory: %v", err)
    }
    safeStationName := strings.ReplaceAll(st.name, " ", "_")
    baseName := fmt.Sprintf("%s_vid%d_chunk_%f", safeStationName, videoID, startTime)
    segName := baseName + ".h264"
    fullSegPath := filepath.Join(HlsDir, segName)
    opusName := baseName + ".opus"
    opusPath := filepath.Join(HlsDir, opusName)
    args := []string{
        "-y",
        "-ss", fmt.Sprintf("%.3f", startTime),
        "-i", fullEpisodePath,
        "-t", fmt.Sprintf("%.3f", adjustedChunkDur),
        "-map", "0:a:0",
        "-c:a", "libopus",
        "-b:a", "64k",
        "-frame_duration", "20",
        "-application", "audio",
        "-avoid_negative_ts", "make_zero",
        "-fflags", "+genpts",
        "-ac", "2",
        "-f", "opus",
        "-strict", "-2",
        opusPath,
    }
    cmdAudio := exec.Command("ffmpeg", args...)
    log.Printf("Station %s: Running ffmpeg audio command: %v", st.name, cmdAudio.Args)
    outputAudio, err := cmdAudio.CombinedOutput()
    if err != nil {
        log.Printf("Station %s: ffmpeg audio command failed: %v\nOutput: %s", st.name, err, string(outputAudio))
        return nil, nil, "", fmt.Errorf("ffmpeg audio failed for video %d at %fs: %v", videoID, startTime, err)
    }
    log.Printf("Station %s: ffmpeg audio succeeded for %s", st.name, opusPath)
    cmdDur := exec.Command(
        "ffprobe",
        "-v", "error",
        "-show_entries", "format=duration",
        "-of", "default=noprint_wrappers=1:nokey=1",
        opusPath,
    )
    outputDur, err := cmdDur.Output()
    if err == nil {
        durStr := strings.TrimSpace(string(outputDur))
        dur, _ := strconv.ParseFloat(durStr, 64)
        log.Printf("Station %s: Opus file %s duration: %.2f s", st.name, opusPath, dur)
        if dur < adjustedChunkDur*0.9 || dur > adjustedChunkDur*1.1 {
            log.Printf("Station %s: Warning: Opus duration %.2fs does not match expected %.2fs", st.name, dur, adjustedChunkDur)
        }
    } else {
        log.Printf("Station %s: ffprobe failed for %s: %v", st.name, opusPath, err)
    }
    audioData, err := os.ReadFile(opusPath)
    if err != nil || len(audioData) == 0 {
        return nil, nil, "", fmt.Errorf("audio file %s is empty or unreadable: %v", opusPath, err)
    }
    log.Printf("Station %s: Read audio file %s, size: %d bytes, first 16 bytes: %x", st.name, opusPath, len(audioData), audioData[:min(16, len(audioData))])
    reader := bytes.NewReader(audioData)
    ogg, _, err := oggreader.NewWith(reader)
    if err != nil {
        return nil, nil, "", fmt.Errorf("failed to create ogg reader for %s: %v", opusPath, err)
    }
    var audioPackets [][]byte
    var packetDurations []int // Store durations for each packet in ms
    previousGranule := uint64(0)
    totalDurationMs := 0
    for {
        payload, header, err := ogg.ParseNextPage()
        if err == io.EOF {
            break
        }
        if err != nil {
            log.Printf("Station %s: Failed to parse ogg page for %s: %v", st.name, opusPath, err)
            continue
        }
        if len(payload) < 1 {
            log.Printf("Station %s: Empty ogg payload for %s, skipping", st.name, opusPath)
            continue
        }
        if len(payload) >= 8 && (string(payload[:8]) == "OpusHead" || string(payload[:8]) == "OpusTags") {
            log.Printf("Station %s: Skipping header packet: %s", st.name, string(payload[:8]))
            continue
        }
        frameDurationMs := 20
        currentGranule := header.GranulePosition
        if currentGranule > previousGranule {
            frameSamples := currentGranule - previousGranule
            frameDurationMs = int((frameSamples * 1000) / 48000)
            if frameDurationMs < 2 || frameDurationMs > 60 {
                log.Printf("Station %s: Invalid calculated duration %dms for packet %d, using 20ms", st.name, frameDurationMs, len(audioPackets))
                frameDurationMs = 20
            }
        }
        previousGranule = currentGranule
        totalDurationMs += frameDurationMs
        if totalDurationMs > int(adjustedChunkDur*1000*1.1) {
            log.Printf("Station %s: Total audio duration %dms exceeds expected %dms, stopping", st.name, totalDurationMs, int(adjustedChunkDur*1000))
            break
        }
        audioPackets = append(audioPackets, payload)
        packetDurations = append(packetDurations, frameDurationMs)
        log.Printf("Station %s: Extracted audio packet %d, size: %d, duration: %dms, granule: %d", st.name, len(audioPackets)-1, len(payload), frameDurationMs, currentGranule)
    }
    if len(audioPackets) == 0 {
        log.Printf("Station %s: No valid audio packets extracted from %s", st.name, opusPath)
        return nil, nil, "", fmt.Errorf("no valid audio packets extracted from %s", opusPath)
    }
    log.Printf("Station %s: Extracted %d audio packets, total duration: %dms", st.name, len(audioPackets), totalDurationMs)
    args = []string{
        "-y",
        "-ss", fmt.Sprintf("%.3f", startTime),
        "-i", fullEpisodePath,
        "-t", fmt.Sprintf("%.3f", adjustedChunkDur),
        "-c:v", "libx264",
        "-preset", "ultrafast",
        "-force_key_frames", "expr:gte(t,n_forced*2)",
        "-an",
        "-bsf:v", "h264_mp4toannexb",
        "-g", "60",
        fullSegPath,
    }
    cmdVideo := exec.Command("ffmpeg", args...)
    log.Printf("Station %s: Running ffmpeg video command: %v", st.name, cmdVideo.Args)
    outputVideo, err := cmdVideo.CombinedOutput()
    if err != nil {
        log.Printf("Station %s: ffmpeg video command failed: %v\nOutput: %s", st.name, err, string(outputVideo))
        return nil, nil, "", fmt.Errorf("ffmpeg video failed for video %d at %fs: %v", videoID, startTime, err)
    }
    segments = append(segments, fullSegPath)
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
    st.fpsCache.Store(fullSegPath, fpsPair{num: fpsNum, den: fpsDen})
    st.durationCache.Store(fullSegPath, adjustedChunkDur)
    data, err := os.ReadFile(fullSegPath)
    if err != nil || len(data) == 0 {
        log.Printf("Station %s: Failed to read video segment %s: %v", st.name, fullSegPath, err)
        return nil, nil, "", fmt.Errorf("failed to read video segment %s: %v", fullSegPath, err)
    }
    nalus := splitNALUs(data)
    if len(nalus) == 0 {
        log.Printf("Station %s: No NALUs found in segment %s", st.name, fullSegPath)
        return nil, nil, "", fmt.Errorf("no NALUs found in segment %s", fullSegPath)
    }
    hasIDR := false
    for _, nalu := range nalus {
        if len(nalu) > 0 && int(nalu[0]&0x1F) == 5 {
            hasIDR = true
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
        log.Printf("Station %s: No IDR frame in segment %s, may affect playback", st.name, fullSegPath)
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
        log.Printf("Station %s: No SPS/PPS found in segment %s", st.name, fullSegPath)
        fmtpLine = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f"
    }
    log.Printf("Station %s: Processed segment %s with %d NALUs, %d SPS/PPS, fmtp: %s", st.name, fullSegPath, len(nalus), len(spsPPS), fmtpLine)
    return segments, spsPPS, fmtpLine, nil
}

func manageProcessing(st *Station, db *sql.DB) {
	for {
		select {
		case <-st.stopProcessing:
			log.Printf("Station %s: Stopping processing due to no viewers", st.name)
			st.mu.Lock()
			st.processing = false
			for _, seg := range st.segmentList {
				os.Remove(seg)
				os.Remove(strings.Replace(seg, ".h264", ".opus", 1))
				os.Remove(strings.Replace(seg, ".h264", ".dur", 1))
				st.fpsCache.Delete(seg)
				st.durationCache.Delete(seg)
			}
			st.segmentList = nil
			st.spsPPS = nil
			st.fmtpLine = ""
			st.lastQueuedStart = 0
			st.mu.Unlock()
			return
		default:
			st.mu.Lock()
			if st.viewers == 0 || len(st.segmentList) == 0 {
				st.mu.Unlock()
				time.Sleep(time.Second)
				continue
			}
			remainingDur := 0.0
			for _, seg := range st.segmentList {
				if val, ok := st.durationCache.Load(seg); ok {
					remainingDur += val.(float64)
				}
			}
			if remainingDur >= BufferThreshold {
				st.mu.Unlock()
				time.Sleep(time.Second)
				continue
			}
			var currentVideoDur float64
			var dur sql.NullFloat64
			err := db.QueryRow(`SELECT duration FROM videos WHERE id = $1`, st.currentVideo).Scan(&dur)
			if err != nil || !dur.Valid {
				log.Printf("Station %s: Failed to get duration for video %d: %v", st.name, st.currentVideo, err)
				currentVideoDur = 3600
			} else {
				currentVideoDur = dur.Float64
			}
			log.Printf("Station %s: Current offset: %f, Remaining duration: %f, Current video: %d", st.name, st.currentOffset, remainingDur, st.currentVideo)
			nextStartTime := st.currentOffset + remainingDur
			if nextStartTime == st.lastQueuedStart {
				log.Printf("Station %s: Skipping duplicate chunk at %fs for video %d", st.name, nextStartTime, st.currentVideo)
				st.mu.Unlock()
				time.Sleep(time.Second)
				continue
			}
			if nextStartTime >= currentVideoDur {
				st.currentIndex++
				if st.currentIndex >= len(st.videoQueue) {
					st.currentIndex = 0
				}
				st.currentVideo = st.videoQueue[st.currentIndex]
				st.currentOffset = 0
				nextStartTime = 0
				st.spsPPS = nil
				st.fmtpLine = ""
				log.Printf("Station %s: Advanced to video %d, reset offset and start time", st.name, st.currentVideo)
			}
			log.Printf("Station %s: Processing chunk for video %d at %fs", st.name, st.currentVideo, nextStartTime)
			segments, spsPPS, fmtpLine, err := processVideo(st, st.currentVideo, db, nextStartTime, ChunkDuration)
			if err != nil {
				log.Printf("Station %s: Failed to process chunk for video %d at %fs: %v", st.name, st.currentVideo, nextStartTime, err)
				st.mu.Unlock()
				time.Sleep(5 * time.Second)
				continue
			}
			st.segmentList = append(st.segmentList, segments...)
			if len(st.spsPPS) == 0 {
				st.spsPPS = spsPPS
				st.fmtpLine = fmtpLine
			}
			st.lastQueuedStart = nextStartTime
			log.Printf("Station %s: Queued chunk at %fs, lastQueuedStart to %f", st.name, nextStartTime, st.lastQueuedStart)
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
		var duration sql.NullFloat64
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
        var duration sql.NullFloat64
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

func sender(st *Station, db *sql.DB) {
    for {
        st.mu.Lock()
        if st.viewers == 0 || len(st.segmentList) == 0 {
            st.mu.Unlock()
            time.Sleep(time.Second)
            continue
        }
        segPath := st.segmentList[0]
        st.mu.Unlock()
        testSample := media.Sample{Data: []byte{}, Duration: time.Duration(0)}
        err := st.trackVideo.WriteSample(testSample)
        if err != nil && strings.Contains(err.Error(), "not bound") {
            log.Printf("Station %s: Video track not bound, checking viewers", st.name)
            st.mu.Lock()
            if st.viewers == 0 {
                close(st.stopProcessing)
                st.stopProcessing = make(chan struct{})
                st.mu.Unlock()
                return
            }
            st.mu.Unlock()
            time.Sleep(time.Second)
            continue
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
            durSeg := strings.Replace(segPath, ".h264", ".dur", 1)
            os.Remove(durSeg)
            st.fpsCache.Delete(segPath)
            st.durationCache.Delete(segPath)
            continue
        }
        fpsNum := DefaultFPSNum
        fpsDen := DefaultFPSDen
        if val, ok := st.fpsCache.Load(segPath); ok {
            pair := val.(fpsPair)
            fpsNum = pair.num
            fpsDen = pair.den
        }
        frameDuration := time.Second * time.Duration(fpsDen) / time.Duration(fpsNum)
        nalus := splitNALUs(data)
        if len(nalus) == 0 {
            log.Printf("Station %s: No NALUs found in segment %s", st.name, segPath)
            st.mu.Lock()
            st.segmentList = st.segmentList[1:]
            st.mu.Unlock()
            os.Remove(segPath)
            opusSeg := strings.Replace(segPath, ".h264", ".opus", 1)
            os.Remove(opusSeg)
            durSeg := strings.Replace(segPath, ".h264", ".dur", 1)
            os.Remove(durSeg)
            st.fpsCache.Delete(segPath)
            st.durationCache.Delete(segPath)
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
                    payload, header, err := ogg.ParseNextPage()
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
                    log.Printf("Station %s: Parsed audio packet %d for %s, size: %d, granule: %d", st.name, len(audioPackets)-1, audioPath, len(payload), header.GranulePosition)
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
                const frameDurationMs = 20 // Fixed 20ms per packet, matching FFmpeg -frame_duration 20
                log.Printf("Station %s: Starting audio transmission for %s, %d packets", st.name, audioPath, len(packets))
                for i, pkt := range packets {
                    if len(pkt) < 1 {
                        log.Printf("Station %s: Empty audio packet %d, skipping", st.name, i)
                        continue
                    }
                    samples := (frameDurationMs * sampleRate) / 1000 // 960 samples for 20ms at 48kHz
                    if !boundChecked {
                        if err := st.trackAudio.WriteSample(testSample); err != nil {
                            log.Printf("Station %s: Audio track not bound for sample %d: %v", st.name, i, err)
                            break
                        }
                        boundChecked = true
                    }
                    sample := media.Sample{
                        Data:            pkt,
                        Duration:        time.Duration(frameDurationMs) * time.Millisecond,
                        PacketTimestamp: audioTimestamp,
                    }
                    if err := st.trackAudio.WriteSample(sample); err != nil {
                        log.Printf("Station %s: Audio sample %d write error: %v, packet size: %d", st.name, i, err, len(pkt))
                        if strings.Contains(err.Error(), "not bound") {
                            break
                        }
                    } else {
                        log.Printf("Station %s: Sent audio sample %d, size: %d, duration: %dms, timestamp: %d samples", st.name, i, len(pkt), frameDurationMs, audioTimestamp)
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
                        Data:            sampleData,
                        Duration:        frameDuration,
                        PacketTimestamp: videoTimestamp,
                    }
                    if err := st.trackVideo.WriteSample(sample); err != nil {
                        log.Printf("Station %s: Video sample write error: %v", st.name, err)
                        if strings.Contains(err.Error(), "not bound") {
                            return
                        }
                    }
                    log.Printf("Station %s: Sent video sample, size: %d, timestamp: %d samples", st.name, len(sampleData), videoTimestamp)
                    segmentSamples++
                    videoTimestamp += uint32((frameDuration * videoClockRate) / time.Second)
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
                                Data:            sampleData,
                                Duration:        frameDuration,
                                PacketTimestamp: videoTimestamp,
                            }
                            if err := st.trackVideo.WriteSample(sample); err != nil {
                                log.Printf("Station %s: Video sample write error: %v", st.name, err)
                                if strings.Contains(err.Error(), "not bound") {
                                    return
                                }
                            }
                            log.Printf("Station %s: Sent video sample, size: %d, timestamp: %d samples", st.name, len(sampleData), videoTimestamp)
                            segmentSamples++
                            videoTimestamp += uint32((frameDuration * videoClockRate) / time.Second)
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
                    Data:            sampleData,
                    Duration:        frameDuration,
                    PacketTimestamp: videoTimestamp,
                }
                if err := st.trackVideo.WriteSample(sample); err != nil {
                    log.Printf("Station %s: Video sample write error: %v", st.name, err)
                    if strings.Contains(err.Error(), "not bound") {
                        return
                    }
                }
                log.Printf("Station %s: Sent video sample, size: %d, timestamp: %d samples", st.name, len(sampleData), videoTimestamp)
                segmentSamples++
            }
            log.Printf("Station %s: Sent %d video frames for %s", st.name, segmentSamples, segPath)
        }(nalus)
        wg.Wait()
        os.Remove(segPath)
        opusSeg := strings.Replace(segPath, ".h264", ".opus", 1)
        os.Remove(opusSeg)
        durSeg := strings.Replace(segPath, ".h264", ".dur", 1)
        os.Remove(durSeg)
        st.mu.Lock()
        if val, ok := st.durationCache.Load(segPath); ok {
            st.currentOffset += val.(float64)
        }
        st.fpsCache.Delete(segPath)
        st.durationCache.Delete(segPath)
        st.segmentList = st.segmentList[1:]
        var duration sql.NullFloat64
        err = db.QueryRow(`SELECT duration FROM videos WHERE id = $1`, st.currentVideo).Scan(&duration)
        if err == nil && duration.Valid && st.currentOffset >= duration.Float64 {
            st.currentIndex++
            if st.currentIndex >= len(st.videoQueue) {
                st.currentIndex = 0
            }
            st.currentVideo = st.videoQueue[st.currentIndex]
            st.currentOffset = 0
            st.spsPPS = nil
            st.fmtpLine = ""
            log.Printf("Station %s: Switched to video %d", st.name, st.currentVideo)
        }
        st.mu.Unlock()
        time.Sleep(10 * time.Millisecond)
    }
}

// Helper function for min
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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

func parseRawOpusPackets(data []byte) ([][]byte, error) {
    var packets [][]byte
    i := 0
    for i < len(data) {
        if i+1 > len(data) {
            return packets, fmt.Errorf("incomplete Opus packet at position %d", i)
        }
        packetStart := i
        toc := data[i]
        i++
        config := (toc >> 3) & 0x1F
        code := toc & 0x3
        var frameLength int
        var numFrames int
        var lengthBytes int
        var baseFrameDurationMs int
        switch config {
        case 0, 1, 2, 3, 16, 17, 18, 19:
            baseFrameDurationMs = 10
        case 4, 5, 6, 7, 20, 21, 22, 23:
            baseFrameDurationMs = 20
        case 8, 9, 10, 11:
            baseFrameDurationMs = 40
        case 12, 13, 14, 15:
            baseFrameDurationMs = 60
        case 24, 25, 26, 27:
            baseFrameDurationMs = 2
        case 28, 29, 30, 31:
            baseFrameDurationMs = 5
        default:
            baseFrameDurationMs = 20
        }
        if code == 0 {
            if i+1 > len(data) {
                return packets, fmt.Errorf("incomplete frame length at position %d", i)
            }
            frameSize := int(data[i])
            i++
            lengthBytes = 1
            if frameSize == 0xFF {
                if i+1 > len(data) {
                    return packets, fmt.Errorf("incomplete extended frame length at position %d", i)
                }
                frameSize = int(data[i]) + 0xFF
                i++
                lengthBytes++
            }
            frameLength = frameSize
            numFrames = 1
        } else if code == 1 {
            if i+1 > len(data) {
                return packets, fmt.Errorf("incomplete frame length at position %d", i)
            }
            frameSize := int(data[i]) * 2
            i++
            lengthBytes = 1
            if frameSize/2 == 0xFF {
                if i+1 > len(data) {
                    return packets, fmt.Errorf("incomplete extended frame length at position %d", i)
                }
                frameSize = (int(data[i]) + 0xFF) * 2
                i++
                lengthBytes++
            }
            frameLength = frameSize
            numFrames = 2
        } else if code == 2 {
            if i+2 > len(data) {
                return packets, fmt.Errorf("incomplete frame lengths at position %d", i)
            }
            firstLength := int(data[i])
            i++
            lengthBytes = 1
            if firstLength == 0xFF {
                if i+1 > len(data) {
                    return packets, fmt.Errorf("incomplete extended first frame length at position %d", i)
                }
                firstLength = int(data[i]) + 0xFF
                i++
                lengthBytes++
            }
            secondLength := int(data[i])
            i++
            lengthBytes++
            if secondLength == 0xFF {
                if i+1 > len(data) {
                    return packets, fmt.Errorf("incomplete extended second frame length at position %d", i)
                }
                secondLength = int(data[i]) + 0xFF
                i++
                lengthBytes++
            }
            frameLength = firstLength + secondLength
            numFrames = 2
        } else if code == 3 {
            if i+1 > len(data) {
                return packets, fmt.Errorf("incomplete frame count at position %d", i)
            }
            vbr := (data[i] & 0x80) != 0
            numFrames = int(data[i]&0x3F) + 1
            if numFrames > 10 {
                log.Printf("Warning: Excessive numFrames %d at position %d, capping at 10", numFrames, packetStart)
                numFrames = 10
            }
            i++
            padding := (data[i] & 0x80) != 0
            i++
            paddingLength := 0
            if padding {
                for i < len(data) && data[i] == 0xFF {
                    paddingLength += 255
                    i++
                }
                if i < len(data) {
                    paddingLength += int(data[i])
                    i++
                } else {
                    return packets, fmt.Errorf("incomplete padding length at position %d", i)
                }
            }
            frameLengths := make([]int, numFrames)
            frameLength = 0
            if vbr {
                for j := 0; j < numFrames; j++ {
                    if i >= len(data) {
                        return packets, fmt.Errorf("incomplete frame length at position %d", i)
                    }
                    frameSize := int(data[i])
                    i++
                    lengthBytes++
                    if frameSize == 0xFF {
                        if i >= len(data) {
                            return packets, fmt.Errorf("incomplete extended frame length at position %d", i)
                        }
                        frameSize = int(data[i]) + 0xFF
                        i++
                        lengthBytes++
                    }
                    frameLengths[j] = frameSize
                    frameLength += frameSize
                }
            } else {
                if i >= len(data) {
                    return packets, fmt.Errorf("incomplete frame length at position %d", i)
                }
                frameSize := int(data[i])
                i++
                lengthBytes++
                if frameSize == 0xFF {
                    if i >= len(data) {
                        return packets, fmt.Errorf("incomplete extended frame length at position %d", i)
                    }
                    frameSize = int(data[i]) + 0xFF
                    i++
                    lengthBytes++
                }
                for j := 0; j < numFrames; j++ {
                    frameLengths[j] = frameSize
                }
                frameLength = frameSize * numFrames
            }
            if i+frameLength+paddingLength > len(data) {
                log.Printf("Warning: incomplete frame data at position %d, expected %d bytes, remaining %d bytes", i, frameLength+paddingLength, len(data)-i)
                continue
            }
            i += paddingLength
        } else {
            log.Printf("Invalid Opus code %d at position %d, skipping packet", code, packetStart)
            continue
        }
        if i+frameLength > len(data) {
            log.Printf("Warning: incomplete frame data at position %d, expected %d bytes, remaining %d bytes", i, frameLength, len(data)-i)
            continue
        }
        totalDurationMs := numFrames * baseFrameDurationMs
        if totalDurationMs > 200 {
            log.Printf("Warning: excessive duration %dms for packet at position %d, skipping", totalDurationMs, packetStart)
            continue
        }
        packet := data[packetStart : i+frameLength]
        packets = append(packets, packet)
        log.Printf("Parsed Opus packet, size: %d, config: %d, code: %d, numFrames: %d, frameLength: %d, duration: %dms", len(packet), config, code, numFrames, frameLength, totalDurationMs)
        i += frameLength
    }
    return packets, nil
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
		segments, spsPPS, fmtpLine, err := processVideo(st, st.currentVideo, db, st.currentOffset, ChunkDuration)
		if err != nil {
			log.Printf("Station %s: Failed to process initial chunk for video %d: %v", st.name, st.currentVideo, err)
			st.viewers--
			st.mu.Unlock()
			c.JSON(500, gin.H{"error": "Failed to process initial video chunk"})
			pc.Close()
			return
		}
		st.segmentList = segments
		st.spsPPS = spsPPS
		st.fmtpLine = fmtpLine
		audioPath := strings.Replace(segments[0], ".h264", ".opus", 1)
		if _, err := os.Stat(audioPath); os.IsNotExist(err) {
			log.Printf("Station %s: Audio file %s not found", st.name, audioPath)
			st.viewers--
			st.mu.Unlock()
			c.JSON(500, gin.H{"error": "Failed to process initial audio chunk"})
			pc.Close()
			return
		}
		st.lastQueuedStart = st.currentOffset
		nextStartTime := st.currentOffset + ChunkDuration
		var dur sql.NullFloat64
		err = db.QueryRow(`SELECT duration FROM videos WHERE id = $1`, st.currentVideo).Scan(&dur)
		if err == nil && dur.Valid && nextStartTime >= dur.Float64 {
			nextStartTime = dur.Float64 - st.currentOffset
		}
		segments, spsPPS, fmtpLine, err = processVideo(st, st.currentVideo, db, nextStartTime, ChunkDuration)
		if err != nil {
			log.Printf("Station %s: Failed to process second initial chunk for video %d: %v", st.name, st.currentVideo, err)
		} else {
			st.segmentList = append(st.segmentList, segments...)
			if len(st.spsPPS) == 0 {
				st.spsPPS = spsPPS
				st.fmtpLine = fmtpLine
			}
			st.lastQueuedStart = nextStartTime
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
<label for="serverUrl">Go Server Base URL (e.g., http://localhost:8081/signal):</label>
<input id="serverUrl" type="text" value="http://localhost:8081/signal">
<label for="station">Station (e.g., default, 1, 2):</label>
<input id="station" type="text" value="default" style="width: 100px;">
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
let offer = null;
let candidates = [];
async function createOffer() {
pc = new RTCPeerConnection(configuration);
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
offer = await pc.createOffer({ offerToReceiveVideo: true, offerToReceiveAudio: true });
await pc.setLocalDescription(offer);
log('Local Offer SDP: ' + offer.sdp);
}
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
', jitter=' + (report.jitter || 0));
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
}, 5000);
}
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
	adRows, err := db.Query(
		"SELECT v.uri FROM videos v JOIN video_tags vt ON v.id = vt.video_id WHERE vt.tag_id = 4")
	if err != nil {
		log.Fatalf("Failed to query commercials: %v", err)
	}
	defer adRows.Close()
	for adRows.Next() {
		var uri string
		if err := adRows.Scan(&uri); err != nil {
			log.Printf("Failed to scan ad URI: %v", err)
			continue
		}
		fullAdPath := filepath.Join(videoBaseDir, uri)
		if _, err := os.Stat(fullAdPath); err == nil {
			adFullPaths = append(adFullPaths, fullAdPath)
		} else {
			log.Printf("Ad file not found: %s", fullAdPath)
		}
	}
	if err := adRows.Err(); err != nil {
		log.Fatalf("Error iterating ads: %v", err)
	}
	log.Printf("Loaded %d commercials", len(adFullPaths))
	discoverStations(db)
	for _, st := range stations {
		go sender(st, db)
	}
	r := gin.Default()
	r.Use(cors.Default())
	r.POST("/signal", func(c *gin.Context) {
		signalingHandler(db, c)
	})
	r.GET("/", indexHandler)
	r.GET("/hls/*path", func(c *gin.Context) {
		c.String(404, "Use WebRTC")
	})
	log.Printf("WebRTC TV server on %s. Stations: %v", Port, stations)
	log.Fatal(r.Run(Port))
}