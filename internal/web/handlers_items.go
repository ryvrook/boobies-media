package web

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"boobies-media/internal/db"
	"boobies-media/internal/ingest"
	"boobies-media/internal/jobs"
)

// handleListItems serves GET /api/items with keyset pagination.
func (s *Server) handleListItems(w http.ResponseWriter, r *http.Request) {
	query, err := s.listItemQuery(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	items, next, err := s.Store.ListItems(r.Context(), query)
	if err != nil {
		// A malformed cursor is client error, not server error.
		if strings.Contains(err.Error(), "malformed cursor") {
			writeJSONError(w, http.StatusBadRequest, "bad_cursor", "that pagination cursor is not valid")
			return
		}
		s.serverError(w, r, err)
		return
	}
	payload, err := s.itemsPayload(r, items)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": payload, "next_cursor": next})
}

// handleListItemIDs returns the complete filtered selection as lightweight
// IDs. It lets the browser select everything without rendering every tile or
// requesting every thumbnail.
func (s *Server) handleListItemIDs(w http.ResponseWriter, r *http.Request) {
	query, err := s.listItemQuery(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	query.Limit = db.MaxItemLimit
	query.Cursor = ""
	ids := make([]string, 0)
	for {
		items, next, err := s.Store.ListItems(r.Context(), query)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		for _, item := range items {
			ids = append(ids, item.ID)
		}
		if next == "" {
			break
		}
		query.Cursor = next
	}
	writeJSON(w, http.StatusOK, map[string]any{"ids": ids})
}

// listItemQuery parses the browse filters from the query string.
func (s *Server) listItemQuery(r *http.Request) (db.ItemQuery, error) {
	params := r.URL.Query()
	sort, err := db.ParseItemSort(params.Get("sort"))
	if err != nil {
		// db.ParseItemSort's error carries an internal "db:"-prefixed message;
		// give the client a clean, fixed one instead of forwarding it.
		return db.ItemQuery{}, errors.New("unknown sort value")
	}
	query := db.ItemQuery{
		Sort:   sort,
		Tag:    params.Get("tag"),
		Query:  params.Get("q"),
		Cursor: params.Get("cursor"),
	}
	if raw := params.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return db.ItemQuery{}, errors.New("limit must be a number")
		}
		query.Limit = limit
	}
	if raw := params.Get("folder"); raw != "" {
		// "root" selects unfiled items; a numeric id selects that folder.
		if raw == "root" {
			root := int64(0)
			query.FolderID = &root
		} else {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return db.ItemQuery{}, errors.New("folder must be a number or \"root\"")
			}
			query.FolderID = &id
		}
	}
	if raw := params.Get("uploader"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return db.ItemQuery{}, errors.New("uploader must be a numeric user id")
		}
		query.UploaderID = id
	}
	return query, nil
}

// itemsPayload serialises a page of items, reading every tag in one query.
func (s *Server) itemsPayload(r *http.Request, items []*db.Item) ([]map[string]any, error) {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	tagsByItem, err := s.Store.TagsForItems(r.Context(), ids)
	if err != nil {
		return nil, err
	}
	payload := make([]map[string]any, 0, len(items))
	for _, item := range items {
		payload = append(payload, itemJSON(item, tagsByItem[item.ID], s.Cfg.BaseURL))
	}
	return payload, nil
}

// handleGetItem serves GET /api/items/{id}.
func (s *Server) handleGetItem(w http.ResponseWriter, r *http.Request) {
	item, ok := s.lookupItem(w, r)
	if !ok {
		return
	}
	tags, err := s.Store.ItemTags(r.Context(), item.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": itemJSON(item, tags, s.Cfg.BaseURL)})
}

// patchItemBody is the PATCH payload. Pointers distinguish "absent" from
// "explicitly set to the zero value": folder_id 0 means move to the root.
type patchItemBody struct {
	Title        *string `json:"title"`
	FolderID     *int64  `json:"folder_id"`
	ShareRevoked *bool   `json:"share_revoked"`
}

// handlePatchItem serves PATCH /api/items/{id}.
func (s *Server) handlePatchItem(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	item, ok := s.lookupItem(w, r)
	if !ok {
		return
	}
	var body patchItemBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "expected a JSON object")
		return
	}
	ctx := r.Context()

	if body.Title != nil {
		if err := s.Store.SetItemTitle(ctx, item.ID, *body.Title); err != nil {
			s.writeItemError(w, r, err)
			return
		}
	}
	if body.FolderID != nil {
		if err := s.Store.MoveItem(ctx, item.ID, *body.FolderID); err != nil {
			s.writeItemError(w, r, err)
			return
		}
	}
	if body.ShareRevoked != nil {
		if err := s.Store.SetItemShareRevoked(ctx, item.ID, *body.ShareRevoked); err != nil {
			s.writeItemError(w, r, err)
			return
		}
	}

	updated, err := s.Store.ItemByID(ctx, item.ID)
	if err != nil {
		s.writeItemError(w, r, err)
		return
	}
	tags, err := s.Store.ItemTags(ctx, updated.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": itemJSON(updated, tags, s.Cfg.BaseURL)})
}

