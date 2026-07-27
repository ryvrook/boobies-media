package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"boobies-media/internal/auth"
	"boobies-media/internal/config"
	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	cfg, err := config.Load([]string{"-data", t.TempDir(), "-insecure-cookies"}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	srv, err := New(cfg, dbtest.New(t), nil)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return srv
}

// testUser creates a user with a known password and returns it.
func testUser(t *testing.T, srv *Server, username, password string) *db.User {
	t.Helper()
	hash, err := auth.HashPasswordWithParams(password, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user, err := srv.Store.CreateUser(context.Background(), username, "Display "+username, hash, "", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return user
}

func TestIsPublicPath(t *testing.T) {
	public := []string{
		"/login", "/robots.txt", "/favicon.ico",
		"/static/dist/main.js", "/static/dist/main.css",
		"/s/abc12345", "/m/abc12345", "/t/abc12345",
	}
	for _, p := range public {
		if !IsPublicPath(p) {
			t.Errorf("IsPublicPath(%q) = false, want true", p)
		}
	}
	private := []string{
		"/", "/admin", "/api/items", "/upload", "/logout",
		// Near-misses must not leak: a prefix check that forgot the trailing
		// slash would open all of these.
		"/s", "/m", "/t", "/statics", "/static-evil/x", "/sneaky", "/login-bypass",
	}
	for _, p := range private {
		if IsPublicPath(p) {
			t.Errorf("IsPublicPath(%q) = true, want false", p)
		}
	}
}

func TestAnonymousHTMLRequestRedirectsToLogin(t *testing.T) {
	srv := testServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/login?next=%2F" {
		t.Errorf("Location = %q, want \"/login?next=%%2F\"", got)
	}
}

func TestAnonymousAPIRequestGetsJSON401(t *testing.T) {
	srv := testServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/items", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body.Code != "unauthorized" {
		t.Errorf("code = %q, want \"unauthorized\"", body.Code)
	}
	if body.Error == "" {
		t.Error("error message is empty")
	}
}

func TestPublicPrefixesBypassTheGate(t *testing.T) {
	srv := testServer(t)
	// No /s/, /m/ or /t/ handler exists until a later plan, so the gate
	// passing the request through shows up as a 404, never a 302 or 401.
	for _, p := range []string{"/s/abc12345", "/m/abc12345", "/t/abc12345"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusFound || rec.Code == http.StatusUnauthorized {
			t.Errorf("%s returned %d; the gate blocked a public route", p, rec.Code)
		}
	}
}

func TestStaticAndRobotsAreServedAnonymously(t *testing.T) {
	srv := testServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/robots.txt status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body == "" {
		t.Error("/robots.txt body is empty")
	}
}

func TestPathTraversalIsCleanedBeforeTheGate(t *testing.T) {
	srv := testServer(t)
	// "/s/../admin" must not sneak past the gate as a public path.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/s/../admin", nil))
	if rec.Code == http.StatusOK {
		t.Fatalf("traversal reached a handler with status 200")
	}
}

// TestGateUsesTheRoutedPathNotTheRawPath guards against a gate bypass that the
// end-to-end traversal test above cannot detect on its own: chi's CleanPath
// middleware normalises dot-segments into RouteContext.RoutePath (what the
// router actually dispatches on) but never touches r.URL.Path. A gate that
// keys IsPublicPath off r.URL.Path directly would see "/s/../admin" as public
// (it has the "/s/" prefix) while the router still sends the request to the
// private "/admin" handler once one exists: a silent deny-by-default bypass.
// This test simulates that post-CleanPath state directly against Gate,
// independent of whether any handler is mounted at "/admin" yet.
func TestGateUsesTheRoutedPathNotTheRawPath(t *testing.T) {
	srv := testServer(t)
	reached := false
	sentinel := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/s/../admin", nil)
	rctx := chi.NewRouteContext()
	rctx.RoutePath = "/admin" // what CleanPath computes for this raw path
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	srv.Gate(sentinel).ServeHTTP(rec, req)

	if reached {
		t.Fatal("Gate let an anonymous request reach a private handler via a raw path that only looks public before cleaning")
	}
	if rec.Code != http.StatusFound {
		t.Errorf("status = %d, want 302 (redirect to login for the private, cleaned path)", rec.Code)
	}
}

