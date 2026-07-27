package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"boobies-media/internal/db"
)

func folderJSON(f *db.Folder) map[string]any {
	return map[string]any{"id": f.ID, "parent_id": f.ParentID, "name": f.Name}
}

// handleListFolders serves GET /api/folders; the sidebar island builds the
// tree from the flat list.
func (s *Server) handleListFolders(w http.ResponseWriter, r *http.Request) {
	folders, err := s.Store.ListFolders(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(folders))
	for _, f := range folders {
		out = append(out, folderJSON(f))
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": out})
}

// handleCreateFolder serves POST /api/folders.
func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	var body struct {
		Name     string `json:"name"`
		ParentID int64  `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	name, err := db.NormalizeFolderName(body.Name)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_folder_name", "a folder name must be 1-100 characters and must not contain a slash")
		return
	}
	folder, err := s.Store.CreateFolder(r.Context(), body.ParentID, name)
	if err != nil {
		s.writeFolderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"folder": folderJSON(folder)})
}

// handleUpdateFolder serves PATCH /api/folders/{id}: rename and/or move.
func (s *Server) handleUpdateFolder(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "folder id must be a number")
		return
	}
	var body struct {
		Name     *string `json:"name"`
		ParentID *int64  `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.Name != nil {
		name, err := db.NormalizeFolderName(*body.Name)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_folder_name", "a folder name must be 1-100 characters and must not contain a slash")
			return
		}
		if err := s.Store.RenameFolder(r.Context(), id, name); err != nil {
			s.writeFolderError(w, r, err)
			return
		}
	}
	if body.ParentID != nil {
		if err := s.Store.MoveFolder(r.Context(), id, *body.ParentID); err != nil {
			s.writeFolderError(w, r, err)
			return
		}
	}
	folder, err := s.Store.FolderByID(r.Context(), id)
	if err != nil {
		s.writeFolderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folder": folderJSON(folder)})
}

// handleDeleteFolder serves DELETE /api/folders/{id}. Child folders cascade;
// items inside fall back to the root rather than being deleted.
func (s *Server) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "folder id must be a number")
		return
	}
	if err := s.Store.DeleteFolder(r.Context(), id); err != nil {
		s.writeFolderError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeFolderError maps the folder store's sentinels to status codes without
// leaking internals; anything unrecognised falls through to serverError.
func (s *Server) writeFolderError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "not_found", "no such folder")
	case errors.Is(err, db.ErrFolderCycle):
		writeJSONError(w, http.StatusConflict, "folder_cycle", "a folder cannot be moved inside itself")
	case errors.Is(err, db.ErrDuplicateFolder):
		writeJSONError(w, http.StatusConflict, "duplicate_folder", "a folder with that name already exists here")
	default:
		s.serverError(w, r, err)
	}
}
