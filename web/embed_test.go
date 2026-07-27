package webassets

import (
	"io/fs"
	"strings"
	"testing"
)

func TestTemplatesAreEmbedded(t *testing.T) {
	for _, name := range []string{"templates/base.html", "templates/pages/login.html", "templates/pages/browse.html"} {
		b, err := Templates.ReadFile(name)
		if err != nil {
			t.Errorf("ReadFile(%q): %v", name, err)
			continue
		}
		if len(b) == 0 {
			t.Errorf("%s is empty", name)
		}
	}
}

func TestRobotsIsEmbeddedAndAllowsEmbedRoutes(t *testing.T) {
	b, err := Static.ReadFile("static/robots.txt")
	if err != nil {
		t.Fatalf("ReadFile robots.txt: %v", err)
	}
	body := string(b)
	// Discord's crawler honours robots.txt. If the share routes are
	// disallowed, embeds silently stop rendering.
	for _, allow := range []string{"Allow: /s/", "Allow: /m/", "Allow: /t/"} {
		if !strings.Contains(body, allow) {
			t.Errorf("robots.txt is missing %q; crawlers would refuse the embed routes", allow)
		}
	}
	if !strings.Contains(body, "Disallow: /") {
		t.Error("robots.txt does not disallow the rest of the site")
	}
}

func TestStaticFSStripsThePrefix(t *testing.T) {
	sub, err := StaticFS()
	if err != nil {
		t.Fatalf("StaticFS: %v", err)
	}
	if _, err := fs.Stat(sub, "robots.txt"); err != nil {
		t.Fatalf("robots.txt not reachable at the root of StaticFS: %v", err)
	}
}

func TestPageTemplatesDefineTitleAndContent(t *testing.T) {
	pages, err := fs.Glob(Templates, "templates/pages/*.html")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(pages) == 0 {
		t.Fatal("no page templates found")
	}
	for _, page := range pages {
		b, err := Templates.ReadFile(page)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", page, err)
		}
		body := string(b)
		for _, block := range []string{`{{define "title"}}`, `{{define "content"}}`} {
			if !strings.Contains(body, block) {
				t.Errorf("%s does not contain %s; the base layout requires it", page, block)
			}
		}
	}
}
