package backup_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"boobies-media/internal/backup"
	"boobies-media/internal/db"
)

func openStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestRunOnceWritesADatedBackup(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)

	dest, err := backup.RunOnce(context.Background(), store, dir, now, 7)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if filepath.Base(dest) != "media-2026-07-24.db" {
		t.Errorf("backup name = %q, want media-2026-07-24.db", filepath.Base(dest))
	}
}

func TestRunOnceKeepsOnlyTheNewestN(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := openStore(t)
	base := time.Date(2026, 7, 1, 3, 0, 0, 0, time.UTC)

	for i := 0; i < 9; i++ {
		if _, err := backup.RunOnce(ctx, store, dir, base.AddDate(0, 0, i), 7); err != nil {
			t.Fatalf("RunOnce day %d: %v", i, err)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "media-*.db"))
	if len(matches) != 7 {
		t.Fatalf("kept %d backups, want 7", len(matches))
	}
	sort.Strings(matches)
	if filepath.Base(matches[0]) != "media-2026-07-03.db" {
		t.Errorf("oldest kept = %q, want media-2026-07-03.db (07-01 and 07-02 pruned)", filepath.Base(matches[0]))
	}
}

// TestRunOnceExactlyRetainCountPrunesNothing is the boundary case: with
// exactly retain backups on disk, the newest run must not prune any of them
// away. A fencepost error here (off by one in either direction) would either
// delete a backup that should have survived or let the count creep past
// retain forever.
func TestRunOnceExactlyRetainCountPrunesNothing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := openStore(t)
	base := time.Date(2026, 7, 1, 3, 0, 0, 0, time.UTC)

	const retain = 7
	for i := 0; i < retain; i++ {
		if _, err := backup.RunOnce(ctx, store, dir, base.AddDate(0, 0, i), retain); err != nil {
			t.Fatalf("RunOnce day %d: %v", i, err)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "media-*.db"))
	if len(matches) != retain {
		t.Fatalf("kept %d backups, want %d (exactly retain must not prune)", len(matches), retain)
	}
	sort.Strings(matches)
	if filepath.Base(matches[0]) != "media-2026-07-01.db" {
		t.Errorf("oldest kept = %q, want media-2026-07-01.db (nothing should have been pruned)", filepath.Base(matches[0]))
	}
}

func TestRunOnceSameDayDoesNotError(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := openStore(t)
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)

	if _, err := backup.RunOnce(ctx, store, dir, now, 7); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	if _, err := backup.RunOnce(ctx, store, dir, now, 7); err != nil {
		t.Fatalf("same-day RunOnce should overwrite, got: %v", err)
	}
}

// TestRunOnceUnwritableDirReturnsError is one of the two required failure
// paths: a backup directory that cannot be written to must surface a clear
// error, not a panic or a silently-empty backup.
func TestRunOnceUnwritableDirReturnsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, permission bits are not enforced")
	}
	ctx := context.Background()
	dir := t.TempDir()
	store := openStore(t)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	if _, err := backup.RunOnce(ctx, store, dir, now, 7); err == nil {
		t.Fatal("RunOnce into an unwritable directory: want error, got nil")
	}
}

// failingSource always fails, simulating a VACUUM INTO interrupted by a
// crash, a cancelled context, or a full disk partway through.
type failingSource struct{}

func (failingSource) BackupTo(ctx context.Context, path string) error {
	return errors.New("simulated interrupted backup")
}

// TestRunOnceInterruptedBackupLeavesNoCorruptFile is the regression test for
// the review's Critical 1: a failed BackupTo must not leave anything behind
// that a later RunOnce's retention count, or an operator restoring from the
// directory, would mistake for a valid backup.
func TestRunOnceInterruptedBackupLeavesNoCorruptFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)

	if _, err := backup.RunOnce(ctx, failingSource{}, dir, now, 7); err == nil {
		t.Fatal("RunOnce with a failing source: want error, got nil")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("backup dir has %d entries after a failed run, want 0: %v", len(entries), entries)
	}

	// A subsequent successful run must not be blocked by anything the failed
	// attempt left behind.
	store := openStore(t)
	dest, err := backup.RunOnce(ctx, store, dir, now, 7)
	if err != nil {
		t.Fatalf("RunOnce after a prior failure: %v", err)
	}
	if filepath.Base(dest) != "media-2026-07-24.db" {
		t.Errorf("backup name = %q, want media-2026-07-24.db", filepath.Base(dest))
	}
}

