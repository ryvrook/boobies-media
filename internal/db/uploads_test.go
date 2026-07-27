package db_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

func newUpload(t *testing.T, store *db.Store, userID int64, size, chunk int64) *db.Upload {
	t.Helper()
	up, err := store.CreateUpload(context.Background(), db.NewUpload{
		UserID:       userID,
		Filename:     "clip.mp4",
		DeclaredSize: size,
		ChunkSize:    chunk,
		TempDir:      t.TempDir(),
		ExpiresAt:    time.Now().UTC().Add(6 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	return up
}

func TestCreateUploadAssignsATokenAndNoChunks(t *testing.T) {
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)

	up := newUpload(t, store, user.ID, 30, 12)
	if len(up.ID) != 8 {
		t.Errorf("id = %q, want an 8-character token", up.ID)
	}
	if len(up.Received) != 0 {
		t.Errorf("Received = %v, want empty", up.Received)
	}
	if up.ChunkCount() != 3 {
		t.Errorf("ChunkCount = %d, want 3 (30 bytes at 12 per chunk)", up.ChunkCount())
	}
	if up.IsComplete() {
		t.Error("a fresh upload reports complete")
	}
}

func TestRecordChunkIsIdempotentAndTracksGaps(t *testing.T) {
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	up := newUpload(t, store, user.ID, 30, 12)
	ctx := context.Background()
	exp := time.Now().UTC().Add(time.Hour)

	if _, err := store.RecordChunk(ctx, up.ID, 0, exp); err != nil {
		t.Fatalf("RecordChunk(0): %v", err)
	}
	// Out of order, and with a gap at index 1.
	if _, err := store.RecordChunk(ctx, up.ID, 2, exp); err != nil {
		t.Fatalf("RecordChunk(2): %v", err)
	}
	// The retry a flaky connection produces.
	got, err := store.RecordChunk(ctx, up.ID, 0, exp)
	if err != nil {
		t.Fatalf("re-recording chunk 0 must be a no-op, got %v", err)
	}

	if want := []int{0, 2}; !equalInts(got.Received, want) {
		t.Errorf("Received = %v, want %v (sorted, deduplicated)", got.Received, want)
	}
	if want := []int{1}; !equalInts(got.Missing(), want) {
		t.Errorf("Missing = %v, want %v", got.Missing(), want)
	}
	if got.IsComplete() {
		t.Error("IsComplete with a gap at index 1")
	}

	if _, err := store.RecordChunk(ctx, up.ID, 1, exp); err != nil {
		t.Fatalf("RecordChunk(1): %v", err)
	}
	final, err := store.UploadByID(ctx, up.ID)
	if err != nil {
		t.Fatalf("UploadByID: %v", err)
	}
	if !final.IsComplete() {
		t.Errorf("IsComplete = false with Received = %v", final.Received)
	}
}

func TestRecordChunkRejectsIndexOutsideRange(t *testing.T) {
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	up := newUpload(t, store, user.ID, 30, 12) // 3 chunks: indices 0, 1, 2
	ctx := context.Background()
	exp := time.Now().UTC().Add(time.Hour)

	if _, err := store.RecordChunk(ctx, up.ID, 3, exp); err == nil {
		t.Error("RecordChunk(3) on a 3-chunk upload succeeded, want an error")
	}
	if _, err := store.RecordChunk(ctx, up.ID, -1, exp); err == nil {
		t.Error("RecordChunk(-1) succeeded, want an error")
	}
	// Rejected chunks must not be recorded.
	got, err := store.UploadByID(ctx, up.ID)
	if err != nil {
		t.Fatalf("UploadByID: %v", err)
	}
	if len(got.Received) != 0 {
		t.Errorf("Received = %v after rejected indices, want empty", got.Received)
	}
}

// TestRecordChunkConcurrentWritesDoNotCorruptReceived guards the read-modify-
// write of the received set: two chunks landing at the same time (a client
// uploading several in parallel, or two retries racing) must not let one
// call's UPDATE clobber the other's with a stale copy of the set.
func TestRecordChunkConcurrentWritesDoNotCorruptReceived(t *testing.T) {
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	const chunks = 10
	up := newUpload(t, store, user.ID, chunks*10, 10) // exactly `chunks` chunks
	ctx := context.Background()
	exp := time.Now().UTC().Add(time.Hour)

	var wg sync.WaitGroup
	errs := make([]error, chunks)
	for i := 0; i < chunks; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := store.RecordChunk(ctx, up.ID, index, exp)
			errs[index] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("RecordChunk(%d): %v", i, err)
		}
	}

	final, err := store.UploadByID(ctx, up.ID)
	if err != nil {
		t.Fatalf("UploadByID: %v", err)
	}
	if len(final.Received) != chunks {
		t.Fatalf("Received = %v (len %d), want all %d indices with none lost or duplicated", final.Received, len(final.Received), chunks)
	}
	if !final.IsComplete() {
		t.Errorf("IsComplete = false with Received = %v", final.Received)
	}
}

