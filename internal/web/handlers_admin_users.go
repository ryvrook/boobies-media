package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"boobies-media/internal/auth"
	"boobies-media/internal/db"
)

func adminUserID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "user id must be a number")
		return 0, false
	}
	return id, true
}

// handleCreateUser serves POST /api/admin/users. The plaintext API key is
// returned exactly once, here, and never again: only its hash is persisted.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	var body struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		IsAdmin     bool   `json:"is_admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.Username == "" || body.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_fields", "username and password are required")
		return
	}
	display := body.DisplayName
	if display == "" {
		display = body.Username
	}
	passwordHash, err := auth.HashPassword(body.Password)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	key, err := auth.NewAPIKey()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	user, err := s.Store.CreateUser(r.Context(), body.Username, display, passwordHash, auth.HashToken(key), body.IsAdmin)
	if err != nil {
		if errors.Is(err, db.ErrDuplicateUser) {
			writeJSONError(w, http.StatusConflict, "duplicate_user", "that username is taken")
			return
		}
		s.serverError(w, r, err)
		return
	}
	// The plaintext key is shown once here and never again; only its hash was
	// passed to CreateUser above, and no handler in this file ever reads or
	// returns api_key_hash. This response body is its sole appearance.
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":    map[string]any{"id": user.ID, "username": user.Username, "is_admin": user.IsAdmin},
		"api_key": key,
	})
}

// handleUpdateUser serves PATCH /api/admin/users/{id}. Currently the only
// mutable field is is_admin; a self-demote is refused so the acting admin
// cannot strand the instance without an operator.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	id, ok := adminUserID(w, r)
	if !ok {
		return
	}
	var body struct {
		IsAdmin *bool `json:"is_admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.IsAdmin != nil {
		me, _ := CurrentUser(r)
		if me != nil && me.ID == id && !*body.IsAdmin {
			writeJSONError(w, http.StatusBadRequest, "self_lockout", "you cannot revoke your own admin")
			return
		}
		if err := s.Store.SetUserAdmin(r.Context(), id, *body.IsAdmin); err != nil {
			s.writeUserError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteUser serves DELETE /api/admin/users/{id}. Self-delete is
// refused for the same last-operator reason as the self-demote guard above.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	id, ok := adminUserID(w, r)
	if !ok {
		return
	}
	me, _ := CurrentUser(r)
	if me != nil && me.ID == id {
		writeJSONError(w, http.StatusBadRequest, "self_delete", "you cannot delete your own account")
		return
	}
	if err := s.Store.DeleteUser(r.Context(), id); err != nil {
		s.writeUserError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleResetPassword serves POST /api/admin/users/{id}/password.
// db.SetUserPassword also destroys every existing session for that user, so
// this actually logs other devices out rather than merely changing the hash.
func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	id, ok := adminUserID(w, r)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "a new password is required")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.Store.SetUserPassword(r.Context(), id, hash); err != nil {
		s.writeUserError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRotateKey serves POST /api/admin/users/{id}/apikey. The new plaintext
// key is returned exactly once, here, and never again; only its hash is
// persisted, replacing whatever hash the user had before.
func (s *Server) handleRotateKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	id, ok := adminUserID(w, r)
	if !ok {
		return
	}
	key, err := auth.NewAPIKey()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.Store.SetUserAPIKeyHash(r.Context(), id, auth.HashToken(key)); err != nil {
		s.writeUserError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_key": key})
}

// writeUserError maps user-store sentinels to status codes without leaking
// internals; anything unrecognised falls through to serverError.
func (s *Server) writeUserError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "not_found", "no such user")
	case errors.Is(err, db.ErrUserHasItems):
		writeJSONError(w, http.StatusConflict, "user_has_items", "delete or reassign this user's items first")
	default:
		s.serverError(w, r, err)
	}
}
