package db_test

import (
	"context"
	"errors"
	"testing"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

func TestRandomItemExcludesRevokedAndDeletedItems(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)

	live := mustCreateItem(t, store, "hash-live", user.ID)
	revoked := mustCreateItem(t, store, "hash-revoked", user.ID)
	if err := store.SetItemShareRevoked(ctx, revoked.ID, true); err != nil {
		t.Fatalf("SetItemShareRevoked: %v", err)
	}
	deleted := mustCreateItem(t, store, "hash-deleted", user.ID)
	if err := store.SoftDeleteItem(ctx, deleted.ID, user); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}

	for i := 0; i < 20; i++ {
		got, err := store.RandomItem(ctx, "")
		if err != nil {
			t.Fatalf("RandomItem: %v", err)
		}
		if got.ID != live.ID {
			t.Fatalf("RandomItem returned %s, want the only servable item %s", got.ID, live.ID)
		}
	}
}

func TestRandomItemExcludesRevokedAndDeletedItemsWithTagFilter(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)

	live := mustCreateItem(t, store, "hash-live", user.ID)
	if err := store.AddItemTag(ctx, live.ID, "cats"); err != nil {
		t.Fatalf("AddItemTag(live): %v", err)
	}
	revoked := mustCreateItem(t, store, "hash-revoked", user.ID)
	if err := store.AddItemTag(ctx, revoked.ID, "cats"); err != nil {
		t.Fatalf("AddItemTag(revoked): %v", err)
	}
	if err := store.SetItemShareRevoked(ctx, revoked.ID, true); err != nil {
		t.Fatalf("SetItemShareRevoked: %v", err)
	}
	deleted := mustCreateItem(t, store, "hash-deleted", user.ID)
	if err := store.AddItemTag(ctx, deleted.ID, "cats"); err != nil {
		t.Fatalf("AddItemTag(deleted): %v", err)
	}
	if err := store.SoftDeleteItem(ctx, deleted.ID, user); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}

	for i := 0; i < 20; i++ {
		got, err := store.RandomItem(ctx, "cats")
		if err != nil {
			t.Fatalf("RandomItem(cats): %v", err)
		}
		if got.ID != live.ID {
			t.Fatalf("RandomItem(cats) returned %s, want the only servable item %s", got.ID, live.ID)
		}
	}
}

func TestRandomItemFiltersByTag(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)

	tagged := mustCreateItem(t, store, "hash-tagged", user.ID)
	if err := store.AddItemTag(ctx, tagged.ID, "cats"); err != nil {
		t.Fatalf("AddItemTag(tagged, cats): %v", err)
	}
	other := mustCreateItem(t, store, "hash-other", user.ID)
	if err := store.AddItemTag(ctx, other.ID, "dogs"); err != nil {
		t.Fatalf("AddItemTag(other, dogs): %v", err)
	}

	got, err := store.RandomItem(ctx, "cats")
	if err != nil {
		t.Fatalf("RandomItem(cats): %v", err)
	}
	if got.ID != tagged.ID {
		t.Errorf("RandomItem(cats) = %s, want %s", got.ID, tagged.ID)
	}

	tags, err := store.ItemTags(ctx, got.ID)
	if err != nil {
		t.Fatalf("ItemTags: %v", err)
	}
	found := false
	for _, tag := range tags {
		if tag == "cats" {
			found = true
		}
	}
	if !found {
		t.Errorf("returned item's tags = %v, want it to include cats", tags)
	}
}

func TestRandomItemNormalizesTheTagQuery(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)

	tagged := mustCreateItem(t, store, "hash-tagged", user.ID)
	if err := store.AddItemTag(ctx, tagged.ID, "Cats"); err != nil {
		t.Fatalf("AddItemTag: %v", err)
	}

	for _, in := range []string{"cats", "Cats", "  cats  ", "CATS"} {
		got, err := store.RandomItem(ctx, in)
		if err != nil {
			t.Fatalf("RandomItem(%q): %v", in, err)
		}
		if got.ID != tagged.ID {
			t.Errorf("RandomItem(%q) = %s, want %s", in, got.ID, tagged.ID)
		}
	}
}

func TestRandomItemReturnsErrNotFoundWhenEmpty(t *testing.T) {
	if _, err := dbtest.New(t).RandomItem(context.Background(), ""); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("RandomItem on an empty library = %v, want ErrNotFound", err)
	}
}

func TestRandomItemReturnsErrNotFoundWhenTagMatchesNothing(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	mustCreateItem(t, store, "hash-a", user.ID)

	if _, err := store.RandomItem(ctx, "nope"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("RandomItem(nope) = %v, want ErrNotFound", err)
	}
}

func TestRandomItemRandomnessIsReal(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)

	const n = 8
	want := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		item := mustCreateItem(t, store, "hash-"+string(rune('a'+i)), user.ID)
		want[item.ID] = true
	}

	seen := make(map[string]bool, n)
	const iterations = 200
	for i := 0; i < iterations; i++ {
		got, err := store.RandomItem(ctx, "")
		if err != nil {
			t.Fatalf("RandomItem: %v", err)
		}
		if !want[got.ID] {
			t.Fatalf("RandomItem returned unseeded id %s", got.ID)
		}
		seen[got.ID] = true
	}
	if len(seen) < 2 {
		t.Errorf("RandomItem returned only %d distinct id(s) across %d calls against %d items, want more than one",
			len(seen), iterations, n)
	}
}

