package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"boobies-media/internal/media"
)

var (
	ErrNeedsCookies      = errors.New("ingest: this source needs an exported cookie file")
	ErrNothingDownloaded = errors.New("ingest: nothing was downloaded")
)

type Ripper struct {
	Runner media.Runner
	TmpDir string
}

func NewRipper(runner media.Runner, tmpDir string) *Ripper {
	return &Ripper{Runner: runner, TmpDir: tmpDir}
}

type RipRequest struct {
	URL, Extractor, Format, CookieFile string
	MaxBytes                           int64
}

type RipResult struct {
	Dir   string
	Files []string
	Tool  string
}

func (r *Ripper) RipWithYtDlp(ctx context.Context, req RipRequest) (*RipResult, error) {
	dir, err := os.MkdirTemp(r.TmpDir, "ytdlp-*")
	if err != nil {
		return nil, fmt.Errorf("ingest: create rip directory: %w", err)
	}
	args := []string{"--no-playlist", "--no-progress", "--no-warnings", "--no-part", "--restrict-filenames", "--merge-output-format", "mp4"}
	if req.Format != "" {
		args = append(args, "-f", req.Format)
	}
	if req.MaxBytes > 0 {
		args = append(args, "--max-filesize", strconv.FormatInt(req.MaxBytes, 10))
	}
	if req.CookieFile != "" {
		args = append(args, "--cookies", req.CookieFile)
	}
	args = append(args, "-o", filepath.Join(dir, "%(id)s.%(ext)s"), "--", req.URL)
	output, runErr := r.Runner.Run(ctx, "yt-dlp", args...)
	if runErr != nil {
		_ = os.RemoveAll(dir)
		if errors.Is(runErr, media.ErrToolMissing) {
			return nil, fmt.Errorf("%w: install yt-dlp to ingest %s links", runErr, req.Extractor)
		}
		return nil, TranslateToolError("yt-dlp", runErr, string(output))
	}
	files, err := collectOutputFiles(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if len(files) == 0 {
		_ = os.RemoveAll(dir)
		return nil, TranslateToolError("yt-dlp", ErrNothingDownloaded, string(output))
	}
	return &RipResult{Dir: dir, Files: files, Tool: "yt-dlp"}, nil
}

func collectOutputFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		for _, suffix := range []string{".part", ".ytdl", ".temp", ".tmp"} {
			if strings.HasSuffix(entry.Name(), suffix) {
				return nil
			}
		}
		info, err := entry.Info()
		if err == nil && info.Size() > 0 {
			files = append(files, path)
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("ingest: list downloaded files: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

func TranslateToolError(tool string, cause error, output string) error {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "max-filesize"), strings.Contains(lower, "larger than"):
		return fmt.Errorf("%w: %s stopped because the file is too large", ErrDownloadTooLarge, tool)
	case strings.Contains(lower, "--cookies"), strings.Contains(lower, "sign in"), strings.Contains(lower, "log in"),
		strings.Contains(lower, "login"), strings.Contains(lower, "requires authentication"),
		strings.Contains(lower, "private video"), strings.Contains(lower, "age-restricted"):
		return fmt.Errorf("%w: %s could not access this without an exported cookie file", ErrNeedsCookies, tool)
	case strings.Contains(lower, "unsupported url"), strings.Contains(lower, "no video formats found"), strings.Contains(lower, "unable to extract"):
		return fmt.Errorf("%w: %s does not know how to download this link", ErrUnsupportedSource, tool)
	}
	detail := cause.Error()
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			detail = strings.TrimSpace(line)
			if strings.HasPrefix(detail, "ERROR") {
				break
			}
		}
	}
	return fmt.Errorf("ingest: %s failed: %s", tool, detail)
}
