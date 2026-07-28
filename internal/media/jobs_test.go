package media_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"boobies-media/internal/db"
	"boobies-media/internal/media"
)

const ffprobeStub = `#!/bin/sh
echo '{"streams":[{"width":1920,"height":1080}],"format":{"duration":"12.500000"}}'`

const ffprobeImageStub = `#!/bin/sh
echo '{"streams":[{"width":640,"height":480}],"format":{}}'`

// ffmpegStub writes a plausible webp to the last argument, which is the output
// path in every invocation this code makes.
const ffmpegStub = `#!/bin/sh
for last in "$@"; do :; done
printf 'RIFF____WEBPVP8 ' > "$last"`

func TestProbeFileReadsDimensionsAndDuration(t *testing.T) {
	store, _, _, _ := newMediaStore(t)
	media.StubTools(t, map[string]string{"ffprobe": ffprobeStub})

	got, err := store.ProbeFile(context.Background(), "/does/not/matter")
	if err != nil {
		t.Fatalf("ProbeFile: %v", err)
	}
	if got.Width != 1920 || got.Height != 1080 {
		t.Errorf("dimensions = %dx%d, want 1920x1080", got.Width, got.Height)
	}
	if got.Duration != 12.5 {
		t.Errorf("Duration = %v, want 12.5", got.Duration)
	}
}

func TestProbeFileHandlesAStillImage(t *testing.T) {
	store, _, _, _ := newMediaStore(t)
	media.StubTools(t, map[string]string{"ffprobe": ffprobeImageStub})

	got, err := store.ProbeFile(context.Background(), "/x")
	if err != nil {
		t.Fatalf("ProbeFile: %v", err)
	}
	if got.Duration != 0 {
		t.Errorf("Duration = %v, want 0 for a still", got.Duration)
	}
	if got.Width != 640 || got.Height != 480 {
		t.Errorf("dimensions = %dx%d, want 640x480", got.Width, got.Height)
	}
}

func TestProbeFileReportsAMissingTool(t *testing.T) {
	store, _, _, _ := newMediaStore(t)
	media.StubTools(t, map[string]string{})
	if _, err := store.ProbeFile(context.Background(), "/x"); err == nil {
		t.Fatal("ProbeFile succeeded without ffprobe, want an error")
	}
}

func TestProbeFileRejectsUnparseableOutput(t *testing.T) {
	store, _, _, _ := newMediaStore(t)
	media.StubTools(t, map[string]string{"ffprobe": `#!/bin/sh
echo 'not json at all'`})
	if _, err := store.ProbeFile(context.Background(), "/x"); err == nil {
		t.Fatal("ProbeFile accepted non-JSON output")
	}
}

func TestHandleProbeJobUpdatesTheItemAndQueuesThumbnails(t *testing.T) {
	ctx := context.Background()
	store, database, queue, _ := newMediaStore(t)
	user := newUploader(t, database)
	media.StubTools(t, map[string]string{})

	saved, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(mp4Bytes), Filename: "clip.mp4", UploaderID: user.ID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	media.StubTools(t, map[string]string{"ffprobe": ffprobeStub})
	payload, _ := json.Marshal(media.ProbePayload{ItemID: saved.Item.ID})
	if err := store.HandleProbeJob(ctx, db.Job{Type: "probe", Payload: payload}); err != nil {
		t.Fatalf("HandleProbeJob: %v", err)
	}

	got, err := database.ItemByID(ctx, saved.Item.ID)
	if err != nil {
		t.Fatalf("ItemByID: %v", err)
	}
	if got.Width != 1920 || got.Height != 1080 || got.Duration != 12.5 {
		t.Errorf("item after probe = %dx%d %vs, want 1920x1080 12.5s", got.Width, got.Height, got.Duration)
	}
	// One probe from Save, then one thumbnail from the probe handler.
	if len(queue.types) != 2 || queue.types[1] != "thumbnail" {
		t.Errorf("enqueued %v, want the probe handler to queue thumbnailing", queue.types)
	}
}

func TestHandleProbeJobIgnoresADeletedItem(t *testing.T) {
	ctx := context.Background()
	store, database, _, _ := newMediaStore(t)
	user := newUploader(t, database)
	media.StubTools(t, map[string]string{})

	saved, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(mp4Bytes), Filename: "clip.mp4", UploaderID: user.ID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := database.SoftDeleteItem(ctx, saved.Item.ID, user); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}

	payload, _ := json.Marshal(media.ProbePayload{ItemID: saved.Item.ID})
	// A vanished item is not a failure: retrying would never help.
	if err := store.HandleProbeJob(ctx, db.Job{Type: "probe", Payload: payload}); err != nil {
		t.Fatalf("HandleProbeJob on a deleted item = %v, want nil", err)
	}
}

