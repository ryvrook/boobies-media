package db

import (
	"context"
	"fmt"
)

// RecoverRunningJobs re-queues jobs that were mid-flight when the process
// died. Without this, a crash strands them in 'running' forever: no worker
// ever picks them up and nothing surfaces them as failed.
func (s *Store) RecoverRunningJobs(ctx context.Context) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE jobs SET status = 'queued' WHERE status = 'running'`)
	if err != nil {
		return 0, fmt.Errorf("db: recover running jobs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("db: recover running jobs: %w", err)
	}
	return n, nil
}
