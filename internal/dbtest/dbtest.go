// Package dbtest provides a migrated, throwaway Store for tests in any package.
package dbtest

import (
	"path/filepath"
	"testing"

	"boobies-media/internal/db"
)

// New returns a Store backed by a fresh SQLite file in the test's temp
// directory. The store is closed automatically when the test finishes.
func New(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatalf("dbtest: open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("dbtest: close store: %v", err)
		}
	})
	return store
}
