package web

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"boobies-media/internal/db"
)

// TestStopUploadJanitorWaitsForTheGoroutineToExit is Finding 3's regression
// test: unlike jobs.Queue.Stop (which blocks on wg.Wait for every worker),
// nothing previously waited for StartUploadJanitor's goroutine before
// cmd/server/main.go's deferred store.Close() ran. Reproduce that by making a
// single pass slow (via srv.Now, which ReapUploads calls first) and asserting
// StopUploadJanitor does not return before that slow pass has actually
// finished -- if it did, cmd/server could close the database out from under
// an in-flight sweep.
func TestStopUploadJanitorWaitsForTheGoroutineToExit(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const delay = 150 * time.Millisecond
	srv.Now = func() time.Time {
		time.Sleep(delay)
		return time.Now().UTC()
	}

	srv.StartUploadJanitor(ctx, time.Hour)
	cancel() // ask the janitor to stop once its in-flight (slow) pass finishes

	start := time.Now()
	srv.StopUploadJanitor()
	elapsed := time.Since(start)

	if elapsed < delay {
		t.Errorf("StopUploadJanitor returned after %v, want it to block at least %v for the in-flight pass to finish", elapsed, delay)
	}
}

// TestReapOnceLogsNoErrorWhenShutdownRacesAReap is the regression test for
// the same bug class Task 12's backup.Runner.tick already guards against
// (see internal/backup): a shutdown that cancels ctx while a reap pass is
// in flight must not log an ERROR. ctx is cancelled before reapOnce runs, so
// database/sql refuses to even acquire the store's connection for
// ExpiredUploads' query and reapOnce sees exactly the failure a real
// shutdown race produces -- no goroutine, ticker, or sleep needed to
// reproduce it, since database/sql's own cancellation check makes this
// deterministic.
func TestReapOnceLogsNoErrorWhenShutdownRacesAReap(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	h := &jobErrorRecordingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv.reapOnce(ctx)

	if h.hasRecordContaining(slog.LevelError, "") {
		t.Errorf("a shutdown that races an in-flight reap logged an ERROR record: %+v", h.records)
	}
}

// TestReapOnceLogsErrorOnAGenuineFailure is the flip side, and the specific
// risk the fix above introduces if done carelessly: suppressing every
// cancellation-flavoured error must not suppress a real one too. ctx is
// never cancelled here; the store's connection is closed directly instead,
// so ExpiredUploads fails for a reason that has nothing to do with
// shutdown, and reapOnce's ctx.Err() guard must not swallow it.
func TestReapOnceLogsErrorOnAGenuineFailure(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	ctx := context.Background()
	if err := srv.Store.DB.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	h := &jobErrorRecordingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv.reapOnce(ctx)

	if !h.hasRecordContaining(slog.LevelError, "reaping uploads") {
		t.Error("a genuine reap failure unrelated to shutdown did not log an ERROR record")
	}
}

