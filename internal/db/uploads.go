package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"boobies-media/internal/auth"
)

// Upload is one in-flight chunked upload. It exists only between the client's
// first POST /api/uploads and either completion or expiry.
type Upload struct {
	ID           string
	UserID       int64
	FolderID     int64 // 0 means the root (SQL NULL)
	Filename     string
	DeclaredSize int64
	ChunkSize    int64
	Received     []int
	TempDir      string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

// NewUpload is the input to CreateUpload.
type NewUpload struct {
	UserID       int64
	FolderID     int64
	Filename     string
	DeclaredSize int64
	ChunkSize    int64
	TempDir      string
	ExpiresAt    time.Time
}

// ChunkCount is how many chunks the declared size divides into. A zero-byte
// file is one empty chunk, which keeps the "upload every index" loop uniform.
func (u *Upload) ChunkCount() int {
	if u.ChunkSize <= 0 {
		return 0
	}
	if u.DeclaredSize == 0 {
		return 1
	}
	return int((u.DeclaredSize + u.ChunkSize - 1) / u.ChunkSize)
}

// Missing lists the indices the server has not stored yet, in order. This is
// what a resuming client asks for.
func (u *Upload) Missing() []int {
	have := make(map[int]bool, len(u.Received))
	for _, i := range u.Received {
		have[i] = true
	}
	var missing []int
	for i := 0; i < u.ChunkCount(); i++ {
		if !have[i] {
			missing = append(missing, i)
		}
	}
	return missing
}

// IsComplete reports whether every chunk has been stored.
func (u *Upload) IsComplete() bool { return len(u.Missing()) == 0 }

// CreateUpload opens an upload and hands back its token.
func (s *Store) CreateUpload(ctx context.Context, in NewUpload) (*Upload, error) {
	if in.UserID == 0 {
		return nil, errors.New("db: upload must have an uploader")
	}
	if in.ChunkSize <= 0 {
		return nil, errors.New("db: upload chunk size must be positive")
	}
	id, err := auth.NewItemID()
	if err != nil {
		return nil, fmt.Errorf("db: upload id: %w", err)
	}
	now := time.Now().UTC()
	createdAt := now.Format(time.RFC3339)
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO uploads (id, user_id, folder_id, filename, declared_size,
			chunk_size, received, temp_dir, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, '[]', ?, ?, ?)`,
		id, in.UserID, nullableID(in.FolderID), in.Filename, in.DeclaredSize,
		in.ChunkSize, in.TempDir, createdAt, in.ExpiresAt.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("db: create upload: %w", err)
	}
	return &Upload{
		ID: id, UserID: in.UserID, FolderID: in.FolderID, Filename: in.Filename,
		DeclaredSize: in.DeclaredSize, ChunkSize: in.ChunkSize, Received: []int{},
		TempDir: in.TempDir, CreatedAt: now, ExpiresAt: in.ExpiresAt.UTC(),
	}, nil
}

const uploadColumns = `id, user_id, COALESCE(folder_id, 0), filename,
	declared_size, chunk_size, received, temp_dir, created_at, expires_at`

func scanUpload(row interface{ Scan(...any) error }) (*Upload, error) {
	var (
		up       Upload
		received string
		created  string
		expires  string
	)
	if err := row.Scan(&up.ID, &up.UserID, &up.FolderID, &up.Filename,
		&up.DeclaredSize, &up.ChunkSize, &received, &up.TempDir, &created, &expires); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(received), &up.Received); err != nil {
		return nil, fmt.Errorf("db: upload %s has unreadable chunk state: %w", up.ID, err)
	}
	if up.Received == nil {
		up.Received = []int{}
	}
	var err error
	if up.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
		return nil, fmt.Errorf("db: upload %s has unreadable created_at: %w", up.ID, err)
	}
	if up.ExpiresAt, err = time.Parse(time.RFC3339, expires); err != nil {
		return nil, fmt.Errorf("db: upload %s has unreadable expires_at: %w", up.ID, err)
	}
	return &up, nil
}

// UploadByID loads one upload. Callers must check ownership themselves.
func (s *Store) UploadByID(ctx context.Context, id string) (*Upload, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+uploadColumns+` FROM uploads WHERE id = ?`, id)
	up, err := scanUpload(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: upload by id: %w", err)
	}
	return up, nil
}

// RecordChunk marks one chunk as stored. Recording an index that is already
// present leaves the received set unchanged: that idempotency is what makes
// a client safe to retry after a lost response.
//
// expiresAt pushes the upload's deadline forward: the caller passes the same
// now+TTL computation CreateUpload used, so expires_at measures time since
// the last chunk rather than time since creation. Without this, a large
// upload over a slow connection can cross its original deadline while chunks
// are still actively arriving, and the janitor (ReapUploads/ExpiredUploads)
// would delete it out from under the client. A retried chunk that is already
// in Received still pushes the deadline forward (a duplicate request is
// still evidence the client is alive), but an upload that stops receiving
// chunks keeps whatever expires_at its last chunk (or creation, if none
// arrived) set, so it is reaped normally once that deadline passes.
//
// The read-modify-write of the received set, and this expiry push, run
// inside one transaction so two chunks for the same upload landing
// concurrently (e.g. a client uploading several chunks in parallel) cannot
// race: with the store's single-connection pool, starting a transaction here
// blocks any other RecordChunk call on the same Store until this one
// commits, so one call can never overwrite the received set (or expires_at)
// with a copy that is stale relative to the other's write.
func (s *Store) RecordChunk(ctx context.Context, id string, index int, expiresAt time.Time) (*Upload, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("db: record chunk: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `SELECT `+uploadColumns+` FROM uploads WHERE id = ?`, id)
	up, err := scanUpload(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: record chunk: %w", err)
	}
	if index < 0 || index >= up.ChunkCount() {
		return nil, fmt.Errorf("db: chunk %d is outside upload %s (%d chunks)", index, id, up.ChunkCount())
	}
	if !slices.Contains(up.Received, index) {
		up.Received = append(up.Received, index)
		sort.Ints(up.Received)
	}
	encoded, err := json.Marshal(up.Received)
	if err != nil {
		return nil, fmt.Errorf("db: encode chunk state: %w", err)
	}
	up.ExpiresAt = expiresAt.UTC()
	if _, err := tx.ExecContext(ctx,
		`UPDATE uploads SET received = ?, expires_at = ? WHERE id = ?`,
		string(encoded), up.ExpiresAt.Format(time.RFC3339), id); err != nil {
		return nil, fmt.Errorf("db: record chunk: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("db: record chunk: %w", err)
	}
	return up, nil
}

// DeleteUpload removes the row. The caller removes TempDir.
func (s *Store) DeleteUpload(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM uploads WHERE id = ?`, id)
	return requireRows(res, err, "delete upload")
}

// ExpiredUploads lists uploads past their deadline so the janitor can reap
// both the row and the bytes.
func (s *Store) ExpiredUploads(ctx context.Context, now time.Time) ([]*Upload, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+uploadColumns+` FROM uploads WHERE expires_at <= ? ORDER BY expires_at`,
		now.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("db: expired uploads: %w", err)
	}
	defer rows.Close()

	var out []*Upload
	for rows.Next() {
		up, err := scanUpload(rows)
		if err != nil {
			return nil, fmt.Errorf("db: scan expired upload: %w", err)
		}
		out = append(out, up)
	}
	return out, rows.Err()
}