func TestHandleProbeJobRejectsAMalformedPayload(t *testing.T) {
	store, _, _, _ := newMediaStore(t)
	media.StubTools(t, map[string]string{"ffprobe": ffprobeStub})
	if err := store.HandleProbeJob(context.Background(), db.Job{Type: "probe", Payload: []byte("{{{")}); err == nil {
		t.Fatal("HandleProbeJob accepted a malformed payload")
	}
}

func TestHandleThumbnailJobWritesBothSizes(t *testing.T) {
	ctx := context.Background()
	store, database, _, cfg := newMediaStore(t)
	user := newUploader(t, database)
	media.StubTools(t, map[string]string{})

	saved, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(pngBytes), Filename: "cat.png", UploaderID: user.ID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	media.StubTools(t, map[string]string{"ffmpeg": ffmpegStub})
	payload, _ := json.Marshal(media.ThumbnailPayload{ItemID: saved.Item.ID})
	if err := store.HandleThumbnailJob(ctx, db.Job{Type: "thumbnail", Payload: payload}); err != nil {
		t.Fatalf("HandleThumbnailJob: %v", err)
	}

	for _, size := range media.ThumbSizes {
		path := media.ThumbPath(cfg.ThumbsDir(), saved.Item.ContentHash, size)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("thumbnail %d is missing: %v", size, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("thumbnail %d is empty", size)
		}
	}
}

func TestThumbnailCommandShape(t *testing.T) {
	ctx := context.Background()
	store, _, _, cfg := newMediaStore(t)
	argsFile := cfg.TmpDir() + "/ffmpeg-args.txt"
	media.StubTools(t, map[string]string{
		"ffmpeg": `#!/bin/sh
echo "$@" >> "` + argsFile + `"
for last in "$@"; do :; done
printf 'RIFF__' > "$last"`,
	})

	dst := cfg.TmpDir() + "/out.webp"
	if err := store.GenerateThumbnail(ctx, "/src.mp4", dst, 320, true, 30); err != nil {
		t.Fatalf("GenerateThumbnail: %v", err)
	}
	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("ffmpeg was not invoked: %v", err)
	}
	args := string(recorded)
	for _, want := range []string{"-frames:v 1", "libwebp", "force_original_aspect_ratio=decrease", "min(320,iw)"} {
		if !strings.Contains(args, want) {
			t.Errorf("ffmpeg args are missing %q\ngot: %s", want, args)
		}
	}
	if !strings.Contains(args, "-ss") {
		t.Error("a video thumbnail did not seek; frame 0 is very often black")
	}
}

func TestThumbnailDoesNotSeekForStills(t *testing.T) {
	ctx := context.Background()
	store, _, _, cfg := newMediaStore(t)
	argsFile := cfg.TmpDir() + "/ffmpeg-args.txt"
	media.StubTools(t, map[string]string{
		"ffmpeg": `#!/bin/sh
echo "$@" >> "` + argsFile + `"
for last in "$@"; do :; done
printf 'RIFF__' > "$last"`,
	})

	if err := store.GenerateThumbnail(ctx, "/src.png", cfg.TmpDir()+"/out.webp", 320, false, 0); err != nil {
		t.Fatalf("GenerateThumbnail: %v", err)
	}
	recorded, _ := os.ReadFile(argsFile)
	if strings.Contains(string(recorded), "-ss") {
		t.Error("a still image thumbnail used -ss, which can produce an empty output")
	}
}

func TestGenerateSocialPreviewUsesBoundedJPEG(t *testing.T) {
	ctx := context.Background()
	store, _, _, cfg := newMediaStore(t)
	argsFile := cfg.TmpDir() + "/social-ffmpeg-args.txt"
	media.StubTools(t, map[string]string{
		"ffmpeg": `#!/bin/sh
echo "$@" >> "` + argsFile + `"
for last in "$@"; do :; done
printf '\377\330\377' > "$last"`,
	})

	dst := cfg.TmpDir() + "/social.jpg"
	if err := store.GenerateSocialPreview(ctx, "/src.gif", dst, false, 0); err != nil {
		t.Fatalf("GenerateSocialPreview: %v", err)
	}
	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("ffmpeg was not invoked: %v", err)
	}
	args := string(recorded)
	for _, want := range []string{"-frames:v 1", "mjpeg", "min(1024,iw)", "-q:v 3"} {
		if !strings.Contains(args, want) {
			t.Errorf("social preview args are missing %q\ngot: %s", want, args)
		}
	}
	if strings.Contains(args, "-ss") {
		t.Error("a GIF social preview sought into the animation instead of using frame one")
	}
}

