package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"boobies-media/internal/auth"
	"boobies-media/internal/config"
	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
	"boobies-media/internal/media"
)

// mediaTestServer builds a server wired to a real media store on a temp dir.
func mediaTestServer(t *testing.T) (*Server, *media.Store, *config.Config) {
	t.Helper()
	cfg, err := config.Load([]string{"-data", t.TempDir(), "-insecure-cookies"}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	store := dbtest.New(t)
	mediaStore := media.NewStore(cfg, store, nil)
	srv, err := New(cfg, store, nil, WithMedia(mediaStore))
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return srv, mediaStore, cfg
}

// storeBlob writes bytes straight into the blob store and returns the item.
// The username is derived from filename so a test calling this more than
// once against the same server (distinct filenames per call) does not
// collide on a duplicate username.
func storeBlob(t *testing.T, srv *Server, mediaStore *media.Store, payload []byte, filename string) *db.Item {
	t.Helper()
	username := "u-" + strings.ToLower(strings.Map(func(r rune) rune {
		if r == '.' {
			return -1
		}
		return r
	}, filename))
	user := testUser(t, srv, username, "hunter2")
	media.StubTools(t, map[string]string{})
	res, err := mediaStore.Save(context.Background(), media.SaveRequest{
		Reader: bytes.NewReader(payload), Filename: filename, UploaderID: user.ID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return res.Item
}

// A small but valid MP4 header followed by padding, so ranges have something to bite on.
var testMP4 = append([]byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm',
	0, 0, 2, 0, 'i', 's', 'o', 'm'}, bytes.Repeat([]byte{0x41}, 4096)...)

func TestRawMediaServesTheStoredBytesAnonymously(t *testing.T) {
	srv, mediaStore, _ := mediaTestServer(t)
	item := storeBlob(t, srv, mediaStore, testMP4, "clip.mp4")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/m/"+item.ID, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), testMP4) {
		t.Error("served bytes differ from the stored bytes")
	}
}

// assertMediaSecurityHeaders checks the four headers mandated for every
// /m/ and /t/ response. It is used against 200 and 206 responses alike:
// the headers are set once before http.ServeContent runs and must survive
// whichever status code ServeContent picks.
func assertMediaSecurityHeaders(t *testing.T, h http.Header, wantContentType, wantFilenameContains string) {
	t.Helper()
	if got := h.Get("Content-Type"); got != wantContentType {
		t.Errorf("Content-Type = %q, want %q", got, wantContentType)
	}
	if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := h.Get("Content-Security-Policy"); got != "default-src 'none'; sandbox" {
		t.Errorf("Content-Security-Policy = %q, want default-src 'none'; sandbox", got)
	}
	disposition := h.Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "inline;") {
		t.Errorf("Content-Disposition = %q, want an inline disposition", disposition)
	}
	if wantFilenameContains != "" && !strings.Contains(disposition, wantFilenameContains) {
		t.Errorf("Content-Disposition = %q, want it to contain %q", disposition, wantFilenameContains)
	}
	cache := h.Get("Cache-Control")
	if !strings.Contains(cache, "immutable") {
		t.Errorf("Cache-Control = %q, want an immutable directive", cache)
	}
}

func TestRawMediaSecurityHeaders(t *testing.T) {
	srv, mediaStore, _ := mediaTestServer(t)
	item := storeBlob(t, srv, mediaStore, testMP4, "clip.mp4")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/m/"+item.ID, nil))

	assertMediaSecurityHeaders(t, rec.Header(), "video/mp4", "clip.mp4")
}

