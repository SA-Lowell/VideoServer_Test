package main

import (
	"database/sql"
	"errors"
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
	videoBaseDir = "Z:/Videos"
	adBreakFadeToBlackDetectorPath  = "./ad_break_fade_to_black_detector.exe" // Relative to the program
	adBreakHardCutDetectorPath  = "./ad_break_hard_cut_detector.exe" // Relative to the program
)

type Title struct {
	ID            int64
	Name          string
	Description   string
	TitleMetadata []Metadata // For title-level metadata
	Videos        []Video
}

type Video struct {
	ID       int64
	URI      string
	Metadata []Metadata
	Tags     []Tag
}

type Metadata struct {
	TypeName string
	Value    string // JSONB as string
}

type Tag struct {
	Name string
}

type DirEntry struct {
	Type string `json:"type"` // "file" or "dir"
	Name string `json:"name"`
	Path string `json:"path"` // Relative path
}

var db *sql.DB

func main() {
	r := gin.Default()

	// Custom recovery middleware to handle runtime panics as 500 or 404
	r.Use(customRecovery())

	// Custom error handler middleware: Handles errors after handlers
	r.Use(customErrorHandler())

	// Safe template loading (no panic if no files)
	loadTemplatesSafely(r, "templates/*.html")

	// DB connection (replace with your creds)
	var err error
	db, err = sql.Open("postgres", "user=postgres password=aaaaaaaaaa dbname=webrtc_tv sslmode=disable host=localhost port=5432")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Log current working directory for debugging
	cwd, err := os.Getwd()
	if err != nil {
		log.Printf("Failed to get current working directory: %v", err)
	} else {
		log.Printf("Current working directory: %s", cwd)
	}

	// Set up public directory (like public_html) for static files with index handling
	publicDir := "./public" // Change to your public_html folder path
	r.StaticFS("/public_html", http.Dir(publicDir)) // Serve static files under /public_html

	// Serve videos from videoBaseDir
	r.StaticFS("/videos", http.Dir(videoBaseDir))

	// Example dynamic endpoint: Add tag to file
	r.POST("/files/:id/tags", func(c *gin.Context) {
		id := c.Param("id")
		var tags []string

		if err := c.BindJSON(&tags); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Update DB (use prepared stmt in production)
		_, err := db.Exec("UPDATE files SET tags = tags || $1 WHERE id = $2", tags, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	// Serve dynamic HTML interface at root
	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "admin.html", gin.H{
			"Title": "Admin Panel",
		})
	})

	// Add the new update ad break points route
	r.GET("/update-ad-breaks", updateAdBreaksHandler)

	// List contents in video directory (files and dirs)
	r.GET("/list-contents", listContentsHandler)

	// Add video to database
	r.POST("/add-video", addVideoHandler)

	// Detect breaks
	r.POST("/detect-breaks", detectBreaksHandler)

	// Add break to database
	r.POST("/add-break", addBreakHandler)

	r.NoRoute(func(c *gin.Context) {
		c.HTML(http.StatusNotFound, "404.html", gin.H{"error": "404"})
	})

	r.Run(":8082")
}

// Custom recovery middleware: Handles panics, checks for template errors as 404
func customRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				// Convert panic to error
				var recoveredErr error
				switch x := err.(type) {
				case string:
					recoveredErr = errors.New(x)
				case error:
					recoveredErr = x
				default:
					recoveredErr = errors.New("unknown panic")
				}

				// Check if it's a template not found error
				errStr := recoveredErr.Error()
				if strings.Contains(errStr, "no template") || strings.Contains(errStr, "pattern matches no files") || strings.Contains(errStr, "template:") || strings.Contains(errStr, "undefined") {
					c.HTML(http.StatusNotFound, "404.html", gin.H{"error": "Resource not found"})
				} else {
					// Default to 500 for other panics
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
				}
				c.Abort()
			}
		}()
		c.Next()
	}
}

// Custom error handler middleware: Handles errors after handlers
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

// Safe template loading: No panic if no files match
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

