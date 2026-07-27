package db_test

import (
	"context"
	"testing"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

func seedJob(t *testing.T, store *db.Store, jobType, status string) int64 {
	t.Helper()
	res, err := store.DB.ExecContext(context.Background(),
		`INSERT INTO jobs (type, status, next_attempt_at, created_at)
		 VALUES (?, ?, '2026-07-23T00:00:00Z', '2026-07-23T00:00:00Z')`, jobType, status)
	if err != nil {
		t.Fatalf("seed job (%s/%s): %v", jobType, status, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("job id: %v", err)
	}
	return id
}

func jobStatus(t *testing.T, store *db.Store, id int64) string {
	t.Helper()
	var status string
	if err := store.DB.QueryRowContext(context.Background(), `SELECT status FROM jobs WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("read job %d: %v", id, err)
	}
	return status
}

func TestRecoverRunningJobsRequeuesOnlyRunning(t *testing.T) {
	store := dbtest.New(t)
	running := seedJob(t, store, "ingest_url", "running")
	queued := seedJob(t, store, "probe", "queued")
	done := seedJob(t, store, "thumbnail", "done")
	failed := seedJob(t, store, "probe", "failed")

	recovered, err := store.RecoverRunningJobs(context.Background())
	if err != nil {
		t.Fatalf("RecoverRunningJobs: %v", err)
	}
	if recovered != 1 {
		t.Errorf("recovered %d jobs, want 1", recovered)
	}
	if got := jobStatus(t, store, running); got != "queued" {
		t.Errorf("the crashed job is %q, want \"queued\"", got)
	}
	for id, want := range map[int64]string{queued: "queued", done: "done", failed: "failed"} {
		if got := jobStatus(t, store, id); got != want {
			t.Errorf("job %d is %q, want %q untouched", id, got, want)
		}
	}
}

func TestRecoverRunningJobsIsSafeOnEmptyQueue(t *testing.T) {
	recovered, err := dbtest.New(t).RecoverRunningJobs(context.Background())
	if err != nil {
		t.Fatalf("RecoverRunningJobs: %v", err)
	}
	if recovered != 0 {
		t.Errorf("recovered %d jobs from an empty queue, want 0", recovered)
	}
}
