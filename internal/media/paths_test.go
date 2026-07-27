package media

import (
	"path/filepath"
	"strings"
	"testing"
)

const testHash = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

func TestBlobPathIsSharded(t *testing.T) {
	got := BlobPath("/data/files", testHash)
	want := filepath.Join("/data/files", "ab", "cd", testHash)
	if got != want {
		t.Errorf("BlobPath = %q, want %q", got, want)
	}
}

func TestSidecarPathSitsBesideTheBlob(t *testing.T) {
	got := SidecarPath("/data/files", testHash)
	want := BlobPath("/data/files", testHash) + ".json"
	if got != want {
		t.Errorf("SidecarPath = %q, want %q", got, want)
	}
}

func TestThumbPathIncludesTheSize(t *testing.T) {
	for _, size := range ThumbSizes {
		got := ThumbPath("/data/thumbs", testHash, size)
		if !strings.HasSuffix(got, ".webp") {
			t.Errorf("ThumbPath(%d) = %q, want a .webp suffix", size, got)
		}
		if !strings.Contains(got, filepath.Join("ab", "cd")) {
			t.Errorf("ThumbPath(%d) = %q, want the same ab/cd sharding as blobs", size, got)
		}
	}
	if ThumbPath("/t", testHash, 320) == ThumbPath("/t", testHash, 1024) {
		t.Error("the two thumbnail sizes collide on one path")
	}
}

func TestThumbSizesAreExactlyThoseInTheSpec(t *testing.T) {
	if len(ThumbSizes) != 2 || ThumbSizes[0] != 320 || ThumbSizes[1] != 1024 {
		t.Fatalf("ThumbSizes = %v, want [320 1024]", ThumbSizes)
	}
}

func TestIsValidThumbSize(t *testing.T) {
	for _, ok := range []int{320, 1024} {
		if !IsValidThumbSize(ok) {
			t.Errorf("IsValidThumbSize(%d) = false, want true", ok)
		}
	}
	// An unbounded size parameter would be an arbitrary-resize denial of service.
	for _, bad := range []int{0, -1, 321, 4096, 999999} {
		if IsValidThumbSize(bad) {
			t.Errorf("IsValidThumbSize(%d) = true, want false", bad)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"cat.png":                    "cat.png",
		"../../etc/passwd":           "passwd",
		`C:\evil\payload.mp4`:        "payload.mp4",
		"with spaces.gif":            "with spaces.gif",
		"quote\"and\\backslash.webp": "quoteandbackslash.webp",
		"new\nline.png":              "newline.png",
		"":                           "download",
		"   ":                        "download",
		"...":                        "download",
		// Regression: a plain multi-segment POSIX path with no ".." must
		// still be reduced to its last element: a Content-Disposition
		// filename with an embedded "/" is a traversal vector for clients
		// that naively join it onto a save directory.
		"uploads/private/photo.png": "photo.png",
		// A rooted/UNC-style Windows path with no drive letter is still
		// Windows-path-shaped (leading backslash) and must be split.
		`\Users\evil.exe`: "evil.exe",
		// A trailing separator leaves nothing after the last "/", so the
		// safe fallback applies rather than leaking the segment before it.
		"trailing/slash/": "download",
		// A name that is only separators reduces to nothing.
		"////": "download",
	}
	for in, want := range cases {
		if got := SanitizeFilename(in); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", 300) + ".png"
	if got := SanitizeFilename(long); len(got) > 120 {
		t.Errorf("SanitizeFilename kept %d characters, want it capped at 120", len(got))
	}
}
