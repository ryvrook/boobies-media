package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrJobNotFailed means a manual requeue was asked for a job that has not
// failed, so there is nothing to retry.
var ErrJobNotFailed = errors.New("db: job is not in the failed state")

// ListJobs returns recent jobs newest-first for the admin queue view.
func (s *Store) ListJobs(ctx context.Context, limit int) ([]*Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, type, payload, status, attempts, error, next_attempt_at, created_at
		 FROM jobs ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("db: list jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		var (
			job           Job
			payload       string
			errText       sql.NullString
			nextAttemptAt string
			createdAt     string
		)
		if err := rows.Scan(&job.ID, &job.Type, &payload, &job.Status, &job.Attempts, &errText, &nextAttemptAt, &createdAt); err != nil {
			return nil, fmt.Errorf("db: scan job: %w", err)
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
		jobs = append(jobs, &job)
	}
	return jobs, rows.Err()
}

// RequeueJob resets a failed job for a fresh set of attempts. It is the admin
// retry button; the queue's own backoff uses RetryJob instead.
func (s *Store) RequeueJob(ctx context.Context, id int64, runAt time.Time) error {
	job, err := s.JobByID(ctx, id)
	if err != nil {
		return err // ErrNotFound propagates
	}
	if job.Status != "failed" {
		return ErrJobNotFailed
	}
	_, err = s.DB.ExecContext(ctx,
		`UPDATE jobs SET status = 'queued', attempts = 0, next_attempt_at = ?, error = '' WHERE id = ?`,
		runAt.UTC().Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("db: requeue job %d: %w", id, err)
	}
	return nil
}
