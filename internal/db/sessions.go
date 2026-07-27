package db

import (
	"context"
	"fmt"
	"time"
)

// CreateSession records a session. tokenHash must be the SHA-256 hex of the
// token that goes into the cookie; the plaintext is never stored.
func (s *Store) CreateSession(ctx context.Context, tokenHash string, userID int64, expiresAt time.Time) error {
	if tokenHash == "" {
		return fmt.Errorf("db: session token hash must not be empty")
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		tokenHash, userID,
		expiresAt.UTC().Format(time.RFC3339),
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("db: create session: %w", err)
	}
	return nil
}

// SessionUser resolves a session token hash to its owner in one query.
// Unknown or expired sessions return ErrNotFound.
func (s *Store) SessionUser(ctx context.Context, tokenHash string, now time.Time) (*User, error) {
	if tokenHash == "" {
		return nil, ErrNotFound
	}
	const q = `SELECT u.id, u.username, u.display_name,
		COALESCE(u.avatar_hash, ''), u.password_hash, COALESCE(u.api_key_hash, ''),
		u.is_admin, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`
	return scanUser(s.DB.QueryRowContext(ctx, q, tokenHash, now.UTC().Format(time.RFC3339)))
}

// DeleteSession removes one session. Deleting a session that does not exist is
// not an error, so logout is safe to repeat.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("db: delete session: %w", err)
	}
	return nil
}

// DeleteUserSessions logs a user out everywhere.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("db: delete user sessions: %w", err)
	}
	return nil
}

// DeleteExpiredSessions prunes sessions that are past their expiry and reports
// how many rows were removed.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("db: delete expired sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("db: delete expired sessions: %w", err)
	}
	return n, nil
}
