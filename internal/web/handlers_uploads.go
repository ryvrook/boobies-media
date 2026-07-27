package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"boobies-media/internal/db"
	"boobies-media/internal/media"
)

// uploadTTL is how long an abandoned upload keeps its bytes before the
// janitor (Task 17) reaps it.
const uploadTTL = 12 * time.Hour

// uploadLimits reads both caps. They are read per request rather than cached
// so an admin raising the cap takes effect without a restart.
func (s *Server) uploadLimits(ctx context.Context) (maxBytes, chunkBytes int64, err error) {
	rawMax, err := s.Store.SettingGet(ctx, "upload_max_bytes")
	if err != nil {
		return 0, 0, err
	}
	rawChunk, err := s.Store.SettingGet(ctx, "upload_chunk_bytes")
	if err != nil {
		return 0, 0, err
	}
	if maxBytes, err = strconv.ParseInt(rawMax, 10, 64); err != nil {
		return 0, 0, fmt.Errorf("web: upload_max_bytes is not a number: %w", err)
	}
	if chunkBytes, err = strconv.ParseInt(rawChunk, 10, 64); err != nil {
		return 0, 0, fmt.Errorf("web: upload_chunk_bytes is not a number: %w", err)
	}
	return maxBytes, chunkBytes, nil
}

