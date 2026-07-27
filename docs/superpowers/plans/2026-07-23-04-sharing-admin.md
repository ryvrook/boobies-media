# Sharing, Admin and Polish Implementation Plan (Plan 4 of 4)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the loop on the friend-group media server — share links that embed correctly in Discord, a folder tree and tag chips over the catalog, a bulk-select toolbar, a random-item endpoint for the future bot, a complete admin surface (users, settings, job queue with retry, dependency banner, trash, per-source test-ingest), a nightly SQLite backup, and the deployment notes.

**Architecture:** No new domain packages beyond one small `internal/backup`. Everything else is thin HTTP handlers over stores that Plans 1–3 already built and tested, a standalone `/s/{id}` embed template rendered outside the base layout so it can carry per-item OpenGraph markup, dependency-free vanilla-TypeScript islands mounted the same way as Plan 2's grid/lightbox/uploader, and a backup goroutine on the same signal context that already runs the job queue. The admin surface is gated server-side by a `requireAdmin` middleware; it never relies on the UI hiding a link.

**Tech Stack:** Everything from Plans 1–3. No new Go module dependencies — `VACUUM INTO` is plain SQL and the backup clock is injected as a `time.Time`. Islands stay vanilla TS bundled by Bun.

## Global Constraints

Plans 1, 2 and 3's Global Constraints hold in full. These add to them.

- **Admin routes are gated server-side.** `/admin` and every `/api/admin/*` route pass through `requireAdmin`, which 403s a non-admin **regardless of what the UI shows**. A hidden nav link is never the access control.
- **The embed page is anonymous but honors revocation.** `/s/{id}` requires no session, but a `share_revoked` or soft-deleted item 404s exactly like `/m/` and `/t/` already do. All URLs in its markup are absolute, built from `config.BaseURL`; `og:video:secure_url` is forced to `https`. `Referrer-Policy: no-referrer` so a share id never leaks through the source-link anchor.
- **Golden-file discipline for embed markup:** every OpenGraph / Twitter-card tag is asserted per case (image, mp4 video, revoked, webm fallback), not merely "some markup exists".
- **JS budget < 50 KB total** for the whole bundle, still. The new islands (folders, bulk-select, admin, copy, plus the folder-move addition to the lightbox) are vanilla TypeScript with no dependencies.
- **No test may touch the network.** HTTP is `httptest`; external tools are stubbed with Plan 2's `media.StubTools`. The backup timer is tested by calling its `RunOnce` directly with an injected timestamp — no `time.Sleep` polling.
- **TDD throughout**; commit at the end of every task.

## Consumed Plan 1 + 2 + 3 interfaces (verbatim)

Do not re-derive these — they exist and are tested.

```go
// internal/config
type Config struct { Addr, DataDir, BaseURL string; Workers int; SecureCookies bool }
func (c *Config) DBPath() string   // FilesDir, ThumbsDir, AvatarsDir, BackupsDir, CookiesDir, TmpDir likewise
func (c *Config) EnsureDirs() error

// internal/auth
func HashPassword(password string) (string, error)
func NewAPIKey() (string, error)     // returns the plaintext key
func HashToken(token string) string  // SHA-256 hex; api-key and session hashing
func NewItemID() (string, error)     // 8-char base58

// internal/db
type Store struct{ DB *sql.DB }
var ErrNotFound, ErrForbidden, ErrDuplicateUser, ErrFolderCycle error
type User struct { ID int64; Username, DisplayName, AvatarHash, PasswordHash, APIKeyHash string; IsAdmin bool; CreatedAt time.Time }
type Item struct {
	ID, ContentHash, Title, Ext, Mime string
	Size, Width, Height int64
	Duration float64
	UploaderID, FolderID, JobID int64
	SourceURL string
	ShareRevoked bool
	DeletedAt, CreatedAt time.Time
}
func (i *Item) IsPubliclyServable() bool
type Folder struct { ID, ParentID int64; Name string; CreatedAt time.Time }
type Job struct { ID int64; Type string; Payload []byte; Status string; Attempts int; Error string; NextAttemptAt, CreatedAt time.Time }
type ItemQuery struct { FolderID *int64; Tag string; UploaderID int64; Query string; Sort ItemSort; Limit int; Cursor string }
type ItemSort int
func ParseItemSort(s string) (ItemSort, error)
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error)
func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error)
func (s *Store) CreateUser(ctx context.Context, username, displayName, passwordHash, apiKeyHash string, isAdmin bool) (*User, error)
func (s *Store) ListUsers(ctx context.Context) ([]*User, error)
func (s *Store) SetUserPassword(ctx context.Context, id int64, passwordHash string) error   // also deletes that user's sessions
func (s *Store) SetUserAPIKeyHash(ctx context.Context, id int64, apiKeyHash string) error   // "" removes the key
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error
func (s *Store) ListItems(ctx context.Context, q ItemQuery) ([]*Item, string, error)
func (s *Store) ItemByID(ctx context.Context, id string) (*Item, error)                      // live items only
func (s *Store) SoftDeleteItem(ctx context.Context, id string, actor *User) error
func (s *Store) MoveItem(ctx context.Context, id string, folderID int64) error
func (s *Store) AddItemTag(ctx context.Context, itemID, name string) error
func (s *Store) ItemTags(ctx context.Context, itemID string) ([]string, error)
func (s *Store) ListDeletedItems(ctx context.Context, limit int) ([]*Item, error)
func (s *Store) RestoreItem(ctx context.Context, id string) error
func (s *Store) CreateFolder(ctx context.Context, parentID int64, name string) (*Folder, error)
func (s *Store) FolderByID(ctx context.Context, id int64) (*Folder, error)
func (s *Store) ListFolders(ctx context.Context) ([]*Folder, error)
func (s *Store) RenameFolder(ctx context.Context, id int64, name string) error
func (s *Store) MoveFolder(ctx context.Context, id, newParentID int64) error
func (s *Store) DeleteFolder(ctx context.Context, id int64) error
func (s *Store) ListTags(ctx context.Context) ([]string, error)
func (s *Store) JobByID(ctx context.Context, id int64) (*Job, error)
func (s *Store) SettingAll(ctx context.Context) (map[string]string, error)
func (s *Store) SettingSet(ctx context.Context, key, value string) error
func NormalizeFolderName(s string) (string, error)

// internal/jobs
func New(store *db.Store, workers int) *Queue
func (q *Queue) Register(jobType string, h Handler)
func (q *Queue) Enqueue(ctx context.Context, jobType string, payload any) (int64, error)
const TypeIngestURL = "ingest_url"

// internal/ingest
type URLJob struct { URL string `json:"url"`; UploaderID int64 `json:"uploader_id"` }
var Extractors []string // {"twitter","youtube","tiktok","medal"}

// internal/media
type Store struct{ /* … */ }
func (s *Store) Purge(ctx context.Context, itemID string) error   // db purge + blob/thumb unlink, refcount-safe
func IsVideoMime(mime string) bool
func StubTools(t *testing.T, scripts map[string]string) string
type SaveRequest struct { Reader io.Reader; Filename string; UploaderID, FolderID int64; SourceURL string; JobID, MaxBytes int64 }
func (s *Store) Save(ctx context.Context, req SaveRequest) (*SaveResult, error)

// internal/deps
type Status struct { Name, Path, Version string; OK bool; Err string }

// internal/web
type Server struct { Cfg *config.Config; Store *db.Store; Renderer *Renderer; Deps []deps.Status; Media *media.Store; Queue *jobs.Queue; PublicLimiter *auth.Limiter; Now func() time.Time; /* … */ }
type Option func(*Server)
func CurrentUser(r *http.Request) (*db.User, bool)
func clientIP(r *http.Request) string
const SessionCookieName = "bm_session"
func writeJSON(w http.ResponseWriter, status int, payload any)
func writeJSONError(w http.ResponseWriter, status int, code, message string)
func itemJSON(item *db.Item, tags []string, baseURL string) map[string]any
func (s *Server) itemsPayload(r *http.Request, items []*db.Item) ([]map[string]any, error)
func (s *Server) listItemQuery(r *http.Request) (db.ItemQuery, error)
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error)
func (s *Server) requireMedia(w http.ResponseWriter, r *http.Request) bool
func (s *Server) requireQueue(w http.ResponseWriter, r *http.Request) bool
type Renderer struct{ /* pages map + embed template */ }
func NewRenderer() (*Renderer, error)
type PageData struct { Title string; User *db.User; Error, Next string; Data any }
func (r *Renderer) Render(w http.ResponseWriter, status int, page string, data PageData) error

// internal/dbtest
func New(t *testing.T) *db.Store

// web/src/main.ts
export function registerIsland(name: string, factory: (el: HTMLElement) => void): void
// web/src/islands/grid.ts
export interface ApiItem { id, title, mime: string; is_video, ready: boolean; share_url, media_url, thumb_url, source_url: string; tags: string[] }
export function renderTile(item: ApiItem): HTMLElement
```

## File Structure

```
internal/db/random.go                    RandomItem for the bot API
internal/db/random_test.go
internal/db/jobs_admin.go                ListJobs + RequeueJob (manual retry)
internal/db/jobs_admin_test.go
internal/db/users_admin.go               DeleteUser (item-guarded) + SetUserAdmin + CountItemsByUploader
internal/db/users_admin_test.go
internal/db/backup.go                    (*Store) BackupTo — VACUUM INTO one path
internal/db/backup_test.go
internal/backup/backup.go                RunOnce (dated backup + retention) + Every loop
internal/backup/backup_test.go
internal/web/handlers_embed.go           GET /s/{id}
internal/web/handlers_embed_test.go
internal/db/items.go                     (modified) SetItemMimeForTest (test-only mime override)
internal/web/handlers_folders.go         folder JSON CRUD
internal/web/handlers_folders_test.go
internal/web/handlers_tags.go            GET /api/tags, GET /api/random
internal/web/handlers_tags_test.go
internal/web/handlers_batch.go           POST /api/items/batch
internal/web/handlers_batch_test.go
internal/web/handlers_admin.go           GET /admin dashboard page
internal/web/handlers_admin_users.go     user create/delete/admin-toggle/password/apikey
internal/web/handlers_admin_settings.go  settings save + per-source test ingest
internal/web/handlers_admin_jobs.go      job retry + trash restore/purge
internal/web/handlers_admin_test.go
internal/web/handlers_admin_users_test.go
internal/web/handlers_admin_settings_test.go
internal/web/handlers_admin_jobs_test.go
internal/web/middleware_admin.go         requireAdmin middleware
internal/web/server.go                   (modified) route registrations, Renderer embed field
internal/web/render.go                   (modified) NewRenderer parses templates/embed.html
web/templates/embed.html                 standalone OpenGraph/Twitter embed document
web/templates/pages/admin.html           admin page
web/templates/base.html                  (modified) admin nav link
web/templates/pages/browse.html          (modified) folder sidebar, tag chips, bulk toolbar
web/src/islands/copy.ts                   embed copy-link button
web/src/islands/folders.ts               folder tree sidebar
web/src/islands/tagfilter.ts             tag filter chips
web/src/islands/bulkselect.ts            multi-select toolbar
web/src/islands/admin.ts                  admin async actions
web/src/islands/lightbox.ts              (modified) folder-move select
web/src/main.ts                          (modified) island registrations
web/src/main.css                         (modified) sidebar/chips/toolbar/admin/embed styles
cmd/server/main.go                       (modified) backup goroutine
docs/deploy.md                           deployment notes
```

---

### Task 1: The `/s/{id}` embed page

**Files:**
- Create: `internal/web/handlers_embed.go`
- Modify: `internal/web/render.go` (parse `templates/embed.html`, add `RenderEmbed`)
- Modify: `internal/web/server.go` (register `GET /s/{id}`)
- Modify: `internal/db/items.go` (add `SetItemMimeForTest`)
- Create: `web/templates/embed.html`
- Test: `internal/web/handlers_embed_test.go`

**Interfaces:**
- Consumes: `db.ItemByID`, `db.UserByID`, `(*Item).IsPubliclyServable`, `db.ErrNotFound`, `media.IsVideoMime`, `clientIP`, `s.PublicLimiter`, `s.serverError` (Plans 1–2); `config.BaseURL`
- Produces:
  - `(*Server) handleEmbed(w http.ResponseWriter, r *http.Request)` on `GET /s/{id}`
  - `embedData` struct (the template model)
  - `(*Renderer) RenderEmbed(w http.ResponseWriter, status int, data embedData) error`

**Why a standalone template, not the `pages/` set.** Every `pages/*.html` is rendered inside `base.html`, whose `<head>` is fixed. The embed document needs per-item OpenGraph/Twitter tags in the head that Discord's crawler reads, so it is parsed separately and rendered directly — the human-facing body (media, title, uploader, source link, copy button) lives in the same document, exactly as the spec's "one document for all clients" requires.

- [ ] **Step 1: Write the failing test**

Create `internal/web/handlers_embed_test.go`:

