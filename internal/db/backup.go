package db

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// backupFileMode is the permission the backup file is forced to regardless of
// the process umask. A backup holds a full copy of the database, including
// password hashes, so it must never be more readable than the source file.
const backupFileMode = 0o600

// BackupTo writes a consistent copy of the database to path using VACUUM
// INTO, which is safe against a live WAL without pausing writers first.
// VACUUM INTO refuses to overwrite an existing file, and it does not accept a
// bound parameter for its target, so path is single-quoted with SQL
// escaping; path is always server-controlled (the backups dir plus a date),
// never user input.
func (s *Store) BackupTo(ctx context.Context, path string) error {
	quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	if _, err := s.DB.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return fmt.Errorf("db: backup to %s: %w", path, err)
	}
	if err := os.Chmod(path, backupFileMode); err != nil {
		return fmt.Errorf("db: restrict permissions on backup %s: %w", path, err)
	}
	return nil
}