func TestGenerateSocialAnimationUsesH264MP4(t *testing.T) {
	ctx := context.Background()
	store, _, _, cfg := newMediaStore(t)
	argsFile := cfg.TmpDir() + "/animation-ffmpeg-args.txt"
	media.StubTools(t, map[string]string{
		"ffmpeg": `#!/bin/sh
echo "$@" >> "` + argsFile + `"
for last in "$@"; do :; done
printf 'mp4' > "$last"`,
	})

	dst := cfg.TmpDir() + "/social.mp4"
	if err := store.GenerateSocialAnimation(ctx, "/src.gif", dst); err != nil {
		t.Fatalf("GenerateSocialAnimation: %v", err)
	}
	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("ffmpeg was not invoked: %v", err)
	}
	args := string(recorded)
	for _, want := range []string{"libx264", "yuv420p", "+faststart", "-f mp4", "min(1280,iw)"} {
		if !strings.Contains(args, want) {
			t.Errorf("social animation args are missing %q\ngot: %s", want, args)
		}
	}
}

// TestHandleThumbnailJobWritesAPosterForAGif pins down the diagnosis behind
// the "GIF previews are broken" report: nothing in this pipeline is actually
// broken. IsVideoMime("image/gif") is false, so HandleThumbnailJob routes a
// GIF down the same non-seeking still-image branch as a JPEG or PNG, and
// ffmpeg's frame-0 extraction produces a normal static webp poster, the
// same artifact a real (non-stubbed) ffmpeg run against a genuine animated
// GIF was verified by hand to produce during development of this feature.
// The real gap this task closes is that nothing ever offered a way to see
// the GIF *animate* as a preview; it is not that the poster was empty or
// frozen wrong.
func TestHandleThumbnailJobWritesAPosterForAGif(t *testing.T) {
	ctx := context.Background()
	store, database, _, cfg := newMediaStore(t)
	user := newUploader(t, database)
	media.StubTools(t, map[string]string{})

	saved, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(gifBytes), Filename: "party.gif", UploaderID: user.ID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Item.Mime != "image/gif" {
		t.Fatalf("Mime = %q, want image/gif", saved.Item.Mime)
	}
	if media.IsVideoMime(saved.Item.Mime) {
		t.Error("IsVideoMime(image/gif) = true; a GIF poster must not seek like a video clip")
	}
	if !media.IsGifMime(saved.Item.Mime) {
		t.Error("IsGifMime(image/gif) = false; the web layer needs this to offer a hover preview")
	}

	media.StubTools(t, map[string]string{"ffmpeg": ffmpegStub})
	payload, _ := json.Marshal(media.ThumbnailPayload{ItemID: saved.Item.ID})
	if err := store.HandleThumbnailJob(ctx, db.Job{Type: "thumbnail", Payload: payload}); err != nil {
		t.Fatalf("HandleThumbnailJob: %v", err)
	}

	for _, size := range media.ThumbSizes {
		path := media.ThumbPath(cfg.ThumbsDir(), saved.Item.ContentHash, size)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("gif thumbnail %d is missing: %v", size, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("gif thumbnail %d is empty", size)
		}
	}
}

// TestThumbnailDoesNotSeekForAGif is TestThumbnailDoesNotSeekForStills'
// sibling, named for the specific case the diagnosis turned on: a -ss seek
// on a GIF (a still by GenerateThumbnail's own split) would risk landing
// past a very short animation and producing nothing.
func TestThumbnailDoesNotSeekForAGif(t *testing.T) {
	ctx := context.Background()
	store, _, _, cfg := newMediaStore(t)
	argsFile := cfg.TmpDir() + "/ffmpeg-args.txt"
	media.StubTools(t, map[string]string{
		"ffmpeg": `#!/bin/sh
echo "$@" >> "` + argsFile + `"
for last in "$@"; do :; done
printf 'RIFF__' > "$last"`,
	})

	isVideo := media.IsVideoMime("image/gif") // false: this is the routing decision under test
	if err := store.GenerateThumbnail(ctx, "/src.gif", cfg.TmpDir()+"/out.webp", 320, isVideo, 0); err != nil {
		t.Fatalf("GenerateThumbnail: %v", err)
	}
	recorded, _ := os.ReadFile(argsFile)
	if strings.Contains(string(recorded), "-ss") {
		t.Error("a GIF thumbnail used -ss; frame 0 of a GIF is always a complete frame and needs no seek")
	}
}

func TestHandleThumbnailJobReportsAMissingTool(t *testing.T) {
	ctx := context.Background()
	store, database, _, _ := newMediaStore(t)
	user := newUploader(t, database)
	media.StubTools(t, map[string]string{})

	saved, err := store.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(pngBytes), Filename: "cat.png", UploaderID: user.ID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	payload, _ := json.Marshal(media.ThumbnailPayload{ItemID: saved.Item.ID})
	err = store.HandleThumbnailJob(ctx, db.Job{Type: "thumbnail", Payload: payload})
	if err == nil {
		t.Fatal("HandleThumbnailJob succeeded without ffmpeg, want an error the UI can show")
	}
	if !strings.Contains(err.Error(), "ffmpeg") {
		t.Errorf("error = %v, want it to name the missing tool", err)
	}
}
