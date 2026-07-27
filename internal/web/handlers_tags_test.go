package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestListTagsReturnsSortedNames(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, err := srv.Store.UserByUsername(ctx, "aiden")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	items := seedItems(t, mediaStore, user.ID, "a")
	if err := srv.Store.AddItemTag(ctx, items[0].ID, "funny"); err != nil {
		t.Fatalf("AddItemTag: %v", err)
	}

	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/tags", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Tags) != 1 || out.Tags[0] != "funny" {
		t.Errorf("tags = %v, want [funny]", out.Tags)
	}
}

func TestListTagsOnAnEmptyStoreReturnsAnEmptyListNotNull(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")

	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/tags", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"tags":[]}` {
		t.Errorf("body = %s, want {\"tags\":[]}", got)
	}
}

func TestRandomItemEndpointReturnsAnItem(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, err := srv.Store.UserByUsername(ctx, "aiden")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	seedItems(t, mediaStore, user.ID, "only")

	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/random", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		Item map[string]any `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Item["share_url"] == "" || out.Item["id"] == "" {
		t.Errorf("random item missing id/share_url: %v", out.Item)
	}
}

func TestRandomItemEndpointExcludesRevokedAndDeletedItems(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, err := srv.Store.UserByUsername(ctx, "aiden")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	items := seedItems(t, mediaStore, user.ID, "live", "revoked", "deleted")
	if err := srv.Store.SetItemShareRevoked(ctx, items[1].ID, true); err != nil {
		t.Fatalf("SetItemShareRevoked: %v", err)
	}
	if err := srv.Store.SoftDeleteItem(ctx, items[2].ID, user); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}

	for i := 0; i < 20; i++ {
		rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/random", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var out struct {
			Item map[string]any `json:"item"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got := out.Item["id"]; got != items[0].ID {
			t.Fatalf("random item id = %v, want the only servable item %s", got, items[0].ID)
		}
	}
}

func TestRandomItemEndpoint404sWhenEmpty(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/random?tag=nope", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestRandomItemEndpoint404sWhenNoItemsExistAtAll(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/random", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestRandomItemEndpointFiltersByTag(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, err := srv.Store.UserByUsername(ctx, "aiden")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	items := seedItems(t, mediaStore, user.ID, "tagged", "plain")
	if err := srv.Store.AddItemTag(ctx, items[0].ID, "cats"); err != nil {
		t.Fatalf("AddItemTag: %v", err)
	}

	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/random?tag="+url.QueryEscape("  Cats  "), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		Item map[string]any `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := out.Item["id"]; got != items[0].ID {
		t.Errorf("random item id = %v, want %s", got, items[0].ID)
	}
	tags, _ := out.Item["tags"].([]any)
	found := false
	for _, tag := range tags {
		if tag == "cats" {
			found = true
		}
	}
	if !found {
		t.Errorf("returned item's tags = %v, want it to include cats", out.Item["tags"])
	}
}
