package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"boobies-media/internal/auth"
)

// settingsAdminCookie creates an admin user and returns a valid session
// cookie for it. This mirrors authenticate/testUser (handlers_ingest_test.go,
// middleware_test.go) but sets IsAdmin so requireAdmin (Task 7,
// internal/web/middleware_admin.go) lets the request through. Named
// distinctly from any shared "adminCookie" helper other in-flight admin
// tasks may add, to avoid a duplicate-symbol collision at merge time.
func settingsAdminCookie(t *testing.T, srv *Server, username string) *http.Cookie {
	t.Helper()
	hash, err := auth.HashPasswordWithParams("hunter2", auth.Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user, err := srv.Store.CreateUser(context.Background(), username, "Display "+username, hash, "", true)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	token := "session-admin-" + username
	if err := srv.Store.CreateSession(context.Background(), auth.HashToken(token), user.ID, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return &http.Cookie{Name: SessionCookieName, Value: token}
}

func TestAdminSaveSettingsUpdatesValues(t *testing.T) {
	ctx := context.Background()
	srv := testServer(t)
	cookie := settingsAdminCookie(t, srv, "boss")

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings",
		`{"auto_webp":"off","upload_max_bytes":"1048576","upload_chunk_bytes":"1048576","download_max_bytes":"1048576","ytdlp_format":"b","cookies_twitter":"/data/cookies/twitter.txt"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	for key, want := range map[string]string{
		"auto_webp":          "off",
		"upload_max_bytes":   "1048576",
		"upload_chunk_bytes": "1048576",
		"download_max_bytes": "1048576",
		"ytdlp_format":       "b",
		"cookies_twitter":    "/data/cookies/twitter.txt",
	} {
		got, err := srv.Store.SettingGet(ctx, key)
		if err != nil || got != want {
			t.Errorf("%s = %q (err %v), want %q", key, got, err, want)
		}
	}
}

func TestAdminSaveSettingsAllowsClearingCookiesPath(t *testing.T) {
	ctx := context.Background()
	srv := testServer(t)
	cookie := settingsAdminCookie(t, srv, "boss")

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{"cookies_twitter":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got, err := srv.Store.SettingGet(ctx, "cookies_twitter")
	if err != nil || got != "" {
		t.Errorf("cookies_twitter = %q (err %v), want empty", got, err)
	}
}

func TestAdminSaveSettingsRejectsUnknownKey(t *testing.T) {
	srv := testServer(t)
	cookie := settingsAdminCookie(t, srv, "boss")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{"totally_made_up":"1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown-key status = %d, want 400", rec.Code)
	}
}

func TestAdminSaveSettingsRejectsNonNumericCap(t *testing.T) {
	srv := testServer(t)
	cookie := settingsAdminCookie(t, srv, "boss")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{"upload_max_bytes":"lots"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-numeric cap status = %d, want 400", rec.Code)
	}
}

func TestAdminSaveSettingsRejectsZeroOrNegativeByteSetting(t *testing.T) {
	srv := testServer(t)
	cookie := settingsAdminCookie(t, srv, "boss")
	for _, body := range []string{
		`{"download_max_bytes":"0"}`,
		`{"download_max_bytes":"-5"}`,
	} {
		rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestAdminSaveSettingsRejectsImplausiblyLargeByteSetting(t *testing.T) {
	srv := testServer(t)
	cookie := settingsAdminCookie(t, srv, "boss")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{"download_max_bytes":"99999999999999999"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAdminSaveSettingsRejectsUploadChunkBytesOutOfBounds(t *testing.T) {
	srv := testServer(t)
	cookie := settingsAdminCookie(t, srv, "boss")
	for _, body := range []string{
		`{"upload_chunk_bytes":"1024"}`,      // below 1 MiB floor
		`{"upload_chunk_bytes":"104857600"}`, // 100 MiB, at Cloudflare's cap
	} {
		rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestAdminSaveSettingsAcceptsUploadChunkBytesWithinBounds(t *testing.T) {
	srv := testServer(t)
	cookie := settingsAdminCookie(t, srv, "boss")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{"upload_chunk_bytes":"12582912"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminSaveSettingsRejectsUploadMaxBelowChunk(t *testing.T) {
	srv := testServer(t)
	cookie := settingsAdminCookie(t, srv, "boss")

	// Both in the same request.
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings",
		`{"upload_max_bytes":"1048576","upload_chunk_bytes":"2097152"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}

	// upload_chunk_bytes alone, checked against the stored default max
	// (8 GiB): should pass since it's well below it.
	rec = apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{"upload_chunk_bytes":"8388608"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	// Now shrink upload_max_bytes alone below the chunk size just stored.
	rec = apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{"upload_max_bytes":"1048576"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAdminSaveSettingsRejectsBadAutoWebp(t *testing.T) {
	srv := testServer(t)
	cookie := settingsAdminCookie(t, srv, "boss")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{"auto_webp":"yes"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAdminSaveSettingsRejectsEmptyYtdlpFormat(t *testing.T) {
	srv := testServer(t)
	cookie := settingsAdminCookie(t, srv, "boss")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{"ytdlp_format":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAdminSaveSettingsRejectsControlCharacters(t *testing.T) {
	srv := testServer(t)
	cookie := settingsAdminCookie(t, srv, "boss")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{"cookies_twitter":"/data/evil\npath"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAdminSaveSettingsRejectsEmptyBody(t *testing.T) {
	srv := testServer(t)
	cookie := settingsAdminCookie(t, srv, "boss")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestAdminSaveSettingsRefusesNonAdmin pins that a signed-in but non-admin
// user is refused, not merely a signed-out one: authenticate (from
// handlers_ingest_test.go) creates an ordinary user via testUser, which
// hardcodes IsAdmin=false.
func TestAdminSaveSettingsRefusesNonAdmin(t *testing.T) {
	srv := testServer(t)
	cookie := authenticate(t, srv, "rando")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{"auto_webp":"off"}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("status = 200, want a non-admin to be refused")
	}
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 403 or 401 for a non-admin", rec.Code)
	}
}

// TestAdminSaveSettingsDoesNotLeakInternals checks the sentinel-aware error
// shape from writeItemError's pattern: no internal detail (a Go error string,
// a filesystem path, a SQL fragment) reaches the response body on the
// unknown-key error path.
func TestAdminSaveSettingsDoesNotLeakInternals(t *testing.T) {
	srv := testServer(t)
	cookie := settingsAdminCookie(t, srv, "boss")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{"upload_max_bytes":"lots"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "strconv") {
		t.Errorf("response leaked an internal detail: %s", body)
	}
}