```go
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// embedBody fetches /s/{id} anonymously (no cookie) and returns the recorder.
func embedBody(t *testing.T, srv *Server, id string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/s/"+id, nil))
	return rec
}

func TestEmbedImageHasEveryOpenGraphTag(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	srv.Cfg.BaseURL = "https://media.example.com"
	item := storeBlob(t, srv, mediaStore, pngTestBytes, "kitten.png")
	if err := srv.Store.SetItemTitle(ctx, item.ID, "Kitten"); err != nil {
		t.Fatalf("SetItemTitle: %v", err)
	}
	if err := srv.Store.SetItemProbe(ctx, item.ID, 640, 480, 0); err != nil {
		t.Fatalf("SetItemProbe: %v", err)
	}

	rec := embedBody(t, srv, item.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	body := rec.Body.String()
	want := []string{
		`<meta property="og:type" content="website">`,
		`<meta property="og:title" content="Kitten">`,
		`<meta property="og:url" content="https://media.example.com/s/` + item.ID + `">`,
		`<meta property="og:image" content="https://media.example.com/m/` + item.ID + `">`,
		`<meta property="og:image:width" content="640">`,
		`<meta property="og:image:height" content="480">`,
		`<meta property="og:image:type" content="image/png">`,
		`<meta name="twitter:card" content="summary_large_image">`,
	}
	for _, tag := range want {
		if !strings.Contains(body, tag) {
			t.Errorf("image embed is missing tag:\n%s", tag)
		}
	}
	if strings.Contains(body, "og:video") {
		t.Error("an image item emitted an og:video tag")
	}
}

func TestEmbedVideoHasVideoAndPosterTags(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	srv.Cfg.BaseURL = "https://media.example.com"
	item := storeBlob(t, srv, mediaStore, testMP4, "clip.mp4")
	if err := srv.Store.SetItemTitle(ctx, item.ID, "Clip"); err != nil {
		t.Fatalf("SetItemTitle: %v", err)
	}
	if err := srv.Store.SetItemProbe(ctx, item.ID, 1280, 720, 12.5); err != nil {
		t.Fatalf("SetItemProbe: %v", err)
	}

	body := embedBody(t, srv, item.ID).Body.String()
	want := []string{
		`<meta property="og:type" content="video.other">`,
		`<meta property="og:video:secure_url" content="https://media.example.com/m/` + item.ID + `">`,
		`<meta property="og:video:type" content="video/mp4">`,
		`<meta property="og:video:width" content="1280">`,
		`<meta property="og:video:height" content="720">`,
		`<meta property="og:image" content="https://media.example.com/t/` + item.ID + `?s=1024">`,
		`<meta name="twitter:card" content="summary_large_image">`,
	}
	for _, tag := range want {
		if !strings.Contains(body, tag) {
			t.Errorf("video embed is missing tag:\n%s", tag)
		}
	}
}

func TestEmbedWebmFallsBackToImageCard(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	srv.Cfg.BaseURL = "https://media.example.com"
	// A webm blob: sniffed as video/webm by the pipeline stub path would need a
	// real webm header, so store the item row directly with a webm mime.
	item := storeBlob(t, srv, mediaStore, testMP4, "old.mp4")
	if err := srv.Store.SetItemMimeForTest(ctx, item.ID, "video/webm"); err != nil {
		t.Fatalf("SetItemMimeForTest: %v", err)
	}
	body := embedBody(t, srv, item.ID).Body.String()
	if strings.Contains(body, "og:video") {
		t.Error("a webm item emitted og:video tags; it must fall back to an image card")
	}
	if !strings.Contains(body, `<meta property="og:image" content="https://media.example.com/t/`+item.ID+`?s=1024">`) {
		t.Error("the webm fallback did not emit a poster og:image")
	}
}

func TestEmbedRevokedAndDeletedReturn404(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	revoked := storeBlob(t, srv, mediaStore, pngTestBytes, "a.png")
	if err := srv.Store.SetItemShareRevoked(ctx, revoked.ID, true); err != nil {
		t.Fatalf("SetItemShareRevoked: %v", err)
	}
	if rec := embedBody(t, srv, revoked.ID); rec.Code != http.StatusNotFound {
		t.Errorf("revoked embed status = %d, want 404", rec.Code)
	}

	deleted := storeBlob(t, srv, mediaStore, append(append([]byte{}, pngTestBytes...), 'x'), "b.png")
	owner, _ := srv.Store.UserByUsername(ctx, "aiden")
	if err := srv.Store.SoftDeleteItem(ctx, deleted.ID, owner); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}
	if rec := embedBody(t, srv, deleted.ID); rec.Code != http.StatusNotFound {
		t.Errorf("deleted embed status = %d, want 404", rec.Code)
	}

	if rec := embedBody(t, srv, "nosuchid0"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown embed status = %d, want 404", rec.Code)
	}
}

func TestEmbedEscapesTitle(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	item := storeBlob(t, srv, mediaStore, pngTestBytes, "x.png")
	if err := srv.Store.SetItemTitle(ctx, item.ID, `"><script>alert(1)</script>`); err != nil {
		t.Fatalf("SetItemTitle: %v", err)
	}
	body := embedBody(t, srv, item.ID).Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("the embed rendered a title unescaped")
	}
}
```

The `storeBlob` helper (Plan 2, Task 13) always uploads a real PNG, so the video and webm tests need to override the stored mime after the fact. That means one small **test-only** helper.

- [ ] **Step 2: Add the test-only mime setter and probe/title/revoke helpers already exist**

`SetItemProbe`, `SetItemTitle`, `SetItemShareRevoked` were built and tested in Plan 2. Only a mime override is missing; add it to `internal/db/items.go` (it is used only by this plan's embed test and is named to make that obvious):

```go
// SetItemMimeForTest overrides the stored mime. It exists so tests can exercise
// the webm embed fallback without a real webm blob; production never calls it.
func (s *Store) SetItemMimeForTest(ctx context.Context, id, mime string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE items SET mime = ? WHERE id = ?`, mime, id)
	return requireItemRows(res, err, "set item mime")
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/web/ -run Embed -v`
Expected: FAIL to compile — `undefined: (*Server).handleEmbed`, `undefined: (*Renderer).RenderEmbed`, and no `/s/{id}` route.

- [ ] **Step 4: Add the embed template**

Create `web/templates/embed.html`:

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · boobies-media</title>
<meta property="og:site_name" content="boobies-media">
<meta property="og:title" content="{{.Title}}">
<meta property="og:url" content="{{.ShareURL}}">
{{if .IsVideoEmbed}}
<meta property="og:type" content="video.other">
<meta property="og:video:secure_url" content="{{.SecureMediaURL}}">
<meta property="og:video:type" content="{{.Mime}}">
<meta property="og:video:width" content="{{.Width}}">
<meta property="og:video:height" content="{{.Height}}">
<meta property="og:image" content="{{.PosterURL}}">
<meta name="twitter:card" content="summary_large_image">
{{else}}
<meta property="og:type" content="website">
<meta property="og:image" content="{{.OGImage}}">
{{if .Width}}<meta property="og:image:width" content="{{.Width}}">{{end}}
{{if .Height}}<meta property="og:image:height" content="{{.Height}}">{{end}}
<meta property="og:image:type" content="{{.OGImageType}}">
<meta name="twitter:card" content="summary_large_image">
{{end}}
<link rel="stylesheet" href="/static/dist/main.css">
<script type="module" src="/static/dist/main.js" defer></script>
</head>
<body class="embed">
<main class="embed__panel" data-island="copy" data-share-url="{{.ShareURL}}">
  <div class="embed__media">
    {{if .IsVideoEmbed}}
    <video src="{{.MediaURL}}" controls preload="metadata" playsinline poster="{{.PosterURL}}"></video>
    {{else if .IsVideo}}
    <video src="{{.MediaURL}}" controls preload="metadata" playsinline poster="{{.PosterURL}}"></video>
    {{else}}
    <img src="{{.MediaURL}}" alt="{{.Title}}">
    {{end}}
  </div>
  <div class="embed__meta">
    <h1 class="embed__title">{{.Title}}</h1>
    <p class="embed__by"><span class="embed__avatar" aria-hidden="true">{{.UploaderInitial}}</span> {{.UploaderName}}</p>
    {{if .SourceURL}}<p><a href="{{.SourceURL}}" rel="noreferrer noopener" target="_blank">Source</a></p>{{end}}
    <button type="button" data-action="copy">Copy share link</button>
  </div>
</main>
</body>
</html>
```

- [ ] **Step 5: Parse the embed template and add `RenderEmbed`**

In `internal/web/render.go`, change the `Renderer` struct to carry the embed template, extend `NewRenderer` to parse it, and add `RenderEmbed`.

Change the struct:

```go
type Renderer struct {
	pages map[string]*template.Template
	embed *template.Template
}
```

At the end of `NewRenderer`, before `return &Renderer{...}`, parse the standalone document and include it in the literal:

```go
	embedTpl, err := template.New("embed.html").ParseFS(webassets.Templates, "templates/embed.html")
	if err != nil {
		return nil, fmt.Errorf("web: parse embed template: %w", err)
	}
	return &Renderer{pages: pages, embed: embedTpl}, nil
```

(Delete the old `return &Renderer{pages: pages}, nil` line it replaces.)

Add the render method:

```go
// RenderEmbed writes the standalone /s/{id} document. It buffers first so a
// mid-template error cannot emit a half-written 200.
func (r *Renderer) RenderEmbed(w http.ResponseWriter, status int, data embedData) error {
	var buf bytes.Buffer
	if err := r.embed.Execute(&buf, data); err != nil {
		return fmt.Errorf("web: render embed: %w", err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}
```

`render.go` already imports `bytes`, `fmt`, `html/template` and `boobies-media/web` (package `webassets`) — the `Render` method that Plan 1 built uses all four. No import changes are needed.

- [ ] **Step 6: Implement the embed handler**

Create `internal/web/handlers_embed.go`:

```go
package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"boobies-media/internal/db"
	"boobies-media/internal/media"
)

// embedData is the template model for /s/{id}. Every URL is absolute so a
// crawler that only reads the <head> resolves them without a base.
type embedData struct {
	Title           string
	ShareURL        string
	MediaURL        string
	SecureMediaURL  string
	PosterURL       string
	OGImage         string
	OGImageType     string
	Mime            string
	Width           int64
	Height          int64
	IsVideo         bool
	IsVideoEmbed    bool // an mp4 that gets full video OG tags
	SourceURL       string
	UploaderName    string
	UploaderInitial string
}

// handleEmbed serves the anonymous share/viewer page at GET /s/{id}.
func (s *Server) handleEmbed(w http.ResponseWriter, r *http.Request) {
	if s.PublicLimiter != nil && !s.PublicLimiter.Allow(clientIP(r)) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	// ItemByID excludes soft-deleted rows.
	item, err := s.Store.ItemByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, r, err)
		return
	}
	if !item.IsPubliclyServable() {
		http.NotFound(w, r)
		return
	}

	uploaderName := "a friend"
	if u, err := s.Store.UserByID(r.Context(), item.UploaderID); err == nil {
		if u.DisplayName != "" {
			uploaderName = u.DisplayName
		} else if u.Username != "" {
			uploaderName = u.Username
		}
	}
	initial := "?"
	if uploaderName != "" {
		initial = strings.ToUpper(uploaderName[:1])
	}

	base := strings.TrimRight(s.Cfg.BaseURL, "/")
	secure := base
	if strings.HasPrefix(secure, "http://") {
		secure = "https://" + strings.TrimPrefix(secure, "http://")
	}
	data := embedData{
		Title:           item.Title,
		ShareURL:        base + "/s/" + item.ID,
		MediaURL:        base + "/m/" + item.ID,
		SecureMediaURL:  secure + "/m/" + item.ID,
		PosterURL:       base + "/t/" + item.ID + "?s=1024",
		Mime:            item.Mime,
		Width:           item.Width,
		Height:          item.Height,
		IsVideo:         media.IsVideoMime(item.Mime),
		SourceURL:       item.SourceURL,
		UploaderName:    uploaderName,
		UploaderInitial: initial,
	}
	// Only H.264 MP4 gets a video card; everything else (webm, images) gets an
	// image card. yt-dlp downloads are constrained to mp4, so this is the norm.
	if item.Mime == "video/mp4" {
		data.IsVideoEmbed = true
	} else {
		data.OGImageType = item.Mime
		data.OGImage = data.MediaURL
		if data.IsVideo {
			// A non-mp4 video cannot be an og:image; use its poster thumbnail.
			data.OGImage = data.PosterURL
			data.OGImageType = "image/webp"
		}
	}

	w.Header().Set("Referrer-Policy", "no-referrer")
	if err := s.Renderer.RenderEmbed(w, http.StatusOK, data); err != nil {
		s.serverError(w, r, err)
	}
}
```

- [ ] **Step 7: Register the route**

In `internal/web/server.go`, after the `r.Get("/t/{id}", s.handleThumbnail)` line added in Plan 2, add:

```go
	r.Get("/s/{id}", s.handleEmbed)
```

`/s/` is already in `IsPublicPath`'s prefix allowlist (Plan 1), so the gate lets it through anonymously.

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./internal/web/ -run Embed -v`
Expected: PASS — `TestEmbedImageHasEveryOpenGraphTag`, `TestEmbedVideoHasVideoAndPosterTags`, `TestEmbedWebmFallsBackToImageCard`, `TestEmbedRevokedAndDeletedReturn404` (3 sub-cases), `TestEmbedEscapesTitle`.

Run: `go test ./internal/web/ -run Renderer -v`
Expected: PASS — Plan 1's `TestNewRendererLoadsEveryPage` and `TestRenderUnknownPageIsAnError` still pass with the embed template added.

- [ ] **Step 9: Commit**

```bash
git add internal/web/handlers_embed.go internal/web/handlers_embed_test.go internal/web/render.go internal/web/server.go internal/db/items.go web/templates/embed.html
git commit -m "feat(web): anonymous /s/{id} embed page with per-item OpenGraph markup"
```

---

### Task 2: Folder JSON CRUD API

**Files:**
- Create: `internal/web/handlers_folders.go`
- Modify: `internal/web/server.go` (register the routes)
- Test: `internal/web/handlers_folders_test.go`

**Interfaces:**
- Consumes: `db.CreateFolder`, `db.FolderByID`, `db.ListFolders`, `db.RenameFolder`, `db.MoveFolder`, `db.DeleteFolder`, `db.NormalizeFolderName`, `db.ErrNotFound`, `db.ErrFolderCycle` (Plan 2)
- Produces:
  - `GET /api/folders` — `{folders:[{id,parent_id,name}]}`
  - `POST /api/folders` — JSON `{name, parent_id?}` → `201 {folder}`
  - `PATCH /api/folders/{id}` — JSON `{name?, parent_id?}`; rename and/or move
  - `DELETE /api/folders/{id}` — `204`
  - `folderJSON(f *db.Folder) map[string]any`

The whole folder store — including the cycle-rejecting `MoveFolder` — was built and tested in Plan 2. This task is only the HTTP surface the sidebar island calls. A cycle attempt surfaces as `409 folder_cycle`.

- [ ] **Step 1: Write the failing test**

Create `internal/web/handlers_folders_test.go`:

```go
package web

import (
	"encoding/json"
	"net/http"
	"testing"
)

type folderResp struct {
	Folder struct {
		ID       int64  `json:"id"`
		ParentID int64  `json:"parent_id"`
		Name     string `json:"name"`
	} `json:"folder"`
}

func createFolder(t *testing.T, srv *Server, cookie *http.Cookie, name string, parent int64) int64 {
	t.Helper()
	body := `{"name":"` + name + `","parent_id":` + itoa(parent) + `}`
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/folders", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %q: status = %d: %s", name, rec.Code, rec.Body.String())
	}
	var out folderResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	return out.Folder.ID
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestFolderCRUDRoundTrip(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")

	root := createFolder(t, srv, cookie, "Memes", 0)
	child := createFolder(t, srv, cookie, "Reaction", root)

	// List returns both.
	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/folders", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	var listed struct {
		Folders []struct {
			ID       int64  `json:"id"`
			ParentID int64  `json:"parent_id"`
			Name     string `json:"name"`
		} `json:"folders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Folders) != 2 {
		t.Fatalf("listed %d folders, want 2", len(listed.Folders))
	}

	// Rename the child.
	rec = apiRequest(t, srv, cookie, http.MethodPatch, "/api/folders/"+itoa(child), `{"name":"Reactions"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d: %s", rec.Code, rec.Body.String())
	}
	var renamed folderResp
	_ = json.Unmarshal(rec.Body.Bytes(), &renamed)
	if renamed.Folder.Name != "Reactions" {
		t.Errorf("renamed to %q, want Reactions", renamed.Folder.Name)
	}

	// Delete the child.
	rec = apiRequest(t, srv, cookie, http.MethodDelete, "/api/folders/"+itoa(child), "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete status = %d, want 204", rec.Code)
	}
}

func TestFolderMoveRejectsCycle(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")

	parent := createFolder(t, srv, cookie, "A", 0)
	child := createFolder(t, srv, cookie, "B", parent)

	// Moving the parent under its own child is a cycle.
	rec := apiRequest(t, srv, cookie, http.MethodPatch, "/api/folders/"+itoa(parent), `{"parent_id":`+itoa(child)+`}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("cycle move status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var errBody struct{ Code string `json:"code"` }
	_ = json.Unmarshal(rec.Body.Bytes(), &errBody)
	if errBody.Code != "folder_cycle" {
		t.Errorf("error code = %q, want folder_cycle", errBody.Code)
	}
}

func TestFolderCreateRejectsBlankName(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/folders", `{"name":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("blank-name status = %d, want 400", rec.Code)
	}
}

func TestFolderRoutesRequireAuth(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/folders", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous folder list status = %d, want 401", rec.Code)
	}
}
```

Add `"net/http/httptest"` to the import block — `TestFolderRoutesRequireAuth` uses it.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/web/ -run Folder -v`
Expected: FAIL to compile — `undefined: (*Server).handleListFolders` and the folder routes are unregistered.

- [ ] **Step 3: Implement the handlers**

Create `internal/web/handlers_folders.go`:

```go
package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"boobies-media/internal/db"
)

func folderJSON(f *db.Folder) map[string]any {
	return map[string]any{"id": f.ID, "parent_id": f.ParentID, "name": f.Name}
}

// handleListFolders returns every folder; the sidebar island builds the tree.
func (s *Server) handleListFolders(w http.ResponseWriter, r *http.Request) {
	folders, err := s.Store.ListFolders(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(folders))
	for _, f := range folders {
		out = append(out, folderJSON(f))
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": out})
}

func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		ParentID int64  `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	name, err := db.NormalizeFolderName(body.Name)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_folder_name", "a folder name must not be empty")
		return
	}
	folder, err := s.Store.CreateFolder(r.Context(), body.ParentID, name)
	if err != nil {
		s.writeFolderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"folder": folderJSON(folder)})
}

