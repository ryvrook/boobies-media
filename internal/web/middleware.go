package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"boobies-media/internal/auth"
	"boobies-media/internal/db"
)

// SessionCookieName is the name of the session cookie.
const SessionCookieName = "bm_session"

// sessionLifetime matches the spec's 30-day session expiry.
const sessionLifetime = 30 * 24 * time.Hour

type contextKey int

const userContextKey contextKey = iota

// CurrentUser returns the authenticated user attached to the request, if any.
func CurrentUser(r *http.Request) (*db.User, bool) {
	user, ok := r.Context().Value(userContextKey).(*db.User)
	return user, ok && user != nil
}

// IsPublicPath reports whether a cleaned request path may be served without
// authentication. This is the complete public surface: /s/, /m/, /t/, the
// login page, and the assets the login page needs. Anything not listed here
// requires a session or a Bearer key, so a route added later is private by
// default.
func IsPublicPath(p string) bool {
	switch p {
	case "/login", "/robots.txt", "/favicon.ico":
		return true
	}
	for _, prefix := range []string{"/s/", "/m/", "/t/", "/static/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// LoadUser attaches the authenticated user to the request context when a valid
// session cookie or Bearer API key is present. It never blocks a request; the
// Gate decides what to do with anonymous ones.
func (s *Server) LoadUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user := s.resolveUser(r); user != nil {
			r = r.WithContext(context.WithValue(r.Context(), userContextKey, user))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) resolveUser(r *http.Request) *db.User {
	ctx := r.Context()
	now := s.Now()

	if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie.Value != "" {
		if user, err := s.Store.SessionUser(ctx, auth.HashToken(cookie.Value), now); err == nil {
			return user
		}
	}
	if key, ok := bearerToken(r); ok {
		if user, err := s.Store.UserByAPIKeyHash(ctx, auth.HashToken(key)); err == nil {
			return user
		}
	}
	return nil
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

// routedPath returns the path chi's router will actually use to dispatch this
// request. The CleanPath middleware (which runs earlier in the chain) resolves
// dot-segments and duplicate slashes into RouteContext.RoutePath but does not
// touch r.URL.Path. Checking IsPublicPath against r.URL.Path directly would
// let a request like "/s/../admin" pass the gate as public while the router
// still dispatches the cleaned "/admin" as the private route it is: a silent
// bypass of deny-by-default. Reading the same value the router uses closes
// that gap.
func routedPath(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePath != "" {
		return rctx.RoutePath
	}
	return r.URL.Path
}

// Gate denies every request that is neither authenticated nor on the public
// allowlist. API paths get a JSON 401; HTML paths are redirected to the login
// page with a next parameter.
func (s *Server) Gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsPublicPath(routedPath(r)) {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := CurrentUser(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(routedPath(r), "/api/") {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		target := routedPath(r)
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(target), http.StatusFound)
	})
}

// setSessionCookie writes the session cookie. Secure is configurable only so
// that plain-HTTP local development works; it is on by default.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   s.Cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie deletes the session cookie from the browser.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.Cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

// writeJSONError emits the {error, code} shape the API contract specifies.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message, "code": code})
}

// writeJSON encodes a successful JSON response.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// clientIP is the rate-limiter key.
//
// The origin binds loopback and is reachable only through cloudflared, so
// CF-Connecting-IP is set by our own tunnel daemon and cannot be forged by a
// remote client. X-Forwarded-For is deliberately NOT consulted: any client can
// send it, and honouring it would hand a login brute-forcer a fresh rate-limit
// bucket on every request. If this deployment ever moves to a plain reverse
// proxy, this function (and only this function) changes.
func clientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// safeNext keeps post-login redirects on this site. Anything that is not a
// site-relative path collapses to "/", which closes the open-redirect hole.
func safeNext(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "/"
	}
	if strings.Contains(raw, "\\") {
		return "/"
	}
	// Reject any ASCII control characters (< 0x20 or == 0x7F) to prevent tab/CR/LF injection.
	if strings.ContainsFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "/"
	}
	return raw
}
