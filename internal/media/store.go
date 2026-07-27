package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"boobies-media/internal/config"
	"boobies-media/internal/db"
	"boobies-media/internal/jobs"
)

// ErrTooLarge means the upload exceeded the configured size cap.
var ErrTooLarge = errors.New("media: file is larger than the upload limit")

// Enqueuer schedules follow-up work. *jobs.Queue satisfies it; tests pass a
// recorder.
type Enqueuer interface {
	Enqueue(ctx context.Context, jobType string, payload any) (int64, error)
}

// Sidecar is written beside every blob so the library stays identifiable even
// if media.db is lost. Extensionless SHA-256 blobs are otherwise anonymous.
type Sidecar struct {
	Ext       string `json:"ext"`
	Mime      string `json:"mime"`
	Title     string `json:"title"`
	SourceURL string `json:"source_url,omitempty"`
	Uploader  string `json:"uploader"`
}

// Store owns the ingestion pipeline and the blob directory.
type Store struct {
	Cfg       *config.Config
	DB        *db.Store
	Runner    Runner
	Optimizer *Optimizer
	Queue     Enqueuer
}

// NewStore wires the pipeline with the real command runner.
func NewStore(cfg *config.Config, database *db.Store, queue Enqueuer) *Store {
	runner := NewExecRunner()
	return &Store{
		Cfg:       cfg,
		DB:        database,
		Runner:    runner,
		Optimizer: NewOptimizer(runner, cfg.TmpDir()),
		Queue:     queue,
	}
}

// SaveRequest describes one file entering the library. Plan 3's URL ingestion
// fills in SourceURL and JobID and is otherwise identical to an upload.
type SaveRequest struct {
	Reader     io.Reader
	Filename   string
	UploaderID int64
	FolderID   int64
	SourceURL  string
	JobID      int64
	// MaxBytes overrides the upload_max_bytes setting when positive.
	MaxBytes int64
}

// SaveResult reports what happened.
type SaveResult struct {
	Item         *db.Item
	Deduplicated bool // the blob already existed; only a new row was written
	Optimized    bool // the bytes were converted to lossless webp
}

