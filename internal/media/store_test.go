package media_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"boobies-media/internal/config"
	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
	"boobies-media/internal/media"
)

// pngBytes is a minimal but structurally valid 8-bit RGB PNG.
var pngBytes = func() []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	buf.Write([]byte{0, 0, 0, 0x0d})
	buf.Write([]byte("IHDR"))
	buf.Write([]byte{0, 0, 0, 0x64, 0, 0, 0, 0x64, 8, 2, 0, 0, 0})
	buf.Write([]byte{0, 0, 0, 0})
	buf.Write([]byte{0, 0, 0, 4})
	buf.Write([]byte("IDAT"))
	buf.Write([]byte{0x78, 0x9c, 0x63, 0x00})
	buf.Write([]byte{0, 0, 0, 0})
	buf.Write([]byte{0, 0, 0, 0})
	buf.Write([]byte("IEND"))
	buf.Write([]byte{0, 0, 0, 0})
	return buf.Bytes()
}()

var mp4Bytes = append([]byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm',
	0, 0, 2, 0, 'i', 's', 'o', 'm'}, bytes.Repeat([]byte{0}, 200)...)

// gifBytes only needs to satisfy Sniff's magic-byte check: every test that
// touches ffmpeg (probing, thumbnailing) stubs the tool out rather than
// decoding real GIF frames, the same way mp4Bytes above is not a playable
// MP4 either.
var gifBytes = append([]byte("GIF89a"), bytes.Repeat([]byte{0}, 32)...)

// recordingQueue captures enqueued follow-up work.
type recordingQueue struct {
	types    []string
	payloads []any
}

func (q *recordingQueue) Enqueue(_ context.Context, jobType string, payload any) (int64, error) {
	q.types = append(q.types, jobType)
	q.payloads = append(q.payloads, payload)
	return int64(len(q.types)), nil
}

// failingQueue always fails Enqueue, so tests can drive Save's handling of a
// probe-enqueue failure.
type failingQueue struct {
	err error
}

func (q *failingQueue) Enqueue(context.Context, string, any) (int64, error) {
	return 0, q.err
}

// cancelingQueue reproduces an ordinary client disconnect landing exactly
// between CreateItem committing and Enqueue running: it cancels the caller's
// context itself and fails the way a real database call would once ctx is
// already done. This is Finding 2's regression fixture: Save is always
// invoked with the inbound request's context, so this is not a contrived
// double-coincidence, just the ordinary disconnect case.
type cancelingQueue struct {
	cancel context.CancelFunc
}

func (q *cancelingQueue) Enqueue(ctx context.Context, _ string, _ any) (int64, error) {
	q.cancel()
	return 0, ctx.Err()
}

// sha256Hex mirrors the hash Save computes over stored bytes, so tests can
// predict a blob's content-addressed path without reaching into the
// unexported hashFile.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// recordingHandler is a slog.Handler that captures records in memory so
// tests can assert a failure was actually logged, not just returned.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

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

