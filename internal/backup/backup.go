// Package backup writes nightly, retention-pruned snapshots of the SQLite
// database so the catalog survives disk loss or operator error.
package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Source is the slice of the store this package needs.
type Source interface {
	BackupTo(ctx context.Context, path string) error
}

// tempName is the fixed, in-progress name every RunOnce vacuums into before
// renaming into the dated name prune's strict pattern (see backupNamePattern
// below) can see. Renaming from here is what makes a backup atomic: the
// pattern can never match this name, so a VACUUM INTO interrupted by a
// crash, a cancelled shutdown, or a full disk can never be counted toward
// the retain quota or handed to an operator as a restorable backup, because
// it never acquires a name prune or a human would recognise as one.
//
// RunOnce's own concurrency is the caller's responsibility: Runner.Trigger
// serialises calls through this package's exported entry point, but two
// RunOnce calls made directly against the same dir would race on this name,
// the same way they would already race on the destination file.
const tempName = ".media-backup.db.tmp"

// backupNamePattern is the exact, dated filename RunOnce writes. prune uses
// it to decide what it is allowed to count or delete: a file that does not
// match, hand-placed or left by anything else, is never touched.
var backupNamePattern = regexp.MustCompile(`^media-\d{4}-\d{2}-\d{2}\.db$`)

// RunOnce writes media-<date>.db into dir and prunes to the newest retain
// files. A same-day rerun overwrites, since VACUUM INTO refuses an existing
// target. now is injected so callers (and tests) control which date the
// backup lands on without touching the wall clock.
//
// The backup is written to a fixed temp name first and only renamed into its
// final dated name after VACUUM INTO succeeds. os.Rename is atomic on one
// filesystem, so dest is either absent or complete; there is no window where
// a crash, a cancelled context, or prune's own listing can observe (or keep)
// a partially written file under a name that looks like a valid backup.
func RunOnce(ctx context.Context, src Source, dir string, now time.Time, retain int) (string, error) {
	tmpPath := filepath.Join(dir, tempName)
	// A previous run that was killed mid VACUUM INTO leaves this behind.
	// Clear it before starting: VACUUM INTO refuses to write to a file that
	// already exists, and leftovers must not accumulate run over run.
	if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("backup: clear stale temp file %s: %w", tmpPath, err)
	}

	if err := src.BackupTo(ctx, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}

	name := "media-" + now.UTC().Format("2006-01-02") + ".db"
	dest := filepath.Join(dir, name)
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("backup: clear existing %s: %w", dest, err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("backup: place %s: %w", dest, err)
	}

	if err := prune(dir, retain); err != nil {
		return dest, err
	}
	return dest, nil
}

// prune keeps the newest retain backups. Only files whose name matches
// backupNamePattern exactly are candidates: anything else in dir, hand
// placed or left by something unrelated, is never counted toward retain and
// never deleted. Matching names sort chronologically because the date is
// ISO-8601, so a lexical sort is a chronological sort.
func prune(dir string, retain int) error {
	if retain <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("backup: list backups: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if backupNamePattern.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	if len(names) <= retain {
		return nil
	}
	sort.Strings(names)
	for _, old := range names[:len(names)-retain] {
		full := filepath.Join(dir, old)
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("backup: prune %s: %w", full, err)
		}
	}
	return nil
}

// ErrInProgress is returned by Trigger when a backup is already running. The
// single SQLite connection serialises everything anyway, but without this
// guard an overlapping tick would queue silently behind the first run and
// then immediately re-run against a destination file the first run may still
// be writing (VACUUM INTO refuses to overwrite an existing file), so the
// second pass would just fail with a confusing "file exists" error instead of
// the clear one this returns.
var ErrInProgress = errors.New("backup: a run is already in progress")

// Runner drives RunOnce on a ticker and guards against overlapping runs. It
// follows the same Start/Stop lifecycle as jobs.Queue and the upload
// janitor: Start launches one goroutine tracked by a WaitGroup, and Stop
// blocks until that goroutine has actually returned. Callers must cancel the
// context passed to Start before calling Stop, or Stop blocks forever.
type Runner struct {
	Source Source
	Dir    string
	Retain int
	Every  time.Duration
	// Now is injectable so tests can control the backup's date without the
	// wall clock.
	Now func() time.Time

	mu      sync.Mutex
	running bool
	wg      sync.WaitGroup
}

// NewRunner builds a Runner with Now defaulting to the real clock.
func NewRunner(src Source, dir string, retain int, every time.Duration) *Runner {
	return &Runner{
		Source: src,
		Dir:    dir,
		Retain: retain,
		Every:  every,
		Now:    func() time.Time { return time.Now().UTC() },
	}
}

// Trigger runs one backup pass now. It returns ErrInProgress instead of
// starting a second pass while one is already running.
func (r *Runner) Trigger(ctx context.Context) (string, error) {
	if !r.begin() {
		return "", ErrInProgress
	}
	defer r.end()
	return RunOnce(ctx, r.Source, r.Dir, r.Now(), r.Retain)
}

func (r *Runner) begin() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return false
	}
	r.running = true
	return true
}

func (r *Runner) end() {
	r.mu.Lock()
	r.running = false
	r.mu.Unlock()
}

// safeTrigger wraps Trigger with a panic recovery, converting a panic from
// deep inside Source.BackupTo into an ordinary error tick can log. Trigger's
// own defer r.end() still runs as the panic unwinds through it, before this
// recover ever sees the panic, so the in-progress flag is released either
// way.
func (r *Runner) safeTrigger(ctx context.Context) (dest string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("backup: run panicked: %v", rec)
		}
	}()
	return r.Trigger(ctx)
}

// Start launches the backup goroutine. It runs one pass immediately, so a
// server that is restarted daily still gets a fresh backup rather than
// waiting up to a full interval, then ticks every r.Every until ctx is
// cancelled.
//
// A pass that is still running when ctx is cancelled is allowed to finish:
// Trigger's context is ctx itself, so a cancellation-caused failure (the
// connection going away mid VACUUM INTO, for instance) is expected here, not
// an error worth logging, and Stop still joins the goroutine before
// returning so cmd/server's deferred store.Close cannot run out from under
// an in-flight pass.
func (r *Runner) Start(ctx context.Context) {
	r.wg.Go(func() {
		r.tick(ctx)
		ticker := time.NewTicker(r.Every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.tick(ctx)
			}
		}
	})
}

// tick runs one pass and logs its outcome. It recovers a panic from Trigger
// (ultimately from Source.BackupTo) into an error, the same protection
// jobs.Queue.runHandler gives job handlers, so one broken Source cannot take
// the whole process down.
func (r *Runner) tick(ctx context.Context) {
	dest, err := r.safeTrigger(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// Shutting down: ctx's own cancellation caused this failure
			// (or raced with it), not a broken backup. Exit quietly so a
			// clean shutdown never logs an ERROR.
			return
		}
		if errors.Is(err, ErrInProgress) {
			slog.Warn("backup: tick skipped, a run is already in progress")
			return
		}
		slog.Error("backup: nightly run failed", "err", err)
		return
	}
	slog.Info("backup: nightly run complete", "path", dest)
}

// Stop waits for the backup goroutine to finish. Cancel the context passed
// to Start first, or this blocks.
func (r *Runner) Stop() { r.wg.Wait() }
