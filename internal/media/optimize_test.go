package media

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestDecideWebpConversion(t *testing.T) {
	stillPNG := buildPNG(t, 100, 100, 8, PNGColorRGB)
	rgbaPNG := buildPNG(t, 100, 100, 8, PNGColorRGBA)
	apng := buildPNG(t, 100, 100, 8, PNGColorRGBA, "acTL")
	deepPNG := buildPNG(t, 100, 100, 16, PNGColorRGB)
	palettePNG := buildPNG(t, 100, 100, 8, 3)
	hugePNG := buildPNG(t, 16384, 100, 8, PNGColorRGB)

	cases := []struct {
		name    string
		mime    string
		header  []byte
		enabled bool
		want    bool
	}{
		{"8-bit RGB PNG", "image/png", stillPNG, true, true},
		{"8-bit RGBA PNG", "image/png", rgbaPNG, true, true},
		{"setting disabled", "image/png", stillPNG, false, false},
		{"APNG", "image/png", apng, true, false},
		{"16-bit PNG", "image/png", deepPNG, true, false},
		{"palette PNG", "image/png", palettePNG, true, false},
		{"oversize PNG", "image/png", hugePNG, true, false},
		{"JPEG", "image/jpeg", []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0, 0, 0, 0, 0, 0, 0}, true, false},
		{"GIF", "image/gif", []byte("GIF89a\x00\x00\x00\x00\x00\x00"), true, false},
		{"webp", "image/webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), true, false},
		{"mp4", "video/mp4", []byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, true, false},
		{"garbage claiming to be png", "image/png", []byte("not really a png at all"), true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideWebpConversion(tc.mime, tc.header, tc.enabled)
			if got.Convert != tc.want {
				t.Errorf("Convert = %v, want %v (reason: %s)", got.Convert, tc.want, got.Reason)
			}
			if !got.Convert && got.Reason == "" {
				t.Error("a refusal must carry a reason, so the admin page can explain itself")
			}
		})
	}
}

func TestOptimizeConvertsWhenTheOutputIsSmaller(t *testing.T) {
	dir := t.TempDir()
	src := writeTemp(t, dir, "in.png", buildPNG(t, 100, 100, 8, PNGColorRGB))
	// The stub writes a deliberately tiny "webp" to the -o path.
	StubTools(t, map[string]string{
		"cwebp": `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then shift; out="$1"; fi
  shift
done
printf 'RIFF____WEBPVP8L' > "$out"`,
	})

	opt := NewOptimizer(NewExecRunner(), dir)
	header, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	path, mime, converted, err := opt.Optimize(context.Background(), src, "image/png", header, true)
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if !converted {
		t.Fatal("converted = false, want true for a smaller output")
	}
	if mime != "image/webp" {
		t.Errorf("mime = %q, want image/webp", mime)
	}
	if path == src {
		t.Error("the returned path is still the original")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the converted file does not exist: %v", err)
	}
}

func TestOptimizeKeepsTheOriginalWhenTheOutputIsLarger(t *testing.T) {
	dir := t.TempDir()
	src := writeTemp(t, dir, "in.png", buildPNG(t, 100, 100, 8, PNGColorRGB))
	StubTools(t, map[string]string{
		"cwebp": `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then shift; out="$1"; fi
  shift
done
# Deliberately bigger than any test PNG.
dd if=/dev/zero of="$out" bs=1024 count=64 2>/dev/null`,
	})

	opt := NewOptimizer(NewExecRunner(), dir)
	header, _ := os.ReadFile(src)
	path, mime, converted, err := opt.Optimize(context.Background(), src, "image/png", header, true)
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if converted {
		t.Error("converted = true although the webp was larger")
	}
	if path != src || mime != "image/png" {
		t.Errorf("got (%q, %q), want the original (%q, image/png)", path, mime, src)
	}
}

func TestOptimizeSilentlyKeepsTheOriginalOnToolFailure(t *testing.T) {
	dir := t.TempDir()
	src := writeTemp(t, dir, "in.png", buildPNG(t, 100, 100, 8, PNGColorRGB))
	StubTools(t, map[string]string{
		"cwebp": `#!/bin/sh
echo "cwebp: cannot read input" >&2
exit 1`,
	})

	opt := NewOptimizer(NewExecRunner(), dir)
	header, _ := os.ReadFile(src)
	path, mime, converted, err := opt.Optimize(context.Background(), src, "image/png", header, true)
	if err != nil {
		t.Fatalf("Optimize returned an error for a cwebp failure: %v (it must degrade silently)", err)
	}
	if converted || path != src || mime != "image/png" {
		t.Errorf("got (%q, %q, %v), want the original kept", path, mime, converted)
	}
}