// Save runs the full pipeline: cap, sniff, allowlist, optimize, hash, place,
// sidecar, item row, probe job.
func (s *Store) Save(ctx context.Context, req SaveRequest) (*SaveResult, error) {
	if req.Reader == nil {
		return nil, errors.New("media: no reader supplied")
	}
	if req.UploaderID == 0 {
		return nil, errors.New("media: no uploader supplied")
	}

	maxBytes, err := s.maxUploadBytes(ctx, req.MaxBytes)
	if err != nil {
		return nil, err
	}

	tmpPath, size, err := s.spool(req.Reader, maxBytes)
	if err != nil {
		return nil, err
	}
	// Track every scratch file so nothing leaks, whatever path we take.
	scratch := map[string]bool{tmpPath: true}
	defer func() {
		for path := range scratch {
			_ = os.Remove(path)
		}
	}()

	header, err := readHeader(tmpPath)
	if err != nil {
		return nil, err
	}
	mime := Sniff(header)
	if !IsAllowedMime(mime) {
		// Rejected before anything is hashed, stored or recorded.
		return nil, fmt.Errorf("%w: %q is not an accepted media type", ErrUnsupportedType, req.Filename)
	}

	autoWebp, err := s.settingEnabled(ctx, "auto_webp")
	if err != nil {
		return nil, err
	}
	finalPath, finalMime, optimized, err := s.Optimizer.Optimize(ctx, tmpPath, mime, header, autoWebp)
	if err != nil {
		return nil, err
	}
	scratch[finalPath] = true
	if optimized {
		info, err := os.Stat(finalPath)
		if err != nil {
			return nil, err
		}
		size = info.Size()
	}

	// Hash the bytes that will actually be stored, so dedup matches what is
	// on disk rather than what was uploaded.
	hash, err := hashFile(finalPath)
	if err != nil {
		return nil, err
	}

	blobPath := BlobPath(s.Cfg.FilesDir(), hash)
	deduplicated, err := placeBlob(finalPath, blobPath)
	if err != nil {
		return nil, err
	}
	if !deduplicated {
		delete(scratch, finalPath) // renamed into place; nothing left to clean
	}

	// From here on, no item row exists yet to own the blob. If we just placed
	// a brand-new blob (not a dedup hit) and anything below fails, unlink it
	// and its sidecar rather than leaving bytes that Purge can never reach:
	// Purge only ever unlinks via an item row, so a blob with none would sit
	// on disk forever. A dedup hit must never be touched here: some other,
	// already-committed item still references those exact bytes.
	//
	// The local `deduplicated` bool is not proof of exclusive ownership: two
	// concurrent Save calls for byte-identical content can both observe
	// placeBlob's os.Stat missing the blob and both rename their own temp
	// file onto the same path (a pre-existing, otherwise-harmless TOCTOU in
	// placeBlob). If this call then fails a later step while the other one
	// commits its item row, trusting `deduplicated` here would unlink bytes
	// the other, already-live item now depends on. So before removing
	// anything, ask the database (the source of truth for who references
	// this hash) rather than our own local belief about how we got here.
	// No item row from *this* call exists yet at any call site below, so
	// there is nothing to exclude from the count.
	cleanupOrphanBlob := func() {
		if deduplicated {
			return
		}
		refs, err := s.DB.ContentHashRefCount(ctx, hash, "")
		if err != nil {
			slog.Error("media: check content hash references before cleanup", "hash", hash, "err", err)
			return // can't prove the blob is orphaned; leave it rather than risk deleting live bytes
		}
		if refs > 0 {
			return // a concurrent upload already committed an item under this hash
		}
		_ = os.Remove(blobPath)
		_ = os.Remove(SidecarPath(s.Cfg.FilesDir(), hash))
	}

	title := titleFromFilename(req.Filename)
	uploader, err := s.DB.UserByID(ctx, req.UploaderID)
	if err != nil {
		cleanupOrphanBlob()
		return nil, fmt.Errorf("media: resolve uploader: %w", err)
	}
	if err := s.writeSidecar(hash, Sidecar{
		Ext:       ExtForMime(finalMime),
		Mime:      finalMime,
		Title:     title,
		SourceURL: req.SourceURL,
		Uploader:  uploader.Username,
	}); err != nil {
		cleanupOrphanBlob()
		return nil, err
	}

	item, err := s.DB.CreateItem(ctx, db.NewItem{
		ContentHash: hash,
		Title:       title,
		Ext:         ExtForMime(finalMime),
		Mime:        finalMime,
		Size:        size,
		UploaderID:  req.UploaderID,
		FolderID:    req.FolderID,
		SourceURL:   req.SourceURL,
		JobID:       req.JobID,
	})
	if err != nil {
		cleanupOrphanBlob()
		return nil, err
	}

	// The item is browsable straight away; the probe job fills in dimensions
	// and then enqueues thumbnailing. If the enqueue fails, the item row is
	// already committed but will never be probed and nothing retries it:
	// left alone, that is a permanently-broken-looking library entry. Rather
	// than leave it, roll the item back: a retry re-hashes to the same blob
	// (placeBlob dedups it for free) and gets one clean row instead of this
	// call's row sitting around dead next to a fresh one. Purge is safe to
	// reuse here: it only unlinks the blob/sidecar/thumbs if the DB confirms
	// (in the same transaction as the row delete) that nothing else
	// references the hash, which also covers a concurrent dedup landing on
	// this exact hash in the meantime.
	if s.Queue != nil {
		if _, err := s.Queue.Enqueue(ctx, jobs.TypeProbe, ProbePayload{ItemID: item.ID}); err != nil {
			slog.Error("media: enqueue probe failed, rolling back item", "item", item.ID, "err", err)
			// Save is always called with the request's context (r.Context()),
			// so an ordinary client disconnect right after CreateItem commits
			// cancels ctx and would fail this rollback for the identical
			// reason it failed Enqueue, leaving the item neither probed nor
			// rolled back. Detach the rollback from ctx's cancellation, the
			// same way internal/jobs/queue.go's outcomeContext detaches a
			// job-outcome write from a shutting-down worker's ctx.
			rbCtx, cancel := rollbackContext(ctx)
			defer cancel()
			if purgeErr := s.Purge(rbCtx, item.ID); purgeErr != nil {
				slog.Error("media: rollback after failed probe enqueue also failed", "item", item.ID, "err", purgeErr)
			}
			return nil, fmt.Errorf("media: enqueue probe for %s: %w", item.ID, err)
		}
	}

	return &SaveResult{Item: item, Deduplicated: deduplicated, Optimized: optimized}, nil
}

// rollbackWriteTimeout bounds the Purge call that rolls an item back after a
// failed probe enqueue, once it has been detached from the request context's
// cancellation (see rollbackContext). Matches jobs.outcomeWriteTimeout.
const rollbackWriteTimeout = 5 * time.Second

