package web

import "net/http"

// sameOrigin reports whether a state-changing request came from our own pages.
//
// SameSite=Lax already blocks a cross-site POST from carrying the session
// cookie, so this is the second lock rather than the first. It is worth having
// because it keeps working if the cookie policy is ever loosened, and because
// it is the check that closes the CSRF follow-up Plan 1's review deferred
// until uploads existed.
func (s *Server) sameOrigin(r *http.Request) bool {
	// Fetch metadata is the precise signal where the browser sends it.
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "cross-site", "same-site":
		return false
	}
	// Older browsers: fall back to Origin.
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No Origin and no fetch metadata means a non-browser client: a bot
		// with a Bearer key. Those are not subject to CSRF; the browser is the
		// confused deputy this check exists to stop.
		return true
	}
	return origin == s.Cfg.BaseURL
}

// requireSameOrigin writes a 403 and returns false when the check fails.
func (s *Server) requireSameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if s.sameOrigin(r) {
		return true
	}
	writeJSONError(w, http.StatusForbidden, "cross_origin", "this request did not come from the site")
	return false
}
