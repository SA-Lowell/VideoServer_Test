package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"log"
	"math/rand"
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
	HlsDir            = "./webrtc_segments"
	Port              = ":8081"
	ClockRate         = 90000
	AudioFrameMs      = 20
	DefaultFPSNum     = 30000
	DefaultFPSDen     = 1001
	DefaultDur        = 0.0
	DefaultStation    = "default"
	AdInsertPath      = "./ad_insert.exe"
	DefaultVideoBaseDir = "Z:/Videos"
	DefaultTempPrefix   = "ad_insert_"
	PreprocessTimeout = 30 * time.Second // Timeout for waiting on preprocessing
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
}

type Station struct {
	name          string
	segmentList   []string
	spsPPS        [][]byte
	fmtpLine      string
	trackVideo    *webrtc.TrackLocalStaticSample
	trackAudio    *webrtc.TrackLocalStaticSample
	fpsCache      sync.Map
	durationCache sync.Map
	videoQueue    []int64 // Ordered list of video IDs
	currentVideo  int64   // Current video ID
	currentIndex  int     // Index in videoQueue
	preprocessed  chan PreprocessedVideo // Buffered channel for preprocessed videos
	mu            sync.Mutex
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

func processVideo(st *Station, videoID int64, db *sql.DB) ([]string, [][]byte, string, error) {
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

	bpRows, err := db.Query(`SELECT value::numeric FROM video_metadata WHERE video_id = $1 AND metadata_type_id = 1 ORDER BY value::numeric ASC`, videoID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to query break_points for video %d: %v", videoID, err)
	}
	defer bpRows.Close()

	var breaks []float64
	for bpRows.Next() {
		var bp float64
		if err := bpRows.Scan(&bp); err != nil {
			log.Printf("Failed to scan break_point: %v", err)
			continue
		}
		breaks = append(breaks, bp)
	}
	if err := bpRows.Err(); err != nil {
		return nil, nil, "", fmt.Errorf("error iterating break_points: %v", err)
	}

	numBreaks := len(breaks)
	tempDir, err := os.MkdirTemp("", DefaultTempPrefix)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to create temp dir for video %d: %v", videoID, err)
	}
	defer os.RemoveAll(tempDir)

	fullMp4Path := filepath.Join(tempDir, "full_with_ads.mp4")
	segmentsDir := HlsDir

	var args []string
	args = append(args, fullEpisodePath, tempDir, fullMp4Path, segmentsDir, strconv.Itoa(numBreaks))

	for _, brk := range breaks {
		args = append(args, fmt.Sprintf("%.4f", brk))
		args = append(args, "3")
		for i := 0; i < 3; i++ {
			if len(adFullPaths) == 0 {
				log.Printf("No ads available for break")
				break
			}
			randIdx := rand.Intn(len(adFullPaths))
			args = append(args, adFullPaths[randIdx])
		}
	}

	var customNames []string
	for b := 0; b <= numBreaks; b++ {
		baseName := fmt.Sprintf("%s_vid%d_part%d", st.name, videoID, b+1)
		customNames = append(customNames, baseName)
		if b < numBreaks {
			for a := 0; a < 3; a++ {
				baseNameAd := fmt.Sprintf("%s_vid%d_break%d_ad%d", st.name, videoID, b+1, a+1)
				customNames = append(customNames, baseNameAd)
			}
		}
	}
	args = append(args, customNames...)

	cmd := exec.Command(AdInsertPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, nil, "", fmt.Errorf("ad_insert.exe failed for video %d: %v\nOutput: %s", videoID, err, string(output))
	}
	log.Printf("ad_insert.exe succeeded for video %d\nOutput: %s", videoID, string(output))

	for _, baseName := range customNames {
		segName := baseName + ".h264"
		fullSegPath := filepath.Join(HlsDir, segName)
		segments = append(segments, fullSegPath)
	}

	var wg sync.WaitGroup
	for _, segPath := range segments {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
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
				log.Printf("Station %s: Pre-probed FPS for %s: %d/%d (%.2f fps)", st.name, path, fpsNum, fpsDen, float64(fpsNum)/float64(fpsDen))
			} else {
				log.Printf("Station %s: ffprobe FPS failed for %s: %v (default %d/%d)", st.name, path, errFPS, fpsNum, fpsDen)
			}
			st.fpsCache.Store(path, fpsPair{num: fpsNum, den: fpsDen})
			durPath := strings.Replace(path, ".h264", ".dur", 1)
			dur := DefaultDur
			if durData, err := os.ReadFile(durPath); err == nil {
				durStr := strings.TrimSpace(string(durData))
				if d, err := strconv.ParseFloat(durStr, 64); err == nil {
					dur = d
				}
			}
			if dur <= 0 {
				log.Printf("Station %s: Invalid duration for %s, defaulting to 0 (will not sleep)", st.name, path)
			} else {
				log.Printf("Station %s: Pre-probed duration for %s: %.2fs", st.name, path, dur)
			}
			st.durationCache.Store(path, dur)
		}(segPath)
	}
	wg.Wait()

	for _, segPath := range segments {
		data, segErr := os.ReadFile(segPath)
		if segErr == nil && len(data) > 0 {
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
				log.Printf("Station %s: Extracted %d config NALUs (SPS/PPS) from %s", st.name, len(spsPPS), segPath)
				break
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
			log.Printf("Station %s: Detected H.264 profile-level-id: %s (fmtp: %s)", st.name, profileLevelID, fmtpLine)
		}
	}
	if fmtpLine == "" {
		fmtpLine = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=64001f"
		log.Printf("Station %s: Using fallback High Profile fmtp: %s", st.name, fmtpLine)
	}

	return segments, spsPPS, fmtpLine, nil
}

