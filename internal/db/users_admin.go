package db

import (
	"context"
	"errors"
	"fmt"
)

// ErrUserHasItems means a user could not be deleted because they still own
// items; deleting them would orphan those rows' uploader attribution.
var ErrUserHasItems = errors.New("db: user still owns items")

// CountItemsByUploader counts every item (including soft-deleted) a user owns.
func (s *Store) CountItemsByUploader(ctx context.Context, uploaderID int64) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE uploader_id = ?`, uploaderID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("db: count items by uploader: %w", err)
	}
	return n, nil
}

// DeleteUser removes a user with no items. It refuses with ErrUserHasItems
// while the user still owns any item (live or trashed), so nothing is ever
// orphaned or silently cascaded away. Sessions cascade via the schema's
// foreign key.
//
// The count and the delete run inside one transaction rather than as two
// top-level calls. With SetMaxOpenConns(1) the pool holds exactly one
// physical connection, and BeginTx checks that connection out exclusively
// until Commit/Rollback: no other query, including a concurrent CreateItem
// for the same user, can run in between. That closes the TOCTOU window a
// separate count-then-delete would have: without the transaction, an item
// created between the count and the delete would make the DELETE hit the
// items.uploader_id foreign key and fail with a raw driver error instead of
// the ErrUserHasItems sentinel the admin HTTP layer depends on.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin delete user: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE uploader_id = ?`, id).Scan(&n); err != nil {
		return fmt.Errorf("db: count items by uploader: %w", err)
	}
	if n > 0 {
		return ErrUserHasItems
	}

	res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err := requireRows(res, err, "delete user"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit delete user: %w", err)
	}
	return nil
}

// SetUserAdmin grants or revokes admin.
func (s *Store) SetUserAdmin(ctx context.Context, id int64, isAdmin bool) error {
	adminInt := 0
	if isAdmin {
		adminInt = 1
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE users SET is_admin = ? WHERE id = ?`, adminInt, id)
	return requireRows(res, err, "set user admin")
}