// loadOwnedUpload fetches an upload and confirms the caller owns it.
//
// A wrong owner is answered exactly like a missing row. The id is the
// capability to write into this upload, so a 403 would tell someone probing
// ids that they had found a real one.
func (s *Server) loadOwnedUpload(w http.ResponseWriter, r *http.Request) (*db.Upload, bool) {
	user, ok := CurrentUser(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return nil, false
	}
	up, err := s.Store.UploadByID(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, db.ErrNotFound) || (up != nil && up.UserID != user.ID) {
		writeJSONError(w, http.StatusNotFound, "not_found", "no such upload")
		return nil, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	return up, true
}

func uploadStatusJSON(up *db.Upload) map[string]any {
	missing := up.Missing()
	if missing == nil {
		missing = []int{}
	}
	return map[string]any{
		"upload_id":  up.ID,
		"chunk_size": up.ChunkSize,
		"size":       up.DeclaredSize,
		"received":   up.Received,
		"missing":    missing,
	}
}

// handleUploadInit opens a chunked upload: POST /api/uploads.
func (s *Server) handleUploadInit(w http.ResponseWriter, r *http.Request) {
	if !s.requireMedia(w, r) || !s.requireSameOrigin(w, r) {
		return
	}
	user, ok := CurrentUser(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	var body struct {
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
		FolderID int64  `json:"folder_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "expected a JSON body with filename and size")
		return
	}
	if body.Filename == "" || body.Size < 0 {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "filename is required and size cannot be negative")
		return
	}
	if body.FolderID != 0 {
		// Folders are a shared, unowned organizational tree (see the media
		// server spec's data model and authorization sections; no user_id or
		// ACL exists on a folder), so there is no ownership check to make
		// here. There is still a plain input-validation gap: an unknown id
		// would otherwise reach CreateUpload and fail on the table's foreign
		// key, surfacing as an opaque 500 instead of a clean 400.
		if _, err := s.Store.FolderByID(r.Context(), body.FolderID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeJSONError(w, http.StatusBadRequest, "bad_request", "no such folder")
				return
			}
			s.serverError(w, r, err)
			return
		}
	}

	maxBytes, chunkBytes, err := s.uploadLimits(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if body.Size > maxBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "too_large",
			fmt.Sprintf("that file is larger than the %d byte upload limit", maxBytes))
		return
	}

	tempDir, err := os.MkdirTemp(s.Cfg.TmpDir(), "upload-")
	if err != nil {
		s.serverError(w, r, fmt.Errorf("web: upload temp dir: %w", err))
		return
	}
	up, err := s.Store.CreateUpload(r.Context(), db.NewUpload{
		UserID:       user.ID,
		FolderID:     body.FolderID,
		Filename:     body.Filename,
		DeclaredSize: body.Size,
		ChunkSize:    chunkBytes,
		TempDir:      tempDir,
		ExpiresAt:    s.Now().UTC().Add(uploadTTL),
	})
	if err != nil {
		_ = os.RemoveAll(tempDir)
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, uploadStatusJSON(up))
}

// handleUploadStatus answers the resume handshake: GET /api/uploads/{id}.
func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	up, ok := s.loadOwnedUpload(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, uploadStatusJSON(up))
}

// handleUploadChunk stores one chunk: PUT /api/uploads/{id}/{index}.
func (s *Server) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	up, ok := s.loadOwnedUpload(w, r)
	if !ok {
		return
	}
	index, err := strconv.Atoi(chi.URLParam(r, "index"))
	if err != nil || index < 0 || index >= up.ChunkCount() {
		writeJSONError(w, http.StatusBadRequest, "bad_chunk",
			fmt.Sprintf("chunk index must be between 0 and %d", up.ChunkCount()-1))
		return
	}

	// The last chunk may be short; no chunk may be long.
	limited := http.MaxBytesReader(w, r.Body, up.ChunkSize)
	defer limited.Close()

	tmpPath := filepath.Join(up.TempDir, fmt.Sprintf("%d.part.tmp", index))
	finalPath := filepath.Join(up.TempDir, fmt.Sprintf("%d.part", index))
	file, err := os.Create(tmpPath)
	if err != nil {
		s.serverError(w, r, fmt.Errorf("web: create chunk file: %w", err))
		return
	}
	written, copyErr := io.Copy(file, limited)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		var tooLarge *http.MaxBytesError
		if errors.As(copyErr, &tooLarge) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "chunk_too_large",
				fmt.Sprintf("a chunk may not exceed %d bytes", up.ChunkSize))
			return
		}
		writeJSONError(w, http.StatusBadRequest, "chunk_incomplete", "the chunk body did not arrive intact")
		return
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		s.serverError(w, r, fmt.Errorf("web: close chunk file: %w", closeErr))
		return
	}
	if index < up.ChunkCount()-1 && written != up.ChunkSize {
		_ = os.Remove(tmpPath)
		writeJSONError(w, http.StatusBadRequest, "chunk_size_mismatch",
			fmt.Sprintf("chunk %d must be exactly %d bytes; got %d", index, up.ChunkSize, written))
		return
	}

	// Rename last: a chunk only counts once its bytes are fully on disk, so a
	// dropped connection leaves a .tmp nobody reads instead of a short chunk
	// the client believes was stored.
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		s.serverError(w, r, fmt.Errorf("web: commit chunk: %w", err))
		return
	}
	if _, err := s.Store.RecordChunk(r.Context(), up.ID, index, s.Now().UTC().Add(uploadTTL)); err != nil {
		s.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// completedUpload is what handleUploadComplete remembers about a finished
// (or claimed) completion, so a retry of the same id (a lost response, a
// double-click, a second tab) is answered from memory instead of either
// 404ing on a row this handler already deleted or, worse, calling Save a
// second time and minting a second item row for the same bytes.
type completedUpload struct {
	ownerID      int64
	itemID       string
	deduplicated bool
	optimized    bool
	at           time.Time
}

// completedUploadTTL bounds how long a finished upload id keeps answering a
// replayed completion. It only needs to outlive a client's realistic retry
// window, not the original 12h upload TTL: the row and the temp bytes behind
// an entry are already gone the moment the entry exists.
const completedUploadTTL = 10 * time.Minute

// rememberCompletedUpload records that id finished (or was claimed) so a
// later completion attempt for the same id can be answered idempotently.
//
// It also sweeps every stale entry out of the map first. completedUploadTTL
// is otherwise aspirational: lookupCompletedUpload only ever evicts an entry
// it is asked to look up again, so an id nobody retries (the overwhelmingly
// common case) would sit in the map for the life of the process. Piggybacking
// the sweep on every insert bounds the map to roughly one TTL window's worth
// of completions without needing a background goroutine.
func (s *Server) rememberCompletedUpload(id string, ownerID int64, result *media.SaveResult) {
	s.completedUploadsMu.Lock()
	defer s.completedUploadsMu.Unlock()
	if s.completedUploads == nil {
		s.completedUploads = make(map[string]completedUpload)
	}
	now := s.Now()
	for existingID, rec := range s.completedUploads {
		if now.Sub(rec.at) > completedUploadTTL {
			delete(s.completedUploads, existingID)
		}
	}
	s.completedUploads[id] = completedUpload{
		ownerID:      ownerID,
		itemID:       result.Item.ID,
		deduplicated: result.Deduplicated,
		optimized:    result.Optimized,
		at:           now,
	}
}

// lookupCompletedUpload returns the remembered outcome for id, if any is
// still fresh and belongs to ownerID. A mismatched owner is reported exactly
// like no record at all, the same rule loadOwnedUpload applies to the row
// itself: the id is a capability, not a public lookup key.
func (s *Server) lookupCompletedUpload(id string, ownerID int64) (completedUpload, bool) {
	s.completedUploadsMu.Lock()
	defer s.completedUploadsMu.Unlock()
	rec, ok := s.completedUploads[id]
	if !ok {
		return completedUpload{}, false
	}
	if s.Now().Sub(rec.at) > completedUploadTTL {
		delete(s.completedUploads, id)
		return completedUpload{}, false
	}
	if rec.ownerID != ownerID {
		return completedUpload{}, false
	}
	return rec, true
}

// writeCompletedUpload answers a replayed completion with the item it
// produced the first time. The item is refetched live (not cached) so a
// title or tag edit made since the original completion still shows up.
func (s *Server) writeCompletedUpload(w http.ResponseWriter, r *http.Request, rec completedUpload) {
	item, err := s.Store.ItemByID(r.Context(), rec.itemID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// The item this completion produced was since purged. Nothing
			// left to hand back; answer the same as an unknown upload id.
			writeJSONError(w, http.StatusNotFound, "not_found", "no such upload")
			return
		}
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"item":         itemJSON(item, nil, s.Cfg.BaseURL),
		"deduplicated": rec.deduplicated,
		"optimized":    rec.optimized,
	})
}

// claimUpload marks id as being completed by this call. It reports false if
// another call already holds the claim, in which case the caller must not
// touch the upload's row or chunks.
//
// This is the sole mechanism serialising concurrent completions of the same
// upload id; it replaces an earlier design that used DeleteUpload itself as
// the claim. That design deleted the row (and, on any Save error, the
// chunks) before Save ever ran, so a transient Save failure (a full spool,
// an optimizer crash, a hash/placeBlob/CreateItem hiccup) destroyed a
// possibly multi-gigabyte upload's progress with no way to retry. Claiming
// in-process instead means the row and chunks are only ever removed once
// Save has actually succeeded (see handleUploadComplete), so a failed
// attempt leaves everything in place for a free retry.
func (s *Server) claimUpload(id string) bool {
	s.uploadClaimsMu.Lock()
	defer s.uploadClaimsMu.Unlock()
	if s.uploadClaims == nil {
		s.uploadClaims = make(map[string]bool)
	}
	if s.uploadClaims[id] {
		return false
	}
	s.uploadClaims[id] = true
	return true
}

// releaseUpload releases id's claim. Call it via defer immediately after a
// successful claimUpload so it still runs on every exit path (an early
// return, a later error, a panic recovered by the router's Recoverer
// middleware), since a claim that is never released would wedge that upload
// id (permanent 409s) until the process restarts.
func (s *Server) releaseUpload(id string) {
	s.uploadClaimsMu.Lock()
	defer s.uploadClaimsMu.Unlock()
	delete(s.uploadClaims, id)
}

// handleUploadComplete assembles the chunks: POST /api/uploads/{id}/complete.
func (s *Server) handleUploadComplete(w http.ResponseWriter, r *http.Request) {
	if !s.requireMedia(w, r) || !s.requireSameOrigin(w, r) {
		return
	}
	user, ok := CurrentUser(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	id := chi.URLParam(r, "id")

	// A retry of an upload this handler already finished must come back as
	// the original item, not a confusing 404 on a row this handler deleted
	// the moment Save succeeded (see below).
	if rec, ok := s.lookupCompletedUpload(id, user.ID); ok {
		s.writeCompletedUpload(w, r, rec)
		return
	}

	// Claim the id before doing any of the real work. Without this, two
	// concurrent completions of the same upload both pass the Missing()
	// check below, both call Save, and both succeed: placeBlob dedups the
	// bytes but CreateItem still writes two rows. The claim is purely
	// in-process (see claimUpload) rather than DeleteUpload itself, so the
	// row and chunks survive until Save actually succeeds; see the comment
	// there for why. The loser gets a 409, unless the winner finished (and
	// released) between this call's cache miss above and its claim attempt
	// here, in which case the outcome is already known and is answered the
	// same idempotent way as the top-of-function check.
	if !s.claimUpload(id) {
		if rec, ok := s.lookupCompletedUpload(id, user.ID); ok {
			s.writeCompletedUpload(w, r, rec)
			return
		}
		writeJSONError(w, http.StatusConflict, "already_completing",
			"this upload is already being completed")
		return
	}
	defer s.releaseUpload(id)

	// Re-check the idempotent-replay cache now that the claim is held. A
	// previous holder of this same claim may have finished and released it
	// (populating the cache) in the window between this call's first check
	// above and the claimUpload call just now; without this second check
	// this call would redo Save and mint a duplicate item.
	if rec, ok := s.lookupCompletedUpload(id, user.ID); ok {
		s.writeCompletedUpload(w, r, rec)
		return
	}

	up, ok := s.loadOwnedUpload(w, r)
	if !ok {
		return
	}
	if missing := up.Missing(); len(missing) > 0 {
		writeJSONError(w, http.StatusConflict, "incomplete",
			fmt.Sprintf("%d chunks are still missing", len(missing)))
		return
	}

	// Open every part and stream them in order. The whole file never exists
	// in memory, and Save writes it to disk exactly once.
	readers := make([]io.Reader, 0, up.ChunkCount())
	var open []*os.File
	var receivedBytes int64
	defer func() {
		for _, f := range open {
			_ = f.Close()
		}
	}()
	for i := 0; i < up.ChunkCount(); i++ {
		f, err := os.Open(filepath.Join(up.TempDir, fmt.Sprintf("%d.part", i)))
		if err != nil {
			s.serverError(w, r, fmt.Errorf("web: reopen chunk %d: %w", i, err))
			return
		}
		open = append(open, f)
		readers = append(readers, f)
		info, err := f.Stat()
		if err != nil {
			s.serverError(w, r, fmt.Errorf("web: stat chunk %d: %w", i, err))
			return
		}
		receivedBytes += info.Size()
	}

	// Compare what the client declared against what it actually sent, before
	// any of it becomes an item. Checking result.Item.Size here instead (the
	// bytes media.Save ends up storing) used to fail this for every
	// optimized upload: auto_webp converts an optimizable PNG to a strictly
	// smaller webp by construction, so the stored size is always less than
	// the declared original size on the very case this is meant to catch
	// honest clients doing nothing wrong. Comparing against the bytes
	// received catches a genuinely dishonest declared size while never
	// penalizing the optimizer for doing its job, and it does so before Save
	// is even called, so a rejected upload never leaves an item behind.
	if receivedBytes != up.DeclaredSize {
		writeJSONError(w, http.StatusBadRequest, "size_mismatch",
			fmt.Sprintf("declared %d bytes but sent %d", up.DeclaredSize, receivedBytes))
		return
	}

	maxBytes, _, err := s.uploadLimits(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	result, err := s.Media.Save(r.Context(), media.SaveRequest{
		Reader:     io.MultiReader(readers...),
		Filename:   up.Filename,
		UploaderID: up.UserID,
		FolderID:   up.FolderID,
		MaxBytes:   maxBytes,
	})
	if err != nil {
		// Deliberately no cleanup here: the row and every staged chunk stay
		// exactly as they were before this call. A Save failure can be
		// transient (a full spool, an optimizer crash, a hash/placeBlob/
		// CreateItem hiccup), and nothing was committed on this path (Save
		// itself rolls back the item on any error past CreateItem), so the
		// only honest, cheap recovery is to let the client retry the same
		// completion, which claimUpload's release (above, deferred) makes
		// possible the instant this call returns.
		s.writeIngestError(w, r, err)
		return
	}

	// Only now, with Save's success as proof there is nothing left to retry,
	// remove the row and the staged chunks, in that order, so a crash
	// between the two leaves an orphaned temp dir (reaped by a future janitor
	// sweep) rather than a row pointing at bytes that no longer exist.
	if err := s.Store.DeleteUpload(r.Context(), up.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
		slog.Error("deleting completed upload row", "upload", up.ID, "err", err)
	}
	if err := os.RemoveAll(up.TempDir); err != nil {
		slog.Error("removing upload temp dir", "upload", up.ID, "err", err)
	}
	s.rememberCompletedUpload(id, up.UserID, result)
	writeJSON(w, http.StatusCreated, map[string]any{
		"item":         itemJSON(result.Item, nil, s.Cfg.BaseURL),
		"deduplicated": result.Deduplicated,
		"optimized":    result.Optimized,
	})
}

// handleUploadCancel lets a client abandon an upload: DELETE /api/uploads/{id}.
func (s *Server) handleUploadCancel(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	up, ok := s.loadOwnedUpload(w, r)
	if !ok {
		return
	}
	s.discardUpload(r.Context(), up)
	w.WriteHeader(http.StatusNoContent)
}

// discardUpload removes an upload's row and its bytes. Failures are logged,
// never returned: the caller has already succeeded at what the user asked for,
// and the janitor will retry the cleanup.
func (s *Server) discardUpload(ctx context.Context, up *db.Upload) {
	if err := os.RemoveAll(up.TempDir); err != nil {
		slog.Error("removing upload temp dir", "upload", up.ID, "err", err)
	}
	if err := s.Store.DeleteUpload(ctx, up.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
		slog.Error("deleting upload row", "upload", up.ID, "err", err)
	}
}
