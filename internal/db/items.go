package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"boobies-media/internal/auth"
)

// Item is one piece of media. The ID doubles as the public share slug.
type Item struct {
	ID           string
	ContentHash  string
	Title        string
	Ext          string
	Mime         string
	Size         int64
	Width        int64   // 0 until the probe job runs
	Height       int64   // 0 until the probe job runs
	Duration     float64 // 0 for stills
	UploaderID   int64
	FolderID     int64 // 0 means the root (SQL NULL)
	SourceURL    string
	JobID        int64 // 0 means a direct upload (SQL NULL)
	ShareRevoked bool
	DeletedAt    time.Time // zero means live
	CreatedAt    time.Time
}

// IsDeleted reports whether the item is in the trash.
func (i *Item) IsDeleted() bool { return !i.DeletedAt.IsZero() }

// IsPubliclyServable reports whether the public share routes may serve it.
func (i *Item) IsPubliclyServable() bool { return !i.IsDeleted() && !i.ShareRevoked }

// NewItem is the payload for CreateItem.
type NewItem struct {
	ContentHash string
	Title       string
	Ext         string
	Mime        string
	Size        int64
	UploaderID  int64
	FolderID    int64
	SourceURL   string
	JobID       int64
}

const itemColumns = `id, content_hash, title, ext, mime, size,
	COALESCE(width, 0), COALESCE(height, 0), COALESCE(duration, 0),
	uploader_id, COALESCE(folder_id, 0), COALESCE(source_url, ''), COALESCE(job_id, 0),
	share_revoked, COALESCE(deleted_at, ''), created_at`

func scanItem(row interface{ Scan(...any) error }) (*Item, error) {
	var (
		item      Item
		deletedAt string
		createdAt string
	)
	err := row.Scan(&item.ID, &item.ContentHash, &item.Title, &item.Ext, &item.Mime, &item.Size,
		&item.Width, &item.Height, &item.Duration,
		&item.UploaderID, &item.FolderID, &item.SourceURL, &item.JobID,
		&item.ShareRevoked, &deletedAt, &createdAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("db: scan item: %w", err)
	}
	return finishItem(&item, deletedAt, createdAt)
}

// finishItem parses the timestamp columns that both scan paths share.
func finishItem(item *Item, deletedAt, createdAt string) (*Item, error) {
	parsed, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("db: parse item created_at %q: %w", createdAt, err)
	}
	item.CreatedAt = parsed.UTC()
	if deletedAt != "" {
		parsed, err = time.Parse(time.RFC3339, deletedAt)
		if err != nil {
			return nil, fmt.Errorf("db: parse item deleted_at %q: %w", deletedAt, err)
		}
		item.DeletedAt = parsed.UTC()
	}
	return item, nil
}

// nullableID turns 0 into a SQL NULL for optional foreign keys.
func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// createItemAttempts bounds the base58 collision retry loop. With ~2^47 of
// space a single retry is already unreachable in practice; this only exists so
// a pathological RNG failure surfaces as an error rather than a hang.
const createItemAttempts = 5

// CreateItem inserts a new item, generating its public base58 id. On the
// (essentially impossible) id collision it retries with a fresh id.
func (s *Store) CreateItem(ctx context.Context, in NewItem) (*Item, error) {
	if in.ContentHash == "" {
		return nil, fmt.Errorf("db: item content hash must not be empty")
	}
	if in.Mime == "" {
		return nil, fmt.Errorf("db: item mime must not be empty")
	}
	if in.UploaderID == 0 {
		return nil, fmt.Errorf("db: item uploader must be set")
	}
	createdAt := time.Now().UTC().Format(time.RFC3339)

	for attempt := 0; attempt < createItemAttempts; attempt++ {
		id, err := auth.NewItemID()
		if err != nil {
			return nil, fmt.Errorf("db: generate item id: %w", err)
		}
		_, err = s.DB.ExecContext(ctx,
			`INSERT INTO items (id, content_hash, title, ext, mime, size,
				uploader_id, folder_id, source_url, job_id, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.ContentHash, in.Title, in.Ext, in.Mime, in.Size,
			in.UploaderID, nullableID(in.FolderID), in.SourceURL, nullableID(in.JobID), createdAt)
		if err == nil {
			return s.ItemByID(ctx, id)
		}
		if strings.Contains(err.Error(), "UNIQUE constraint failed: items.id") {
			continue // vanishingly rare; draw another id
		}
		return nil, fmt.Errorf("db: create item: %w", err)
	}
	return nil, fmt.Errorf("db: could not allocate a unique item id after %d attempts", createItemAttempts)
}

// ItemByID returns a live item. Soft-deleted rows are reported as ErrNotFound
// so a handler cannot serve trash by forgetting a check.
func (s *Store) ItemByID(ctx context.Context, id string) (*Item, error) {
	return scanItem(s.DB.QueryRowContext(ctx,
		`SELECT `+itemColumns+` FROM items WHERE id = ? AND deleted_at IS NULL`, id))
}

// ItemByIDIncludingDeleted returns an item whatever its state. Trash listing,
// restore and purge use this.
func (s *Store) ItemByIDIncludingDeleted(ctx context.Context, id string) (*Item, error) {
	return scanItem(s.DB.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM items WHERE id = ?`, id))
}

