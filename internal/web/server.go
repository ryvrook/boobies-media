package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"boobies-media/internal/auth"
	"boobies-media/internal/config"
	"boobies-media/internal/db"
	"boobies-media/internal/deps"
	"boobies-media/internal/jobs"
	"boobies-media/internal/media"
	webassets "boobies-media/web"
)

// Server owns the HTTP surface. It implements http.Handler, so tests can drive
// it through httptest without binding a port.
type Server struct {
	Cfg      *config.Config
	Store    *db.Store
	Renderer *Renderer
	Limiter  *auth.Limiter
	Deps     []deps.Status
	Now      func() time.Time

	// Media is the ingestion and blob store. Nil until wired with WithMedia.
	Media *media.Store
	// Queue schedules background work. Nil until wired with WithQueue.
	Queue *jobs.Queue
	// PublicLimiter throttles the unauthenticated media routes so a share link
	// posted somewhere busy cannot saturate a home uplink.
	PublicLimiter *auth.Limiter

	router chi.Router
	// janitorWG tracks the upload janitor's goroutine, so StopUploadJanitor
	// can join it the same way jobs.Queue's Stop joins its workers.
	janitorWG sync.WaitGroup

	// completedUploads remembers recent upload completions so a retried
	// POST /api/uploads/{id}/complete is answered idempotently (the same
	// item, not a second row) instead of racing Save a second time or
	// 404ing on a row this handler already deleted. See handlers_uploads.go.
	// Best-effort and process-local: entries are swept on every insert so an
	// id nobody ever retries does not sit in the map for the life of the
	// process; a live entry only needs to outlive a client's realistic retry
	// window since the upload row and temp bytes behind it are already gone.
	completedUploads   map[string]completedUpload
	completedUploadsMu sync.Mutex

	// uploadClaims tracks which upload ids are currently being completed, so
	// two concurrent POST /api/uploads/{id}/complete calls for the same id
	// cannot both run media.Save. This replaces an earlier design that used
	// DeleteUpload itself as the claim (deleting the row before Save ran):
	// that made a Save failure destroy the row and every staged chunk with
	// no way to retry a multi-gigabyte upload. Claiming in-process instead
	// means the row and chunks only ever get deleted after Save actually
	// succeeds, so a transient failure stays retryable. See
	// handlers_uploads.go's claimUpload/releaseUpload.
	uploadClaims   map[string]bool
	uploadClaimsMu sync.Mutex
}

// Public route throttling: generous enough for a page of thumbnails, tight
// enough that one client cannot monopolise a home connection.
const (
	PublicRateLimit  = 240
	PublicRateWindow = time.Minute
)

// Option customises the server. Options exist so the Plan 1 constructor call
// shape keeps working while later plans add collaborators.
type Option func(*Server)

// WithMedia attaches the media store, enabling uploads and media serving.
func WithMedia(m *media.Store) Option {
	return func(s *Server) { s.Media = m }
}

// WithQueue attaches the job queue.
func WithQueue(q *jobs.Queue) Option {
	return func(s *Server) { s.Queue = q }
}

