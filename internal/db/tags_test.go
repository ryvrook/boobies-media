package db_test

import (
	"context"
	"testing"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

func TestNormalizeTag(t *testing.T) {
	ok := map[string]string{
		"Cats":      "cats",
		"  DOGS  ":  "dogs",
		"re-action": "re-action",
		"vid_2026":  "vid_2026",
	}
	for in, want := range ok {
		got, err := db.NormalizeTag(in)
		if err != nil {
			t.Errorf("NormalizeTag(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeTag(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "   ", "has space", "a/b", "x;y"} {
		if _, err := db.NormalizeTag(bad); err == nil {
			t.Errorf("NormalizeTag(%q) succeeded, want an error", bad)
		}
	}
}

func TestAddAndRemoveItemTags(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	item := mustCreateItem(t, store, "hash-a", user.ID)

	for _, tag := range []string{"Cats", "funny", "cats"} { // deliberate duplicate
		if err := store.AddItemTag(ctx, item.ID, tag); err != nil {
			t.Fatalf("AddItemTag(%q): %v", tag, err)
		}
	}
	tags, err := store.ItemTags(ctx, item.ID)
	if err != nil {
		t.Fatalf("ItemTags: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("tags = %v, want 2 distinct entries (case folded)", tags)
	}
	if tags[0] != "cats" || tags[1] != "funny" {
		t.Errorf("tags = %v, want [cats funny] sorted and lowercased", tags)
	}

	if err := store.RemoveItemTag(ctx, item.ID, "CATS"); err != nil {
		t.Fatalf("RemoveItemTag: %v", err)
	}
	tags, _ = store.ItemTags(ctx, item.ID)
	if len(tags) != 1 || tags[0] != "funny" {
		t.Errorf("tags after removal = %v, want [funny]", tags)
	}
	// Removing a tag that is not attached is not an error.
	if err := store.RemoveItemTag(ctx, item.ID, "nonexistent"); err != nil {
		t.Errorf("RemoveItemTag for an unattached tag: %v", err)
	}
}

func TestAddItemTagRejectsAMissingItem(t *testing.T) {
	if err := dbtest.New(t).AddItemTag(context.Background(), "nosuchid", "cats"); err == nil {
		t.Fatal("AddItemTag on a missing item succeeded, want an error")
	}
}

func TestTagsForItemsAvoidsNPlusOne(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	a := mustCreateItem(t, store, "h1", user.ID)
	b := mustCreateItem(t, store, "h2", user.ID)
	c := mustCreateItem(t, store, "h3", user.ID)

	for _, tag := range []string{"cats", "funny"} {
		if err := store.AddItemTag(ctx, a.ID, tag); err != nil {
			t.Fatalf("AddItemTag: %v", err)
		}
	}
	if err := store.AddItemTag(ctx, b.ID, "dogs"); err != nil {
		t.Fatalf("AddItemTag: %v", err)
	}

	byItem, err := store.TagsForItems(ctx, []string{a.ID, b.ID, c.ID})
	if err != nil {
		t.Fatalf("TagsForItems: %v", err)
	}
	if len(byItem[a.ID]) != 2 || byItem[a.ID][0] != "cats" {
		t.Errorf("tags for a = %v, want [cats funny]", byItem[a.ID])
	}
	if len(byItem[b.ID]) != 1 || byItem[b.ID][0] != "dogs" {
		t.Errorf("tags for b = %v, want [dogs]", byItem[b.ID])
	}
	if len(byItem[c.ID]) != 0 {
		t.Errorf("tags for the untagged item = %v, want none", byItem[c.ID])
	}
}

func TestTagsForItemsHandlesAnEmptyList(t *testing.T) {
	got, err := dbtest.New(t).TagsForItems(context.Background(), nil)
	if err != nil {
		t.Fatalf("TagsForItems(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want none", len(got))
	}
}

func TestListTags(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	item := mustCreateItem(t, store, "h", user.ID)
	for _, tag := range []string{"zebra", "apple"} {
		if err := store.AddItemTag(ctx, item.ID, tag); err != nil {
			t.Fatalf("AddItemTag: %v", err)
		}
	}
	tags, err := store.ListTags(ctx)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags) != 2 || tags[0] != "apple" || tags[1] != "zebra" {
		t.Errorf("ListTags = %v, want [apple zebra]", tags)
	}
}
