// Package webassets embeds the HTML templates and the Bun-built frontend
// bundle into the server binary.
//
// The package is named webassets rather than web so that internal/web (the
// HTTP server) can import it without an alias.
//
// web/static/dist is produced by `make assets` and is gitignored. The
// //go:embed static directive still compiles on a fresh clone because
// static/robots.txt is committed; the dist files simply 404 until Bun runs.
package webassets

import (
	"embed"
	"io/fs"
)

//go:embed templates
var Templates embed.FS

//go:embed static
var Static embed.FS

// StaticFS returns the embedded static tree with the "static/" prefix removed,
// so it can be handed straight to http.FileServer.
func StaticFS() (fs.FS, error) {
	return fs.Sub(Static, "static")
}
