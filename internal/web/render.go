// Package web is the HTTP layer: routing, middleware, and handlers.
package web

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"

	webassets "boobies-media/web"

	"boobies-media/internal/db"
	"boobies-media/internal/ingest"
)

// PageData is the value every page template receives. The base layout reads
// User; individual pages read the rest.
type PageData struct {
	Title   string
	SiteURL string
	Storage *StorageUsage
	User    *db.User
	Error   string
	Next    string
	Data    any
}

// StorageUsage is the topbar's view of media bytes against the space the app
// can grow into on the data filesystem.
type StorageUsage struct {
	UsedBytes     int64
	CapacityBytes int64
	Percent       int
}

func (s *Server) storageUsage(ctx context.Context) *StorageUsage {
	used, err := s.Store.MediaStorageBytes(ctx)
	if err != nil {
		return nil
	}
	free, err := ingest.FreeSpace(s.Cfg.DataDir)
	if err != nil {
		return nil
	}
	capacity := used + int64(free)
	percent := 0
	if capacity > 0 {
		percent = int((used * 100) / capacity)
		if used > 0 && percent == 0 {
			percent = 1
		}
	}
	return &StorageUsage{UsedBytes: used, CapacityBytes: capacity, Percent: percent}
}

// Renderer holds one parsed template set per page.
type Renderer struct {
	pages map[string]*template.Template
	embed *template.Template
}

// NewRenderer parses every templates/pages/*.html against the base layout.
// Pages get their own template set because they all define "title" and
// "content"; parsing them into one set would make the last one win.
func NewRenderer() (*Renderer, error) {
	entries, err := fs.Glob(webassets.Templates, "templates/pages/*.html")
	if err != nil {
		return nil, fmt.Errorf("web: glob page templates: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("web: no page templates embedded")
	}

	pages := make(map[string]*template.Template, len(entries))
	for _, entry := range entries {
		name := strings.TrimSuffix(path.Base(entry), ".html")
		tpl, err := template.New("base.html").Funcs(templateFuncs).ParseFS(webassets.Templates, "templates/base.html", entry)
		if err != nil {
			return nil, fmt.Errorf("web: parse page %q: %w", name, err)
		}
		pages[name] = tpl
	}

	// embed.html lives at the templates root, not under pages/: it does not
	// extend base.html, so it is parsed on its own rather than picked up by
	// the glob above.
	embedTpl, err := template.New("embed.html").Funcs(templateFuncs).ParseFS(webassets.Templates, "templates/embed.html")
	if err != nil {
		return nil, fmt.Errorf("web: parse embed template: %w", err)
	}
	return &Renderer{pages: pages, embed: embedTpl}, nil
}

// RenderString renders a page to a string. Handlers use Render; tests use this.
func (r *Renderer) RenderString(page string, data PageData) (string, error) {
	tpl, ok := r.pages[page]
	if !ok {
		return "", fmt.Errorf("web: unknown page %q", page)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
		return "", fmt.Errorf("web: render page %q: %w", page, err)
	}
	return buf.String(), nil
}

// Render writes a fully rendered page. The document is buffered first so a
// template failure cannot leave a half-written 200 on the wire.
func (r *Renderer) Render(w http.ResponseWriter, status int, page string, data PageData) error {
	body, err := r.RenderString(page, data)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write([]byte(body)); err != nil {
		return fmt.Errorf("web: write page %q: %w", page, err)
	}
	return nil
}

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