// TestRunOnceClearsStaleTempFileFromACrashedRun proves the same claim from
// the other direction: a leftover temp file from a run that never got to
// clean up after itself (a hard crash, not just a returned error) must not
// block, or be miscounted by, the next run.
func TestRunOnceClearsStaleTempFileFromACrashedRun(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := openStore(t)
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)

	stale := filepath.Join(dir, ".media-backup.db.tmp")
	if err := os.WriteFile(stale, []byte("leftover from a crashed run"), 0o600); err != nil {
		t.Fatalf("seed stale temp file: %v", err)
	}

	dest, err := backup.RunOnce(ctx, store, dir, now, 7)
	if err != nil {
		t.Fatalf("RunOnce with a stale temp file present: %v", err)
	}
	if filepath.Base(dest) != "media-2026-07-24.db" {
		t.Errorf("backup name = %q, want media-2026-07-24.db", filepath.Base(dest))
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the stale temp file survived a successful run")
	}
}

// TestPruneIgnoresFilesNotMatchingTheDatedPattern is the regression test for
// the review's Important 4: a file that merely looks like a backup by prefix
// and suffix, but was not written by this package, must never be counted
// toward retain and must never be pruned.
func TestPruneIgnoresFilesNotMatchingTheDatedPattern(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := openStore(t)
	base := time.Date(2026, 7, 1, 3, 0, 0, 0, time.UTC)

	handPlaced := filepath.Join(dir, "media-pre-migration-snapshot.db")
	if err := os.WriteFile(handPlaced, []byte("not written by RunOnce"), 0o600); err != nil {
		t.Fatalf("seed hand-placed file: %v", err)
	}
	unrelated := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(unrelated, []byte("also not a backup"), 0o600); err != nil {
		t.Fatalf("seed unrelated file: %v", err)
	}

	for i := 0; i < 9; i++ {
		if _, err := backup.RunOnce(ctx, store, dir, base.AddDate(0, 0, i), 7); err != nil {
			t.Fatalf("RunOnce day %d: %v", i, err)
		}
	}

	if _, err := os.Stat(handPlaced); err != nil {
		t.Errorf("the hand-placed file was removed by pruning: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("the unrelated file was removed by pruning: %v", err)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "media-????-??-??.db"))
	if len(matches) != 7 {
		t.Fatalf("kept %d dated backups, want 7 (the hand-placed file must not steal a slot)", len(matches))
	}
	sort.Strings(matches)
	if filepath.Base(matches[0]) != "media-2026-07-03.db" {
		t.Errorf("oldest kept = %q, want media-2026-07-03.db", filepath.Base(matches[0]))
	}
}

// blockingSource lets a test control exactly when BackupTo starts and
// finishes, so concurrency behaviour can be asserted without sleeping or
// polling for a timer. started and proceed may be swapped out by the test
// between calls, so BackupTo reads them under a lock instead of closing a
// channel it does not own the lifetime of.
//
// Once unblocked, it checks ctx the same way the real db.Store.BackupTo
// would (ExecContext refuses to even acquire the connection once ctx is
// done): if ctx was cancelled while the call was in flight, it returns
// ctx.Err() instead of succeeding, so tests can exercise the shutdown race
// tick's ctx.Err() guard exists for.
type blockingSource struct {
	mu      sync.Mutex
	started chan struct{}
	proceed chan struct{}
}

func (s *blockingSource) BackupTo(ctx context.Context, path string) error {
	s.mu.Lock()
	started, proceed := s.started, s.proceed
	s.mu.Unlock()

	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	<-proceed
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("db"), 0o600)
}

// TestRunnerTriggerRejectsConcurrentRun is the second required failure path:
// a backup attempted while another is in progress must fail clearly instead
// of racing the first run for the same destination file.
func TestRunnerTriggerRejectsConcurrentRun(t *testing.T) {
	dir := t.TempDir()
	src := &blockingSource{started: make(chan struct{}, 1), proceed: make(chan struct{})}
	r := backup.NewRunner(src, dir, 7, time.Hour)
	r.Now = func() time.Time { return time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC) }

	firstErr := make(chan error, 1)
	go func() {
		_, err := r.Trigger(context.Background())
		firstErr <- err
	}()

	<-src.started // the first run has entered BackupTo and is holding the lock

	if _, err := r.Trigger(context.Background()); !errors.Is(err, backup.ErrInProgress) {
		t.Fatalf("second Trigger while first is in flight: got %v, want ErrInProgress", err)
	}

	close(src.proceed) // let the first run finish
	if err := <-firstErr; err != nil {
		t.Fatalf("first Trigger: %v", err)
	}

	// Now that the first run has released the lock, a third call must
	// succeed rather than being permanently wedged as "in progress".
	src.proceed = make(chan struct{})
	close(src.proceed)
	if _, err := r.Trigger(context.Background()); err != nil {
		t.Fatalf("Trigger after the lock was released: %v", err)
	}
}

