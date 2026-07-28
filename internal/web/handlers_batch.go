package web

import (
	"encoding/json"
	"net/http"

	"boobies-media/internal/db"
)

// maxBatchItems bounds one batch request. The store's single connection
// (modernc.org/sqlite, SetMaxOpenConns(1)) serialises every statement this
// process issues, so a request that looped over an unbounded id list could
// hold that connection, one statement at a time, for as long as a client
// pleased. 500 is comfortably above anything a person selects by hand in the
// browse grid while still bounding the worst case.
const maxBatchItems = 500

// batchItemsBody is the POST /api/items/batch payload.
type batchItemsBody struct {
	IDs      []string `json:"ids"`
	Action   string   `json:"action"`
	FolderID int64    `json:"folder_id"`
	Tag      string   `json:"tag"`
}

// handleBatchItems serves POST /api/items/batch: one action (delete, move,
// copy or tag) applied to many items in a single request.
//
// Atomicity: this is deliberately per-item, not all-or-nothing. Each id runs
// through the same already-tested per-item store method
// (SoftDeleteItem/MoveItem/AddItemTag) used by the single-item handlers, one
// call at a time, and its own success or failure is recorded independently;
// one id's failure never rolls back or blocks another id's already-applied
// change. Two things make an all-or-nothing transaction the wrong choice
// here: first, SoftDeleteItem enforces per-item authorization (uploader or
// admin) by reading the item and checking the actor inside the same call
// that performs the delete, so wrapping the whole loop in one outer
// transaction would not change what gets checked, only whether a single
// forbidden id could discard everyone else's otherwise-valid change; second,
// the client needs to know exactly which ids succeeded to update the grid
// (removing/moving only those tiles) even when the batch is a mix of a
// user's own items and someone else's. The response's ok/failed lists are
// that per-item report. Bounded by maxBatchItems above so this loop, even in
// the worst case, cannot monopolise the store's one connection indefinitely.
func (s *Server) handleBatchItems(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	user, ok := CurrentUser(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	var body batchItemsBody
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
	tag := body.Tag
	switch body.Action {
	case "delete", "move", "copy":
	case "tag":
		// Validated once, up front, for the whole batch (one tag value is
		// shared by every id in the request) so a malformed tag is reported
		// as the client mistake it is rather than surfacing as a per-item
		// store failure on every single id. Mirrors handleAddItemTag's
		// reasoning exactly (see handlers_items.go).
		normalized, err := db.NormalizeTag(body.Tag)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_tag", "tag must be 1-32 characters of a-z, 0-9, dot, dash or underscore")
			return
		}
		tag = normalized
	default:
		writeJSONError(w, http.StatusBadRequest, "bad_action", "action must be delete, move, copy or tag")
		return
	}

	ctx := r.Context()
	okIDs := make([]string, 0, len(body.IDs))
	failed := make([]map[string]any, 0)
	for _, id := range body.IDs {
		var err error
		switch body.Action {
		case "delete":
			// SoftDeleteItem itself enforces uploader-or-admin per item; no
			// separate authorization check is added here, matching
			// handleDeleteItem's convention exactly (see handlers_items.go).
			err = s.Store.SoftDeleteItem(ctx, id, user)
		case "move":
			err = s.Store.MoveItem(ctx, id, body.FolderID)
		case "copy":
			_, err = s.Store.CopyItem(ctx, id, body.FolderID, user.ID)
		case "tag":
			err = s.Store.AddItemTag(ctx, id, tag)
		}
		if err != nil {
			failed = append(failed, map[string]any{"id": id, "error": err.Error()})
			continue
		}
		okIDs = append(okIDs, id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": len(okIDs), "ok": okIDs, "failed": failed})
}