func (s *Server) handleUpdateFolder(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "folder id must be a number")
		return
	}
	var body struct {
		Name     *string `json:"name"`
		ParentID *int64  `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.Name != nil {
		name, err := db.NormalizeFolderName(*body.Name)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_folder_name", "a folder name must not be empty")
			return
		}
		if err := s.Store.RenameFolder(r.Context(), id, name); err != nil {
			s.writeFolderError(w, r, err)
			return
		}
	}
	if body.ParentID != nil {
		if err := s.Store.MoveFolder(r.Context(), id, *body.ParentID); err != nil {
			s.writeFolderError(w, r, err)
			return
		}
	}
	folder, err := s.Store.FolderByID(r.Context(), id)
	if err != nil {
		s.writeFolderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folder": folderJSON(folder)})
}

func (s *Server) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "folder id must be a number")
		return
	}
	if err := s.Store.DeleteFolder(r.Context(), id); err != nil {
		s.writeFolderError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeFolderError maps the folder store's sentinels to status codes.
func (s *Server) writeFolderError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "not_found", "no such folder")
	case errors.Is(err, db.ErrFolderCycle):
		writeJSONError(w, http.StatusConflict, "folder_cycle", "a folder cannot be moved inside itself")
	default:
		s.serverError(w, r, err)
	}
}
```

- [ ] **Step 4: Register the routes**

In `internal/web/server.go`, after the `/s/{id}` line from Task 1, add the folder routes (these are authenticated, not admin, so they need no group):

```go
	r.Get("/api/folders", s.handleListFolders)
	r.Post("/api/folders", s.handleCreateFolder)
	r.Patch("/api/folders/{id}", s.handleUpdateFolder)
	r.Delete("/api/folders/{id}", s.handleDeleteFolder)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/web/ -run Folder -v`
Expected: PASS — `TestFolderCRUDRoundTrip`, `TestFolderMoveRejectsCycle`, `TestFolderCreateRejectsBlankName`, `TestFolderRoutesRequireAuth`.

- [ ] **Step 6: Commit**

```bash
git add internal/web/handlers_folders.go internal/web/handlers_folders_test.go internal/web/server.go
git commit -m "feat(web): folder JSON CRUD over the cycle-safe folder store"
```

---

### Task 3: Browse layout — folder sidebar, filter mount points, lightbox folder move

**Files:**
- Modify: `internal/web/handlers_pages.go` (`browseData` gains `Folder`, `Tag`)
- Modify: `web/templates/pages/browse.html` (sidebar, chip/bulk mount points, lightbox folder select)
- Create: `web/src/islands/folders.ts`
- Modify: `web/src/islands/lightbox.ts` (folder-move select)
- Modify: `web/src/main.ts` (register the folders island)
- Modify: `web/src/main.css` (layout, sidebar, tree styles)
- Test: `internal/web/handlers_pages_test.go` (extend)

**Interfaces:**
- Consumes: `GET /api/folders`, `POST /api/folders`, `PATCH /api/folders/{id}`, `DELETE /api/folders/{id}` (Task 2); `PATCH /api/items/{id}` (Plan 2); `GET /api/items?folder=` (Plan 2, already filters)
- Produces:
  - `browseData.Folder string`, `browseData.Tag string`
  - a `data-island="folders"` sidebar and, in the lightbox, a `data-role="folder"` `<select>`
  - `mountFolders(root)` in `web/src/islands/folders.ts`

**Server-rendered navigation, island-enhanced management.** Filtering by folder is a plain link to `/?folder=<id>` that the already-built `handleBrowse` honors, so it works with JavaScript off. The island only adds create/rename/delete and highlights the current folder. This lands the sidebar plus the mount points that Tasks 4 and 5 fill, so `browse.html` is rewritten exactly once.

- [ ] **Step 1: Extend the browse page test**

Add to `internal/web/handlers_pages_test.go`:

```go
func TestBrowseMountsSidebarChipsAndBulkbar(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, mount := range []string{
		`data-island="folders"`,
		`data-island="tagfilter"`,
		`data-island="bulkselect"`,
		`data-role="folder"`, // the lightbox folder-move select
	} {
		if !strings.Contains(body, mount) {
			t.Errorf("browse page is missing mount point %s", mount)
		}
	}
}

func TestBrowseMarksTheActiveFolder(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")

	req := httptest.NewRequest(http.MethodGet, "/?folder=7", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), `data-current="7"`) {
		t.Error("the sidebar did not record the active folder id")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/web/ -run Browse -v`
Expected: FAIL — the Plan 2 `browse.html` has no `data-island="folders"`.

- [ ] **Step 3: Extend `browseData` and the handler**

In `internal/web/handlers_pages.go`, add two fields to `browseData`:

```go
type browseData struct {
	Items      []map[string]any
	NextCursor string
	Sort       string
	Query      string
	Folder     string
	Tag        string
}
```

and set them in `handleBrowse` where the `browseData` literal is built:

```go
		Data: browseData{
			Items:      payload,
			NextCursor: next,
			Sort:       sort,
			Query:      r.URL.Query().Get("q"),
			Folder:     r.URL.Query().Get("folder"),
			Tag:        r.URL.Query().Get("tag"),
		},
```

- [ ] **Step 4: Rewrite the browse template**

Replace `web/templates/pages/browse.html` with:

```html
{{define "title"}}Browse{{end}}
{{define "content"}}
{{$data := .Data}}
<div class="browse">
  <aside class="sidebar" data-island="folders" data-current="{{$data.Folder}}">
    <div class="sidebar__head">
      <h2>Folders</h2>
      <button type="button" data-action="new-folder" aria-label="New folder">+</button>
    </div>
    <ul class="foldertree" data-role="tree">
      <li><a href="/" class="foldertree__all">All items</a></li>
    </ul>
  </aside>

  <div class="browse__main">
    <section class="toolbar">
      <form method="get" action="/" class="toolbar__filters">
        <input type="search" name="q" placeholder="Search titles" value="{{$data.Query}}" aria-label="Search titles">
        <select name="sort" aria-label="Sort order">
          <option value="newest" {{if eq $data.Sort "newest"}}selected{{end}}>Newest</option>
          <option value="oldest" {{if eq $data.Sort "oldest"}}selected{{end}}>Oldest</option>
          <option value="name" {{if eq $data.Sort "name"}}selected{{end}}>Name</option>
          <option value="size" {{if eq $data.Sort "size"}}selected{{end}}>Size</option>
          <option value="uploader" {{if eq $data.Sort "uploader"}}selected{{end}}>Uploader</option>
        </select>
        <button type="submit">Apply</button>
      </form>
      <div class="toolbar__upload" data-island="uploader">
        <input type="file" id="file-input" multiple hidden
               accept="image/jpeg,image/png,image/gif,image/webp,image/avif,video/mp4,video/webm">
        <button type="button" data-action="pick">Upload files</button>
        <ul class="uploads" data-role="progress"></ul>
      </div>
    </section>

    <div class="chips" data-island="tagfilter" data-current-tag="{{$data.Tag}}"></div>

    <div class="bulkbar" data-island="bulkselect" hidden>
      <label class="bulkbar__toggle"><input type="checkbox" data-action="select-mode"> Select</label>
      <span class="bulkbar__count" data-role="count">0 selected</span>
      <button type="button" data-action="bulk-move" disabled>Move…</button>
      <button type="button" data-action="bulk-tag" disabled>Tag…</button>
      <button type="button" data-action="bulk-delete" class="danger" disabled>Delete</button>
    </div>

    <div class="grid" data-island="grid" data-cursor="{{$data.NextCursor}}" data-sort="{{$data.Sort}}">
      {{range $data.Items}}
      <article class="tile{{if not .ready}} tile--processing{{end}}" data-item-id="{{.id}}" data-role="tile">
        <button type="button" class="tile__button" data-action="open" aria-label="Open {{.title}}">
          <img class="tile__image" src="/t/{{.id}}?s=320" alt="{{.title}}" loading="lazy" width="320" height="320">
          {{if .is_video}}<span class="tile__badge" aria-label="Video">&#9654;</span>{{end}}
        </button>
        <p class="tile__title" title="{{.title}}">{{.title}}</p>
      </article>
      {{else}}
      <p class="empty" data-role="empty">Nothing here yet. Upload something.</p>
      {{end}}
    </div>
    <p class="grid__status" data-role="grid-status" hidden>Loading…</p>
  </div>
</div>

<div class="lightbox" data-island="lightbox" hidden>
  <div class="lightbox__backdrop" data-action="close"></div>
  <figure class="lightbox__panel">
    <div class="lightbox__media" data-role="media"></div>
    <figcaption class="lightbox__meta">
      <input class="lightbox__title" data-role="title" aria-label="Title">
      <label class="lightbox__folder">Folder
        <select data-role="folder" aria-label="Folder"><option value="0">— none —</option></select>
      </label>
      <p class="lightbox__tags" data-role="tags"></p>
      <form class="lightbox__tagform" data-role="tagform">
        <input name="tag" placeholder="Add a tag" aria-label="Add a tag">
        <button type="submit">Add</button>
      </form>
      <p class="lightbox__links">
        <a data-role="share" href="#" target="_blank" rel="noopener">Share link</a>
        <a data-role="source" href="#" target="_blank" rel="noopener noreferrer" hidden>Source</a>
      </p>
      <div class="lightbox__actions">
        <button type="button" data-action="copy">Copy share link</button>
        <label class="lightbox__revoke"><input type="checkbox" data-role="revoke"> Revoke share</label>
        <button type="button" data-action="delete" class="danger">Delete</button>
        <button type="button" data-action="close">Close</button>
      </div>
      <p class="lightbox__error" data-role="error" hidden></p>
    </figcaption>
  </figure>
</div>
{{end}}
```

- [ ] **Step 5: Write the folders island**

Create `web/src/islands/folders.ts`:

```ts
/**
 * Folder tree sidebar. Filtering is a plain link to /?folder=<id> that the
 * server already honors; this island only fetches the tree, highlights the
 * active folder, and offers create/rename/delete over /api/folders.
 */

interface ApiFolder {
  id: number;
  parent_id: number;
  name: string;
}

export function mountFolders(root: HTMLElement): void {
  const tree = root.querySelector<HTMLElement>('[data-role="tree"]');
  const current = root.dataset.current ?? "";
  if (!tree) return;

  root.querySelector<HTMLButtonElement>('[data-action="new-folder"]')?.addEventListener("click", () => void newFolder(0));

  async function load(): Promise<void> {
    const response = await fetch("/api/folders", { headers: { Accept: "application/json" } });
    if (!response.ok) return;
    const { folders } = (await response.json()) as { folders: ApiFolder[] };
    render(folders);
  }

  function render(folders: ApiFolder[]): void {
    const byParent = new Map<number, ApiFolder[]>();
    for (const f of folders) {
      const list = byParent.get(f.parent_id) ?? [];
      list.push(f);
      byParent.set(f.parent_id, list);
    }
    // Keep the "All items" row, drop any previous dynamic rows.
    for (const extra of Array.from(tree!.querySelectorAll("li:not(:first-child)"))) extra.remove();
    appendChildren(0, 0);

    function appendChildren(parentId: number, depth: number): void {
      for (const folder of (byParent.get(parentId) ?? []).sort((a, b) => a.name.localeCompare(b.name))) {
        tree!.appendChild(row(folder, depth));
        appendChildren(folder.id, depth + 1);
      }
    }
  }

  function row(folder: ApiFolder, depth: number): HTMLElement {
    const li = document.createElement("li");
    li.style.paddingLeft = `${depth * 0.75}rem`;
    const link = document.createElement("a");
    link.href = `/?folder=${folder.id}`;
    link.textContent = folder.name;
    if (String(folder.id) === current) link.classList.add("foldertree__active");

    const actions = document.createElement("span");
    actions.className = "foldertree__actions";
    actions.appendChild(iconButton("Rename", () => void rename(folder)));
    actions.appendChild(iconButton("New subfolder", () => void newFolder(folder.id)));
    actions.appendChild(iconButton("Delete", () => void remove(folder)));

    li.append(link, actions);
    return li;
  }

  function iconButton(label: string, onClick: () => void): HTMLButtonElement {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "foldertree__icon";
    button.title = label;
    button.setAttribute("aria-label", label);
    button.textContent = label[0];
    button.addEventListener("click", onClick);
    return button;
  }

  async function newFolder(parentId: number): Promise<void> {
    const name = window.prompt("New folder name");
    if (!name) return;
    const response = await fetch("/api/folders", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, parent_id: parentId }),
    });
    if (response.ok) void load();
    else window.alert("Could not create that folder.");
  }

  async function rename(folder: ApiFolder): Promise<void> {
    const name = window.prompt("Rename folder", folder.name);
    if (!name || name === folder.name) return;
    const response = await fetch(`/api/folders/${folder.id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    });
    if (response.ok) void load();
    else window.alert("Rename failed.");
  }

  async function remove(folder: ApiFolder): Promise<void> {
    if (!window.confirm(`Delete folder "${folder.name}"? Items inside move to no folder.`)) return;
    const response = await fetch(`/api/folders/${folder.id}`, { method: "DELETE" });
    if (response.ok) void load();
    else window.alert("Delete failed.");
  }

  void load();
}
```

- [ ] **Step 6: Add the folder-move select to the lightbox**

In `web/src/islands/lightbox.ts`, resolve the new select at the top of `mountLightbox`, right after the existing `errorBox` lookup:

```ts
  const folderSelect = root.querySelector<HTMLSelectElement>('[data-role="folder"]');
  const revokeToggle = root.querySelector<HTMLInputElement>('[data-role="revoke"]');
```

Populate the select once, at the end of `mountLightbox` (before the deep-link block):

```ts
  // Fill the folder move dropdown from the folder store.
  void (async () => {
    if (!folderSelect) return;
    const response = await fetch("/api/folders", { headers: { Accept: "application/json" } });
    if (!response.ok) return;
    const { folders } = (await response.json()) as { folders: { id: number; name: string }[] };
    for (const folder of folders.sort((a, b) => a.name.localeCompare(b.name))) {
      const option = document.createElement("option");
      option.value = String(folder.id);
      option.textContent = folder.name;
      folderSelect.appendChild(option);
    }
  })();

  folderSelect?.addEventListener("change", async () => {
    if (!current) return;
    const response = await fetch(`/api/items/${encodeURIComponent(current.id)}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ folder_id: Number(folderSelect.value) }),
    });
    if (!response.ok) showError("That move did not save.");
  });

  revokeToggle?.addEventListener("change", async () => {
    if (!current) return;
    const response = await fetch(`/api/items/${encodeURIComponent(current.id)}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ share_revoked: revokeToggle.checked }),
    });
    if (!response.ok) {
      showError("That change did not save.");
      revokeToggle.checked = !revokeToggle.checked;
    }
  });
```

In the existing `open(id)` function, set the two new controls from the loaded item, right after `renderTags(item.tags);`:

```ts
    if (folderSelect) folderSelect.value = String(item.folder_id ?? 0);
    if (revokeToggle) revokeToggle.checked = Boolean(item.revoked);
```

Add `folder_id?: number;` and `revoked?: boolean;` to the `ApiItem` interface in `web/src/islands/grid.ts` (they are already present in the JSON payload from `itemJSON`; the interface simply did not declare them):

```ts
  folder_id?: number;
  revoked?: boolean;
```

- [ ] **Step 7: Register the folders island**

In `web/src/main.ts`, add the import and registration alongside the Plan 2 islands:

```ts
import { mountFolders } from "./islands/folders";
```

```ts
registerIsland("folders", mountFolders);
```

- [ ] **Step 8: Add the layout and sidebar styles**

Append to `web/src/main.css`:

```css
.browse { display: grid; grid-template-columns: minmax(180px, 240px) minmax(0, 1fr); gap: 1.25rem; }
@media (max-width: 720px) { .browse { grid-template-columns: 1fr; } }
.browse__main { min-width: 0; }