// Handler to list immediate contents (files and dirs) of a path
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
			if ext == ".mp4" || ext == ".mkv" {
				contents = append(contents, DirEntry{Type: "file", Name: entry.Name(), Path: entryPath})
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"contents": contents})
}

// Handler to add a video to the database
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
		req.TitleID = 0 // Default to N/A title
	}
	var id int64
	err := db.QueryRow("INSERT INTO videos (title_id, uri) VALUES ($1, $2) RETURNING id", req.TitleID, req.URI).Scan(&id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id})
}

// Handler to detect ad breaks
type DetectReq struct {
	URI string `json:"uri,omitempty"`
	ID  int64  `json:"id,omitempty"`
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

	// Resolve absolute path for ad_break_detector.exe
	absAdBreakFadeToBlackDetectorPath, err := filepath.Abs(adBreakFadeToBlackDetectorPath)
	if err != nil {
		log.Printf("Failed to resolve absolute path for %s: %v", adBreakFadeToBlackDetectorPath, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Cannot resolve ad break detector path"})
		return
	}
	log.Printf("Resolved ad_break_detector.exe path: %s", absAdBreakFadeToBlackDetectorPath)
	if _, err := os.Stat(absAdBreakFadeToBlackDetectorPath); err != nil {
		log.Printf("ad_break_detector.exe not found at %s: %v", absAdBreakFadeToBlackDetectorPath, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ad break detector executable not found at " + absAdBreakFadeToBlackDetectorPath})
		return
	}

	log.Printf("Running ad_break_detector.exe on %s with --no-format", fullPath)
	cmd := exec.Command(absAdBreakFadeToBlackDetectorPath, fullPath, "--no-format")
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("ad_break_detector.exe failed: %v, output: %s", err, string(output))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ad break detection failed: " + string(output)})
		return
	}

	outStr := strings.TrimSpace(string(output))
	log.Printf("ad_break_detector.exe output: %s", outStr)
	if outStr == "No suitable ad insertion points detected." {
		c.JSON(http.StatusOK, gin.H{"breaks": []interface{}{}, "video_id": videoID})
		return
	}

	fields := strings.Fields(outStr)
	if len(fields)%3 != 0 {
		log.Printf("Invalid break data format: %v", fields)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid ad break data format"})
		return
	}

	var breaks []map[string]float64
	for i := 0; i < len(fields); i += 3 {
		start, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			log.Printf("Failed to parse start time %s: %v", fields[i], err)
			continue
		}
		mid, err := strconv.ParseFloat(fields[i+1], 64)
		if err != nil {
			log.Printf("Failed to parse mid time %s: %v", fields[i+1], err)
			continue
		}
		end, err := strconv.ParseFloat(fields[i+2], 64)
		if err != nil {
			log.Printf("Failed to parse end time %s: %v", fields[i+2], err)
			continue
		}
		breaks = append(breaks, map[string]float64{"start": start, "mid": mid, "end": end})
	}
	log.Printf("Returning %d breaks for video ID %d", len(breaks), videoID)
	c.JSON(http.StatusOK, gin.H{"breaks": breaks, "video_id": videoID})
}

// Handler to add a break
type AddBreakReq struct {
	ID   int64   `json:"id"`
	Time float64 `json:"time"`
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

// Handler for updating ad break points
func updateAdBreaksHandler(c *gin.Context) {
	// Fetch all titles with nested title_metadata, videos, metadata, tags
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

		// Fetch title_metadata for title
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

		// Fetch videos for title
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

			// Fetch metadata for video
			mrows, err := db.Query(`
                SELECT mt.name, vm.value::text
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
				if err := mrows.Scan(&m.TypeName, &m.Value); err != nil {
					log.Printf("Video metadata scan error: %v", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				v.Metadata = append(v.Metadata, m)
			}

			// Fetch tags for video
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

		// Check if any data was fetched (for debugging)
		if len(t.Videos) == 0 && len(t.TitleMetadata) == 0 {
			log.Printf("No data for title ID %d: %s", t.ID, t.Name)
		}

		titles = append(titles, t)
	}

	c.HTML(http.StatusOK, "update_ad_break_points.html", gin.H{"Titles": titles})
}