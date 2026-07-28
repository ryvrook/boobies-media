package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boobies-media/internal/auth"
	"boobies-media/internal/media"
)

// embedBody fetches /s/{id} anonymously (no cookie) and returns the recorder.
func embedBody(t *testing.T, srv *Server, id string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/s/"+id, nil))
	return rec
}

func TestEmbedImageHasEveryOpenGraphTag(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	srv.Cfg.BaseURL = "https://media.example.com"
	item := storeBlob(t, srv, mediaStore, pngTestBytes, "kitten.png")
	if err := srv.Store.SetItemTitle(ctx, item.ID, "Kitten"); err != nil {
		t.Fatalf("SetItemTitle: %v", err)
	}
	if err := srv.Store.SetItemProbe(ctx, item.ID, 640, 480, 0); err != nil {
		t.Fatalf("SetItemProbe: %v", err)
	}

	rec := embedBody(t, srv, item.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	body := rec.Body.String()
	want := []string{
		`<meta property="og:type" content="website">`,
		`<meta property="og:title" content="Kitten">`,
		`<meta property="og:url" content="https://media.example.com/s/` + item.ID + `">`,
		`<meta property="og:image" content="https://media.example.com/m/` + item.ID + `">`,
		`<meta property="og:image:width" content="640">`,
		`<meta property="og:image:height" content="480">`,
		`<meta property="og:image:type" content="image/png">`,
		`<meta name="twitter:card" content="summary_large_image">`,
	}
	for _, tag := range want {
		if !strings.Contains(body, tag) {
			t.Errorf("image embed is missing tag:\n%s", tag)
		}
	}
	if strings.Contains(body, "og:video") {
		t.Error("an image item emitted an og:video tag")
	}
}

func TestEmbedVideoHasVideoAndPosterTags(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	srv.Cfg.BaseURL = "https://media.example.com"
	item := storeBlob(t, srv, mediaStore, testMP4, "clip.mp4")
	if err := srv.Store.SetItemTitle(ctx, item.ID, "Clip"); err != nil {
		t.Fatalf("SetItemTitle: %v", err)
	}
	if err := srv.Store.SetItemProbe(ctx, item.ID, 1280, 720, 12.5); err != nil {
		t.Fatalf("SetItemProbe: %v", err)
	}

	body := embedBody(t, srv, item.ID).Body.String()
	want := []string{
		`<meta property="og:type" content="video.other">`,
		`<meta property="og:video:secure_url" content="https://media.example.com/m/` + item.ID + `">`,
		`<meta property="og:video:type" content="video/mp4">`,
		`<meta property="og:video:width" content="1280">`,
		`<meta property="og:video:height" content="720">`,
		`<meta property="og:image" content="https://media.example.com/p/` + item.ID + `">`,
		`<meta property="og:image:type" content="image/jpeg">`,
		`<meta name="twitter:image" content="https://media.example.com/p/` + item.ID + `">`,
		`<meta name="twitter:card" content="summary_large_image">`,
	}
	for _, tag := range want {
		if !strings.Contains(body, tag) {
			t.Errorf("video embed is missing tag:\n%s", tag)
		}
	}
}

func TestEmbedWebmFallsBackToImageCard(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	srv.Cfg.BaseURL = "https://media.example.com"
	// A webm blob: sniffed as video/webm by the pipeline stub path would need a
	// real webm header, so store the item row directly with a webm mime.
	item := storeBlob(t, srv, mediaStore, testMP4, "old.mp4")
	if err := srv.Store.SetItemMimeForTest(ctx, item.ID, "video/webm"); err != nil {
		t.Fatalf("SetItemMimeForTest: %v", err)
	}
	// The probed source dimensions exceed the poster's 1024x1024 box-fit on
	// both axes, so a real thumbnail generated from this item would NOT be
	// 1920x1080: it would be letterboxed down to fit. This is the exact shape
	// that must not leak the source's dimensions onto the poster's og:image.
	if err := srv.Store.SetItemProbe(ctx, item.ID, 1920, 1080, 0); err != nil {
		t.Fatalf("SetItemProbe: %v", err)
	}
	body := embedBody(t, srv, item.ID).Body.String()
	if strings.Contains(body, "og:video") {
		t.Error("a webm item emitted og:video tags; it must fall back to an image card")
	}
	if !strings.Contains(body, `<meta property="og:image" content="https://media.example.com/p/`+item.ID+`">`) {
		t.Error("the webm fallback did not emit a poster og:image")
	}
	if strings.Contains(body, `property="og:image:width"`) || strings.Contains(body, `property="og:image:height"`) {
		t.Error("the webm fallback declared a poster width/height; the poster is a box-fit thumbnail whose actual size differs from the probed source dimensions, so no dimension claim may be made for it")
	}
}

func TestEmbedGIFFallsBackToJPEGCard(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	srv.Cfg.BaseURL = "https://media.example.com"
	item := storeBlob(t, srv, mediaStore, gifTestBytes, "party.gif")
	if err := srv.Store.SetItemProbe(ctx, item.ID, 800, 600, 0); err != nil {
		t.Fatalf("SetItemProbe: %v", err)
	}

	body := embedBody(t, srv, item.ID).Body.String()
	for _, tag := range []string{
		`<meta property="og:image" content="https://media.example.com/p/` + item.ID + `">`,
		`<meta property="og:image:type" content="image/jpeg">`,
		`<meta name="twitter:image" content="https://media.example.com/p/` + item.ID + `">`,
	} {
		if !strings.Contains(body, tag) {
			t.Errorf("GIF embed is missing crawler-compatible tag:\n%s", tag)
		}
	}
	if strings.Contains(body, `<meta property="og:image" content="https://media.example.com/m/`+item.ID+`">`) {
		t.Error("GIF embed points crawlers at the full animated GIF instead of its bounded JPEG poster")
	}
	if strings.Contains(body, `property="og:image:width"`) || strings.Contains(body, `property="og:image:height"`) {
		t.Error("GIF poster declared the original animation dimensions")
	}
}

func TestEmbedRevokedAndDeletedReturn404(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	revoked := storeBlob(t, srv, mediaStore, pngTestBytes, "a.png")
	if err := srv.Store.SetItemShareRevoked(ctx, revoked.ID, true); err != nil {
		t.Fatalf("SetItemShareRevoked: %v", err)
	}
	if rec := embedBody(t, srv, revoked.ID); rec.Code != http.StatusNotFound {
		t.Errorf("revoked embed status = %d, want 404", rec.Code)
	}

	deleted := storeBlob(t, srv, mediaStore, append(append([]byte{}, pngTestBytes...), 'x'), "b.png")
	owner, err := srv.Store.UserByID(ctx, deleted.UploaderID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if err := srv.Store.SoftDeleteItem(ctx, deleted.ID, owner); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}
	if rec := embedBody(t, srv, deleted.ID); rec.Code != http.StatusNotFound {
		t.Errorf("deleted embed status = %d, want 404", rec.Code)
	}

	if rec := embedBody(t, srv, "nosuchid0"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown embed status = %d, want 404", rec.Code)
	}
}

func TestEmbedEscapesTitle(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	item := storeBlob(t, srv, mediaStore, pngTestBytes, "x.png")
	if err := srv.Store.SetItemTitle(ctx, item.ID, `"><script>alert(1)</script>`); err != nil {
		t.Fatalf("SetItemTitle: %v", err)
	}
	body := embedBody(t, srv, item.ID).Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("the embed rendered a title unescaped")
	}
}

