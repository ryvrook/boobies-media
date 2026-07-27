package db_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

func TestDeleteUserRefusesWhileUserOwnsItems(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user, err := store.CreateUser(ctx, "aiden", "Aiden", "h", "", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := store.CreateItem(ctx, db.NewItem{ContentHash: "h1", Title: "t", Ext: "png", Mime: "image/png", Size: 1, UploaderID: user.ID}); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if err := store.DeleteUser(ctx, user.ID); !errors.Is(err, db.ErrUserHasItems) {
		t.Fatalf("DeleteUser with items = %v, want ErrUserHasItems", err)
	}
}

func TestDeleteUserRemovesAnEmptyUser(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user, _ := store.CreateUser(ctx, "spare", "Spare", "h", "", false)
	if err := store.DeleteUser(ctx, user.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := store.UserByID(ctx, user.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("UserByID after delete = %v, want ErrNotFound", err)
	}
	if err := store.DeleteUser(ctx, 999); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("DeleteUser missing = %v, want ErrNotFound", err)
	}
}

func TestSetUserAdminToggles(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user, _ := store.CreateUser(ctx, "aiden", "Aiden", "h", "", false)
	if err := store.SetUserAdmin(ctx, user.ID, true); err != nil {
		t.Fatalf("SetUserAdmin: %v", err)
	}
	got, _ := store.UserByID(ctx, user.ID)
	if !got.IsAdmin {
		t.Error("SetUserAdmin(true) did not stick")
	}
	if err := store.SetUserAdmin(ctx, 999, true); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("SetUserAdmin missing = %v, want ErrNotFound", err)
	}
}

// TestDeleteUserNeverReturnsAnAmbiguousErrorUnderConcurrency guards against the
// TOCTOU window between counting a user's items and deleting the row: with
// SetMaxOpenConns(1) the pool has exactly one physical connection, so
// DeleteUser must run its count-then-delete inside a single transaction to
// hold that connection for the whole check. If it instead ran two separate
// top-level calls, a CreateItem could land in between them and the final
// DELETE would hit the items.uploader_id foreign key and fail with a raw,
// unsentineled driver error instead of ErrUserHasItems, which is exactly the
// ambiguity the admin HTTP layer (a later task) cannot tolerate: it needs to
// tell "still owns items" apart from "no such user" apart from "unexpected
// failure". This test hammers CreateItem and DeleteUser concurrently on the
// same user and asserts every DeleteUser result is one of the two documented
// sentinels or nil, never anything else.
func TestDeleteUserNeverReturnsAnAmbiguousErrorUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user, err := store.CreateUser(ctx, "racer", "Racer", "h", "", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	const rounds = 200
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan error, rounds)

	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			_, _ = store.CreateItem(ctx, db.NewItem{
				ContentHash: fmt.Sprintf("race-%d", i),
				Title:       "t",
				Ext:         "png",
				Mime:        "image/png",
				Size:        1,
				UploaderID:  user.ID,
			})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			results <- store.DeleteUser(ctx, user.ID)
		}
	}()
	wg.Wait()
	close(results)

	for err := range results {
		if err != nil && !errors.Is(err, db.ErrUserHasItems) && !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("DeleteUser under concurrent item creation returned an ambiguous error, want nil, ErrUserHasItems or ErrNotFound: %v", err)
		}
	}
}
