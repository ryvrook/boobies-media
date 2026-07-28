package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"boobies-media/internal/db"
	"boobies-media/internal/jobs"
)

// RegisterHandlers wires this package's job types into the queue. Plan 3
// registers the ingest_url handler the same way.
func (s *Store) RegisterHandlers(queue *jobs.Queue) {
	queue.Register(jobs.TypeProbe, s.HandleProbeJob)
	queue.Register(jobs.TypeThumbnail, s.HandleThumbnailJob)
}

// HandleProbeJob records dimensions and duration, then queues thumbnailing.
//
// Probing and thumbnailing are separate jobs so a broken ffmpeg does not also
// cost you the dimensions, and so each retries independently.
func (s *Store) HandleProbeJob(ctx context.Context, job db.Job) error {
	var payload ProbePayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("media: bad probe payload: %w", err)
	}
	item, err := s.DB.ItemByID(ctx, payload.ItemID)
	if errors.Is(err, db.ErrNotFound) {
		// Deleted before the worker got to it. Retrying will never help.
		slog.Info("probe job skipped: item is gone", "item", payload.ItemID)
		return nil
	}
	if err != nil {
		return err
	}

	result, err := s.ProbeFile(ctx, BlobPath(s.Cfg.FilesDir(), item.ContentHash))
	if err != nil {
		return err
	}
	if err := s.DB.SetItemProbe(ctx, item.ID, result.Width, result.Height, result.Duration); err != nil {
		return err
	}
	if s.Queue != nil {
		if _, err := s.Queue.Enqueue(ctx, jobs.TypeThumbnail, ThumbnailPayload{ItemID: item.ID}); err != nil {
			return fmt.Errorf("media: enqueue thumbnail for %s: %w", item.ID, err)
		}
	}
	return nil
}

// HandleThumbnailJob renders every configured thumbnail size.
func (s *Store) HandleThumbnailJob(ctx context.Context, job db.Job) error {
	var payload ThumbnailPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("media: bad thumbnail payload: %w", err)
	}
	item, err := s.DB.ItemByID(ctx, payload.ItemID)
	if errors.Is(err, db.ErrNotFound) {
		slog.Info("thumbnail job skipped: item is gone", "item", payload.ItemID)
		return nil
	}
	if err != nil {
		return err
	}

	src := BlobPath(s.Cfg.FilesDir(), item.ContentHash)
	isVideo := IsVideoMime(item.Mime)
	for _, size := range ThumbSizes {
		dst := ThumbPath(s.Cfg.ThumbsDir(), item.ContentHash, size)
		if err := s.GenerateThumbnail(ctx, src, dst, size, isVideo, item.Duration); err != nil {
			return fmt.Errorf("media: thumbnail %d for %s: %w", size, item.ID, err)
		}
	}
	// Social crawlers have short timeouts. Generate the compatible video
	// rendition during normal processing instead of making Discord perform a
	// full transcode on its first request to /v/{id}.mp4.
	if isVideo {
		dst := SocialVideoPath(s.Cfg.ThumbsDir(), item.ContentHash)
		if err := s.GenerateSocialVideo(ctx, src, dst); err != nil {
			return fmt.Errorf("media: social video for %s: %w", item.ID, err)
		}
	}
	return nil
}
