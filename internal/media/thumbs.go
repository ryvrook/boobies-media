package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// ThumbnailPayload is the job payload for the thumbnail handler.
type ThumbnailPayload struct {
	ItemID string `json:"item_id"`
}

// thumbnailQuality is the libwebp quality for thumbnails. Thumbnails are
// derived artefacts, so lossy is fine here, unlike the original bytes.
const thumbnailQuality = "82"
const socialPreviewSize = 1024

// GenerateThumbnail renders one thumbnail of the given size.
//
// ffmpeg does both the decode and the webp encode, so thumbnails keep working
// when cwebp is absent: a common state, since cwebp ships separately from
// ffmpeg on most systems.
func (s *Store) GenerateThumbnail(ctx context.Context, srcPath, dstPath string, size int, isVideo bool, duration float64) error {
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
		return fmt.Errorf("media: create thumbnail directory: %w", err)
	}

	// Fit inside a size x size box, never upscaling: the target box is
	// min(size, source) on each axis and the aspect ratio is preserved.
	scale := fmt.Sprintf("scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease:flags=lanczos", size, size)

	args := []string{"-v", "error", "-y"}
	if isVideo {
		// Frame 0 of a clip is very often black or a fade-in. Seek a little way
		// in, but never past the end of a short clip.
		seek := 1.0
		if duration > 0 && duration < 2 {
			seek = duration / 2
		}
		args = append(args, "-ss", strconv.FormatFloat(seek, 'f', 3, 64))
	}
	args = append(args,
		"-i", srcPath,
		"-frames:v", "1",
		"-vf", scale,
		"-c:v", "libwebp",
		"-lossless", "0",
		"-quality", thumbnailQuality,
		"-f", "image2",
		dstPath)

	if _, err := s.Runner.Run(ctx, "ffmpeg", args...); err != nil {
		return err
	}
	info, err := os.Stat(dstPath)
	if err != nil {
		return fmt.Errorf("media: ffmpeg reported success but wrote no thumbnail: %w", err)
	}
	if info.Size() == 0 {
		_ = os.Remove(dstPath)
		return fmt.Errorf("media: ffmpeg wrote an empty thumbnail for %s", srcPath)
	}
	return nil
}

// GenerateSocialPreview creates a bounded JPEG poster for Open Graph and
// Twitter cards. It writes through a temporary file and atomically renames
// it, so concurrent crawler requests never observe a partial image.
func (s *Store) GenerateSocialPreview(ctx context.Context, srcPath, dstPath string, isVideo bool, duration float64) error {
	if _, err := os.Stat(dstPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
		return fmt.Errorf("media: create social preview directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dstPath), ".social-*.jpg")
	if err != nil {
		return fmt.Errorf("media: create social preview: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	defer os.Remove(tmpPath)

	scale := fmt.Sprintf(
		"scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease:flags=lanczos",
		socialPreviewSize, socialPreviewSize,
	)
	args := []string{"-v", "error", "-y"}
	if isVideo {
		seek := 1.0
		if duration > 0 && duration < 2 {
			seek = duration / 2
		}
		args = append(args, "-ss", strconv.FormatFloat(seek, 'f', 3, 64))
	}
	args = append(args,
		"-i", srcPath,
		"-frames:v", "1",
		"-vf", scale,
		"-c:v", "mjpeg",
		"-q:v", "3",
		"-f", "image2",
		tmpPath,
	)
	if _, err := s.Runner.Run(ctx, "ffmpeg", args...); err != nil {
		return err
	}
	info, err := os.Stat(tmpPath)
	if err != nil || info.Size() == 0 {
		return fmt.Errorf("media: ffmpeg wrote no social preview")
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		return fmt.Errorf("media: place social preview: %w", err)
	}
	return nil
}
