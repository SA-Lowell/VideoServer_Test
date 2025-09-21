package video

import (
    "fmt"
    "log"
    "os"
    "path/filepath"
    "strings"
)

const (
    MediaDir = "./media"
    HlsDir   = "./hls"
)

func InsertBreak(duration int, numBreaks int) error {
    if err := os.MkdirAll(HlsDir, 0755); err != nil {
        return fmt.Errorf("failed to create HLS dir: %w", err)
    }

    for i := 0; i < numBreaks; i++ {
        tsFile := filepath.Join(HlsDir, fmt.Sprintf("segment_%d.ts", i+1))
        if err := os.WriteFile(tsFile, []byte(fmt.Sprintf("Dummy segment %d content", i+1)), 0644); err != nil {
            return fmt.Errorf("failed to create segment %d: %w", i+1, err)
        }
    }

    var playlist strings.Builder
    playlist.WriteString("#EXTM3U\n")
    playlist.WriteString("#EXT-X-VERSION:3\n")
    playlist.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", duration))
    playlist.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")

    for i := 0; i < numBreaks; i++ {
        playlist.WriteString(fmt.Sprintf("#EXTINF:%d,\n", duration))
        playlist.WriteString(fmt.Sprintf("segment_%d.ts\n", i+1))
    }
    playlist.WriteString("#EXT-X-ENDLIST\n")

    m3u8File := filepath.Join(HlsDir, "playlist.m3u8")
    if err := os.WriteFile(m3u8File, []byte(playlist.String()), 0644); err != nil {
        return fmt.Errorf("failed to write playlist: %w", err)
    }

    log.Printf("Generated %d segments into %s", numBreaks, HlsDir)
    return nil
}