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

	// Probe source audio properties
	var sampleRate, channels int
	cmdProbe := exec.Command(
		"ffprobe",
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=sample_rate,channels,codec_name",
		"-of", "json",
		fullEpisodePath,
	)
	outputProbe, err := cmdProbe.Output()
	if err == nil {
		var result struct {
			Streams []struct {
				SampleRate string `json:"sample_rate"`
				Channels   int    `json:"channels"`
				CodecName  string `json:"codec_name"`
			} `json:"streams"`
		}
		if err := json.Unmarshal(outputProbe, &result); err == nil && len(result.Streams) > 0 {
			sampleRate, _ = strconv.Atoi(result.Streams[0].SampleRate)
			channels = result.Streams[0].Channels
			codec := result.Streams[0].CodecName
			log.Printf("Station %s: Probed audio for %s: sample_rate=%d, channels=%d, codec=%s", st.name, fullEpisodePath, sampleRate, channels, codec)
		} else {
			log.Printf("Station %s: Failed to parse ffprobe audio JSON for %s: %v", st.name, fullEpisodePath, err)
		}
	} else {
		log.Printf("Station %s: Failed to probe input audio for %s: %v", st.name, fullEpisodePath, err)
	}
	if sampleRate == 0 {
		sampleRate = 48000 // Fallback
	}
	if channels == 0 {
		channels = 2 // Fallback
	}

	// Audio encoding with dynaudnorm
	args := []string{
		"-y",
		"-ss", fmt.Sprintf("%.3f", startTime),
		"-i", fullEpisodePath,
		"-t", fmt.Sprintf("%.3f", adjustedChunkDur),
		"-map", "0:a:0",
		"-c:a", "libopus",
		"-b:a", "128k",
		"-ar", fmt.Sprintf("%d", sampleRate),
		"-ac", fmt.Sprintf("%d", channels),
		"-frame_duration", "20",
		"-page_duration", "20000",
		"-application", "audio",
		"-vbr", "on",
		"-avoid_negative_ts", "make_zero",
		"-fflags", "+genpts",
		"-f", "opus",
		opusPath,
	}
	audioFilter := "dynaudnorm=f=150:g=15,aresample=async=1:min_hard_comp=0.100000:first_pts=0"
	args = append(args[:len(args)-3], append([]string{"-af", audioFilter}, args[len(args)-3:]...)...)
	log.Printf("Station %s: Applying audio filter: %s", st.name, audioFilter)
	cmdAudio := exec.Command("ffmpeg", args...)
	outputAudio, err := cmdAudio.CombinedOutput()
	if err != nil {
		log.Printf("Station %s: ffmpeg audio command failed for %s: %v\nOutput: %s", st.name, opusPath, err, string(outputAudio))
		// Fallback to copy if normalization fails
		log.Printf("Station %s: Falling back to audio copy for %s due to normalization failure", st.name, opusPath)
		args = []string{
			"-y",
			"-ss", fmt.Sprintf("%.3f", startTime),
			"-i", fullEpisodePath,
			"-t", fmt.Sprintf("%.3f", adjustedChunkDur),
			"-map", "0:a:0",
			"-c:a", "copy",
			"-f", "opus",
			opusPath,
		}
		cmdAudio = exec.Command("ffmpeg", args...)
		outputAudio, err = cmdAudio.CombinedOutput()
		if err != nil {
			log.Printf("Station %s: ffmpeg audio copy failed for %s: %v\nOutput: %s", st.name, opusPath, err, string(outputAudio))
			return nil, nil, "", 0, fpsPair{}, fmt.Errorf("ffmpeg audio processing failed for video %d at %fs: %v", videoID, startTime, err)
		}
		log.Printf("Station %s: ffmpeg audio copy succeeded for %s", st.name, opusPath)
	} else {
		log.Printf("Station %s: ffmpeg audio succeeded for %s", st.name, opusPath)
	}
	audioData, err := os.ReadFile(opusPath)
	if err != nil || len(audioData) == 0 {
		log.Printf("Station %s: Audio file %s is empty or unreadable: %v, size=%d", st.name, opusPath, err, len(audioData))
		return nil, nil, "", 0, fpsPair{}, fmt.Errorf("audio file %s is empty or unreadable: %v", opusPath, err)
	}
	log.Printf("Station %s: Read audio file %s, size: %d bytes", st.name, opusPath, len(audioData))

	// Video encoding
	args = []string{
		"-y",
		"-ss", fmt.Sprintf("%.3f", startTime),
		"-i", fullEpisodePath,
		"-t", fmt.Sprintf("%.3f", adjustedChunkDur),
		"-c:v", "libx264",
		"-preset", "fast",
		"-crf", "23",
		"-profile:v", "baseline",
		"-level", "3.0",
		"-pix_fmt", "yuv420p",
		"-force_key_frames", "expr:gte(t,n_forced*2)",
		"-sc_threshold", "0",
		"-an",
		"-bsf:v", "h264_mp4toannexb",
		"-g", "60",
		"-reset_timestamps", "1",
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

	// Get video duration
	cmdDur := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "json",
		fullSegPath,
	)
	outputDur, err := cmdDur.Output()
	var actualDur float64
	if err == nil {
		var result struct {
			Format struct {
				Duration string `json:"duration"`
			} `json:"format"`
		}
		if err := json.Unmarshal(outputDur, &result); err != nil {
			log.Printf("Station %s: Failed to parse ffprobe JSON for %s: %v", st.name, fullSegPath, err)
		} else if result.Format.Duration != "" {
			actualDur, err = strconv.ParseFloat(result.Format.Duration, 64)
			if err != nil {
				log.Printf("Station %s: Failed to parse duration for %s: %v", st.name, fullSegPath, err)
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
				log.Printf("Station %s: Failed to parse source ffprobe JSON for %s: %v", st.name, fullEpisodePath, err)
			} else if result.Format.Duration != "" {
				actualDur, err = strconv.ParseFloat(result.Format.Duration, 64)
				if err != nil {
					log.Printf("Station %s: Failed to parse source duration for %s: %v", st.name, fullEpisodePath, err)
				} else {
					actualDur = math.Min(actualDur, adjustedChunkDur)
					log.Printf("Station %s: Video segment %s duration from source: %.3fs", st.name, fullSegPath, actualDur)
				}
			}
		}
		if actualDur == 0 {
			log.Printf("Station %s: All duration probes failed, using adjustedChunkDur %.3f", st.name, adjustedChunkDur)
			actualDur = adjustedChunkDur
		}
	}

	// Check audio duration and re-encode if mismatch
	cmdAudioDur := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "json",
		opusPath,
	)
	outputAudioDur, err := cmdAudioDur.Output()
	var audioDur float64
	if err == nil {
		var result struct {
			Format struct {
				Duration string `json:"duration"`
			} `json:"format"`
		}
		if err := json.Unmarshal(outputAudioDur, &result); err != nil {
			log.Printf("Station %s: Failed to parse audio duration JSON for %s: %v", st.name, opusPath, err)
			audioDur = actualDur
		} else if result.Format.Duration != "" {
			audioDur, err = strconv.ParseFloat(result.Format.Duration, 64)
			if err != nil {
				log.Printf("Station %s: Failed to parse audio duration for %s: %v", st.name, opusPath, err)
				audioDur = actualDur
			} else {
				log.Printf("Station %s: Audio segment %s duration: %.3fs", st.name, opusPath, audioDur)
			}
		}
	} else {
		log.Printf("Station %s: ffprobe audio duration failed for %s: %v, assuming video duration %.3fs", st.name, opusPath, err, actualDur)
		audioDur = actualDur
	}
	if math.Abs(audioDur-actualDur) > 0.1 {
		log.Printf("Station %s: Audio duration %.3fs differs from video duration %.3fs, re-encoding audio", st.name, audioDur, actualDur)
		args = []string{
			"-y",
			"-ss", fmt.Sprintf("%.3f", startTime),
			"-i", fullEpisodePath,
			"-t", fmt.Sprintf("%.3f", actualDur),
			"-map", "0:a:0",
			"-c:a", "libopus",
			"-b:a", "128k",
			"-ar", fmt.Sprintf("%d", sampleRate),
			"-ac", fmt.Sprintf("%d", channels),
			"-frame_duration", "20",
			"-page_duration", "20000",
			"-application", "audio",
			"-vbr", "on",
			"-avoid_negative_ts", "make_zero",
			"-fflags", "+genpts",
			"-f", "opus",
			opusPath,
		}
		args = append(args[:len(args)-3], append([]string{"-af", audioFilter}, args[len(args)-3:]...)...)
		cmdAudio := exec.Command("ffmpeg", args...)
		outputAudio, err = cmdAudio.CombinedOutput()
		if err != nil {
			log.Printf("Station %s: ffmpeg audio re-encode failed for %s: %v\nOutput: %s", st.name, opusPath, err, string(outputAudio))
			// Fallback to copy on re-encode failure
			log.Printf("Station %s: Falling back to audio copy for %s due to re-encode failure", st.name, opusPath)
			args = []string{
				"-y",
				"-ss", fmt.Sprintf("%.3f", startTime),
				"-i", fullEpisodePath,
				"-t", fmt.Sprintf("%.3f", actualDur),
				"-map", "0:a:0",
				"-c:a", "copy",
				"-f", "opus",
				opusPath,
			}
			cmdAudio = exec.Command("ffmpeg", args...)
			outputAudio, err = cmdAudio.CombinedOutput()
			if err != nil {
				log.Printf("Station %s: ffmpeg audio copy failed for %s: %v\nOutput: %s", st.name, opusPath, err, string(outputAudio))
				return nil, nil, "", 0, fpsPair{}, fmt.Errorf("ffmpeg audio re-encode and copy failed for video %d at %fs: %v", videoID, startTime, err)
			}
			log.Printf("Station %s: ffmpeg audio copy succeeded for %s", st.name, opusPath)
		} else {
			log.Printf("Station %s: ffmpeg audio re-encode succeeded for %s", st.name, opusPath)
		}
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
			"-preset", "fast",
			"-crf", "23",
			"-profile:v", "baseline",
			"-level", "3.0",
			"-pix_fmt", "yuv420p",
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
	fmtpLine = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42000a"
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
	const maxRetries = 3 // Limit retries per chunk to avoid infinite loops
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
			log.Printf("Station %s (adsEnabled: %v): Remaining buffer %.3fs, non-ad %.3fs, current video %d, offset %.3f", st.name, st.adsEnabled, remainingDur, sumNonAd, st.currentVideo, st.currentOffset)
			if remainingDur < BufferThreshold {
				videoDur := getVideoDur(st.currentVideo, db)
				if videoDur <= 0 {
					log.Printf("Station %s (adsEnabled: %v): Invalid duration for video %d, advancing to next video", st.name, st.adsEnabled, st.currentVideo)
					st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
					st.currentVideo = st.videoQueue[st.currentIndex]
					st.currentOffset = 0.0
					st.spsPPS = nil
					st.fmtpLine = ""
					sumNonAd = 0.0
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
					sumNonAd = 0.0
					st.mu.Unlock()
					continue
				}
				breaks, err := getBreakPoints(st.currentVideo, db)
				if err != nil {
					log.Printf("Station %s (adsEnabled: %v): Failed to get break points for video %d: %v", st.name, st.adsEnabled, st.currentVideo, err)
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
				if chunkEnd > videoDur {
					chunkEnd = videoDur
					chunkDur = videoDur - nextStart
				}
				if nextBreak < chunkEnd {
					chunkEnd = nextBreak
					chunkDur = nextBreak - nextStart
				}
				if chunkDur > 0 {
					var retryCount int
					for retryCount < maxRetries {
						segments, spsPPS, fmtpLine, actualDur, fps, err := processVideo(st, st.currentVideo, db, nextStart, chunkDur)
						if err != nil {
							log.Printf("Station %s (adsEnabled: %v): Failed to process episode chunk for video %d at %.3fs (retry %d/%d): %v", st.name, st.adsEnabled, st.currentVideo, nextStart, retryCount+1, maxRetries, err)
							retryCount++
							if retryCount == maxRetries {
								st.mu.Unlock()
								time.Sleep(5 * time.Second)
								continue
							}
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
						log.Printf("Station %s (adsEnabled: %v): Queued episode chunk for video %d at %.3fs, duration %.3fs", st.name, st.adsEnabled, st.currentVideo, nextStart, actualDur)
						break
					}
				}
				// Skip ad insertion for no-ads stations
				if !st.adsEnabled && nextBreak == chunkEnd && len(breaks) > 0 {
					log.Printf("Station %s (adsEnabled: %v): Skipping ad break at %.3fs for video %d", st.name, st.adsEnabled, nextBreak, st.currentVideo)
					// Pre-queue next episode segment
					nextStart = st.currentOffset + sumNonAd
					if nextStart < videoDur {
						var retryCount int
						for retryCount < maxRetries {
							segments, spsPPS, fmtpLine, actualDur, fps, err := processVideo(st, st.currentVideo, db, nextStart, ChunkDuration)
							if err != nil {
								log.Printf("Station %s (adsEnabled: %v): Failed to pre-queue episode chunk after skipped ad break for video %d at %.3fs (retry %d/%d): %v", st.name, st.adsEnabled, st.currentVideo, nextStart, retryCount+1, maxRetries, err)
								retryCount++
								if retryCount == maxRetries {
									break
								}
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
							log.Printf("Station %s (adsEnabled: %v): Pre-queued episode chunk after skipped ad break for video %d at %.3fs, duration %.3fs", st.name, st.adsEnabled, st.currentVideo, nextStart, actualDur)
							break
						}
					}
					continue
				}
				if nextBreak == chunkEnd && len(breaks) > 0 {
					log.Printf("Station %s (adsEnabled: %v): Inserting ad break at %.3fs for video %d", st.name, st.adsEnabled, nextBreak, st.currentVideo)
					availableAds := make([]int64, len(adIDs))
					copy(availableAds, adIDs)
					adDurTotal := 0.0
					for i := 0; i < 3 && len(availableAds) > 0; i++ {
						idx := rand.Intn(len(availableAds))
						adID := availableAds[idx]
						adDur := getVideoDur(adID, db)
						if adDur <= 0 {
							log.Printf("Station %s (adsEnabled: %v): Invalid duration for ad %d, skipping", st.name, st.adsEnabled, adID)
							availableAds = append(availableAds[:idx], availableAds[idx+1:]...)
							i--
							continue
						}
						var retryCount int
						for retryCount < maxRetries {
							segments, spsPPS, fmtpLine, actualDur, fps, err := processVideo(st, adID, db, 0, adDur)
							if err != nil {
								log.Printf("Station %s (adsEnabled: %v): Failed to process ad %d (retry %d/%d): %v", st.name, st.adsEnabled, adID, retryCount+1, maxRetries, err)
								retryCount++
								if retryCount == maxRetries {
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
								dur:     actualDur,
								isAd:    true,
								videoID: adID,
								fps:     fps,
							}
							st.segmentList = append(st.segmentList, adChunk)
							remainingDur += actualDur
							adDurTotal += actualDur
							log.Printf("Station %s (adsEnabled: %v): Queued ad %d with duration %.3fs at break %.3fs", st.name, st.adsEnabled, adID, actualDur, nextBreak)
							availableAds = append(availableAds[:idx], availableAds[idx+1:]...)
							break
						}
					}
					if adDurTotal > 0 {
						nextStart = st.currentOffset + sumNonAd
						if nextStart < videoDur {
							var retryCount int
							for retryCount < maxRetries {
								segments, spsPPS, fmtpLine, actualDur, fps, err := processVideo(st, st.currentVideo, db, nextStart, ChunkDuration)
								if err != nil {
									log.Printf("Station %s (adsEnabled: %v): Failed to pre-queue episode chunk after ads for video %d at %.3fs (retry %d/%d): %v", st.name, st.adsEnabled, st.currentVideo, nextStart, retryCount+1, maxRetries, err)
									retryCount++
									if retryCount == maxRetries {
										break
									}
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
								log.Printf("Station %s (adsEnabled: %v): Pre-queued episode chunk after ads for video %d at %.3fs, duration %.3fs", st.name, st.adsEnabled, st.currentVideo, nextStart, actualDur)
								break
							}
						}
					}
				}
			}
			log.Printf("Station %s (adsEnabled: %v): Buffer check complete, remainingDur %.3fs", st.name, st.adsEnabled, remainingDur)
			st.mu.Unlock()
			time.Sleep(time.Second)
		}
	}
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
	if originalSt != nil {
		// For no-ads station, copy initial state from original station
		unixStart = 0 // Will be set based on DB query
		videoIds = make([]int64, len(originalSt.videoQueue))
		copy(videoIds, originalSt.videoQueue)
		currentVideoID = originalSt.currentVideo
		currentVideoIndex = originalSt.currentIndex
		currentOffset = originalSt.currentOffset
	}
	// Fetch station details from DB
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
	if originalSt == nil {
		videoIds = nil // Reset videoIds if not copying from original
		for rows.Next() {
			var vid int64
			if err := rows.Scan(&vid); err != nil {
				log.Printf("Failed to scan video_id for station %s: %v", stationName, err)
				continue
			}
			videoIds = append(videoIds, vid)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("Error iterating station_videos: %v", err)
		return nil
	}
	if len(videoIds) == 0 {
		log.Printf("No videos found for station %s", stationName)
		return nil
	}
	st.videoQueue = videoIds
	// Calculate current position if not copied from original station
	if originalSt == nil {
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
		currentOffset = remainingSeconds
		currentVideoIndex = 0
		currentVideoID = 0
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
	}
	st.currentVideo = currentVideoID
	st.currentIndex = currentVideoIndex
	st.currentOffset = currentOffset
	st.trackVideo, err = webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
		fmt.Sprintf("video_%s_%t", sanitizeTrackID(stationName), adsEnabled), // Unique track ID
		"pion",
	)
	if err != nil {
		log.Printf("Station %s: Failed to create video track: %v", stationName, err)
		return nil
	}
	st.trackAudio, err = webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		fmt.Sprintf("audio_%s_%t", sanitizeTrackID(stationName), adsEnabled), // Unique track ID
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

func sender(st *Station, db *sql.DB) {
	for {
		select {
		case <-st.stopCh:
			log.Printf("Station %s (adsEnabled: %v): Stopping sender due to no viewers", st.name, st.adsEnabled)
			return
		default:
			st.mu.Lock()
			if st.viewers == 0 || len(st.segmentList) == 0 {
				log.Printf("Station %s (adsEnabled: %v): No viewers or empty segment list, waiting", st.name, st.adsEnabled)
				st.mu.Unlock()
				time.Sleep(time.Second)
				continue
			}
			chunk := st.segmentList[0]
			segPath := chunk.segPath
			log.Printf("Station %s (adsEnabled: %v): Sending chunk %s (video %d, isAd: %v, duration: %.3fs)", st.name, st.adsEnabled, segPath, chunk.videoID, chunk.isAd, chunk.dur)
			st.mu.Unlock()
			testSample := media.Sample{Data: []byte{}, Duration: time.Duration(0)}
			err := st.trackVideo.WriteSample(testSample)
			if err != nil {
				log.Printf("Station %s (adsEnabled: %v): Video track write test error: %v", st.name, st.adsEnabled, err)
				if strings.Contains(err.Error(), "not bound") {
					log.Printf("Station %s (adsEnabled: %v): Video track not bound, checking viewers", st.name, st.adsEnabled)
					st.mu.Lock()
					if st.viewers == 0 {
						close(st.stopCh)
						st.stopCh = make(chan struct{})
					}
					st.mu.Unlock()
					time.Sleep(time.Second)
					continue
				}
			}
			data, err := os.ReadFile(segPath)
			if err != nil || len(data) == 0 {
				log.Printf("Station %s (adsEnabled: %v): Segment %s read error or empty: %v", st.name, st.adsEnabled, segPath, err)
				st.mu.Lock()
				st.segmentList = st.segmentList[1:]
				st.mu.Unlock()
				os.Remove(segPath)
				opusSeg := strings.Replace(segPath, ".h264", ".opus", 1)
				os.Remove(opusSeg)
				continue
			}
			if chunk.dur <= 0 {
				log.Printf("Station %s (adsEnabled: %v): Skipping chunk %s with zero duration", st.name, st.adsEnabled, segPath)
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
				log.Printf("Station %s (adsEnabled: %v): No NALUs found in segment %s", st.name, st.adsEnabled, segPath)
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
					log.Printf("Station %s (adsEnabled: %v): Failed to create ogg reader for %s: %v", st.name, st.adsEnabled, audioPath, err)
				} else {
					for {
						payload, _, err := ogg.ParseNextPage()
						if err == io.EOF {
							break
						}
						if err != nil {
							log.Printf("Station %s (adsEnabled: %v): Failed to parse ogg page for %s: %v", st.name, st.adsEnabled, audioPath, err)
							continue
						}
						if len(payload) < 1 {
							log.Printf("Station %s (adsEnabled: %v): Empty ogg payload for %s, skipping", st.name, st.adsEnabled, audioPath)
							continue
						}
						if len(payload) >= 8 && (string(payload[:8]) == "OpusHead" || string(payload[:8]) == "OpusTags") {
							log.Printf("Station %s (adsEnabled: %v): Skipping header packet: %s", st.name, st.adsEnabled, string(payload[:8]))
							continue
						}
						audioPackets = append(audioPackets, payload)
					}
					log.Printf("Station %s (adsEnabled: %v): Parsed %d audio packets for %s", st.name, st.adsEnabled, len(audioPackets), audioPath)
				}
			} else {
				log.Printf("Station %s (adsEnabled: %v): Failed to read audio %s: %v", st.name, st.adsEnabled, audioPath, err)
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
					log.Printf("Station %s (adsEnabled: %v): Starting audio transmission for %s, %d packets", st.name, st.adsEnabled, audioPath, len(packets))
					for i, pkt := range packets {
						if len(pkt) < 1 {
							log.Printf("Station %s (adsEnabled: %v): Empty audio packet %d, skipping", st.name, st.adsEnabled, i)
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
								log.Printf("Station %s (adsEnabled: %v): Audio track not bound for sample %d: %v", st.name, st.adsEnabled, i, err)
								break
							}
							boundChecked = true
						}
						sample := media.Sample{
							Data:            pkt,
							Duration:        time.Duration(baseFrameDurationMs) * time.Millisecond,
							PacketTimestamp: audioTimestamp,
						}
						if err := st.trackAudio.WriteSample(sample); err != nil {
							log.Printf("Station %s (adsEnabled: %v): Audio sample %d write error: %v, packet size: %d", st.name, st.adsEnabled, i, err, len(pkt))
							break
						}
						audioTimestamp += uint32(samples)
						targetTime := audioStart.Add(time.Duration(audioTimestamp*1000/sampleRate) * time.Millisecond)
						now := time.Now()
						if now.Before(targetTime) {
							time.Sleep(targetTime.Sub(now))
						}
					}
					log.Printf("Station %s (adsEnabled: %v): Sent %d audio packets for %s, total timestamp: %d samples", st.name, st.adsEnabled, len(packets), audioPath, audioTimestamp)
				}(audioPackets)
			} else {
				log.Printf("Station %s (adsEnabled: %v): No audio packets to send for %s", st.name, st.adsEnabled, audioPath)
			}
			wg.Add(1)
			go func(nalus [][]byte) {
				defer wg.Done()
				var allNALUs [][]byte
				if len(st.spsPPS) > 0 {
					allNALUs = append(st.spsPPS, nalus...)
					log.Printf("Station %s (adsEnabled: %v): Prefixed %d config NALUs to segment %s", st.name, st.adsEnabled, len(st.spsPPS), segPath)
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
				expectedFrames := int(math.Ceil(chunk.dur * float64(fpsNum) / float64(fpsDen)))
				if expectedFrames == 0 {
					expectedFrames = 1
				}
				frameInterval := time.Duration(float64(chunk.dur) / float64(expectedFrames) * float64(time.Second))
				log.Printf("Station %s (adsEnabled: %v): Sending %s with %d expected frames, frame interval %v", st.name, st.adsEnabled, segPath, expectedFrames, frameInterval)
				for _, nalu := range allNALUs {
					if len(nalu) == 0 {
						log.Printf("Station %s (adsEnabled: %v): Empty NALU in segment %s, skipping", st.name, st.adsEnabled, segPath)
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
								log.Printf("Station %s (adsEnabled: %v): Video track not bound: %v", st.name, st.adsEnabled, err)
								return
							}
							boundChecked = true
						}
						sample := media.Sample{
							Data:            sampleData,
							Duration:        frameInterval,
							PacketTimestamp: videoTimestamp,
						}
						if err := st.trackVideo.WriteSample(sample); err != nil {
							log.Printf("Station %s (adsEnabled: %v): Video sample write error: %v", st.name, st.adsEnabled, err)
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
								log.Printf("Station %s (adsEnabled: %v): Failed to parse first_mb_in_slice for NALU in %s: %v", st.name, st.adsEnabled, segPath, err)
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
										log.Printf("Station %s (adsEnabled: %v): Video track not bound: %v", st.name, st.adsEnabled, err)
										return
									}
									boundChecked = true
								}
								sample := media.Sample{
									Data:            sampleData,
									Duration:        frameInterval,
									PacketTimestamp: videoTimestamp,
								}
								if err := st.trackVideo.WriteSample(sample); err != nil {
									log.Printf("Station %s (adsEnabled: %v): Video sample write error: %v", st.name, st.adsEnabled, err)
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
							log.Printf("Station %s (adsEnabled: %v): Video track not bound: %v", st.name, st.adsEnabled, err)
							return
						}
						boundChecked = true
					}
					sample := media.Sample{
						Data:            sampleData,
						Duration:        frameInterval,
						PacketTimestamp: videoTimestamp,
					}
					if err := st.trackVideo.WriteSample(sample); err != nil {
						log.Printf("Station %s (adsEnabled: %v): Video sample write error: %v", st.name, st.adsEnabled, err)
						return
					}
					segmentSamples++
				}
				log.Printf("Station %s (adsEnabled: %v): Sent %d video frames for %s", st.name, st.adsEnabled, segmentSamples, segPath)
			}(nalus)
			wg.Wait()
			os.Remove(segPath)
			opusSeg := strings.Replace(segPath, ".h264", ".opus", 1)
			os.Remove(opusSeg)
			st.mu.Lock()
			if !chunk.isAd {
				st.currentOffset += chunk.dur
				log.Printf("Station %s (adsEnabled: %v): Updated offset to %.3fs for video %d", st.name, st.adsEnabled, st.currentOffset, st.currentVideo)
				videoDur := getVideoDur(st.currentVideo, db)
				if videoDur > 0 && st.currentOffset >= videoDur {
					log.Printf("Station %s (adsEnabled: %v): Completed video %d, advancing to next", st.name, st.adsEnabled, st.currentVideo)
					st.currentIndex = (st.currentIndex + 1) % len(st.videoQueue)
					st.currentVideo = st.videoQueue[st.currentIndex]
					st.currentOffset = 0.0
					st.spsPPS = nil
					st.fmtpLine = ""
				}
			}
			st.segmentList = st.segmentList[1:]
			log.Printf("Station %s (adsEnabled: %v): Removed chunk %s from segment list, %d chunks remain", st.name, st.adsEnabled, segPath, len(st.segmentList))
			st.mu.Unlock()
			time.Sleep(time.Millisecond)
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
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42000a",
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
		if len(st.spsPPS) == 0 {
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
		nextStart += actualDur
		if st.adsEnabled && nextBreak == chunkEnd && len(breaks) > 0 {
			log.Printf("Station %s: Inserting initial ad break at %.3fs for video %d", st.name, nextBreak, st.currentVideo)
			for i := 0; i < 3; i++ {
				if len(adIDs) == 0 {
					log.Printf("Station %s: No ads available for initial break at %.3fs", st.name, nextBreak)
					break
				}
				adID := adIDs[rand.Intn(len(adIDs))]
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
				log.Printf("Station %s: Queued initial ad %d with duration %.3fs at break %.3fs", st.name, adID, actualDur, nextBreak)
			}
		} else if nextStart < videoDur {
			segments, spsPPS, fmtpLine, actualDur, fps, err := processVideo(st, st.currentVideo, db, nextStart, ChunkDuration)
			if err != nil {
				log.Printf("Station %s: Failed to process second initial chunk for video %d: %v", st.name, st.currentVideo, err)
			} else {
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
				log.Printf("Station %s: Queued second initial chunk for video %d at %.3fs, duration %.3fs", st.name, st.currentVideo, nextStart, actualDur)
			}
		}
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
body {
    font-family: Arial, sans-serif;
    margin: 20px;
}
#log {
    white-space: pre-wrap;
    background-color: #fff;
    border: 1px solid #ccc;
    padding: 10px;
    max-height: 400px;
    overflow-y: auto;
}
</style>
</head>
<body>
<h1>WebRTC Video Receiver</h1>
<video id="remoteVideo" autoplay playsinline controls></video>
<button id="startButton">Start Connection</button>
<label for="serverUrl">Server URL (e.g., http://192.168.0.60:8081/signal):</label>
<input id="serverUrl" type="text" value="http://192.168.0.60:8081/signal">
<label for="station">Station (e.g., Bob's Burgers):</label>
<input id="station" type="text" value="Bob's Burgers" style="width: 100px;">
<label><input type="checkbox" id="adsEnabled" checked> Enable Ads</label>
<button id="sendOfferButton" disabled>Send Offer to Server</button>
<button id="restartIceButton" disabled>Restart ICE</button>
<pre id="log"></pre>
<script>
function log(message) {
    console.log(message);
    const logElement = document.getElementById('log');
    logElement.textContent += message + '\n';
    logElement.scrollTop = logElement.scrollHeight;
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
let lastVideoTime = 0;
let lastCheckTime = 0;
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
    window.staticAudio = noise;
    log('Starting static audio');
}
function stopStaticAudio() {
    if (window.staticAudio) {
        window.staticAudio.disconnect();
        window.staticAudio = null;
        log('Stopping static audio');
    }
}
startStaticAudio();
async function createOffer() {
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
            const audioContext = new AudioContext({ sampleRate: 48000 });
            log('AudioContext created with sampleRate=' + audioContext.sampleRate);
            const source = audioContext.createMediaStreamSource(remoteStream);
            analyser = audioContext.createAnalyser();
            source.connect(analyser);
            analyser.connect(audioContext.destination);
            log('Connected audio audio track to AudioContext, channels=' + source.channelCount);
            track.onmute = () => log('Track muted: kind=' + track.kind + ', id=' + track.id + ' - possible silence or no media flow');
            track.onunmute = () => {
                log('Track unmuted: kind=' + track.kind + ', id=' + track.id + ' - media flowing');
                const data = new Float32Array(analyser.fftSize);
                analyser.getFloatTimeDomainData(data);
                log('Audio data sample on unmute: ' + data.slice(0, 10).join(', '));
            };
            track.onended = () => log('Track ended: kind=' + track.kind + ', id=' + track.id);
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
            }).catch(err => log('Audio and video playback error: ' + err));
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
    pc.onconnectionstatechange = () => log('Peer connection state: ' + pc.connectionState);
    pc.onicecandidate = event => {
        if (event.candidate) {
            log('New ICE candidate: ' + JSON.stringify(event.candidate));
        } else {
            log('All ICE candidates gathered (end-of-candidates)');
        }
    };
    const offer = await pc.createOffer({ offerToReceiveVideo: true, offerToReceiveAudio: true });
    await pc.setLocalDescription(offer);
    log('Local Offer SDP: ' + offer.sdp);
    document.getElementById('sendOfferButton').disabled = false;
}
async function sendOfferToServer() {
    let baseUrl = document.getElementById('serverUrl').value;
    const station = document.getElementById('station').value;
    const adsEnabled = document.getElementById('adsEnabled').checked;
    const serverUrl = baseUrl + (baseUrl.includes('?') ? '&' : '?') + 'station=' + encodeURIComponent(station) + '&adsEnabled=' + adsEnabled;
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
    log('Restarting ICE to fix desync or freeze...');
    try {
        const newOffer = await pc.createOffer({ iceRestart: true });
        await pc.setLocalDescription(newOffer);
        log('New offer with ICE restart: ' + newOffer.sdp);
        await sendOfferToServer();
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
const videoElement = document.getElementById('remoteVideo');
function checkForDesync() {
    if (!videoElement.srcObject || videoElement.paused || videoElement.ended) return;
    const now = performance.now() / 1000;
    const videoTime = videoElement.currentTime;
    const elapsed = now - lastCheckTime;
    if (elapsed > 1) {
        if (Math.abs(videoTime - lastVideoTime) < 0.1 && !videoElement.paused) {
            log('Video freeze detected: currentTime=' + videoTime + ', lastTime=' + lastVideoTime);
            restartIce();
        } else if (analyser) {
            const data = new Float32Array(analyser.fftSize);
            analyser.getFloatTimeDomainData(data);
            const level = data.reduce((sum, val) => sum + Math.abs(val), 0) / data.length;
            if (level > 0.01 && Math.abs(videoTime - lastVideoTime - elapsed) > 0.5) {
                log('Audio-video desync detected: videoTime=' + videoTime + ', expected=' + (lastVideoTime + elapsed) + ', audioLevel=' + level.toFixed(4));
                restartIce();
            }
        }
        lastVideoTime = videoTime;
        lastCheckTime = now;
    }
}
videoElement.addEventListener('stalled', () => {
    log('Video stalled - triggering ICE restart');
    restartIce();
});
videoElement.addEventListener('waiting', () => {
    log('Video waiting for data - triggering ICE restart');
    restartIce();
});
setInterval(checkForDesync, 1000);
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
			// Update duration if needed
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
			// Update loudnorm measurements if needed
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
				// Extract JSON from output (it's at the end of stderr)
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

var adIDs []int64
var videoBaseDir string
var stations = make(map[string]*Station)
var noAdsStations = make(map[string]*Station) // Map for no-ads versions of stations
var mu sync.Mutex
var globalStart = time.Now()

type Station struct {
	name         string
	segmentList  []bufferedChunk
	spsPPS       [][]byte
	fmtpLine     string
	trackVideo   *webrtc.TrackLocalStaticSample
	trackAudio   *webrtc.TrackLocalStaticSample
	videoQueue   []int64
	currentVideo int64
	currentIndex int
	currentOffset float64
	viewers      int
	processing   bool
	stopCh       chan struct{}
	adsEnabled   bool // Indicates if ads are enabled for this station
	mu           sync.Mutex
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