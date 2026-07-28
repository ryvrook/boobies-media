package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"boobies-media/internal/db"
)

type adminBatchItemsBody struct {
	IDs    []string `json:"ids"`
	Action string   `json:"action"`
}

// handleRetryJob serves POST /api/jobs/{id}/retry. Admin-gated because only
// the admin queue view surfaces failed jobs at all; requeuing anything but a
// failed job is rejected as a distinct, actionable conflict rather than a 500.
func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "job id must be a number")
		return
	}
	if err := s.Store.RequeueJob(r.Context(), id, s.Now()); err != nil {
		switch {
		case errors.Is(err, db.ErrNotFound):
			writeJSONError(w, http.StatusNotFound, "not_found", "no such job")
		case errors.Is(err, db.ErrJobNotFailed):
			writeJSONError(w, http.StatusConflict, "not_failed", "only a failed job can be retried")
		default:
			s.serverError(w, r, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRestoreItem serves POST /api/admin/items/{id}/restore, the inverse of
// soft delete. Scoped to admins per the brief: unlike SoftDeleteItem, restore
// here does not check uploader ownership.
func (s *Server) handleRestoreItem(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.Store.RestoreItem(r.Context(), id); err != nil {
		s.writeItemError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePurgeItem serves DELETE /api/admin/items/{id}/purge. This permanently
// destroys media; admin gating happens exclusively in the route registration
// via requireAdmin in server.go, since neither db.PurgeItem nor
// media.Store.Purge take an actor and enforce nothing themselves.
//
// media.Store.Purge does the database purge and the refcounted blob/thumbnail
// unlink together: it only unlinks when db.PurgeItem reports no other item
// row (live or trashed) still references the same content hash. The handler
// never second-guesses that decision or unlinks anything itself.
func (s *Server) handlePurgeItem(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	if !s.requireMedia(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.Media.Purge(r.Context(), id); err != nil {
		s.writeItemError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAdminBatchItems restores or permanently purges selected trash rows.
// Like the library batch endpoint, it reports success per item so one stale
// row cannot hide the successful work performed on the others.
func (s *Server) handleAdminBatchItems(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	var body adminBatchItemsBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "expected a JSON object")
		return
	}
	if len(body.IDs) == 0 {
		writeJSONError(w, http.StatusBadRequest, "no_items", "no item ids given")
		return
	}
	if len(body.IDs) > maxBatchItems {
		writeJSONError(w, http.StatusBadRequest, "too_many", "at most 500 items per batch")
		return
	}
	if body.Action != "restore" && body.Action != "purge" {
		writeJSONError(w, http.StatusBadRequest, "bad_action", "action must be restore or purge")
		return
	}
	if body.Action == "purge" && !s.requireMedia(w, r) {
		return
	}

	okIDs := make([]string, 0, len(body.IDs))
	failed := make([]map[string]any, 0)
	for _, id := range body.IDs {
		var err error
		if body.Action == "restore" {
			err = s.Store.RestoreItem(r.Context(), id)
		} else {
			err = s.Media.Purge(r.Context(), id)
		}
		if err != nil {
			message := "item could not be updated"
			if errors.Is(err, db.ErrNotFound) {
				message = "item is no longer in the trash"
			}
			failed = append(failed, map[string]any{"id": id, "error": message})
			continue
		}
		okIDs = append(okIDs, id)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"applied": len(okIDs),
		"ok":      okIDs,
		"failed":  failed,
	})
}