func TestRawMediaContentTypeComesFromTheDatabaseNotTheBytes(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	item := storeBlob(t, srv, mediaStore, testMP4, "clip.mp4")

	// Corrupt the stored mime. The response must follow the database, because
	// re-sniffing at serve time would sidestep the ingest allowlist entirely.
	if _, err := srv.Store.DB.ExecContext(ctx, `UPDATE items SET mime = 'image/webp' WHERE id = ?`, item.ID); err != nil {
		t.Fatalf("update mime: %v", err)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/m/"+item.ID, nil))
	if got := rec.Header().Get("Content-Type"); got != "image/webp" {
		t.Errorf("Content-Type = %q, want the stored image/webp", got)
	}
}

func TestRawMediaSupportsRangeRequests(t *testing.T) {
	srv, mediaStore, _ := mediaTestServer(t)
	item := storeBlob(t, srv, mediaStore, testMP4, "clip.mp4")

	// Safari refuses to play <video> from a server that will not answer 206.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/m/"+item.ID, nil))
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/m/"+item.ID, nil)
	req.Header.Set("Range", "bytes=0-1023")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if rec.Body.Len() != 1024 {
		t.Errorf("body length = %d, want 1024", rec.Body.Len())
	}
	contentRange := rec.Header().Get("Content-Range")
	if !strings.HasPrefix(contentRange, "bytes 0-1023/") {
		t.Errorf("Content-Range = %q, want it to start with \"bytes 0-1023/\"", contentRange)
	}
	if !bytes.Equal(rec.Body.Bytes(), testMP4[:1024]) {
		t.Error("the partial body is not the requested slice")
	}
	// The security headers must not be conditional on ServeContent choosing
	// 206 over 200: they are set on the response before it decides.
	assertMediaSecurityHeaders(t, rec.Header(), "video/mp4", "clip.mp4")
}

func TestRawMedia404s(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)

	t.Run("unknown id", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/m/nosuchid", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("revoked share", func(t *testing.T) {
		item := storeBlob(t, srv, mediaStore, testMP4, "revoked.mp4")
		if err := srv.Store.SetItemShareRevoked(ctx, item.ID, true); err != nil {
			t.Fatalf("SetItemShareRevoked: %v", err)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/m/"+item.ID, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for a revoked share link", rec.Code)
		}
	})

	t.Run("soft deleted", func(t *testing.T) {
		item := storeBlob(t, srv, mediaStore, testMP4, "deleted.mp4")
		user, err := srv.Store.UserByID(ctx, item.UploaderID)
		if err != nil {
			t.Fatalf("UserByID: %v", err)
		}
		if err := srv.Store.SoftDeleteItem(ctx, item.ID, user); err != nil {
			t.Fatalf("SoftDeleteItem: %v", err)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/m/"+item.ID, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for a deleted item", rec.Code)
		}
	})
}

func TestRawMediaTraversalShapedIDsCannotEscape(t *testing.T) {
	srv, _, _ := mediaTestServer(t)

	// The {id} segment only ever keys a database lookup; the on-disk path is
	// built from item.ContentHash, a value the server generated at ingest,
	// never from client input. A traversal-shaped id therefore cannot do
	// anything more interesting than fail to match an item.
	for _, id := range []string{"..", "...", "%2e%2e", "etc%2Fpasswd", "a%00b"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/m/"+id, nil))
		if rec.Code == http.StatusOK || rec.Code >= http.StatusInternalServerError {
			t.Errorf("id %q: status = %d, want neither 200 nor a 5xx", id, rec.Code)
		}
	}
}

func TestThumbnailServesBothAllowedSizes(t *testing.T) {
	srv, mediaStore, cfg := mediaTestServer(t)
	item := storeBlob(t, srv, mediaStore, testMP4, "clip.mp4")

	for _, size := range media.ThumbSizes {
		path := media.ThumbPath(cfg.ThumbsDir(), item.ContentHash, size)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("RIFF____WEBPVP8 fake"), 0o644); err != nil {
			t.Fatalf("write thumbnail: %v", err)
		}
	}

	for _, size := range []string{"320", "1024"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/"+item.ID+"?s="+size, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("size %s: status = %d, want 200", size, rec.Code)
		}
		assertMediaSecurityHeaders(t, rec.Header(), "image/webp", "clip.webp")
	}
}

func TestThumbnailDefaultsToTheSmallSize(t *testing.T) {
	srv, mediaStore, cfg := mediaTestServer(t)
	item := storeBlob(t, srv, mediaStore, testMP4, "clip.mp4")

	path := media.ThumbPath(cfg.ThumbsDir(), item.ContentHash, 320)
	_ = os.MkdirAll(filepath.Dir(path), 0o750)
	if err := os.WriteFile(path, []byte("RIFF____WEBPVP8 fake"), 0o644); err != nil {
		t.Fatalf("write thumbnail: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/"+item.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with no ?s= parameter", rec.Code)
	}
}

func TestExtensionBearingEmbedImageIsPublicAndSupportsHead(t *testing.T) {
	srv, mediaStore, _ := mediaTestServer(t)
	item := storeBlob(t, srv, mediaStore, gifTestBytes, "party.gif")

	req := httptest.NewRequest(http.MethodHead, "/i/"+item.ID+".gif", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (Location %q)", rec.Code, rec.Header().Get("Location"))
	}
	if got := rec.Header().Get("Content-Type"); got != "image/gif" {
		t.Errorf("Content-Type = %q, want image/gif", got)
	}
	if rec.Body.Len() != 0 {
		t.Error("HEAD response unexpectedly included media bytes")
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/i/"+item.ID+".png", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("wrong extension status = %d, want 404", rec.Code)
	}
}

func TestThumbnailRejectsSizesOutsideTheAllowlist(t *testing.T) {
	srv, mediaStore, _ := mediaTestServer(t)
	item := storeBlob(t, srv, mediaStore, testMP4, "clip.mp4")

	// An unbounded size would be an arbitrary-resize denial of service.
	for _, size := range []string{"64", "4096", "0", "-1", "999999", "abc", "320;DROP"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/"+item.ID+"?s="+size, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("size %q: status = %d, want 400", size, rec.Code)
		}
	}
}

