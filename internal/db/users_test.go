package db_test

import (
	"context"
	"errors"
	"testing"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

func mustCreateUser(t *testing.T, store *db.Store, username string, isAdmin bool) *db.User {
	t.Helper()
	u, err := store.CreateUser(context.Background(), username, "Display "+username, "hash-"+username, "apikeyhash-"+username, isAdmin)
	if err != nil {
		t.Fatalf("CreateUser(%q): %v", username, err)
	}
	return u
}

func TestCreateUserAndLookups(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)

	created := mustCreateUser(t, store, "aiden", true)
	if created.ID == 0 {
		t.Error("CreateUser returned ID 0, want the generated row id")
	}
	if !created.IsAdmin {
		t.Error("IsAdmin = false, want true")
	}
	if created.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero, want the creation timestamp")
	}
	if created.CreatedAt.Location().String() != "UTC" {
		t.Errorf("CreatedAt location = %s, want UTC", created.CreatedAt.Location())
	}

	byID, err := store.UserByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if byID.Username != "aiden" || byID.DisplayName != "Display aiden" {
		t.Errorf("UserByID = %+v, want username aiden", byID)
	}

	byName, err := store.UserByUsername(ctx, "aiden")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	if byName.ID != created.ID {
		t.Errorf("UserByUsername returned id %d, want %d", byName.ID, created.ID)
	}

	byKey, err := store.UserByAPIKeyHash(ctx, "apikeyhash-aiden")
	if err != nil {
		t.Fatalf("UserByAPIKeyHash: %v", err)
	}
	if byKey.ID != created.ID {
		t.Errorf("UserByAPIKeyHash returned id %d, want %d", byKey.ID, created.ID)
	}
}

func TestUserLookupsReturnErrNotFound(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)

	if _, err := store.UserByID(ctx, 404); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("UserByID: %v, want ErrNotFound", err)
	}
	if _, err := store.UserByUsername(ctx, "ghost"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("UserByUsername: %v, want ErrNotFound", err)
	}
	if _, err := store.UserByAPIKeyHash(ctx, "nope"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("UserByAPIKeyHash: %v, want ErrNotFound", err)
	}
}

func TestUserByAPIKeyHashRejectsEmpty(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	// A user with no API key stores NULL. An empty lookup must never match.
	if _, err := store.CreateUser(ctx, "nokey", "No Key", "hash", "", false); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := store.UserByAPIKeyHash(ctx, ""); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("UserByAPIKeyHash(\"\") = %v, want ErrNotFound", err)
	}
}

func TestCreateUserRejectsDuplicateUsername(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	mustCreateUser(t, store, "aiden", false)
	_, err := store.CreateUser(ctx, "AIDEN", "Shouty", "hash", "otherkey", false)
	if !errors.Is(err, db.ErrDuplicateUser) {
		t.Fatalf("CreateUser with a case-different duplicate returned %v, want ErrDuplicateUser", err)
	}
}

func TestNormalizeUsername(t *testing.T) {
	ok := map[string]string{
		"Aiden":   "aiden",
		"  bob  ": "bob",
		"a_b.c-d": "a_b.c-d",
		"user2":   "user2",
	}
	for in, want := range ok {
		got, err := db.NormalizeUsername(in)
		if err != nil {
			t.Errorf("NormalizeUsername(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeUsername(%q) = %q, want %q", in, got, want)
		}
	}
	bad := []string{"", "a", "_leading", "has space", "hasEmoji\U0001F600", "way-too-long-username-that-exceeds-the-limit"}
	for _, in := range bad {
		if _, err := db.NormalizeUsername(in); err == nil {
			t.Errorf("NormalizeUsername(%q) succeeded, want an error", in)
		}
	}
}

func TestListUsersIsOrderedByUsername(t *testing.T) {
	store := dbtest.New(t)
	mustCreateUser(t, store, "zoe", false)
	mustCreateUser(t, store, "aiden", true)
	mustCreateUser(t, store, "mia", false)

	users, err := store.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	want := []string{"aiden", "mia", "zoe"}
	if len(users) != len(want) {
		t.Fatalf("ListUsers returned %d users, want %d", len(users), len(want))
	}
	for i, name := range want {
		if users[i].Username != name {
			t.Errorf("users[%d] = %q, want %q", i, users[i].Username, name)
		}
	}
}

func TestSetUserPasswordInvalidatesSessions(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)

	if _, err := store.DB.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, created_at) VALUES ('tok', ?, '2099-01-01T00:00:00Z', '2026-07-23T00:00:00Z')`,
		user.ID); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if err := store.SetUserPassword(ctx, user.ID, "new-hash"); err != nil {
		t.Fatalf("SetUserPassword: %v", err)
	}

	updated, err := store.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if updated.PasswordHash != "new-hash" {
		t.Errorf("PasswordHash = %q, want \"new-hash\"", updated.PasswordHash)
	}

	var sessions int
	if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE user_id = ?`, user.ID).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 0 {
		t.Errorf("%d sessions survived a password change, want 0", sessions)
	}
}

func TestSetUserPasswordUnknownUser(t *testing.T) {
	if err := dbtest.New(t).SetUserPassword(context.Background(), 404, "x"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("SetUserPassword on a missing user returned %v, want ErrNotFound", err)
	}
}
