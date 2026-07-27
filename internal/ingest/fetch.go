package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"boobies-media/internal/media"
)

var (
	ErrDownloadTooLarge = errors.New("ingest: the download is larger than the limit")
	ErrBadStatus        = errors.New("ingest: the server refused the request")
)

type Fetcher struct {
	Client       *http.Client
	TmpDir       string
	FreeSpace    FreeSpaceFunc
	DiskHeadroom uint64
}

func NewFetcher(tmpDir string, opts ClientOptions) *Fetcher {
	return &Fetcher{Client: NewGuardedClient(opts), TmpDir: tmpDir, FreeSpace: FreeSpace, DiskHeadroom: DefaultDiskHeadroom}
}

type FetchResult struct {
	Path     string
	Size     int64
	Filename string
}

func (f *Fetcher) Fetch(ctx context.Context, rawURL string, maxBytes int64) (*FetchResult, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("ingest: download cap must be positive")
	}
	if err := CheckFreeSpace(f.FreeSpace, f.TmpDir, uint64(maxBytes), f.DiskHeadroom); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedSource, err)
	}
	req.Header.Set("User-Agent", "boobies-media/1.0 (+self-hosted media server)")
	req.Header.Set("Accept", "*/*")
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%w: HTTP %d", ErrBadStatus, resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("%w: server announced %d bytes", ErrDownloadTooLarge, resp.ContentLength)
	}
	tmp, err := os.CreateTemp(f.TmpDir, "fetch-*")
	if err != nil {
		return nil, fmt.Errorf("ingest: create scratch file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	written, err := io.Copy(tmp, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("ingest: download failed: %w", err)
	}
	if written > maxBytes {
		cleanup()
		return nil, fmt.Errorf("%w: stopped after %d bytes", ErrDownloadTooLarge, written)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("ingest: close scratch file: %w", err)
	}
	return &FetchResult{Path: tmpPath, Size: written, Filename: filenameFor(rawURL, resp.Header.Get("Content-Disposition"))}, nil
}

func filenameFor(rawURL, disposition string) string {
	if _, params, err := mime.ParseMediaType(disposition); err == nil {
		if name := strings.TrimSpace(params["filename"]); name != "" {
			return media.SanitizeFilename(name)
		}
	}
	if parsed, err := url.Parse(rawURL); err == nil {
		if base := path.Base(parsed.Path); base != "" && base != "/" && base != "." {
			return media.SanitizeFilename(base)
		}
	}
	return "download"
}
