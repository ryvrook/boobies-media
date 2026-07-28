package db_test

import (
	"context"
	"testing"

	"boobies-media/internal/dbtest"
)

func TestMediaStorageBytesCountsDeduplicatedBlobsOnce(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user := mustCreateUser(t, store, "storage-owner", false)
	first := mustCreateItem(t, store, "shared", user.ID)
	second := mustCreateItem(t, store, "shared", user.ID)
	unique := mustCreateItem(t, store, "unique", user.ID)
	if _, err := store.DB.ExecContext(ctx, `UPDATE items SET size = 37 WHERE id IN (?, ?)`, first.ID, second.ID); err != nil {
		t.Fatalf("set shared sizes: %v", err)
	}
	if _, err := store.DB.ExecContext(ctx, `UPDATE items SET size = 11 WHERE id = ?`, unique.ID); err != nil {
		t.Fatalf("set unique size: %v", err)
	}

	total, err := store.MediaStorageBytes(ctx)
	if err != nil {
		t.Fatalf("MediaStorageBytes: %v", err)
	}
	if total != 48 {
		t.Errorf("total = %d, want 48 unique bytes", total)
	}
}
