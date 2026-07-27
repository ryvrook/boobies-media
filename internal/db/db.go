// Package db owns the SQLite connection and every query the server runs.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"
)

// ErrNotFound is returned by every query helper when no row matches.
var ErrNotFound = errors.New("db: not found")

// Store wraps the single SQLite connection used by the whole process.
type Store struct {
	DB *sql.DB
}

// Open opens (creating if needed) the SQLite database at dbPath with the
// pragmas the design mandates, caps the pool at one connection, and runs
// migrations. A non-nil error here is a startup hard-fail.
func Open(dbPath string) (*Store, error) {
	if strings.ContainsAny(dbPath, "?#") {
		return nil, fmt.Errorf("db: database path %q must not contain '?' or '#'", dbPath)
	}
	dsn := "file:" + dbPath +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)"

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", dbPath, err)
	}
	// One writer connection removes SQLITE_BUSY as a class. WAL still lets
	// readers proceed; at friends-scale a single connection is plenty.
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("db: cannot open database %s: %w", dbPath, err)
	}
	if err := Migrate(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return &Store{DB: sqlDB}, nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

// requireRows turns a zero-rows-affected update into ErrNotFound, so every
// mutation across the package reports an unknown id the same way.
func requireRows(res sql.Result, err error, op string) error {
	if err != nil {
		return fmt.Errorf("db: %s: %w", op, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: %s: %w", op, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
