package media

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
)

// ProbePayload is the job payload for the probe handler.
type ProbePayload struct {
	ItemID string `json:"item_id"`
}

// ProbeResult is what ffprobe reported.
type ProbeResult struct {
	Width    int64
	Height   int64
	Duration float64
}

// ffprobeOutput mirrors the JSON ffprobe emits for the flags used below.
// format.duration is a string, and it is absent entirely for still images.
type ffprobeOutput struct {
	Streams []struct {
		Width  int64 `json:"width"`
		Height int64 `json:"height"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// ProbeFile reads dimensions and duration from a media file.
func (s *Store) ProbeFile(ctx context.Context, path string) (*ProbeResult, error) {
	out, err := s.Runner.Run(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-show_entries", "format=duration",
		"-of", "json",
		"--", path)
	if err != nil {
		if fallback, fallbackErr := probeImageHeader(path); fallbackErr == nil {
			return fallback, nil
		}
		return nil, err
	}

	var parsed ffprobeOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("media: ffprobe produced output that is not JSON: %w", err)
	}

	result := &ProbeResult{}
	if len(parsed.Streams) > 0 {
		result.Width = parsed.Streams[0].Width
		result.Height = parsed.Streams[0].Height
	}
	if result.Width <= 0 || result.Height <= 0 {
		if fallback, err := probeImageHeader(path); err == nil {
			return fallback, nil
		}
	}
	if parsed.Format.Duration != "" {
		// Ignore a malformed duration rather than failing the whole probe: the
		// dimensions are the part the UI actually needs.
		if seconds, err := strconv.ParseFloat(parsed.Format.Duration, 64); err == nil && seconds > 0 {
			result.Duration = seconds
		}
	}
	return result, nil
}

// probeImageHeader reads dimensions directly from GIF/WebP container headers.
// It keeps browser-decodable animated images out of a permanent processing
// state when the installed FFprobe cannot decode their animation stream.
func probeImageHeader(path string) (*ProbeResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	header := make([]byte, 64)
	n, err := file.Read(header)
	if err != nil && err != io.EOF {
		return nil, err
	}
	header = header[:n]

	if len(header) >= 10 && (string(header[:6]) == "GIF87a" || string(header[:6]) == "GIF89a") {
		width := binary.LittleEndian.Uint16(header[6:8])
		height := binary.LittleEndian.Uint16(header[8:10])
		if width > 0 && height > 0 {
			return &ProbeResult{Width: int64(width), Height: int64(height)}, nil
		}
	}
	if len(header) < 25 || string(header[:4]) != "RIFF" || string(header[8:12]) != "WEBP" {
		return nil, fmt.Errorf("media: image header has no supported dimensions")
	}
	switch string(header[12:16]) {
	case "VP8X":
		if len(header) >= 30 {
			width := 1 + int64(header[24]) + int64(header[25])<<8 + int64(header[26])<<16
			height := 1 + int64(header[27]) + int64(header[28])<<8 + int64(header[29])<<16
			return &ProbeResult{Width: width, Height: height}, nil
		}
	case "VP8L":
		if len(header) >= 25 && header[20] == 0x2f {
			width := 1 + int64(header[21]) + int64(header[22]&0x3f)<<8
			height := 1 + int64(header[22]>>6) + int64(header[23])<<2 + int64(header[24]&0x0f)<<10
			return &ProbeResult{Width: width, Height: height}, nil
		}
	case "VP8 ":
		if len(header) >= 30 && header[23] == 0x9d && header[24] == 0x01 && header[25] == 0x2a {
			width := binary.LittleEndian.Uint16(header[26:28]) & 0x3fff
			height := binary.LittleEndian.Uint16(header[28:30]) & 0x3fff
			return &ProbeResult{Width: int64(width), Height: int64(height)}, nil
		}
	}
	return nil, fmt.Errorf("media: WebP header has no supported dimensions")
}
