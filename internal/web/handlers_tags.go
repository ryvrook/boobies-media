package web

import (
	"errors"
	"net/http"

	"boobies-media/internal/db"
)

// handleListTags returns every tag currently in use, for the browse page's
// server-rendered filter chips and any other client that wants the full list.
func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.Store.ListTags(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// handleRandomItem powers the bot facing GET /api/random?tag=. It always
// resolves to a live, non revoked item so a link the bot posts stays
// servable.
func (s *Server) handleRandomItem(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.RandomItem(r.Context(), r.URL.Query().Get("tag"))
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "no matching item")
			return
		}
		s.serverError(w, r, err)
		return
	}
	tags, err := s.Store.ItemTags(r.Context(), item.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": itemJSON(item, tags, s.Cfg.BaseURL)})
}
