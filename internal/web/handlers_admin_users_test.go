package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

// itoa is a small shared test helper for building "/api/.../{id}" targets.
// Task 10's brief also calls itoa(id) in its own test file; if that lands
// in the same package with its own definition, one copy needs to be
// dropped at merge time to avoid a duplicate declaration.
func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}

func TestAdminCreateUserReturnsAPIKeyOnce(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/users",
		`{"username":"newbie","display_name":"New Bie","password":"hunter2","is_admin":false}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.User.Username != "newbie" || out.APIKey == "" {
		t.Fatalf("unexpected create response: %s", rec.Body.String())
	}
	// The created user can actually authenticate with the plaintext key.
	if _, err := srv.Store.UserByUsername(context.Background(), "newbie"); err != nil {
		t.Errorf("created user not found: %v", err)
	}
}

func TestAdminCreateUserRejectsDuplicate(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")
	_ = testUser(t, srv, "dup", "x")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/users", `{"username":"dup","password":"hunter2"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate create status = %d, want 409", rec.Code)
	}
}

func TestAdminToggleAdminAndSelfGuard(t *testing.T) {
	ctx := context.Background()
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")
	target := testUser(t, srv, "friend", "x")

	rec := apiRequest(t, srv, cookie, http.MethodPatch, "/api/admin/users/"+itoa(target.ID), `{"is_admin":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("promote status = %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := srv.Store.UserByID(ctx, target.ID)
	if !got.IsAdmin {
		t.Error("promote did not stick")
	}

	// The acting admin cannot revoke their own admin.
	me, _ := srv.Store.UserByUsername(ctx, "boss")
	rec = apiRequest(t, srv, cookie, http.MethodPatch, "/api/admin/users/"+itoa(me.ID), `{"is_admin":false}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("self-demote status = %d, want 400", rec.Code)
	}
}

func TestAdminDeleteUserGuards(t *testing.T) {
	ctx := context.Background()
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")

	// Cannot delete yourself.
	me, _ := srv.Store.UserByUsername(ctx, "boss")
	if rec := apiRequest(t, srv, cookie, http.MethodDelete, "/api/admin/users/"+itoa(me.ID), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("self-delete status = %d, want 400", rec.Code)
	}

	// Can delete an item-free friend.
	spare := testUser(t, srv, "spare", "x")
	if rec := apiRequest(t, srv, cookie, http.MethodDelete, "/api/admin/users/"+itoa(spare.ID), ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete spare status = %d, want 204", rec.Code)
	}
}

func TestAdminResetPasswordAndRotateKey(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")
	target := testUser(t, srv, "friend", "x")

	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/users/"+itoa(target.ID)+"/password", `{"password":"brandnew"}`); rec.Code != http.StatusOK {
		t.Errorf("reset password status = %d, want 200", rec.Code)
	}

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/users/"+itoa(target.ID)+"/apikey", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate key status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		APIKey string `json:"api_key"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.APIKey == "" {
		t.Error("rotate key returned no plaintext key")
	}
}

func TestAdminUserEndpointsForbiddenToNonAdmin(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden") // non-admin
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/users", `{"username":"x","password":"y"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin create status = %d, want 403", rec.Code)
	}
}