// preprocessNextVideos continuously preprocesses upcoming videos
func preprocessNextVideos(st *Station, db *sql.DB) {
	for {
		st.mu.Lock()
		// Determine the next video to preprocess
		nextIndex := st.currentIndex + 1
		if nextIndex >= len(st.videoQueue) {
			nextIndex = 0 // Loop back to start
		}
		nextVideoID := st.videoQueue[nextIndex]
		// Check if this video is already preprocessed
		if _, exists := st.fpsCache.Load("preprocessed_" + fmt.Sprintf("%d", nextVideoID)); exists {
			st.mu.Unlock()
			time.Sleep(5 * time.Second) // Wait before checking again
			continue
		}
		st.mu.Unlock()

		log.Printf("Station %s: Preprocessing video %d", st.name, nextVideoID)
		segments, spsPPS, fmtpLine, err := processVideo(st, nextVideoID, db)
		if err != nil {
			log.Printf("Station %s: Failed to preprocess video %d: %v, retrying in 5s", st.name, nextVideoID, err)
			time.Sleep(5 * time.Second)
			continue
		}

		// Store preprocessed data
		st.mu.Lock()
		st.fpsCache.Store("preprocessed_"+fmt.Sprintf("%d", nextVideoID), PreprocessedVideo{
			videoID:  nextVideoID,
			segments: segments,
			spsPPS:   spsPPS,
			fmtpLine: fmtpLine,
		})
		log.Printf("Station %s: Preprocessed video %d with %d segments", st.name, nextVideoID, len(segments))
		st.mu.Unlock()

		// Wait briefly to avoid overwhelming the system
		time.Sleep(1 * time.Second)
	}
}

func loadStation(stationName string, db *sql.DB) *Station {
	st := &Station{
		name:         stationName,
		currentIndex: 0,
		preprocessed: make(chan PreprocessedVideo, 2), // Buffer for up to 2 preprocessed videos
	}

	rows, err := db.Query(`
		SELECT sv.video_id
		FROM station_videos sv
		JOIN stations s ON sv.station_id = s.id
		WHERE s.name = $1
		ORDER BY sv.id ASC`, stationName)
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
	if err := rows.Err(); err != nil {
		log.Printf("Error iterating station_videos for station %s: %v", stationName, err)
		return nil
	}

	if len(videoIds) == 0 {
		log.Printf("No videos found for station %s", stationName)
		return nil
	}

	st.videoQueue = videoIds
	st.currentVideo = videoIds[0]

	// Process current video
	segments, spsPPS, fmtpLine, err := processVideo(st, st.currentVideo, db)
	if err != nil {
		log.Printf("Failed to process current video %d for station %s: %v", st.currentVideo, stationName, err)
		return nil
	}
	st.segmentList = segments
	st.spsPPS = spsPPS
	st.fmtpLine = fmtpLine

	// Start preprocessing goroutine
	go preprocessNextVideos(st, db)

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

	log.Printf("Station %s: Initialized with %d segments for video %d", stationName, len(st.segmentList), st.currentVideo)
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
		log.Fatal("No stations found in DB - populate the 'stations' and 'station_videos' tables")
	}
}

