package db_test

import (
	"context"
	"testing"
	"time"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

// seedItem inserts an item with an explicit created_at so ordering is exact.
func seedItem(t *testing.T, store *db.Store, uploaderID int64, title string, size int64, minutesAgo int) *db.Item {
	t.Helper()
	ctx := context.Background()
	item, err := store.CreateItem(ctx, db.NewItem{
		ContentHash: "hash-" + title,
		Title:       title,
		Ext:         "png",
		Mime:        "image/png",
		Size:        size,
		UploaderID:  uploaderID,
	})
	if err != nil {
		t.Fatalf("CreateItem(%s): %v", title, err)
	}
	when := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC).Add(-time.Duration(minutesAgo) * time.Minute)
	if _, err := store.DB.ExecContext(ctx, `UPDATE items SET created_at = ? WHERE id = ?`,
		when.Format(time.RFC3339), item.ID); err != nil {
		t.Fatalf("backdate %s: %v", title, err)
	}
	item.CreatedAt = when
	return item
}

func titlesOf(items []*db.Item) []string {
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i.Title)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestListItemsNewestFirstByDefault(t *testing.T) {
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	seedItem(t, store, user.ID, "oldest", 10, 30)
	seedItem(t, store, user.ID, "middle", 20, 20)
	seedItem(t, store, user.ID, "newest", 30, 10)

	items, _, err := store.ListItems(context.Background(), db.ItemQuery{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	want := []string{"newest", "middle", "oldest"}
	if got := titlesOf(items); !sameStrings(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestListItemsKeysetPaginationCoversEveryRowExactlyOnce(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	const total = 25
	for i := 0; i < total; i++ {
		seedItem(t, store, user.ID, string(rune('a'+i%26))+string(rune('0'+i/26))+"-item", int64(i), total-i)
	}

	seen := map[string]int{}
	cursor := ""
	pages := 0
	for {
		items, next, err := store.ListItems(ctx, db.ItemQuery{Limit: 7, Cursor: cursor})
		if err != nil {
			t.Fatalf("ListItems(page %d): %v", pages, err)
		}
		for _, item := range items {
			seen[item.ID]++
		}
		pages++
		if next == "" {
			break
		}
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		cursor = next
	}
	if len(seen) != total {
		t.Errorf("saw %d distinct items across %d pages, want %d", len(seen), pages, total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("item %s appeared %d times; keyset pages must not overlap", id, n)
		}
	}
}

func TestListItemsPaginationIsStableWhenNewItemsArrive(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	for i := 0; i < 6; i++ {
		seedItem(t, store, user.ID, "old-"+string(rune('a'+i)), 1, 100+i)
	}

	first, cursor, err := store.ListItems(ctx, db.ItemQuery{Limit: 3})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	// A friend uploads while the reader is mid-scroll. With OFFSET this would
	// shift the window and repeat a row.
	seedItem(t, store, user.ID, "brand-new", 1, 0)

	second, _, err := store.ListItems(ctx, db.ItemQuery{Limit: 3, Cursor: cursor})
	if err != nil {
		t.Fatalf("ListItems(page 2): %v", err)
	}
	for _, a := range first {
		for _, b := range second {
			if a.ID == b.ID {
				t.Fatalf("item %s appeared on both pages after a concurrent insert", a.ID)
			}
		}
	}
	for _, b := range second {
		if b.Title == "brand-new" {
			t.Fatal("the newly inserted item leaked into a later page")
		}
	}
}

func TestListItemsSortModes(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	alice := mustCreateUser(t, store, "alice", false)
	bob := mustCreateUser(t, store, "bob", false)

	seedItem(t, store, bob.ID, "banana", 300, 30)
	seedItem(t, store, alice.ID, "apple", 100, 20)
	seedItem(t, store, alice.ID, "cherry", 200, 10)

	cases := []struct {
		sort db.ItemSort
		want []string
	}{
		{db.SortNewest, []string{"cherry", "apple", "banana"}},
		{db.SortOldest, []string{"banana", "apple", "cherry"}},
		{db.SortTitle, []string{"apple", "banana", "cherry"}},
		{db.SortSize, []string{"banana", "cherry", "apple"}}, // largest first
	}
	for _, tc := range cases {
		items, _, err := store.ListItems(ctx, db.ItemQuery{Sort: tc.sort})
		if err != nil {
			t.Fatalf("ListItems(sort %v): %v", tc.sort, err)
		}
		if got := titlesOf(items); !sameStrings(got, tc.want) {
			t.Errorf("sort %v gave %v, want %v", tc.sort, got, tc.want)
		}
	}

	// Uploader sort groups by username; only the grouping is asserted.
	items, _, err := store.ListItems(ctx, db.ItemQuery{Sort: db.SortUploader})
	if err != nil {
		t.Fatalf("ListItems(sort uploader): %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	if items[0].UploaderID != alice.ID || items[1].UploaderID != alice.ID || items[2].UploaderID != bob.ID {
		t.Errorf("uploader sort did not group alice before bob: %v", titlesOf(items))
	}
}

func TestListItemsPaginatesUnderEverySort(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	for i := 0; i < 9; i++ {
		seedItem(t, store, user.ID, "title-"+string(rune('a'+i)), int64(i*10), 20-i)
	}
	for _, sort := range []db.ItemSort{db.SortNewest, db.SortOldest, db.SortTitle, db.SortSize, db.SortUploader} {
		seen := map[string]bool{}
		cursor := ""
		for page := 0; page < 10; page++ {
			items, next, err := store.ListItems(ctx, db.ItemQuery{Sort: sort, Limit: 4, Cursor: cursor})
			if err != nil {
				t.Fatalf("sort %v page %d: %v", sort, page, err)
			}
			for _, item := range items {
				if seen[item.ID] {
					t.Fatalf("sort %v repeated item %s across pages", sort, item.ID)
				}
				seen[item.ID] = true
			}
			if next == "" {
				break
			}
			cursor = next
		}
		if len(seen) != 9 {
			t.Errorf("sort %v paged over %d items, want 9", sort, len(seen))
		}
	}
}

func TestListItemsFilters(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	alice := mustCreateUser(t, store, "alice", false)
	bob := mustCreateUser(t, store, "bob", false)

	folder, err := store.CreateFolder(ctx, 0, "memes")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	inFolder := seedItem(t, store, alice.ID, "filed", 1, 30)
	if err := store.MoveItem(ctx, inFolder.ID, folder.ID); err != nil {
		t.Fatalf("MoveItem: %v", err)
	}
	seedItem(t, store, bob.ID, "unfiled", 1, 20)
	tagged := seedItem(t, store, alice.ID, "Cat Picture", 1, 10)
	if err := store.AddItemTag(ctx, tagged.ID, "cats"); err != nil {
		t.Fatalf("AddItemTag: %v", err)
	}

	root := int64(0)
	folderID := folder.ID

	t.Run("by folder", func(t *testing.T) {
		items, _, err := store.ListItems(ctx, db.ItemQuery{FolderID: &folderID})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if got := titlesOf(items); !sameStrings(got, []string{"filed"}) {
			t.Errorf("folder filter = %v, want [filed]", got)
		}
	})

	t.Run("root only", func(t *testing.T) {
		items, _, err := store.ListItems(ctx, db.ItemQuery{FolderID: &root})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		for _, item := range items {
			if item.ID == inFolder.ID {
				t.Error("the root filter returned a filed item")
			}
		}
		if len(items) != 2 {
			t.Errorf("root filter returned %d items, want 2", len(items))
		}
	})

	t.Run("by tag", func(t *testing.T) {
		items, _, err := store.ListItems(ctx, db.ItemQuery{Tag: "cats"})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if got := titlesOf(items); !sameStrings(got, []string{"Cat Picture"}) {
			t.Errorf("tag filter = %v, want [Cat Picture]", got)
		}
	})

	t.Run("by uploader", func(t *testing.T) {
		items, _, err := store.ListItems(ctx, db.ItemQuery{UploaderID: bob.ID})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if got := titlesOf(items); !sameStrings(got, []string{"unfiled"}) {
			t.Errorf("uploader filter = %v, want [unfiled]", got)
		}
	})

	t.Run("by title substring, case insensitive", func(t *testing.T) {
		items, _, err := store.ListItems(ctx, db.ItemQuery{Query: "cat pic"})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if got := titlesOf(items); !sameStrings(got, []string{"Cat Picture"}) {
			t.Errorf("query filter = %v, want [Cat Picture]", got)
		}
	})

	t.Run("query wildcards are escaped", func(t *testing.T) {
		// A bare % must match literally, not act as a wildcard.
		items, _, err := store.ListItems(ctx, db.ItemQuery{Query: "%"})
		if err != nil {
			t.Fatalf("ListItems: %v", err)
		}
		if len(items) != 0 {
			t.Errorf("query %%%% returned %d items, want 0 (LIKE metacharacters must be escaped)", len(items))
		}
	})
}

func TestListItemsHidesDeletedItems(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	seedItem(t, store, user.ID, "live", 1, 20)
	gone := seedItem(t, store, user.ID, "gone", 1, 10)

	if err := store.SoftDeleteItem(ctx, gone.ID, user); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}
	items, _, err := store.ListItems(ctx, db.ItemQuery{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if got := titlesOf(items); !sameStrings(got, []string{"live"}) {
		t.Errorf("ListItems = %v, want only [live]", got)
	}

	trash, err := store.ListDeletedItems(ctx, 50)
	if err != nil {
		t.Fatalf("ListDeletedItems: %v", err)
	}
	if len(trash) != 1 || trash[0].ID != gone.ID {
		t.Errorf("ListDeletedItems = %v, want just the deleted item", titlesOf(trash))
	}
}

func TestListItemsClampsTheLimit(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	for i := 0; i < 3; i++ {
		seedItem(t, store, user.ID, "i"+string(rune('a'+i)), 1, i)
	}
	for _, limit := range []int{0, -5, db.MaxItemLimit + 1000} {
		items, _, err := store.ListItems(ctx, db.ItemQuery{Limit: limit})
		if err != nil {
			t.Fatalf("ListItems(limit %d): %v", limit, err)
		}
		if len(items) != 3 {
			t.Errorf("limit %d returned %d items, want all 3", limit, len(items))
		}
	}
}

func TestListItemsRejectsAMalformedCursor(t *testing.T) {
	if _, _, err := dbtest.New(t).ListItems(context.Background(), db.ItemQuery{Cursor: "!!!not base64!!!"}); err == nil {
		t.Fatal("ListItems accepted a malformed cursor, want an error")
	}
}

func TestParseItemSort(t *testing.T) {
	want := map[string]db.ItemSort{
		"":         db.SortNewest,
		"newest":   db.SortNewest,
		"date":     db.SortNewest,
		"oldest":   db.SortOldest,
		"name":     db.SortTitle,
		"title":    db.SortTitle,
		"size":     db.SortSize,
		"uploader": db.SortUploader,
	}
	for in, expected := range want {
		got, err := db.ParseItemSort(in)
		if err != nil {
			t.Errorf("ParseItemSort(%q): %v", in, err)
			continue
		}
		if got != expected {
			t.Errorf("ParseItemSort(%q) = %v, want %v", in, got, expected)
		}
	}
	if _, err := db.ParseItemSort("; DROP TABLE items"); err == nil {
		t.Fatal("ParseItemSort accepted an unknown sort, want an error")
	}
}
