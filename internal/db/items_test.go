package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

func newTestItem(hash string, uploaderID int64) db.NewItem {
	return db.NewItem{
		ContentHash: hash,
		Title:       "test " + hash,
		Ext:         "png",
		Mime:        "image/png",
		Size:        1234,
		UploaderID:  uploaderID,
	}
}

func mustCreateItem(t *testing.T, store *db.Store, hash string, uploaderID int64) *db.Item {
	t.Helper()
	item, err := store.CreateItem(context.Background(), newTestItem(hash, uploaderID))
	if err != nil {
		t.Fatalf("CreateItem(%s): %v", hash, err)
	}
	return item
}

func TestCreateItemGeneratesABase58ID(t *testing.T) {
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	item := mustCreateItem(t, store, "hash-a", user.ID)

	if len(item.ID) != 8 {
		t.Errorf("id %q has length %d, want 8", item.ID, len(item.ID))
	}
	if item.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	if item.CreatedAt.Location().String() != "UTC" {
		t.Errorf("CreatedAt location = %s, want UTC", item.CreatedAt.Location())
	}
	if item.ShareRevoked {
		t.Error("ShareRevoked = true on a new item")
	}
	if !item.DeletedAt.IsZero() {
		t.Error("DeletedAt is set on a new item")
	}
	if item.Width != 0 || item.Height != 0 || item.Duration != 0 {
		t.Error("dimensions are set before the probe job has run")
	}
}

func TestCreateItemIDsAreUnique(t *testing.T) {
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		item := mustCreateItem(t, store, "hash", user.ID)
		if seen[item.ID] {
			t.Fatalf("duplicate item id %q", item.ID)
		}
		seen[item.ID] = true
	}
}

func TestCreateItemRoundTripsEveryField(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)

	in := db.NewItem{
		ContentHash: "deadbeef",
		Title:       "A clip",
		Ext:         "mp4",
		Mime:        "video/mp4",
		Size:        98765,
		UploaderID:  user.ID,
		SourceURL:   "https://example.com/video",
	}
	created, err := store.CreateItem(ctx, in)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	got, err := store.ItemByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("ItemByID: %v", err)
	}
	if got.ContentHash != in.ContentHash || got.Title != in.Title || got.Ext != in.Ext ||
		got.Mime != in.Mime || got.Size != in.Size || got.SourceURL != in.SourceURL {
		t.Errorf("round trip lost data: %+v", got)
	}
	if got.FolderID != 0 || got.JobID != 0 {
		t.Errorf("FolderID = %d, JobID = %d, want 0 (NULL) for an unfiled direct upload", got.FolderID, got.JobID)
	}
}

func TestCreateItemRequiresARealUploader(t *testing.T) {
	if _, err := dbtest.New(t).CreateItem(context.Background(), newTestItem("h", 999)); err == nil {
		t.Fatal("CreateItem accepted a dangling uploader_id, want a foreign-key error")
	}
}

func TestCreateItemValidatesItsInput(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)

	bad := map[string]db.NewItem{
		"no content hash": {Mime: "image/png", UploaderID: user.ID},
		"no mime":         {ContentHash: "h", UploaderID: user.ID},
		"no uploader":     {ContentHash: "h", Mime: "image/png"},
	}
	for name, in := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := store.CreateItem(ctx, in); err == nil {
				t.Fatal("CreateItem accepted invalid input, want an error")
			}
		})
	}
}

func TestItemByIDHidesSoftDeletedRows(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	item := mustCreateItem(t, store, "hash-a", user.ID)

	if _, err := store.DB.ExecContext(ctx, `UPDATE items SET deleted_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), item.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	if _, err := store.ItemByID(ctx, item.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("ItemByID on a deleted item = %v, want ErrNotFound", err)
	}
	got, err := store.ItemByIDIncludingDeleted(ctx, item.ID)
	if err != nil {
		t.Fatalf("ItemByIDIncludingDeleted: %v", err)
	}
	if got.DeletedAt.IsZero() {
		t.Error("DeletedAt is zero on a soft-deleted item")
	}
}

func TestItemByIDUnknown(t *testing.T) {
	if _, err := dbtest.New(t).ItemByID(context.Background(), "nosuchid"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("ItemByID = %v, want ErrNotFound", err)
	}
}

func TestItemsByJobID(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)

	res, err := store.DB.ExecContext(ctx,
		`INSERT INTO jobs (type, next_attempt_at, created_at) VALUES ('ingest_url','2026-07-23T00:00:00Z','2026-07-23T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}
	jobID, _ := res.LastInsertId()

	// A Twitter gallery yields several items from one job.
	for i := 0; i < 3; i++ {
		in := newTestItem("hash", user.ID)
		in.JobID = jobID
		if _, err := store.CreateItem(ctx, in); err != nil {
			t.Fatalf("CreateItem: %v", err)
		}
	}
	mustCreateItem(t, store, "unrelated", user.ID)

	items, err := store.ItemsByJobID(ctx, jobID)
	if err != nil {
		t.Fatalf("ItemsByJobID: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("ItemsByJobID returned %d items, want 3", len(items))
	}
	for _, item := range items {
		if item.JobID != jobID {
			t.Errorf("item %s has JobID %d, want %d", item.ID, item.JobID, jobID)
		}
	}
}

func TestContentHashRefCount(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	alice := mustCreateUser(t, store, "alice", false)
	bob := mustCreateUser(t, store, "bob", false)

	// Two friends upload the same meme: two rows, one blob.
	a := mustCreateItem(t, store, "shared", alice.ID)
	b := mustCreateItem(t, store, "shared", bob.ID)
	solo := mustCreateItem(t, store, "unique", alice.ID)

	count, err := store.ContentHashRefCount(ctx, "shared", a.ID)
	if err != nil {
		t.Fatalf("ContentHashRefCount: %v", err)
	}
	if count != 1 {
		t.Errorf("refcount excluding %s = %d, want 1 (%s still references it)", a.ID, count, b.ID)
	}

	count, err = store.ContentHashRefCount(ctx, "unique", solo.ID)
	if err != nil {
		t.Fatalf("ContentHashRefCount: %v", err)
	}
	if count != 0 {
		t.Errorf("refcount excluding the only referrer = %d, want 0", count)
	}
}

func TestContentHashRefCountCountsSoftDeletedRows(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	alice := mustCreateUser(t, store, "alice", false)

	a := mustCreateItem(t, store, "shared", alice.ID)
	b := mustCreateItem(t, store, "shared", alice.ID)
	if _, err := store.DB.ExecContext(ctx, `UPDATE items SET deleted_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), b.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	// A soft-deleted item is restorable, so its blob must survive.
	count, err := store.ContentHashRefCount(ctx, "shared", a.ID)
	if err != nil {
		t.Fatalf("ContentHashRefCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("refcount = %d, want 1: unlinking the blob would break restoring %s", count, b.ID)
	}
}
