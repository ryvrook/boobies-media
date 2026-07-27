package media

import (
	"context"
	"encoding/json"
	"fmt"
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
	if parsed.Format.Duration != "" {
		// Ignore a malformed duration rather than failing the whole probe: the
		// dimensions are the part the UI actually needs.
		if seconds, err := strconv.ParseFloat(parsed.Format.Duration, 64); err == nil && seconds > 0 {
			result.Duration = seconds
		}
	}
	return result, nil
}
