package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"boobies-media/internal/auth"
	"boobies-media/internal/db"
	"boobies-media/internal/media"
)

// pngTestBytes is a structurally valid 8-bit RGB PNG.
var pngTestBytes = func() []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	buf.Write([]byte{0, 0, 0, 0x0d})
	buf.Write([]byte("IHDR"))
	buf.Write([]byte{0, 0, 0, 0x64, 0, 0, 0, 0x64, 8, 2, 0, 0, 0})
	buf.Write([]byte{0, 0, 0, 0})
	buf.Write([]byte{0, 0, 0, 4})
	buf.Write([]byte("IDAT"))
	buf.Write([]byte{0x78, 0x9c, 0x63, 0x00})
	buf.Write([]byte{0, 0, 0, 0, 0, 0, 0, 0})
	buf.Write([]byte("IEND"))
	buf.Write([]byte{0, 0, 0, 0})
	return buf.Bytes()
}()

// gifTestBytes only needs to satisfy Sniff's magic-byte check: ingest never
// decodes the file, and thumbnailing is always stubbed in tests.
var gifTestBytes = append([]byte("GIF89a"), bytes.Repeat([]byte{0}, 32)...)

// uploadRequest builds a multipart POST carrying one file.
func uploadRequest(t *testing.T, filename string, payload []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

// authenticate attaches a valid session for a freshly created user.
func authenticate(t *testing.T, srv *Server, username string) *http.Cookie {
	t.Helper()
	user := testUser(t, srv, username, "hunter2")
	token := "session-" + username
	if err := srv.Store.CreateSession(context.Background(), auth.HashToken(token), user.ID, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return &http.Cookie{Name: SessionCookieName, Value: token}
}

func TestIngestStoresAnUploadedFile(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	media.StubTools(t, map[string]string{})

	req := uploadRequest(t, "funny cat.png", pngTestBytes)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Item struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Mime     string `json:"mime"`
			ShareURL string `json:"share_url"`
			ThumbURL string `json:"thumb_url"`
			MediaURL string `json:"media_url"`
			Ready    bool   `json:"ready"`
		} `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if len(body.Item.ID) != 8 {
		t.Errorf("id = %q, want an 8-character share slug", body.Item.ID)
	}
	if body.Item.Title != "funny cat" {
		t.Errorf("title = %q", body.Item.Title)
	}
	if body.Item.Mime != "image/png" {
		t.Errorf("mime = %q", body.Item.Mime)
	}
	if body.Item.ShareURL == "" || body.Item.MediaURL == "" || body.Item.ThumbURL == "" {
		t.Errorf("URLs missing from the response: %+v", body.Item)
	}
	if body.Item.Ready {
		t.Error("ready = true before the probe job has run; the UI needs a processing placeholder")
	}

	// The item must be immediately browsable.
	items, _, err := srv.Store.ListItems(context.Background(), db.ItemQuery{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
}

// TestIngestReportsIsGifDistinctFromIsVideo pins the itemJSON shape the grid
// island keys its hover-preview wiring on: a GIF must report is_video=false
// (it needs the same non-seeking still-poster treatment as any other image,
// not a video's) but is_gif=true (it still has its own animation to preview,
// which a plain JPEG or PNG does not).
func TestIngestReportsIsGifDistinctFromIsVideo(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	media.StubTools(t, map[string]string{})

	req := uploadRequest(t, "party.gif", gifTestBytes)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Item struct {
			Mime    string `json:"mime"`
			IsVideo bool   `json:"is_video"`
			IsGif   bool   `json:"is_gif"`
		} `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body.Item.Mime != "image/gif" {
		t.Fatalf("mime = %q, want image/gif", body.Item.Mime)
	}
	if body.Item.IsVideo {
		t.Error("is_video = true for a GIF, want false")
	}
	if !body.Item.IsGif {
		t.Error("is_gif = false for a GIF, want true")
	}
}

func TestIngestRequiresAuthentication(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, uploadRequest(t, "cat.png", pngTestBytes))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an anonymous upload", rec.Code)
	}
}

