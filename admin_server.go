package main

import (
    "database/sql"
    "errors"
    "log"
    "net/http"
    "path/filepath"
    "strings"

    "github.com/gin-gonic/gin"
    _ "github.com/lib/pq"
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

    // Set up public directory (like public_html) for static files with index handling
    publicDir := "./public" // Change to your public_html folder path
    r.StaticFS("/public_html", http.Dir(publicDir)) // Serve static files under /public_html

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

    // Serve dynamic HTML interface at root (or use static if preferred)
    r.GET("/", func(c *gin.Context) {
        c.HTML(http.StatusOK, "admin.html", gin.H{
            "Title": "Admin Panel", // Example dynamic data
        })
    })

    // Add the new browse route
    r.GET("/browse", browseHandler)

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

                // Check if it's a template not found error (updated matching)
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

// New browse handler for Gin
func browseHandler(c *gin.Context) {
    // Fetch all titles with nested title_metadata, videos, metadata, tags
    rows, err := db.Query(`
        SELECT t.id, t.name, t.description
        FROM titles t
        ORDER BY t.name
    `)
    if err != nil {
        log.Printf("Query error: %v", err)  // Add logging for debugging
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

    c.HTML(http.StatusOK, "browse.html", gin.H{"Titles": titles})
}