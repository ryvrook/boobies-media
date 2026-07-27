package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"boobies-media/internal/db"
	"boobies-media/internal/media"
)

type uploadInitResponse struct {
	UploadID  string `json:"upload_id"`
	ChunkSize int64  `json:"chunk_size"`
	Size      int64  `json:"size"`
	Received  []int  `json:"received"`
	Missing   []int  `json:"missing"`
}

// initUpload opens an upload and returns the decoded response.
func initUpload(t *testing.T, srv *Server, cookie *http.Cookie, filename string, size int) uploadInitResponse {
	t.Helper()
	body := fmt.Sprintf(`{"filename":%q,"size":%d}`, filename, size)
	req := httptest.NewRequest(http.MethodPost, "/api/uploads", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/uploads = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	var out uploadInitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode init response %q: %v", rec.Body.String(), err)
	}
	return out
}

// putChunk sends one chunk and returns the recorder.
func putChunk(t *testing.T, srv *Server, cookie *http.Cookie, id string, index int, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/uploads/%s/%d", id, index), bytes.NewReader(payload))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func completeUpload(t *testing.T, srv *Server, cookie *http.Cookie, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/uploads/"+id+"/complete", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// chunkedTestServer forces a tiny chunk size so a small PNG spans several
// chunks without needing megabytes of fixture.
func chunkedTestServer(t *testing.T, chunkSize int) (*Server, *http.Cookie) {
	t.Helper()
	srv, _, _ := mediaTestServer(t)
	if err := srv.Store.SettingSet(context.Background(), "upload_chunk_bytes", fmt.Sprint(chunkSize)); err != nil {
		t.Fatalf("SettingSet: %v", err)
	}
	return srv, authenticate(t, srv, "aiden")
}

func TestChunkedUploadResumesAfterAGap(t *testing.T) {
	const chunk = 16
	srv, cookie := chunkedTestServer(t, chunk)

	init := initUpload(t, srv, cookie, "cat.png", len(pngTestBytes))
	if init.ChunkSize != chunk {
		t.Fatalf("chunk_size = %d, want %d", init.ChunkSize, chunk)
	}
	total := (len(pngTestBytes) + chunk - 1) / chunk
	if len(init.Missing) != total {
		t.Fatalf("missing = %v, want all %d chunks", init.Missing, total)
	}

	// Send every chunk except the second: the interruption.
	for i := 0; i < total; i++ {
		if i == 1 {
			continue
		}
		if rec := putChunk(t, srv, cookie, init.UploadID, i, chunkOf(pngTestBytes, i, chunk)); rec.Code != http.StatusNoContent {
			t.Fatalf("PUT chunk %d = %d (body: %s)", i, rec.Code, rec.Body.String())
		}
	}

	// Completing with a hole must fail loudly rather than store a torn file.
	if rec := completeUpload(t, srv, cookie, init.UploadID); rec.Code != http.StatusConflict {
		t.Fatalf("complete with a missing chunk = %d, want 409", rec.Code)
	}

	// The resume handshake: ask what is missing.
	req := httptest.NewRequest(http.MethodGet, "/api/uploads/"+init.UploadID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/uploads/{id} = %d", rec.Code)
	}
	var status uploadInitResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if len(status.Missing) != 1 || status.Missing[0] != 1 {
		t.Fatalf("missing = %v, want [1]", status.Missing)
	}

	// Send the gap, then re-send a chunk the server already has: both fine.
	if rec := putChunk(t, srv, cookie, init.UploadID, 1, chunkOf(pngTestBytes, 1, chunk)); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT the missing chunk = %d", rec.Code)
	}
	if rec := putChunk(t, srv, cookie, init.UploadID, 0, chunkOf(pngTestBytes, 0, chunk)); rec.Code != http.StatusNoContent {
		t.Fatalf("re-sending a stored chunk = %d, want 204 (the retry must be safe)", rec.Code)
	}

	done := completeUpload(t, srv, cookie, init.UploadID)
	if done.Code != http.StatusCreated {
		t.Fatalf("complete = %d, want 201 (body: %s)", done.Code, done.Body.String())
	}
	var body struct {
		Item struct {
			ID   string `json:"id"`
			Mime string `json:"mime"`
			Size int64  `json:"size"`
		} `json:"item"`
	}
	if err := json.Unmarshal(done.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode complete response: %v", err)
	}
	if body.Item.Mime != "image/png" {
		t.Errorf("mime = %q: the assembled bytes are not the original file", body.Item.Mime)
	}
	if body.Item.Size != int64(len(pngTestBytes)) {
		t.Errorf("size = %d, want %d", body.Item.Size, len(pngTestBytes))
	}

	// The upload row and its temp bytes are gone once it became an item.
	if _, err := srv.Store.UploadByID(context.Background(), init.UploadID); err == nil {
		t.Error("the upload row survived completion")
	}
}

