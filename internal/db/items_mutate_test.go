package db_test

import (
	"context"
	"errors"
	"testing"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

func TestSetItemTitle(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	item := mustCreateItem(t, store, "hash-a", user.ID)

	if err := store.SetItemTitle(ctx, item.ID, "  A Better Name  "); err != nil {
		t.Fatalf("SetItemTitle: %v", err)
	}
	got, err := store.ItemByID(ctx, item.ID)
	if err != nil {
		t.Fatalf("ItemByID: %v", err)
	}
	if got.Title != "A Better Name" {
		t.Errorf("Title = %q, want it trimmed to \"A Better Name\"", got.Title)
	}
	if err := store.SetItemTitle(ctx, "nosuchid", "x"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("SetItemTitle on a missing item = %v, want ErrNotFound", err)
	}
}

func TestMoveItemBetweenFoldersAndBackToRoot(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	item := mustCreateItem(t, store, "hash-a", user.ID)
	folder, err := store.CreateFolder(ctx, 0, "memes")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}

	if err := store.MoveItem(ctx, item.ID, folder.ID); err != nil {
		t.Fatalf("MoveItem: %v", err)
	}
	got, _ := store.ItemByID(ctx, item.ID)
	if got.FolderID != folder.ID {
		t.Errorf("FolderID = %d, want %d", got.FolderID, folder.ID)
	}

	if err := store.MoveItem(ctx, item.ID, 0); err != nil {
		t.Fatalf("MoveItem to root: %v", err)
	}
	got, _ = store.ItemByID(ctx, item.ID)
	if got.FolderID != 0 {
		t.Errorf("FolderID = %d after moving to root, want 0", got.FolderID)
	}
}

func TestMoveItemRejectsAMissingFolder(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	item := mustCreateItem(t, store, "hash-a", user.ID)

	if err := store.MoveItem(ctx, item.ID, 4040); err == nil {
		t.Fatal("MoveItem into a nonexistent folder succeeded, want an error")
	}
}

func TestCopyItemSharesBlobAndCopiesMetadataAndTags(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	alice := mustCreateUser(t, store, "alice", false)
	bob := mustCreateUser(t, store, "bob", false)
	source := mustCreateItem(t, store, "shared-hash", alice.ID)
	if err := store.SetItemProbe(ctx, source.ID, 1280, 720, 4.5); err != nil {
		t.Fatal(err)
	}
	if err := store.AddItemTag(ctx, source.ID, "favorite"); err != nil {
		t.Fatal(err)
	}
	folder, err := store.CreateFolder(ctx, 0, "Copies")
	if err != nil {
		t.Fatal(err)
	}

	copied, err := store.CopyItem(ctx, source.ID, folder.ID, bob.ID)
	if err != nil {
		t.Fatalf("CopyItem: %v", err)
	}
	if copied.ID == source.ID || copied.ContentHash != source.ContentHash {
		t.Errorf("copy id/hash = %q/%q, source = %q/%q", copied.ID, copied.ContentHash, source.ID, source.ContentHash)
	}
	if copied.FolderID != folder.ID || copied.UploaderID != bob.ID {
		t.Errorf("copy folder/uploader = %d/%d, want %d/%d", copied.FolderID, copied.UploaderID, folder.ID, bob.ID)
	}
	if copied.Width != 1280 || copied.Height != 720 || copied.Duration != 4.5 {
		t.Errorf("copy probe metadata = %dx%d/%v", copied.Width, copied.Height, copied.Duration)
	}
	tags, err := store.ItemTags(ctx, copied.ID)
	if err != nil || len(tags) != 1 || tags[0] != "favorite" {
		t.Errorf("copied tags = %v, %v", tags, err)
	}
}

func TestSetItemProbe(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	item := mustCreateItem(t, store, "hash-a", user.ID)

	if err := store.SetItemProbe(ctx, item.ID, 1920, 1080, 12.5); err != nil {
		t.Fatalf("SetItemProbe: %v", err)
	}
	got, _ := store.ItemByID(ctx, item.ID)
	if got.Width != 1920 || got.Height != 1080 {
		t.Errorf("dimensions = %dx%d, want 1920x1080", got.Width, got.Height)
	}
	if got.Duration != 12.5 {
		t.Errorf("Duration = %v, want 12.5", got.Duration)
	}
}

func TestSetItemShareRevokedHidesTheItemFromSharing(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	item := mustCreateItem(t, store, "hash-a", user.ID)

	if err := store.SetItemShareRevoked(ctx, item.ID, true); err != nil {
		t.Fatalf("SetItemShareRevoked: %v", err)
	}
	got, err := store.ItemByID(ctx, item.ID)
	if err != nil {
		t.Fatalf("ItemByID: %v", err)
	}
	if !got.ShareRevoked {
		t.Error("ShareRevoked = false after revoking")
	}
	if got.IsPubliclyServable() {
		t.Error("IsPubliclyServable() = true for a revoked item")
	}
	// Revoking must not delete: the item still browses normally for friends.
	if got.IsDeleted() {
		t.Error("revoking marked the item deleted")
	}
}

