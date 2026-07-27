package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrFolderCycle means a move would have made a folder its own ancestor.
var ErrFolderCycle = errors.New("db: a folder cannot be moved inside itself")

// ErrDuplicateFolder means a folder with that name already exists under the
// same parent, or at the root.
var ErrDuplicateFolder = errors.New("db: a folder with that name already exists here")

// maxFolderDepth bounds the ancestor walk. It is a guard against a tree that
// is already corrupt, not a product limit.
const maxFolderDepth = 64

// Folder is a node in the virtual tree. Files never move on disk.
type Folder struct {
	ID        int64
	ParentID  int64 // 0 means the root
	Name      string
	CreatedAt time.Time
}

func scanFolder(row interface{ Scan(...any) error }) (*Folder, error) {
	var (
		folder    Folder
		createdAt string
	)
	err := row.Scan(&folder.ID, &folder.ParentID, &folder.Name, &createdAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("db: scan folder: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("db: parse folder created_at %q: %w", createdAt, err)
	}
	folder.CreatedAt = parsed.UTC()
	return &folder, nil
}

const folderColumns = `id, COALESCE(parent_id, 0), name, created_at`

// NormalizeFolderName trims and validates a folder name.
func NormalizeFolderName(s string) (string, error) {
	name := strings.TrimSpace(s)
	if name == "" {
		return "", fmt.Errorf("db: folder name must not be empty")
	}
	if len(name) > 100 {
		return "", fmt.Errorf("db: folder name must be 100 characters or fewer")
	}
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("db: folder name must not contain slashes")
	}
	return name, nil
}

// CreateFolder adds a folder. parentID 0 creates it at the root.
func (s *Store) CreateFolder(ctx context.Context, parentID int64, name string) (*Folder, error) {
	clean, err := NormalizeFolderName(name)
	if err != nil {
		return nil, err
	}
	if parentID != 0 {
		if _, err := s.FolderByID(ctx, parentID); err != nil {
			return nil, fmt.Errorf("db: parent folder %d: %w", parentID, err)
		}
	}
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO folders (parent_id, name, created_at) VALUES (?, ?, ?)`,
		nullableID(parentID), clean, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, fmt.Errorf("db: create folder %q: %w", clean, ErrDuplicateFolder)
		}
		return nil, fmt.Errorf("db: create folder: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("db: create folder: %w", err)
	}
	return s.FolderByID(ctx, id)
}

// FolderByID looks up one folder.
func (s *Store) FolderByID(ctx context.Context, id int64) (*Folder, error) {
	return scanFolder(s.DB.QueryRowContext(ctx, `SELECT `+folderColumns+` FROM folders WHERE id = ?`, id))
}

// ListFolders returns every folder ordered root-first then alphabetically, so
// a caller can build the tree in a single pass.
func (s *Store) ListFolders(ctx context.Context) ([]*Folder, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+folderColumns+` FROM folders ORDER BY COALESCE(parent_id, 0), lower(name)`)
	if err != nil {
		return nil, fmt.Errorf("db: list folders: %w", err)
	}
	defer rows.Close()

	var folders []*Folder
	for rows.Next() {
		folder, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		folders = append(folders, folder)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate folders: %w", err)
	}
	return folders, nil
}

// RenameFolder changes a folder's name in place.
func (s *Store) RenameFolder(ctx context.Context, id int64, name string) error {
	clean, err := NormalizeFolderName(name)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE folders SET name = ? WHERE id = ?`, clean, id)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("db: rename folder %d to %q: %w", id, clean, ErrDuplicateFolder)
	}
	return requireRows(res, err, "rename folder")
}

// MoveFolder re-parents a folder. newParentID 0 moves it to the root.
//
// Moving a folder inside its own descendant would detach that subtree from the
// root and make any recursive tree render loop forever, so the ancestors of
// the target are walked first and the move is refused.
func (s *Store) MoveFolder(ctx context.Context, id, newParentID int64) error {
	if id == 0 {
		return fmt.Errorf("db: the root is not a movable folder")
	}
	folder, err := s.FolderByID(ctx, id)
	if err != nil {
		return err
	}
	if newParentID != 0 {
		if id == newParentID {
			return ErrFolderCycle
		}
		if _, err := s.FolderByID(ctx, newParentID); err != nil {
			return fmt.Errorf("db: target folder %d: %w", newParentID, err)
		}
		descendant, err := s.isDescendantOf(ctx, newParentID, id)
		if err != nil {
			return err
		}
		if descendant {
			return ErrFolderCycle
		}
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE folders SET parent_id = ? WHERE id = ?`, nullableID(newParentID), id)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("db: move folder %d (%q): %w", id, folder.Name, ErrDuplicateFolder)
	}
	return requireRows(res, err, "move folder")
}

// isDescendantOf reports whether candidate sits anywhere beneath ancestor.
func (s *Store) isDescendantOf(ctx context.Context, candidate, ancestor int64) (bool, error) {
	current := candidate
	for depth := 0; depth < maxFolderDepth; depth++ {
		if current == 0 {
			return false, nil
		}
		if current == ancestor {
			return true, nil
		}
		var parent int64
		err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(parent_id, 0) FROM folders WHERE id = ?`, current).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("db: walk folder ancestors: %w", err)
		}
		current = parent
	}
	// Only reachable if the tree is already cyclic; refuse rather than hang.
	return true, nil
}

// FolderPath returns the breadcrumb from the root down to id, inclusive.
func (s *Store) FolderPath(ctx context.Context, id int64) ([]*Folder, error) {
	var reversed []*Folder
	current := id
	for depth := 0; depth < maxFolderDepth && current != 0; depth++ {
		folder, err := s.FolderByID(ctx, current)
		if err != nil {
			return nil, err
		}
		reversed = append(reversed, folder)
		current = folder.ParentID
	}
	path := make([]*Folder, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		path = append(path, reversed[i])
	}
	return path, nil
}

// DeleteFolder removes a folder. Child folders cascade; items inside fall back
// to the root rather than being deleted (ON DELETE SET NULL in the schema).
func (s *Store) DeleteFolder(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM folders WHERE id = ?`, id)
	return requireRows(res, err, "delete folder")
}
