package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"boobies-media/internal/auth"
)

func postForm(t *testing.T, srv *Server, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.113.10:1234"
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie was set; response was %d %s", rec.Code, rec.Body.String())
	return nil
}

func TestLoginFormRenders(t *testing.T) {
	srv := testServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `name="password"`) {
		t.Error("login form does not contain a password field")
	}
}

func TestLoginSucceedsAndSetsSession(t *testing.T) {
	srv := testServer(t)
	user := testUser(t, srv, "aiden", "hunter2")

	rec := postForm(t, srv, "/login", url.Values{"username": {"aiden"}, "password": {"hunter2"}})
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (body: %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want \"/\"", loc)
	}

	cookie := sessionCookie(t, rec)
	// The DB must hold only the hash of the cookie value.
	got, err := srv.Store.SessionUser(context.Background(), auth.HashToken(cookie.Value), time.Now().UTC())
	if err != nil {
		t.Fatalf("SessionUser: %v", err)
	}
	if got.ID != user.ID {
		t.Errorf("session belongs to user %d, want %d", got.ID, user.ID)
	}

	var stored int
	if err := srv.Store.DB.QueryRow(`SELECT count(*) FROM sessions WHERE token_hash = ?`, cookie.Value).Scan(&stored); err != nil {
		t.Fatalf("count: %v", err)
	}
	if stored != 0 {
		t.Error("the plaintext session token was stored in the database")
	}
}

func TestLoginIsCaseInsensitiveOnUsername(t *testing.T) {
	srv := testServer(t)
	testUser(t, srv, "aiden", "hunter2")
	rec := postForm(t, srv, "/login", url.Values{"username": {"  AIDEN  "}, "password": {"hunter2"}})
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	srv := testServer(t)
	testUser(t, srv, "aiden", "hunter2")

	rec := postForm(t, srv, "/login", url.Values{"username": {"aiden"}, "password": {"wrong"}})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName && c.Value != "" {
			t.Fatal("a session cookie was set for a failed login")
		}
	}
}

func TestLoginDoesNotRevealWhetherAUserExists(t *testing.T) {
	srv := testServer(t)
	testUser(t, srv, "aiden", "hunter2")

	wrongPassword := postForm(t, srv, "/login", url.Values{"username": {"aiden"}, "password": {"wrong"}})
	noSuchUser := postForm(t, srv, "/login", url.Values{"username": {"ghost"}, "password": {"wrong"}})

	if wrongPassword.Code != noSuchUser.Code {
		t.Errorf("status codes differ: existing user %d, missing user %d", wrongPassword.Code, noSuchUser.Code)
	}
	if wrongPassword.Body.String() != noSuchUser.Body.String() {
		t.Error("response bodies differ between a wrong password and a missing user; that is a username oracle")
	}
}

func TestLoginRateLimitBlocksAfterFiveFailures(t *testing.T) {
	srv := testServer(t)
	testUser(t, srv, "aiden", "hunter2")

	for i := 1; i <= auth.LoginAttemptLimit; i++ {
		rec := postForm(t, srv, "/login", url.Values{"username": {"aiden"}, "password": {"wrong"}})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, rec.Code)
		}
	}
	rec := postForm(t, srv, "/login", url.Values{"username": {"aiden"}, "password": {"wrong"}})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status after exceeding the limit = %d, want 429", rec.Code)
	}
	// Even the correct password is refused while the window is open.
	rec = postForm(t, srv, "/login", url.Values{"username": {"aiden"}, "password": {"hunter2"}})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 while rate limited", rec.Code)
	}
}

func TestSuccessfulLoginResetsTheRateLimit(t *testing.T) {
	srv := testServer(t)
	testUser(t, srv, "aiden", "hunter2")

	for i := 0; i < auth.LoginAttemptLimit-1; i++ {
		postForm(t, srv, "/login", url.Values{"username": {"aiden"}, "password": {"wrong"}})
	}
	if rec := postForm(t, srv, "/login", url.Values{"username": {"aiden"}, "password": {"hunter2"}}); rec.Code != http.StatusFound {
		t.Fatalf("correct password gave %d, want 302", rec.Code)
	}
	// The budget is fresh again.
	if rec := postForm(t, srv, "/login", url.Values{"username": {"aiden"}, "password": {"wrong"}}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("after a successful login the counter was not reset: got %d, want 401", rec.Code)
	}
}

