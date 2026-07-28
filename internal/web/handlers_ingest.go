package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"boobies-media/internal/db"
	"boobies-media/internal/ingest"
	"boobies-media/internal/jobs"
	"boobies-media/internal/media"
)

// multipartMemoryLimit is how much of a multipart body is buffered in RAM
// before Go spills the rest to a temp file. It bounds memory, not disk: the
// real ceiling on what gets written to that temp file is upload_chunk_bytes
// plus multipartFramingSlack below, applied via http.MaxBytesReader before
// parsing ever starts.
const multipartMemoryLimit = 1 << 20 // 1 MiB

// multipartFramingSlack is added on top of the chunk cap when bounding the
// raw HTTP body. mime/multipart's own boundary markers, part headers and
// trailing CRLFs add a small, fixed amount of overhead around the file bytes
// themselves; without this slack, a file sized exactly at the chunk cap
// would be rejected for the framing alone. It is a fixed constant, not
// proportional to the cap, so it cannot itself become a meaningful
// resource-exhaustion allowance.
const multipartFramingSlack = 4 << 10 // 4 KiB

// handleIngest accepts a single uploaded file at POST /api/ingest.
//
// Plan 3 extends the same route with a JSON {url} branch; both go through
// media.Store.Save, so the allowlist, optimization and dedup rules can never
// diverge between an upload and a paste.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if !s.requireMedia(w, r) || !s.requireSameOrigin(w, r) {
		return
	}
	user, ok := CurrentUser(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		s.handleIngestURL(w, r, user)
		return
	}

	// A single request can never exceed Cloudflare's body cap, so the
	// convenience path is capped at one chunk; anything larger belongs on the
	// chunked flow. That cap has to be enforced here, at the HTTP layer,
	// before parsing: mime/multipart.Reader.readForm spools any part over
	// multipartMemoryLimit to a temp file via an unbounded io.Copy, with no
	// size check of its own. Left unwrapped, r.Body, an authenticated caller
	// could make the server write an arbitrarily large body to a temp file
	// (often backed by tmpfs, i.e. RAM) long before media.Store.Save's own
	// cap is ever consulted. Wrapping r.Body first means ParseMultipartForm
	// itself fails the moment it tries to read past the cap.
	_, chunkBytes, err := s.uploadLimits(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, chunkBytes+multipartFramingSlack)

	if err := r.ParseMultipartForm(multipartMemoryLimit); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "too_large",
				"that file is larger than the upload limit")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "bad_request", "expected a multipart upload with a file field")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "no file field in the upload")
		return
	}
	defer file.Close()

	result, err := s.Media.Save(r.Context(), media.SaveRequest{
		Reader:     file,
		Filename:   header.Filename,
		UploaderID: user.ID,
		MaxBytes:   chunkBytes,
	})
	if err != nil {
		s.writeIngestError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"item":         itemJSON(result.Item, nil, s.Cfg.BaseURL),
		"deduplicated": result.Deduplicated,
		"optimized":    result.Optimized,
	})
}

func (s *Server) handleIngestURL(w http.ResponseWriter, r *http.Request, user *db.User) {
	if s.Queue == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "queue_unavailable", "the job queue is not running")
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", `expected a JSON body like {"url":"https://…"}`)
		return
	}
	classification, err := ingest.Classify(body.URL)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "unsupported_url", "only http and https links can be ingested")
		return
	}
	jobID, err := s.Queue.Enqueue(r.Context(), jobs.TypeIngestURL, ingest.URLJob{
		URL: classification.URL, UploaderID: user.ID,
	})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id": jobID, "status": "queued", "kind": classification.Kind.String(),
	})
}

// writeIngestError maps pipeline failures onto honest status codes.
func (s *Server) writeIngestError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, media.ErrUnsupportedType):
		writeJSONError(w, http.StatusUnsupportedMediaType, "unsupported_type",
			"that file type is not accepted. Images (jpeg, png, gif, webp, avif) and videos (mp4, webm) only.")
	case errors.Is(err, media.ErrTooLarge):
		writeJSONError(w, http.StatusRequestEntityTooLarge, "too_large",
			"that file is larger than the upload limit")
	default:
		s.serverError(w, r, err)
	}
}

// requireMedia guards routes that need the media store. It returns false and
// writes a response when the server was built without one.
func (s *Server) requireMedia(w http.ResponseWriter, r *http.Request) bool {
	if s.Media == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "media_unavailable", "media storage is not configured")
		return false
	}
	return true
}

// itemJSON is the one item serialisation used by every JSON response, so the
// islands and the Discord bot always see the same shape.
func itemJSON(item *db.Item, tags []string, baseURL string) map[string]any {
	if tags == nil {
		tags = []string{}
	}
	return map[string]any{
		"id":         item.ID,
		"title":      item.Title,
		"mime":       item.Mime,
		"ext":        item.Ext,
		"size":       item.Size,
		"width":      item.Width,
		"height":     item.Height,
		"duration":   item.Duration,
		"uploader":   item.UploaderID,
		"folder_id":  item.FolderID,
		"source_url": item.SourceURL,
		"tags":       tags,
		"is_video":   media.IsVideoMime(item.Mime),
		"is_gif":     media.IsAnimatedImage(item.Mime, item.Duration),
		// ready is false until the probe job fills in the dimensions; the grid
		// shows a processing placeholder until then.
		"ready":      item.Width > 0,
		"revoked":    item.ShareRevoked,
		"created_at": item.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		"share_url":  fmt.Sprintf("%s/s/%s", baseURL, item.ID),
		"media_url":  fmt.Sprintf("%s/m/%s", baseURL, item.ID),
		"thumb_url":  fmt.Sprintf("%s/t/%s?s=320", baseURL, item.ID),
	}
}
