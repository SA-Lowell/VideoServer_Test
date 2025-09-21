package main

import (
    "log"
    "net/http"
    "os"
    "path/filepath"
    "strings"  // Add this import

    "github.com/gin-gonic/gin"

    "tv-station/video"
)

func streamHandler(c *gin.Context) {
    c.Header("Content-Type", "application/vnd.apple.mpegurl")
    http.ServeFile(c.Writer, c.Request, filepath.Join(video.HlsDir, "playlist.m3u8"))
}

// NEW: Custom /hls handler
func hlsHandler(c *gin.Context) {
    path := strings.TrimPrefix(c.Request.URL.Path, "/hls/")  // e.g., "playlist.m3u8" or "playlist0.ts"
    if path == "" {
        path = "playlist.m3u8"  // Default to playlist
    }
    fullPath := filepath.Join(video.HlsDir, path)

    // Basic security: Block traversal
    if !strings.HasPrefix(fullPath, filepath.Clean(video.HlsDir)) {
        c.String(http.StatusNotFound, "Not found")
        return
    }

    // Check file exists
    if _, err := os.Stat(fullPath); os.IsNotExist(err) {
        c.String(http.StatusNotFound, "Not found")
        return
    }

    // Force MIME types
    ext := filepath.Ext(fullPath)
    switch ext {
    case ".m3u8":
        c.Header("Content-Type", "application/vnd.apple.mpegurl")
    case ".ts":
        c.Header("Content-Type", "video/mp2t")  // Key fix: What VLC expects for HLS segments
    default:
        c.Header("Content-Type", "application/octet-stream")
    }
    c.Header("Accept-Ranges", "bytes")  // Enables partial requests if needed
    c.Header("Cache-Control", "no-cache")  // Fresh fetches for polling

    http.ServeFile(c.Writer, c.Request, fullPath)
}

func main() {
    if err := os.MkdirAll(video.MediaDir, 0755); err != nil {
        log.Fatal(err)
    }
    if err := os.MkdirAll(video.HlsDir, 0755); err != nil {
        log.Fatal(err)
    }

    r := gin.Default()
    r.GET("/stream", streamHandler)
    r.GET("/hls/*path", hlsHandler)  // Replace Static with this

    //log.Println("Generating stream immediately...")
   // if err := video.InsertBreak(1, 10); err != nil {
    //    log.Printf("Generation failed: %v", err)
    //} else {
    //    log.Println("Stream ready at http://localhost:8080/hls/playlist.m3u8")
    //}

    log.Println("Server running on :8080. Add media to ./media if needed.")
    if err := r.Run(":8080"); err != nil {
        log.Fatal(err)
    }
}