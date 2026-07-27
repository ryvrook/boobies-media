package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"boobies-media/internal/config"
	"boobies-media/internal/db"
	"boobies-media/internal/jobs"
	"boobies-media/internal/media"
	"boobies-media/internal/web"
)

// bootMediaServer starts a full server with media, a queue and stub tools.
func bootMediaServer(t *testing.T) (string, *db.Store, *jobs.Queue, *config.Config) {
	t.Helper()
	dataDir := t.TempDir()
	cfg, err := config.Load([]string{"-data", dataDir, "-insecure-cookies"}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	queue := jobs.New(store, 1)
	mediaStore := media.NewStore(cfg, store, queue)
	mediaStore.RegisterHandlers(queue)

	srv, err := web.New(cfg, store, nil, web.WithMedia(mediaStore), web.WithQueue(queue))
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	ts := newTestServer(t, srv)
	return ts, store, queue, cfg
}

func TestEndToEndUploadProbeThumbnailAndServe(t *testing.T) {
	ctx := context.Background()
	base, store, queue, cfg := bootMediaServer(t)
	seedUser(t, store, "aiden", "hunter2")
	client := newClient(t)

	// Stub the media tools so no real ffmpeg is needed.
	media.StubTools(t, map[string]string{
		"ffprobe": `#!/bin/sh
echo '{"streams":[{"width":640,"height":480}],"format":{}}'`,
		"ffmpeg": `#!/bin/sh
for last in "$@"; do :; done
printf 'RIFF____WEBPVP8 thumbnail' > "$last"`,
	})

	// 1. Sign in.
	resp, err := client.PostForm(base+"/login", url.Values{"username": {"aiden"}, "password": {"hunter2"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login status = %d, want 302", resp.StatusCode)
	}

	// 2. Upload a PNG.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "kitten.png")
	_, _ = part.Write(e2ePNG)
	_ = writer.Close()

	req, _ := http.NewRequest(http.MethodPost, base+"/api/ingest", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201 (body: %s)", resp.StatusCode, raw)
	}
	var created struct {
		Item struct {
			ID    string `json:"id"`
			Ready bool   `json:"ready"`
		} `json:"item"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if created.Item.Ready {
		t.Error("ready = true immediately after upload; the probe has not run yet")
	}

	// 3. Drain the queue: probe, then thumbnail.
	for i := 0; i < 4; i++ {
		if _, err := queue.RunOnce(ctx); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
	}
	item, err := store.ItemByID(ctx, created.Item.ID)
	if err != nil {
		t.Fatalf("ItemByID: %v", err)
	}
	if item.Width != 640 || item.Height != 480 {
		t.Errorf("dimensions = %dx%d, want 640x480 after the probe job", item.Width, item.Height)
	}
	for _, size := range media.ThumbSizes {
		if _, err := os.Stat(media.ThumbPath(cfg.ThumbsDir(), item.ContentHash, size)); err != nil {
			t.Errorf("thumbnail %d was not generated: %v", size, err)
		}
	}

	// 4. The media route serves it anonymously, with ranges.
	anon := newClient(t)
	resp, err = anon.Get(base + "/m/" + item.ID)
	if err != nil {
		t.Fatalf("GET /m/: %v", err)
	}
	served, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /m/ status = %d, want 200", resp.StatusCode)
	}
	if !bytes.Equal(served, e2ePNG) {
		t.Error("the served bytes differ from what was uploaded")
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Error("Accept-Ranges is missing; Safari would refuse to play video")
	}

	rangeReq, _ := http.NewRequest(http.MethodGet, base+"/m/"+item.ID, nil)
	rangeReq.Header.Set("Range", "bytes=0-9")
	resp, err = anon.Do(rangeReq)
	if err != nil {
		t.Fatalf("range request: %v", err)
	}
	partial, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", resp.StatusCode)
	}
	if len(partial) != 10 {
		t.Errorf("range body = %d bytes, want 10", len(partial))
	}

	// 5. The thumbnail route serves both sizes.
	for _, size := range []string{"320", "1024"} {
		resp, err = anon.Get(base + "/t/" + item.ID + "?s=" + size)
		if err != nil {
			t.Fatalf("GET /t/?s=%s: %v", size, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET /t/?s=%s status = %d, want 200", size, resp.StatusCode)
		}
	}

	// 6. The browse page now shows it.
	resp, err = client.Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), item.ID) {
		t.Error("the browse page does not list the uploaded item")
	}
}

func TestEndToEndSVGUploadIsRejected(t *testing.T) {
	base, store, _, cfg := bootMediaServer(t)
	seedUser(t, store, "aiden", "hunter2")
	client := newClient(t)
	media.StubTools(t, map[string]string{})

	resp, err := client.PostForm(base+"/login", url.Values{"username": {"aiden"}, "password": {"hunter2"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	resp.Body.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "innocent.png")
	_, _ = part.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>fetch('/api/items')</script></svg>`))
	_ = writer.Close()

	req, _ := http.NewRequest(http.MethodPost, base+"/api/ingest", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415: an SVG served same-origin is stored XSS", resp.StatusCode)
	}

	entries, err := os.ReadDir(cfg.FilesDir())
	if err != nil {
		t.Fatalf("read files dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("%d entries were written for a rejected upload", len(entries))
	}
}

func TestEndToEndDedupAndPurge(t *testing.T) {
	ctx := context.Background()
	base, store, queue, cfg := bootMediaServer(t)
	seedUser(t, store, "aiden", "hunter2")
	seedUser(t, store, "mia", "hunter2")
	media.StubTools(t, map[string]string{})
	_ = queue // this test drives the media store directly, not the workers

	mediaStore := media.NewStore(cfg, store, nil)
	alice, _ := store.UserByUsername(ctx, "aiden")
	mia, _ := store.UserByUsername(ctx, "mia")

	first, err := mediaStore.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(e2ePNG), Filename: "a.png", UploaderID: alice.ID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	second, err := mediaStore.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(e2ePNG), Filename: "b.png", UploaderID: mia.ID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if first.Item.ContentHash != second.Item.ContentHash {
		t.Fatal("identical uploads did not dedup")
	}

	blob := media.BlobPath(cfg.FilesDir(), first.Item.ContentHash)
	if err := mediaStore.Purge(ctx, first.Item.ID); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if _, err := os.Stat(blob); err != nil {
		t.Fatal("purging one of two items deleted the shared blob")
	}
	// The survivor must still serve.
	resp, err := newClient(t).Get(base + "/m/" + second.Item.ID)
	if err != nil {
		t.Fatalf("GET /m/: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the surviving item serves %d, want 200", resp.StatusCode)
	}
}

// e2ePNG is a structurally valid 8-bit RGB PNG.
var e2ePNG = func() []byte {
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