func TestOptimizeSilentlyKeepsTheOriginalWhenCwebpIsMissing(t *testing.T) {
	dir := t.TempDir()
	src := writeTemp(t, dir, "in.png", buildPNG(t, 100, 100, 8, PNGColorRGB))
	StubTools(t, map[string]string{}) // cwebp absent, as on many stock systems

	opt := NewOptimizer(NewExecRunner(), dir)
	header, _ := os.ReadFile(src)
	path, mime, converted, err := opt.Optimize(context.Background(), src, "image/png", header, true)
	if err != nil {
		t.Fatalf("Optimize errored when cwebp was missing: %v", err)
	}
	if converted || path != src || mime != "image/png" {
		t.Error("a missing cwebp must leave the original untouched")
	}
}

func TestOptimizeSkipsWorkEntirelyWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	src := writeTemp(t, dir, "in.png", buildPNG(t, 100, 100, 8, PNGColorRGB))
	marker := filepath.Join(dir, "cwebp-was-called")
	StubTools(t, map[string]string{
		"cwebp": `#!/bin/sh
touch "` + marker + `"`,
	})

	opt := NewOptimizer(NewExecRunner(), dir)
	header, _ := os.ReadFile(src)
	if _, _, converted, err := opt.Optimize(context.Background(), src, "image/png", header, false); err != nil || converted {
		t.Fatalf("Optimize with the setting off: converted=%v err=%v", converted, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("cwebp was invoked even though auto_webp is off")
	}
}

func TestOptimizeKeepsOriginalOnScratchDirFailure(t *testing.T) {
	dir := t.TempDir()
	src := writeTemp(t, dir, "in.png", buildPNG(t, 100, 100, 8, PNGColorRGB))
	// A TmpDir that does not exist makes os.CreateTemp fail deterministically
	// without touching permissions (which root can ignore) and without
	// leaving anything behind to clean up: the directory was never created.
	missingTmpDir := filepath.Join(dir, "does-not-exist")

	opt := NewOptimizer(NewExecRunner(), missingTmpDir)
	header, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	path, mime, converted, err := opt.Optimize(context.Background(), src, "image/png", header, true)
	if err != nil {
		t.Fatalf("Optimize returned an error for a scratch-dir failure: %v (srcPath is untouched, so this must degrade silently)", err)
	}
	if converted || path != src || mime != "image/png" {
		t.Errorf("got (%q, %q, %v), want the original kept when scratch space is unwritable", path, mime, converted)
	}
}

func TestOptimizeKeepsOriginalWhenCwebpOutputIsRiffButNotWebp(t *testing.T) {
	dir := t.TempDir()
	src := writeTemp(t, dir, "in.png", buildPNG(t, 100, 100, 8, PNGColorRGB))
	// Smaller than the source and starts with "RIFF", but it is WAV-shaped,
	// not webp: RIFF alone (the old check) would have accepted this.
	StubTools(t, map[string]string{
		"cwebp": `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then shift; out="$1"; fi
  shift
done
printf 'RIFF____WAVEfmt ' > "$out"`,
	})

	opt := NewOptimizer(NewExecRunner(), dir)
	header, _ := os.ReadFile(src)
	path, mime, converted, err := opt.Optimize(context.Background(), src, "image/png", header, true)
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if converted {
		t.Error("converted = true for a RIFF payload that is not actually webp (WAV shares the RIFF prefix)")
	}
	if path != src || mime != "image/png" {
		t.Errorf("got (%q, %q), want the original (%q, image/png)", path, mime, src)
	}
}

func TestOptimizeUsesLosslessAndKeepsMetadata(t *testing.T) {
	dir := t.TempDir()
	src := writeTemp(t, dir, "in.png", buildPNG(t, 100, 100, 8, PNGColorRGB))
	argsFile := filepath.Join(dir, "args.txt")
	StubTools(t, map[string]string{
		"cwebp": `#!/bin/sh
echo "$@" > "` + argsFile + `"
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then shift; out="$1"; fi
  shift
done
printf 'RIFF__' > "$out"`,
	})

	opt := NewOptimizer(NewExecRunner(), dir)
	header, _ := os.ReadFile(src)
	if _, _, _, err := opt.Optimize(context.Background(), src, "image/png", header, true); err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("cwebp was never invoked: %v", err)
	}
	args := string(recorded)
	if !strings.Contains(args, "-lossless") {
		t.Error("cwebp was called without -lossless; the zero-quality-loss guarantee depends on it")
	}
	if !strings.Contains(args, "-metadata all") {
		t.Error("cwebp was called without -metadata all; EXIF and ICC profiles would be dropped")
	}
}
