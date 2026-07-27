package web

import (
	"errors"
	"net/http"
	"strings"

	"boobies-media/internal/auth"
	"boobies-media/internal/db"
)

// dummyHash is verified against when the submitted username does not exist, so
// a missing account costs the same time as a wrong password. Generated once
// with auth.HashPassword; the plaintext behind it is irrelevant and unused.
const dummyHash = `$argon2id$v=19$m=19456,t=2,p=1$YWJjZGVmZ2hpamtsbW5vcA$` +
	`SGVsbG9UaGlzSXNOb3RBUmVhbEtleUJ1dEl0SXNUaGVSaWdodExlbmd0aA`

const loginFailedMessage = "Incorrect username or password."

// handleLoginForm serves GET /login.
func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := CurrentUser(r); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	s.renderLogin(w, r, http.StatusOK, "", safeNext(r.URL.Query().Get("next")))
}

// handleLoginSubmit serves POST /login.
func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, r, http.StatusBadRequest, "That form could not be read.", "/")
		return
	}
	var (
		username = strings.TrimSpace(r.PostFormValue("username"))
		password = r.PostFormValue("password")
		next     = safeNext(r.PostFormValue("next"))
		ip       = clientIP(r)
	)

	if !s.Limiter.Allow(ip) {
		s.renderLogin(w, r, http.StatusTooManyRequests,
			"Too many sign-in attempts. Try again in a few minutes.", next)
		return
	}

	user, err := s.Store.UserByUsername(r.Context(), username)
	if err != nil {
		if !errors.Is(err, db.ErrNotFound) {
			s.serverError(w, r, err)
			return
		}
		// Spend the same work on a missing account so the response does not
		// become a username oracle.
		_, _ = auth.VerifyPassword(dummyHash, password)
		s.renderLogin(w, r, http.StatusUnauthorized, loginFailedMessage, next)
		return
	}

	ok, err := auth.VerifyPassword(user.PasswordHash, password)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		s.renderLogin(w, r, http.StatusUnauthorized, loginFailedMessage, next)
		return
	}

	token, err := auth.NewSessionToken()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	expires := s.Now().Add(sessionLifetime)
	if err := s.Store.CreateSession(r.Context(), auth.HashToken(token), user.ID, expires); err != nil {
		s.serverError(w, r, err)
		return
	}

	s.Limiter.Reset(ip)
	s.setSessionCookie(w, token, expires)
	http.Redirect(w, r, next, http.StatusFound)
}

// handleLogout serves POST /logout.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie.Value != "" {
		if err := s.Store.DeleteSession(r.Context(), auth.HashToken(cookie.Value)); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, status int, message, next string) {
	data := PageData{Title: "Sign in", Error: message, Next: next}
	if err := s.Renderer.Render(w, status, "login", data); err != nil {
		s.serverError(w, r, err)
	}
}
