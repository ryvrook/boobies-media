// Package config parses server configuration from flags and environment
// variables, and owns the on-disk layout of the data directory.
package config

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the fully resolved server configuration.
type Config struct {
	// Addr is the TCP address the HTTP server listens on, e.g. ":8080".
	Addr string
	// DataDir is the root of all persistent state (DB, media, thumbnails).
	DataDir string
	// BaseURL is the public origin, used to build absolute share and
	// OpenGraph URLs. Never has a trailing slash.
	BaseURL string
	// Workers is the number of job-queue goroutines.
	Workers int
	// SecureCookies sets the Secure attribute on the session cookie. True in
	// production; disable only for plain-HTTP local development and tests.
	SecureCookies bool
}

// Load resolves configuration from environment variables, then applies flag
// overrides. args must exclude the program name. getenv is injected so tests
// need not mutate the real process environment.
func Load(args []string, getenv func(string) string) (*Config, error) {
	cfg := &Config{
		// Loopback by default: the tunnel is the only intended way in, and a
		// home server that accidentally binds every interface is one port-
		// forward away from an open media library.
		Addr:          envString(getenv, "BM_ADDR", "127.0.0.1:8080"),
		DataDir:       envString(getenv, "BM_DATA_DIR", "data"),
		BaseURL:       envString(getenv, "BM_BASE_URL", "http://localhost:8080"),
		Workers:       2,
		SecureCookies: envString(getenv, "BM_INSECURE_COOKIES", "") == "",
	}
	if raw := envString(getenv, "BM_WORKERS", ""); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("config: BM_WORKERS=%q is not a number", raw)
		}
		cfg.Workers = n
	}

	insecure := !cfg.SecureCookies
	fs := flag.NewFlagSet("boobies-media", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "listen address, e.g. 127.0.0.1:8080")
	fs.StringVar(&cfg.DataDir, "data", cfg.DataDir, "data directory")
	fs.StringVar(&cfg.BaseURL, "base-url", cfg.BaseURL, "public base URL, e.g. https://media.example.com")
	fs.IntVar(&cfg.Workers, "workers", cfg.Workers, "job queue worker count")
	fs.BoolVar(&insecure, "insecure-cookies", insecure, "drop the Secure attribute on session cookies (local dev only)")
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg.SecureCookies = !insecure

	if strings.TrimSpace(cfg.Addr) == "" {
		return nil, fmt.Errorf("config: addr must not be empty")
	}
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, fmt.Errorf("config: data dir must not be empty")
	}
	if cfg.Workers < 1 {
		return nil, fmt.Errorf("config: workers must be at least 1, got %d", cfg.Workers)
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("config: base-url %q is not a valid URL: %w", cfg.BaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("config: base-url %q must start with http:// or https://", cfg.BaseURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("config: base-url %q has no host", cfg.BaseURL)
	}
	return cfg, nil
}

func envString(getenv func(string) string, key, def string) string {
	if getenv == nil {
		return def
	}
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

// DBPath is the SQLite database file.
func (c *Config) DBPath() string { return filepath.Join(c.DataDir, "media.db") }

// FilesDir holds content-addressed originals.
func (c *Config) FilesDir() string { return filepath.Join(c.DataDir, "files") }

// ThumbsDir holds generated thumbnails.
func (c *Config) ThumbsDir() string { return filepath.Join(c.DataDir, "thumbs") }

// AvatarsDir holds user avatars, named by content hash.
func (c *Config) AvatarsDir() string { return filepath.Join(c.DataDir, "avatars") }

// BackupsDir holds nightly VACUUM INTO snapshots.
func (c *Config) BackupsDir() string { return filepath.Join(c.DataDir, "backups") }

// CookiesDir holds optional per-extractor cookie files for yt-dlp/gallery-dl.
func (c *Config) CookiesDir() string { return filepath.Join(c.DataDir, "cookies") }

// TmpDir holds in-flight uploads. It lives under DataDir so the final move
// into FilesDir is an atomic rename on the same filesystem.
func (c *Config) TmpDir() string { return filepath.Join(c.DataDir, "tmp") }

// EnsureDirs creates the data directory tree and verifies it is writable.
// A failure here is a startup hard-fail: the caller must exit.
func (c *Config) EnsureDirs() error {
	for _, dir := range []string{
		c.DataDir, c.FilesDir(), c.ThumbsDir(), c.AvatarsDir(), c.BackupsDir(), c.CookiesDir(), c.TmpDir(),
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("config: cannot create %s: %w", dir, err)
		}
	}
	// Backups hold a full copy of the catalog, including session tokens and
	// password hashes, so unlike the rest of the data tree nothing else needs
	// group access to them. Chmod explicitly rather than relying on the
	// MkdirAll mode above, so a backups directory left more permissive by an
	// older deploy is tightened on every startup too.
	if err := os.Chmod(c.BackupsDir(), 0o700); err != nil {
		return fmt.Errorf("config: cannot restrict permissions on %s: %w", c.BackupsDir(), err)
	}
	probe := filepath.Join(c.DataDir, ".write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o640); err != nil {
		return fmt.Errorf("config: data directory %s is not writable: %w", c.DataDir, err)
	}
	if err := os.Remove(probe); err != nil {
		return fmt.Errorf("config: cannot clean up write probe in %s: %w", c.DataDir, err)
	}
	return nil
}
