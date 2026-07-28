package web

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"boobies-media/internal/db"
	"boobies-media/internal/media"
)

// immutableCacheControl is safe because the URL is bound to an item whose
// bytes never change: renames and moves are metadata-only.
const immutableCacheControl = "public, max-age=31536000, immutable"

// handleRawMedia serves the original bytes at GET /m/{id}.
func (s *Server) handleRawMedia(w http.ResponseWriter, r *http.Request) {
	if !s.checkPublicRateLimit(w, r) {
		return
	}
	item, ok := s.publicItem(w, r)
	if !ok {
		return
	}
	path := media.BlobPath(s.Cfg.FilesDir(), item.ContentHash)
	filename := media.SanitizeFilename(item.Title + "." + item.Ext)
	s.serveFile(w, r, path, item.Mime, filename)
}

// handleEmbedImage serves an image at an extension-bearing URL. Discord's
// media proxy uses both Content-Type and the URL suffix when deciding whether
// an external image is animated, so /i/{id}.gif|webp is used by share-card
// metadata while /m/{id} remains the stable download URL.
func (s *Server) handleEmbedImage(w http.ResponseWriter, r *http.Request) {
	if !s.checkPublicRateLimit(w, r) {
		return
	}
	item, ok := s.publicItem(w, r)
	if !ok {
		return
	}
	canonicalExt := media.ExtForMime(item.Mime)
	if !strings.HasPrefix(item.Mime, "image/") || chi.URLParam(r, "ext") != canonicalExt {
		http.NotFound(w, r)
		return
	}
	path := media.BlobPath(s.Cfg.FilesDir(), item.ContentHash)
	filename := media.SanitizeFilename(item.Title + "." + canonicalExt)
	s.serveFile(w, r, path, item.Mime, filename)
}

// handleThumbnail serves a generated thumbnail at GET /t/{id}?s=320|1024.
func (s *Server) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	// The rate limit is spent before anything else (including parsing and
	// validating ?s=), so a flood of requests with a malformed size cannot
	// dodge the budget by getting rejected before publicItem runs.
	if !s.checkPublicRateLimit(w, r) {
		return
	}
	size := media.ThumbSizes[0]
	// Parsed by hand rather than via r.URL.Query(): that helper silently
	// swallows a malformed query (e.g. a stray ";") and returns "", which
	// would make a garbled size indistinguishable from an absent one and
	// let it fall through to the default size instead of being rejected.
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "unsupported thumbnail size", http.StatusBadRequest)
		return
	}
	if raw := query.Get("s"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		// A strict allowlist, not a clamp: anything else is an arbitrary
		// resize request and is refused outright.
		if err != nil || !media.IsValidThumbSize(parsed) {
			http.Error(w, "unsupported thumbnail size", http.StatusBadRequest)
			return
		}
		size = parsed
	}
	item, ok := s.publicItem(w, r)
	if !ok {
		return
	}
	path := media.ThumbPath(s.Cfg.ThumbsDir(), item.ContentHash, size)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		animatedImage := media.IsAnimatedImage(item.Mime, item.Duration)
		if (item.Width <= 0 || item.Height <= 0) && !animatedImage {
			http.NotFound(w, r)
			return
		}
		if s.Media == nil {
			http.Error(w, "media storage is not configured", http.StatusServiceUnavailable)
			return
		}
		src := media.BlobPath(s.Cfg.FilesDir(), item.ContentHash)
		if err := s.Media.GenerateThumbnail(r.Context(), src, path, size, media.IsVideoMime(item.Mime), item.Duration); err != nil {
			if animatedImage {
				// Existing animated files may predate the native WebP/GIF
				// probe and thumbnail fallbacks. The original is already a
				// safe, browser-served image, so use it as the poster instead
				// of returning a broken thumbnail forever.
				filename := media.SanitizeFilename(item.Title + "." + item.Ext)
				s.serveFile(w, r, src, item.Mime, filename)
				return
			}
			s.serverError(w, r, err)
			return
		}
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	filename := media.SanitizeFilename(item.Title + ".webp")
	s.serveFile(w, r, path, "image/webp", filename)
}

// handleSocialPreview serves a broadly compatible JPEG card image. It is
// generated on first request so media uploaded before this feature works
// without re-running every historical thumbnail job.
func (s *Server) handleSocialPreview(w http.ResponseWriter, r *http.Request) {
	if !s.checkPublicRateLimit(w, r) {
		return
	}
	item, ok := s.publicItem(w, r)
	if !ok {
		return
	}
	if s.Media == nil {
		http.Error(w, "media storage is not configured", http.StatusServiceUnavailable)
		return
	}
	dst := media.SocialPreviewPath(s.Cfg.ThumbsDir(), item.ContentHash)
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		src := media.BlobPath(s.Cfg.FilesDir(), item.ContentHash)
		if err := s.Media.GenerateSocialPreview(r.Context(), src, dst, media.IsVideoMime(item.Mime), item.Duration); err != nil {
			s.serverError(w, r, err)
			return
		}
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	filename := media.SanitizeFilename(item.Title + ".jpg")
	s.serveFile(w, r, dst, "image/jpeg", filename)
}