.sidebar__head { display: flex; align-items: center; justify-content: space-between; gap: 0.5rem; }
.sidebar__head h2 { font-size: 0.9rem; margin: 0; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.05em; }
.foldertree { list-style: none; margin: 0.5rem 0 0; padding: 0; }
.foldertree li { display: flex; align-items: center; justify-content: space-between; gap: 0.25rem; }
.foldertree a { display: block; padding: 0.25rem 0; color: var(--fg); text-decoration: none; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.foldertree a:hover { color: var(--accent); }
.foldertree__active { color: var(--accent); font-weight: 600; }
.foldertree__actions { display: none; gap: 0.15rem; }
.foldertree li:hover .foldertree__actions { display: flex; }
.foldertree__icon {
  width: 1.4rem; height: 1.4rem; padding: 0; line-height: 1;
  background: var(--bg-raised); color: var(--fg-muted);
  border: 1px solid var(--border); border-radius: 4px; cursor: pointer;
}
.lightbox__folder { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.8rem; color: var(--fg-muted); }
.lightbox__revoke { display: inline-flex; align-items: center; gap: 0.35rem; font-size: 0.85rem; }
```

- [ ] **Step 9: Build the assets and type-check**

Run: `bunx tsc --noEmit`
Expected: clean — the new island and the lightbox additions type-check under the strict `tsconfig.json`.

Run: `bun run build && ls -l web/static/dist`
Expected: `main.js` still well under 50 KB.

- [ ] **Step 10: Run the Go tests**

Run: `go test ./internal/web/ -run Browse -v`
Expected: PASS — `TestBrowseMountsSidebarChipsAndBulkbar`, `TestBrowseMarksTheActiveFolder`, and the Plan 2 browse tests (`TestBrowseRendersTheFirstPageOfItems`, `TestBrowseEscapesItemTitles`, `TestBrowseShowsAnEmptyStateWithNoItems`).

- [ ] **Step 11: Commit**

```bash
git add internal/web/handlers_pages.go internal/web/handlers_pages_test.go web/
git commit -m "feat(web): folder tree sidebar and lightbox folder move"
```

---

### Task 4: Tag list endpoint, random-item endpoint, and the tag-filter chips

**Files:**
- Create: `internal/db/random.go`
- Create: `internal/web/handlers_tags.go`
- Modify: `internal/web/server.go` (register `/api/tags`, `/api/random`)
- Create: `web/src/islands/tagfilter.ts`
- Modify: `web/src/main.ts` (register the tagfilter island)
- Modify: `web/src/main.css` (chip styles)
- Test: `internal/db/random_test.go`, `internal/web/handlers_tags_test.go`

**Interfaces:**
- Consumes: `db.ListTags`, `db.ItemTags`, `db.NormalizeTag`, `db.ItemByID`, `db.ErrNotFound`, `itemJSON` (Plans 1–2)
- Produces:
  - `(*Store) RandomItem(ctx context.Context, tag string) (*Item, error)` — a uniformly random live, non-revoked item, optionally tag-filtered; `ErrNotFound` when none match
  - `GET /api/tags` — `{tags:[…]}`
  - `GET /api/random?tag=` — `{item}` (the bot-facing endpoint)
  - `mountTagFilter(root)` in `web/src/islands/tagfilter.ts`

`GET /api/items?tag=` already filters (Plan 2's `listItemQuery`), so the chips are plain links to `/?tag=<name>`. The bot's `GET /api/random?tag=` is the only new query capability, and it excludes revoked/deleted items so a link the bot posts is always servable.

- [ ] **Step 1: Write the failing db test**

Create `internal/db/random_test.go`:

```go
package db_test

import (
	"context"
	"errors"
	"testing"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

func mkItem(t *testing.T, store *db.Store, uploaderID int64, hash, title string) *db.Item {
	t.Helper()
	item, err := store.CreateItem(context.Background(), db.NewItem{
		ContentHash: hash, Title: title, Ext: "png", Mime: "image/png", Size: 10, UploaderID: uploaderID,
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	return item
}

func TestRandomItemReturnsOnlyServableItems(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user, err := store.CreateUser(ctx, "aiden", "Aiden", "h", "", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	live := mkItem(t, store, user.ID, "hash-live", "live")
	revoked := mkItem(t, store, user.ID, "hash-revoked", "revoked")
	if err := store.SetItemShareRevoked(ctx, revoked.ID, true); err != nil {
		t.Fatalf("SetItemShareRevoked: %v", err)
	}

	for i := 0; i < 20; i++ {
		got, err := store.RandomItem(ctx, "")
		if err != nil {
			t.Fatalf("RandomItem: %v", err)
		}
		if got.ID != live.ID {
			t.Fatalf("RandomItem returned %s, want the only servable item %s", got.ID, live.ID)
		}
	}
}

func TestRandomItemFiltersByTag(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user, _ := store.CreateUser(ctx, "aiden", "Aiden", "h", "", false)
	tagged := mkItem(t, store, user.ID, "hash-tagged", "tagged")
	_ = mkItem(t, store, user.ID, "hash-plain", "plain")
	if err := store.AddItemTag(ctx, tagged.ID, "Cats"); err != nil {
		t.Fatalf("AddItemTag: %v", err)
	}

	got, err := store.RandomItem(ctx, "cats")
	if err != nil {
		t.Fatalf("RandomItem(cats): %v", err)
	}
	if got.ID != tagged.ID {
		t.Errorf("RandomItem(cats) = %s, want %s", got.ID, tagged.ID)
	}
}

func TestRandomItemReturnsErrNotFoundWhenEmpty(t *testing.T) {
	if _, err := dbtest.New(t).RandomItem(context.Background(), ""); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("RandomItem on an empty library = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/db/ -run Random -v`
Expected: FAIL to compile — `undefined: (*Store).RandomItem`.

- [ ] **Step 3: Implement `RandomItem`**

Create `internal/db/random.go`:

```go
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// RandomItem returns a uniformly random item that is live and not share-revoked,
// so a link the bot posts always resolves. An optional tag narrows the pool.
// It selects an id with ORDER BY RANDOM() then reuses ItemByID's tested scan.
func (s *Store) RandomItem(ctx context.Context, tag string) (*Item, error) {
	var (
		id    string
		query string
		args  []any
	)
	if tag != "" {
		norm, err := NormalizeTag(tag)
		if err != nil {
			return nil, ErrNotFound // an unusable tag matches nothing
		}
		query = `SELECT i.id FROM items i
			JOIN item_tags it ON it.item_id = i.id
			JOIN tags t ON t.id = it.tag_id
			WHERE t.name = ? AND i.deleted_at IS NULL AND i.share_revoked = 0
			ORDER BY RANDOM() LIMIT 1`
		args = []any{norm}
	} else {
		query = `SELECT id FROM items
			WHERE deleted_at IS NULL AND share_revoked = 0
			ORDER BY RANDOM() LIMIT 1`
	}
	err := s.DB.QueryRowContext(ctx, query, args...).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("db: random item: %w", err)
	}
	return s.ItemByID(ctx, id)
}
```

- [ ] **Step 4: Run the db test to verify it passes**

Run: `go test ./internal/db/ -run Random -v`
Expected: PASS — `TestRandomItemReturnsOnlyServableItems`, `TestRandomItemFiltersByTag`, `TestRandomItemReturnsErrNotFoundWhenEmpty`.

- [ ] **Step 5: Write the failing web test**

Create `internal/web/handlers_tags_test.go`:

```go
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestListTagsReturnsSortedNames(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, srv, mediaStore, user.ID, "a")
	if err := srv.Store.AddItemTag(ctx, items[0].ID, "funny"); err != nil {
		t.Fatalf("AddItemTag: %v", err)
	}

	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/tags", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Tags) != 1 || out.Tags[0] != "funny" {
		t.Errorf("tags = %v, want [funny]", out.Tags)
	}
}

func TestRandomItemEndpointReturnsAnItem(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	seedItems(t, srv, mediaStore, user.ID, "only")

	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/random", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		Item map[string]any `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Item["share_url"] == "" || out.Item["id"] == "" {
		t.Errorf("random item missing id/share_url: %v", out.Item)
	}
}

func TestRandomItemEndpoint404sWhenEmpty(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodGet, "/api/random?tag=nope", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
```

- [ ] **Step 6: Run the web test to verify it fails**

Run: `go test ./internal/web/ -run 'Tags|Random' -v`
Expected: FAIL to compile — `undefined: (*Server).handleListTags`.

- [ ] **Step 7: Implement the handlers**

Create `internal/web/handlers_tags.go`:

```go
package web

import (
	"errors"
	"net/http"

	"boobies-media/internal/db"
)

// handleListTags returns every tag name for the filter chips.
func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.Store.ListTags(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// handleRandomItem powers the Discord bot's GET /api/random?tag=.
func (s *Server) handleRandomItem(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.RandomItem(r.Context(), r.URL.Query().Get("tag"))
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "no matching item")
			return
		}
		s.serverError(w, r, err)
		return
	}
	tags, err := s.Store.ItemTags(r.Context(), item.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": itemJSON(item, tags, s.Cfg.BaseURL)})
}
```

- [ ] **Step 8: Register the routes**

In `internal/web/server.go`, after the folder routes from Task 2, add:

```go
	r.Get("/api/tags", s.handleListTags)
	r.Get("/api/random", s.handleRandomItem)
```

- [ ] **Step 9: Write the tag-filter island**

Create `web/src/islands/tagfilter.ts`:

```ts
/**
 * Tag filter chips. GET /api/items?tag= already filters server-side, so each
 * chip is a plain link to /?tag=<name>; this island only lists the tags and
 * marks the active one.
 */

export function mountTagFilter(root: HTMLElement): void {
  const current = root.dataset.currentTag ?? "";

  void (async () => {
    const response = await fetch("/api/tags", { headers: { Accept: "application/json" } });
    if (!response.ok) return;
    const { tags } = (await response.json()) as { tags: string[] };
    if (tags.length === 0) return;

    root.appendChild(chip("All", "/", current === ""));
    for (const tag of tags) {
      root.appendChild(chip(`#${tag}`, `/?tag=${encodeURIComponent(tag)}`, tag === current));
    }
  })();
}

function chip(label: string, href: string, active: boolean): HTMLAnchorElement {
  const link = document.createElement("a");
  link.className = active ? "chip chip--active" : "chip";
  link.href = href;
  link.textContent = label;
  return link;
}
```

- [ ] **Step 10: Register the island and add chip styles**

In `web/src/main.ts`:

```ts
import { mountTagFilter } from "./islands/tagfilter";
```

```ts
registerIsland("tagfilter", mountTagFilter);
```

Append to `web/src/main.css`:

```css
.chips { display: flex; flex-wrap: wrap; gap: 0.35rem; margin-bottom: 1rem; }
.chips:empty { margin: 0; }
a.chip { text-decoration: none; }
.chip--active { background: var(--accent); color: var(--bg); border-color: var(--accent); }
```

- [ ] **Step 11: Type-check, build, and run the tests**

Run: `bunx tsc --noEmit && bun run build`
Expected: clean; `main.js` under 50 KB.

Run: `go test ./internal/db/ ./internal/web/ -run 'Random|Tags' -v`
Expected: PASS — the three db random tests and the three web tag/random tests.

- [ ] **Step 12: Commit**

```bash
git add internal/db/random.go internal/db/random_test.go internal/web/handlers_tags.go internal/web/handlers_tags_test.go internal/web/server.go web/
git commit -m "feat: tag filter chips, /api/tags, and the bot /api/random endpoint"
```

---

### Task 5: Bulk-select toolbar and the batch endpoint

**Files:**
- Create: `internal/web/handlers_batch.go`
- Modify: `internal/web/server.go` (register `/api/items/batch`)
- Create: `web/src/islands/bulkselect.ts`
- Modify: `web/src/main.ts` (register the bulkselect island)
- Modify: `web/src/main.css` (toolbar + selection styles)
- Test: `internal/web/handlers_batch_test.go`

**Interfaces:**
- Consumes: `db.SoftDeleteItem`, `db.MoveItem`, `db.AddItemTag`, `db.ErrForbidden`, `db.ErrNotFound`, `CurrentUser` (Plans 1–2)
- Produces:
  - `POST /api/items/batch` — JSON `{ids:[…], action:"delete"|"move"|"tag", folder_id?, tag?}` → `{applied, ok:[…], failed:[{id,error}]}`
  - `mountBulkSelect(root)` in `web/src/islands/bulkselect.ts`

**Why one thin batch endpoint rather than N per-item calls.** A bulk delete of fifty tiles as fifty sequential `fetch`es is fifty round-trips, fifty log lines, and a partial-failure UX that is hard to report. One endpoint loops server-side over the **already-tested** per-item store methods — `SoftDeleteItem` still enforces uploader-or-admin via `ErrForbidden`, so authorization is not re-implemented — and returns a single `{ok, failed}` summary the toolbar shows at once. It adds no new authorization surface: everything it does, a friend could already do one tile at a time.

- [ ] **Step 1: Write the failing test**

Create `internal/web/handlers_batch_test.go`:

```go
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBatchDeleteRemovesItems(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, srv, mediaStore, user.ID, "one", "two", "three")

	ids := []string{items[0].ID, items[1].ID}
	body, _ := json.Marshal(map[string]any{"ids": ids, "action": "delete"})
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/batch", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Applied int      `json:"applied"`
		OK      []string `json:"ok"`
		Failed  []struct {
			ID    string `json:"id"`
			Error string `json:"error"`
		} `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Applied != 2 || len(out.Failed) != 0 {
		t.Fatalf("applied=%d failed=%d, want 2/0", out.Applied, len(out.Failed))
	}
	if _, err := srv.Store.ItemByID(ctx, items[0].ID); err == nil {
		t.Error("item[0] still live after batch delete")
	}
	if _, err := srv.Store.ItemByID(ctx, items[2].ID); err != nil {
		t.Error("item[2] should be untouched")
	}
}

func TestBatchMoveAndTag(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(ctx, "aiden")
	items := seedItems(t, srv, mediaStore, user.ID, "x", "y")
	folder, err := srv.Store.CreateFolder(ctx, 0, "Box")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}

	move, _ := json.Marshal(map[string]any{"ids": []string{items[0].ID, items[1].ID}, "action": "move", "folder_id": folder.ID})
	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/batch", string(move)); rec.Code != http.StatusOK {
		t.Fatalf("move status = %d: %s", rec.Code, rec.Body.String())
	}
	moved, _ := srv.Store.ItemByID(ctx, items[0].ID)
	if moved.FolderID != folder.ID {
		t.Errorf("item[0] folder = %d, want %d", moved.FolderID, folder.ID)
	}

	tag, _ := json.Marshal(map[string]any{"ids": []string{items[0].ID}, "action": "tag", "tag": "batchtag"})
	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/batch", string(tag)); rec.Code != http.StatusOK {
		t.Fatalf("tag status = %d: %s", rec.Code, rec.Body.String())
	}
	tags, _ := srv.Store.ItemTags(ctx, items[0].ID)
	if len(tags) != 1 || tags[0] != "batchtag" {
		t.Errorf("tags = %v, want [batchtag]", tags)
	}
}

func TestBatchRejectsUnknownAction(t *testing.T) {
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	user, _ := srv.Store.UserByUsername(context.Background(), "aiden")
	items := seedItems(t, srv, mediaStore, user.ID, "z")
	body, _ := json.Marshal(map[string]any{"ids": []string{items[0].ID}, "action": "explode"})
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/batch", string(body))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestBatchDeleteReportsForbiddenPerItem(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	// aiden uploads; a second, non-admin user attempts the batch delete.
	uploader := testUser(t, srv, "aiden", "hunter2")
	items := seedItems(t, srv, mediaStore, uploader.ID, "secret")
	cookie := authenticate(t, srv, "mallory") // a different, non-admin user

	body, _ := json.Marshal(map[string]any{"ids": []string{items[0].ID}, "action": "delete"})
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/items/batch", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a per-item failure", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"failed"`) || !strings.Contains(rec.Body.String(), items[0].ID) {
		t.Errorf("expected a per-item failure entry, got %s", rec.Body.String())
	}
	if _, err := srv.Store.ItemByID(ctx, items[0].ID); err != nil {
		t.Error("a forbidden batch delete still removed the item")
	}
}
```

`authenticate` (Plan 2) creates the user it signs in, so `authenticate(t, srv, "mallory")` yields a distinct non-admin. The stray `owner` lookup is discarded; `testUser` then (re)creates "aiden" as the uploader before seeding.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/web/ -run Batch -v`
Expected: FAIL to compile — `undefined: (*Server).handleBatchItems`.

- [ ] **Step 3: Implement the handler**

Create `internal/web/handlers_batch.go`:

```go
package web

import (
	"encoding/json"
	"net/http"
)

// maxBatch bounds one batch request so a stray client cannot ask the server to
// loop over an unbounded id list.
const maxBatch = 500

// handleBatchItems applies one action to many items, reusing the per-item store
// methods (and therefore their authorization) and returning a single summary.
func (s *Server) handleBatchItems(w http.ResponseWriter, r *http.Request) {
	user, ok := CurrentUser(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	var body struct {
		IDs      []string `json:"ids"`
		Action   string   `json:"action"`
		FolderID int64    `json:"folder_id"`
		Tag      string   `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if len(body.IDs) == 0 {
		writeJSONError(w, http.StatusBadRequest, "no_items", "no item ids given")
		return
	}
	if len(body.IDs) > maxBatch {
		writeJSONError(w, http.StatusBadRequest, "too_many", "too many items in one batch")
		return
	}
	switch body.Action {
	case "delete", "move":
	case "tag":
		if body.Tag == "" {
			writeJSONError(w, http.StatusBadRequest, "no_tag", "a tag is required")
			return
		}
	default:
		writeJSONError(w, http.StatusBadRequest, "bad_action", "action must be delete, move or tag")
		return
	}

	okIDs := make([]string, 0, len(body.IDs))
	failed := make([]map[string]any, 0)
	for _, id := range body.IDs {
		var err error
		switch body.Action {
		case "delete":
			err = s.Store.SoftDeleteItem(r.Context(), id, user)
		case "move":
			err = s.Store.MoveItem(r.Context(), id, body.FolderID)
		case "tag":
			err = s.Store.AddItemTag(r.Context(), id, body.Tag)
		}
		if err != nil {
			failed = append(failed, map[string]any{"id": id, "error": err.Error()})
			continue
		}
		okIDs = append(okIDs, id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": len(okIDs), "ok": okIDs, "failed": failed})
}
```

- [ ] **Step 4: Register the route**

In `internal/web/server.go`, after the `/api/tags` and `/api/random` routes, add:

```go
	r.Post("/api/items/batch", s.handleBatchItems)
```

Register it **before** any `/api/items/{id}` pattern is not a concern — chi distinguishes the static `/batch` segment from the `{id}` wildcard regardless of order.

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/web/ -run Batch -v`
Expected: PASS — `TestBatchDeleteRemovesItems`, `TestBatchMoveAndTag`, `TestBatchRejectsUnknownAction`, `TestBatchDeleteReportsForbiddenPerItem`.

- [ ] **Step 6: Write the bulk-select island**

Create `web/src/islands/bulkselect.ts`:

```ts
/**
 * Multi-select toolbar over the grid tiles. In select mode a click toggles a
 * tile instead of opening the lightbox; the toolbar then applies one batch
 * action through POST /api/items/batch.
 */

interface BatchResult {
  applied: number;
  ok: string[];
  failed: { id: string; error: string }[];
}

export function mountBulkSelect(root: HTMLElement): void {
  root.removeAttribute("hidden");
  const toggle = root.querySelector<HTMLInputElement>('[data-action="select-mode"]');
  const count = root.querySelector<HTMLElement>('[data-role="count"]');
  const moveBtn = root.querySelector<HTMLButtonElement>('[data-action="bulk-move"]');
  const tagBtn = root.querySelector<HTMLButtonElement>('[data-action="bulk-tag"]');
  const deleteBtn = root.querySelector<HTMLButtonElement>('[data-action="bulk-delete"]');
  const grid = document.querySelector<HTMLElement>('[data-island="grid"]');
  if (!toggle || !count || !moveBtn || !tagBtn || !deleteBtn || !grid) return;

  const selected = new Set<string>();
  let selecting = false;

  function refresh(): void {
    count!.textContent = `${selected.size} selected`;
    for (const button of [moveBtn!, tagBtn!, deleteBtn!]) button.disabled = selected.size === 0;
  }

  toggle.addEventListener("change", () => {
    selecting = toggle.checked;
    document.body.classList.toggle("selecting", selecting);
    if (!selecting) {
      selected.clear();
      for (const el of grid!.querySelectorAll(".tile--selected")) el.classList.remove("tile--selected");
      refresh();
    }
  });

  // Capture phase so we can claim the click before the lightbox's open handler.
  grid.addEventListener(
    "click",
    (event) => {
      if (!selecting) return;
      const tile = (event.target as HTMLElement | null)?.closest<HTMLElement>("[data-item-id]");
      if (!tile) return;
      event.preventDefault();
      event.stopPropagation();
      const id = tile.dataset.itemId!;
      if (selected.has(id)) {
        selected.delete(id);
        tile.classList.remove("tile--selected");
      } else {
        selected.add(id);
        tile.classList.add("tile--selected");
      }
      refresh();
    },
    true,
  );

  async function apply(payload: Record<string, unknown>): Promise<void> {
    const response = await fetch("/api/items/batch", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ...payload, ids: Array.from(selected) }),
    });
    if (!response.ok) {
      window.alert("That bulk action failed.");
      return;
    }
    const result = (await response.json()) as BatchResult;
    for (const id of result.ok) {
      if (payload.action === "delete" || payload.action === "move") {
        grid!.querySelector(`[data-item-id="${CSS.escape(id)}"]`)?.remove();
      }
    }
    if (result.failed.length > 0) {
      window.alert(`${result.failed.length} item(s) could not be updated.`);
    }
    selected.clear();
    refresh();
  }

  deleteBtn.addEventListener("click", () => {
    if (selected.size === 0) return;
    if (!window.confirm(`Delete ${selected.size} item(s)? They go to the trash.`)) return;
    void apply({ action: "delete" });
  });
  moveBtn.addEventListener("click", () => {
    const raw = window.prompt("Move to folder id (0 for none)");
    if (raw === null) return;
    const folderId = Number(raw);
    if (Number.isNaN(folderId)) return;
    void apply({ action: "move", folder_id: folderId });
  });
  tagBtn.addEventListener("click", () => {
    const tag = window.prompt("Tag to add");
    if (!tag) return;
    void apply({ action: "tag", tag });
  });

  refresh();
}
```

- [ ] **Step 7: Register the island and add styles**

In `web/src/main.ts`:

```ts
import { mountBulkSelect } from "./islands/bulkselect";
```

```ts
registerIsland("bulkselect", mountBulkSelect);
```

Append to `web/src/main.css`:

```css
.bulkbar { display: flex; flex-wrap: wrap; align-items: center; gap: 0.75rem; margin-bottom: 1rem; }
.bulkbar__toggle { display: inline-flex; align-items: center; gap: 0.35rem; }
.bulkbar__count { color: var(--fg-muted); font-size: 0.85rem; }
body.selecting .tile__button { cursor: copy; }
.tile--selected .tile__button { outline: 3px solid var(--accent); outline-offset: -3px; }
```

- [ ] **Step 8: Type-check, build, and run the tests**

Run: `bunx tsc --noEmit && bun run build`
Expected: clean; `main.js` under 50 KB.

Run: `go test ./internal/web/ -run Batch -v`
Expected: PASS (already green from Step 5; re-confirm after the JS changes).

- [ ] **Step 9: Commit**

```bash
git add internal/web/handlers_batch.go internal/web/handlers_batch_test.go internal/web/server.go web/
git commit -m "feat: bulk-select toolbar and the /api/items/batch endpoint"
```

---

### Task 6: Admin DB additions — job listing, manual requeue, user delete/admin toggle

**Files:**
- Create: `internal/db/jobs_admin.go`
- Create: `internal/db/users_admin.go`
- Test: `internal/db/jobs_admin_test.go`, `internal/db/users_admin_test.go`

**Interfaces:**
- Consumes: `db.Store`, `db.ErrNotFound`, `db.JobByID`, `db.Job` (Plans 1–2)
- Produces:
  - `(*Store) ListJobs(ctx context.Context, limit int) ([]*Job, error)` — newest first
  - `(*Store) RequeueJob(ctx context.Context, id int64, runAt time.Time) error` — resets a **failed** job to `queued`, `attempts=0`, error cleared, for the admin retry button
  - `db.ErrJobNotFailed`
  - `(*Store) CountItemsByUploader(ctx context.Context, uploaderID int64) (int, error)` — includes soft-deleted rows
  - `(*Store) DeleteUser(ctx context.Context, id int64) error` — refuses (`ErrUserHasItems`) while the user still owns any item, so no item is orphaned; sessions cascade
  - `(*Store) SetUserAdmin(ctx context.Context, id int64, isAdmin bool) error`
  - `db.ErrUserHasItems`

**Why these are new.** Plans 1–2 shipped `CreateUser`, `ListUsers`, `SetUserPassword` (session-invalidating) and `SetUserAPIKeyHash` (rotation), plus `RetryJob` for the queue's own backoff. The admin page needs three things they did not build: a queue **listing**, a **manual** requeue that gives a dead job a fresh three attempts (distinct from the backoff `RetryJob`), and the destructive user operations — delete and admin-toggle. `DeleteUser` is item-guarded because `items.uploader_id` has no user to fall back to; forcing the admin to clear a friend's uploads first keeps attribution honest.

- [ ] **Step 1: Write the failing jobs test**

Create `internal/db/jobs_admin_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/db/ -run 'ListJobs|Requeue' -v`
Expected: FAIL to compile — `undefined: (*Store).ListJobs`, `undefined: db.ErrJobNotFailed`.

- [ ] **Step 3: Implement the job admin functions**

Create `internal/db/jobs_admin.go`:

```go
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
```

- [ ] **Step 4: Run the jobs test to verify it passes**

Run: `go test ./internal/db/ -run 'ListJobs|Requeue' -v`
Expected: PASS — `TestListJobsIsNewestFirst`, `TestRequeueJobResetsAFailedJob`, `TestRequeueJobRejectsNonFailedAndMissing`.

- [ ] **Step 5: Write the failing users test**

Create `internal/db/users_admin_test.go`:

```go
package db_test

import (
	"context"
	"errors"
	"testing"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

func TestDeleteUserRefusesWhileUserOwnsItems(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user, err := store.CreateUser(ctx, "aiden", "Aiden", "h", "", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := store.CreateItem(ctx, db.NewItem{ContentHash: "h1", Title: "t", Ext: "png", Mime: "image/png", Size: 1, UploaderID: user.ID}); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if err := store.DeleteUser(ctx, user.ID); !errors.Is(err, db.ErrUserHasItems) {
		t.Fatalf("DeleteUser with items = %v, want ErrUserHasItems", err)
	}
}

func TestDeleteUserRemovesAnEmptyUser(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user, _ := store.CreateUser(ctx, "spare", "Spare", "h", "", false)
	if err := store.DeleteUser(ctx, user.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := store.UserByID(ctx, user.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("UserByID after delete = %v, want ErrNotFound", err)
	}
	if err := store.DeleteUser(ctx, 999); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("DeleteUser missing = %v, want ErrNotFound", err)
	}
}

func TestSetUserAdminToggles(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	user, _ := store.CreateUser(ctx, "aiden", "Aiden", "h", "", false)
	if err := store.SetUserAdmin(ctx, user.ID, true); err != nil {
		t.Fatalf("SetUserAdmin: %v", err)
	}
	got, _ := store.UserByID(ctx, user.ID)
	if !got.IsAdmin {
		t.Error("SetUserAdmin(true) did not stick")
	}
	if err := store.SetUserAdmin(ctx, 999, true); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("SetUserAdmin missing = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 6: Run the users test to verify it fails**

Run: `go test ./internal/db/ -run 'DeleteUser|SetUserAdmin' -v`
Expected: FAIL to compile — `undefined: (*Store).DeleteUser`.

- [ ] **Step 7: Implement the user admin functions**

Create `internal/db/users_admin.go`:

```go
package db

import (
	"context"
	"errors"
	"fmt"
)

// ErrUserHasItems means a user could not be deleted because they still own
// items; deleting them would orphan those rows' uploader attribution.
var ErrUserHasItems = errors.New("db: user still owns items")

// CountItemsByUploader counts every item (including soft-deleted) a user owns.
func (s *Store) CountItemsByUploader(ctx context.Context, uploaderID int64) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE uploader_id = ?`, uploaderID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("db: count items by uploader: %w", err)
	}
	return n, nil
}

// DeleteUser removes a user with no items. Sessions cascade via the schema's
// foreign key (see TestDeletingUserCascadesSessions).
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	n, err := s.CountItemsByUploader(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrUserHasItems
	}
	res, err := s.DB.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete user: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: delete user rows: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserAdmin grants or revokes admin.
func (s *Store) SetUserAdmin(ctx context.Context, id int64, isAdmin bool) error {
	adminInt := 0
	if isAdmin {
		adminInt = 1
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE users SET is_admin = ? WHERE id = ?`, adminInt, id)
	if err != nil {
		return fmt.Errorf("db: set user admin: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: set user admin rows: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}
```

- [ ] **Step 8: Run the users test to verify it passes**

Run: `go test ./internal/db/ -run 'DeleteUser|SetUserAdmin' -v`
Expected: PASS — `TestDeleteUserRefusesWhileUserOwnsItems`, `TestDeleteUserRemovesAnEmptyUser`, `TestSetUserAdminToggles`.

- [ ] **Step 9: Commit**

```bash
git add internal/db/jobs_admin.go internal/db/jobs_admin_test.go internal/db/users_admin.go internal/db/users_admin_test.go
git commit -m "feat(db): admin job listing/requeue and guarded user delete/admin toggle"
```

---

### Task 7: Admin gate, page, dependency banner, and nav link

**Files:**
- Create: `internal/web/middleware_admin.go`
- Create: `internal/web/handlers_admin.go` (the page; endpoints land in Tasks 8–10)
- Modify: `internal/web/server.go` (register `/admin`)
- Create: `web/templates/pages/admin.html`
- Modify: `web/templates/base.html` (admin nav link)
- Test: `internal/web/handlers_admin_test.go`

**Interfaces:**
- Consumes: `db.ListUsers`, `db.CountItemsByUploader`, `db.SettingAll`, `db.ListJobs`, `db.ListDeletedItems`, `deps.Status`, `deps.AllOK`, `ingest.Extractors`, `s.itemsPayload`, `CurrentUser`, `Renderer.Render` (Plans 1–3, Task 6)
- Produces:
  - `(*Server) requireAdmin(next http.Handler) http.Handler` — 403 (JSON on `/api/`, plain text otherwise) for non-admins
  - `(*Server) handleAdmin(w http.ResponseWriter, r *http.Request)` on `GET /admin`
  - `adminData`, `adminUserRow`, `adminSetting` structs
  - the `admin` page template and the admin nav link, shown only to admins

**Server-side gating, not a hidden link.** `requireAdmin` is the access control; the nav link is only a convenience. Every admin endpoint in Tasks 8–10 is registered with `r.With(s.requireAdmin)`, so a non-admin who guesses the URL still gets 403.

- [ ] **Step 1: Write the failing test**

Create `internal/web/handlers_admin_test.go`:

```go
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boobies-media/internal/deps"
)

// adminCookie signs a user in and promotes them to admin.
func adminCookie(t *testing.T, srv *Server, username string) *http.Cookie {
	t.Helper()
	cookie := authenticate(t, srv, username)
	user, err := srv.Store.UserByUsername(context.Background(), username)
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	if err := srv.Store.SetUserAdmin(context.Background(), user.ID, true); err != nil {
		t.Fatalf("SetUserAdmin: %v", err)
	}
	return cookie
}

func TestAdminPageForbiddenToNonAdmin(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden") // non-admin

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin /admin status = %d, want 403", rec.Code)
	}
}

func TestAdminPageRendersForAdmin(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	srv.Deps = []deps.Status{{Name: "yt-dlp", OK: false, Err: "not found on PATH"}}
	cookie := adminCookie(t, srv, "aiden")

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin /admin status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Users", "Settings", "Job queue", "Trash",
		"yt-dlp",              // the dependency banner
		"not found on PATH",   // the failing dep's error
		`name="ytdlp_format"`, // a settings field
		`data-extractor="twitter"`, // a test-ingest button
	} {
		if !strings.Contains(body, want) {
			t.Errorf("admin page missing %q", want)
		}
	}
}

func TestAdminNavLinkOnlyForAdmins(t *testing.T) {
	srv, _, _ := mediaTestServer(t)

	admin := adminCookie(t, srv, "boss")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(admin)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `href="/admin"`) {
		t.Error("admin does not see the Admin nav link")
	}

	plain := authenticate(t, srv, "aiden")
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(plain)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), `href="/admin"`) {
		t.Error("a non-admin sees the Admin nav link")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/web/ -run Admin -v`
Expected: FAIL to compile — `undefined: (*Server).requireAdmin`, `undefined: (*Server).handleAdmin`.

- [ ] **Step 3: Implement the admin middleware**

Create `internal/web/middleware_admin.go`:

```go
package web

import (
	"net/http"
	"strings"
)

// requireAdmin lets only admins through. It is the access control for /admin
// and every /api/admin/* route — never the visibility of a nav link.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := CurrentUser(r)
		if !ok {
			// The gate normally handles anonymous requests; this is defence in depth.
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			} else {
				http.Redirect(w, r, "/login", http.StatusFound)
			}
			return
		}
		if !user.IsAdmin {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSONError(w, http.StatusForbidden, "forbidden", "admin only")
			} else {
				http.Error(w, "Forbidden — this page is for admins.", http.StatusForbidden)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 4: Implement the admin page handler**

Create `internal/web/handlers_admin.go`:

```go
package web

import (
	"net/http"

	"boobies-media/internal/db"
	"boobies-media/internal/deps"
	"boobies-media/internal/ingest"
)

type adminUserRow struct {
	ID          int64
	Username    string
	DisplayName string
	IsAdmin     bool
	ItemCount   int
	HasKey      bool
}

type adminSetting struct {
	Key   string
	Label string
	Value string
}

type adminData struct {
	Users      []adminUserRow
	Settings   []adminSetting
	Jobs       []*db.Job
	Trash      []map[string]any
	Deps       []deps.Status
	DepsAllOK  bool
	Extractors []string
}

// handleAdmin renders the admin dashboard. Every mutation is a JSON endpoint
// (Tasks 8–10) driven by the admin island; this handler is read-only.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	user, _ := CurrentUser(r)
	ctx := r.Context()

	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rows := make([]adminUserRow, 0, len(users))
	for _, u := range users {
		count, err := s.Store.CountItemsByUploader(ctx, u.ID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		rows = append(rows, adminUserRow{
			ID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
			IsAdmin: u.IsAdmin, ItemCount: count, HasKey: u.APIKeyHash != "",
		})
	}

	all, err := s.Store.SettingAll(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	settings := []adminSetting{
		{"auto_webp", "Auto WebP conversion (on/off)", all["auto_webp"]},
		{"upload_max_bytes", "Total upload size cap (bytes)", all["upload_max_bytes"]},
		{"upload_chunk_bytes", "Upload chunk size (bytes) — must stay under 100 MB", all["upload_chunk_bytes"]},
		{"download_max_bytes", "Download size cap (bytes)", all["download_max_bytes"]},
		{"ytdlp_format", "yt-dlp format string", all["ytdlp_format"]},
		{"cookies_twitter", "Twitter/X cookie file path", all["cookies_twitter"]},
		{"cookies_youtube", "YouTube cookie file path", all["cookies_youtube"]},
		{"cookies_tiktok", "TikTok cookie file path", all["cookies_tiktok"]},
		{"cookies_medal", "Medal cookie file path", all["cookies_medal"]},
		{"min_free_disk_bytes", "Minimum free disk (bytes)", all["min_free_disk_bytes"]},
	}

	jobs, err := s.Store.ListJobs(ctx, 50)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	trashItems, err := s.Store.ListDeletedItems(ctx, 100)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	trash, err := s.itemsPayload(r, trashItems)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := PageData{
		Title: "Admin",
		User:  user,
		Data: adminData{
			Users:      rows,
			Settings:   settings,
			Jobs:       jobs,
			Trash:      trash,
			Deps:       s.Deps,
			DepsAllOK:  deps.AllOK(s.Deps),
			Extractors: ingest.Extractors,
		},
	}
	if err := s.Renderer.Render(w, http.StatusOK, "admin", data); err != nil {
		s.serverError(w, r, err)
	}
}
```

- [ ] **Step 5: Register the route**

In `internal/web/server.go`, after the item batch route from Task 5, add:

```go
	r.With(s.requireAdmin).Get("/admin", s.handleAdmin)
```

- [ ] **Step 6: Add the admin template**

Create `web/templates/pages/admin.html`:

```html
{{define "title"}}Admin{{end}}
{{define "content"}}
{{$data := .Data}}
<section class="admin" data-island="admin">
  <h1>Admin</h1>

  {{if not $data.DepsAllOK}}
  <div class="banner banner--warn" role="alert">
    <strong>Some external tools are unavailable.</strong> Related jobs will fail with a clear error until they are installed.
    <ul>
      {{range $data.Deps}}{{if not .OK}}<li>{{.Name}} — {{.Err}}</li>{{end}}{{end}}
    </ul>
  </div>
  {{end}}

  <details class="admin__deps">
    <summary>Dependency versions</summary>
    <ul>
      {{range $data.Deps}}
      <li>{{.Name}}: {{if .OK}}{{.Version}} ({{.Path}}){{else}}<span class="admin__bad">missing — {{.Err}}</span>{{end}}</li>
      {{end}}
    </ul>
  </details>

  <h2>Users</h2>
  <p class="admin__newkey" data-role="new-key" hidden></p>
  <table class="admin__table">
    <thead><tr><th>Username</th><th>Display</th><th>Admin</th><th>Items</th><th>API key</th><th>Actions</th></tr></thead>
    <tbody>
      {{range $data.Users}}
      <tr data-user-id="{{.ID}}" data-is-admin="{{.IsAdmin}}">
        <td>{{.Username}}</td>
        <td>{{.DisplayName}}</td>
        <td>{{if .IsAdmin}}yes{{else}}no{{end}}</td>
        <td>{{.ItemCount}}</td>
        <td>{{if .HasKey}}set{{else}}—{{end}}</td>
        <td class="admin__actions">
          <button type="button" data-action="toggle-admin">{{if .IsAdmin}}Revoke admin{{else}}Make admin{{end}}</button>
          <button type="button" data-action="reset-password">Reset password</button>
          <button type="button" data-action="rotate-key">Rotate key</button>
          <button type="button" data-action="delete-user" class="danger">Delete</button>
        </td>
      </tr>
      {{end}}
    </tbody>
  </table>

  <form class="admin__create" data-role="create-user">
    <h3>Add a friend</h3>
    <input name="username" placeholder="username" required>
    <input name="display_name" placeholder="display name">
    <input name="password" type="password" placeholder="password" required>
    <label><input type="checkbox" name="is_admin"> admin</label>
    <button type="submit">Create</button>
  </form>

  <h2>Settings</h2>
  <form class="admin__settings" data-role="settings">
    {{range $data.Settings}}
    <label class="admin__setting">
      <span>{{.Label}}</span>
      <input name="{{.Key}}" value="{{.Value}}">
    </label>
    {{end}}
    <button type="submit">Save settings</button>
  </form>

  <h2>Ingest self-test</h2>
  <p class="admin__hint">Enqueue a known-good link per source to see whether cookies and tools still work.</p>
  <div class="admin__tests">
    {{range $data.Extractors}}
    <button type="button" data-action="test-ingest" data-extractor="{{.}}">Test {{.}}</button>
    {{end}}
  </div>
  <p class="admin__testresult" data-role="test-result" hidden></p>

  <h2>Job queue</h2>
  <table class="admin__table">
    <thead><tr><th>ID</th><th>Type</th><th>Status</th><th>Attempts</th><th>Error</th><th></th></tr></thead>
    <tbody>
      {{range $data.Jobs}}
      <tr data-job-id="{{.ID}}">
        <td>{{.ID}}</td>
        <td>{{.Type}}</td>
        <td>{{.Status}}</td>
        <td>{{.Attempts}}</td>
        <td class="admin__err">{{.Error}}</td>
        <td>{{if eq .Status "failed"}}<button type="button" data-action="retry-job">Retry</button>{{end}}</td>
      </tr>
      {{end}}
    </tbody>
  </table>

  <h2>Trash</h2>
  <table class="admin__table">
    <thead><tr><th>Title</th><th>Item</th><th>Actions</th></tr></thead>
    <tbody>
      {{range $data.Trash}}
      <tr data-item-id="{{.id}}">
        <td>{{.title}}</td>
        <td><a href="/s/{{.id}}" target="_blank" rel="noopener">{{.id}}</a></td>
        <td class="admin__actions">
          <button type="button" data-action="restore-item">Restore</button>
          <button type="button" data-action="purge-item" class="danger">Purge</button>
        </td>
      </tr>
      {{end}}
    </tbody>
  </table>
</section>
{{end}}
```

- [ ] **Step 7: Add the admin nav link**

In `web/templates/base.html`, change the `topbar__user` block so admins get an Admin link:

```html
  <div class="topbar__user">
    <span>{{.User.DisplayName}}</span>
    {{if .User.IsAdmin}}<a href="/admin" class="topbar__admin">Admin</a>{{end}}
    <form method="post" action="/logout"><button type="submit">Sign out</button></form>
  </div>
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./internal/web/ -run Admin -v`
Expected: PASS — `TestAdminPageForbiddenToNonAdmin`, `TestAdminPageRendersForAdmin`, `TestAdminNavLinkOnlyForAdmins`.

Run: `go test ./internal/web/ -run Renderer -v`
Expected: still PASS — the renderer now also loads `admin.html` against `base.html`.

- [ ] **Step 9: Commit**

```bash
git add internal/web/middleware_admin.go internal/web/handlers_admin.go internal/web/server.go web/templates/pages/admin.html web/templates/base.html internal/web/handlers_admin_test.go
git commit -m "feat(web): admin dashboard, server-side admin gate and dependency banner"
```

---

### Task 8: Admin user-management endpoints

**Files:**
- Create: `internal/web/handlers_admin_users.go`
- Modify: `internal/web/server.go` (register the routes)
- Test: `internal/web/handlers_admin_users_test.go`

**Interfaces:**
- Consumes: `auth.HashPassword`, `auth.NewAPIKey`, `auth.HashToken` (Plan 1); `db.CreateUser`, `db.DeleteUser`, `db.SetUserAdmin`, `db.SetUserPassword`, `db.SetUserAPIKeyHash`, `db.ErrDuplicateUser`, `db.ErrUserHasItems`, `db.ErrNotFound` (Plans 1, 6); `CurrentUser`, `requireAdmin`
- Produces:
  - `POST /api/admin/users` — `{username, display_name?, password, is_admin?}` → `201 {user, api_key}` (plaintext key shown **once**)
  - `PATCH /api/admin/users/{id}` — `{is_admin}` 
  - `DELETE /api/admin/users/{id}`
  - `POST /api/admin/users/{id}/password` — `{password}` (invalidates that user's sessions)
  - `POST /api/admin/users/{id}/apikey` — rotate → `200 {api_key}` (plaintext once)

Two self-lockout guards: an admin cannot delete their own account nor revoke their own admin, so the last operator cannot strand themselves.

- [ ] **Step 1: Write the failing test**

Create `internal/web/handlers_admin_users_test.go`:

```go
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestAdminCreateUserReturnsAPIKeyOnce(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/users",
		`{"username":"newbie","display_name":"New Bie","password":"hunter2","is_admin":false}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		User   struct{ Username string `json:"username"` } `json:"user"`
		APIKey string                                       `json:"api_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.User.Username != "newbie" || out.APIKey == "" {
		t.Fatalf("unexpected create response: %s", rec.Body.String())
	}
	// The created user can actually authenticate with the plaintext key.
	if _, err := srv.Store.UserByUsername(context.Background(), "newbie"); err != nil {
		t.Errorf("created user not found: %v", err)
	}
}

func TestAdminCreateUserRejectsDuplicate(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")
	_ = testUser(t, srv, "dup", "x")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/users", `{"username":"dup","password":"hunter2"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate create status = %d, want 409", rec.Code)
	}
}

func TestAdminToggleAdminAndSelfGuard(t *testing.T) {
	ctx := context.Background()
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")
	target := testUser(t, srv, "friend", "x")

	rec := apiRequest(t, srv, cookie, http.MethodPatch, "/api/admin/users/"+itoa(target.ID), `{"is_admin":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("promote status = %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := srv.Store.UserByID(ctx, target.ID)
	if !got.IsAdmin {
		t.Error("promote did not stick")
	}

	// The acting admin cannot revoke their own admin.
	me, _ := srv.Store.UserByUsername(ctx, "boss")
	rec = apiRequest(t, srv, cookie, http.MethodPatch, "/api/admin/users/"+itoa(me.ID), `{"is_admin":false}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("self-demote status = %d, want 400", rec.Code)
	}
}

func TestAdminDeleteUserGuards(t *testing.T) {
	ctx := context.Background()
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")

	// Cannot delete yourself.
	me, _ := srv.Store.UserByUsername(ctx, "boss")
	if rec := apiRequest(t, srv, cookie, http.MethodDelete, "/api/admin/users/"+itoa(me.ID), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("self-delete status = %d, want 400", rec.Code)
	}

	// Can delete an item-free friend.
	spare := testUser(t, srv, "spare", "x")
	if rec := apiRequest(t, srv, cookie, http.MethodDelete, "/api/admin/users/"+itoa(spare.ID), ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete spare status = %d, want 204", rec.Code)
	}
}

func TestAdminResetPasswordAndRotateKey(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")
	target := testUser(t, srv, "friend", "x")

	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/users/"+itoa(target.ID)+"/password", `{"password":"brandnew"}`); rec.Code != http.StatusOK {
		t.Errorf("reset password status = %d, want 200", rec.Code)
	}

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/users/"+itoa(target.ID)+"/apikey", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate key status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct{ APIKey string `json:"api_key"` }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.APIKey == "" {
		t.Error("rotate key returned no plaintext key")
	}
}

