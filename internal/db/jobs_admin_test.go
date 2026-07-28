package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

var adminJobEpoch = time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC)

func TestListJobsIsNewestFirst(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	for i := 0; i < 3; i++ {
		if _, err := store.EnqueueJob(ctx, "probe", []byte(`{}`), adminJobEpoch.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}
	}
	jobs, err := store.ListJobs(ctx, 10)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("listed %d jobs, want 3", len(jobs))
	}
	if jobs[0].ID < jobs[2].ID {
		t.Errorf("jobs are not newest-first: %d then %d", jobs[0].ID, jobs[2].ID)
	}
}

func TestListJobsPageAndCount(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	for i := 0; i < 25; i++ {
		if _, err := store.EnqueueJob(ctx, "probe", []byte(`{}`), adminJobEpoch.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}
	}
	count, err := store.CountJobs(ctx)
	if err != nil {
		t.Fatalf("CountJobs: %v", err)
	}
	if count != 25 {
		t.Fatalf("CountJobs = %d, want 25", count)
	}
	page, err := store.ListJobsPage(ctx, 20, 20)
	if err != nil {
		t.Fatalf("ListJobsPage: %v", err)
	}
	if len(page) != 5 {
		t.Fatalf("second page has %d jobs, want 5", len(page))
	}
	if page[0].ID != 5 || page[4].ID != 1 {
		t.Errorf("second page IDs = %d...%d, want 5...1", page[0].ID, page[4].ID)
	}
}

func TestRequeueJobResetsAFailedJob(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	id, err := store.EnqueueJob(ctx, "ingest_url", []byte(`{}`), adminJobEpoch)
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if err := store.FailJob(ctx, id, "boom"); err != nil {
		t.Fatalf("FailJob: %v", err)
	}

	if err := store.RequeueJob(ctx, id, adminJobEpoch); err != nil {
		t.Fatalf("RequeueJob: %v", err)
	}
	job, err := store.JobByID(ctx, id)
	if err != nil {
		t.Fatalf("JobByID: %v", err)
	}
	if job.Status != "queued" || job.Attempts != 0 || job.Error != "" {
		t.Errorf("requeued job = {status:%q attempts:%d error:%q}, want {queued 0 \"\"}", job.Status, job.Attempts, job.Error)
	}
}

func TestRequeueJobRejectsNonFailedAndMissing(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	id, _ := store.EnqueueJob(ctx, "probe", []byte(`{}`), adminJobEpoch) // still queued
	if err := store.RequeueJob(ctx, id, adminJobEpoch); !errors.Is(err, db.ErrJobNotFailed) {
		t.Errorf("requeue of a queued job = %v, want ErrJobNotFailed", err)
	}
	if err := store.RequeueJob(ctx, 999, adminJobEpoch); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("requeue of a missing job = %v, want ErrNotFound", err)
	}
}

func TestBulkRetryAndCancelPendingJobs(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	failedA, _ := store.EnqueueJob(ctx, "ingest_url", []byte(`{}`), adminJobEpoch)
	failedB, _ := store.EnqueueJob(ctx, "ingest_url", []byte(`{}`), adminJobEpoch)
	pending, _ := store.EnqueueJob(ctx, "ingest_url", []byte(`{}`), adminJobEpoch)
	_ = store.FailJob(ctx, failedA, "a")
	_ = store.FailJob(ctx, failedB, "b")

	retried, err := store.RequeueAllFailedJobs(ctx, adminJobEpoch)
	if err != nil || retried != 2 {
		t.Fatalf("RequeueAllFailedJobs = %d, %v; want 2, nil", retried, err)
	}
	cancelled, err := store.CancelPendingJobs(ctx)
	if err != nil || cancelled != 3 {
		t.Fatalf("CancelPendingJobs = %d, %v; want 3, nil", cancelled, err)
	}
	for _, id := range []int64{failedA, failedB, pending} {
		if _, err := store.JobByID(ctx, id); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("job %d survived pending cancellation: %v", id, err)
		}
	}
}