// TestRecordChunkPushesExpiresAtForward guards the fix for the janitor
// reaping an actively-uploading file: expires_at must move forward on every
// chunk, including a retried one, so the TTL measures inactivity rather than
// wall-clock time since creation.
func TestRecordChunkPushesExpiresAtForward(t *testing.T) {
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	ctx := context.Background()
	// expires_at round-trips through the DB as RFC3339 (whole seconds), so
	// the expiry values compared below are truncated to the second up front
	// rather than tripping over sub-second precision the store never kept.
	now := time.Now().UTC().Truncate(time.Second)

	up, err := store.CreateUpload(ctx, db.NewUpload{
		UserID: user.ID, Filename: "clip.mp4", DeclaredSize: 30, ChunkSize: 12,
		TempDir: t.TempDir(), ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	pushed := now.Add(time.Hour)
	got, err := store.RecordChunk(ctx, up.ID, 0, pushed)
	if err != nil {
		t.Fatalf("RecordChunk: %v", err)
	}
	if !got.ExpiresAt.Equal(pushed) {
		t.Errorf("ExpiresAt = %v, want %v (RecordChunk must push the deadline forward)", got.ExpiresAt, pushed)
	}
	reloaded, err := store.UploadByID(ctx, up.ID)
	if err != nil {
		t.Fatalf("UploadByID: %v", err)
	}
	if !reloaded.ExpiresAt.Equal(pushed) {
		t.Errorf("persisted ExpiresAt = %v, want %v (the push must be committed, not just returned)", reloaded.ExpiresAt, pushed)
	}

	// A retry of an already-received chunk is still evidence the client is
	// alive, so it must push the deadline again rather than being a total
	// no-op.
	pushedAgain := pushed.Add(time.Hour)
	got2, err := store.RecordChunk(ctx, up.ID, 0, pushedAgain)
	if err != nil {
		t.Fatalf("RecordChunk (retry): %v", err)
	}
	if !got2.ExpiresAt.Equal(pushedAgain) {
		t.Errorf("ExpiresAt after retried chunk = %v, want %v", got2.ExpiresAt, pushedAgain)
	}
	if want := []int{0}; !equalInts(got2.Received, want) {
		t.Errorf("Received after retry = %v, want %v (a retry must stay idempotent)", got2.Received, want)
	}
}

func TestUploadByIDIsNotFoundAfterDelete(t *testing.T) {
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	up := newUpload(t, store, user.ID, 30, 12)
	ctx := context.Background()

	if err := store.DeleteUpload(ctx, up.ID); err != nil {
		t.Fatalf("DeleteUpload: %v", err)
	}
	if _, err := store.UploadByID(ctx, up.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("UploadByID after delete = %v, want ErrNotFound", err)
	}
}

func TestExpiredUploadsOnlyReturnsThePast(t *testing.T) {
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	ctx := context.Background()
	now := time.Now().UTC()

	live := newUpload(t, store, user.ID, 30, 12)
	stale, err := store.CreateUpload(ctx, db.NewUpload{
		UserID: user.ID, Filename: "old.mp4", DeclaredSize: 30, ChunkSize: 12,
		TempDir: t.TempDir(), ExpiresAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateUpload(stale): %v", err)
	}

	expired, err := store.ExpiredUploads(ctx, now)
	if err != nil {
		t.Fatalf("ExpiredUploads: %v", err)
	}
	if len(expired) != 1 || expired[0].ID != stale.ID {
		t.Fatalf("ExpiredUploads = %+v, want only %s", expired, stale.ID)
	}
	if expired[0].TempDir == "" {
		t.Error("the janitor cannot delete bytes it is not told about; TempDir is empty")
	}
	if _, err := store.UploadByID(ctx, live.ID); err != nil {
		t.Errorf("the live upload was disturbed: %v", err)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