func TestAdminUserEndpointsForbiddenToNonAdmin(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden") // non-admin
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/users", `{"username":"x","password":"y"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin create status = %d, want 403", rec.Code)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/web/ -run AdminCreateUser -v`
Expected: FAIL to compile — `undefined: (*Server).handleCreateUser`.

- [ ] **Step 3: Implement the endpoints**

Create `internal/web/handlers_admin_users.go`:

```go
package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"boobies-media/internal/auth"
	"boobies-media/internal/db"
)

func adminUserID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "user id must be a number")
		return 0, false
	}
	return id, true
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		IsAdmin     bool   `json:"is_admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.Username == "" || body.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_fields", "username and password are required")
		return
	}
	display := body.DisplayName
	if display == "" {
		display = body.Username
	}
	passwordHash, err := auth.HashPassword(body.Password)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	key, err := auth.NewAPIKey()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	user, err := s.Store.CreateUser(r.Context(), body.Username, display, passwordHash, auth.HashToken(key), body.IsAdmin)
	if err != nil {
		if errors.Is(err, db.ErrDuplicateUser) {
			writeJSONError(w, http.StatusConflict, "duplicate_user", "that username is taken")
			return
		}
		s.serverError(w, r, err)
		return
	}
	// The plaintext key is shown once here and never again — only its hash is stored.
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":    map[string]any{"id": user.ID, "username": user.Username, "is_admin": user.IsAdmin},
		"api_key": key,
	})
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := adminUserID(w, r)
	if !ok {
		return
	}
	var body struct {
		IsAdmin *bool `json:"is_admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.IsAdmin != nil {
		me, _ := CurrentUser(r)
		if me != nil && me.ID == id && !*body.IsAdmin {
			writeJSONError(w, http.StatusBadRequest, "self_lockout", "you cannot revoke your own admin")
			return
		}
		if err := s.Store.SetUserAdmin(r.Context(), id, *body.IsAdmin); err != nil {
			s.writeUserError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := adminUserID(w, r)
	if !ok {
		return
	}
	me, _ := CurrentUser(r)
	if me != nil && me.ID == id {
		writeJSONError(w, http.StatusBadRequest, "self_delete", "you cannot delete your own account")
		return
	}
	if err := s.Store.DeleteUser(r.Context(), id); err != nil {
		s.writeUserError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := adminUserID(w, r)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "a new password is required")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.Store.SetUserPassword(r.Context(), id, hash); err != nil {
		s.writeUserError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRotateKey(w http.ResponseWriter, r *http.Request) {
	id, ok := adminUserID(w, r)
	if !ok {
		return
	}
	key, err := auth.NewAPIKey()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.Store.SetUserAPIKeyHash(r.Context(), id, auth.HashToken(key)); err != nil {
		s.writeUserError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_key": key})
}

// writeUserError maps user-store sentinels to status codes.
func (s *Server) writeUserError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "not_found", "no such user")
	case errors.Is(err, db.ErrUserHasItems):
		writeJSONError(w, http.StatusConflict, "user_has_items", "delete or reassign this user's items first")
	default:
		s.serverError(w, r, err)
	}
}
```

- [ ] **Step 4: Register the routes**

In `internal/web/server.go`, after the `/admin` route from Task 7, add:

```go
	r.With(s.requireAdmin).Post("/api/admin/users", s.handleCreateUser)
	r.With(s.requireAdmin).Patch("/api/admin/users/{id}", s.handleUpdateUser)
	r.With(s.requireAdmin).Delete("/api/admin/users/{id}", s.handleDeleteUser)
	r.With(s.requireAdmin).Post("/api/admin/users/{id}/password", s.handleResetPassword)
	r.With(s.requireAdmin).Post("/api/admin/users/{id}/apikey", s.handleRotateKey)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/web/ -run AdminCreateUser -v` then `go test ./internal/web/ -run 'Admin(Toggle|Delete|Reset|User)' -v`
Expected: PASS — `TestAdminCreateUserReturnsAPIKeyOnce`, `TestAdminCreateUserRejectsDuplicate`, `TestAdminToggleAdminAndSelfGuard`, `TestAdminDeleteUserGuards`, `TestAdminResetPasswordAndRotateKey`, `TestAdminUserEndpointsForbiddenToNonAdmin`.

- [ ] **Step 6: Commit**

```bash
git add internal/web/handlers_admin_users.go internal/web/handlers_admin_users_test.go internal/web/server.go
git commit -m "feat(web): admin user management endpoints with self-lockout guards"
```

---

### Task 9: Admin settings save and per-source test-ingest

**Files:**
- Create: `internal/web/handlers_admin_settings.go`
- Modify: `internal/web/server.go` (register the routes)
- Test: `internal/web/handlers_admin_settings_test.go`

**Interfaces:**
- Consumes: `db.SettingSet`, `s.requireQueue`, `(*jobs.Queue).Enqueue`, `jobs.TypeIngestURL`, `ingest.URLJob`, `ingest.Extractors`, `CurrentUser` (Plans 1–3)
- Produces:
  - `POST /api/admin/settings` — flat JSON `{key: value, …}`; only the known settings keys are writable, and byte-count keys must parse as integers
  - `POST /api/admin/test-ingest` — `{extractor, url?}` → `202 {job_id, extractor, status}`; enqueues an `ingest_url` job for a known-good (or admin-supplied) URL so cookie/tool breakage is visible

The settings key allowlist means a typo or a hostile client cannot write arbitrary rows into `settings`; the numeric guard means a non-number never lands in `upload_max_bytes` where a later `strconv` would choke.

- [ ] **Step 1: Write the failing test**

Create `internal/web/handlers_admin_settings_test.go`:

```go
package web