// ItemsByJobID returns every live item an ingest job produced. One job can
// yield several items: a Twitter gallery is four images.
func (s *Store) ItemsByJobID(ctx context.Context, jobID int64) ([]*Item, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+itemColumns+` FROM items WHERE job_id = ? AND deleted_at IS NULL ORDER BY created_at, id`, jobID)
	if err != nil {
		return nil, fmt.Errorf("db: items for job %d: %w", jobID, err)
	}
	defer rows.Close()

	var items []*Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate job items: %w", err)
	}
	return items, nil
}

// ContentHashRefCount counts the rows other than excludeItemID that still
// reference a content hash.
//
// Deliberately counts soft-deleted rows too. The spec says "no other
// non-deleted item", but a soft-deleted item is restorable: unlinking its blob
// would turn the trash into a set of broken records. Counting every remaining
// row is the restore-safe reading.
func (s *Store) ContentHashRefCount(ctx context.Context, hash, excludeItemID string) (int, error) {
	var count int
	err := s.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM items WHERE content_hash = ? AND id <> ?`, hash, excludeItemID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("db: count references to %s: %w", hash, err)
	}
	return count, nil
}

// ErrForbidden means the actor is not allowed to perform the operation.
var ErrForbidden = errors.New("db: not permitted")

// SetItemTitle renames an item. Titles are trimmed; renaming never touches disk.
func (s *Store) SetItemTitle(ctx context.Context, id, title string) error {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE items SET title = ? WHERE id = ? AND deleted_at IS NULL`, strings.TrimSpace(title), id)
	return requireRows(res, err, "rename item")
}

// SetItemMimeForTest overrides the stored mime. It exists so tests can
// exercise the webm embed fallback without a real webm blob; production
// never calls it.
func (s *Store) SetItemMimeForTest(ctx context.Context, id, mime string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE items SET mime = ? WHERE id = ?`, mime, id)
	return requireRows(res, err, "set item mime")
}

// MoveItem re-files an item. folderID 0 means the root. Files never move on
// disk: this is a single column update.
func (s *Store) MoveItem(ctx context.Context, id string, folderID int64) error {
	if folderID != 0 {
		var exists int
		if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM folders WHERE id = ?`, folderID).Scan(&exists); err != nil {
			return fmt.Errorf("db: check folder %d: %w", folderID, err)
		}
		if exists == 0 {
			return fmt.Errorf("db: folder %d does not exist: %w", folderID, ErrNotFound)
		}
	}
	res, err := s.DB.ExecContext(ctx,
		`UPDATE items SET folder_id = ? WHERE id = ? AND deleted_at IS NULL`, nullableID(folderID), id)
	return requireRows(res, err, "move item")
}

// SetItemProbe records what ffprobe found. duration is 0 for stills.
func (s *Store) SetItemProbe(ctx context.Context, id string, width, height int64, duration float64) error {
	var durationValue any
	if duration > 0 {
		durationValue = duration
	}
	res, err := s.DB.ExecContext(ctx,
		`UPDATE items SET width = ?, height = ?, duration = ? WHERE id = ?`,
		width, height, durationValue, id)
	return requireRows(res, err, "record probe results")
}

// SetItemShareRevoked kills or restores a leaked share link without deleting
// the item. Friends can still browse it; the public routes 404.
func (s *Store) SetItemShareRevoked(ctx context.Context, id string, revoked bool) error {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE items SET share_revoked = ? WHERE id = ? AND deleted_at IS NULL`, revoked, id)
	return requireRows(res, err, "set share revocation")
}

// SoftDeleteItem moves an item to the trash. Only the uploader or an admin may
// do this; everything stays recoverable until an admin purges it.
func (s *Store) SoftDeleteItem(ctx context.Context, id string, actor *User) error {
	if actor == nil {
		return ErrForbidden
	}
	item, err := s.ItemByID(ctx, id)
	if err != nil {
		return err
	}
	if !actor.IsAdmin && item.UploaderID != actor.ID {
		return ErrForbidden
	}
	res, err := s.DB.ExecContext(ctx,
		`UPDATE items SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), id)
	return requireRows(res, err, "soft delete item")
}

// RestoreItem brings an item back out of the trash.
func (s *Store) RestoreItem(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE items SET deleted_at = NULL WHERE id = ? AND deleted_at IS NOT NULL`, id)
	return requireRows(res, err, "restore item")
}

// PurgeItem permanently removes an item row and reports whether its blob may
// be unlinked. The returned hash is non-empty only when no other row (live or
// trashed) still references the same content.
//
// The caller unlinks the file; doing it here would mix database and filesystem
// concerns and make the refcount untestable without a data directory.
func (s *Store) PurgeItem(ctx context.Context, id string) (string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("db: begin purge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var hash string
	err = tx.QueryRowContext(ctx, `SELECT content_hash FROM items WHERE id = ?`, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("db: read item for purge: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id); err != nil {
		return "", fmt.Errorf("db: purge item: %w", err)
	}

	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM items WHERE content_hash = ?`, hash).Scan(&remaining); err != nil {
		return "", fmt.Errorf("db: count remaining references: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("db: commit purge: %w", err)
	}
	if remaining > 0 {
		return "", nil
	}
	return hash, nil
}
