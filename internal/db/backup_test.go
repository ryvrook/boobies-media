package db_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"boobies-media/internal/db"
)

func TestBackupToWritesAReadableCopy(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := store.BackupTo(ctx, dest); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if info.Size() == 0 {
		t.Error("backup file is empty")
	}
	// The copy opens as a valid database.
	copyStore, err := db.Open(dest)
	if err != nil {
		t.Fatalf("open backup copy: %v", err)
	}
	_ = copyStore.Close()
}

func TestBackupToFileIsNotWorldReadable(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := store.BackupTo(ctx, dest); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("backup file mode = %v, want no group or world permission bits", info.Mode().Perm())
	}
}

func TestBackupToCancelledContext(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := store.BackupTo(ctx, dest); err == nil {
		t.Fatal("BackupTo with a cancelled context: want error, got nil")
	}
}
