// Package jobs is the background-work runtime. It knows how to claim, retry
// and dispatch jobs; it knows nothing about media, ingestion or HTTP. Handlers
// are registered by the packages that own the work.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"boobies-media/internal/db"
)

// Job type identifiers. These must match the CHECK constraint on jobs.type.
const (
	TypeIngestURL  = "ingest_url"
	TypeThumbnail  = "thumbnail"
	TypeProbe      = "probe"
	TypeFolderMove = "folder_move"
)

// MaxAttempts is how many times a job runs before it is marked failed. The UI
// shows the recorded error with a retry button.
const MaxAttempts = 3

// DefaultPollInterval is how long an idle worker waits before looking again.
const DefaultPollInterval = 500 * time.Millisecond

// Handler performs one job. Returning an error triggers backoff and retry.
type Handler func(ctx context.Context, job db.Job) error

// Backoff is the delay before a job's next attempt.
func Backoff(attempts int) time.Duration {
	switch {
	case attempts <= 0:
		return 5 * time.Second
	case attempts == 1:
		return 10 * time.Second
	case attempts == 2:
		return time.Minute
	default:
		return 5 * time.Minute
	}
}

// Queue claims jobs from the database and dispatches them to handlers.
type Queue struct {
	Store        *db.Store
	Workers      int
	PollInterval time.Duration
	// Now is injectable so tests can advance past a backoff without sleeping.
	Now func() time.Time

	mu       sync.RWMutex
	handlers map[string]Handler
	wg       sync.WaitGroup
}

// New builds a queue. workers below 1 is clamped to 1.
func New(store *db.Store, workers int) *Queue {
	if workers < 1 {
		workers = 1
	}
	return &Queue{
		Store:        store,
		Workers:      workers,
		PollInterval: DefaultPollInterval,
		Now:          func() time.Time { return time.Now().UTC() },
		handlers:     make(map[string]Handler),
	}
}

// Register attaches a handler to a job type. Registering twice replaces the
// previous handler.
func (q *Queue) Register(jobType string, h Handler) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers[jobType] = h
}

func (q *Queue) handlerFor(jobType string) (Handler, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	h, ok := q.handlers[jobType]
	return h, ok
}

// Enqueue serialises payload as JSON and adds a job runnable immediately.
func (q *Queue) Enqueue(ctx context.Context, jobType string, payload any) (int64, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("jobs: encode %s payload: %w", jobType, err)
	}
	return q.Store.EnqueueJob(ctx, jobType, encoded, q.Now())
}

// RunOnce claims and runs at most one job. It reports whether work was found.
// This is the deterministic primitive the tests drive; workers just loop on
// it, and a handler panic is contained so it cannot kill the pool.
func (q *Queue) RunOnce(ctx context.Context) (ran bool, err error) {
	job, err := q.Store.ClaimJob(ctx, q.Now())
	if errors.Is(err, db.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	handler, ok := q.handlerFor(job.Type)
	if !ok {
		// Retrying will not conjure a handler, so fail immediately.
		cause := fmt.Sprintf("no handler registered for job type %q", job.Type)
		slog.Error("job has no handler", "job", job.ID, "type", job.Type)
		writeCtx, cancel := outcomeContext(ctx)
		defer cancel()
		if err := q.Store.FailJob(writeCtx, job.ID, cause); err != nil {
			return true, err
		}
		return true, nil
	}

	// The handler itself still sees ctx and must react to cancellation, but
	// recording its outcome must not: Stop's documented contract is "cancel
	// the context passed to Start, then call Stop", so ctx is routinely
	// already canceled by the time a handler returns. A write on that same
	// ctx would deterministically fail with context.Canceled (database/sql
	// checks ctx.Done() before it will even acquire a connection) and leave
	// the row stuck in "running" forever, since ClaimJob only ever selects
	// "queued" rows.
	runErr := q.runHandler(ctx, handler, *job)

	writeCtx, cancel := outcomeContext(ctx)
	defer cancel()

	if runErr != nil {
		cause := runErr.Error()
		if job.Attempts >= MaxAttempts {
			slog.Error("job failed permanently", "job", job.ID, "type", job.Type, "attempts", job.Attempts, "err", runErr)
			return true, q.Store.FailJob(writeCtx, job.ID, cause)
		}
		retryAt := q.Now().Add(Backoff(job.Attempts))
		slog.Warn("job failed, will retry", "job", job.ID, "type", job.Type, "attempt", job.Attempts, "retry_at", retryAt, "err", runErr)
		return true, q.Store.RetryJob(writeCtx, job.ID, retryAt, cause)
	}
	return true, q.Store.CompleteJob(writeCtx, job.ID)
}

// outcomeWriteTimeout bounds a job-outcome write once it has been detached
// from ctx's cancellation (see outcomeContext), so a wedged store cannot
// hang a shutdown forever. It matches the busy_timeout the store's single
// SQLite connection is opened with (see db.Open), giving the write the same
// window SQLite itself is already configured to wait out lock contention.
const outcomeWriteTimeout = 5 * time.Second

// outcomeContext derives a context for recording a job's outcome (complete,
// retry, or fail) that survives cancellation of ctx.
func outcomeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), outcomeWriteTimeout)
}

// runHandler invokes h and converts a panic into an error, so one broken
// handler cannot take down the worker pool.
func (q *Queue) runHandler(ctx context.Context, h Handler, job db.Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("jobs: handler for %q panicked: %v", job.Type, r)
		}
	}()
	return h(ctx, job)
}

// Start launches the worker goroutines. They stop when ctx is cancelled.
func (q *Queue) Start(ctx context.Context) {
	for i := 0; i < q.Workers; i++ {
		q.wg.Add(1)
		go func(worker int) {
			defer q.wg.Done()
			q.loop(ctx, worker)
		}(i)
	}
}

func (q *Queue) loop(ctx context.Context, worker int) {
	interval := q.PollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		ran, err := q.RunOnce(ctx)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil && errors.Is(err, cerr) {
				// Shutting down and RunOnce's error IS ctx's own
				// cancellation (e.g. ClaimJob lost the race mid-query).
				// Job-outcome writes are recorded on a context detached
				// from ctx (see outcomeContext), so they never produce
				// this error; anything else here is a real failure and
				// falls through to be logged below.
				return
			}
			slog.Error("job worker error", "worker", worker, "err", err)
		}
		if ran && err == nil {
			continue // drain greedily while there is work
		}

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(interval)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

// Stop waits for the workers to finish. Cancel the context passed to Start
// first, or this blocks.
func (q *Queue) Stop() { q.wg.Wait() }
