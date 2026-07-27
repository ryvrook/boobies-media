package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ErrDuplicateUser means the username is already taken.
var ErrDuplicateUser = errors.New("db: username already exists")

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{1,31}$`)

// User is a friend-group account.
type User struct {
	ID           int64
	Username     string
	DisplayName  string
	AvatarHash   string // empty when unset
	PasswordHash string
	APIKeyHash   string // empty when the user has no API key
	IsAdmin      bool
	CreatedAt    time.Time
}

// NormalizeUsername trims and lowercases a username and validates its shape.
func NormalizeUsername(s string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(s))
	if !usernamePattern.MatchString(name) {
		return "", fmt.Errorf("db: username %q must be 2-32 characters of a-z, 0-9, dot, dash or underscore, starting with a letter or digit", s)
	}
	return name, nil
}

const userColumns = `id, username, display_name,
	COALESCE(avatar_hash, ''), password_hash, COALESCE(api_key_hash, ''),
	is_admin, created_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var (
		u         User
		createdAt string
	)
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.AvatarHash,
		&u.PasswordHash, &u.APIKeyHash, &u.IsAdmin, &createdAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("db: scan user: %w", err)
	}
	u.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("db: parse user created_at %q: %w", createdAt, err)
	}
	u.CreatedAt = u.CreatedAt.UTC()
	return &u, nil
}

// nullable turns an empty string into a SQL NULL so UNIQUE columns such as
// api_key_hash allow many users without a key.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CreateUser inserts a new account. passwordHash must already be argon2id
// encoded; apiKeyHash must already be SHA-256 hex (empty for no key).
func (s *Store) CreateUser(ctx context.Context, username, displayName, passwordHash, apiKeyHash string, isAdmin bool) (*User, error) {
	name, err := NormalizeUsername(username)
	if err != nil {
		return nil, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = name
	}
	if passwordHash == "" {
		return nil, fmt.Errorf("db: password hash must not be empty")
	}

	createdAt := time.Now().UTC().Format(time.RFC3339)
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO users (username, display_name, password_hash, api_key_hash, is_admin, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		name, displayName, passwordHash, nullable(apiKeyHash), isAdmin, createdAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.username") {
			return nil, fmt.Errorf("db: create user %q: %w", name, ErrDuplicateUser)
		}
		return nil, fmt.Errorf("db: create user %q: %w", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("db: create user %q: %w", name, err)
	}
	return s.UserByID(ctx, id)
}

// UserByID looks up an account by row id.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// UserByUsername looks up an account by its normalized username.
func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	name, err := NormalizeUsername(username)
	if err != nil {
		return nil, ErrNotFound
	}
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, name))
}

// UserByAPIKeyHash resolves a Bearer key hash to its owner. An empty hash
// never matches, so users without a key cannot be impersonated.
func (s *Store) UserByAPIKeyHash(ctx context.Context, apiKeyHash string) (*User, error) {
	if apiKeyHash == "" {
		return nil, ErrNotFound
	}
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE api_key_hash = ?`, apiKeyHash))
}

// ListUsers returns every account ordered by username.
func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("db: list users: %w", err)
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate users: %w", err)
	}
	return users, nil
}

// SetUserPassword replaces the stored hash and destroys every existing session
// for that user, so a password change actually logs other devices out.
func (s *Store) SetUserPassword(ctx context.Context, id int64, passwordHash string) error {
	if passwordHash == "" {
		return fmt.Errorf("db: password hash must not be empty")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin password change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, id)
	if err != nil {
		return fmt.Errorf("db: update password: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: update password: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
		return fmt.Errorf("db: invalidate sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit password change: %w", err)
	}
	return nil
}

// SetUserAPIKeyHash rotates a user's API key hash. Passing an empty string
// removes the key entirely.
func (s *Store) SetUserAPIKeyHash(ctx context.Context, id int64, apiKeyHash string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE users SET api_key_hash = ? WHERE id = ?`, nullable(apiKeyHash), id)
	if err != nil {
		return fmt.Errorf("db: rotate api key: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: rotate api key: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}