// TestRandomItemReturnsErrNotFoundWhenOnlyItemIsRevoked pins the fix for the
// single-row-race bug: RandomItem must never hand back a revoked item, even
// when it is the only row in the table and would otherwise be the trivial,
// only possible answer.
func TestRandomItemReturnsErrNotFoundWhenOnlyItemIsRevoked(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)

	revoked := mustCreateItem(t, store, "hash-revoked", user.ID)
	if err := store.SetItemShareRevoked(ctx, revoked.ID, true); err != nil {
		t.Fatalf("SetItemShareRevoked: %v", err)
	}

	if _, err := store.RandomItem(ctx, ""); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("RandomItem with only a revoked item present = %v, want ErrNotFound", err)
	}
}

// TestRandomItemReturnsErrNotFoundWhenOnlyTaggedItemIsRevoked is the tag
// filtered counterpart: the join path must apply the same exclusion.
func TestRandomItemReturnsErrNotFoundWhenOnlyTaggedItemIsRevoked(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)

	revoked := mustCreateItem(t, store, "hash-revoked", user.ID)
	if err := store.AddItemTag(ctx, revoked.ID, "cats"); err != nil {
		t.Fatalf("AddItemTag: %v", err)
	}
	if err := store.SetItemShareRevoked(ctx, revoked.ID, true); err != nil {
		t.Fatalf("SetItemShareRevoked: %v", err)
	}

	if _, err := store.RandomItem(ctx, "cats"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("RandomItem(cats) with only a revoked tagged item present = %v, want ErrNotFound", err)
	}
}

// TestRandomItemAlwaysReturnsAPubliclyServableItem seeds a mix weighted
// toward unservable items (more revoked and deleted rows than live ones) and
// asserts the IsPubliclyServable predicate directly on every draw, rather
// than only checking the returned id equals a single known-live item. This
// exercises the exclusion across a pool where a bug could plausibly pick a
// revoked or deleted row and still look "reasonable" by id alone.
func TestRandomItemAlwaysReturnsAPubliclyServableItem(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)

	mustCreateItem(t, store, "hash-live-1", user.ID)
	mustCreateItem(t, store, "hash-live-2", user.ID)
	for i, hash := range []string{"hash-revoked-1", "hash-revoked-2", "hash-revoked-3"} {
		item := mustCreateItem(t, store, hash, user.ID)
		if err := store.SetItemShareRevoked(ctx, item.ID, true); err != nil {
			t.Fatalf("SetItemShareRevoked[%d]: %v", i, err)
		}
	}
	for i, hash := range []string{"hash-deleted-1", "hash-deleted-2", "hash-deleted-3"} {
		item := mustCreateItem(t, store, hash, user.ID)
		if err := store.SoftDeleteItem(ctx, item.ID, user); err != nil {
			t.Fatalf("SoftDeleteItem[%d]: %v", i, err)
		}
	}

	for i := 0; i < 40; i++ {
		got, err := store.RandomItem(ctx, "")
		if err != nil {
			t.Fatalf("RandomItem: %v", err)
		}
		if !got.IsPubliclyServable() {
			t.Fatalf("RandomItem returned %s (revoked=%v deleted=%v), want only publicly servable items",
				got.ID, got.ShareRevoked, got.IsDeleted())
		}
	}
}

// TestRandomItemWithTagFilterAlwaysReturnsAPubliclyServableItem is the tag
// filtered counterpart of the predicate check above.
func TestRandomItemWithTagFilterAlwaysReturnsAPubliclyServableItem(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)

	live := mustCreateItem(t, store, "hash-live-1", user.ID)
	if err := store.AddItemTag(ctx, live.ID, "cats"); err != nil {
		t.Fatalf("AddItemTag(live): %v", err)
	}
	for i, hash := range []string{"hash-revoked-1", "hash-revoked-2"} {
		item := mustCreateItem(t, store, hash, user.ID)
		if err := store.AddItemTag(ctx, item.ID, "cats"); err != nil {
			t.Fatalf("AddItemTag(revoked[%d]): %v", i, err)
		}
		if err := store.SetItemShareRevoked(ctx, item.ID, true); err != nil {
			t.Fatalf("SetItemShareRevoked[%d]: %v", i, err)
		}
	}
	for i, hash := range []string{"hash-deleted-1", "hash-deleted-2"} {
		item := mustCreateItem(t, store, hash, user.ID)
		if err := store.AddItemTag(ctx, item.ID, "cats"); err != nil {
			t.Fatalf("AddItemTag(deleted[%d]): %v", i, err)
		}
		if err := store.SoftDeleteItem(ctx, item.ID, user); err != nil {
			t.Fatalf("SoftDeleteItem[%d]: %v", i, err)
		}
	}

	for i := 0; i < 40; i++ {
		got, err := store.RandomItem(ctx, "cats")
		if err != nil {
			t.Fatalf("RandomItem(cats): %v", err)
		}
		if !got.IsPubliclyServable() {
			t.Fatalf("RandomItem(cats) returned %s (revoked=%v deleted=%v), want only publicly servable items",
				got.ID, got.ShareRevoked, got.IsDeleted())
		}
		if got.ID != live.ID {
			t.Fatalf("RandomItem(cats) = %s, want the only servable tagged item %s", got.ID, live.ID)
		}
	}
}
