package db_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

var jobEpoch = time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

func TestEnqueueAndClaimJob(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)

	id, err := store.EnqueueJob(ctx, "probe", []byte(`{"item_id":"abc"}`), jobEpoch)
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	job, err := store.ClaimJob(ctx, jobEpoch)
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if job.ID != id {
		t.Errorf("claimed job %d, want %d", job.ID, id)
	}
	if job.Type != "probe" {
		t.Errorf("Type = %q, want probe", job.Type)
	}
	if string(job.Payload) != `{"item_id":"abc"}` {
		t.Errorf("Payload = %q", job.Payload)
	}
	if job.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1 after a claim", job.Attempts)
	}

	// A claimed job is no longer claimable.
	if _, err := store.ClaimJob(ctx, jobEpoch); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("second ClaimJob = %v, want ErrNotFound", err)
	}
}

func TestClaimJobRespectsNextAttemptAt(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)

	if _, err := store.EnqueueJob(ctx, "probe", []byte(`{}`), jobEpoch.Add(time.Hour)); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if _, err := store.ClaimJob(ctx, jobEpoch); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("ClaimJob before next_attempt_at = %v, want ErrNotFound", err)
	}
	if _, err := store.ClaimJob(ctx, jobEpoch.Add(2*time.Hour)); err != nil {
		t.Fatalf("ClaimJob after next_attempt_at: %v", err)
	}
}

func TestClaimJobIsFIFO(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	first, _ := store.EnqueueJob(ctx, "probe", []byte(`{"n":1}`), jobEpoch)
	second, _ := store.EnqueueJob(ctx, "probe", []byte(`{"n":2}`), jobEpoch)

	a, err := store.ClaimJob(ctx, jobEpoch)
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	b, err := store.ClaimJob(ctx, jobEpoch)
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if a.ID != first || b.ID != second {
		t.Errorf("claim order = %d, %d; want %d, %d", a.ID, b.ID, first, second)
	}
}

func TestCompleteRetryAndFailJob(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)

	done, _ := store.EnqueueJob(ctx, "probe", []byte(`{}`), jobEpoch)
	if _, err := store.ClaimJob(ctx, jobEpoch); err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if err := store.CompleteJob(ctx, done); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}
	job, err := store.JobByID(ctx, done)
	if err != nil {
		t.Fatalf("JobByID: %v", err)
	}
	if job.Status != "done" {
		t.Errorf("Status = %q, want done", job.Status)
	}

	retry, _ := store.EnqueueJob(ctx, "probe", []byte(`{}`), jobEpoch)
	if _, err := store.ClaimJob(ctx, jobEpoch); err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if err := store.RetryJob(ctx, retry, jobEpoch.Add(time.Minute), "ffprobe exploded"); err != nil {
		t.Fatalf("RetryJob: %v", err)
	}
	job, _ = store.JobByID(ctx, retry)
	if job.Status != "queued" {
		t.Errorf("Status = %q, want queued after a retry", job.Status)
	}
	if job.Error != "ffprobe exploded" {
		t.Errorf("Error = %q, want the cause recorded", job.Error)
	}
	if !job.NextAttemptAt.After(jobEpoch) {
		t.Error("NextAttemptAt was not pushed into the future")
	}

	failed, _ := store.EnqueueJob(ctx, "probe", []byte(`{}`), jobEpoch)
	if _, err := store.ClaimJob(ctx, jobEpoch); err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if err := store.FailJob(ctx, failed, "gave up"); err != nil {
		t.Fatalf("FailJob: %v", err)
	}
	job, _ = store.JobByID(ctx, failed)
	if job.Status != "failed" || job.Error != "gave up" {
		t.Errorf("job = %+v, want failed with the cause", job)
	}
}

func TestEnqueueJobRejectsAnUnknownType(t *testing.T) {
	// The schema CHECK constraint is the backstop.
	if _, err := dbtest.New(t).EnqueueJob(context.Background(), "nonsense", []byte(`{}`), jobEpoch); err == nil {
		t.Fatal("EnqueueJob accepted an unknown type, want an error")
	}
}

func TestJobByIDUnknown(t *testing.T) {
	if _, err := dbtest.New(t).JobByID(context.Background(), 4040); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("JobByID = %v, want ErrNotFound", err)
	}
}

func TestRecoverRunningJobsWorksWithClaim(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	id, _ := store.EnqueueJob(ctx, "probe", []byte(`{}`), jobEpoch)
	if _, err := store.ClaimJob(ctx, jobEpoch); err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	// Simulate a crash: the job is stuck in 'running'.
	recovered, err := store.RecoverRunningJobs(ctx)
	if err != nil {
		t.Fatalf("RecoverRunningJobs: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered %d, want 1", recovered)
	}
	job, err := store.ClaimJob(ctx, jobEpoch)
	if err != nil {
		t.Fatalf("ClaimJob after recovery: %v", err)
	}
	if job.ID != id {
		t.Errorf("claimed %d, want the recovered job %d", job.ID, id)
	}
}

// TestClaimJobIsAtomicUnderConcurrency is the binding constraint from the
// task brief made concrete: real goroutines racing ClaimJob must never both
// come away with the same row. It enqueues N jobs and starts more than N
// goroutines claiming concurrently; every job must be claimed by exactly one
// goroutine, and every claim must resolve to a distinct job (or ErrNotFound
// once the queue is empty).
func TestClaimJobIsAtomicUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)

	const numJobs = 50
	const numWorkers = 12

	want := make(map[int64]bool, numJobs)
	for i := 0; i < numJobs; i++ {
		id, err := store.EnqueueJob(ctx, "probe", []byte(`{}`), jobEpoch)
		if err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}
		want[id] = true
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed = make(map[int64]int) // job id -> number of times claimed
	)
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				job, err := store.ClaimJob(ctx, jobEpoch)
				if errors.Is(err, db.ErrNotFound) {
					return
				}
				if err != nil {
					t.Errorf("ClaimJob: %v", err)
					return
				}
				mu.Lock()
				claimed[job.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(claimed) != numJobs {
		t.Fatalf("claimed %d distinct jobs, want %d", len(claimed), numJobs)
	}
	for id, n := range claimed {
		if !want[id] {
			t.Errorf("claimed unknown job %d", id)
		}
		if n != 1 {
			t.Errorf("job %d was claimed %d times, want exactly 1", id, n)
		}
	}
}