func TestReapUploadsDeletesExpiredRowsAndBytes(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	user := testUser(t, srv, "aiden", "hunter2")
	ctx := context.Background()
	now := time.Now().UTC()
	srv.Now = func() time.Time { return now }

	staleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staleDir, "0.part"), []byte("half a video"), 0o600); err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
	stale, err := srv.Store.CreateUpload(ctx, db.NewUpload{
		UserID: user.ID, Filename: "abandoned.mp4", DeclaredSize: 100, ChunkSize: 50,
		TempDir: staleDir, ExpiresAt: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateUpload(stale): %v", err)
	}
	liveDir := t.TempDir()
	live, err := srv.Store.CreateUpload(ctx, db.NewUpload{
		UserID: user.ID, Filename: "in-flight.mp4", DeclaredSize: 100, ChunkSize: 50,
		TempDir: liveDir, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateUpload(live): %v", err)
	}

	reaped, err := srv.ReapUploads(ctx)
	if err != nil {
		t.Fatalf("ReapUploads: %v", err)
	}
	if reaped != 1 {
		t.Errorf("reaped = %d, want 1", reaped)
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Error("the abandoned upload's bytes are still on disk")
	}
	if _, err := srv.Store.UploadByID(ctx, stale.ID); err == nil {
		t.Error("the abandoned upload's row survived")
	}
	if _, err := srv.Store.UploadByID(ctx, live.ID); err != nil {
		t.Errorf("the in-flight upload was reaped: %v", err)
	}
	if _, err := os.Stat(liveDir); err != nil {
		t.Errorf("the in-flight upload's bytes were deleted: %v", err)
	}
}

// TestReapUploadsSkipsAnUploadKeptAliveByChunks is the regression test for the
// cross-task gap Task 17's reviewer found: expires_at was fixed at creation
// and RecordChunk never moved it, so a large upload over a slow connection
// could cross its deadline while chunks were still actively arriving and get
// reaped mid-flight. RecordChunk now pushes expires_at forward on every
// chunk, so this upload must still be present once the janitor runs past its
// *original* deadline.
func TestReapUploadsSkipsAnUploadKeptAliveByChunks(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	user := testUser(t, srv, "aiden", "hunter2")
	ctx := context.Background()
	t0 := time.Now().UTC()
	srv.Now = func() time.Time { return t0 }

	dir := t.TempDir()
	up, err := srv.Store.CreateUpload(ctx, db.NewUpload{
		UserID: user.ID, Filename: "slow.mp4", DeclaredSize: 100, ChunkSize: 50,
		TempDir: dir, ExpiresAt: t0.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// A chunk lands shortly before the original deadline and pushes
	// expires_at forward by a fresh TTL, exactly like handleUploadChunk does
	// on every chunk it stores.
	srv.Now = func() time.Time { return t0.Add(55 * time.Minute) }
	if _, err := srv.Store.RecordChunk(ctx, up.ID, 0, srv.Now().Add(time.Hour)); err != nil {
		t.Fatalf("RecordChunk: %v", err)
	}

	// The janitor runs after the ORIGINAL deadline (t0+1h) has passed. A
	// pre-fix store would reap this upload here even though a chunk landed
	// only 35 minutes earlier.
	srv.Now = func() time.Time { return t0.Add(90 * time.Minute) }
	reaped, err := srv.ReapUploads(ctx)
	if err != nil {
		t.Fatalf("ReapUploads: %v", err)
	}
	if reaped != 0 {
		t.Errorf("reaped = %d, want 0 (a chunk landed 35 minutes ago; the upload is active, not idle)", reaped)
	}
	if _, err := srv.Store.UploadByID(ctx, up.ID); err != nil {
		t.Errorf("the actively-uploading file was reaped: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the actively-uploading file's bytes were deleted: %v", err)
	}
}

// TestReapUploadsReapsAnUploadThatWentIdleAfterAChunk confirms the flip side
// of the fix above: pushing expires_at on chunk arrival must not make an
// upload immortal. Once no further chunks arrive and the pushed deadline
// itself passes, the janitor must still reap it.
func TestReapUploadsReapsAnUploadThatWentIdleAfterAChunk(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	user := testUser(t, srv, "aiden", "hunter2")
	ctx := context.Background()
	t0 := time.Now().UTC()
	srv.Now = func() time.Time { return t0 }

	dir := t.TempDir()
	up, err := srv.Store.CreateUpload(ctx, db.NewUpload{
		UserID: user.ID, Filename: "abandoned-midway.mp4", DeclaredSize: 100, ChunkSize: 50,
		TempDir: dir, ExpiresAt: t0.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// One chunk lands, pushing expires_at out to t0+2h ...
	if _, err := srv.Store.RecordChunk(ctx, up.ID, 0, t0.Add(2*time.Hour)); err != nil {
		t.Fatalf("RecordChunk: %v", err)
	}

	// ... but the client then disappears. No further chunks arrive, so once
	// the pushed deadline itself passes, the janitor must still reap it.
	srv.Now = func() time.Time { return t0.Add(3 * time.Hour) }
	reaped, err := srv.ReapUploads(ctx)
	if err != nil {
		t.Fatalf("ReapUploads: %v", err)
	}
	if reaped != 1 {
		t.Errorf("reaped = %d, want 1 (no chunk arrived in the 2 hours since the last one)", reaped)
	}
	if _, err := srv.Store.UploadByID(ctx, up.ID); err == nil {
		t.Error("the idle-after-one-chunk upload's row survived")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("the idle-after-one-chunk upload's bytes are still on disk")
	}
}
