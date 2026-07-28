package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"boobies-media/internal/auth"
)

// signedInRequest returns a request carrying a valid session for username.
func signedInRequest(t *testing.T, srv *Server, method, path, username string) *http.Request {
	t.Helper()
	user := testUser(t, srv, username, "hunter2")
	token := "session-for-" + username
	if err := srv.Store.CreateSession(context.Background(), auth.HashToken(token), user.ID, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	req := httptest.NewRequest(method, path, nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	return req
}

func TestBrowseRendersForSignedInUser(t *testing.T) {
	srv := testServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, signedInRequest(t, srv, http.MethodGet, "/", "aiden"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Display aiden") {
		t.Error("browse page does not show the signed-in display name")
	}
	if !strings.Contains(body, "Sign out") {
		t.Error("browse page does not render the signed-in header")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

func TestBrowseRequiresAuthentication(t *testing.T) {
	srv := testServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login") {
		t.Errorf("Location = %q, want a redirect to /login", loc)
	}
}

func TestFaviconIsPublic(t *testing.T) {
	srv := testServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.Bytes(); len(body) < 4 || string(body[:4]) != "\x00\x00\x01\x00" {
		t.Error("favicon response is not an ICO file")
	}
}

func TestUnknownPathIs404ForSignedInUser(t *testing.T) {
	srv := testServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, signedInRequest(t, srv, http.MethodGet, "/no-such-page", "aiden"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestBrowseRendersTheFirstPageOfItems(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, err := srv.Store.UserByUsername(ctx, "aiden")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	items := seedItems(t, mediaStore, user.ID, "first", "second")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, item := range items {
		if !strings.Contains(body, item.ID) {
			t.Errorf("the browse page does not mention item %s", item.ID)
		}
	}
	if !strings.Contains(body, `data-island="grid"`) {
		t.Error("the grid island is not mounted")
	}
	if !strings.Contains(body, `data-island="uploader"`) {
		t.Error("the uploader island is not mounted")
	}
	if !strings.Contains(body, `data-island="lightbox"`) {
		t.Error("the lightbox island is not mounted")
	}
	if !strings.Contains(body, "/t/"+items[0].ID) {
		t.Error("thumbnails are not referenced by their /t/ URL")
	}
}

func TestBrowseEscapesItemTitles(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, mediaStore, user.ID, "ok")
	if err := srv.Store.SetItemTitle(ctx, items[0].ID, `<img src=x onerror=alert(1)>`); err != nil {
		t.Fatalf("SetItemTitle: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "<img src=x onerror=alert(1)>") {
		t.Fatal("an item title was rendered unescaped")
	}
}

func TestBrowseLightboxHasDialogSemantics(t *testing.T) {
	srv := testServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, signedInRequest(t, srv, http.MethodGet, "/", "aiden"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-island="lightbox"`) {
		t.Fatal("the lightbox island is not mounted")
	}
	for _, want := range []string{`role="dialog"`, `aria-modal="true"`, `aria-label="Item viewer"`} {
		if !strings.Contains(body, want) {
			t.Errorf("browse page lightbox markup is missing %s", want)
		}
	}
}

func TestBrowseShowsAnEmptyStateWithNoItems(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "Nothing here yet") {
		t.Error("the empty library has no empty state")
	}
}

// browsePage fetches GET target as a signed-in user and returns the body.
func browsePage(t *testing.T, srv *Server, cookie *http.Cookie, target string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", target, rec.Code)
	}
	return rec.Body.String()
}

func TestBrowseMountsTheFoldersIsland(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")

	body := browsePage(t, srv, cookie, "/")
	for _, mount := range []string{
		`data-island="folders"`, // folder create/rename/move/delete
		`data-role="folder"`,    // the lightbox folder-move select
	} {
		if !strings.Contains(body, mount) {
			t.Errorf("browse page is missing mount point %s", mount)
		}
	}
}

func TestBrowseMarksTheActiveFolderForTheFoldersIsland(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	id := createFolder(t, srv, cookie, "Memes", 0)

	body := browsePage(t, srv, cookie, "/?folder="+itoa(id))
	if want := `data-current="` + itoa(id) + `"`; !strings.Contains(body, want) {
		t.Errorf("the folders island did not record the active folder id (%s)", want)
	}

	if root := browsePage(t, srv, cookie, "/"); !strings.Contains(root, `data-current=""`) {
		t.Error("the folders island should carry an empty data-current at the library root")
	}
}

func TestBrowseLightboxFolderSelectListsEveryFolder(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	parent := createFolder(t, srv, cookie, "Memes", 0)
	child := createFolder(t, srv, cookie, "Reaction", parent)

	body := browsePage(t, srv, cookie, "/")
	for _, want := range []string{
		`<option value="0">Library</option>`,
		`<option value="` + itoa(parent) + `">Memes</option>`,
		// Nested folders are indented with non-breaking spaces: option text
		// cannot be indented with CSS.
		`<option value="` + itoa(child) + `">` + "\u00a0\u00a0" + `Reaction</option>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the lightbox folder select is missing %q", want)
		}
	}
}