func newMediaStore(t *testing.T) (*media.Store, *db.Store, *recordingQueue, *config.Config) {
	t.Helper()
	cfg, err := config.Load([]string{"-data", t.TempDir()}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	database := dbtest.New(t)
	queue := &recordingQueue{}
	return media.NewStore(cfg, database, queue), database, queue, cfg
}

func newUploader(t *testing.T, database *db.Store) *db.User {
	t.Helper()
	user, err := database.CreateUser(context.Background(), "aiden", "Aiden", "hash", "", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return user
}

func TestSaveStoresABlobAndCreatesAnItem(t *testing.T) {
	ctx := context.Background()
	store, database, queue, cfg := newMediaStore(t)
	user := newUploader(t, database)
	media.StubTools(t, map[string]string{}) // no cwebp: optimization degrades silently

	res, err := store.Save(ctx, media.SaveRequest{
		Reader:     bytes.NewReader(pngBytes),
		Filename:   "funny cat.png",
		UploaderID: user.ID,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if res.Item.Mime != "image/png" {
		t.Errorf("Mime = %q, want image/png", res.Item.Mime)
	}
	if res.Item.Ext != "png" {
		t.Errorf("Ext = %q, want png", res.Item.Ext)
	}
	if res.Item.Title != "funny cat" {
		t.Errorf("Title = %q, want the filename without its extension", res.Item.Title)
	}
	if res.Item.Size != int64(len(pngBytes)) {
		t.Errorf("Size = %d, want %d", res.Item.Size, len(pngBytes))
	}
	if res.Deduplicated {
		t.Error("Deduplicated = true for the first upload")
	}

	blob := media.BlobPath(cfg.FilesDir(), res.Item.ContentHash)
	stored, err := os.ReadFile(blob)
	if err != nil {
		t.Fatalf("read stored blob: %v", err)
	}
	if !bytes.Equal(stored, pngBytes) {
		t.Error("the stored bytes differ from the uploaded bytes")
	}

	// The item must be readable through the store immediately.
	if _, err := database.ItemByID(ctx, res.Item.ID); err != nil {
		t.Fatalf("ItemByID: %v", err)
	}

	if len(queue.types) != 1 || queue.types[0] != "probe" {
		t.Errorf("enqueued %v, want exactly one probe job", queue.types)
	}
}

func TestSaveWritesASelfDescribingSidecar(t *testing.T) {
	ctx := context.Background()
	store, database, _, cfg := newMediaStore(t)
	user := newUploader(t, database)
	media.StubTools(t, map[string]string{})

	res, err := store.Save(ctx, media.SaveRequest{
		Reader:     bytes.NewReader(pngBytes),
		Filename:   "cat.png",
		UploaderID: user.ID,
		SourceURL:  "https://example.com/cat.png",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(media.SidecarPath(cfg.FilesDir(), res.Item.ContentHash))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var sidecar media.Sidecar
	if err := json.Unmarshal(raw, &sidecar); err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}
	if sidecar.Mime != "image/png" || sidecar.Ext != "png" {
		t.Errorf("sidecar = %+v, want the stored mime and ext", sidecar)
	}
	if sidecar.Title != "cat" {
		t.Errorf("sidecar title = %q, want cat", sidecar.Title)
	}
	if sidecar.SourceURL != "https://example.com/cat.png" {
		t.Errorf("sidecar source = %q", sidecar.SourceURL)
	}
	if sidecar.Uploader != "aiden" {
		t.Errorf("sidecar uploader = %q, want aiden", sidecar.Uploader)
	}
}

func TestSaveDedupsIdenticalBytesToOneBlob(t *testing.T) {
	ctx := context.Background()
	store, database, _, cfg := newMediaStore(t)
	alice := newUploader(t, database)
	bob, err := database.CreateUser(ctx, "bob", "Bob", "hash", "", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	media.StubTools(t, map[string]string{})

	first, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(pngBytes), Filename: "a.png", UploaderID: alice.ID})
	if err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	second, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(pngBytes), Filename: "b.png", UploaderID: bob.ID})
	if err != nil {
		t.Fatalf("Save(second): %v", err)
	}

	if first.Item.ContentHash != second.Item.ContentHash {
		t.Fatal("identical bytes produced different content hashes")
	}
	if first.Item.ID == second.Item.ID {
		t.Fatal("the two uploads collapsed into one item; each upload keeps its own row")
	}
	if !second.Deduplicated {
		t.Error("Deduplicated = false for the second copy of the same bytes")
	}
	if _, err := os.Stat(media.BlobPath(cfg.FilesDir(), first.Item.ContentHash)); err != nil {
		t.Fatalf("the shared blob is missing: %v", err)
	}
	// Nothing should be left behind in the temp directory.
	entries, err := os.ReadDir(cfg.TmpDir())
	if err != nil {
		t.Fatalf("read tmp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("%d temp files leaked: %v", len(entries), entries)
	}
}

func TestSaveRejectsDisallowedTypes(t *testing.T) {
	ctx := context.Background()
	store, database, _, cfg := newMediaStore(t)
	user := newUploader(t, database)
	media.StubTools(t, map[string]string{})

	payloads := map[string][]byte{
		"svg":   []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(document.cookie)</script></svg>`),
		"html":  []byte("<!DOCTYPE html><html><body><script>fetch('/api/items')</script></body></html>"),
		"text":  []byte("this is definitely not media, just a plain text file with words"),
		"empty": {},
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			_, err := store.Save(ctx, media.SaveRequest{
				Reader: bytes.NewReader(payload), Filename: name + ".png", UploaderID: user.ID})
			if !errors.Is(err, media.ErrUnsupportedType) {
				t.Fatalf("Save(%s) = %v, want ErrUnsupportedType", name, err)
			}
		})
	}

	// Nothing may have been written anywhere.
	var files int
	_ = filepathWalkCount(cfg.FilesDir(), &files)
	if files != 0 {
		t.Errorf("%d files were written for rejected uploads", files)
	}
	items, _, err := database.ListItems(ctx, db.ItemQuery{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("%d item rows were created for rejected uploads", len(items))
	}
}

// filepathWalkCount counts regular files under root.
func filepathWalkCount(root string, count *int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err := filepathWalkCount(root+"/"+entry.Name(), count); err != nil {
				return err
			}
			continue
		}
		*count++
	}
	return nil
}

func TestSaveEnforcesTheSizeCap(t *testing.T) {
	ctx := context.Background()
	store, database, _, cfg := newMediaStore(t)
	user := newUploader(t, database)
	media.StubTools(t, map[string]string{})

	big := append(append([]byte{}, mp4Bytes...), bytes.Repeat([]byte{0x41}, 4096)...)
	_, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(big), Filename: "big.mp4", UploaderID: user.ID, MaxBytes: 1024})
	if !errors.Is(err, media.ErrTooLarge) {
		t.Fatalf("Save over the cap = %v, want ErrTooLarge", err)
	}
	entries, _ := os.ReadDir(cfg.TmpDir())
	if len(entries) != 0 {
		t.Errorf("%d temp files leaked after an oversize upload", len(entries))
	}
}

func TestSaveUsesTheUploadCapSetting(t *testing.T) {
	ctx := context.Background()
	store, database, _, _ := newMediaStore(t)
	user := newUploader(t, database)
	media.StubTools(t, map[string]string{})

	if err := database.SettingSet(ctx, "upload_max_bytes", "16"); err != nil {
		t.Fatalf("SettingSet: %v", err)
	}
	_, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(pngBytes), Filename: "cat.png", UploaderID: user.ID})
	if !errors.Is(err, media.ErrTooLarge) {
		t.Fatalf("Save = %v, want ErrTooLarge from the admin setting", err)
	}
}

func TestSaveOptimizesEligiblePNGAndHashesTheStoredBytes(t *testing.T) {
	ctx := context.Background()
	store, database, _, cfg := newMediaStore(t)
	user := newUploader(t, database)
	// database is used only to create the uploader above.

	// A cwebp stub that produces a much smaller "webp".
	media.StubTools(t, map[string]string{
		"cwebp": `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then shift; out="$1"; fi
  shift
done
printf 'RIFF\004\000\000\000WEBPVP8L!' > "$out"`,
	})

	res, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(pngBytes), Filename: "cat.png", UploaderID: user.ID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !res.Optimized {
		t.Fatal("Optimized = false although the stub produced a smaller file")
	}
	if res.Item.Mime != "image/webp" || res.Item.Ext != "webp" {
		t.Errorf("item = %s/%s, want image/webp and webp", res.Item.Mime, res.Item.Ext)
	}

	// The hash must cover the stored (converted) bytes, not the original.
	stored, err := os.ReadFile(media.BlobPath(cfg.FilesDir(), res.Item.ContentHash))
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if bytes.Equal(stored, pngBytes) {
		t.Error("the original PNG was stored despite a successful conversion")
	}
	if res.Item.Size != int64(len(stored)) {
		t.Errorf("Size = %d, want the stored length %d", res.Item.Size, len(stored))
	}
}

func TestSaveRespectsTheAutoWebpSetting(t *testing.T) {
	ctx := context.Background()
	store, database, _, _ := newMediaStore(t)
	user := newUploader(t, database)
	if err := database.SettingSet(ctx, "auto_webp", "off"); err != nil {
		t.Fatalf("SettingSet: %v", err)
	}
	media.StubTools(t, map[string]string{
		"cwebp": `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then shift; out="$1"; fi
  shift
done
printf 'RIFF!' > "$out"`,
	})

	res, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(pngBytes), Filename: "cat.png", UploaderID: user.ID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if res.Optimized || res.Item.Mime != "image/png" {
		t.Errorf("conversion happened with auto_webp off: %+v", res)
	}
}

func TestSaveCarriesSourceURLAndJobID(t *testing.T) {
	ctx := context.Background()
	store, database, _, _ := newMediaStore(t)
	user := newUploader(t, database)
	media.StubTools(t, map[string]string{})

	jobID, err := database.EnqueueJob(ctx, "ingest_url", []byte(`{}`), timeNowForTest())
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	res, err := store.Save(ctx, media.SaveRequest{
		Reader:     bytes.NewReader(mp4Bytes),
		Filename:   "clip.mp4",
		UploaderID: user.ID,
		SourceURL:  "https://medal.tv/clip/123",
		JobID:      jobID,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if res.Item.SourceURL != "https://medal.tv/clip/123" {
		t.Errorf("SourceURL = %q", res.Item.SourceURL)
	}
	if res.Item.JobID != jobID {
		t.Errorf("JobID = %d, want %d", res.Item.JobID, jobID)
	}
	linked, err := database.ItemsByJobID(ctx, jobID)
	if err != nil {
		t.Fatalf("ItemsByJobID: %v", err)
	}
	if len(linked) != 1 || linked[0].ID != res.Item.ID {
		t.Error("the item is not reachable from its ingest job")
	}
}

func TestSaveFilenameFallbacks(t *testing.T) {
	ctx := context.Background()
	store, database, _, _ := newMediaStore(t)
	user := newUploader(t, database)
	media.StubTools(t, map[string]string{})

	for _, filename := range []string{"", "   ", "../../etc/passwd"} {
		res, err := store.Save(ctx, media.SaveRequest{
			Reader: bytes.NewReader(mp4Bytes), Filename: filename, UploaderID: user.ID})
		if err != nil {
			t.Fatalf("Save(%q): %v", filename, err)
		}
		if strings.TrimSpace(res.Item.Title) == "" {
			t.Errorf("Save(%q) produced an empty title", filename)
		}
		if strings.Contains(res.Item.Title, "/") {
			t.Errorf("Save(%q) produced a title containing a path separator: %q", filename, res.Item.Title)
		}
	}
}

func TestPurgeUnlinksOnlyWhenNoItemRemains(t *testing.T) {
	ctx := context.Background()
	store, database, _, cfg := newMediaStore(t)
	alice := newUploader(t, database)
	bob, err := database.CreateUser(ctx, "bob", "Bob", "hash", "", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	media.StubTools(t, map[string]string{})

	first, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(pngBytes), Filename: "a.png", UploaderID: alice.ID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	second, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(pngBytes), Filename: "b.png", UploaderID: bob.ID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	blob := media.BlobPath(cfg.FilesDir(), first.Item.ContentHash)

	if err := store.Purge(ctx, first.Item.ID); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("the blob was unlinked while %s still referenced it: %v", second.Item.ID, err)
	}
	if _, err := database.ItemByID(ctx, second.Item.ID); err != nil {
		t.Fatalf("the surviving item broke: %v", err)
	}

	if err := store.Purge(ctx, second.Item.ID); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if _, err := os.Stat(blob); !os.IsNotExist(err) {
		t.Error("the blob survived the purge of its last referrer")
	}
	if _, err := os.Stat(media.SidecarPath(cfg.FilesDir(), first.Item.ContentHash)); !os.IsNotExist(err) {
		t.Error("the sidecar survived the purge of its last referrer")
	}
}

func timeNowForTest() time.Time { return time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC) }

func TestSaveLeavesNoOrphanBlobWhenTheUploaderLookupFails(t *testing.T) {
	ctx := context.Background()
	store, _, _, cfg := newMediaStore(t)
	media.StubTools(t, map[string]string{})

	_, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(pngBytes), Filename: "cat.png", UploaderID: 999999})
	if err == nil {
		t.Fatal("Save with an unknown uploader succeeded, want an error")
	}

	var files int
	_ = filepathWalkCount(cfg.FilesDir(), &files)
	if files != 0 {
		t.Errorf("%d orphan files remain under FilesDir after a failed save", files)
	}
	entries, err := os.ReadDir(cfg.TmpDir())
	if err != nil {
		t.Fatalf("read tmp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("%d temp files leaked after a failed save", len(entries))
	}
}

// TestSaveCleanupNeverUnlinksABlobAnotherLiveItemReferences deterministically
// reproduces the concurrent-upload hazard: two Save calls for byte-identical
// content can each have placeBlob's os.Stat miss the other's in-flight
// rename, so both locally believe deduplicated=false. If one of them fails a
// later step while the other has already committed its item row, a cleanup
// that trusts only its own local bool deletes the blob out from under the
// sibling's now-live item.
//
// Real racing goroutines can't force this deterministically: placeBlob's
// decision is disk-state-based (os.Stat), so a second, strictly-sequential
// Save always sees a blob a prior Save already placed and correctly reports
// deduplicated=true, which even the old code already handles safely. The
// dangerous branch only triggers when the *disk* has no blob yet at the
// moment cleanup's caller places one, while the *database* already has a
// live row for that hash -- exactly the state a true race produces (the
// "winner" may commit its item row before or after this call's rename lands,
// since the two are unsynchronized). So the setup below constructs that
// combination directly: a committed item row for the hash, with no blob
// file yet on disk. The second Save is then the one that actually places
// the bytes (deduplicated=false, honestly, from its own point of view) and
// then fails afterward -- reproducing exactly what a losing racer's cleanup
// would see.
func TestSaveCleanupNeverUnlinksABlobAnotherLiveItemReferences(t *testing.T) {
	ctx := context.Background()
	store, database, _, cfg := newMediaStore(t)
	media.StubTools(t, map[string]string{}) // no cwebp: hash covers the raw bytes
	alice := newUploader(t, database)

	hash := sha256Hex(pngBytes)

	// The concurrent sibling that already won: its item row is committed and
	// references this exact content hash, but (as in the race) nothing has
	// placed the blob on disk yet at this point.
	winner, err := database.CreateItem(ctx, db.NewItem{
		ContentHash: hash,
		Title:       "winner",
		Ext:         "png",
		Mime:        "image/png",
		Size:        int64(len(pngBytes)),
		UploaderID:  alice.ID,
	})
	if err != nil {
		t.Fatalf("CreateItem (winner): %v", err)
	}

	// The losing racer: places the blob for real (deduplicated=false, since
	// nothing was on disk yet), then fails at a later step. An unknown
	// uploader stands in for any of UserByID/writeSidecar/CreateItem
	// failing -- cleanupOrphanBlob fires the same way regardless of which
	// one trips.
	if _, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(pngBytes), Filename: "loser.png", UploaderID: 999999,
	}); err == nil {
		t.Fatal("Save with an unknown uploader succeeded, want an error")
	}

	blob := media.BlobPath(cfg.FilesDir(), hash)
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("cleanup deleted the blob that item %s still references: %v", winner.ID, err)
	}
	if _, err := database.ItemByID(ctx, winner.ID); err != nil {
		t.Fatalf("the surviving item broke: %v", err)
	}
}

// TestSaveRollsBackTheItemWhenProbeEnqueueFails covers Finding 2: when
// Queue.Enqueue fails after CreateItem has already committed, Save must not
// leave a permanently-unprobed item sitting silently in the library. The
// chosen contract is to roll the item back (via Purge, which is safe to
// reuse: it only unlinks the blob when the DB confirms nothing else
// references the hash) so a client's retry gets one clean row instead of a
// dead one next to a fresh one, and to log loudly so the failure isn't
// silent even though it's also returned.
func TestSaveRollsBackTheItemWhenProbeEnqueueFails(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.Load([]string{"-data", t.TempDir()}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	database := dbtest.New(t)
	queueErr := errors.New("queue unavailable")
	store := media.NewStore(cfg, database, &failingQueue{err: queueErr})
	user := newUploader(t, database)
	media.StubTools(t, map[string]string{})

	handler := &recordingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	_, err = store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(pngBytes), Filename: "cat.png", UploaderID: user.ID})
	if err == nil {
		t.Fatal("Save succeeded despite a failing probe enqueue, want an error")
	}
	if !errors.Is(err, queueErr) {
		t.Errorf("Save error = %v, want it to wrap the enqueue failure", err)
	}
	if !handler.hasLevel(slog.LevelError) {
		t.Error("no error-level log record was emitted for the failed enqueue; the failure must be loud, not just returned")
	}

	items, _, err := database.ListItems(ctx, db.ItemQuery{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("%d item rows survived a failed probe enqueue, want the item rolled back so a retry starts clean", len(items))
	}

	hash := sha256Hex(pngBytes)
	if _, err := os.Stat(media.BlobPath(cfg.FilesDir(), hash)); !os.IsNotExist(err) {
		t.Error("the blob survived a rolled-back item that had no other referrer")
	}
}

// TestSaveRollsBackTheItemEvenWhenTheRequestContextIsCancelled is Finding 2's
// regression test. Save is always called with the inbound request's context
// (r.Context()), so an ordinary client disconnect right after CreateItem
// commits cancels ctx at exactly the moment Enqueue observes it -- the
// rollback Purge must not reuse that same cancelled context, or the
// disconnect takes the rollback down with it and leaves a permanently
// unprobed item behind. cancelingQueue reproduces that timing deterministically:
// it cancels the context itself, from inside Enqueue, the same instant a real
// disconnect would.
func TestSaveRollsBackTheItemEvenWhenTheRequestContextIsCancelled(t *testing.T) {
	cfg, err := config.Load([]string{"-data", t.TempDir()}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	database := dbtest.New(t)
	user := newUploader(t, database)
	media.StubTools(t, map[string]string{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := media.NewStore(cfg, database, &cancelingQueue{cancel: cancel})

	_, err = store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(pngBytes), Filename: "cat.png", UploaderID: user.ID})
	if err == nil {
		t.Fatal("Save succeeded despite a failing probe enqueue, want an error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Save error = %v, want it to wrap context.Canceled", err)
	}

	// Assertions run on a fresh, uncancelled context: ctx above is the
	// (cancelled) request context, not something the verification queries
	// should inherit.
	bg := context.Background()
	items, _, err := database.ListItems(bg, db.ItemQuery{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("%d item rows survived a probe enqueue that failed via context cancellation, want the item rolled back", len(items))
	}

	hash := sha256Hex(pngBytes)
	if _, err := os.Stat(media.BlobPath(cfg.FilesDir(), hash)); !os.IsNotExist(err) {
		t.Error("the blob survived a rolled-back item that had no other referrer")
	}
}