func TestLoginHonoursSafeNext(t *testing.T) {
	srv := testServer(t)
	testUser(t, srv, "aiden", "hunter2")

	rec := postForm(t, srv, "/login", url.Values{
		"username": {"aiden"}, "password": {"hunter2"}, "next": {"/admin?tab=users"},
	})
	if loc := rec.Header().Get("Location"); loc != "/admin?tab=users" {
		t.Errorf("Location = %q, want the requested path", loc)
	}
}

func TestLoginRejectsOffsiteNext(t *testing.T) {
	for _, evil := range []string{
		"https://evil.example.com/",
		"//evil.example.com/",
		"\\\\evil.example.com",
		"/\t/evil.example.com",
		"/\r\n/evil.example.com",
		"/\n/evil.example.com",
	} {
		srv := testServer(t)
		testUser(t, srv, "aiden", "hunter2")
		rec := postForm(t, srv, "/login", url.Values{
			"username": {"aiden"}, "password": {"hunter2"}, "next": {evil},
		})
		if loc := rec.Header().Get("Location"); loc != "/" {
			t.Errorf("next=%q redirected to %q, want \"/\" (open redirect)", evil, loc)
		}
	}
}

func TestLoginFormRedirectsWhenAlreadySignedIn(t *testing.T) {
	srv := testServer(t)
	user := testUser(t, srv, "aiden", "hunter2")
	token := "already-signed-in"
	if err := srv.Store.CreateSession(context.Background(), auth.HashToken(token), user.ID, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 for an already-authenticated visitor", rec.Code)
	}
}

func TestLogoutDeletesTheSession(t *testing.T) {
	srv := testServer(t)
	testUser(t, srv, "aiden", "hunter2")

	login := postForm(t, srv, "/login", url.Values{"username": {"aiden"}, "password": {"hunter2"}})
	cookie := sessionCookie(t, login)

	rec := postForm(t, srv, "/logout", url.Values{}, cookie)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want \"/login\"", loc)
	}
	if _, err := srv.Store.SessionUser(context.Background(), auth.HashToken(cookie.Value), time.Now().UTC()); err == nil {
		t.Error("the session still resolves after logout")
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName && c.MaxAge >= 0 {
			t.Error("logout did not expire the session cookie")
		}
	}
}

func TestLogoutRequiresAuthentication(t *testing.T) {
	srv := testServer(t)
	// /logout is not on the public allowlist, so the gate must intercept it.
	rec := postForm(t, srv, "/logout", url.Values{})
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login") {
		t.Errorf("Location = %q, want a redirect to /login", loc)
	}
}

// TestLogoutRejectsACrossSiteRequest closes the last gap in the second-lock
// CSRF coverage: /logout now carries the same requireSameOrigin check as
// /api/ingest, the /api/uploads* routes, and the destructive /api/items*
// routes (origin.go; see TestItemAPIRejectsACrossSiteDelete in
// handlers_items_test.go). A cross-site POST must be refused and the
// session must survive; the same request carrying Sec-Fetch-Site:
// same-origin, as a real browser form submission does, must still log the
// user out.
func TestLogoutRejectsACrossSiteRequest(t *testing.T) {
	srv := testServer(t)
	testUser(t, srv, "aiden", "hunter2")

	login := postForm(t, srv, "/login", url.Values{"username": {"aiden"}, "password": {"hunter2"}})
	cookie := sessionCookie(t, login)

	crossSite := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(url.Values{}.Encode()))
	crossSite.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSite.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, crossSite)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site logout: status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
	if _, err := srv.Store.SessionUser(context.Background(), auth.HashToken(cookie.Value), time.Now().UTC()); err != nil {
		t.Error("the session was deleted despite the cross-site logout being refused")
	}

	sameOrigin := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(url.Values{}.Encode()))
	sameOrigin.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	sameOrigin.Header.Set("Sec-Fetch-Site", "same-origin")
	sameOrigin.AddCookie(cookie)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, sameOrigin)
	if rec.Code != http.StatusFound {
		t.Fatalf("same-origin logout: status = %d, want 302 (body: %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want \"/login\"", loc)
	}
	if _, err := srv.Store.SessionUser(context.Background(), auth.HashToken(cookie.Value), time.Now().UTC()); err == nil {
		t.Error("the session still resolves after a same-origin logout")
	}
}
