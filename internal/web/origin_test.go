package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Every other test in this package sets Sec-Fetch-Site explicitly, so the
// Origin-header fallback in sameOrigin (origin.go:21-28), the path taken
// when a client sends no fetch metadata at all, never actually runs in the
// suite without these. These three tests pin down all three states the
// fallback can see.

// TestSameOriginAllowsAMatchingOriginWithNoFetchMetadata covers an older
// browser that sends Origin but not Sec-Fetch-Site, from our own page.
func TestSameOriginAllowsAMatchingOriginWithNoFetchMetadata(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/whatever", nil)
	req.Header.Set("Origin", srv.Cfg.BaseURL)

	if !srv.sameOrigin(req) {
		t.Errorf("sameOrigin = false for a same-origin Origin header, want true")
	}
}

// TestSameOriginRejectsAMismatchedOriginWithNoFetchMetadata covers the same
// browser, but the request is cross-origin: the fallback must fail closed.
func TestSameOriginRejectsAMismatchedOriginWithNoFetchMetadata(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/whatever", nil)
	req.Header.Set("Origin", "https://evil.example")

	if srv.sameOrigin(req) {
		t.Errorf("sameOrigin = true for a mismatched Origin header, want false")
	}

	rec := httptest.NewRecorder()
	if srv.requireSameOrigin(rec, req) {
		t.Errorf("requireSameOrigin = true for a mismatched Origin header, want false")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a mismatched Origin header", rec.Code)
	}
}

// TestSameOriginFailsOpenWithNeitherHeader pins the intentional fail-open
// design: with no Sec-Fetch-Site and no Origin, the request is treated as a
// non-browser client (e.g. a Bearer-key bot), which is not subject to CSRF.
// SameSite=Lax remains the primary defense; this check is the second lock,
// not the first. This test exists so a future edit cannot silently flip the
// design to fail-closed and break API clients.
func TestSameOriginFailsOpenWithNeitherHeader(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/whatever", nil)

	if !srv.sameOrigin(req) {
		t.Errorf("sameOrigin = false with neither header present, want true (fail open by design)")
	}
}
