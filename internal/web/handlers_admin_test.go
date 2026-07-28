package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boobies-media/internal/deps"
)

// adminCookie signs a user in and promotes them to admin.
func adminCookie(t *testing.T, srv *Server, username string) *http.Cookie {
	t.Helper()
	cookie := authenticate(t, srv, username)
	user, err := srv.Store.UserByUsername(context.Background(), username)
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	if err := srv.Store.SetUserAdmin(context.Background(), user.ID, true); err != nil {
		t.Fatalf("SetUserAdmin: %v", err)
	}
	return cookie
}

func TestAdminPageForbiddenToNonAdmin(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden") // non-admin

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin /admin status = %d, want 403", rec.Code)
	}
}

func TestAdminPageRendersForAdmin(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	srv.Deps = []deps.Status{{Name: "yt-dlp", OK: false, Err: "not found on PATH"}}
	cookie := adminCookie(t, srv, "aiden")

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin /admin status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Users", "Settings", "Job queue", "Trash",
		"yt-dlp",                   // the dependency banner
		"not found on PATH",        // the failing dep's error
		`name="ytdlp_format"`,      // a settings field
		`data-extractor="twitter"`, // a test-ingest button
	} {
		if !strings.Contains(body, want) {
			t.Errorf("admin page missing %q", want)
		}
	}
}

func TestAdminJobQueueIsPaginated(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "aiden")
	for i := 0; i < 25; i++ {
		if _, err := srv.Store.EnqueueJob(context.Background(), "probe", []byte(`{}`), srv.Now()); err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/admin?job_page=2", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin /admin status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Page 2 of 2") {
		t.Error("admin queue does not show its current page")
	}
	if !strings.Contains(body, `href="/admin?job_page=1#job-queue"`) {
		t.Error("admin queue does not link back to the previous page")
	}
	if got := strings.Count(body, `data-job-id="`); got != 5 {
		t.Errorf("second queue page rendered %d jobs, want 5", got)
	}
}

func TestAdminPageTestIngestButtonsAreEnabled(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "aiden")

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin /admin status = %d, want 200", rec.Code)
	}

	body := rec.Body.String()
	const wantButton = `<button type="button" data-action="test-ingest" data-extractor="twitter">Test twitter</button>`
	if !strings.Contains(body, wantButton) {
		t.Errorf("admin page test-ingest button not enabled with data-extractor intact; body:\n%s", body)
	}
	const wantHint = "Enqueue a known-good link per source"
	if !strings.Contains(body, wantHint) {
		t.Errorf("admin page missing self-test hint %q; body:\n%s", wantHint, body)
	}
}

func TestAdminNavLinkOnlyForAdmins(t *testing.T) {
	srv, _, _ := mediaTestServer(t)

	admin := adminCookie(t, srv, "boss")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(admin)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `href="/admin"`) {
		t.Error("admin does not see the Admin nav link")
	}

	plain := authenticate(t, srv, "aiden")
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(plain)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), `href="/admin"`) {
		t.Error("a non-admin sees the Admin nav link")
	}
}

// TestAdminPageForbidsAdminOnlyLeakage confirms a signed-in non-admin's 403
// response carries none of the admin page's content: not a user row, not a
// setting value, not a job or trash row. A 403 that leaked the page body
// would defeat the point of gating it.
func TestAdminPageForbidsAdminOnlyLeakage(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, leak := range []string{
		"admin__table", "admin__settings", "ytdlp_format", "Job queue", "Trash",
	} {
		if strings.Contains(body, leak) {
			t.Errorf("403 response leaked admin-only content %q", leak)
		}
	}
}

// TestAdminPageAnonymousBehavesLikeOtherPrivatePaths confirms /admin is
// covered by the same Gate as every other private route: an anonymous
// request is redirected to /login, not merely refused with 403 (that
// distinction matters because a 403-for-anonymous response would confirm to
// an unauthenticated prober that the path exists and is admin-gated, versus
// the uniform login redirect every other private path gives).
func TestAdminPageAnonymousBehavesLikeOtherPrivatePaths(t *testing.T) {
	srv, _, _ := mediaTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("anonymous /admin status = %d, want %d (redirect to login)", rec.Code, http.StatusFound)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login") {
		t.Fatalf("anonymous /admin redirected to %q, want /login", loc)
	}
}

// TestDependencyBannerReadyToolOmitsWarning confirms an all-OK Deps slice
// renders no warning banner, so the banner only ever fires when something is
// actually broken.
func TestDependencyBannerReadyToolOmitsWarning(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	srv.Deps = []deps.Status{{Name: "ffmpeg", OK: true, Version: "n8.1.2", Path: "/usr/bin/ffmpeg"}}
	cookie := adminCookie(t, srv, "aiden")

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "banner--warn") {
		t.Error("all-OK deps still rendered the warning banner")
	}
	if !strings.Contains(body, "n8.1.2") {
		t.Error("admin page missing the ready tool's version")
	}
}
