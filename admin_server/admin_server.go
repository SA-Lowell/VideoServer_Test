package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
)

const (
	videoBaseDir                    = "Z:/Videos"
	adBreakFadeToBlackDetectorPath  = "./ad_break_fade_to_black_detector.exe"
	adBreakHardCutDetectorPath      = "./ad_break_hard_cut_detector.exe"
	remuxVideoPath                 = "./remux_video.exe"
)

type Title struct {
	ID            int64
	Name          string
	Description   string
	TitleMetadata []Metadata
	Videos        []Video
}

type Video struct {
	ID       int64
	URI      string
	Metadata []Metadata
	Tags     []Tag
}

type Metadata struct {
	ID       int64
	TypeName string
	Value    string
}

type Tag struct {
	Name string
}

type DirEntry struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type AddBreakReq struct {
	ID   int64   `json:"id"`
	Time float64 `json:"time"`
}

type RemuxVideoReq struct {
	ID int64 `json:"id"`
}

type Station struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	UnixStart int64  `json:"unix_start"`
}

type UpdateBreakReq struct {
	ID   int64   `json:"id"`
	Time float64 `json:"time"`
}

type DeleteBreakReq struct {
	ID int64 `json:"id"`
}

var db *sql.DB

func main() {
	r := gin.Default()

	r.Use(customRecovery())
	r.Use(customErrorHandler())
	loadTemplatesSafely(r, "templates/*.html")

	var err error
	db, err = sql.Open("postgres", "user=postgres password=aaaaaaaaaa dbname=webrtc_tv sslmode=disable host=localhost port=5432")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	cwd, err := os.Getwd()
	if err != nil {
		log.Printf("Failed to get current working directory: %v", err)
	} else {
		log.Printf("Current working directory: %s", cwd)
	}

	publicDir := "./public"
	r.StaticFS("/public_html", http.Dir(publicDir))
	r.StaticFS("/videos", http.Dir(videoBaseDir))
	r.StaticFS("/temp_videos", http.Dir("./temp_videos"))

	r.POST("/files/:id/tags", func(c *gin.Context) {
		id := c.Param("id")
		var tags []string
		if err := c.BindJSON(&tags); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		_, err := db.Exec("UPDATE files SET tags = tags || $1 WHERE id = $2", tags, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "admin.html", gin.H{
			"Title": "Admin Panel",
		})
	})

	r.GET("/update-ad-breaks", updateAdBreaksHandler)
	r.GET("/get-video-metadata/:id", getVideoMetadataHandler)
	r.GET("/list-contents", listContentsHandler)
	r.POST("/add-video", addVideoHandler)
	r.POST("/detect-breaks", detectBreaksHandler)
	r.POST("/add-break", addBreakHandler)
	r.POST("/add-breaks", addBreaksHandler)
	r.POST("/remux-video", remuxVideoHandler)
	r.POST("/fix-audio", fixAudioHandler)
	r.POST("/update-break", updateBreakHandler)
	r.POST("/delete-break", deleteBreakHandler)

	r.GET("/manage-titles", manageTitlesHandler)
	r.GET("/manage-channels", manageChannelsHandler)
	r.GET("/manage-title-videos", manageTitleVideosHandler)
	r.GET("/manage-channel-videos", manageChannelVideosHandler)

	r.GET("/api/titles", apiTitlesHandler)
	r.POST("/api/titles", apiCreateTitleHandler)
	r.PUT("/api/titles/:id", apiUpdateTitleHandler)
	r.DELETE("/api/titles/:id", apiDeleteTitleHandler)

	r.GET("/api/stations", apiStationsHandler)
	r.POST("/api/stations", apiCreateStationHandler)
	r.PUT("/api/stations/:id", apiUpdateStationHandler)
	r.DELETE("/api/stations/:id", apiDeleteStationHandler)

	r.GET("/api/videos", apiVideosHandler)
	r.POST("/api/assign-video-title/:vid/:tid", apiAssignVideoToTitleHandler)
	r.DELETE("/api/assign-video-title/:vid", apiRemoveVideoFromTitleHandler)
	r.POST("/api/assign-video-station", apiAssignVideoToStationHandler)
	r.DELETE("/api/assign-video-station/:sid/:vid", apiRemoveVideoFromStationHandler)

	r.NoRoute(func(c *gin.Context) {
		c.HTML(http.StatusNotFound, "404.html", gin.H{"error": "404"})
	})

	r.Run(":8082")
}

func customRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				var recoveredErr error
				switch x := err.(type) {
				case string:
					recoveredErr = errors.New(x)
				case error:
					recoveredErr = x
				default:
					recoveredErr = errors.New("unknown panic")
				}
				errStr := recoveredErr.Error()
				if strings.Contains(errStr, "no template") || strings.Contains(errStr, "pattern matches no files") || strings.Contains(errStr, "template:") || strings.Contains(errStr, "undefined") {
					c.HTML(http.StatusNotFound, "404.html", gin.H{"error": "Resource not found"})
				} else {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
				}
				c.Abort()
			}
		}()
		c.Next()
	}
}

func customErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 {
			errStr := c.Errors.Last().Error()
			if strings.Contains(errStr, "undefined") {
				c.HTML(http.StatusNotFound, "404.html", gin.H{"error": "Resource not found"})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
			}
			c.Abort()
		}
	}
}

func loadTemplatesSafely(r *gin.Engine, pattern string) {
	files, err := filepath.Glob(pattern)
	if err != nil {
		log.Printf("Warning: Failed to glob templates: %v", err)
		return
	}
	if len(files) == 0 {
		log.Printf("Warning: No templates found matching '%s'", pattern)
		return
	}
	r.LoadHTMLFiles(files...)
}

func getVideoMetadataHandler(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid video ID"})
		return
	}

	vrows, err := db.Query(`
		SELECT v.id, v.uri
		FROM videos v
		WHERE v.id = $1
	`, id)
	if err != nil {
		log.Printf("Video query error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer vrows.Close()

	var video Video
	if vrows.Next() {
		if err := vrows.Scan(&video.ID, &video.URI); err != nil {
			log.Printf("Video scan error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	} else {
		c.JSON(http.StatusNotFound, gin.H{"error": "Video not found"})
		return
	}

	mrows, err := db.Query(`
		SELECT vm.id, mt.name, vm.value::text
		FROM video_metadata vm
		JOIN metadata_types mt ON vm.metadata_type_id = mt.id
		WHERE vm.video_id = $1
	`, video.ID)
	if err != nil {
		log.Printf("Video metadata query error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer mrows.Close()

	for mrows.Next() {
		var m Metadata
		if err := mrows.Scan(&m.ID, &m.TypeName, &m.Value); err != nil {
			log.Printf("Video metadata scan error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		video.Metadata = append(video.Metadata, m)
	}

	trows, err := db.Query(`
		SELECT tg.name
		FROM video_tags vt
		JOIN tags tg ON vt.tag_id = tg.id
		WHERE vt.video_id = $1
	`, video.ID)
	if err != nil {
		log.Printf("Tags query error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer trows.Close()

	for trows.Next() {
		var tag Tag
		if err := trows.Scan(&tag.Name); err != nil {
			log.Printf("Tags scan error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		video.Tags = append(video.Tags, tag)
	}

	c.JSON(http.StatusOK, video)
}

func listContentsHandler(c *gin.Context) {
	relPath := c.Query("path")
	fullPath := filepath.Join(videoBaseDir, relPath)
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var contents []DirEntry
	for _, entry := range entries {
		entryPath := filepath.Join(relPath, entry.Name())
		entryPath = strings.ReplaceAll(entryPath, "\\", "/")
		if entry.IsDir() {
			contents = append(contents, DirEntry{Type: "dir", Name: entry.Name(), Path: entryPath})
		} else {
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			supportedExtensions := []string{".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm", ".mpeg", ".mpg", ".m4v", ".3gp", ".3g2", ".ogv", ".rm", ".rmvb", ".vob", ".ts", ".m2ts", ".mts", ".divx", ".asf"}

			for _, supportedExt := range supportedExtensions {
				if ext == supportedExt {
					contents = append(contents, DirEntry{Type: "file", Name: entry.Name(), Path: entryPath})
					break
				}
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"contents": contents})
}

type AddVideoReq struct {
	URI     string `json:"uri"`
	TitleID int64  `json:"title_id"`
}

func addVideoHandler(c *gin.Context) {
	var req AddVideoReq
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.TitleID == 0 {
		req.TitleID = 0
	}

	var id int64
	err := db.QueryRow("SELECT id FROM videos WHERE uri = $1", req.URI).Scan(&id)
	if err == nil {
		c.JSON(http.StatusOK, gin.H{"id": id, "existed": true})
		return
	} else if err != sql.ErrNoRows {
		log.Printf("Check video query error for URI %s: %v", req.URI, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error: " + err.Error()})
		return
	}

	err = db.QueryRow("INSERT INTO videos (title_id, uri) VALUES ($1, $2) RETURNING id", req.TitleID, req.URI).Scan(&id)
	if err != nil {
		log.Printf("Failed to add video to DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add video to DB: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "existed": false})
}

type DetectReq struct {
	URI      string `json:"uri,omitempty"`
	ID       int64  `json:"id,omitempty"`
	Detector string `json:"detector"`
}

func detectBreaksHandler(c *gin.Context) {
	var req DetectReq
	if err := c.BindJSON(&req); err != nil {
		log.Printf("BindJSON error: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}
	log.Printf("DetectBreaks request: %+v", req)

	if req.ID == 0 && req.URI == "" {
		log.Printf("Invalid request: both id and uri are empty")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Must provide either id or uri"})
		return
	}

	var fullPath string
	var videoID int64
	if req.ID != 0 {
		var uri string
		err := db.QueryRow("SELECT uri FROM videos WHERE id = $1", req.ID).Scan(&uri)
		if err == sql.ErrNoRows {
			log.Printf("No video found for ID %d", req.ID)
			c.JSON(http.StatusNotFound, gin.H{"error": "Video not found"})
			return
		} else if err != nil {
			log.Printf("Video query error for ID %d: %v", req.ID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error: " + err.Error()})
			return
		}
		fullPath = filepath.Join(videoBaseDir, uri)
		videoID = req.ID
	} else {
		fullPath = filepath.Join(videoBaseDir, req.URI)
		err := db.QueryRow("SELECT id FROM videos WHERE uri = $1", req.URI).Scan(&videoID)
		if err == sql.ErrNoRows {
			log.Printf("Adding new video to DB: %s", req.URI)
			err = db.QueryRow("INSERT INTO videos (title_id, uri) VALUES (0, $1) RETURNING id", req.URI).Scan(&videoID)
			if err != nil {
				log.Printf("Failed to add video to DB: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add video to DB: " + err.Error()})
				return
			}
		} else if err != nil {
			log.Printf("Video URI query error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error: " + err.Error()})
			return
		}
	}

	fullPath = filepath.Clean(fullPath)
	log.Printf("Checking file: %s", fullPath)
	if _, err := os.Stat(fullPath); err != nil {
		log.Printf("File not found: %s, error: %v", fullPath, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "File not found: " + fullPath})
		return
	}

	var detectorPath string
	if req.Detector == "hard-cut" {
		detectorPath = adBreakHardCutDetectorPath
	} else {
		detectorPath = adBreakFadeToBlackDetectorPath
	}

	absDetectorPath, err := filepath.Abs(detectorPath)
	if err != nil {
		log.Printf("Failed to resolve absolute path for %s: %v", detectorPath, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Cannot resolve ad break detector path"})
		return
	}
	log.Printf("Resolved detector path: %s", absDetectorPath)
	if _, err := os.Stat(absDetectorPath); err != nil {
		log.Printf("Detector executable not found at %s: %v", absDetectorPath, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ad break detector executable not found at " + absDetectorPath})
		return
	}

	log.Printf("Running detector on %s with --no-format", fullPath)
	cmd := exec.Command(absDetectorPath, fullPath, "--no-format")
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Detector failed: %v, output: %s", err, string(output))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ad break detection failed: " + string(output)})
		return
	}

	outStr := strings.TrimSpace(string(output))
	log.Printf("Detector raw output: %s", outStr)
	if outStr == "No suitable ad insertion points detected." {
		c.JSON(http.StatusOK, gin.H{"breaks": []interface{}{}, "video_id": videoID})
		return
	}

	fields := strings.Fields(outStr)
	log.Printf("Number of fields: %d", len(fields))
	if len(fields) < 3 {
		log.Printf("Insufficient fields in output: got %d, expected at least 3", len(fields))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Insufficient data from ad break detector"})
		return
	}
	if len(fields)%3 != 0 {
		log.Printf("Invalid break data format: expected triplets, got %d fields (processing complete triplets only)", len(fields))
	}

	var breaks []map[string]float64
	for i := 0; i < len(fields)-2; i += 3 {
		start, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			log.Printf("Failed to parse start time %s at index %d: %v", fields[i], i, err)
			continue
		}
		mid, err := strconv.ParseFloat(fields[i+1], 64)
		if err != nil {
			log.Printf("Failed to parse mid time %s at index %d: %v", fields[i+1], i+1, err)
			continue
		}
		end, err := strconv.ParseFloat(fields[i+2], 64)
		if err != nil {
			log.Printf("Failed to parse end time %s at index %d: %v", fields[i+2], i+2, err)
			continue
		}
		breaks = append(breaks, map[string]float64{"start": start, "mid": mid, "end": end})
	}

	if len(breaks) == 0 {
		log.Printf("No valid breakpoints parsed from %d fields", len(fields))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "No valid breakpoints detected"})
		return
	}

	log.Printf("Returning %d valid breaks for video ID %d", len(breaks), videoID)
	c.JSON(http.StatusOK, gin.H{"breaks": breaks, "video_id": videoID})
}

func addBreakHandler(c *gin.Context) {
	var req AddBreakReq
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_, err := db.Exec("INSERT INTO video_metadata (video_id, metadata_type_id, value) VALUES ($1, 1, to_jsonb($2::numeric))", req.ID, req.Time)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func addBreaksHandler(c *gin.Context) {
	var req []AddBreakReq
	if err := c.BindJSON(&req); err != nil {
		log.Printf("BindJSON error for add-breaks: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}

	if len(req) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No breakpoints provided"})
		return
	}

	tx, err := db.Begin()
	if err != nil {
		log.Printf("Failed to start transaction: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start transaction: " + err.Error()})
		return
	}

	for _, breakPoint := range req {
		if breakPoint.ID == 0 {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid video ID in breakpoint"})
			return
		}
		_, err := tx.Exec("INSERT INTO video_metadata (video_id, metadata_type_id, value) VALUES ($1, 1, to_jsonb($2::numeric))", breakPoint.ID, breakPoint.Time)
		if err != nil {
			tx.Rollback()
			log.Printf("Failed to insert breakpoint for video ID %d: %v", breakPoint.ID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to insert breakpoint: " + err.Error()})
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("Failed to commit transaction: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to commit transaction: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

func remuxVideoHandler(c *gin.Context) {
	var req RemuxVideoReq
	if err := c.BindJSON(&req); err != nil {
		log.Printf("BindJSON error for remux-video: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}

	if req.ID == 0 {
		log.Printf("Invalid request: video ID is empty")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Must provide video ID"})
		return
	}

	var uri string
	err := db.QueryRow("SELECT uri FROM videos WHERE id = $1", req.ID).Scan(&uri)
	if err == sql.ErrNoRows {
		log.Printf("No video found for ID %d", req.ID)
		c.JSON(http.StatusNotFound, gin.H{"error": "Video not found"})
		return
	} else if err != nil {
		log.Printf("Video query error for ID %d: %v", req.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error: " + err.Error()})
		return
	}

	fullPath := filepath.Join(videoBaseDir, uri)
	fullPath = filepath.Clean(fullPath)
	log.Printf("Checking file: %s", fullPath)
	if _, err := os.Stat(fullPath); err != nil {
		log.Printf("File not found: %s, error: %v", fullPath, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "File not found: " + fullPath})
		return
	}

	absRemuxPath, err := filepath.Abs(remuxVideoPath)
	if err != nil {
		log.Printf("Failed to resolve absolute path for %s: %v", remuxVideoPath, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Cannot resolve remux video executable path"})
		return
	}
	log.Printf("Resolved remux executable path: %s", absRemuxPath)
	if _, err := os.Stat(absRemuxPath); err != nil {
		log.Printf("Remux executable not found at %s: %v", absRemuxPath, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Remux executable not found at " + absRemuxPath})
		return
	}

	log.Printf("Running remux on %s", fullPath)
	cmd := exec.Command(absRemuxPath, fullPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Remux failed: %v, output: %s", err, string(output))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Video remux failed: " + string(output)})
		return
	}

	log.Printf("Remux output: %s", string(output))
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func fixAudioHandler(c *gin.Context) {
	var req RemuxVideoReq
	if err := c.BindJSON(&req); err != nil {
		log.Printf("BindJSON error for fix-audio: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}

	if req.ID == 0 {
		log.Printf("Invalid request: video ID is empty")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Must provide video ID"})
		return
	}

	var uri string
	err := db.QueryRow("SELECT uri FROM videos WHERE id = $1", req.ID).Scan(&uri)
	if err == sql.ErrNoRows {
		log.Printf("No video found for ID %d", req.ID)
		c.JSON(http.StatusNotFound, gin.H{"error": "Video not found"})
		return
	} else if err != nil {
		log.Printf("Video query error for ID %d: %v", req.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error: " + err.Error()})
		return
	}

	fullPath := filepath.Join(videoBaseDir, uri)
	fullPath = filepath.Clean(fullPath)
	log.Printf("Checking file: %s", fullPath)
	if _, err := os.Stat(fullPath); err != nil {
		log.Printf("File not found: %s, error: %v", fullPath, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "File not found: " + fullPath})
		return
	}

	tempDir := "./temp_videos"
	if err := os.MkdirAll(tempDir, os.ModePerm); err != nil {
		log.Printf("Failed to create temp directory %s: %v", tempDir, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create temp directory: " + err.Error()})
		return
	}

	tempFileName := fmt.Sprintf("%d_fixed_audio.mp4", req.ID)
	tempPath := filepath.Join(tempDir, tempFileName)

	if _, err := os.Stat(tempPath); err == nil {
		log.Printf("Temp file already exists: %s", tempPath)
		tempURI := "/temp_videos/" + tempFileName
		c.JSON(http.StatusOK, gin.H{"temp_uri": tempURI})
		return
	}

	log.Printf("Running FFmpeg to fix audio on %s", fullPath)
	cmd := exec.Command("ffmpeg", "-i", fullPath, "-c:v", "copy", "-c:a", "aac", tempPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("FFmpeg failed: %v, output: %s", err, string(output))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fix audio: " + string(output)})
		return
	}

	log.Printf("FFmpeg output: %s", string(output))
	tempURI := "/temp_videos/" + tempFileName
	c.JSON(http.StatusOK, gin.H{"temp_uri": tempURI})
}

func updateBreakHandler(c *gin.Context) {
	var req UpdateBreakReq
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_, err := db.Exec("UPDATE video_metadata SET value = to_jsonb($1::numeric) WHERE id = $2 AND metadata_type_id = 1", req.Time, req.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func deleteBreakHandler(c *gin.Context) {
	var req DeleteBreakReq
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_, err := db.Exec("DELETE FROM video_metadata WHERE id = $1 AND metadata_type_id = 1", req.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func updateAdBreaksHandler(c *gin.Context) {
	rows, err := db.Query(`
		SELECT t.id, t.name, t.description
		FROM titles t
		ORDER BY t.name
	`)
	if err != nil {
		log.Printf("Query error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var titles []Title
	for rows.Next() {
		var t Title
		if err := rows.Scan(&t.ID, &t.Name, &t.Description); err != nil {
			log.Printf("Scan error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		tmrows, err := db.Query(`
			SELECT mt.name, tm.value::text
			FROM title_metadata tm
			JOIN metadata_types mt ON tm.metadata_type_id = mt.id
			WHERE tm.title_id = $1
		`, t.ID)
		if err != nil {
			log.Printf("Title metadata query error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer tmrows.Close()

		for tmrows.Next() {
			var m Metadata
			if err := tmrows.Scan(&m.TypeName, &m.Value); err != nil {
				log.Printf("Title metadata scan error: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			t.TitleMetadata = append(t.TitleMetadata, m)
		}

		vrows, err := db.Query(`
			SELECT v.id, v.uri
			FROM videos v
			WHERE v.title_id = $1
			ORDER BY v.id
		`, t.ID)
		if err != nil {
			log.Printf("Videos query error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer vrows.Close()

		for vrows.Next() {
			var v Video
			if err := vrows.Scan(&v.ID, &v.URI); err != nil {
				log.Printf("Videos scan error: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			mrows, err := db.Query(`
				SELECT vm.id, mt.name, vm.value::text
				FROM video_metadata vm
				JOIN metadata_types mt ON vm.metadata_type_id = mt.id
				WHERE vm.video_id = $1
			`, v.ID)
			if err != nil {
				log.Printf("Video metadata query error: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			defer mrows.Close()

			for mrows.Next() {
				var m Metadata
				if err := mrows.Scan(&m.ID, &m.TypeName, &m.Value); err != nil {
					log.Printf("Video metadata scan error: %v", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				v.Metadata = append(v.Metadata, m)
			}

			trows, err := db.Query(`
				SELECT tg.name
				FROM video_tags vt
				JOIN tags tg ON vt.tag_id = tg.id
				WHERE vt.video_id = $1
			`, v.ID)
			if err != nil {
				log.Printf("Tags query error: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			defer trows.Close()

			for trows.Next() {
				var tag Tag
				if err := trows.Scan(&tag.Name); err != nil {
					log.Printf("Tags scan error: %v", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				v.Tags = append(v.Tags, tag)
			}

			t.Videos = append(t.Videos, v)
		}

		if len(t.Videos) == 0 && len(t.TitleMetadata) == 0 {
			log.Printf("No data for title ID %d: %s", t.ID, t.Name)
		}

		titles = append(titles, t)
	}

	c.HTML(http.StatusOK, "update_ad_break_points.html", gin.H{"Titles": titles})
}

func manageTitlesHandler(c *gin.Context) {
	c.HTML(http.StatusOK, "manage_titles.html", gin.H{})
}

func manageChannelsHandler(c *gin.Context) {
	c.HTML(http.StatusOK, "manage_channels.html", gin.H{})
}

func manageTitleVideosHandler(c *gin.Context) {
	c.HTML(http.StatusOK, "manage_title_videos.html", gin.H{})
}

func manageChannelVideosHandler(c *gin.Context) {
	c.HTML(http.StatusOK, "manage_channel_videos.html", gin.H{})
}

func apiTitlesHandler(c *gin.Context) {
	search := strings.TrimSpace(c.Query("search"))
	limitStr := c.Query("limit")
	offsetStr := c.Query("offset")

	limit := 10
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}
	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil {
			offset = o
		}
	}

	query := `SELECT id, name, description FROM titles`
	args := []interface{}{}
	if search != "" {
		query += ` WHERE name ILIKE $1`
		args = append(args, "%"+search+"%")
	}
	query += ` ORDER BY name LIMIT $` + strconv.Itoa(len(args)+1) + ` OFFSET $` + strconv.Itoa(len(args)+2)
	args = append(args, limit, offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var titles []Title
	for rows.Next() {
		var t Title
		if err := rows.Scan(&t.ID, &t.Name, &t.Description); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		titles = append(titles, t)
	}

	c.JSON(http.StatusOK, titles)
}

func apiCreateTitleHandler(c *gin.Context) {
	var t Title
	if err := c.BindJSON(&t); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	err := db.QueryRow(`INSERT INTO titles (name, description) VALUES ($1, $2) RETURNING id`, t.Name, t.Description).Scan(&t.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, t)
}

func apiUpdateTitleHandler(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID"})
		return
	}
	var t Title
	if err := c.BindJSON(&t); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_, err = db.Exec(`UPDATE titles SET name = $1, description = $2 WHERE id = $3`, t.Name, t.Description, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	t.ID = id
	c.JSON(http.StatusOK, t)
}

func apiDeleteTitleHandler(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID"})
		return
	}
	_, err = db.Exec(`DELETE FROM titles WHERE id = $1`, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func apiStationsHandler(c *gin.Context) {
	search := strings.TrimSpace(c.Query("search"))
	limitStr := c.Query("limit")
	offsetStr := c.Query("offset")

	limit := 10
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}
	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil {
			offset = o
		}
	}

	query := `SELECT id, name, unix_start FROM stations`
	args := []interface{}{}
	if search != "" {
		query += ` WHERE name ILIKE $1`
		args = append(args, "%"+search+"%")
	}
	query += ` ORDER BY name LIMIT $` + strconv.Itoa(len(args)+1) + ` OFFSET $` + strconv.Itoa(len(args)+2)
	args = append(args, limit, offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	var stations []Station
	for rows.Next() {
		var s Station
		if err := rows.Scan(&s.ID, &s.Name, &s.UnixStart); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		stations = append(stations, s)
	}

	c.JSON(http.StatusOK, stations)
}

func apiCreateStationHandler(c *gin.Context) {
	var s Station
	if err := c.BindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	err := db.QueryRow(`INSERT INTO stations (name, unix_start) VALUES ($1, $2) RETURNING id`, s.Name, s.UnixStart).Scan(&s.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, s)
}

func apiUpdateStationHandler(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID"})
		return
	}
	var s Station
	if err := c.BindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_, err = db.Exec(`UPDATE stations SET name = $1, unix_start = $2 WHERE id = $3`, s.Name, s.UnixStart, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	s.ID = id
	c.JSON(http.StatusOK, s)
}

func apiDeleteStationHandler(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID"})
		return
	}
	_, err = db.Exec(`DELETE FROM stations WHERE id = $1`, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func apiVideosHandler(c *gin.Context) {
    search := strings.TrimSpace(c.Query("search"))
    titleIDStr := c.Query("title_id")
    stationIDStr := c.Query("station_id")
	notInStationIDStr := c.Query("not_in_station_id")
    limitStr := c.Query("limit")
    offsetStr := c.Query("offset")
	orderBy := c.Query("order_by")
    limit := 10
    if limitStr != "" {
        if l, err := strconv.Atoi(limitStr); err == nil {
            limit = l
        }
    }
    offset := 0
    if offsetStr != "" {
        if o, err := strconv.Atoi(offsetStr); err == nil {
            offset = o
        }
    }
	var orderField string
	if orderBy == "uri" {
		orderField = "v.uri"
	} else {
		orderField = "v.id"
	}
    query := `SELECT v.id, v.uri FROM videos v`
    args := []interface{}{}
    whereClauses := []string{}
    if titleIDStr != "" {
        titleID, err := strconv.ParseInt(titleIDStr, 10, 64)
        if err == nil {
            whereClauses = append(whereClauses, `v.title_id = $`+strconv.Itoa(len(args)+1))
            args = append(args, titleID)
        }
    }
	if stationIDStr != "" {
		stationID, err := strconv.ParseInt(stationIDStr, 10, 64)
		if err == nil {
			query += ` JOIN station_videos sv ON v.id = sv.video_id`
			whereClauses = append(whereClauses, `sv.station_id = $`+strconv.Itoa(len(args)+1))
			args = append(args, stationID)
		}
	}
	if notInStationIDStr != "" {
		notInStationID, err := strconv.ParseInt(notInStationIDStr, 10, 64)
		if err == nil {
			query += ` LEFT JOIN station_videos svx ON v.id = svx.video_id AND svx.station_id = $`+strconv.Itoa(len(args)+1)
			whereClauses = append(whereClauses, `svx.station_id IS NULL`)
			args = append(args, notInStationID)
		}
	}
    if search != "" {
        whereClauses = append(whereClauses, `v.uri ILIKE $`+strconv.Itoa(len(args)+1))
        args = append(args, "%"+search+"%")
    }
    if len(whereClauses) > 0 {
        query += ` WHERE ` + strings.Join(whereClauses, " AND ")
    }
    query += ` ORDER BY ` + orderField + ` LIMIT $` + strconv.Itoa(len(args)+1) + ` OFFSET $` + strconv.Itoa(len(args)+2)
    args = append(args, limit, offset)
    log.Printf("Executing query: %s with args: %v", query, args)
    rows, err := db.Query(query, args...)
    if err != nil {
        log.Printf("Query execution error: %v", err)
        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
        return
    }
    defer rows.Close()
    var videos []Video
    for rows.Next() {
        var v Video
        if err := rows.Scan(&v.ID, &v.URI); err != nil {
            log.Printf("Scan error: %v", err)
            c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
            return
        }
        log.Printf("Fetched video: ID=%d, URI=%s", v.ID, v.URI)
        videos = append(videos, v)
    }
    log.Printf("Returning %d videos", len(videos))
    c.JSON(http.StatusOK, videos)
}

func apiAssignVideoToTitleHandler(c *gin.Context) {
	vidStr := c.Param("vid")
	tidStr := c.Param("tid")
	vid, err := strconv.ParseInt(vidStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid video ID"})
		return
	}
	tid, err := strconv.ParseInt(tidStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid title ID"})
		return
	}
	_, err = db.Exec(`UPDATE videos SET title_id = $1 WHERE id = $2`, tid, vid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func apiRemoveVideoFromTitleHandler(c *gin.Context) {
	vidStr := c.Param("vid")
	vid, err := strconv.ParseInt(vidStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid video ID"})
		return
	}
	_, err = db.Exec(`UPDATE videos SET title_id = 0 WHERE id = $1`, vid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func apiAssignVideoToStationHandler(c *gin.Context) {
	type AssignReq struct {
		StationID int64 `json:"station_id"`
		VideoID   int64 `json:"video_id"`
	}
	var req AssignReq
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_, err := db.Exec(`INSERT INTO station_videos (station_id, video_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, req.StationID, req.VideoID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func apiRemoveVideoFromStationHandler(c *gin.Context) {
	sidStr := c.Param("sid")
	vidStr := c.Param("vid")
	sid, err := strconv.ParseInt(sidStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid station ID"})
		return
	}
	vid, err := strconv.ParseInt(vidStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid video ID"})
		return
	}
	_, err = db.Exec(`DELETE FROM station_videos WHERE station_id = $1 AND video_id = $2`, sid, vid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}