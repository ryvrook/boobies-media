package web

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"boobies-media/internal/db"
	"boobies-media/internal/media"
)

// jobErrorRecordingHandler is a slog.Handler that captures records in memory
// so a test can assert a suppressed failure was actually logged, not just
// dropped. Same pattern as recordingHandler in internal/media/store_test.go
// and internal/jobs/queue_test.go.
type jobErrorRecordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *jobErrorRecordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *jobErrorRecordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *jobErrorRecordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *jobErrorRecordingHandler) WithGroup(string) slog.Handler      { return h }

// hasRecordContaining reports whether any record at the given level carries
// substr in its message or in any attribute value.
func (h *jobErrorRecordingHandler) hasRecordContaining(level slog.Level, substr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level != level {
			continue
		}
		if strings.Contains(r.Message, substr) {
			return true
		}
		found := false
		r.Attrs(func(a slog.Attr) bool {
			if strings.Contains(a.Value.String(), substr) {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// apiRequest issues an authenticated JSON request and returns the recorder.
func apiRequest(t *testing.T, srv *Server, cookie *http.Cookie, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// seedItems uploads n files through the real pipeline.
func seedItems(t *testing.T, mediaStore *media.Store, uploaderID int64, names ...string) []*db.Item {
	t.Helper()
	media.StubTools(t, map[string]string{})
	var items []*db.Item
	for _, name := range names {
		// Vary the bytes so each upload gets its own content hash.
		payload := append(append([]byte{}, pngTestBytes...), []byte(name)...)
		res, err := mediaStore.Save(context.Background(), media.SaveRequest{
			Reader: bytes.NewReader(payload), Filename: name + ".png", UploaderID: uploaderID})
		if err != nil {
			t.Fatalf("Save(%s): %v", name, err)
		}
		items = append(items, res.Item)
	}
	return items
}

func TestListItemsAPIPaginatesWithACursor(t *testing.T) {
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, err := srv.Store.UserByUsername(context.Background(), "aiden")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	seedItems(t, mediaStore, user.ID, "a", "b", "c", "d", "e")

	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/items?limit=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(page.Items))
	}
	if page.NextCursor == "" {
		t.Fatal("next_cursor is empty although more items exist")
	}

	rec = apiRequest(t, srv, cookie, http.MethodGet, "/api/items?limit=2&cursor="+page.NextCursor, "")
	var second struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, a := range page.Items {
		for _, b := range second.Items {
			if a["id"] == b["id"] {
				t.Errorf("item %v appeared on both pages", a["id"])
			}
		}
	}
}

func TestListItemsAPIRejectsABadSort(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/items?sort=nonsense", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestListItemsAPIBadSortDoesNotLeakInternalError guards against
// db.ParseItemSort's "db: unknown sort ..." message reaching the client
// verbatim: the response must be a clean, generic 400.
func TestListItemsAPIBadSortDoesNotLeakInternalError(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/items?sort=nonsense", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "db:") {
		t.Errorf("response body leaks an internal error string: %s", rec.Body.String())
	}
}

func TestGetItemIncludesTagsAndURLs(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, mediaStore, user.ID, "cat")
	if err := srv.Store.AddItemTag(ctx, items[0].ID, "cats"); err != nil {
		t.Fatalf("AddItemTag: %v", err)
	}

	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/items/"+items[0].ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Item struct {
			ID       string   `json:"id"`
			Tags     []string `json:"tags"`
			ShareURL string   `json:"share_url"`
		} `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Item.Tags) != 1 || body.Item.Tags[0] != "cats" {
		t.Errorf("tags = %v, want [cats]", body.Item.Tags)
	}
	if !strings.HasSuffix(body.Item.ShareURL, "/s/"+items[0].ID) {
		t.Errorf("share_url = %q", body.Item.ShareURL)
	}
}

func TestGetItemUnknown(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/items/nosuchid", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPatchItemRenamesAndRevokes(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, mediaStore, user.ID, "cat")

	rec := apiRequest(t, srv, cookie, http.MethodPatch, "/api/items/"+items[0].ID,
		`{"title":"A Better Name","share_revoked":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	got, err := srv.Store.ItemByID(ctx, items[0].ID)
	if err != nil {
		t.Fatalf("ItemByID: %v", err)
	}
	if got.Title != "A Better Name" {
		t.Errorf("Title = %q", got.Title)
	}
	if !got.ShareRevoked {
		t.Error("ShareRevoked = false after a revoke request")
	}
}

func TestPatchItemMovesToAFolderAndBack(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, mediaStore, user.ID, "cat")
	folder, err := srv.Store.CreateFolder(ctx, 0, "memes")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}

	body := `{"folder_id":` + strconv.FormatInt(folder.ID, 10) + `}`
	if rec := apiRequest(t, srv, cookie, http.MethodPatch, "/api/items/"+items[0].ID, body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	got, _ := srv.Store.ItemByID(ctx, items[0].ID)
	if got.FolderID != folder.ID {
		t.Errorf("FolderID = %d, want %d", got.FolderID, folder.ID)
	}

	if rec := apiRequest(t, srv, cookie, http.MethodPatch, "/api/items/"+items[0].ID, `{"folder_id":0}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got, _ = srv.Store.ItemByID(ctx, items[0].ID)
	if got.FolderID != 0 {
		t.Errorf("FolderID = %d, want 0", got.FolderID)
	}
}

func TestItemTagEndpoints(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, mediaStore, user.ID, "cat")

	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/"+items[0].ID+"/tags", `{"tag":"Cats"}`); rec.Code != http.StatusOK {
		t.Fatalf("add tag: status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	tags, err := srv.Store.ItemTags(ctx, items[0].ID)
	if err != nil {
		t.Fatalf("ItemTags: %v", err)
	}
	if len(tags) != 1 || tags[0] != "cats" {
		t.Errorf("tags = %v, want [cats] lowercased", tags)
	}

	if rec := apiRequest(t, srv, cookie, http.MethodDelete, "/api/items/"+items[0].ID+"/tags/cats", ""); rec.Code != http.StatusOK {
		t.Fatalf("remove tag: status = %d", rec.Code)
	}
	tags, _ = srv.Store.ItemTags(ctx, items[0].ID)
	if len(tags) != 0 {
		t.Errorf("tags = %v, want none", tags)
	}

	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/"+items[0].ID+"/tags", `{"tag":"not a valid tag"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid tag: status = %d, want 400", rec.Code)
	}
}

// TestAddItemTagStoreFailureIsA500 simulates a genuine store failure (as
// opposed to a bad tag format) by dropping the tags table out from under a
// well-formed request. That must surface as a logged 500, never as a silent
// 400 that hides a real outage from the server logs.
func TestAddItemTagStoreFailureIsA500(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, mediaStore, user.ID, "cat")

	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	// The tag itself is well-formed; the failure comes from the store, not
	// from validation, so it must never be reported as a 400.
	if _, err := srv.Store.DB.ExecContext(ctx, `DROP TABLE tags`); err != nil {
		t.Fatalf("drop tags table to simulate a store failure: %v", err)
	}

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/"+items[0].ID+"/tags", `{"tag":"cats"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "db:") || strings.Contains(rec.Body.String(), "no such table") {
		t.Errorf("response body leaks an internal error string: %s", rec.Body.String())
	}
	if !strings.Contains(logBuf.String(), "request failed") {
		t.Errorf("the store failure was not logged: %s", logBuf.String())
	}
}

