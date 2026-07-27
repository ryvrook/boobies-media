package web_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"boobies-media/internal/auth"
	"boobies-media/internal/config"
	"boobies-media/internal/db"
	"boobies-media/internal/deps"
	"boobies-media/internal/web"
)

// bootServer starts a real HTTP server on an ephemeral port backed by a temp
// data directory, exactly as production would, and returns its base URL.
func bootServer(t *testing.T) (string, *db.Store) {
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

	// A probe against tools that are absent must not stop the server booting.
	statuses := deps.Probe(context.Background(), []string{"definitely-not-installed"})
	if deps.AllOK(statuses) {
		t.Fatal("the fake tool probe unexpectedly succeeded")
	}

	srv, err := web.New(cfg, store, statuses)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return newTestServer(t, srv), store
}

// newTestServer starts handler on an ephemeral port and returns its base URL.
func newTestServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // inspect redirects instead of following
		},
	}
}

func seedUser(t *testing.T, store *db.Store, username, password string) {
	t.Helper()
	hash, err := auth.HashPasswordWithParams(password, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := store.CreateUser(context.Background(), username, "Display "+username, hash, "", false); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
}

func TestEndToEndLoginBrowseLogout(t *testing.T) {
	base, store := bootServer(t)
	seedUser(t, store, "aiden", "hunter2")
	client := newClient(t)

	// 1. Anonymous browse is redirected to the login page.
	resp, err := client.Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("anonymous GET / = %d, want 302", resp.StatusCode)
	}

	// 2. The login page is reachable anonymously.
	resp, err = client.Get(base + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), `name="username"`) {
		t.Fatal("the login page has no username field")
	}

	// 3. Log in.
	resp, err = client.PostForm(base+"/login", url.Values{"username": {"aiden"}, "password": {"hunter2"}})
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("POST /login = %d, want 302", resp.StatusCode)
	}

	// 4. Browse now renders for the signed-in user.
	resp, err = client.Get(base + "/")
	if err != nil {
		t.Fatalf("GET / after login: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / after login = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Display aiden") {
		t.Error("the browse page does not show the signed-in display name")
	}

	// 5. Log out, and the catalog locks again.
	resp, err = client.PostForm(base+"/logout", url.Values{})
	if err != nil {
		t.Fatalf("POST /logout: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("POST /logout = %d, want 302", resp.StatusCode)
	}

	resp, err = client.Get(base + "/")
	if err != nil {
		t.Fatalf("GET / after logout: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("GET / after logout = %d, want 302", resp.StatusCode)
	}
}

func TestEndToEndStaticAssetsAndRobots(t *testing.T) {
	base, _ := bootServer(t)
	client := newClient(t)

	resp, err := client.Get(base + "/robots.txt")
	if err != nil {
		t.Fatalf("GET /robots.txt: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /robots.txt = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Allow: /s/") {
		t.Error("robots.txt does not allow crawlers on the share routes")
	}
}

func TestEndToEndAPIRequiresABearerKey(t *testing.T) {
	base, store := bootServer(t)
	client := newClient(t)

	resp, err := client.Get(base + "/api/items")
	if err != nil {
		t.Fatalf("GET /api/items: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /api/items = %d, want 401", resp.StatusCode)
	}

	key, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	hash, err := auth.HashPasswordWithParams("pw", auth.Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := store.CreateUser(context.Background(), "bot", "Bot", hash, auth.HashToken(key), false); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, base+"/api/items", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/items with a key: %v", err)
	}
	resp.Body.Close()
	// No /api route exists yet, so 404 proves the gate let the request past.
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("a valid Bearer key was rejected with 401")
	}
}

func TestDataDirectoryIsCreatedOnDisk(t *testing.T) {
	dataDir := t.TempDir()
	cfg, err := config.Load([]string{"-data", dataDir}, func(string) string { return "" })
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
	defer store.Close()

	if got := cfg.DBPath(); got != filepath.Join(dataDir, "media.db") {
		t.Errorf("DBPath = %q", got)
	}
	if _, err := store.RecoverRunningJobs(context.Background()); err != nil {
		t.Errorf("RecoverRunningJobs on a fresh database: %v", err)
	}
}