import (
	"context"
	"net/http"
	"testing"

	"boobies-media/internal/config"
	"boobies-media/internal/dbtest"
	"boobies-media/internal/jobs"
	"boobies-media/internal/media"
)

// adminQueueServer builds an admin-capable server wired to a real queue so the
// test-ingest endpoint can enqueue.
func adminQueueServer(t *testing.T) (*Server, *jobs.Queue) {
	t.Helper()
	cfg, err := config.Load([]string{"-data", t.TempDir(), "-insecure-cookies"}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	store := dbtest.New(t)
	queue := jobs.New(store, 1)
	mediaStore := media.NewStore(cfg, store, queue)
	srv, err := New(cfg, store, nil, WithMedia(mediaStore), WithQueue(queue))
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return srv, queue
}

func TestAdminSaveSettingsUpdatesValues(t *testing.T) {
	ctx := context.Background()
	srv, _ := adminQueueServer(t)
	cookie := adminCookie(t, srv, "boss")

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings",
		`{"auto_webp":"off","upload_max_bytes":"1048576","cookies_twitter":"/data/cookies/twitter.txt"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got, err := srv.Store.SettingGet(ctx, "auto_webp")
	if err != nil || got != "off" {
		t.Errorf("auto_webp = %q (err %v), want off", got, err)
	}
	got, _ = srv.Store.SettingGet(ctx, "cookies_twitter")
	if got != "/data/cookies/twitter.txt" {
		t.Errorf("cookies_twitter = %q", got)
	}
}

func TestAdminSaveSettingsRejectsUnknownKey(t *testing.T) {
	srv, _ := adminQueueServer(t)
	cookie := adminCookie(t, srv, "boss")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{"totally_made_up":"1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown-key status = %d, want 400", rec.Code)
	}
}

func TestAdminSaveSettingsRejectsNonNumericCap(t *testing.T) {
	srv, _ := adminQueueServer(t)
	cookie := adminCookie(t, srv, "boss")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/settings", `{"upload_max_bytes":"lots"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-numeric cap status = %d, want 400", rec.Code)
	}
}

func TestAdminTestIngestEnqueuesAJob(t *testing.T) {
	ctx := context.Background()
	srv, _ := adminQueueServer(t)
	cookie := adminCookie(t, srv, "boss")

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/test-ingest", `{"extractor":"youtube"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	jobsList, err := srv.Store.ListJobs(ctx, 10)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobsList) != 1 || jobsList[0].Type != jobs.TypeIngestURL {
		t.Fatalf("expected one queued ingest_url job, got %+v", jobsList)
	}
}

func TestAdminTestIngestRejectsUnknownExtractor(t *testing.T) {
	srv, _ := adminQueueServer(t)
	cookie := adminCookie(t, srv, "boss")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/test-ingest", `{"extractor":"myspace"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown extractor status = %d, want 400", rec.Code)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/web/ -run 'AdminSaveSettings|AdminTestIngest' -v`
Expected: FAIL to compile — `undefined: (*Server).handleSaveSettings`.

- [ ] **Step 3: Implement the endpoints**

Create `internal/web/handlers_admin_settings.go`:

```go
package web

import (
	"encoding/json"
	"net/http"
	"strconv"

	"boobies-media/internal/ingest"
	"boobies-media/internal/jobs"
)

// settableKeys is the exact set of settings the admin form may write.
var settableKeys = map[string]bool{
	"auto_webp":           true,
	"upload_max_bytes":    true,
	"upload_chunk_bytes":  true,
	"download_max_bytes":  true,
	"ytdlp_format":        true,
	"cookies_twitter":     true,
	"cookies_youtube":     true,
	"cookies_tiktok":      true,
	"cookies_medal":       true,
	"min_free_disk_bytes": true,
}

// numericSettingKeys must parse as an integer number of bytes.
var numericSettingKeys = map[string]bool{
	"upload_max_bytes":    true,
	"upload_chunk_bytes":  true,
	"download_max_bytes":  true,
	"min_free_disk_bytes": true,
}