func TestDeleteItemAuthorization(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	ownerCookie := authenticate(t, srv, "owner")
	otherCookie := authenticate(t, srv, "other")
	owner, _ := srv.Store.UserByUsername(ctx, "owner")
	items := seedItems(t, mediaStore, owner.ID, "a")

	if rec := apiRequest(t, srv, otherCookie, http.MethodDelete, "/api/items/"+items[0].ID, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("another friend deleting: status = %d, want 403", rec.Code)
	}
	if _, err := srv.Store.ItemByID(ctx, items[0].ID); err != nil {
		t.Error("the item was deleted despite the 403")
	}

	if rec := apiRequest(t, srv, ownerCookie, http.MethodDelete, "/api/items/"+items[0].ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("uploader deleting: status = %d, want 200", rec.Code)
	}
	if _, err := srv.Store.ItemByID(ctx, items[0].ID); err == nil {
		t.Error("the item is still live after its uploader deleted it")
	}
	// The blob survives: delete is soft, and an admin purge is what unlinks.
	if _, err := srv.Store.ItemByIDIncludingDeleted(ctx, items[0].ID); err != nil {
		t.Error("a soft delete removed the row entirely")
	}
}

// TestItemAPIRejectsACrossSiteDelete covers Finding I1: the destructive item
// routes (PATCH/DELETE /api/items/{id}, POST/DELETE .../tags) now carry the
// same second-lock CSRF check as /api/ingest and the /api/uploads* routes.
// A cross-site DELETE must be refused and the item must survive; the same
// request from the same origin must still succeed.
func TestItemAPIRejectsACrossSiteDelete(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, mediaStore, user.ID, "cat")

	crossSite := httptest.NewRequest(http.MethodDelete, "/api/items/"+items[0].ID, nil)
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSite.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, crossSite)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site delete: status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
	if _, err := srv.Store.ItemByID(ctx, items[0].ID); err != nil {
		t.Error("the item was deleted despite the cross-site request being refused")
	}

	sameOrigin := httptest.NewRequest(http.MethodDelete, "/api/items/"+items[0].ID, nil)
	sameOrigin.Header.Set("Sec-Fetch-Site", "same-origin")
	sameOrigin.AddCookie(cookie)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, sameOrigin)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin delete: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if _, err := srv.Store.ItemByID(ctx, items[0].ID); err == nil {
		t.Error("the item is still live after a same-origin delete")
	}
}

