package web

import (
	"net/http"
	"strings"

	"boobies-media/internal/media"
)

// embedData is the template model for /s/{id}. Every URL is absolute so a
// crawler that only reads the <head> resolves them without a base.
type embedData struct {
	Title            string
	ShareURL         string
	MediaURL         string
	SecureMediaURL   string
	PosterURL        string
	SocialPreviewURL string
	OGImage          string
	OGImageType      string
	Mime             string
	Width            int64
	Height           int64
	// OGImageDimensionsKnown gates og:image:width/height. It is true only
	// when OGImage points at the original media (whose probed Width/Height
	// are its real dimensions), never when OGImage is a poster thumbnail:
	// GenerateThumbnail fits a poster inside a 1024x1024 box preserving
	// aspect ratio, so a source video larger than that on either axis has a
	// poster whose actual dimensions differ from the probed source
	// dimensions. Declaring the source's dimensions for the poster would be
	// a false claim a crawler that validates declared vs fetched size can
	// reject the card over.
	OGImageDimensionsKnown bool
	IsVideo                bool
	IsVideoEmbed           bool // an mp4 that gets full video OG tags
	SourceURL              string
	UploaderName           string
	UploaderInitial        string
}

// handleEmbed serves the anonymous share/viewer page at GET /s/{id}. It
// mirrors handlers_media.go's publicItem lookup so a revoked or deleted item
// 404s exactly like /m/ and /t/ do.
func (s *Server) handleEmbed(w http.ResponseWriter, r *http.Request) {
	if !s.checkPublicRateLimit(w, r) {
		return
	}
	item, ok := s.publicItem(w, r)
	if !ok {
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
		// Sliced as runes, not bytes: a display name starting with a
		// multi-byte UTF-8 character (e.g. "Émile") would otherwise cut mid
		// character and render an invalid byte in the avatar span.
		runes := []rune(uploaderName)
		initial = strings.ToUpper(string(runes[:1]))
	}

	base := strings.TrimRight(s.Cfg.BaseURL, "/")
	secure := base
	if strings.HasPrefix(secure, "http://") {
		secure = "https://" + strings.TrimPrefix(secure, "http://")
	}
	data := embedData{
		Title:            item.Title,
		ShareURL:         base + "/s/" + item.ID,
		MediaURL:         base + "/m/" + item.ID,
		SecureMediaURL:   secure + "/m/" + item.ID,
		PosterURL:        base + "/t/" + item.ID + "?s=1024",
		SocialPreviewURL: base + "/p/" + item.ID,
		Mime:             item.Mime,
		Width:            item.Width,
		Height:           item.Height,
		IsVideo:          media.IsVideoMime(item.Mime),
		SourceURL:        item.SourceURL,
		UploaderName:     uploaderName,
		UploaderInitial:  initial,
	}
	// Only H.264 MP4 gets a video card; everything else (webm, images) gets an
	// image card. yt-dlp downloads are constrained to mp4, so this is the norm.
	if item.Mime == "video/mp4" {
		data.IsVideoEmbed = true
	} else {
		data.OGImageType = item.Mime
		data.OGImage = data.MediaURL
		data.OGImageDimensionsKnown = data.Width > 0 && data.Height > 0
		if data.IsVideo || media.IsGifMime(item.Mime) {
			// A non-mp4 video cannot be an og:image; use its poster
			// thumbnail instead. That thumbnail is box-fit into 1024x1024
			// preserving aspect ratio (see media.GenerateThumbnail), so it is
			// not the same size as the source video: the source's probed
			// Width/Height must not be declared for it.
			data.OGImage = data.SocialPreviewURL
			data.OGImageType = "image/jpeg"
			data.OGImageDimensionsKnown = false
		}
	}

	w.Header().Set("Referrer-Policy", "no-referrer")
	if err := s.Renderer.RenderEmbed(w, http.StatusOK, data); err != nil {
		s.serverError(w, r, err)
	}
}