func TestSoftDeleteAuthorization(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	owner := mustCreateUser(t, store, "owner", false)
	other := mustCreateUser(t, store, "other", false)
	admin := mustCreateUser(t, store, "admin", true)

	t.Run("uploader may delete", func(t *testing.T) {
		item := mustCreateItem(t, store, "h1", owner.ID)
		if err := store.SoftDeleteItem(ctx, item.ID, owner); err != nil {
			t.Fatalf("SoftDeleteItem: %v", err)
		}
		if _, err := store.ItemByID(ctx, item.ID); !errors.Is(err, db.ErrNotFound) {
			t.Error("the item is still live after a soft delete")
		}
	})

	t.Run("admin may delete anyone's item", func(t *testing.T) {
		item := mustCreateItem(t, store, "h2", owner.ID)
		if err := store.SoftDeleteItem(ctx, item.ID, admin); err != nil {
			t.Fatalf("SoftDeleteItem: %v", err)
		}
	})

	t.Run("another friend may not", func(t *testing.T) {
		item := mustCreateItem(t, store, "h3", owner.ID)
		err := store.SoftDeleteItem(ctx, item.ID, other)
		if !errors.Is(err, db.ErrForbidden) {
			t.Fatalf("SoftDeleteItem by a non-owner = %v, want ErrForbidden", err)
		}
		if _, err := store.ItemByID(ctx, item.ID); err != nil {
			t.Error("the item was deleted despite the authorization failure")
		}
	})

	t.Run("nil actor is refused", func(t *testing.T) {
		item := mustCreateItem(t, store, "h4", owner.ID)
		if err := store.SoftDeleteItem(ctx, item.ID, nil); !errors.Is(err, db.ErrForbidden) {
			t.Fatalf("SoftDeleteItem(nil actor) = %v, want ErrForbidden", err)
		}
	})
}

func TestRestoreItem(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	item := mustCreateItem(t, store, "hash-a", user.ID)

	if err := store.SoftDeleteItem(ctx, item.ID, user); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}
	if err := store.RestoreItem(ctx, item.ID); err != nil {
		t.Fatalf("RestoreItem: %v", err)
	}
	got, err := store.ItemByID(ctx, item.ID)
	if err != nil {
		t.Fatalf("ItemByID after restore: %v", err)
	}
	if got.IsDeleted() {
		t.Error("the item is still marked deleted after a restore")
	}
}

func TestPurgeItemKeepsTheBlobWhileAnotherItemSharesIt(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	alice := mustCreateUser(t, store, "alice", false)
	bob := mustCreateUser(t, store, "bob", false)

	// The same meme, uploaded twice: two rows, one blob.
	a := mustCreateItem(t, store, "shared-hash", alice.ID)
	b := mustCreateItem(t, store, "shared-hash", bob.ID)

	unlink, err := store.PurgeItem(ctx, a.ID)
	if err != nil {
		t.Fatalf("PurgeItem: %v", err)
	}
	if unlink != "" {
		t.Fatalf("PurgeItem said to unlink %q while %s still references it", unlink, b.ID)
	}
	if _, err := store.ItemByIDIncludingDeleted(ctx, a.ID); !errors.Is(err, db.ErrNotFound) {
		t.Error("the purged row still exists")
	}
	if _, err := store.ItemByID(ctx, b.ID); err != nil {
		t.Errorf("the surviving item broke: %v", err)
	}

	// Purging the last referrer releases the blob.
	unlink, err = store.PurgeItem(ctx, b.ID)
	if err != nil {
		t.Fatalf("PurgeItem: %v", err)
	}
	if unlink != "shared-hash" {
		t.Errorf("PurgeItem returned %q, want the hash to unlink", unlink)
	}
}

func TestPurgeItemKeepsTheBlobForASoftDeletedSibling(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	alice := mustCreateUser(t, store, "alice", false)
	bob := mustCreateUser(t, store, "bob", false)

	// Same blob, two rows. The sibling is trashed but still restorable, so its
	// blob must not be unlinked out from under it.
	a := mustCreateItem(t, store, "shared-hash-2", alice.ID)
	b := mustCreateItem(t, store, "shared-hash-2", bob.ID)
	if err := store.SoftDeleteItem(ctx, b.ID, bob); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}

	unlink, err := store.PurgeItem(ctx, a.ID)
	if err != nil {
		t.Fatalf("PurgeItem: %v", err)
	}
	if unlink != "" {
		t.Fatalf("PurgeItem said to unlink %q while soft-deleted %s still references it", unlink, b.ID)
	}

	// b is still trashed but restorable; its row must be untouched.
	got, err := store.ItemByIDIncludingDeleted(ctx, b.ID)
	if err != nil {
		t.Fatalf("ItemByIDIncludingDeleted(%s): %v", b.ID, err)
	}
	if !got.IsDeleted() {
		t.Error("b unexpectedly came back live")
	}

	// Now purge the trashed sibling too: nothing references the blob anymore.
	unlink, err = store.PurgeItem(ctx, b.ID)
	if err != nil {
		t.Fatalf("PurgeItem: %v", err)
	}
	if unlink != "shared-hash-2" {
		t.Errorf("PurgeItem returned %q, want the hash to unlink now that both rows are gone", unlink)
	}
}

func TestPurgeItemUnknown(t *testing.T) {
	if _, err := dbtest.New(t).PurgeItem(context.Background(), "nosuchid"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("PurgeItem = %v, want ErrNotFound", err)
	}
}

func TestPurgeItemRemovesTagLinks(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "aiden", false)
	item := mustCreateItem(t, store, "hash-a", user.ID)
	if err := store.AddItemTag(ctx, item.ID, "cats"); err != nil {
		t.Fatalf("AddItemTag: %v", err)
	}
	if _, err := store.PurgeItem(ctx, item.ID); err != nil {
		t.Fatalf("PurgeItem: %v", err)
	}
	var links int
	if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM item_tags WHERE item_id = ?`, item.ID).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 0 {
		t.Errorf("%d tag links survived the purge, want 0 (ON DELETE CASCADE)", links)
	}
}
