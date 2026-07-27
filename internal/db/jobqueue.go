package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Job is one queued unit of background work.
type Job struct {
	ID            int64
	Type          string
	Payload       []byte
	Status        string
	Attempts      int
	Error         string
	NextAttemptAt time.Time
	CreatedAt     time.Time
}

// EnqueueJob adds a job, runnable from runAt onwards.
func (s *Store) EnqueueJob(ctx context.Context, jobType string, payload []byte, runAt time.Time) (int64, error) {
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO jobs (type, payload, status, attempts, next_attempt_at, created_at)
		 VALUES (?, ?, 'queued', 0, ?, ?)`,
		jobType, string(payload),
		runAt.UTC().Format(time.RFC3339),
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("db: enqueue %s job: %w", jobType, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("db: enqueue %s job: %w", jobType, err)
	}
	return id, nil
}

// ClaimJob atomically takes the oldest runnable job, marks it running and
// increments its attempt counter. ErrNotFound means the queue is idle.
//
// One statement with RETURNING is what makes this atomic; combined with the
// single-connection pool (see Open), two workers can never claim the same
// row: SQLite serialises the UPDATE ... RETURNING against the single
// connection, so the second claim's subquery no longer sees the row once the
// first has flipped its status.
func (s *Store) ClaimJob(ctx context.Context, now time.Time) (*Job, error) {
	var (
		job           Job
		payload       string
		nextAttemptAt string
		createdAt     string
		errText       sql.NullString
	)
	err := s.DB.QueryRowContext(ctx,
		`UPDATE jobs SET status = 'running', attempts = attempts + 1
		 WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'queued' AND next_attempt_at <= ?
			ORDER BY id LIMIT 1
		 )
		 RETURNING id, type, payload, status, attempts, error, next_attempt_at, created_at`,
		now.UTC().Format(time.RFC3339),
	).Scan(&job.ID, &job.Type, &payload, &job.Status, &job.Attempts, &errText, &nextAttemptAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: claim job: %w", err)
	}
	job.Payload = []byte(payload)
	job.Error = errText.String
	if job.NextAttemptAt, err = time.Parse(time.RFC3339, nextAttemptAt); err != nil {
		return nil, fmt.Errorf("db: parse job next_attempt_at %q: %w", nextAttemptAt, err)
	}
	job.NextAttemptAt = job.NextAttemptAt.UTC()
	if job.CreatedAt, err = time.Parse(time.RFC3339, createdAt); err != nil {
		return nil, fmt.Errorf("db: parse job created_at %q: %w", createdAt, err)
	}
	job.CreatedAt = job.CreatedAt.UTC()
	return &job, nil
}

// CompleteJob marks a job done and clears any earlier error.
func (s *Store) CompleteJob(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE jobs SET status = 'done', error = NULL WHERE id = ?`, id)
	return requireRows(res, err, "complete job")
}

// RetryJob puts a job back in the queue, runnable again at runAt, recording
// why the previous attempt failed.
func (s *Store) RetryJob(ctx context.Context, id int64, runAt time.Time, cause string) error {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE jobs SET status = 'queued', next_attempt_at = ?, error = ? WHERE id = ?`,
		runAt.UTC().Format(time.RFC3339), nullable(cause), id)
	return requireRows(res, err, "retry job")
}

// FailJob gives up on a job and records the final cause for the UI.
func (s *Store) FailJob(ctx context.Context, id int64, cause string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE jobs SET status = 'failed', error = ? WHERE id = ?`, nullable(cause), id)
	return requireRows(res, err, "fail job")
}

// JobByID reads one job, for the status API and the admin queue view.
func (s *Store) JobByID(ctx context.Context, id int64) (*Job, error) {
	var (
		job           Job
		payload       string
		nextAttemptAt string
		createdAt     string
		errText       sql.NullString
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, type, payload, status, attempts, error, next_attempt_at, created_at FROM jobs WHERE id = ?`, id,
	).Scan(&job.ID, &job.Type, &payload, &job.Status, &job.Attempts, &errText, &nextAttemptAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: read job %d: %w", id, err)
	}
	job.Payload = []byte(payload)
	job.Error = errText.String
	if job.NextAttemptAt, err = time.Parse(time.RFC3339, nextAttemptAt); err != nil {
		return nil, fmt.Errorf("db: parse job next_attempt_at %q: %w", nextAttemptAt, err)
	}
	job.NextAttemptAt = job.NextAttemptAt.UTC()
	if job.CreatedAt, err = time.Parse(time.RFC3339, createdAt); err != nil {
		return nil, fmt.Errorf("db: parse job created_at %q: %w", createdAt, err)
	}
	job.CreatedAt = job.CreatedAt.UTC()
	return &job, nil
}
