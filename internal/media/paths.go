// Package media owns the byte-level ingestion pipeline: sniffing, the
// served-mime allowlist, optional lossless webp optimization, content hashing,
// blob placement, probing and thumbnailing.
package media

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ThumbSizes are the only thumbnail dimensions the server generates or serves.
var ThumbSizes = []int{320, 1024}

// IsValidThumbSize gates the ?s= parameter on /t/{id}. Without a strict
// allowlist an attacker could request arbitrary resizes.
func IsValidThumbSize(size int) bool {
	for _, s := range ThumbSizes {
		if s == size {
			return true
		}
	}
	return false
}

// shardDir splits a hex hash into two levels so no directory holds every file.
func shardDir(root, hash string) string {
	if len(hash) < 4 {
		// Never reached with SHA-256, but a short hash must not escape root.
		return filepath.Join(root, "xx", "xx")
	}
	return filepath.Join(root, hash[0:2], hash[2:4])
}

// BlobPath is where the original bytes for a content hash live.
func BlobPath(filesDir, hash string) string {
	return filepath.Join(shardDir(filesDir, hash), hash)
}

// SidecarPath is the JSON descriptor beside a blob. It exists so the library
// is still identifiable if media.db is ever lost.
func SidecarPath(filesDir, hash string) string {
	return BlobPath(filesDir, hash) + ".json"
}

// ThumbPath is where a generated thumbnail of the given size lives.
func ThumbPath(thumbsDir, hash string, size int) string {
	return filepath.Join(shardDir(thumbsDir, hash), fmt.Sprintf("%s_%d.webp", hash, size))
}

// SocialPreviewPath is a crawler-compatible JPEG poster. Unlike the WebP
// thumbnails used by the app, JPEG is accepted consistently by social-card
// scrapers. The poster is generated lazily so existing libraries gain it
// without a migration.
func SocialPreviewPath(thumbsDir, hash string) string {
	return filepath.Join(shardDir(thumbsDir, hash), hash+"_social.jpg")
}

// SocialAnimationPath is the H.264 MP4 rendition used to embed an animated
// GIF on social platforms that support Open Graph video.
func SocialAnimationPath(thumbsDir, hash string) string {
	return filepath.Join(shardDir(thumbsDir, hash), hash+"_social.mp4")
}

// SocialVideoPath is a browser/social-compatible H.264/AAC rendition. The
// original remains untouched; this derivative exists because an MP4
// container can still carry AV1, HEVC, or another codec that embed players
// do not support.
func SocialVideoPath(thumbsDir, hash string) string {
	return filepath.Join(shardDir(thumbsDir, hash), hash+"_embed.mp4")
}

// maxFilenameLength caps the name echoed in Content-Disposition.
const maxFilenameLength = 120

// SanitizeFilename reduces an arbitrary client-supplied name to something safe
// to echo in a Content-Disposition header and safe to log. It never returns an
// empty string.
func SanitizeFilename(name string) string {
	// A forward slash is never a legitimate character in a bare filename, so
	// unconditionally take the last path element under it. A backslash *is* a
	// valid filename character on POSIX systems, so it is only treated as a
	// separator when the (remaining) string looks Windows-path-shaped: a
	// drive prefix ("C:\...") or a rooted/UNC path (leading "\"). This way
	// neither "../../etc/passwd" nor "C:\evil\payload.mp4" nor
	// "uploads/private/photo.png" survives, while a filename that merely
	// contains a backslash (e.g. "quote\"and\backslash.webp") is not
	// mistaken for a path.
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if strings.Contains(name, `:\`) || strings.HasPrefix(name, `\`) {
		if i := strings.LastIndex(name, `\`); i >= 0 {
			name = name[i+1:]
		}
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			// Control characters would let a filename inject header lines.
			continue
		case r == '"' || r == '\\':
			continue
		default:
			b.WriteRune(r)
		}
	}
	cleaned := strings.TrimSpace(b.String())
	cleaned = strings.Trim(cleaned, ".")
	if cleaned == "" {
		return "download"
	}
	if len(cleaned) > maxFilenameLength {
		cleaned = cleaned[:maxFilenameLength]
	}
	return cleaned
}
