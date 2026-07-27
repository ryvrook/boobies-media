package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"boobies-media/internal/config"
	"boobies-media/internal/db"
	"boobies-media/internal/media"
)

type URLJob struct {
	URL        string `json:"url"`
	UploaderID int64  `json:"uploader_id"`
}

type Saver interface {
	Save(context.Context, media.SaveRequest) (*media.SaveResult, error)
}

const defaultDownloadCap = 2 << 30
const publicErrorPrefix = "ingest-user: "

func PublicError(stored string) (string, bool) {
	message, ok := strings.CutPrefix(stored, publicErrorPrefix)
	return message, ok
}

type Ingestor struct {
	Settings   SettingsReader
	Media      Saver
	Fetcher    *Fetcher
	Ripper     *Ripper
	CookiesDir string
	TmpDir     string
	FreeSpace  FreeSpaceFunc
}

func NewIngestor(cfg *config.Config, settings SettingsReader, saver Saver) *Ingestor {
	return &Ingestor{
		Settings: settings, Media: saver,
		Fetcher:    NewFetcher(cfg.TmpDir(), ClientOptions{}),
		Ripper:     NewRipper(media.NewExecRunner(), cfg.TmpDir()),
		CookiesDir: cfg.CookiesDir(), TmpDir: cfg.TmpDir(), FreeSpace: FreeSpace,
	}
}

func (i *Ingestor) Handle(ctx context.Context, job db.Job) (resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = errors.New(publicErrorPrefix + resultErr.Error())
		}
	}()
	var payload URLJob
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("ingest: job payload is not valid JSON: %w", err)
	}
	if payload.UploaderID == 0 {
		return errors.New("ingest: job payload has no uploader")
	}
	classification, err := Classify(payload.URL)
	if err != nil {
		return err
	}
	maxBytes := i.settingInt64(ctx, "download_max_bytes", defaultDownloadCap)
	headroom := uint64(i.settingInt64(ctx, "min_free_disk_bytes", DefaultDiskHeadroom))
	if err := CheckFreeSpace(i.FreeSpace, i.TmpDir, uint64(maxBytes), headroom); err != nil {
		return err
	}
	i.Fetcher.DiskHeadroom = headroom
	switch classification.Kind {
	case KindDirect, KindDiscordCDN:
		return i.fetchOne(ctx, job, payload, classification, maxBytes)
	case KindYtDlp:
		return i.rip(ctx, job, payload, classification, maxBytes)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedSource, payload.URL)
	}
}

func (i *Ingestor) fetchOne(ctx context.Context, job db.Job, payload URLJob, c Classification, maxBytes int64) error {
	res, err := i.Fetcher.Fetch(ctx, c.URL, maxBytes)
	if err != nil {
		return err
	}
	defer os.Remove(res.Path)
	file, err := os.Open(res.Path)
	if err != nil {
		return fmt.Errorf("ingest: reopen download: %w", err)
	}
	defer file.Close()
	_, err = i.Media.Save(ctx, media.SaveRequest{
		Reader: file, Filename: res.Filename, UploaderID: payload.UploaderID,
		SourceURL: c.URL, JobID: job.ID, MaxBytes: maxBytes,
	})
	if errors.Is(err, media.ErrUnsupportedType) {
		return fmt.Errorf("the link downloaded, but it is not a supported image or video: %w", err)
	}
	return err
}

func (i *Ingestor) rip(ctx context.Context, job db.Job, payload URLJob, c Classification, maxBytes int64) error {
	format, err := i.Settings.SettingGet(ctx, "ytdlp_format")
	if err != nil {
		return fmt.Errorf("ingest: read ytdlp_format: %w", err)
	}
	cookie, err := ResolveCookieFile(ctx, i.Settings, i.CookiesDir, c.Extractor)
	if err != nil {
		return err
	}
	res, err := i.Ripper.Rip(ctx, RipRequest{
		URL: c.URL, Extractor: c.Extractor, Format: format, CookieFile: cookie, MaxBytes: maxBytes,
	})
	if err != nil {
		return err
	}
	defer os.RemoveAll(res.Dir)
	saved := 0
	for _, path := range res.Files {
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("ingest: open ripped file: %w", err)
		}
		_, saveErr := i.Media.Save(ctx, media.SaveRequest{
			Reader: file, Filename: filepath.Base(path), UploaderID: payload.UploaderID,
			SourceURL: c.URL, JobID: job.ID, MaxBytes: maxBytes,
		})
		_ = file.Close()
		if errors.Is(saveErr, media.ErrUnsupportedType) {
			continue
		}
		if saveErr != nil {
			return saveErr
		}
		saved++
	}
	if saved == 0 {
		return fmt.Errorf("%s downloaded %d file(s), but none is supported media", res.Tool, len(res.Files))
	}
	return nil
}

func (i *Ingestor) settingInt64(ctx context.Context, key string, fallback int64) int64 {
	raw, err := i.Settings.SettingGet(ctx, key)
	if err != nil {
		return fallback
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