// TestGateNegotiatesJSONOnTheRoutedPath verifies that the gate uses the
// routed (cleaned) path for content negotiation, not the raw request path.
// A request to "/x/../api/items" (raw) with RoutePath "/api/items" (cleaned)
// must get a JSON 401, not an HTML 302 redirect.
func TestGateNegotiatesJSONOnTheRoutedPath(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/x/../api/items", nil)
	rctx := chi.NewRouteContext()
	rctx.RoutePath = "/api/items" // what CleanPath computes for this raw path
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	srv.Gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an unauthenticated API request", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestValidSessionCookieAuthenticates(t *testing.T) {
	srv := testServer(t)
	user := testUser(t, srv, "aiden", "hunter2")

	token := "plaintext-session-token"
	if err := srv.Store.CreateSession(context.Background(), auth.HashToken(token), user.ID, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an authenticated request", rec.Code)
	}
}

func TestExpiredSessionCookieIsRejected(t *testing.T) {
	srv := testServer(t)
	user := testUser(t, srv, "aiden", "hunter2")

	token := "expired-token"
	if err := srv.Store.CreateSession(context.Background(), auth.HashToken(token), user.ID, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 for an expired session", rec.Code)
	}
}

func TestBearerAPIKeyAuthenticates(t *testing.T) {
	srv := testServer(t)
	key := "bm_test-api-key"
	hash, err := auth.HashPasswordWithParams("pw", auth.Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := srv.Store.CreateUser(context.Background(), "bot", "Bot", hash, auth.HashToken(key), false); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/items", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// The route does not exist yet, but the gate must have let it through.
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("a valid Bearer key was rejected with 401")
	}
}

func TestBadBearerKeyIsRejected(t *testing.T) {
	srv := testServer(t)
	for _, header := range []string{"Bearer wrong-key", "Bearer ", "Basic abc", "wrong-key", ""} {
		req := httptest.NewRequest(http.MethodGet, "/api/items", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q gave status %d, want 401", header, rec.Code)
		}
	}
}

func TestCurrentUserOnAnAnonymousRequest(t *testing.T) {
	if _, ok := CurrentUser(httptest.NewRequest(http.MethodGet, "/", nil)); ok {
		t.Fatal("CurrentUser reported a user on a bare request")
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	cfg, err := config.Load([]string{"-data", t.TempDir()}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	srv, err := New(cfg, dbtest.New(t), nil)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.setSessionCookie(rec, "tok", time.Now().UTC().Add(30*24*time.Hour))

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != SessionCookieName {
		t.Errorf("name = %q, want %q", c.Name, SessionCookieName)
	}
	if !c.HttpOnly {
		t.Error("HttpOnly = false, want true")
	}
	if !c.Secure {
		t.Error("Secure = false, want true when SecureCookies is on")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, want \"/\"", c.Path)
	}
	if c.MaxAge <= 0 {
		t.Errorf("MaxAge = %d, want a positive 30-day lifetime", c.MaxAge)
	}
}

func TestInsecureCookiesFlagDropsSecure(t *testing.T) {
	srv := testServer(t) // built with -insecure-cookies
	rec := httptest.NewRecorder()
	srv.setSessionCookie(rec, "tok", time.Now().UTC().Add(time.Hour))
	if rec.Result().Cookies()[0].Secure {
		t.Error("Secure = true despite -insecure-cookies")
	}
}

func TestClearSessionCookieExpiresIt(t *testing.T) {
	srv := testServer(t)
	rec := httptest.NewRecorder()
	srv.clearSessionCookie(rec)
	c := rec.Result().Cookies()[0]
	if c.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want a negative value to delete the cookie", c.MaxAge)
	}
	if c.Value != "" {
		t.Errorf("Value = %q, want empty", c.Value)
	}
}

func TestClientIPIgnoresSpoofableForwardingHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.7:51234"

	if got := clientIP(req); got != "203.0.113.7" {
		t.Errorf("clientIP = %q, want the peer address", got)
	}

	// Behind the tunnel only cloudflared can reach the origin, so only
	// CF-Connecting-IP is authoritative.
	req.Header.Set("CF-Connecting-IP", "198.51.100.5")
	if got := clientIP(req); got != "198.51.100.5" {
		t.Errorf("clientIP with CF-Connecting-IP = %q, want \"198.51.100.5\"", got)
	}

	// X-Forwarded-For is attacker-controlled and must not move the rate-limit
	// key: otherwise a login brute-force gets a fresh bucket per request.
	req.Header.Del("CF-Connecting-IP")
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	if got := clientIP(req); got != "203.0.113.7" {
		t.Errorf("clientIP honoured X-Forwarded-For (%q); it must be ignored", got)
	}
}

func TestUnknownUserSessionDoesNotPanic(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "not-a-real-token"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 for an unknown session token", rec.Code)
	}
}

func TestErrNotFoundIsNotLeakedToClients(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/items", nil)
	req.Header.Set("Authorization", "Bearer bm_nope")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if got := rec.Body.String(); strings.Contains(got, db.ErrNotFound.Error()) {
		t.Errorf("response body leaks an internal error: %q", got)
	}
}
