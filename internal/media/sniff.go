package media

import (
	"bytes"
	"errors"
)

// ErrUnsupportedType is returned when a file's sniffed type is not on the
// served-mime allowlist.
var ErrUnsupportedType = errors.New("media: unsupported file type")

// SniffLen is how many leading bytes Sniff needs. 512 comfortably covers the
// WebM DocType, which sits around offset 24.
const SniffLen = 512

// AllowedMimes is the complete set of types this server will ever store or
// serve. Everything else is rejected at ingest. SVG and HTML are absent on
// purpose: served same-origin from /m/ they are stored XSS against the session
// cookie.
var AllowedMimes = []string{
	"image/jpeg",
	"image/png",
	"image/gif",
	"image/webp",
	"image/avif",
	"video/mp4",
	"video/webm",
}

var extByMime = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/gif":  "gif",
	"image/webp": "webp",
	"image/avif": "avif",
	"video/mp4":  "mp4",
	"video/webm": "webm",
}

// mp4Brands are the ISO-BMFF major brands treated as video/mp4.
var mp4Brands = []string{"isom", "iso2", "iso4", "iso5", "iso6", "mp41", "mp42", "avc1", "M4V ", "dash", "mmp4"}

// avifBrands are the ISO-BMFF major brands treated as image/avif.
var avifBrands = []string{"avif", "avis"}

// IsAllowedMime reports whether a mime type may be stored and served.
func IsAllowedMime(mime string) bool {
	_, ok := extByMime[mime]
	return ok
}

// ExtForMime returns the canonical extension (no dot) for an allowed mime, or
// "" for anything else.
func ExtForMime(mime string) string { return extByMime[mime] }

// IsVideoMime reports whether the type needs a poster frame rather than a
// direct resize.
func IsVideoMime(mime string) bool {
	return mime == "video/mp4" || mime == "video/webm"
}

// IsGifMime reports whether the type is an animated-capable GIF.
//
// GIF sits outside the IsVideoMime split on purpose: GenerateThumbnail still
// treats it as a still (frame 0 of a GIF is always a complete frame, so it
// needs no seek the way frame 0 of a video clip often does), but unlike a
// plain JPEG or PNG it has its own motion. The web layer uses this to offer
// GIFs the same hover-to-play preview that video items get, sourced from the
// original bytes at /m/{id} rather than a new derived artifact.
func IsGifMime(mime string) bool {
	return mime == "image/gif"
}

// Sniff identifies a file from its leading bytes, returning "" for anything
// not on the allowlist. The filename is never consulted: an attacker controls
// it, and they do not control magic bytes.
func Sniff(header []byte) string {
	switch {
	case bytes.HasPrefix(header, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"

	case bytes.HasPrefix(header, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		return "image/png"

	case bytes.HasPrefix(header, []byte("GIF87a")), bytes.HasPrefix(header, []byte("GIF89a")):
		return "image/gif"

	case len(header) >= 12 && bytes.HasPrefix(header, []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WEBP")):
		// RIFF alone is not enough: WAV and AVI share it.
		return "image/webp"

	case len(header) >= 12 && bytes.Equal(header[4:8], []byte("ftyp")):
		return sniffISOBMFF(header)

	case bytes.HasPrefix(header, []byte{0x1a, 0x45, 0xdf, 0xa3}):
		// EBML covers both WebM and Matroska; only the DocType separates them,
		// and Matroska is not on the allowlist.
		limit := len(header)
		if limit > 64 {
			limit = 64
		}
		if bytes.Contains(header[:limit], []byte("webm")) {
			return "video/webm"
		}
		return ""
	}
	return ""
}

func sniffISOBMFF(header []byte) string {
	if len(header) < 12 {
		return ""
	}
	brand := string(header[8:12])
	for _, b := range avifBrands {
		if brand == b {
			return "image/avif"
		}
	}
	for _, b := range mp4Brands {
		if brand == b {
			return "video/mp4"
		}
	}
	return ""
}
