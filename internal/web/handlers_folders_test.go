package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boobies-media/internal/jobs"
)

type folderResp struct {
	Folder struct {
		ID       int64  `json:"id"`
		ParentID int64  `json:"parent_id"`
		Name     string `json:"name"`
	} `json:"folder"`
}

func createFolder(t *testing.T, srv *Server, cookie *http.Cookie, name string, parent int64) int64 {
	t.Helper()
	body := `{"name":"` + name + `","parent_id":` + itoa(parent) + `}`
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/folders", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %q: status = %d: %s", name, rec.Code, rec.Body.String())
	}
	var out folderResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	return out.Folder.ID
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return out.Code
}

func TestFolderCRUDRoundTrip(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")

	root := createFolder(t, srv, cookie, "Memes", 0)
	child := createFolder(t, srv, cookie, "Reaction", root)

	// List returns both.
	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/folders", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	var listed struct {
		Folders []struct {
			ID       int64  `json:"id"`
			ParentID int64  `json:"parent_id"`
			Name     string `json:"name"`
		} `json:"folders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Folders) != 2 {
		t.Fatalf("listed %d folders, want 2", len(listed.Folders))
	}

	// Rename the child.
	rec = apiRequest(t, srv, cookie, http.MethodPatch, "/api/folders/"+itoa(child), `{"name":"Reactions"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d: %s", rec.Code, rec.Body.String())
	}
	var renamed folderResp
	_ = json.Unmarshal(rec.Body.Bytes(), &renamed)
	if renamed.Folder.Name != "Reactions" {
		t.Errorf("renamed to %q, want Reactions", renamed.Folder.Name)
	}

	// Delete the child.
	rec = apiRequest(t, srv, cookie, http.MethodDelete, "/api/folders/"+itoa(child), "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete status = %d, want 204", rec.Code)
	}
}

func TestMoveFolderContentsQueuesAndRunsBackgroundJob(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	queue := jobs.New(srv.Store, 1)
	srv.Queue = queue
	queue.Register(jobs.TypeFolderMove, srv.handleFolderMoveJob)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	source := createFolder(t, srv, cookie, "Source", 0)
	destination := createFolder(t, srv, cookie, "Destination", 0)
	items := seedItems(t, mediaStore, user.ID, "one", "two")
	for _, item := range items {
		if err := srv.Store.MoveItem(ctx, item.ID, source); err != nil {
			t.Fatalf("MoveItem: %v", err)
		}
	}

	body := `{"destination_id":` + itoa(destination) + `}`
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/folders/"+itoa(source)+"/move-contents", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("queue move status = %d: %s", rec.Code, rec.Body.String())
	}
	ran, err := queue.RunOnce(ctx)
	if err != nil || !ran {
		t.Fatalf("RunOnce = %v, %v; want true, nil", ran, err)
	}
	for _, item := range items {
		got, err := srv.Store.ItemByID(ctx, item.ID)
		if err != nil || got.FolderID != destination {
			t.Errorf("item %s destination = %d, %v; want %d", item.ID, got.FolderID, err, destination)
		}
	}
}

func TestFolderMoveRejectsCycle(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")

	parent := createFolder(t, srv, cookie, "A", 0)
	child := createFolder(t, srv, cookie, "B", parent)

	// Moving the parent under its own child is a cycle.
	rec := apiRequest(t, srv, cookie, http.MethodPatch, "/api/folders/"+itoa(parent), `{"parent_id":`+itoa(child)+`}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("cycle move status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "folder_cycle" {
		t.Errorf("error code = %q, want folder_cycle", code)
	}
}

func TestFolderCreateRejectsBlankName(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/folders", `{"name":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("blank-name status = %d, want 400", rec.Code)
	}
	if code := errorCode(t, rec); code != "bad_folder_name" {
		t.Errorf("error code = %q, want bad_folder_name", code)
	}
}

func TestFolderCreateRejectsNameOverLimit(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	long := strings.Repeat("a", 101)
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/folders", `{"name":"`+long+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("over-limit-name status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "bad_folder_name" {
		t.Errorf("error code = %q, want bad_folder_name", code)
	}
}

func TestFolderCreateRejectsNameWithSlash(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/folders", `{"name":"a/b"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("slash-name status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "bad_folder_name" {
		t.Errorf("error code = %q, want bad_folder_name", code)
	}
}

func TestFolderCreateRejectsDuplicateNameUnderSameParent(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	parent := createFolder(t, srv, cookie, "Memes", 0)
	createFolder(t, srv, cookie, "Reaction", parent)

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/folders", `{"name":"Reaction","parent_id":`+itoa(parent)+`}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate-name status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "duplicate_folder" {
		t.Errorf("error code = %q, want duplicate_folder", code)
	}
}

func TestFolderCreateRejectsMalformedJSON(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/folders", `{"name":`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed-json status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "bad_request" {
		t.Errorf("error code = %q, want bad_request", code)
	}
}

func TestFolderUpdateRejectsMalformedJSON(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	folder := createFolder(t, srv, cookie, "Memes", 0)
	rec := apiRequest(t, srv, cookie, http.MethodPatch, "/api/folders/"+itoa(folder), `{"name":`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed-json status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "bad_request" {
		t.Errorf("error code = %q, want bad_request", code)
	}
}

func TestFolderUpdateRejectsNonNumericID(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodPatch, "/api/folders/not-a-number", `{"name":"whatever"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-numeric-id status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "bad_request" {
		t.Errorf("error code = %q, want bad_request", code)
	}
}

func TestFolderDeleteRejectsNonNumericID(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodDelete, "/api/folders/not-a-number", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-numeric-id status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "bad_request" {
		t.Errorf("error code = %q, want bad_request", code)
	}
}

func TestFolderCreateRejectsUnknownParent(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/folders", `{"name":"orphan","parent_id":9999}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown-parent status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestFolderUpdateRejectsUnknownID(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodPatch, "/api/folders/9999", `{"name":"whatever"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown-id rename status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestFolderDeleteRejectsUnknownID(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodDelete, "/api/folders/9999", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown-id delete status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestFolderRenameRejectsCollisionWithSibling(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	parent := createFolder(t, srv, cookie, "Memes", 0)
	createFolder(t, srv, cookie, "Reaction", parent)
	moving := createFolder(t, srv, cookie, "Clips", parent)

	rec := apiRequest(t, srv, cookie, http.MethodPatch, "/api/folders/"+itoa(moving), `{"name":"Reaction"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("rename-collision status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "duplicate_folder" {
		t.Errorf("error code = %q, want duplicate_folder", code)
	}
}

func TestFolderMoveRejectsCollisionWithDestinationSibling(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	parentA := createFolder(t, srv, cookie, "A", 0)
	parentB := createFolder(t, srv, cookie, "B", 0)
	createFolder(t, srv, cookie, "Clips", parentB)
	moving := createFolder(t, srv, cookie, "Clips", parentA)

	rec := apiRequest(t, srv, cookie, http.MethodPatch, "/api/folders/"+itoa(moving), `{"parent_id":`+itoa(parentB)+`}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("move-collision status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "duplicate_folder" {
		t.Errorf("error code = %q, want duplicate_folder", code)
	}
}

func TestFolderRoutesRequireAuth(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/folders", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous folder list status = %d, want 401", rec.Code)
	}
}
