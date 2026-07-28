package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boobies-media/internal/db"
)

func TestNewRendererLoadsEveryPage(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	for _, page := range []string{"login", "browse"} {
		if _, err := r.RenderString(page, PageData{User: &db.User{Username: "aiden", DisplayName: "Aiden"}}); err != nil {
			t.Errorf("RenderString(%q): %v", page, err)
		}
	}
}

func TestRenderUnknownPageIsAnError(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	if _, err := r.RenderString("does-not-exist", PageData{}); err == nil {
		t.Fatal("RenderString accepted an unknown page, want an error")
	}
}

func TestLoginPageShowsErrorAndCarriesNext(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	out, err := r.RenderString("login", PageData{Error: "Incorrect username or password.", Next: "/some/path"})
	if err != nil {
		t.Fatalf("RenderString: %v", err)
	}
	if !strings.Contains(out, "Incorrect username or password.") {
		t.Error("rendered login page does not show the error message")
	}
	if !strings.Contains(out, `value="/some/path"`) {
		t.Error("rendered login page does not carry the next parameter through the form")
	}
	// Anonymous pages must not render the signed-in header.
	if strings.Contains(out, "Sign out") {
		t.Error("login page rendered the signed-in header for an anonymous visitor")
	}
	if !strings.Contains(out, `/static/brand/booby-logo.webp`) {
		t.Error("login page does not render the booby logo")
	}
	if !strings.Contains(out, `rel="icon" href="/favicon.ico"`) {
		t.Error("login page does not link the favicon")
	}
}

func TestBrowsePageShowsDisplayName(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	out, err := r.RenderString("browse", PageData{User: &db.User{Username: "aiden", DisplayName: "Aiden S"}})
	if err != nil {
		t.Fatalf("RenderString: %v", err)
	}
	if !strings.Contains(out, "Aiden S") {
		t.Error("browse page does not show the signed-in display name")
	}
	if !strings.Contains(out, "Sign out") {
		t.Error("browse page does not render the signed-in header")
	}
	if !strings.Contains(out, `/static/brand/booby-logo.webp`) {
		t.Error("browse page does not render the booby logo in the topbar")
	}
}

func TestRenderEscapesUserContent(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	out, err := r.RenderString("browse", PageData{
		User: &db.User{Username: "x", DisplayName: `<script>alert(1)</script>`},
	})
	if err != nil {
		t.Fatalf("RenderString: %v", err)
	}
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Fatal("a display name was emitted unescaped; html/template escaping is not in effect")
	}
}

func TestRenderWritesStatusAndContentType(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	rec := httptest.NewRecorder()
	if err := r.Render(rec, http.StatusUnauthorized, "login", PageData{}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want \"text/html; charset=utf-8\"", ct)
	}
	if !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Error("response body is not a full HTML document")
	}
}