// handleAddItemTag serves POST /api/items/{id}/tags.
func (s *Server) handleAddItemTag(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	item, ok := s.lookupItem(w, r)
	if !ok {
		return
	}
	var body struct {
		Tag string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "expected a JSON object with a tag field")
		return
	}
	// Validate the tag format here, up front, so we can tell a genuine client
	// mistake apart from a store failure: db.AddItemTag's error can also come
	// from an exec/query failure, and that must not be reported as a 400.
	tag, err := db.NormalizeTag(body.Tag)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_tag", "tag must be 1-32 characters of a-z, 0-9, dot, dash or underscore")
		return
	}
	if err := s.Store.AddItemTag(r.Context(), item.ID, tag); err != nil {
		s.writeItemError(w, r, err)
		return
	}
	s.respondWithTags(w, r, item.ID)
}

// handleRemoveItemTag serves DELETE /api/items/{id}/tags/{tag}.
func (s *Server) handleRemoveItemTag(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	item, ok := s.lookupItem(w, r)
	if !ok {
		return
	}
	// Same reasoning as handleAddItemTag: validate the format ourselves so a
	// store/exec failure from db.RemoveItemTag can't be mistaken for a 400.
	tag, err := db.NormalizeTag(chi.URLParam(r, "tag"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_tag", "tag must be 1-32 characters of a-z, 0-9, dot, dash or underscore")
		return
	}
	if err := s.Store.RemoveItemTag(r.Context(), item.ID, tag); err != nil {
		s.writeItemError(w, r, err)
		return
	}
	s.respondWithTags(w, r, item.ID)
}

func (s *Server) respondWithTags(w http.ResponseWriter, r *http.Request, itemID string) {
	tags, err := s.Store.ItemTags(r.Context(), itemID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// handleDeleteItem serves DELETE /api/items/{id}. The delete is soft and is
// restricted to the uploader or an admin.
func (s *Server) handleDeleteItem(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	user, ok := CurrentUser(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.Store.SoftDeleteItem(r.Context(), id, user); err != nil {
		s.writeItemError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// handleJobStatus serves GET /api/jobs/{id}, including the items the job
// produced so the uploader island can show results for a pasted URL.
func (s *Server) handleJobStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "job id must be a number")
		return
	}
	job, err := s.Store.JobByID(r.Context(), id)
	if err != nil {
		s.writeItemError(w, r, err)
		return
	}
	items, err := s.Store.ItemsByJobID(r.Context(), job.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	payload, err := s.itemsPayload(r, items)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":       job.ID,
		"type":     job.Type,
		"status":   job.Status,
		"attempts": job.Attempts,
		"error":    s.jobErrorForClient(r, job),
		"items":    payload,
	})
}

// jobErrorForClient reports a job's failure without forwarding job.Error
// verbatim: it is whatever the media handlers returned, and those routinely
// embed raw ffmpeg/ffprobe stderr and absolute filesystem paths (see
// internal/media/exec.go and thumbs.go). The real cause is logged here, the
// same way serverError logs it, and only a fixed, safe message goes to the
// client.
func (s *Server) jobErrorForClient(r *http.Request, job *db.Job) string {
	if job.Error == "" {
		return ""
	}
	if job.Type == jobs.TypeIngestURL {
		if message, ok := ingest.PublicError(job.Error); ok {
			return message
		}
	}
	slog.Error("job failed", "method", r.Method, "path", r.URL.Path, "job", job.ID, "type", job.Type, "err", job.Error)
	return "processing failed"
}

// lookupItem resolves {id} to a live item, writing the response on failure.
func (s *Server) lookupItem(w http.ResponseWriter, r *http.Request) (*db.Item, bool) {
	item, err := s.Store.ItemByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeItemError(w, r, err)
		return nil, false
	}
	return item, true
}

// writeItemError maps store errors onto status codes without leaking internals.
func (s *Server) writeItemError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "not_found", "no such item")
	case errors.Is(err, db.ErrForbidden):
		writeJSONError(w, http.StatusForbidden, "forbidden", "only the uploader or an admin can do that")
	case errors.Is(err, db.ErrFolderCycle):
		writeJSONError(w, http.StatusBadRequest, "folder_cycle", "a folder cannot be moved inside itself")
	default:
		s.serverError(w, r, err)
	}
}
