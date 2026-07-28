package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const maxFixupXMetadataBytes = 2 << 20

// fixupXMediaURLs asks FxEmbed's public JSON endpoint for the original media
// attachments. This is the last fallback for FixupX links because sensitive
// posts can be unavailable to logged-out yt-dlp/gallery-dl sessions while
// still being intentionally exposed by the embed helper.
func (i *Ingestor) fixupXMediaURLs(ctx context.Context, statusID string) ([]string, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"https://api.fxtwitter.com/status/"+statusID,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("ingest: create FixupX API request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "boobies-media/1.0 (+self-hosted media server)")
	resp, err := i.Fetcher.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ingest: request FixupX API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("ingest: FixupX API returned HTTP %d", resp.StatusCode)
	}

	var body struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Tweet   struct {
			Media struct {
				All []struct {
					Type string `json:"type"`
					URL  string `json:"url"`
				} `json:"all"`
			} `json:"media"`
		} `json:"tweet"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxFixupXMetadataBytes))
	if err := decoder.Decode(&body); err != nil {
		return nil, fmt.Errorf("ingest: decode FixupX API response: %w", err)
	}
	if body.Code != 0 && body.Code != http.StatusOK {
		return nil, fmt.Errorf("ingest: FixupX API refused the post: %s", body.Message)
	}
	urls := make([]string, 0, len(body.Tweet.Media.All))
	for _, attachment := range body.Tweet.Media.All {
		switch attachment.Type {
		case "photo", "video", "gif", "animated_gif":
			if attachment.URL != "" {
				urls = append(urls, attachment.URL)
			}
		}
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("%w: FixupX returned no media attachments", ErrNothingDownloaded)
	}
	return urls, nil
}
