package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"boobies-media/internal/db"
)

// ReapUploads deletes every expired upload's row and temp directory. It
// returns how many it actually reaped.
//
// A failure on one upload does not stop the pass: the point of a janitor is
// that it keeps making progress, and anything it misses is still expired on
// the next tick. ErrNotFound from DeleteUpload is expected, not an error: it
// means another actor (a client cancelling the same upload, or a previous
// janitor tick that removed the row but crashed before this run) already
// cleaned it up, so this pass still counts it as reaped rather than logging
// a spurious failure.
func (s *Server) ReapUploads(ctx context.Context) (int, error) {
	expired, err := s.Store.ExpiredUploads(ctx, s.Now())
	if err != nil {
		return 0, fmt.Errorf("web: janitor: listing expired uploads: %w", err)
	}
	reaped := 0
	for _, up := range expired {
		if err := os.RemoveAll(up.TempDir); err != nil {
			slog.Error("janitor: removing upload temp dir", "upload", up.ID, "err", err)
			continue
		}
		if err := s.Store.DeleteUpload(ctx, up.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
			slog.Error("janitor: deleting upload row", "upload", up.ID, "err", err)
			continue
		}
		reaped++
	}
	return reaped, nil
}

// StartUploadJanitor runs ReapUploads on a ticker until ctx is cancelled. It
// runs one pass immediately, so an abandoned upload left by a crash is
// cleaned up at the next boot rather than waiting a full interval.
//
// Call StopUploadJanitor after cancelling ctx to join the goroutine before
// tearing down anything it depends on (the database handle, in particular) --
// mirrors jobs.Queue's Start/Stop contract.
func (s *Server) StartUploadJanitor(ctx context.Context, every time.Duration) {
	s.janitorWG.Go(func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			s.reapOnce(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

// reapOnce runs one ReapUploads pass and logs its outcome. A pass can fail
// because ctx itself was cancelled: database/sql checks ctx.Done() before it
// will even acquire the store's connection, so a shutdown racing an
// in-flight ReapUploads reliably surfaces as a context-cancelled error here,
// not a broken reap. That is expected on a clean shutdown, not worth an
// ERROR, and safe to drop silently: nothing this pass missed is lost, since
// StartUploadJanitor already runs a fresh pass immediately on every boot.
//
// Any other failure -- a real store error unrelated to shutdown -- still
// logs ERROR exactly as before; only the cancellation-caused case is now
// distinguished from it.
func (s *Server) reapOnce(ctx context.Context) {
	n, err := s.ReapUploads(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Error("janitor: reaping uploads", "err", err)
		return
	}
	if n > 0 {
		slog.Info("janitor: reaped abandoned uploads", "count", n)
	}
}

// StopUploadJanitor waits for the upload janitor's goroutine to return.
// Cancel the context passed to StartUploadJanitor first, or this blocks --
// same contract as jobs.Queue.Stop.
func (s *Server) StopUploadJanitor() {
	s.janitorWG.Wait()
}