func TestThumbnail404sWhileStillProcessing(t *testing.T) {
	srv, mediaStore, _ := mediaTestServer(t)
	item := storeBlob(t, srv, mediaStore, testMP4, "clip.mp4")

	// No thumbnail file yet; the grid falls back to a placeholder.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/"+item.ID, nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 before the thumbnail job has run", rec.Code)
	}
}

func TestMediaRoutesAreRateLimitedPerIP(t *testing.T) {
	srv, mediaStore, _ := mediaTestServer(t)
	item := storeBlob(t, srv, mediaStore, testMP4, "clip.mp4")
	// A share link posted somewhere public must not saturate a home uplink.
	srv.PublicLimiter = newTestPublicLimiter(2)

	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/m/"+item.ID, nil)
		req.RemoteAddr = "203.0.113.9:1111"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/m/"+item.ID, nil)
	req.RemoteAddr = "203.0.113.9:1111"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once the budget is spent", rec.Code)
	}

	// A different client is unaffected.
	req = httptest.NewRequest(http.MethodGet, "/m/"+item.ID, nil)
	req.RemoteAddr = "198.51.100.4:2222"
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("a different IP got %d, want 200", rec.Code)
	}
}

func TestMalformedThumbnailSizeStillConsumesRateBudget(t *testing.T) {
	srv, mediaStore, cfg := mediaTestServer(t)
	item := storeBlob(t, srv, mediaStore, testMP4, "clip.mp4")
	path := media.ThumbPath(cfg.ThumbsDir(), item.ContentHash, media.ThumbSizes[0])
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("RIFF____WEBPVP8 fake"), 0o644); err != nil {
		t.Fatalf("write thumbnail: %v", err)
	}
	// A budget of 2: exhausted entirely by malformed requests below.
	srv.PublicLimiter = newTestPublicLimiter(2)

	// Two malformed-size requests must still cost budget, even though each
	// one 400s before the item is ever resolved. If the limiter were only
	// reachable after size validation, these would be free and an attacker
	// could hammer /t/{id}?s=bogus without ever tripping the 429.
	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/t/"+item.ID+"?s=bogus", nil)
		req.RemoteAddr = "203.0.113.9:1111"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("request %d: status = %d, want 400", i, rec.Code)
		}
	}

	// The budget is now spent. A well-formed, otherwise-servable request from
	// the same IP must be turned away with 429: not served, and not 400'd.
	req := httptest.NewRequest(http.MethodGet, "/t/"+item.ID, nil)
	req.RemoteAddr = "203.0.113.9:1111"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: malformed ?s= requests must spend the same budget as well-formed ones", rec.Code)
	}
}

func TestMediaRoutesRequireNoAuthentication(t *testing.T) {
	// The gate must let these through; that is the whole point of a share link.
	for _, path := range []string{"/m/abc12345", "/t/abc12345", "/p/abc12345", "/g/abc12345.mp4"} {
		if !IsPublicPath(path) {
			t.Errorf("IsPublicPath(%q) = false; share links would need a login", path)
		}
	}
}

func TestSocialAnimationRouteGeneratesAndServesGIFAsMP4(t *testing.T) {
	srv, mediaStore, _ := mediaTestServer(t)
	item := storeBlob(t, srv, mediaStore, gifTestBytes, "party.gif")
	media.StubTools(t, map[string]string{
		"ffmpeg": `#!/bin/sh
for last in "$@"; do :; done
printf 'social-mp4' > "$last"`,
	})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/g/"+item.ID+".mp4", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Errorf("Content-Type = %q, want video/mp4", got)
	}
	if rec.Body.String() != "social-mp4" {
		t.Errorf("body = %q, want generated MP4 bytes", rec.Body.String())
	}
}

func newTestPublicLimiter(max int) *auth.Limiter {
	return auth.NewLimiter(max, time.Minute)
}