func TestJobStatusReportsResultingItems(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")

	jobID, err := srv.Store.EnqueueJob(ctx, "ingest_url", []byte(`{"url":"https://example.com"}`), user.CreatedAt)
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	media.StubTools(t, map[string]string{})
	res, err := mediaStore.Save(ctx, media.SaveRequest{
		Reader: bytes.NewReader(pngTestBytes), Filename: "from-url.png", UploaderID: user.ID, JobID: jobID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := srv.Store.CompleteJob(ctx, jobID); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}

	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/jobs/"+strconv.FormatInt(jobID, 10), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Status string `json:"status"`
		Items  []struct {
			ID       string `json:"id"`
			ShareURL string `json:"share_url"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "done" {
		t.Errorf("status = %q, want done", body.Status)
	}
	if len(body.Items) != 1 || body.Items[0].ID != res.Item.ID {
		t.Fatalf("items = %+v, want the item the job produced", body.Items)
	}
	if body.Items[0].ShareURL == "" {
		t.Error("the resulting item has no share URL; the uploader island needs it")
	}
}

func TestJobStatusUnknown(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	if rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/jobs/9999", ""); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestJobStatusHidesInternalErrorDetail covers Finding I2: a failed job's
// error column routinely carries raw ffmpeg/ffprobe stderr and an absolute
// filesystem path (see internal/media/exec.go and thumbs.go), and
// GET /api/jobs/{id} used to serialise it verbatim. The client must get a
// fixed, safe message instead, and the real cause must still be logged, the
// same way serverError logs it, so the failure isn't just dropped.
func TestJobStatusHidesInternalErrorDetail(t *testing.T) {
	ctx := context.Background()
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")

	jobID, err := srv.Store.EnqueueJob(ctx, "ingest_url", []byte(`{"url":"https://example.com"}`), user.CreatedAt)
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	rawCause := "media: ffmpeg failed: Input #0, matroska,webm: /data/files/ab/cd/abcd1234.mp4: No such file or directory"
	if err := srv.Store.FailJob(ctx, jobID, rawCause); err != nil {
		t.Fatalf("FailJob: %v", err)
	}

	handler := &jobErrorRecordingHandler{}
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/jobs/"+strconv.FormatInt(jobID, 10), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "/data/files") {
		t.Errorf("response body leaks an absolute filesystem path: %s", body)
	}
	if strings.Contains(body, "ffmpeg") || strings.Contains(body, "No such file or directory") {
		t.Errorf("response body leaks raw tool stderr: %s", body)
	}

	var decoded struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Status != "failed" {
		t.Errorf("status = %q, want failed", decoded.Status)
	}
	if decoded.Error == "" || decoded.Error == rawCause {
		t.Errorf("error = %q, want a fixed safe message, not the raw job error", decoded.Error)
	}

	if !handler.hasRecordContaining(slog.LevelError, "/data/files") {
		t.Error("the raw job failure was not logged; the detail must not simply be dropped")
	}
}

func TestItemAPIRequiresAuthentication(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	for _, target := range []string{"/api/items", "/api/items/abc12345", "/api/jobs/1"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", target, rec.Code)
		}
	}
}