// TestEmbedUploaderInitialIsARune pins the fix for a byte-slicing bug: a
// display name starting with a multi-byte UTF-8 character must still render
// its whole first character in the avatar span, not a truncated invalid byte.
func TestEmbedUploaderInitialIsARune(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	hash, err := auth.HashPasswordWithParams("hunter2", auth.Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	owner, err := srv.Store.CreateUser(ctx, "unicodefriend", "Émile", hash, "", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	item := storeBlobFor(t, srv, mediaStore, owner.ID, pngTestBytes, "u.png")
	body := embedBody(t, srv, item.ID).Body.String()
	if !strings.Contains(body, `<span class="embed__avatar" aria-hidden="true">É</span>`) {
		t.Error("uploader initial did not render the display name's first rune intact")
	}
}

// TestEmbedEscapesSourceURL pins that a hostile SourceURL (the one field on
// this page that comes from an attacker-influenced source, e.g. a scraped
// page's canonical link) cannot inject a javascript: navigation or break out
// of the href attribute. html/template's contextual autoescaping is what
// protects this today; this test exists so a future change to raw string
// concatenation or template.URL cannot silently reopen it.
func TestEmbedEscapesSourceURL(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	user := testUser(t, srv, "sourceurlfriend", "hunter2")
	media.StubTools(t, map[string]string{})
	res, err := mediaStore.Save(ctx, media.SaveRequest{
		Reader:     bytes.NewReader(pngTestBytes),
		Filename:   "s.png",
		UploaderID: user.ID,
		SourceURL:  `javascript:alert(1)"><script>alert(2)</script>`,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	body := embedBody(t, srv, res.Item.ID).Body.String()
	if strings.Contains(body, "javascript:") {
		t.Error("the embed did not neutralize a javascript: source URL")
	}
	if strings.Contains(body, "<script>alert(2)</script>") {
		t.Error("the embed rendered the source URL unescaped, breaking out of the href attribute")
	}
}
