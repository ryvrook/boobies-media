// Command server runs the boobies-media HTTP server, or its `user`
// administration subcommands.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boobies-media/internal/backup"
	"boobies-media/internal/config"
	"boobies-media/internal/db"
	"boobies-media/internal/deps"
	"boobies-media/internal/ingest"
	"boobies-media/internal/jobs"
	"boobies-media/internal/media"
	"boobies-media/internal/usercli"
	"boobies-media/internal/web"
)

// backupRetain is how many nightly snapshots survive pruning.
const backupRetain = 7

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "user" {
		return runUserCommand(args)
	}
	return runServer(args)
}

// runUserCommand handles `server user ...`. It opens the database with the
// same configuration the server would use.
func runUserCommand(args []string) error {
	cfg, err := config.Load(nil, os.Getenv)
	if err != nil {
		return err
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()

	return usercli.Run(context.Background(), store, args, os.Stdin, os.Stdout, usercli.PromptPassword)
}

func runServer(args []string) error {
	cfg, err := config.Load(args, os.Getenv)
	if err != nil {
		return err
	}

	// Hard fail: without a writable data directory there is nothing to serve.
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	// Hard fail: without the database there is no catalog.
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()

	// Soft fail: a missing external tool degrades ingestion but must never
	// stop the server serving the library that is already on disk.
	depStatus := deps.Probe(context.Background(), deps.Required)
	for _, status := range depStatus {
		if status.OK {
			slog.Info("dependency ready", "tool", status.Name, "version", status.Version)
			continue
		}
		slog.Warn("dependency unavailable; related features will fail with a clear message",
			"tool", status.Name, "detail", status.Err)
	}

	// A crash leaves jobs stranded in 'running'; put them back in the queue.
	recovered, err := store.RecoverRunningJobs(context.Background())
	if err != nil {
		return err
	}
	if recovered > 0 {
		slog.Info("re-queued jobs left running by a previous crash", "count", recovered)
	}

	// Background work: probe and thumbnail handlers live in the media package;
	// Plan 3 registers the ingest_url handler the same way.
	queue := jobs.New(store, cfg.Workers)
	mediaStore := media.NewStore(cfg, store, queue)
	mediaStore.RegisterHandlers(queue)
	ingestor := ingest.NewIngestor(cfg, store, mediaStore)
	queue.Register(jobs.TypeIngestURL, ingestor.Handle)

	srv, err := web.New(cfg, store, depStatus, web.WithMedia(mediaStore), web.WithQueue(queue))
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: large media responses in later plans stream for
		// longer than any fixed deadline would allow.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	queue.Start(ctx)
	slog.Info("job workers started", "workers", cfg.Workers)

	// Same signal context, so a shutdown stops the janitor too. The first pass
	// runs immediately, which is what cleans up chunks orphaned by a crash.
	srv.StartUploadJanitor(ctx, time.Hour)

	// Nightly VACUUM INTO snapshot, kept 7 deep. Same signal context and
	// Start/Stop-with-WaitGroup shape as the queue and the janitor above, so
	// shutdown joins it the same way instead of leaking the goroutine.
	backupRunner := backup.NewRunner(store, cfg.BackupsDir(), backupRetain, 24*time.Hour)
	backupRunner.Start(ctx)

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr, "base_url", cfg.BaseURL, "data_dir", cfg.DataDir)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	var runErr error
	select {
	case runErr = <-errCh:
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		runErr = httpServer.Shutdown(shutdownCtx)
	}

	// ctx is already cancelled on the signal path; cancel it explicitly on the
	// error path so the workers stop either way.
	stop()
	queue.Stop()
	slog.Info("job workers stopped")
	// Join the janitor too, so a sweep in flight cannot outlive the database
	// handle the deferred store.Close() above is about to close.
	srv.StopUploadJanitor()
	slog.Info("upload janitor stopped")
	// Join the backup runner for the same reason: an in-flight VACUUM INTO
	// must finish (or be cut off by its own cancelled context) before
	// store.Close() runs.
	backupRunner.Stop()
	slog.Info("backup runner stopped")
	return runErr
}