// TestIngestRejectsACrossSiteRequest pins the consistency fix: /api/ingest
// carries the same second-lock CSRF check as the five /api/uploads* routes,
// the destructive /api/items* routes (origin.go; see
// TestItemAPIRejectsACrossSiteDelete in handlers_items_test.go), and now
// POST /logout (see TestLogoutRejectsACrossSiteRequest in
// handlers_auth_test.go), closing the gap the review flagged. This is not
// currently exploitable, SameSite=Lax already blocks a cross-site form post
// from carrying the session cookie, but the second lock keeps working if the
// cookie policy is ever loosened. Every state-changing route now carries it.
func TestIngestRejectsACrossSiteRequest(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	media.StubTools(t, map[string]string{})

	req := uploadRequest(t, "cat.png", pngTestBytes)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a cross-site ingest request (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestIngestAcceptsABearerKey(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	media.StubTools(t, map[string]string{})

	key, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	hash, err := auth.HashPasswordWithParams("pw", auth.Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := srv.Store.CreateUser(context.Background(), "bot", "Bot", hash, auth.HashToken(key), false); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := uploadRequest(t, "cat.png", pngTestBytes)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 for a Bearer upload (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestIngestRejectsDisallowedTypesWithAClearError(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	media.StubTools(t, map[string]string{})

	payloads := map[string][]byte{
		"cat.svg":   []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(document.cookie)</script></svg>`),
		"page.html": []byte("<!DOCTYPE html><html><body><script>x</script></body></html>"),
		// A hostile client renaming an SVG must not help it through.
		"disguised.png": []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`),
	}
	for filename, payload := range payloads {
		t.Run(filename, func(t *testing.T) {
			req := uploadRequest(t, filename, payload)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("status = %d, want 415", rec.Code)
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Code != "unsupported_type" {
				t.Errorf("code = %q, want unsupported_type", body.Code)
			}
		})
	}

	items, _, err := srv.Store.ListItems(context.Background(), db.ItemQuery{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("%d items were created from rejected uploads", len(items))
	}
}

func TestIngestRejectsOversizeUploads(t *testing.T) {
	ctx := context.Background()
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	media.StubTools(t, map[string]string{})

	// Since Task 16, the multipart branch is capped by upload_chunk_bytes (one
	// request = one chunk, at most), not upload_max_bytes: see
	// TestMultipartIngestIsCappedAtOneChunk. Shrinking the chunk cap is what
	// makes this file oversize now.
	if err := srv.Store.SettingSet(ctx, "upload_chunk_bytes", "32"); err != nil {
		t.Fatalf("SettingSet: %v", err)
	}
	req := uploadRequest(t, "cat.png", pngTestBytes)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Code != "too_large" {
		t.Errorf("code = %q, want too_large", body.Code)
	}
}

func TestIngestRejectsARequestWithNoFile(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("notafile", "x")
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/ingest", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestMultipartIngestIsCappedAtOneChunk(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	ctx := context.Background()
	// A generous total cap, but a tiny per-request one.
	if err := srv.Store.SettingSet(ctx, "upload_max_bytes", "1048576"); err != nil {
		t.Fatalf("SettingSet(max): %v", err)
	}
	if err := srv.Store.SettingSet(ctx, "upload_chunk_bytes", "16"); err != nil {
		t.Fatalf("SettingSet(chunk): %v", err)
	}

	req := uploadRequest(t, "cat.png", pngTestBytes) // comfortably over 16 bytes
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413: the multipart branch must use the chunk cap, not the total cap", rec.Code)
	}
}

// countingReader tracks how many bytes have actually been pulled from the
// wrapped reader, so a test can tell "the server stopped reading early" apart
// from "the server read everything and rejected it afterwards": the two are
// indistinguishable from the status code alone.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// zeroReader is an infinite source of zero bytes, so the huge multipart part
// below can be built without holding a second huge buffer in memory (beyond
// the one multipart.Writer necessarily produces once, up front).
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// TestMultipartIngestBodyIsNotFullySpooledPastTheCap proves the fix for the
// gap Task 14's review found: ParseMultipartForm spools an over-limit file
// part to a temp file via an unbounded io.Copy: the size cap inside
// media.Store.Save only rejects it afterwards, so an attacker could already
// make the server write an arbitrary amount to disk before that rejection.
// Checking the status code alone cannot tell the fixed and broken code apart
// (both eventually 413), so this counts bytes actually read from the request
// body instead.
func TestMultipartIngestBodyIsNotFullySpooledPastTheCap(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	ctx := context.Background()
	if err := srv.Store.SettingSet(ctx, "upload_chunk_bytes", "16"); err != nil {
		t.Fatalf("SettingSet(chunk): %v", err)
	}

	// A file that dwarfs the 16-byte cap: if the server ever read all of it,
	// that is unambiguously the bug.
	const hugeFileSize = 5 << 20 // 5 MiB
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", "huge.bin")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := io.Copy(part, io.LimitReader(zeroReader{}, hugeFileSize)); err != nil {
		t.Fatalf("write huge part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	fullBody := buf.Bytes()

	counter := &countingReader{r: bytes.NewReader(fullBody)}
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", counter)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body: %s)", rec.Code, rec.Body.String())
	}
	// A generous allowance for multipart framing and internal buffering,
	// still two orders of magnitude short of the 5 MiB body. The bug this
	// guards makes counter.n equal len(fullBody) (every byte read).
	const maxHonestRead = 64 << 10 // 64 KiB
	if counter.n > maxHonestRead {
		t.Errorf("read %d of %d body bytes before rejecting the upload: the oversize body was spooled to disk before the cap was enforced (want no more than %d)",
			counter.n, len(fullBody), maxHonestRead)
	}
}

func TestIngestReturns503WhenMediaIsNotWired(t *testing.T) {
	// Plan 1's constructor shape, with no media store attached.
	srv := testServer(t)
	cookie := authenticate(t, srv, "aiden")
	req := uploadRequest(t, "cat.png", pngTestBytes)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when the media store is absent", rec.Code)
	}
}
