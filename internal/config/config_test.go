package config

import (
	"os"
	"path/filepath"
	"testing"
)

func noEnv(string) string { return "" }

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "127.0.0.1:8080" {
		t.Errorf("Addr = %q, want \"127.0.0.1:8080\"", cfg.Addr)
	}
	if cfg.DataDir != "data" {
		t.Errorf("DataDir = %q, want \"data\"", cfg.DataDir)
	}
	if cfg.BaseURL != "http://localhost:8080" {
		t.Errorf("BaseURL = %q, want \"http://localhost:8080\"", cfg.BaseURL)
	}
	if cfg.Workers != 2 {
		t.Errorf("Workers = %d, want 2", cfg.Workers)
	}
	if !cfg.SecureCookies {
		t.Error("SecureCookies = false, want true by default")
	}
}

func TestLoadEnv(t *testing.T) {
	env := map[string]string{
		"BM_ADDR":             "127.0.0.1:9000",
		"BM_DATA_DIR":         "/srv/media",
		"BM_BASE_URL":         "https://media.example.com/",
		"BM_WORKERS":          "4",
		"BM_INSECURE_COOKIES": "1",
	}
	cfg, err := Load(nil, func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9000" {
		t.Errorf("Addr = %q", cfg.Addr)
	}
	if cfg.DataDir != "/srv/media" {
		t.Errorf("DataDir = %q", cfg.DataDir)
	}
	if cfg.BaseURL != "https://media.example.com" {
		t.Errorf("BaseURL = %q, want trailing slash stripped", cfg.BaseURL)
	}
	if cfg.Workers != 4 {
		t.Errorf("Workers = %d", cfg.Workers)
	}
	if cfg.SecureCookies {
		t.Error("SecureCookies = true, want false when BM_INSECURE_COOKIES is set")
	}
}

func TestFlagsBeatEnv(t *testing.T) {
	env := map[string]string{"BM_ADDR": "127.0.0.1:9000"}
	cfg, err := Load([]string{"-addr", "0.0.0.0:1234"}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "0.0.0.0:1234" {
		t.Errorf("Addr = %q, want the flag to win over the env var", cfg.Addr)
	}
}

func TestLoadRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"zero workers", []string{"-workers", "0"}},
		{"base URL without scheme", []string{"-base-url", "media.example.com"}},
		{"empty data dir", []string{"-data", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(tc.args, noEnv); err == nil {
				t.Fatal("Load succeeded, want an error")
			}
		})
	}
}

func TestPaths(t *testing.T) {
	cfg, err := Load([]string{"-data", "/srv/media"}, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{
		"db":      "/srv/media/media.db",
		"files":   "/srv/media/files",
		"thumbs":  "/srv/media/thumbs",
		"avatars": "/srv/media/avatars",
		"backups": "/srv/media/backups",
		"cookies": "/srv/media/cookies",
		"tmp":     "/srv/media/tmp",
	}
	got := map[string]string{
		"db":      cfg.DBPath(),
		"files":   cfg.FilesDir(),
		"thumbs":  cfg.ThumbsDir(),
		"avatars": cfg.AvatarsDir(),
		"backups": cfg.BackupsDir(),
		"cookies": cfg.CookiesDir(),
		"tmp":     cfg.TmpDir(),
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
}

func TestEnsureDirsCreatesTreeAndIsIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	cfg, err := Load([]string{"-data", root}, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := cfg.EnsureDirs(); err != nil {
			t.Fatalf("EnsureDirs (call %d): %v", i+1, err)
		}
	}
	for _, dir := range []string{cfg.FilesDir(), cfg.ThumbsDir(), cfg.AvatarsDir(), cfg.BackupsDir(), cfg.CookiesDir(), cfg.TmpDir()} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", dir)
		}
	}
}

// TestEnsureDirsRestrictsBackupsDirToOwnerOnly is the regression test for
// Task 12's review: backups hold a full copy of the catalog, including
// session tokens and password hashes, so BackupsDir must never be group or
// other accessible, unlike the rest of the data tree which is 0750. This
// also proves EnsureDirs tightens a BackupsDir left more permissive by an
// older deploy, not just one it creates fresh.
func TestEnsureDirsRestrictsBackupsDirToOwnerOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, permission bits are not enforced")
	}
	root := filepath.Join(t.TempDir(), "data")
	cfg, err := Load([]string{"-data", root}, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	info, err := os.Stat(cfg.BackupsDir())
	if err != nil {
		t.Fatalf("stat %s: %v", cfg.BackupsDir(), err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("BackupsDir mode = %v, want 0700", info.Mode().Perm())
	}

	// Simulate a directory left behind at the old, looser 0750 by a prior
	// version of this server.
	if err := os.Chmod(cfg.BackupsDir(), 0o750); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs (retightening): %v", err)
	}
	info, err = os.Stat(cfg.BackupsDir())
	if err != nil {
		t.Fatalf("stat %s: %v", cfg.BackupsDir(), err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("BackupsDir mode after retightening = %v, want 0700", info.Mode().Perm())
	}
}

func TestEnsureDirsFailsOnUnwritableParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission checks do not apply")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	cfg, err := Load([]string{"-data", filepath.Join(parent, "data")}, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.EnsureDirs(); err == nil {
		t.Fatal("EnsureDirs succeeded on an unwritable parent, want an error")
	}
}
