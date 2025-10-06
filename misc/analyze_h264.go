package main

import (
    "fmt"
    "io/ioutil"
    "log"
    "os"
    "path/filepath"
)

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

func main() {
    if len(os.Args) < 2 {
        log.Fatal("Usage: go run this.go <path_to_h264_file>")
    }
    filePath := os.Args[1]
    data, err := ioutil.ReadFile(filePath)
    if err != nil {
        log.Fatal(err)
    }
    nalus := splitNALUs(data)
    fmt.Printf("File: %s, NALUs: %d\n", filepath.Base(filePath), len(nalus))
    for i, nalu := range nalus {
        if len(nalu) > 0 {
            nalType := int(nalu[0] & 0x1F)
            fmt.Printf("NALU %d type %d\n", i, nalType)
        }
    }
}