// handleSocialAnimation serves a cached H.264 rendition of a GIF for inline
// social embeds. Non-GIF items are rejected so this cannot become a public
// general-purpose transcoder.
func (s *Server) handleSocialAnimation(w http.ResponseWriter, r *http.Request) {
	if !s.checkPublicRateLimit(w, r) {
		return
	}
	item, ok := s.publicItem(w, r)
	if !ok {
		return
	}
	if !media.IsGifMime(item.Mime) {
		http.NotFound(w, r)
		return
	}
	if s.Media == nil {
		http.Error(w, "media storage is not configured", http.StatusServiceUnavailable)
		return
	}
	dst := media.SocialAnimationPath(s.Cfg.ThumbsDir(), item.ContentHash)
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		src := media.BlobPath(s.Cfg.FilesDir(), item.ContentHash)
		if err := s.Media.GenerateSocialAnimation(r.Context(), src, dst); err != nil {
			s.serverError(w, r, err)
			return
		}
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	filename := media.SanitizeFilename(item.Title + ".mp4")
	s.serveFile(w, r, dst, "video/mp4", filename)
}

// handleSocialVideo serves a cached H.264/AAC rendition for reliable browser
// and social embeds. MP4 describes a container, not its codecs, so serving an
// arbitrary uploaded MP4 directly can yield audio with a frozen video frame.
func (s *Server) handleSocialVideo(w http.ResponseWriter, r *http.Request) {
	if !s.checkPublicRateLimit(w, r) {
		return
	}
	item, ok := s.publicItem(w, r)
	if !ok {
		return
	}
	if !media.IsVideoMime(item.Mime) {
		http.NotFound(w, r)
		return
	}
	if s.Media == nil {
		http.Error(w, "media storage is not configured", http.StatusServiceUnavailable)
		return
	}
	dst := media.SocialVideoPath(s.Cfg.ThumbsDir(), item.ContentHash)
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		src := media.BlobPath(s.Cfg.FilesDir(), item.ContentHash)
		if err := s.Media.GenerateSocialVideo(r.Context(), src, dst); err != nil {
			s.serverError(w, r, err)
			return
		}
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	filename := media.SanitizeFilename(item.Title + ".mp4")
	s.serveFile(w, r, dst, "video/mp4", filename)
}

// checkPublicRateLimit enforces the public-route rate limit. It must be
// called first, before any other per-request work (including query
// validation), so that every request against /m/ and /t/ spends budget
// regardless of how it ultimately ends: a request rejected for a bad
// parameter is not free. It writes the 429 response and returns false when
// the client's budget is exhausted.
func (s *Server) checkPublicRateLimit(w http.ResponseWriter, r *http.Request) bool {
	if s.PublicLimiter != nil && !s.PublicLimiter.Allow(clientIP(r)) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return false
	}
	return true
}

// publicItem resolves the share slug, enforcing the revoked/deleted rules.
// Callers must call checkPublicRateLimit first: this only performs the
// lookup and does not itself touch the rate limiter, so it cannot be used
// to spend budget twice for one request.
func (s *Server) publicItem(w http.ResponseWriter, r *http.Request) (*db.Item, bool) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.NotFound(w, r)
		return nil, false
	}
	// ItemByID already excludes soft-deleted rows.
	item, err := s.Store.ItemByID(r.Context(), id)
	if err != nil {
		if !errors.Is(err, db.ErrNotFound) {
			s.serverError(w, r, err)
			return nil, false
		}
		http.NotFound(w, r)
		return nil, false
	}
	if !item.IsPubliclyServable() {
		// A revoked share link is indistinguishable from a nonexistent one.
		http.NotFound(w, r)
		return nil, false
	}
	return item, true
}

// serveFile writes a media response with the mandated security headers and
// full Range support.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, path, contentType, filename string) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Not an error: thumbnails 404 until their job has run, and the
			// grid falls back to a placeholder.
			http.NotFound(w, r)
			return
		}
		s.serverError(w, r, err)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	// Content-Type is set before ServeContent so it is never re-sniffed;
	// ServeContent only guesses when the header is absent.
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", immutableCacheControl)
	w.Header().Set("Referrer-Policy", "no-referrer")

	// ServeContent gives Accept-Ranges, 206 responses and conditional requests.
	http.ServeContent(w, r, filename, info.ModTime(), file)
}