// testIngestURLs are stable, well-known links per extractor. The admin can
// override with an explicit url in the request body.
var testIngestURLs = map[string]string{
	"twitter": "https://twitter.com/jack/status/20",
	"youtube": "https://www.youtube.com/watch?v=aqz-KE-bpKQ",
	"tiktok":  "https://www.tiktok.com/@tiktok/video/7106594312292453675",
	"medal":   "https://medal.tv/games/valorant/clips/1",
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if len(body) == 0 {
		writeJSONError(w, http.StatusBadRequest, "no_settings", "no settings given")
		return
	}
	for key, value := range body {
		if !settableKeys[key] {
			writeJSONError(w, http.StatusBadRequest, "unknown_setting", "unknown setting: "+key)
			return
		}
		if numericSettingKeys[key] {
			if _, err := strconv.ParseInt(value, 10, 64); err != nil {
				writeJSONError(w, http.StatusBadRequest, "bad_number", key+" must be an integer number of bytes")
				return
			}
		}
		if key == "auto_webp" && value != "on" && value != "off" {
			writeJSONError(w, http.StatusBadRequest, "bad_value", "auto_webp must be on or off")
			return
		}
	}
	for key, value := range body {
		if err := s.Store.SettingSet(r.Context(), key, value); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTestIngest(w http.ResponseWriter, r *http.Request) {
	if !s.requireQueue(w, r) {
		return
	}
	user, ok := CurrentUser(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	var body struct {
		Extractor string `json:"extractor"`
		URL       string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if !isExtractor(body.Extractor) {
		writeJSONError(w, http.StatusBadRequest, "bad_extractor", "unknown extractor")
		return
	}
	url := body.URL
	if url == "" {
		url = testIngestURLs[body.Extractor]
	}
	if url == "" {
		writeJSONError(w, http.StatusBadRequest, "no_url", "no test URL for that extractor")
		return
	}
	jobID, err := s.Queue.Enqueue(r.Context(), jobs.TypeIngestURL, ingest.URLJob{URL: url, UploaderID: user.ID})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID, "extractor": body.Extractor, "status": "queued"})
}

func isExtractor(name string) bool {
	for _, e := range ingest.Extractors {
		if e == name {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Register the routes**

In `internal/web/server.go`, after the admin user routes from Task 8, add:

```go
	r.With(s.requireAdmin).Post("/api/admin/settings", s.handleSaveSettings)
	r.With(s.requireAdmin).Post("/api/admin/test-ingest", s.handleTestIngest)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/web/ -run 'AdminSaveSettings|AdminTestIngest' -v`
Expected: PASS — `TestAdminSaveSettingsUpdatesValues`, `TestAdminSaveSettingsRejectsUnknownKey`, `TestAdminSaveSettingsRejectsNonNumericCap`, `TestAdminTestIngestEnqueuesAJob`, `TestAdminTestIngestRejectsUnknownExtractor`.

- [ ] **Step 6: Commit**

```bash
git add internal/web/handlers_admin_settings.go internal/web/handlers_admin_settings_test.go internal/web/server.go
git commit -m "feat(web): admin settings save and per-source test-ingest"
```

---

### Task 10: Job retry and trash restore/purge endpoints

**Files:**
- Create: `internal/web/handlers_admin_jobs.go`
- Modify: `internal/web/server.go` (register the routes)
- Test: `internal/web/handlers_admin_jobs_test.go`

**Interfaces:**
- Consumes: `db.RequeueJob`, `db.RestoreItem`, `db.ErrJobNotFailed`, `db.ErrNotFound`, `media.Store.Purge`, `s.requireMedia`, `s.Now` (Plans 1–2, Task 6)
- Produces:
  - `POST /api/jobs/{id}/retry` — requeue a failed job (`404` missing, `409` not-failed)
  - `POST /api/admin/items/{id}/restore` — undelete a soft-deleted item
  - `DELETE /api/admin/items/{id}/purge` — hard-delete and unlink blob (refcount-safe via `media.Store.Purge`)

`POST /api/jobs/{id}/retry` is the endpoint the spec calls for; it is admin-gated because only the admin page surfaces the queue. Purge goes through `media.Store.Purge`, which only unlinks the blob when no other live item shares the content hash — the dedup-safe deletion Plan 2 built and tested.

- [ ] **Step 1: Write the failing test**

Create `internal/web/handlers_admin_jobs_test.go`:

```go
package web

import (
	"context"
	"net/http"
	"testing"
)

func TestRetryJobRequeuesAFailedJob(t *testing.T) {
	ctx := context.Background()
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")

	id, err := srv.Store.EnqueueJob(ctx, "ingest_url", []byte(`{}`), srv.Now())
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if err := srv.Store.FailJob(ctx, id, "boom"); err != nil {
		t.Fatalf("FailJob: %v", err)
	}

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/jobs/"+itoa(id)+"/retry", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d: %s", rec.Code, rec.Body.String())
	}
	job, _ := srv.Store.JobByID(ctx, id)
	if job.Status != "queued" {
		t.Errorf("job status = %q, want queued", job.Status)
	}
}

func TestRetryJobRejectsNonFailedAndMissing(t *testing.T) {
	ctx := context.Background()
	srv, _, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")

	id, _ := srv.Store.EnqueueJob(ctx, "probe", []byte(`{}`), srv.Now()) // still queued
	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/jobs/"+itoa(id)+"/retry", ""); rec.Code != http.StatusConflict {
		t.Errorf("retry queued job status = %d, want 409", rec.Code)
	}
	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/jobs/999/retry", ""); rec.Code != http.StatusNotFound {
		t.Errorf("retry missing job status = %d, want 404", rec.Code)
	}
}

func TestRestoreAndPurgeItem(t *testing.T) {
	ctx := context.Background()
	srv, mediaStore, _ := mediaTestServer(t)
	cookie := adminCookie(t, srv, "boss")
	owner, _ := srv.Store.UserByUsername(ctx, "boss")
	item := storeBlobFor(t, srv, mediaStore, owner.ID, pngTestBytes, "a.png")

	if err := srv.Store.SoftDeleteItem(ctx, item.ID, owner); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}
	// Restore.
	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/admin/items/"+item.ID+"/restore", ""); rec.Code != http.StatusOK {
		t.Fatalf("restore status = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := srv.Store.ItemByID(ctx, item.ID); err != nil {
		t.Errorf("item not live after restore: %v", err)
	}

	// Soft delete again, then purge.
	if err := srv.Store.SoftDeleteItem(ctx, item.ID, owner); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}
	if rec := apiRequest(t, srv, cookie, http.MethodDelete, "/api/admin/items/"+item.ID+"/purge", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("purge status = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := srv.Store.ItemByIDIncludingDeleted(ctx, item.ID); err == nil {
		t.Error("item still present after purge")
	}
}

func TestJobRetryForbiddenToNonAdmin(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	cookie := authenticate(t, srv, "aiden")
	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/jobs/1/retry", "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin retry status = %d, want 403", rec.Code)
	}
}
```

`storeBlobFor` is a small variant of Plan 2's `storeBlob` that uploads for a *given* uploader id rather than creating its own "aiden". Add it to `internal/web/handlers_admin_jobs_test.go`:

```go
func storeBlobFor(t *testing.T, srv *Server, mediaStore *media.Store, uploaderID int64, payload []byte, filename string) *db.Item {
	t.Helper()
	media.StubTools(t, map[string]string{})
	res, err := mediaStore.Save(context.Background(), media.SaveRequest{
		Reader: bytesReader(payload), Filename: filename, UploaderID: uploaderID})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return res.Item
}
```

Add the two helper imports at the top of the file — `"bytes"`, `"boobies-media/internal/db"`, `"boobies-media/internal/media"` — and a one-line `bytesReader` shim:

```go
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/web/ -run 'RetryJob|RestoreAndPurge|JobRetry' -v`
Expected: FAIL to compile — `undefined: (*Server).handleRetryJob`.

- [ ] **Step 3: Implement the endpoints**

Create `internal/web/handlers_admin_jobs.go`:

```go
package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"boobies-media/internal/db"
)

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "job id must be a number")
		return
	}
	if err := s.Store.RequeueJob(r.Context(), id, s.Now()); err != nil {
		switch {
		case errors.Is(err, db.ErrNotFound):
			writeJSONError(w, http.StatusNotFound, "not_found", "no such job")
		case errors.Is(err, db.ErrJobNotFailed):
			writeJSONError(w, http.StatusConflict, "not_failed", "only a failed job can be retried")
		default:
			s.serverError(w, r, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRestoreItem(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.Store.RestoreItem(r.Context(), id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "no such item")
			return
		}
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePurgeItem(w http.ResponseWriter, r *http.Request) {
	if !s.requireMedia(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	// Purge unlinks the blob only when no other live item shares the hash.
	if err := s.Media.Purge(r.Context(), id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "no such item")
			return
		}
		s.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Register the routes**

In `internal/web/server.go`, after the admin settings routes from Task 9, add:

```go
	r.With(s.requireAdmin).Post("/api/jobs/{id}/retry", s.handleRetryJob)
	r.With(s.requireAdmin).Post("/api/admin/items/{id}/restore", s.handleRestoreItem)
	r.With(s.requireAdmin).Delete("/api/admin/items/{id}/purge", s.handlePurgeItem)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/web/ -run 'RetryJob|RestoreAndPurge|JobRetry' -v`
Expected: PASS — `TestRetryJobRequeuesAFailedJob`, `TestRetryJobRejectsNonFailedAndMissing`, `TestRestoreAndPurgeItem`, `TestJobRetryForbiddenToNonAdmin`.

- [ ] **Step 6: Commit**

```bash
git add internal/web/handlers_admin_jobs.go internal/web/handlers_admin_jobs_test.go internal/web/server.go
git commit -m "feat(web): job retry and trash restore/purge endpoints"
```

---

### Task 11: Admin and embed islands

**Files:**
- Create: `web/src/islands/admin.ts`
- Create: `web/src/islands/copy.ts`
- Modify: `web/src/main.ts` (register both)
- Modify: `web/src/main.css` (admin + embed styles)
- Test: type-check and bundle-budget checks; the Go template tests from Tasks 1 and 7 already assert the mount points

**Interfaces:**
- Consumes: every admin endpoint (Tasks 8–10) plus the embed page's `data-share-url` (Task 1)
- Produces: `mountAdmin(root)`, `mountCopy(root)`

The admin island delegates every button through one click listener on the dashboard root, so rows rendered server-side need no per-row wiring. Most actions `fetch` then reload the server-rendered page; the two that reveal a one-time API key show it and leave the page in place.

- [ ] **Step 1: Write the admin island**

Create `web/src/islands/admin.ts`:

```ts
/**
 * Admin dashboard actions. One delegated click listener drives the user, job
 * and trash buttons; the create-user and settings forms submit as JSON. Most
 * actions reload the (server-rendered) page; the two that mint an API key show
 * it once instead, because a reload would lose it.
 */

export function mountAdmin(root: HTMLElement): void {
  const newKey = root.querySelector<HTMLElement>('[data-role="new-key"]');
  const testResult = root.querySelector<HTMLElement>('[data-role="test-result"]');

  async function send(url: string, method: string, body?: unknown): Promise<Response> {
    return fetch(url, {
      method,
      headers: body === undefined ? {} : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  }

  async function failMessage(response: Response): Promise<string> {
    const detail = (await response.json().catch(() => null)) as { error?: string } | null;
    return detail?.error ?? `failed (${response.status})`;
  }

  function showKey(message: string): void {
    if (!newKey) return;
    newKey.textContent = message;
    newKey.hidden = false;
  }

  async function reloadOrAlert(response: Response): Promise<void> {
    if (response.ok) window.location.reload();
    else window.alert(await failMessage(response));
  }

  root.addEventListener("click", (event) => {
    const button = (event.target as HTMLElement | null)?.closest<HTMLElement>("[data-action]");
    if (!button) return;
    const userRow = button.closest<HTMLElement>("[data-user-id]");
    const jobRow = button.closest<HTMLElement>("[data-job-id]");
    const itemRow = button.closest<HTMLElement>("[data-item-id]");

    switch (button.dataset.action) {
      case "toggle-admin":
        if (userRow) {
          const makeAdmin = userRow.dataset.isAdmin !== "true";
          void send(`/api/admin/users/${userRow.dataset.userId}`, "PATCH", { is_admin: makeAdmin }).then(reloadOrAlert);
        }
        break;
      case "reset-password":
        if (userRow) {
          const password = window.prompt("New password for this user");
          if (password) void send(`/api/admin/users/${userRow.dataset.userId}/password`, "POST", { password }).then(reloadOrAlert);
        }
        break;
      case "rotate-key":
        if (userRow) {
          void send(`/api/admin/users/${userRow.dataset.userId}/apikey`, "POST").then(async (response) => {
            if (!response.ok) { window.alert(await failMessage(response)); return; }
            const { api_key } = (await response.json()) as { api_key: string };
            showKey(`New API key (shown once): ${api_key}`);
          });
        }
        break;
      case "delete-user":
        if (userRow && window.confirm("Delete this user? This cannot be undone.")) {
          void send(`/api/admin/users/${userRow.dataset.userId}`, "DELETE").then(reloadOrAlert);
        }
        break;
      case "retry-job":
        if (jobRow) void send(`/api/jobs/${jobRow.dataset.jobId}/retry`, "POST").then(reloadOrAlert);
        break;
      case "restore-item":
        if (itemRow) void send(`/api/admin/items/${itemRow.dataset.itemId}/restore`, "POST").then(reloadOrAlert);
        break;
      case "purge-item":
        if (itemRow && window.confirm("Permanently delete this item and its files?")) {
          void send(`/api/admin/items/${itemRow.dataset.itemId}/purge`, "DELETE").then(reloadOrAlert);
        }
        break;
      case "test-ingest":
        void testIngest(button.dataset.extractor ?? "");
        break;
    }
  });

  async function testIngest(extractor: string): Promise<void> {
    if (!extractor || !testResult) return;
    const response = await send("/api/admin/test-ingest", "POST", { extractor });
    testResult.hidden = false;
    if (!response.ok) {
      testResult.textContent = `${extractor}: ${await failMessage(response)}`;
      return;
    }
    const { job_id } = (await response.json()) as { job_id: number };
    testResult.textContent = `${extractor}: queued as job ${job_id}. Watch the job queue below for the result.`;
  }

  root.querySelector<HTMLFormElement>('[data-role="create-user"]')?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    const data = new FormData(form);
    const response = await send("/api/admin/users", "POST", {
      username: String(data.get("username") ?? ""),
      display_name: String(data.get("display_name") ?? ""),
      password: String(data.get("password") ?? ""),
      is_admin: data.get("is_admin") === "on",
    });
    if (!response.ok) { window.alert(await failMessage(response)); return; }
    const { api_key } = (await response.json()) as { api_key: string };
    form.reset();
    showKey(`Created. API key (shown once): ${api_key} — refresh to see the user in the table.`);
  });

  root.querySelector<HTMLFormElement>('[data-role="settings"]')?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    const payload: Record<string, string> = {};
    for (const [key, value] of new FormData(form).entries()) payload[key] = String(value);
    const response = await send("/api/admin/settings", "POST", payload);
    if (response.ok) window.alert("Settings saved.");
    else window.alert(await failMessage(response));
  });
}
```

- [ ] **Step 2: Write the copy island**

Create `web/src/islands/copy.ts`:

```ts
/**
 * Copy-share-link button for the /s/{id} embed page. The share URL is on the
 * mount point as data-share-url so no per-page inline script is needed.
 */

export function mountCopy(root: HTMLElement): void {
  const url = root.dataset.shareUrl ?? window.location.href;
  const button = root.querySelector<HTMLButtonElement>('[data-action="copy"]');
  button?.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(url);
      button.textContent = "Copied!";
      window.setTimeout(() => (button.textContent = "Copy share link"), 2000);
    } catch {
      /* clipboard denied; nothing to do */
    }
  });
}
```

- [ ] **Step 3: Register both islands**

In `web/src/main.ts`:

```ts
import { mountAdmin } from "./islands/admin";
import { mountCopy } from "./islands/copy";
```

```ts
registerIsland("admin", mountAdmin);
registerIsland("copy", mountCopy);
```

- [ ] **Step 4: Add the admin and embed styles**

Append to `web/src/main.css`:

```css
.banner { padding: 0.75rem 1rem; border-radius: var(--radius); margin-bottom: 1rem; }
.banner--warn { background: color-mix(in srgb, var(--danger) 18%, var(--bg-raised)); border: 1px solid var(--danger); }
.banner ul { margin: 0.5rem 0 0; padding-left: 1.25rem; }

.admin h2 { margin-top: 2rem; }
.admin__table { width: 100%; border-collapse: collapse; font-size: 0.9rem; }
.admin__table th, .admin__table td { text-align: left; padding: 0.4rem 0.5rem; border-bottom: 1px solid var(--border); vertical-align: top; }
.admin__actions { display: flex; flex-wrap: wrap; gap: 0.35rem; }
.admin__err { color: var(--danger); max-width: 28rem; overflow-wrap: anywhere; }
.admin__bad { color: var(--danger); }
.admin__newkey, .admin__testresult { background: var(--bg-raised); border: 1px solid var(--border); border-radius: var(--radius); padding: 0.5rem 0.75rem; overflow-wrap: anywhere; }
.admin__create, .admin__settings { display: flex; flex-wrap: wrap; gap: 0.5rem; align-items: end; margin-top: 0.75rem; }
.admin__setting { display: flex; flex-direction: column; gap: 0.2rem; font-size: 0.8rem; color: var(--fg-muted); min-width: 16rem; }
.admin__tests { display: flex; flex-wrap: wrap; gap: 0.5rem; }
.admin__deps { margin: 0.5rem 0 1rem; }

