package jobs_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
	"boobies-media/internal/jobs"
)

func TestRunOnceDispatchesToTheRegisteredHandler(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	queue := jobs.New(store, 1)

	var got db.Job
	queue.Register(jobs.TypeProbe, func(_ context.Context, job db.Job) error {
		got = job
		return nil
	})

	id, err := queue.Enqueue(ctx, jobs.TypeProbe, map[string]string{"item_id": "abc12345"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	ran, err := queue.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !ran {
		t.Fatal("RunOnce reported no work despite a queued job")
	}
	if got.ID != id {
		t.Errorf("handler saw job %d, want %d", got.ID, id)
	}
	if string(got.Payload) != `{"item_id":"abc12345"}` {
		t.Errorf("payload = %q", got.Payload)
	}

	job, err := store.JobByID(ctx, id)
	if err != nil {
		t.Fatalf("JobByID: %v", err)
	}
	if job.Status != "done" {
		t.Errorf("Status = %q, want done", job.Status)
	}
}

func TestRunOnceOnAnIdleQueue(t *testing.T) {
	ran, err := jobs.New(dbtest.New(t), 1).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if ran {
		t.Error("RunOnce reported work on an empty queue")
	}
}

func TestFailingJobRetriesThenFails(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	queue := jobs.New(store, 1)

	calls := 0
	queue.Register(jobs.TypeProbe, func(context.Context, db.Job) error {
		calls++
		return errors.New("ffprobe is not installed")
	})

	id, err := queue.Enqueue(ctx, jobs.TypeProbe, map[string]string{})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	for attempt := 1; attempt <= jobs.MaxAttempts; attempt++ {
		// Move the clock past the backoff so the job is claimable again.
		queue.Now = func() time.Time { return time.Now().UTC().Add(time.Duration(attempt) * time.Hour) }
		ran, err := queue.RunOnce(ctx)
		if err != nil {
			t.Fatalf("RunOnce(attempt %d): %v", attempt, err)
		}
		if !ran {
			t.Fatalf("attempt %d found no work; the backoff did not expire", attempt)
		}
	}
	if calls != jobs.MaxAttempts {
		t.Errorf("handler ran %d times, want %d", calls, jobs.MaxAttempts)
	}

	job, err := store.JobByID(ctx, id)
	if err != nil {
		t.Fatalf("JobByID: %v", err)
	}
	if job.Status != "failed" {
		t.Errorf("Status = %q, want failed after exhausting the attempts", job.Status)
	}
	if job.Error != "ffprobe is not installed" {
		t.Errorf("Error = %q, want the cause surfaced for the retry button", job.Error)
	}

	// A permanently failed job must not be picked up again.
	queue.Now = func() time.Time { return time.Now().UTC().Add(99 * time.Hour) }
	if ran, _ := queue.RunOnce(ctx); ran {
		t.Error("a failed job was claimed again")
	}
}

func TestUnknownJobTypeFailsImmediately(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	queue := jobs.New(store, 1)
	// Nothing registered for TypeIngestURL: Plan 3 adds it.

	id, err := queue.Enqueue(ctx, jobs.TypeIngestURL, map[string]string{"url": "https://example.com"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := queue.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	job, err := store.JobByID(ctx, id)
	if err != nil {
		t.Fatalf("JobByID: %v", err)
	}
	if job.Status != "failed" {
		t.Errorf("Status = %q, want failed for an unhandled type", job.Status)
	}
	if job.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1: retrying an unhandled type is pointless", job.Attempts)
	}
}

func TestBackoffGrows(t *testing.T) {
	previous := time.Duration(0)
	for attempt := 1; attempt <= jobs.MaxAttempts; attempt++ {
		d := jobs.Backoff(attempt)
		if d <= previous {
			t.Errorf("Backoff(%d) = %v, not greater than Backoff(%d) = %v", attempt, d, attempt-1, previous)
		}
		previous = d
	}
	if jobs.Backoff(0) <= 0 {
		t.Error("Backoff(0) must still be positive")
	}
}

func TestEnqueueRejectsAnUnserialisablePayload(t *testing.T) {
	queue := jobs.New(dbtest.New(t), 1)
	if _, err := queue.Enqueue(context.Background(), jobs.TypeProbe, make(chan int)); err == nil {
		t.Fatal("Enqueue accepted a payload that cannot be marshalled")
	}
}

func TestRunOncePanicIsContainedAndRetried(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	queue := jobs.New(store, 1)

	calls := 0
	queue.Register(jobs.TypeProbe, func(context.Context, db.Job) error {
		calls++
		panic("ffprobe segfaulted")
	})

	id, err := queue.Enqueue(ctx, jobs.TypeProbe, map[string]string{})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// RunOnce must survive the panic and record it as a normal failure, not
	// crash the caller (i.e. the worker goroutine that would otherwise take
	// the whole pool down with it).
	ran, err := queue.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !ran {
		t.Fatal("RunOnce reported no work despite a queued job")
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1", calls)
	}

	job, err := store.JobByID(ctx, id)
	if err != nil {
		t.Fatalf("JobByID: %v", err)
	}
	if job.Status != "queued" {
		t.Errorf("Status = %q, want queued: a panic should be retried like any other failure", job.Status)
	}
	if job.Error == "" {
		t.Error("Error was not recorded for the panicking attempt")
	}
}

func TestStartAndStopDrainTheQueue(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	queue := jobs.New(store, 2)
	queue.PollInterval = 5 * time.Millisecond

	var (
		mu   sync.Mutex
		seen int
	)
	queue.Register(jobs.TypeProbe, func(context.Context, db.Job) error {
		mu.Lock()
		seen++
		mu.Unlock()
		return nil
	})

	for i := 0; i < 6; i++ {
		if _, err := queue.Enqueue(ctx, jobs.TypeProbe, map[string]int{"n": i}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	queue.Start(runCtx)

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		done := seen
		mu.Unlock()
		if done == 6 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	queue.Stop()

	mu.Lock()
	defer mu.Unlock()
	if seen != 6 {
		t.Errorf("workers processed %d jobs, want 6", seen)
	}
}

// TestCancelMidHandlerStillRecordsTheOutcome drives the exact documented
// shutdown sequence ("cancel the context passed to Start, then call Stop")
// while a handler is genuinely still in flight, not already finished. A
// worker that threads the same cancelled context into its outcome write
// deterministically fails that write (database/sql refuses to acquire a
// connection once ctx is done) and leaves the row stuck in "running"
// forever, since ClaimJob only ever selects "queued" rows.
func TestCancelMidHandlerStillRecordsTheOutcome(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	queue := jobs.New(store, 1)
	queue.PollInterval = 5 * time.Millisecond

	started := make(chan struct{})
	proceed := make(chan struct{})
	queue.Register(jobs.TypeProbe, func(context.Context, db.Job) error {
		close(started)
		<-proceed // genuinely in flight until the test releases it
		return nil
	})

	id, err := queue.Enqueue(ctx, jobs.TypeProbe, map[string]string{})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	queue.Start(runCtx)

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never started")
	}

	// Documented shutdown, step 1: cancel the context passed to Start
	// while the handler is still blocked inside it.
	cancel()
	// Only now does the handler return, exactly as the finding requires:
	// cancellation must land while it is genuinely still running.
	close(proceed)

	// Documented shutdown, step 2: call Stop. Must not hang and must not
	// silently strand the row.
	queue.Stop()

	job, err := store.JobByID(ctx, id)
	if err != nil {
		t.Fatalf("JobByID: %v", err)
	}
	if job.Status == "running" {
		t.Fatalf("Status = %q: cancelling mid-handler must not strand the row in running forever (only a restart + RecoverRunningJobs would ever pick it up again)", job.Status)
	}
	if job.Status != "done" {
		t.Errorf("Status = %q, want done: the handler succeeded, and its outcome must still be recorded despite the cancelled context", job.Status)
	}
}

// recordingHandler is a slog.Handler that captures records in memory so
// tests can assert on structured fields (message, attrs) instead of
// scraping formatted log text.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) has(message string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Message == message {
			return true
		}
	}
	return false
}

// TestWorkerLoopLogsAGenuinePersistenceFailureDuringShutdown proves the
// loop's shutdown-quieting only swallows RunOnce losing the race against
// ctx's own cancellation, not an unrelated persistence failure that happens
// to land while a shutdown is also in progress. The row is deleted out from
// under the in-flight handler (simulating a genuine, unrelated failure); the
// outcome write then fails with db.ErrNotFound, which has nothing to do
// with the cancellation racing it, and so must still be logged.
func TestWorkerLoopLogsAGenuinePersistenceFailureDuringShutdown(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	queue := jobs.New(store, 1)
	queue.PollInterval = 5 * time.Millisecond

	started := make(chan struct{})
	proceed := make(chan struct{})
	queue.Register(jobs.TypeProbe, func(context.Context, db.Job) error {
		close(started)
		<-proceed
		return nil
	})

	id, err := queue.Enqueue(ctx, jobs.TypeProbe, map[string]string{})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	handler := &recordingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	runCtx, cancel := context.WithCancel(ctx)
	queue.Start(runCtx)

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never started")
	}

	// An unrelated persistence failure: the row disappears while the
	// handler is still running. This has nothing to do with the
	// cancellation below.
	if _, err := store.DB.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, id); err != nil {
		t.Fatalf("delete job row: %v", err)
	}

	// Now a shutdown races it, exactly as in the previous test.
	cancel()
	close(proceed)
	queue.Stop()

	if !handler.has("job worker error") {
		t.Error("a persistence failure unrelated to the cancellation must still be logged, not swallowed just because a shutdown was also in progress")
	}
}
