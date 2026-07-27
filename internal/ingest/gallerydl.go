package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"

	"boobies-media/internal/media"
)

func (r *Ripper) RipWithGalleryDL(ctx context.Context, req RipRequest) (*RipResult, error) {
	dir, err := os.MkdirTemp(r.TmpDir, "gallerydl-*")
	if err != nil {
		return nil, fmt.Errorf("ingest: create rip directory: %w", err)
	}
	args := []string{"-D", dir, "--no-mtime"}
	if req.CookieFile != "" {
		args = append(args, "--cookies", req.CookieFile)
	}
	args = append(args, "--", req.URL)
	output, runErr := r.Runner.Run(ctx, "gallery-dl", args...)
	if runErr != nil {
		_ = os.RemoveAll(dir)
		if errors.Is(runErr, media.ErrToolMissing) {
			return nil, fmt.Errorf("%w: install gallery-dl to ingest image galleries", runErr)
		}
		return nil, TranslateToolError("gallery-dl", runErr, string(output))
	}
	files, err := collectOutputFiles(dir)
	if err != nil || len(files) == 0 {
		_ = os.RemoveAll(dir)
		if err != nil {
			return nil, err
		}
		return nil, TranslateToolError("gallery-dl", ErrNothingDownloaded, string(output))
	}
	return &RipResult{Dir: dir, Files: files, Tool: "gallery-dl"}, nil
}

func (r *Ripper) Rip(ctx context.Context, req RipRequest) (*RipResult, error) {
	result, err := r.RipWithYtDlp(ctx, req)
	if err == nil || !shouldTryGallery(req.Extractor, err) {
		return result, err
	}
	fallback, fallbackErr := r.RipWithGalleryDL(ctx, req)
	if fallbackErr != nil {
		return nil, fmt.Errorf("%v; gallery-dl also failed: %w", err, fallbackErr)
	}
	return fallback, nil
}

func shouldTryGallery(extractor string, err error) bool {
	if extractor != "twitter" || errors.Is(err, ErrNeedsCookies) || errors.Is(err, ErrDownloadTooLarge) {
		return false
	}
	return errors.Is(err, media.ErrToolMissing) || errors.Is(err, ErrUnsupportedSource) || errors.Is(err, ErrNothingDownloaded)
}