// rollbackContext derives a context for the enqueue-failure rollback that
// survives cancellation of ctx, mirroring internal/jobs/queue.go's
// outcomeContext: Save is always called with the inbound request's context,
// so without this an ordinary client disconnect would take the rollback
// down along with the enqueue call it is trying to undo.
func rollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), rollbackWriteTimeout)
}

// maxUploadBytes resolves the effective cap.
func (s *Store) maxUploadBytes(ctx context.Context, override int64) (int64, error) {
	if override > 0 {
		return override, nil
	}
	raw, err := s.DB.SettingGet(ctx, "upload_max_bytes")
	if err != nil {
		return 0, err
	}
	limit, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("media: upload_max_bytes is not a positive number: %q", raw)
	}
	return limit, nil
}

func (s *Store) settingEnabled(ctx context.Context, key string) (bool, error) {
	raw, err := s.DB.SettingGet(ctx, key)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true", "1", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// spool streams the upload to a temp file on the same filesystem as FilesDir,
// enforcing the cap without ever holding the whole file in memory.
func (s *Store) spool(reader io.Reader, maxBytes int64) (string, int64, error) {
	tmp, err := os.CreateTemp(s.Cfg.TmpDir(), "upload-*")
	if err != nil {
		return "", 0, fmt.Errorf("media: create temp file: %w", err)
	}
	path := tmp.Name()

	// Read one byte past the cap so exceeding it is detectable.
	written, copyErr := io.Copy(tmp, io.LimitReader(reader, maxBytes+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(path)
		return "", 0, fmt.Errorf("media: buffer upload: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", 0, fmt.Errorf("media: close temp file: %w", closeErr)
	}
	if written > maxBytes {
		_ = os.Remove(path)
		return "", 0, fmt.Errorf("%w: limit is %d bytes", ErrTooLarge, maxBytes)
	}
	return path, written, nil
}

func readHeader(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("media: open for sniffing: %w", err)
	}
	defer file.Close()

	header := make([]byte, SniffLen)
	n, err := io.ReadFull(file, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("media: read header: %w", err)
	}
	return header[:n], nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("media: open for hashing: %w", err)
	}
	defer file.Close()

	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("media: hash: %w", err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// placeBlob moves src into the content-addressed location. It reports whether
// the blob was already there, in which case the upload deduplicated.
func placeBlob(src, dst string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return false, fmt.Errorf("media: create blob directory: %w", err)
	}
	if _, err := os.Stat(dst); err == nil {
		return true, nil // identical bytes already stored
	}
	if err := os.Rename(src, dst); err != nil {
		return false, fmt.Errorf("media: move blob into place: %w", err)
	}
	return false, nil
}

// writeSidecar writes via a same-directory temp file plus rename, so a
// concurrent reader (or a crash mid-write) never observes a truncated or
// half-written sidecar: it is always either absent or complete.
func (s *Store) writeSidecar(hash string, sidecar Sidecar) error {
	encoded, err := json.Marshal(sidecar)
	if err != nil {
		return fmt.Errorf("media: encode sidecar: %w", err)
	}
	path := SidecarPath(s.Cfg.FilesDir(), hash)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("media: create sidecar directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("media: create sidecar temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("media: write sidecar: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("media: close sidecar temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("media: place sidecar: %w", err)
	}
	return nil
}

// titleFromFilename derives a human title, falling back to something usable
// when the client sends nothing or sends a path.
func titleFromFilename(filename string) string {
	base := SanitizeFilename(filename)
	if base == "download" {
		return "Untitled"
	}
	title := strings.TrimSuffix(base, filepath.Ext(base))
	title = strings.TrimSpace(title)
	if title == "" {
		return "Untitled"
	}
	return title
}

// Purge permanently removes an item and, when nothing else references its
// content, unlinks the blob, its sidecar and its thumbnails.
func (s *Store) Purge(ctx context.Context, itemID string) error {
	hash, err := s.DB.PurgeItem(ctx, itemID)
	if err != nil {
		return err
	}
	if hash == "" {
		return nil // another item still points at these bytes
	}
	paths := []string{
		BlobPath(s.Cfg.FilesDir(), hash),
		SidecarPath(s.Cfg.FilesDir(), hash),
	}
	for _, size := range ThumbSizes {
		paths = append(paths, ThumbPath(s.Cfg.ThumbsDir(), hash, size))
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("media: unlink %s: %w", path, err)
		}
	}
	return nil
}