body.embed { display: grid; place-items: center; min-height: 100vh; padding: 1rem; }
.embed__panel { display: grid; grid-template-columns: minmax(0, 2fr) minmax(220px, 1fr); gap: 1rem; max-width: min(1000px, 94vw); background: var(--bg-raised); border: 1px solid var(--border); border-radius: var(--radius); padding: 1rem; }
@media (max-width: 640px) { .embed__panel { grid-template-columns: 1fr; } }
.embed__media img, .embed__media video { max-width: 100%; max-height: 78vh; display: block; }
.embed__meta { display: flex; flex-direction: column; gap: 0.6rem; }
.embed__title { font-size: 1.15rem; margin: 0; overflow-wrap: anywhere; }
.embed__by { display: flex; align-items: center; gap: 0.5rem; color: var(--fg-muted); margin: 0; }
.embed__avatar { display: inline-grid; place-items: center; width: 1.75rem; height: 1.75rem; border-radius: 999px; background: var(--accent); color: var(--bg); font-weight: 600; }
```

- [ ] **Step 5: Type-check, build, and confirm the budget**

Run: `bunx tsc --noEmit`
Expected: clean — `admin.ts` and `copy.ts` type-check under the strict `tsconfig.json`.

Run: `bun run build && ls -l web/static/dist`
Expected: `main.js` still **under 50 KB**. All islands remain dependency-free.

- [ ] **Step 6: Confirm the server tests still pass**

Run: `go test ./internal/web/ -run 'Admin|Embed' -v`
Expected: PASS — nothing server-side changed, so the Task 1 and Task 7 template tests (which assert the `data-island` mount points the islands attach to) are unaffected.

- [ ] **Step 7: Commit**

```bash
git add web/src/islands/admin.ts web/src/islands/copy.ts web/src/main.ts web/src/main.css
git commit -m "feat(web): admin dashboard island and embed copy-link island"
```

---

### Task 12: Nightly SQLite backup timer

**Files:**
- Create: `internal/db/backup.go`
- Create: `internal/backup/backup.go`
- Modify: `cmd/server/main.go` (launch the backup goroutine)
- Test: `internal/db/backup_test.go`, `internal/backup/backup_test.go`

**Interfaces:**
- Consumes: `db.Store` (Plan 1); `config.BackupsDir` (Plan 1)
- Produces:
  - `(*Store) BackupTo(ctx context.Context, path string) error` — `VACUUM INTO` a single file (safe on a live WAL)
  - `backup.Source` interface — `BackupTo(ctx context.Context, path string) error`
  - `backup.RunOnce(ctx context.Context, src Source, dir string, now time.Time, retain int) (string, error)` — write `media-<date>.db`, prune to the newest `retain`
  - `backup.Every(ctx context.Context, interval time.Duration, fn func())` — ticker loop that stops on context cancel

**Injected time, no sleeps in tests.** `RunOnce` takes the timestamp as a parameter, so the retention logic is exercised by calling it with nine distinct dates and asserting seven files survive — no clock, no `time.Sleep`. `Every` is the thin, untested goroutine shell `main` uses; all the logic lives in the tested `RunOnce`.

- [ ] **Step 1: Write the failing db test**

Create `internal/db/backup_test.go`:

```go
package db_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"boobies-media/internal/db"
)

func TestBackupToWritesAReadableCopy(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := store.BackupTo(ctx, dest); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if info.Size() == 0 {
		t.Error("backup file is empty")
	}
	// The copy opens as a valid database.
	copyStore, err := db.Open(dest)
	if err != nil {
		t.Fatalf("open backup copy: %v", err)
	}
	_ = copyStore.Close()
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/db/ -run BackupTo -v`
Expected: FAIL to compile — `undefined: (*Store).BackupTo`.

- [ ] **Step 3: Implement `BackupTo`**

Create `internal/db/backup.go`:

```go
package db

import (
	"context"
	"fmt"
	"strings"
)

// BackupTo writes a consistent copy of the database to path using VACUUM INTO,
// which is safe against a live WAL. VACUUM INTO does not accept a bound
// parameter, so path is single-quoted with SQL escaping; path is always
// server-controlled (the backups dir plus a date), never user input.
func (s *Store) BackupTo(ctx context.Context, path string) error {
	quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	if _, err := s.DB.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return fmt.Errorf("db: backup to %s: %w", path, err)
	}
	return nil
}
```

- [ ] **Step 4: Run the db test to verify it passes**

Run: `go test ./internal/db/ -run BackupTo -v`
Expected: PASS — `TestBackupToWritesAReadableCopy`.

- [ ] **Step 5: Write the failing backup-package test**

Create `internal/backup/backup_test.go`:

```go
package backup_test

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"boobies-media/internal/backup"
	"boobies-media/internal/db"
)

func openStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestRunOnceWritesADatedBackup(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)

	dest, err := backup.RunOnce(context.Background(), store, dir, now, 7)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if filepath.Base(dest) != "media-2026-07-24.db" {
		t.Errorf("backup name = %q, want media-2026-07-24.db", filepath.Base(dest))
	}
}

func TestRunOnceKeepsOnlyTheNewestN(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := openStore(t)
	base := time.Date(2026, 7, 1, 3, 0, 0, 0, time.UTC)

	for i := 0; i < 9; i++ {
		if _, err := backup.RunOnce(ctx, store, dir, base.AddDate(0, 0, i), 7); err != nil {
			t.Fatalf("RunOnce day %d: %v", i, err)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "media-*.db"))
	if len(matches) != 7 {
		t.Fatalf("kept %d backups, want 7", len(matches))
	}
	sort.Strings(matches)
	if filepath.Base(matches[0]) != "media-2026-07-03.db" {
		t.Errorf("oldest kept = %q, want media-2026-07-03.db (07-01 and 07-02 pruned)", filepath.Base(matches[0]))
	}
}

func TestRunOnceSameDayDoesNotError(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := openStore(t)
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)

	if _, err := backup.RunOnce(ctx, store, dir, now, 7); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	if _, err := backup.RunOnce(ctx, store, dir, now, 7); err != nil {
		t.Fatalf("same-day RunOnce should overwrite, got: %v", err)
	}
}
```

- [ ] **Step 6: Run the test to verify it fails**

Run: `go test ./internal/backup/ -v`
Expected: FAIL to compile — `undefined: backup.RunOnce`.

- [ ] **Step 7: Implement the backup package**

Create `internal/backup/backup.go`:

```go
package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Source is the slice of the store this package needs.
type Source interface {
	BackupTo(ctx context.Context, path string) error
}

// RunOnce writes media-<date>.db into dir and prunes to the newest retain
// files. A same-day rerun overwrites, since VACUUM INTO refuses an existing
// target.
func RunOnce(ctx context.Context, src Source, dir string, now time.Time, retain int) (string, error) {
	name := "media-" + now.UTC().Format("2006-01-02") + ".db"
	dest := filepath.Join(dir, name)
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("backup: clear existing %s: %w", dest, err)
	}
	if err := src.BackupTo(ctx, dest); err != nil {
		return "", err
	}
	if err := prune(dir, retain); err != nil {
		return dest, err
	}
	return dest, nil
}

// prune keeps the newest retain backups. Filenames sort chronologically
// because the date is ISO-8601, so a lexical sort is a chronological sort.
func prune(dir string, retain int) error {
	if retain <= 0 {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(dir, "media-*.db"))
	if err != nil {
		return fmt.Errorf("backup: list backups: %w", err)
	}
	if len(matches) <= retain {
		return nil
	}
	sort.Strings(matches)
	for _, old := range matches[:len(matches)-retain] {
		if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("backup: prune %s: %w", old, err)
		}
	}
	return nil
}

// Every calls fn on each tick until ctx is cancelled. It is the goroutine shell
// main uses; the tested logic is in RunOnce.
func Every(ctx context.Context, interval time.Duration, fn func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn()
		}
	}
}
```

- [ ] **Step 8: Run the test to verify it passes**

Run: `go test ./internal/backup/ -v`
Expected: PASS — `TestRunOnceWritesADatedBackup`, `TestRunOnceKeepsOnlyTheNewestN`, `TestRunOnceSameDayDoesNotError`.

- [ ] **Step 9: Wire the backup goroutine into main**

In `cmd/server/main.go`, add the import `"boobies-media/internal/backup"`. In `runServer`, immediately after `queue.Start(ctx)` (Plan 2 wiring), add:

```go
	// Nightly backup on the same signal context as the queue; the retention
	// count matches the spec's "keep 7".
	go backup.Every(ctx, 24*time.Hour, func() {
		dest, err := backup.RunOnce(ctx, store, cfg.BackupsDir(), time.Now().UTC(), 7)
		if err != nil {
			slog.Error("nightly backup failed", "err", err)
			return
		}
		slog.Info("nightly backup written", "path", dest)
	})
```

`*db.Store` satisfies `backup.Source` through the `BackupTo` method from Step 3, so it passes directly. `time` and `slog` are already imported by `main.go` (Plan 2 uses both).

- [ ] **Step 10: Run the whole suite**

Run: `go build ./... && go test ./... -race -count=1`
Expected: builds clean; PASS everywhere, no `DATA RACE`.

- [ ] **Step 11: Commit**

```bash
git add internal/db/backup.go internal/db/backup_test.go internal/backup/backup.go internal/backup/backup_test.go cmd/server/main.go
git commit -m "feat: nightly VACUUM INTO backup with 7-file retention"
```

---

### Task 13: Deployment notes

**Files:**
- Create: `docs/deploy.md`

**Interfaces:**
- Consumes: nothing (documentation).
- Produces: `docs/deploy.md`.

The spec calls deployment notes "part of the design, not an afterthought". This captures the Cloudflare Tunnel setup, the chunk-size constraint that replaced the old body-size trap, the mandatory WAF and cache rules, the systemd unit, and the recurring cookie re-export maintenance.

- [ ] **Step 1: Write the deployment notes**

Create `docs/deploy.md`:

````markdown
# Deploying boobies-media

The server is a single Go binary plus external tools (`yt-dlp`, `gallery-dl`,
`ffmpeg`, `ffprobe`, `cwebp`). It serves HTTP on `-addr` and keeps all state
under `-data`. Put a reverse proxy in front for TLS.

## Build

```bash
bun install
bun run build          # bundles web/src into web/static/dist (embedded by go:embed)
go build -o bin/server ./cmd/server
```

## First run

```bash
./bin/server user add aiden --display-name "Aiden"   # prompts for a password
./bin/server -addr 127.0.0.1:8080 -data /srv/media -base-url https://media.example.com
```

`-base-url` must be the public HTTPS origin: it is what the `/s/{id}` embed page
puts into every absolute OpenGraph URL, so Discord fetches media over the right
host. Getting it wrong means broken embeds.

## The body-size trap — and why chunking defuses it

Cloudflare proxies request bodies only up to **100 MB** on Free and Pro (200 MB
Business, 500 MB Enterprise), and answers `413` above that. A Cloudflare Tunnel
is proxied by definition, so there is no unproxied route to fall back on.

Uploads are therefore **chunked**: `upload_chunk_bytes` (default 12 MiB) is the
only value that has to respect that cap, and `upload_max_bytes` (default 8 GiB)
is a policy limit the admin picks. The number to keep an eye on is the chunk
size, not the file size.

Two further constraints on the chunk size:

- Cloudflare's proxy read timeout is **125 s** and is tunable on Enterprise
  only. A chunk must upload within it on the server's worst upstream. At
  1 Mbit/s upstream, 12 MiB takes ~100 s — if the connection is slower than
  that, lower `upload_chunk_bytes`, do not raise the timeout.
- If a plain reverse proxy is ever put in front instead, **nginx** defaults
  `client_max_body_size` to **1 MB** and will reject every chunk until raised.
  Set it above `upload_chunk_bytes`. **Caddy** has no low default:

  ```
  media.example.com {
      reverse_proxy 127.0.0.1:8080
  }
  ```

Raising `upload_max_bytes` needs no proxy change at all — that is the whole
point of chunking. Only `upload_chunk_bytes` interacts with proxy limits.

## Cloudflare Tunnel

`cloudflared` runs on the same host and dials out, so no port is forwarded and
the home IP is never published. The server binds `127.0.0.1:8080`; `cloudflared`
is the only process that can reach it.

```yaml
# /etc/cloudflared/config.yml
tunnel: <tunnel-id>
credentials-file: /etc/cloudflared/<tunnel-id>.json
ingress:
  - hostname: media.example.com
    service: http://127.0.0.1:8080
  - service: http_status:404
```

Two settings follow from terminating TLS at the edge:

- Run the server **without** `-insecure-cookies`. The origin sees plain HTTP, so
  the `Secure` flag comes from config; `r.TLS` is always nil here and is never
  consulted.
- `-base-url` is the public `https://` origin, not the loopback address.

## Cloudflare dashboard rules — required, not optional

Everything is proxied through Cloudflare on a tunnel; there is no DNS-only path
to fall back on, so these are configuration, not preferences.

- **WAF / Bot Fight Mode must skip crawler user-agents on `/s/*`, `/m/*` and
  `/t/*`.** A challenged Discordbot caches a *failed* embed and the bad embed
  sticks around.
- **Cache Rule: bypass cache on `/m/*` and `/t/*`.** This keeps the media
  library out of Cloudflare's CDN — which is the behaviour self-serve Terms of
  Service §2.8 actually restricts — and stops stale bytes being served after a
  purge.
- `/s/`, `/m/`, `/t/` are the only anonymous routes; everything else already
  requires a session or Bearer key, so exposing them is expected and safe.

## Client IP

Because the origin is loopback-bound and `cloudflared` is its only peer,
`CF-Connecting-IP` is authoritative and is what the login rate limiter keys on.
`X-Forwarded-For` is deliberately ignored — any client can send it. **If you
ever move off the tunnel to a plain reverse proxy, `clientIP()` in
`internal/web/middleware.go` must change with it**; nothing else depends on the
forwarding story.

## systemd unit

`/etc/systemd/system/boobies-media.service`:

```ini
[Unit]
Description=boobies-media
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=media
Group=media
# Tools must be on PATH; a distro upgrade that renames or drops one shows up
# as a soft-fail banner on the admin page rather than a crash.
Environment=PATH=/usr/local/bin:/usr/bin:/bin
ExecStart=/srv/media/bin/server -addr 127.0.0.1:8080 -data /srv/media -base-url https://media.example.com
Restart=on-failure
RestartSec=2
# Hardening: the process only needs its data dir.
ProtectSystem=strict
ReadWritePaths=/srv/media
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now boobies-media
journalctl -u boobies-media -f
```

The nightly `VACUUM INTO` backup runs inside the process (goroutine, 24 h
interval, keeps the newest 7 under `data/backups/`) — no cron needed. Copy that
directory offsite if you care about the data.

## Cookie maintenance (recurring)

Unauthenticated Twitter/X access no longer exists, and YouTube/TikTok tighten
periodically. Export browser cookies to Netscape-format files and point the
admin **Settings** at them (`cookies_twitter`, `cookies_youtube`,
`cookies_tiktok`, `cookies_medal`), or drop them at
`data/cookies/<extractor>.txt`. Cookies **expire**: when a source starts failing,
re-export. The admin page's **Ingest self-test** buttons enqueue a known-good
link per source so you can see breakage instead of guessing.

## Upgrading tools

`yt-dlp` staleness is the number-one cause of ingest breakage. The admin page
shows `yt-dlp --version`; update it (`yt-dlp -U` or your package manager) when
YouTube/TikTok ingests start failing, then use the self-test buttons to confirm.
````

- [ ] **Step 2: Verify the document renders**

Run: `sed -n '1,40p' docs/deploy.md`
Expected: the header and build section render as valid Markdown (the fenced blocks are balanced).

- [ ] **Step 3: Commit**

```bash
git add docs/deploy.md
git commit -m "docs: deployment notes (proxy body size, Cloudflare, systemd, cookies)"
```

---

## Definition of Done

- [ ] `make test` passes; `go test ./... -race -count=1` reports no data races.
- [ ] `bunx tsc --noEmit` is clean and `web/static/dist/main.js` is **under 50 KB**.
- [ ] `/s/{id}` serves anonymously and emits **every** OpenGraph/Twitter tag the spec lists — image case, mp4 video case (video tags + poster), and webm fallback (image card + poster) — each asserted by a golden test.
- [ ] `/s/{id}` 404s for a `share_revoked` or soft-deleted item, and uses `config.BaseURL` for absolute URLs with `Referrer-Policy: no-referrer`.
- [ ] The browse page shows a folder tree sidebar (create/rename/move/delete over the cycle-safe store), tag filter chips, and a bulk-select toolbar; the lightbox moves an item between folders and toggles share-revoke.
- [ ] `GET /api/items?folder=` and `?tag=` filter (Plan 2), and `GET /api/random?tag=` returns only live, non-revoked items for the bot.
- [ ] `POST /api/items/batch` applies delete/move/tag over many items, reusing per-item authorization, and reports per-item failures.
- [ ] `/admin` and every `/api/admin/*` route 403 a non-admin **server-side**, independent of the nav link.
- [ ] Admin can create users (API key shown once), reset passwords, rotate keys, toggle admin and delete item-free users — and cannot lock themselves out.
- [ ] Admin can edit settings (`auto_webp`, `upload_max_bytes`, `upload_chunk_bytes`, `download_max_bytes`, `ytdlp_format`, four cookie paths, `min_free_disk_bytes`) with key-allowlist and numeric validation.
- [ ] The admin job queue lists jobs with error strings; `POST /api/jobs/{id}/retry` requeues a failed job for a fresh three attempts; non-failed and missing jobs are rejected.
- [ ] The dependency banner shows any soft-failed tool and its `--version`; the per-source test-ingest buttons enqueue an `ingest_url` job.
- [ ] The trash lists soft-deleted items; restore undeletes and purge hard-deletes (blob unlinked only when no live item shares the hash).
- [ ] A nightly `VACUUM INTO` runs in-process on the signal context, writing `data/backups/media-<date>.db` and keeping the newest 7; retention is unit-tested with an injected timestamp and no sleeps.
- [ ] `docs/deploy.md` documents the Cloudflare Tunnel config, the chunk-size versus 100 MB body-cap constraint, the mandatory WAF crawler-skip and `/m/` `/t/` cache-bypass rules, the `CF-Connecting-IP` trust story, the systemd unit, and cookie re-export maintenance.
- [ ] No test touches the network; external tools are stubbed with `media.StubTools`.
