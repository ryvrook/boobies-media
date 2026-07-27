package media

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// StubTools installs fake executables on PATH for the duration of a test.
//
// The keys are command names ("ffmpeg", "ffprobe", "cwebp"); the values are
// shell script bodies, which must begin with a "#!" line. PATH is replaced
// entirely, so a command with no stub is genuinely missing: that is how the
// missing-tool paths get tested.
//
// This works because ExecRunner resolves commands with exec.LookPath at call
// time rather than caching them at startup. t.Setenv restores PATH when the
// test finishes.
func StubTools(t *testing.T, scripts map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub tools rely on #! scripts, which Windows does not execute")
	}
	dir := t.TempDir()
	for name, body := range scripts {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body+"\n"), 0o755); err != nil {
			t.Fatalf("StubTools: write %s: %v", name, err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}
