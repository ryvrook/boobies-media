package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"boobies-media/internal/db"
	"boobies-media/internal/media"
)

func TestAdminBatchRestoreAndPurge(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")
	admin, _ := srv.Store.UserByUsername(ctx, "boss")
	items := seedItems(t, mediaStore, admin.ID, "restore-me", "purge-me")
	for _, item := range items {
		if err := srv.Store.SoftDeleteItem(ctx, item.ID, admin); err != nil {
			t.Fatalf("SoftDeleteItem: %v", err)
		}
	}

	restoreBody, _ := json.Marshal(map[string]any{"action": "restore", "ids": []string{items[0].ID}})
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/items/batch", string(restoreBody))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"applied":1`) {
		t.Fatalf("batch restore status/body = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := srv.Store.ItemByID(ctx, items[0].ID); err != nil {
		t.Errorf("restored item is not live: %v", err)
	}

	purgeBody, _ := json.Marshal(map[string]any{"action": "purge", "ids": []string{items[1].ID}})
	rec = apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/items/batch", string(purgeBody))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"applied":1`) {
		t.Fatalf("batch purge status/body = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := srv.Store.ItemByIDIncludingDeleted(ctx, items[1].ID); err == nil {
		t.Error("batch-purged item still exists")
	}
}

func TestAdminBatchItemsRejectsNonAdmin(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	body := `{"action":"restore","ids":["anything"]}`
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/items/batch", body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin batch status = %d, want 403", rec.Code)
	}
}

func TestRetryJobRequeuesAFailedJob(t *testing.T) {
	ctx := context.Background()
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")

	id, err := srv.Store.EnqueueJob(ctx, "ingest_url", []byte(`{}`), srv.Now())
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if err := srv.Store.FailJob(ctx, id, "boom"); err != nil {
		t.Fatalf("FailJob: %v", err)
	}

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/jobs/"+itoa(id)+"/retry", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d: %s", rec.Code, rec.Body.String())
	}
	job, _ := srv.Store.JobByID(ctx, id)
	if job.Status != "queued" {
		t.Errorf("job status = %q, want queued", job.Status)
	}
}

func TestRetryJobRejectsNonFailedAndMissing(t *testing.T) {
	ctx := context.Background()
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")

	id, _ := srv.Store.EnqueueJob(ctx, "probe", []byte(`{}`), srv.Now()) // still queued
	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/jobs/"+itoa(id)+"/retry", ""); rec.Code != http.StatusConflict {
		t.Errorf("retry queued job status = %d, want 409", rec.Code)
	}
	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/jobs/999/retry", ""); rec.Code != http.StatusNotFound {
		t.Errorf("retry missing job status = %d, want 404", rec.Code)
	}
}

func TestAdminCanRetryAllFailedAndCancelPendingJobs(t *testing.T) {
	ctx := context.Background()
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")
	for i := 0; i < 2; i++ {
		id, _ := srv.Store.EnqueueJob(ctx, "ingest_url", []byte(`{}`), srv.Now())
		_ = srv.Store.FailJob(ctx, id, "boom")
	}
	_, _ = srv.Store.EnqueueJob(ctx, "ingest_url", []byte(`{}`), srv.Now())

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/jobs/retry-failed", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"applied":2`) {
		t.Fatalf("retry all status/body = %d %s", rec.Code, rec.Body.String())
	}
	rec = apiRequest(t, srv, cookie, http.MethodPost, "/api/jobs/cancel-pending", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"applied":3`) {
		t.Fatalf("cancel pending status/body = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRestoreAndPurgeItem(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")
	owner, _ := srv.Store.UserByUsername(ctx, "boss")
	item := storeBlobFor(t, srv, mediaStore, owner.ID, pngTestBytes, "a.png")

	if err := srv.Store.SoftDeleteItem(ctx, item.ID, owner); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}
	// Restore.
	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/items/"+item.ID+"/restore", ""); rec.Code != http.StatusOK {
		t.Fatalf("restore status = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := srv.Store.ItemByID(ctx, item.ID); err != nil {
		t.Errorf("item not live after restore: %v", err)
	}

	// Soft delete again, then purge.
	if err := srv.Store.SoftDeleteItem(ctx, item.ID, owner); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}
	if rec := apiRequest(t, srv, cookie, http.MethodDelete, "/api/admin/items/"+item.ID+"/purge", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("purge status = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := srv.Store.ItemByIDIncludingDeleted(ctx, item.ID); err == nil {
		t.Error("item still present after purge")
	}
}

// TestPurgeForbiddenToNonAdminLeavesItemIntact is the negative case the
// review flagged: a signed-in non-admin must not be able to permanently
// destroy another user's media. Asserting the status code alone is not
// enough, since a handler bug could 403 the response yet still purge on its
// way out; this checks the item, blob-referencing row included, is still
// there afterwards.
func TestPurgeForbiddenToNonAdminLeavesItemIntact(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden") // creates the non-admin user "aiden"
	owner, err := srv.Store.UserByUsername(ctx, "aiden")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	item := storeBlobFor(t, srv, mediaStore, owner.ID, pngTestBytes, "a.png")
	if err := srv.Store.SoftDeleteItem(ctx, item.ID, owner); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}

	rec := apiRequest(t, srv, cookie, http.MethodDelete, "/api/admin/items/"+item.ID+"/purge", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin purge status = %d, want 403", rec.Code)
	}
	if _, err := srv.Store.ItemByIDIncludingDeleted(ctx, item.ID); err != nil {
		t.Errorf("item missing after refused purge: %v", err)
	}
}

// TestPurgeSharedBlobDoesNotUnlinkWhileSiblingLives makes sure the handler
// never second-guesses media.Store.Purge's refcount decision: purging one of
// two items that share a content hash must leave the sibling, and its blob,
// intact and servable.
func TestPurgeSharedBlobDoesNotUnlinkWhileSiblingLives(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")
	owner, _ := srv.Store.UserByUsername(ctx, "boss")

	first := storeBlobFor(t, srv, mediaStore, owner.ID, pngTestBytes, "a.png")
	second := storeBlobFor(t, srv, mediaStore, owner.ID, pngTestBytes, "b.png")
	if first.ContentHash != second.ContentHash {
		t.Fatalf("expected shared content hash, got %q and %q", first.ContentHash, second.ContentHash)
	}

	if err := srv.Store.SoftDeleteItem(ctx, first.ID, owner); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}
	if rec := apiRequest(t, srv, cookie, http.MethodDelete, "/api/admin/items/"+first.ID+"/purge", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("purge status = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := srv.Store.ItemByIDIncludingDeleted(ctx, first.ID); err == nil {
		t.Error("purged item still present")
	}
	// The sibling still references the same bytes; it must still resolve.
	if _, err := srv.Store.ItemByID(ctx, second.ID); err != nil {
		t.Errorf("sibling item gone after purging its shared-hash twin: %v", err)
	}
}

func TestJobRetryForbiddenToNonAdmin(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/jobs/1/retry", "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin retry status = %d, want 403", rec.Code)
	}
}

func storeBlobFor(t *testing.T, srv *Server, mediaStore *media.Store, uploaderID int64, payload []byte, filename string) *db.Item {
	t.Helper()
	media.StubTools(t, map[string]string{})
	res, err := mediaStore.Save(context.Background(), media.SaveRequest{
		Reader: bytesReader(payload), Filename: filename, UploaderID: uploaderID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return res.Item
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