// TestRunnerStopWaitsForInFlightRun is the regression the upload janitor
// already guards against (see web/janitor_test.go): Stop must block until an
// in-flight pass has actually finished, not just until ctx is cancelled, or
// cmd/server's deferred store.Close could run out from under a backup still
// writing to the database handle.
func TestRunnerStopWaitsForInFlightRun(t *testing.T) {
	const delay = 150 * time.Millisecond
	src := &blockingSource{started: make(chan struct{}, 1), proceed: make(chan struct{})}
	r := backup.NewRunner(src, t.TempDir(), 7, time.Hour)
	r.Now = func() time.Time { return time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC) }

	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx) // runs one pass immediately, which blocks in src.BackupTo
	<-src.started

	cancel() // ask the runner to stop once its in-flight pass finishes
	time.AfterFunc(delay, func() { close(src.proceed) })

	start := time.Now()
	r.Stop()
	elapsed := time.Since(start)

	if elapsed < delay {
		t.Errorf("Stop returned after %v, want it to block at least %v for the in-flight pass to finish", elapsed, delay)
	}
}

// recordingHandler is a minimal slog.Handler that captures every record it
// receives, so a test can assert on what was actually logged rather than
// asserting on a return value that holds whether or not the code under test
// is correct.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *recordingHandler) hasLevel(level slog.Level) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level == level {
			return true
		}
	}
	return false
}

// TestRunnerCleanShutdownLogsNoError pins the bug this codebase has already
// fixed twice (see jobs.outcomeContext and media.Store's rollbackContext): a
// context cancelled at shutdown must not make an in-flight backup log an
// ERROR. It reproduces the real race: Start's immediate pass is blocked
// inside Source.BackupTo, the caller cancels ctx (the same ctx Trigger was
// given), and only then does BackupTo return -- observing the cancellation
// and failing, exactly as the real db.Store.BackupTo would if ExecContext's
// connection went away mid VACUUM INTO. tick's ctx.Err() guard is what must
// turn that failure into silence; this test asserts on the actual log
// output, not on a value that would be identical whether or not the guard
// exists.
func TestRunnerCleanShutdownLogsNoError(t *testing.T) {
	src := &blockingSource{started: make(chan struct{}, 1), proceed: make(chan struct{})}
	r := backup.NewRunner(src, t.TempDir(), 7, time.Hour)
	r.Now = func() time.Time { return time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC) }

	h := &recordingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx) // the immediate first pass blocks inside src.BackupTo
	<-src.started

	cancel()           // a shutdown races the in-flight pass
	close(src.proceed) // let BackupTo observe the now-cancelled ctx and fail
	r.Stop()           // joins the goroutine; tick has already logged (or not) by the time this returns

	if h.hasLevel(slog.LevelError) {
		t.Errorf("a shutdown that races an in-flight backup logged an ERROR record: %+v", h.records)
	}
}