func TestUploadRejectsAnOversizeDeclaration(t *testing.T) {
	srv, cookie := chunkedTestServer(t, 16)
	if err := srv.Store.SettingSet(context.Background(), "upload_max_bytes", "64"); err != nil {
		t.Fatalf("SettingSet: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/uploads",
		strings.NewReader(`{"filename":"huge.mp4","size":1048576}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413 before a single byte is accepted", rec.Code)
	}
}

// TestUploadCompletionRejectsALyingDeclaredSize covers the completion-time
// check the spec mandates a test for (media server design doc, "client lying
// about size rejected at completion"): declaring fewer bytes than actually
// sent. The file fits in a single chunk, so that chunk is also the last one
// and the per-chunk exact-size check (which only applies to non-final
// chunks) never catches the lie: only handleUploadComplete's own
// declared-vs-received comparison can.
func TestUploadCompletionRejectsALyingDeclaredSize(t *testing.T) {
	const chunk = 128 // bigger than the whole PNG fixture: one chunk total
	srv, cookie := chunkedTestServer(t, chunk)

	declared := len(pngTestBytes) - 1
	init := initUpload(t, srv, cookie, "cat.png", declared)

	if rec := putChunk(t, srv, cookie, init.UploadID, 0, pngTestBytes); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT chunk 0 = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}

	rec := completeUpload(t, srv, cookie, init.UploadID)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("complete with a lying declared size = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != "size_mismatch" {
		t.Errorf("code = %q, want size_mismatch", body.Code)
	}
}

// cwebpSmallerStub is a fake cwebp that always emits a short, valid-sniffing
// webp payload, standing in for a real optimizer producing a strictly
// smaller file: the case that makes auto_webp adopt the conversion (see
// internal/media/optimize.go's "only when strictly smaller" rule).
const cwebpSmallerStub = `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then shift; out="$1"; fi
  shift
done
printf 'RIFF____WEBPVP8L' > "$out"`

// TestChunkedUploadOfOptimizablePNGSucceedsOnce is the C1 regression: a
// chunked upload of a PNG that auto_webp can shrink used to fail every time
// with size_mismatch, because the completion check compared the DECLARED
// (original) size against the STORED (post-optimization, strictly smaller)
// size. It must instead succeed once, produce exactly one item row, and a
// second completion of the same upload must not mint a duplicate.
func TestChunkedUploadOfOptimizablePNGSucceedsOnce(t *testing.T) {
	const chunk = 16
	srv, cookie := chunkedTestServer(t, chunk)
	ctx := context.Background()
	media.StubTools(t, map[string]string{"cwebp": cwebpSmallerStub})

	init := initUpload(t, srv, cookie, "cat.png", len(pngTestBytes))
	total := (len(pngTestBytes) + chunk - 1) / chunk
	for i := 0; i < total; i++ {
		if rec := putChunk(t, srv, cookie, init.UploadID, i, chunkOf(pngTestBytes, i, chunk)); rec.Code != http.StatusNoContent {
			t.Fatalf("PUT chunk %d = %d (body: %s)", i, rec.Code, rec.Body.String())
		}
	}

	type completeBody struct {
		Item struct {
			ID   string `json:"id"`
			Mime string `json:"mime"`
			Size int64  `json:"size"`
		} `json:"item"`
		Optimized bool `json:"optimized"`
	}

	first := completeUpload(t, srv, cookie, init.UploadID)
	if first.Code != http.StatusCreated {
		t.Fatalf("first complete of an optimizable upload = %d, want 201 (body: %s)", first.Code, first.Body.String())
	}
	var firstBody completeBody
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatalf("decode first complete response %q: %v", first.Body.String(), err)
	}
	if !firstBody.Optimized {
		t.Errorf("optimized = false, want true: the stub cwebp output should have been adopted")
	}
	if firstBody.Item.Mime != "image/webp" {
		t.Errorf("mime = %q, want image/webp", firstBody.Item.Mime)
	}

	items, _, err := srv.Store.ListItems(ctx, db.ItemQuery{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items after first complete = %d, want exactly 1", len(items))
	}

	// A retry of the same completion (a lost response, a double-click, a
	// second tab) must not mint a second row, must not 500, and should hand
	// back the same item it made the first time.
	second := completeUpload(t, srv, cookie, init.UploadID)
	if second.Code != http.StatusOK {
		t.Errorf("second complete of an already-completed upload = %d, want 200", second.Code)
	}
	var secondBody completeBody
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatalf("decode second complete response %q: %v", second.Body.String(), err)
	}
	if secondBody.Item.ID != firstBody.Item.ID {
		t.Errorf("second complete returned item %q, want the original item %q", secondBody.Item.ID, firstBody.Item.ID)
	}

	itemsAfter, _, err := srv.Store.ListItems(ctx, db.ItemQuery{})
	if err != nil {
		t.Fatalf("ListItems (second): %v", err)
	}
	if len(itemsAfter) != 1 {
		t.Errorf("items after second complete = %d, want still exactly 1 (no duplicate row)", len(itemsAfter))
	}
}

func TestUploadInitRejectsAnUnknownFolder(t *testing.T) {
	srv, cookie := chunkedTestServer(t, 16)
	req := httptest.NewRequest(http.MethodPost, "/api/uploads",
		strings.NewReader(`{"filename":"cat.png","size":10,"folder_id":999999}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a folder_id that does not exist (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestOversizeChunkBodyIsRejected(t *testing.T) {
	const chunk = 16
	srv, cookie := chunkedTestServer(t, chunk)
	init := initUpload(t, srv, cookie, "cat.png", len(pngTestBytes))

	rec := putChunk(t, srv, cookie, init.UploadID, 0, bytes.Repeat([]byte{'x'}, chunk*4))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413 for a chunk larger than chunk_size", rec.Code)
	}
}

func TestUploadOfAnotherUserIs404(t *testing.T) {
	srv, cookie := chunkedTestServer(t, 16)
	init := initUpload(t, srv, cookie, "cat.png", len(pngTestBytes))
	intruder := authenticate(t, srv, "mallory")

	rec := putChunk(t, srv, intruder, init.UploadID, 0, chunkOf(pngTestBytes, 0, 16))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: 403 would confirm the id exists", rec.Code)
	}
}

func TestUploadRejectsACrossSiteRequest(t *testing.T) {
	srv, cookie := chunkedTestServer(t, 16)
	req := httptest.NewRequest(http.MethodPost, "/api/uploads",
		strings.NewReader(`{"filename":"cat.png","size":10}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a cross-site upload", rec.Code)
	}
}

func TestCancelledUploadCannotBeAppendedTo(t *testing.T) {
	srv, cookie := chunkedTestServer(t, 16)
	init := initUpload(t, srv, cookie, "cat.png", len(pngTestBytes))

	req := httptest.NewRequest(http.MethodDelete, "/api/uploads/"+init.UploadID, nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/uploads/{id} = %d, want 204", rec.Code)
	}

	if rec := putChunk(t, srv, cookie, init.UploadID, 0, chunkOf(pngTestBytes, 0, 16)); rec.Code != http.StatusNotFound {
		t.Errorf("PUT chunk after cancel = %d, want 404: a cancelled upload id must stop working", rec.Code)
	}
	if rec := completeUpload(t, srv, cookie, init.UploadID); rec.Code != http.StatusNotFound {
		t.Errorf("complete after cancel = %d, want 404", rec.Code)
	}
}

// chunkOf slices the i-th chunk out of payload.
func chunkOf(payload []byte, index, size int) []byte {
	start := index * size
	end := start + size
	if end > len(payload) {
		end = len(payload)
	}
	return payload[start:end]
}

// alwaysFailingQueue makes media.Save fail deterministically inside its
// Enqueue step, which runs after CreateItem has already committed the item
// row, the same shape as a hash/placeBlob/CreateItem/optimizer failure that
// media.Save's caller (handleUploadComplete) sees as a plain error, since
// Save itself rolls the item back on any enqueue failure (see
// internal/media/store.go's rollback comment). This mirrors the technique
// internal/media/store_test.go's failingQueue uses to reach the same point
// in Save deterministically, without touching the filesystem or timing.
type alwaysFailingQueue struct{ err error }

func (q *alwaysFailingQueue) Enqueue(context.Context, string, any) (int64, error) {
	return 0, q.err
}

// blockingQueue holds media.Save inside its Enqueue call until release is
// closed, so a test can force two /complete requests to genuinely overlap
// instead of hoping timing lines up. started closes the moment Enqueue is
// entered, which happens deep inside Save, well after handleUploadComplete
// has already taken its claim on the upload id, so a goroutine waiting on
// started knows the claim is held before it fires a second, truly
// concurrent completion attempt.
type blockingQueue struct {
	started chan struct{}
	release chan struct{}
}

func newBlockingQueue() *blockingQueue {
	return &blockingQueue{started: make(chan struct{}), release: make(chan struct{})}
}

func (q *blockingQueue) Enqueue(context.Context, string, any) (int64, error) {
	close(q.started)
	<-q.release
	return 1, nil
}

// uploadThroughChunks drives init + every chunk PUT for pngTestBytes and
// returns the upload id and the chunk count, so the tests below don't repeat
// the same setup boilerplate.
func uploadThroughChunks(t *testing.T, srv *Server, cookie *http.Cookie, chunk int) (string, int) {
	t.Helper()
	init := initUpload(t, srv, cookie, "cat.png", len(pngTestBytes))
	total := (len(pngTestBytes) + chunk - 1) / chunk
	for i := 0; i < total; i++ {
		if rec := putChunk(t, srv, cookie, init.UploadID, i, chunkOf(pngTestBytes, i, chunk)); rec.Code != http.StatusNoContent {
			t.Fatalf("PUT chunk %d = %d (body: %s)", i, rec.Code, rec.Body.String())
		}
	}
	return init.UploadID, total
}

// TestUploadCompleteSaveFailureLeavesProgressIntact is the fix for the
// regression the re-review flagged (rereview-c1.md, Q2): the prior claim
// design deleted the upload row via DeleteUpload *before* calling Save, and
// on any Save error unconditionally deleted every staged chunk too. A
// transient Save failure (disk pressure mid-spool, an optimizer crash, a
// hash/placeBlob/CreateItem hiccup) turned into "re-upload the entire file
// from byte zero" even though nothing was actually wrong with the bytes
// already on disk. The claim is now purely in-process (claimUpload), so the
// row and chunks must survive a Save failure untouched, and a retry must be
// able to succeed without resending a single byte.
func TestUploadCompleteSaveFailureLeavesProgressIntact(t *testing.T) {
	const chunk = 16
	srv, cookie := chunkedTestServer(t, chunk)
	media.StubTools(t, map[string]string{}) // no cwebp: this test is about Save failing, not optimizing
	ctx := context.Background()

	uploadID, total := uploadThroughChunks(t, srv, cookie, chunk)
	up, err := srv.Store.UploadByID(ctx, uploadID)
	if err != nil {
		t.Fatalf("UploadByID before complete: %v", err)
	}
	tempDir := up.TempDir

	boom := errors.New("boom: queue unavailable")
	srv.Media.Queue = &alwaysFailingQueue{err: boom}

	t.Run("SaveFailureKeepsRowAndChunks", func(t *testing.T) {
		rec := completeUpload(t, srv, cookie, uploadID)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("complete during a Save failure = %d, want 500 (body: %s)", rec.Code, rec.Body.String())
		}

		// Save rolls its own item back on an enqueue failure (it happens
		// after CreateItem commits), so nothing should be left behind.
		items, _, err := srv.Store.ListItems(ctx, db.ItemQuery{})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if len(items) != 0 {
			t.Fatalf("items after a failed complete = %d, want 0", len(items))
		}

		// This is the regression check: the row and every chunk must still
		// be here, not deleted the instant the claim was taken.
		if _, err := srv.Store.UploadByID(ctx, uploadID); err != nil {
			t.Fatalf("upload row after a Save failure: %v, want it intact", err)
		}
		for i := 0; i < total; i++ {
			path := filepath.Join(tempDir, fmt.Sprintf("%d.part", i))
			if _, err := os.Stat(path); err != nil {
				t.Errorf("chunk %d after a Save failure: %v, want it intact", i, err)
			}
		}
	})

	t.Run("RetryAfterFailureSucceeds", func(t *testing.T) {
		// The claim must have been released on the failure above: a retry
		// with a working queue must now succeed, without resending bytes.
		srv.Media.Queue = nil
		retry := completeUpload(t, srv, cookie, uploadID)
		if retry.Code != http.StatusCreated {
			t.Fatalf("retry after a Save failure = %d, want 201 (body: %s)", retry.Code, retry.Body.String())
		}
		items, _, err := srv.Store.ListItems(ctx, db.ItemQuery{})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("items after the successful retry = %d, want exactly 1", len(items))
		}
	})
}

// TestConcurrentDoubleCompleteOneWinsOneGets409 proves the in-process claim
// still serialises two genuinely concurrent completions of the same upload
// id: exactly one must run Save, the other must be rejected with 409 rather
// than both succeeding and minting two item rows for the same bytes.
// blockingQueue forces real overlap instead of relying on timing.
func TestConcurrentDoubleCompleteOneWinsOneGets409(t *testing.T) {
	const chunk = 16
	srv, cookie := chunkedTestServer(t, chunk)
	media.StubTools(t, map[string]string{})
	ctx := context.Background()

	uploadID, _ := uploadThroughChunks(t, srv, cookie, chunk)

	queue := newBlockingQueue()
	srv.Media.Queue = queue

	winnerDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		winnerDone <- completeUpload(t, srv, cookie, uploadID)
	}()

	select {
	case <-queue.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first complete never reached Save's enqueue step")
	}

	loser := completeUpload(t, srv, cookie, uploadID)
	if loser.Code != http.StatusConflict {
		t.Errorf("second concurrent complete = %d, want 409 (body: %s)", loser.Code, loser.Body.String())
	}

	close(queue.release)
	winner := <-winnerDone
	if winner.Code != http.StatusCreated {
		t.Fatalf("first complete = %d, want 201 (body: %s)", winner.Code, winner.Body.String())
	}

	items, _, err := srv.Store.ListItems(ctx, db.ItemQuery{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("items after a concurrent double complete = %d, want exactly 1", len(items))
	}
}

// TestSecondCompleteAfterSuccessIsIdempotent covers the claim mechanism's
// idempotent-replay guarantee directly (independent of the optimizing-upload
// path TestChunkedUploadOfOptimizablePNGSucceedsOnce already exercises): a
// second /complete after a successful one must return 200 with the original
// item, never a duplicate row, and never a 500 on the row this handler
// already deleted.
func TestSecondCompleteAfterSuccessIsIdempotent(t *testing.T) {
	const chunk = 16
	srv, cookie := chunkedTestServer(t, chunk)
	media.StubTools(t, map[string]string{})
	ctx := context.Background()

	uploadID, _ := uploadThroughChunks(t, srv, cookie, chunk)

	type completeBody struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
	}

	first := completeUpload(t, srv, cookie, uploadID)
	if first.Code != http.StatusCreated {
		t.Fatalf("first complete = %d, want 201 (body: %s)", first.Code, first.Body.String())
	}
	var firstBody completeBody
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatalf("decode first complete response %q: %v", first.Body.String(), err)
	}

	second := completeUpload(t, srv, cookie, uploadID)
	if second.Code != http.StatusOK {
		t.Fatalf("second complete after success = %d, want 200 (body: %s)", second.Code, second.Body.String())
	}
	var secondBody completeBody
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatalf("decode second complete response %q: %v", second.Body.String(), err)
	}
	if secondBody.Item.ID != firstBody.Item.ID {
		t.Errorf("second complete returned item %q, want the original item %q", secondBody.Item.ID, firstBody.Item.ID)
	}

	items, _, err := srv.Store.ListItems(ctx, db.ItemQuery{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("items after the idempotent retry = %d, want exactly 1", len(items))
	}

	// The row is really gone: the second complete above must have been
	// answered from the idempotency cache, not by re-touching a live row.
	if _, err := srv.Store.UploadByID(ctx, uploadID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("upload row after a successful complete = %v, want db.ErrNotFound", err)
	}
}
