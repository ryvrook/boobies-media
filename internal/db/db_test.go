package db

import (
	"database/sql"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestOpenAppliesPragmas(t *testing.T) {
	store := openTemp(t)
	want := map[string]string{
		"journal_mode": "wal",
		"synchronous":  "1", // NORMAL
		"foreign_keys": "1",
		"busy_timeout": "5000",
	}
	for pragma, expected := range want {
		var got string
		if err := store.DB.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", pragma, err)
		}
		if got != expected {
			t.Errorf("PRAGMA %s = %q, want %q", pragma, got, expected)
		}
	}
}

func TestOpenLimitsToOneConnection(t *testing.T) {
	store := openTemp(t)
	if got := store.DB.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}
}

func TestOpenFailsOnUnopenablePath(t *testing.T) {
	// A directory that does not exist cannot hold a database file.
	if _, err := Open(filepath.Join(t.TempDir(), "no-such-dir", "media.db")); err == nil {
		t.Fatal("Open succeeded for a path in a missing directory, want an error")
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	store := openTemp(t)
	// Open already migrated once; running again must be a no-op.
	for i := 0; i < 2; i++ {
		if err := Migrate(store.DB); err != nil {
			t.Fatalf("Migrate (extra call %d): %v", i+1, err)
		}
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	var applied int
	if err := store.DB.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Errorf("schema_migrations has %d rows, want exactly %d (one per embedded migration) after repeated Migrate calls", applied, len(migrations))
	}
}

func TestSchemaCreatesEveryTable(t *testing.T) {
	store := openTemp(t)
	rows, err := store.DB.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := []string{
		"folders", "item_tags", "items", "jobs",
		"schema_migrations", "sessions", "settings", "tags", "uploads", "users",
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("tables = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tables = %v, want %v", got, want)
		}
	}
}

func TestSchemaCreatesEveryIndex(t *testing.T) {
	store := openTemp(t)
	rows, err := store.DB.Query(`SELECT name FROM sqlite_master WHERE type='index' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("query indexes: %v", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	for _, name := range []string{
		"items_content_hash", "items_folder_created", "items_uploader", "items_created",
		"item_tags_tag", "jobs_poll", "sessions_user", "folders_root_name",
	} {
		if !have[name] {
			t.Errorf("missing index %s", name)
		}
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	store := openTemp(t)
	_, err := store.DB.Exec(
		`INSERT INTO items (id, content_hash, mime, uploader_id, created_at) VALUES ('abcdefgh','h','image/png',999,'2026-07-23T00:00:00Z')`)
	if err == nil {
		t.Fatal("insert with a dangling uploader_id succeeded, want a foreign-key violation")
	}
}

func TestRootFolderNamesAreUnique(t *testing.T) {
	store := openTemp(t)
	insert := func() error {
		_, err := store.DB.Exec(`INSERT INTO folders (parent_id, name, created_at) VALUES (NULL, 'memes', '2026-07-23T00:00:00Z')`)
		return err
	}
	if err := insert(); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// SQLite treats NULLs as distinct in UNIQUE(parent_id, name), so the
	// partial index folders_root_name is what actually enforces this.
	if err := insert(); err == nil {
		t.Fatal("duplicate root folder name was accepted, want a uniqueness violation")
	}
}

func TestJobStatusAndTypeAreConstrained(t *testing.T) {
	store := openTemp(t)
	if _, err := store.DB.Exec(
		`INSERT INTO jobs (type, next_attempt_at, created_at) VALUES ('nonsense','2026-07-23T00:00:00Z','2026-07-23T00:00:00Z')`,
	); err == nil {
		t.Fatal("job with an unknown type was accepted, want a CHECK violation")
	}
	if _, err := store.DB.Exec(
		`INSERT INTO jobs (type, status, next_attempt_at, created_at) VALUES ('probe','nonsense','2026-07-23T00:00:00Z','2026-07-23T00:00:00Z')`,
	); err == nil {
		t.Fatal("job with an unknown status was accepted, want a CHECK violation")
	}
}

func TestErrNotFoundWrapsSQLNoRows(t *testing.T) {
	// Later tasks translate sql.ErrNoRows into ErrNotFound; assert the
	// sentinel exists and is distinct from the driver error.
	if ErrNotFound == nil {
		t.Fatal("ErrNotFound is nil")
	}
	if ErrNotFound == sql.ErrNoRows {
		t.Fatal("ErrNotFound must be its own sentinel, not sql.ErrNoRows")
	}
}

func TestMigrationTimestampIsRFC3339UTC(t *testing.T) {
	store := openTemp(t)
	// After migration runs (which happens in openTemp via Open), read the timestamp
	// from schema_migrations and verify it parses as RFC3339.
	var appliedAt string
	if err := store.DB.QueryRow(
		`SELECT applied_at FROM schema_migrations LIMIT 1`,
	).Scan(&appliedAt); err != nil {
		t.Fatalf("query applied_at: %v", err)
	}

	// Attempt to parse as RFC3339; this must succeed.
	_, err := time.Parse(time.RFC3339, appliedAt)
	if err != nil {
		t.Errorf("applied_at %q does not parse as RFC3339: %v", appliedAt, err)
	}
}
