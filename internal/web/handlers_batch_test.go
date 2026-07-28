package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boobies-media/internal/db"
)

// batchResult mirrors the JSON shape handleBatchItems returns, used across
// the tests below to decode and assert on applied/ok/failed.
type batchResult struct {
	Applied int      `json:"applied"`
	OK      []string `json:"ok"`
	Failed  []struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	} `json:"failed"`
}

func TestBatchDeleteRemovesItems(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, mediaStore, user.ID, "one", "two", "three")

	ids := []string{items[0].ID, items[1].ID}
	body, _ := json.Marshal(map[string]any{"ids": ids, "action": "delete"})
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/batch", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out batchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Applied != 2 || len(out.Failed) != 0 {
		t.Fatalf("applied=%d failed=%d, want 2/0", out.Applied, len(out.Failed))
	}
	if _, err := srv.Store.ItemByID(ctx, items[0].ID); err == nil {
		t.Error("item[0] still live after batch delete")
	}
	if _, err := srv.Store.ItemByID(ctx, items[2].ID); err != nil {
		t.Error("item[2] should be untouched")
	}
}

func TestBatchMoveAndTag(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, mediaStore, user.ID, "x", "y")
	folder, err := srv.Store.CreateFolder(ctx, 0, "Box")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}

	move, _ := json.Marshal(map[string]any{"ids": []string{items[0].ID, items[1].ID}, "action": "move", "folder_id": folder.ID})
	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/batch", string(move)); rec.Code != http.StatusOK {
		t.Fatalf("move status = %d: %s", rec.Code, rec.Body.String())
	}
	moved, _ := srv.Store.ItemByID(ctx, items[0].ID)
	if moved.FolderID != folder.ID {
		t.Errorf("item[0] folder = %d, want %d", moved.FolderID, folder.ID)
	}

	tag, _ := json.Marshal(map[string]any{"ids": []string{items[0].ID}, "action": "tag", "tag": "batchtag"})
	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/batch", string(tag)); rec.Code != http.StatusOK {
		t.Fatalf("tag status = %d: %s", rec.Code, rec.Body.String())
	}
	tags, _ := srv.Store.ItemTags(ctx, items[0].ID)
	if len(tags) != 1 || tags[0] != "batchtag" {
		t.Errorf("tags = %v, want [batchtag]", tags)
	}
}

func TestBatchCopyCreatesASecondEntryInTheChosenFolder(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, mediaStore, user.ID, "source")
	if err := srv.Store.AddItemTag(ctx, items[0].ID, "copied"); err != nil {
		t.Fatal(err)
	}
	folder, err := srv.Store.CreateFolder(ctx, 0, "Copies")
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{
		"ids": []string{items[0].ID}, "action": "copy", "folder_id": folder.ID,
	})
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/batch", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	folderID := folder.ID
	copies, _, err := srv.Store.ListItems(ctx, db.ItemQuery{FolderID: &folderID})
	if err != nil || len(copies) != 1 {
		t.Fatalf("copies = %+v, err=%v", copies, err)
	}
	if copies[0].ID == items[0].ID || copies[0].ContentHash != items[0].ContentHash {
		t.Errorf("copy = %+v, source = %+v", copies[0], items[0])
	}
}

func TestBatchRejectsUnknownAction(t *testing.T) {
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(context.Background(), "aiden")
	items := seedItems(t, mediaStore, user.ID, "z")
	body, _ := json.Marshal(map[string]any{"ids": []string{items[0].ID}, "action": "explode"})
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/batch", string(body))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestBatchRejectsEmptyIDList(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	body, _ := json.Marshal(map[string]any{"ids": []string{}, "action": "delete"})
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/batch", string(body))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestBatchRejectsTooManyIDs(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	ids := make([]string, maxBatchItems+1)
	for i := range ids {
		ids[i] = itoa(int64(i))
	}
	body, _ := json.Marshal(map[string]any{"ids": ids, "action": "delete"})
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/batch", string(body))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestBatchDeleteReportsForbiddenPerItem(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	// aiden uploads; a second, non-admin user attempts the batch delete.
	uploader := testUser(t, srv, "aiden", "hunter2")
	items := seedItems(t, mediaStore, uploader.ID, "secret")
	cookie := authenticate(t, srv, "mallory") // a different, non-admin user

	body, _ := json.Marshal(map[string]any{"ids": []string{items[0].ID}, "action": "delete"})
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/batch", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a per-item failure", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"failed"`) || !strings.Contains(rec.Body.String(), items[0].ID) {
		t.Errorf("expected a per-item failure entry, got %s", rec.Body.String())
	}
	if _, err := srv.Store.ItemByID(ctx, items[0].ID); err != nil {
		t.Error("a forbidden batch delete still removed the item")
	}
}

// A duplicate id in one batch is not double-applied: the store methods are
// re-run for each occurrence, so the second occurrence of an already-deleted
// item reports its own (not_found) failure rather than being silently
// coalesced with the first, successful, occurrence. That is the observable
// consequence of this handler's per-item, no-shared-transaction design (see
// the doc comment on handleBatchItems).
func TestBatchDeleteWithDuplicateIDReportsSecondAsFailed(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, mediaStore, user.ID, "dup")

	body, _ := json.Marshal(map[string]any{"ids": []string{items[0].ID, items[0].ID}, "action": "delete"})
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/batch", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out batchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Applied != 1 || len(out.Failed) != 1 {
		t.Fatalf("applied=%d failed=%d, want 1/1", out.Applied, len(out.Failed))
	}
	if _, err := srv.Store.ItemByID(ctx, items[0].ID); err == nil {
		t.Error("item still live after a batch delete that included it")
	}
}

func TestBatchRejectsACrossSiteRequest(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, mediaStore, user.ID, "cross")

	body, _ := json.Marshal(map[string]any{"ids": []string{items[0].ID}, "action": "delete"})

	crossSite := httptest.NewRequest(http.MethodPost, "/api/items/batch", strings.NewReader(string(body)))
	crossSite.Header.Set("Content-Type", "application/json")
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSite.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, crossSite)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site batch: status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
	if _, err := srv.Store.ItemByID(ctx, items[0].ID); err != nil {
		t.Error("the item was deleted despite the cross-site request being refused")
	}

	sameOrigin := httptest.NewRequest(http.MethodPost, "/api/items/batch", strings.NewReader(string(body)))
	sameOrigin.Header.Set("Content-Type", "application/json")
	sameOrigin.Header.Set("Sec-Fetch-Site", "same-origin")
	sameOrigin.AddCookie(cookie)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, sameOrigin)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin batch: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if _, err := srv.Store.ItemByID(ctx, items[0].ID); err == nil {
		t.Error("the item is still live after a same-origin batch delete")
	}
}
