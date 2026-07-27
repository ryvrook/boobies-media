package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

func TestSessionRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

	if err := store.CreateSession(ctx, "hash-a", user.ID, now.Add(30*24*time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := store.SessionUser(ctx, "hash-a", now)
	if err != nil {
		t.Fatalf("SessionUser: %v", err)
	}
	if got.ID != user.ID || got.Username != "aiden" {
		t.Errorf("SessionUser = %+v, want user aiden", got)
	}
}

func TestSessionUserRejectsExpired(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

	if err := store.CreateSession(ctx, "hash-a", user.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := store.SessionUser(ctx, "hash-a", now.Add(2*time.Hour)); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("SessionUser on an expired session returned %v, want ErrNotFound", err)
	}
	// Still valid one second before expiry.
	if _, err := store.SessionUser(ctx, "hash-a", now.Add(time.Hour-time.Second)); err != nil {
		t.Fatalf("SessionUser just before expiry: %v", err)
	}
}

func TestSessionUserUnknownToken(t *testing.T) {
	store := dbtest.New(t)
	if _, err := store.SessionUser(context.Background(), "nope", time.Now().UTC()); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("SessionUser with an unknown token returned %v, want ErrNotFound", err)
	}
}

func TestSessionUserRejectsEmptyToken(t *testing.T) {
	store := dbtest.New(t)
	if _, err := store.SessionUser(context.Background(), "", time.Now().UTC()); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("SessionUser(\"\") returned %v, want ErrNotFound", err)
	}
}

func TestDeleteSessionIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	now := time.Now().UTC()

	if err := store.CreateSession(ctx, "hash-a", user.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := store.DeleteSession(ctx, "hash-a"); err != nil {
			t.Fatalf("DeleteSession (call %d): %v", i+1, err)
		}
	}
	if _, err := store.SessionUser(ctx, "hash-a", now); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("session survived deletion: %v", err)
	}
}

func TestDeleteUserSessions(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	alice := mustCreateUser(t, store, "alice", false)
	bob := mustCreateUser(t, store, "bob", false)
	now := time.Now().UTC()

	for _, s := range []struct {
		hash string
		id   int64
	}{{"a1", alice.ID}, {"a2", alice.ID}, {"b1", bob.ID}} {
		if err := store.CreateSession(ctx, s.hash, s.id, now.Add(time.Hour)); err != nil {
			t.Fatalf("CreateSession(%s): %v", s.hash, err)
		}
	}
	if err := store.DeleteUserSessions(ctx, alice.ID); err != nil {
		t.Fatalf("DeleteUserSessions: %v", err)
	}
	if _, err := store.SessionUser(ctx, "a1", now); !errors.Is(err, db.ErrNotFound) {
		t.Error("alice's session a1 survived")
	}
	if _, err := store.SessionUser(ctx, "b1", now); err != nil {
		t.Errorf("bob's session was deleted too: %v", err)
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

	if err := store.CreateSession(ctx, "old", user.ID, now.Add(-time.Hour)); err != nil {
		t.Fatalf("CreateSession(old): %v", err)
	}
	if err := store.CreateSession(ctx, "fresh", user.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession(fresh): %v", err)
	}
	removed, err := store.DeleteExpiredSessions(ctx, now)
	if err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed %d sessions, want 1", removed)
	}
	if _, err := store.SessionUser(ctx, "fresh", now); err != nil {
		t.Errorf("the unexpired session was removed: %v", err)
	}
}

func TestCreateSessionRequiresRealUser(t *testing.T) {
	err := dbtest.New(t).CreateSession(context.Background(), "hash", 404, time.Now().UTC().Add(time.Hour))
	if err == nil {
		t.Fatal("CreateSession for a nonexistent user succeeded, want a foreign-key error")
	}
}

func TestDeletingUserCascadesSessions(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	now := time.Now().UTC()
	if err := store.CreateSession(ctx, "hash-a", user.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := store.DB.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, user.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var count int
	if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Errorf("%d sessions survived user deletion, want 0 (ON DELETE CASCADE)", count)
	}
}
