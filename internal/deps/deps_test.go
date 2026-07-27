package deps

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeTool creates an executable script that prints version on --version.
func writeFakeTool(t *testing.T, dir, name, version string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shell tools are not portable to Windows")
	}
	script := "#!/bin/sh\necho \"" + version + "\"\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tool %s: %v", name, err)
	}
}

func TestRequiredToolList(t *testing.T) {
	want := []string{"yt-dlp", "gallery-dl", "ffmpeg", "ffprobe", "cwebp"}
	if len(Required) != len(want) {
		t.Fatalf("Required = %v, want %v", Required, want)
	}
	for i := range want {
		if Required[i] != want[i] {
			t.Fatalf("Required = %v, want %v", Required, want)
		}
	}
}

func TestProbeFindsToolAndReadsVersion(t *testing.T) {
	dir := t.TempDir()
	writeFakeTool(t, dir, "yt-dlp", "2026.07.01")
	t.Setenv("PATH", dir)

	statuses := Probe(context.Background(), []string{"yt-dlp"})
	if len(statuses) != 1 {
		t.Fatalf("Probe returned %d statuses, want 1", len(statuses))
	}
	got := statuses[0]
	if !got.OK {
		t.Fatalf("OK = false, want true (Err = %q)", got.Err)
	}
	if got.Name != "yt-dlp" {
		t.Errorf("Name = %q, want \"yt-dlp\"", got.Name)
	}
	if got.Version != "2026.07.01" {
		t.Errorf("Version = %q, want \"2026.07.01\"", got.Version)
	}
	if !strings.HasSuffix(got.Path, "yt-dlp") {
		t.Errorf("Path = %q, want it to end in yt-dlp", got.Path)
	}
}

func TestProbeReportsMissingToolWithoutFailing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	statuses := Probe(context.Background(), []string{"definitely-not-installed"})
	if len(statuses) != 1 {
		t.Fatalf("Probe returned %d statuses, want 1", len(statuses))
	}
	got := statuses[0]
	if got.OK {
		t.Error("OK = true for a missing tool, want false")
	}
	if got.Err == "" {
		t.Error("Err is empty for a missing tool, want an explanation")
	}
	if got.Version != "" {
		t.Errorf("Version = %q for a missing tool, want empty", got.Version)
	}
}

func TestProbeKeepsOnlyTheFirstVersionLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"),
		[]byte("#!/bin/sh\nprintf 'ffmpeg version 7.1\\nbuilt with gcc\\nconfiguration: --lots\\n'\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	t.Setenv("PATH", dir)

	got := Probe(context.Background(), []string{"ffmpeg"})[0]
	if got.Version != "ffmpeg version 7.1" {
		t.Errorf("Version = %q, want just the first line", got.Version)
	}
}

func TestProbePreservesOrder(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	names := []string{"a", "b", "c"}
	statuses := Probe(context.Background(), names)
	if len(statuses) != 3 {
		t.Fatalf("Probe returned %d statuses, want 3", len(statuses))
	}
	for i, name := range names {
		if statuses[i].Name != name {
			t.Errorf("statuses[%d].Name = %q, want %q", i, statuses[i].Name, name)
		}
	}
}

// ffmpegStyleVersionScript rejects "--version" the way real ffmpeg/ffprobe
// do (their argv parser strips one leading dash, turning "--version" into
// the unregistered option "-version" with a leftover dash) and only accepts
// the single-dash "-version" real installs use. This is Finding 1's
// regression test: Probe must classify a genuinely-present tool as OK, not
// misreport it as missing because it asked for the wrong flag.
func ffmpegStyleVersionScript(version string) string {
	return "#!/bin/sh\n" +
		"if [ \"$1\" = \"-version\" ]; then\n" +
		"  echo \"" + version + "\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo \"Unrecognized option '$1'.\" >&2\n" +
		"exit 1\n"
}

func writeScript(t *testing.T, dir, name, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shell tools are not portable to Windows")
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("write fake tool %s: %v", name, err)
	}
}

func TestProbeReportsFfmpegFamilyToolsPresentUsingTheSingleDashFlag(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "ffmpeg", ffmpegStyleVersionScript("ffmpeg version n8.1.2"))
	writeScript(t, dir, "ffprobe", ffmpegStyleVersionScript("ffprobe version n8.1.2"))
	writeScript(t, dir, "cwebp", ffmpegStyleVersionScript("1.6.0"))
	t.Setenv("PATH", dir)

	for _, status := range Probe(context.Background(), []string{"ffmpeg", "ffprobe", "cwebp"}) {
		if !status.OK {
			t.Errorf("%s: OK = false (Err = %q), want true: a present tool must not be misreported as missing", status.Name, status.Err)
		}
	}
}

// TestProbeStillUsesDoubleDashForPythonTools guards the other half of the
// fix: yt-dlp/gallery-dl are argparse-based and only accept "--version"; a
// blanket switch to "-version" for every tool would break them.
func TestProbeStillUsesDoubleDashForPythonTools(t *testing.T) {
	argparseStyleScript := func(version string) string {
		return "#!/bin/sh\n" +
			"if [ \"$1\" = \"--version\" ]; then\n" +
			"  echo \"" + version + "\"\n" +
			"  exit 0\n" +
			"fi\n" +
			"echo \"error: unrecognized arguments: $1\" >&2\n" +
			"exit 2\n"
	}
	dir := t.TempDir()
	writeScript(t, dir, "yt-dlp", argparseStyleScript("2026.07.04"))
	writeScript(t, dir, "gallery-dl", argparseStyleScript("1.30.0"))
	t.Setenv("PATH", dir)

	for _, status := range Probe(context.Background(), []string{"yt-dlp", "gallery-dl"}) {
		if !status.OK {
			t.Errorf("%s: OK = false (Err = %q), want true", status.Name, status.Err)
		}
	}
}

func TestAllOK(t *testing.T) {
	if !AllOK(nil) {
		t.Error("AllOK(nil) = false, want true")
	}
	if !AllOK([]Status{{OK: true}, {OK: true}}) {
		t.Error("AllOK with all-OK statuses = false, want true")
	}
	if AllOK([]Status{{OK: true}, {OK: false}}) {
		t.Error("AllOK with one failure = true, want false")
	}
}