// New builds the server and its routes. depStatus comes from deps.Probe and is
// surfaced in the admin banner in a later plan.
func New(cfg *config.Config, store *db.Store, depStatus []deps.Status, opts ...Option) (*Server, error) {
	renderer, err := NewRenderer()
	if err != nil {
		return nil, err
	}
	staticFS, err := webassets.StaticFS()
	if err != nil {
		return nil, fmt.Errorf("web: open static assets: %w", err)
	}

	s := &Server{
		Cfg:      cfg,
		Store:    store,
		Renderer: renderer,
		Limiter:  auth.NewLimiter(auth.LoginAttemptLimit, auth.LoginAttemptWindow),
		Deps:     depStatus,
		Now:      func() time.Time { return time.Now().UTC() },
	}
	s.PublicLimiter = auth.NewLimiter(PublicRateLimit, PublicRateWindow)
	for _, opt := range opts {
		opt(s)
	}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	// CleanPath must run before the gate so "/s/../admin" is normalised to
	// "/admin" and evaluated as the private path it really is.
	r.Use(middleware.CleanPath)
	r.Use(s.LoadUser)
	r.Use(s.Gate)

	fileServer := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))
	r.Handle("/static/*", fileServer)
	r.Get("/robots.txt", func(w http.ResponseWriter, req *http.Request) {
		http.ServeFileFS(w, req, staticFS, "robots.txt")
	})
	r.Get("/favicon.ico", func(w http.ResponseWriter, req *http.Request) {
		http.ServeFileFS(w, req, staticFS, "favicon.ico")
	})
	r.Get("/login", s.handleLoginForm)
	r.Post("/login", s.handleLoginSubmit)
	r.Post("/logout", s.handleLogout)
	r.Get("/", s.handleBrowse)
	r.Get("/m/{id}", s.handleRawMedia)
	r.Get("/t/{id}", s.handleThumbnail)
	r.Get("/p/{id}", s.handleSocialPreview)
	r.Get("/s/{id}", s.handleEmbed)
	r.Post("/api/ingest", s.handleIngest)
	r.Post("/api/uploads", s.handleUploadInit)
	r.Get("/api/uploads/{id}", s.handleUploadStatus)
	r.Put("/api/uploads/{id}/{index}", s.handleUploadChunk)
	r.Post("/api/uploads/{id}/complete", s.handleUploadComplete)
	r.Delete("/api/uploads/{id}", s.handleUploadCancel)
	r.Get("/api/items", s.handleListItems)
	r.Post("/api/items/batch", s.handleBatchItems)
	r.Get("/api/items/{id}", s.handleGetItem)
	r.Patch("/api/items/{id}", s.handlePatchItem)
	r.Delete("/api/items/{id}", s.handleDeleteItem)
	r.Post("/api/items/{id}/tags", s.handleAddItemTag)
	r.Delete("/api/items/{id}/tags/{tag}", s.handleRemoveItemTag)
	r.Get("/api/tags", s.handleListTags)
	r.Get("/api/random", s.handleRandomItem)
	r.Get("/api/jobs/{id}", s.handleJobStatus)
	r.Get("/api/folders", s.handleListFolders)
	r.Post("/api/folders", s.handleCreateFolder)
	r.Patch("/api/folders/{id}", s.handleUpdateFolder)
	r.Delete("/api/folders/{id}", s.handleDeleteFolder)
	r.With(s.requireAdmin).Get("/admin", s.handleAdmin)

	r.With(s.requireAdmin).Post("/api/admin/users", s.handleCreateUser)
	r.With(s.requireAdmin).Patch("/api/admin/users/{id}", s.handleUpdateUser)
	r.With(s.requireAdmin).Delete("/api/admin/users/{id}", s.handleDeleteUser)
	r.With(s.requireAdmin).Post("/api/admin/users/{id}/password", s.handleResetPassword)
	r.With(s.requireAdmin).Post("/api/admin/users/{id}/apikey", s.handleRotateKey)

	r.With(s.requireAdmin).Post("/api/admin/settings", s.handleSaveSettings)
	r.With(s.requireAdmin).Post("/api/admin/test-ingest", s.handleTestIngest)

	r.With(s.requireAdmin).Post("/api/jobs/{id}/retry", s.handleRetryJob)
	r.With(s.requireAdmin).Post("/api/admin/items/{id}/restore", s.handleRestoreItem)
	r.With(s.requireAdmin).Delete("/api/admin/items/{id}/purge", s.handlePurgeItem)

	s.router = r
	return s, nil
}

// ServeHTTP makes Server an http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// serverError logs the real cause and shows the visitor something generic.
// Internal error strings never reach a response body.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "something went wrong")
		return
	}
	http.Error(w, "Something went wrong. Check the server logs.", http.StatusInternalServerError)
}