func sender(st *Station, db *sql.DB) {
	audioFrameDur := time.Duration(AudioFrameMs) * time.Millisecond
	cycleIndex := 0
	for {
		st.mu.Lock()
		if len(st.segmentList) == 0 {
			st.mu.Unlock()
			time.Sleep(time.Second)
			continue
		}
		if cycleIndex >= len(st.segmentList) {
			// Current video finished, move to next video
			oldSegments := st.segmentList
			st.segmentList = nil
			st.currentIndex++
			if st.currentIndex >= len(st.videoQueue) {
				st.currentIndex = 0 // Loop back to start
			}
			st.currentVideo = st.videoQueue[st.currentIndex]

			// Try to load preprocessed data
			key := "preprocessed_" + fmt.Sprintf("%d", st.currentVideo)
			var preprocessed PreprocessedVideo
			if val, ok := st.fpsCache.Load(key); ok {
				preprocessed = val.(PreprocessedVideo)
				st.segmentList = preprocessed.segments
				st.spsPPS = preprocessed.spsPPS
				st.fmtpLine = preprocessed.fmtpLine
				st.fpsCache.Delete(key)
				log.Printf("Station %s: Using preprocessed data for video %d with %d segments", st.name, st.currentVideo, len(st.segmentList))
			} else {
				// Fallback: Process synchronously with timeout
				log.Printf("Station %s: No preprocessed data for video %d, processing synchronously", st.name, st.currentVideo)
				timeout := time.After(PreprocessTimeout)
				done := make(chan struct{})
				var segments []string
				var spsPPS [][]byte
				var fmtpLine string
				var err error
				go func() {
					segments, spsPPS, fmtpLine, err = processVideo(st, st.currentVideo, db)
					close(done)
				}()
				select {
				case <-done:
					if err != nil {
						log.Printf("Station %s: Failed to process video %d: %v, retrying in 5s", st.name, st.currentVideo, err)
						st.mu.Unlock()
						time.Sleep(5 * time.Second)
						continue
					}
					st.segmentList = segments
					st.spsPPS = spsPPS
					st.fmtpLine = fmtpLine
				case <-timeout:
					log.Printf("Station %s: Timeout processing video %d, retrying in 5s", st.name, st.currentVideo)
					st.mu.Unlock()
					time.Sleep(5 * time.Second)
					continue
				}
			}

			// Clean up old segments
			for _, seg := range oldSegments {
				if err := os.Remove(seg); err != nil {
					log.Printf("Failed to remove old segment %s: %v", seg, err)
				}
				opusSeg := strings.Replace(seg, ".h264", ".opus", 1)
				if err := os.Remove(opusSeg); err != nil {
					log.Printf("Failed to remove old opus segment %s: %v", opusSeg, err)
				}
				durSeg := strings.Replace(seg, ".h264", ".dur", 1)
				if err := os.Remove(durSeg); err != nil {
					log.Printf("Failed to remove old duration file %s: %v", durSeg, err)
				}
				st.fpsCache.Delete(seg)
				st.durationCache.Delete(seg)
			}

			cycleIndex = 0
			log.Printf("Station %s: Switched to video %d with %d segments", st.name, st.currentVideo, len(st.segmentList))
		}
		segPath := st.segmentList[cycleIndex]
		st.mu.Unlock()

		testSample := media.Sample{Data: []byte{}, Duration: time.Duration(0)}
		err := st.trackVideo.WriteSample(testSample)
		if err != nil && strings.Contains(err.Error(), "not bound") {
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
			cycleIndex++
			continue
		} else if err != nil {
			log.Printf("Station %s: Track test write error: %v (continuing)", st.name, err)
		}
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
		fpsNum := DefaultFPSNum
		fpsDen := DefaultFPSDen
		if val, ok := st.fpsCache.Load(segPath); ok {
			pair := val.(fpsPair)
			fpsNum = pair.num
			fpsDen = pair.den
			log.Printf("Station %s: Cache hit FPS for %s: %d/%d (%.2f fps)", st.name, segPath, fpsNum, fpsDen, float64(fpsNum)/float64(fpsDen))
		}
		frameDuration := time.Second * time.Duration(fpsDen) / time.Duration(fpsNum)
		nalus := splitNALUs(data)
		log.Printf("Station %s: Split %d NALUs from %s", st.name, len(nalus), segPath)
		allNALUs := nalus
		if len(st.spsPPS) > 0 {
			allNALUs = append(st.spsPPS, nalus...)
			log.Printf("Station %s: Prefixed %d config NALUs to segment", st.name, len(st.spsPPS))
		}
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
		var wg sync.WaitGroup
		wg.Add(1)
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
		go func(nalus [][]byte) {
			defer wg.Done()
			videoStart := time.Now()
			videoTs := time.Duration(0)
			segmentSamples := 0
			idrSent := false
			var currentFrame [][]byte
			var hasVCL bool
			boundChecked := false
			for _, nalu := range nalus {
				if len(nalu) == 0 {
					continue
				}
				nalType := int(nalu[0] & 0x1F)
				isVCL := nalType >= 1 && nalType <= 5
				if hasVCL && !isVCL {
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
			cycleIndex++
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
			SDPFmtpLine: "minptime=10;useinbandfec=1",
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
            offer = await pc.createOffer({
                offerToReceiveVideo: true,
                offerToReceiveAudio: true
            });
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

	adRows, err := db.Query(`
		SELECT v.uri 
		FROM videos v 
		JOIN video_tags vt ON v.id = vt.video_id 
		WHERE vt.tag_id = 4`)
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
	r.POST("/signal", signalingHandler)
	r.GET("/", indexHandler)
	r.GET("/hls/*path", func(c *gin.Context) { c.String(404, "Use WebRTC") })
	log.Printf("WebRTC TV server on %s. Stations: %v", Port, stations)
	log.Fatal(r.Run(Port))
}