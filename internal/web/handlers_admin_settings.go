package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"boobies-media/internal/ingest"
	"boobies-media/internal/jobs"
)

// settableKeys is the exact set of settings the admin form may write. It
// mirrors db.DefaultSettings exactly: a key that is not a real, known
// setting must be rejected here, with a clear message naming the field,
// rather than reaching db.SettingSet (whose own unknown-key error would
// otherwise surface as an opaque 500 through serverError).
var settableKeys = map[string]bool{
	"auto_webp":           true,
	"upload_max_bytes":    true,
	"upload_chunk_bytes":  true,
	"download_max_bytes":  true,
	"ytdlp_format":        true,
	"cookies_twitter":     true,
	"cookies_youtube":     true,
	"cookies_tiktok":      true,
	"cookies_medal":       true,
	"min_free_disk_bytes": true,
}

// byteSettingKeys must parse as a positive integer number of bytes.
var byteSettingKeys = map[string]bool{
	"upload_max_bytes":    true,
	"upload_chunk_bytes":  true,
	"download_max_bytes":  true,
	"min_free_disk_bytes": true,
}

const (
	// minUploadChunkBytes keeps a chunk large enough that per-request
	// overhead (one HTTP request per chunk) does not dominate throughput on
	// a large upload.
	minUploadChunkBytes int64 = 1 << 20 // 1 MiB

	// maxUploadChunkBytes stays well under Cloudflare's 100 MB request body
	// cap. The margin (100 MiB - 64 MiB = 36 MiB) covers multipart/header
	// overhead and keeps a full chunk finishing comfortably inside the
	// 125 s proxy read timeout even on a slow upstream link.
	maxUploadChunkBytes int64 = 64 << 20 // 64 MiB

	// maxByteSettingValue is a shared sanity ceiling for the byte-count
	// settings. It is not a technical limit; it exists only to catch an
	// obviously fat-fingered value (an extra digit) at save time instead of
	// silently accepting it.
	maxByteSettingValue int64 = 1 << 40 // 1 TiB

	maxYtdlpFormatLen = 1024
	maxCookiesPathLen = 4096
)

// handleSaveSettings serves POST /api/admin/settings. The body is a flat
// {key: value} JSON object of strings; only the keys in settableKeys are
// writable. Every value is validated before anything is persisted, so a bad
// request fails loudly here, naming the offending field, instead of being
// stored and failing later, far from the admin who typed it (in a yt-dlp
// invocation, a browser upload, or a strconv on the next read).
func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireSameOrigin(w, r) {
		return
	}
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "expected a flat JSON object of settings")
		return
	}
	if len(body) == 0 {
		writeJSONError(w, http.StatusBadRequest, "no_settings", "no settings given")
		return
	}

	// Sort keys so which field is reported first on a multi-error request is
	// deterministic rather than a function of Go's random map iteration.
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parsed := make(map[string]int64, len(body))
	for _, key := range keys {
		value := body[key]
		if !settableKeys[key] {
			writeJSONError(w, http.StatusBadRequest, "unknown_setting", "unknown setting: "+key)
			return
		}
		if byteSettingKeys[key] {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, "bad_number", key+" must be an integer number of bytes")
				return
			}
			if n <= 0 {
				writeJSONError(w, http.StatusBadRequest, "bad_value", key+" must be greater than zero")
				return
			}
			if n > maxByteSettingValue {
				writeJSONError(w, http.StatusBadRequest, "bad_value", key+" is implausibly large")
				return
			}
			if key == "upload_chunk_bytes" && (n < minUploadChunkBytes || n > maxUploadChunkBytes) {
				writeJSONError(w, http.StatusBadRequest, "bad_value",
					"upload_chunk_bytes must be between 1 MiB and 64 MiB so a chunk fits under Cloudflare's 100 MB request limit and finishes inside its 125 s proxy timeout")
				return
			}
			parsed[key] = n
			continue
		}
		switch key {
		case "auto_webp":
			if value != "on" && value != "off" {
				writeJSONError(w, http.StatusBadRequest, "bad_value", "auto_webp must be \"on\" or \"off\"")
				return
			}
		case "ytdlp_format":
			if value == "" {
				writeJSONError(w, http.StatusBadRequest, "bad_value", "ytdlp_format must not be empty")
				return
			}
			if len(value) > maxYtdlpFormatLen {
				writeJSONError(w, http.StatusBadRequest, "bad_value", "ytdlp_format is too long")
				return
			}
			if containsControlChar(value) {
				writeJSONError(w, http.StatusBadRequest, "bad_value", "ytdlp_format must not contain control characters")
				return
			}
		case "cookies_twitter", "cookies_youtube", "cookies_tiktok", "cookies_medal":
			// Empty is a valid value: it clears the override and falls back
			// to db.DefaultSettings' "not configured" state.
			if len(value) > maxCookiesPathLen {
				writeJSONError(w, http.StatusBadRequest, "bad_value", key+" path is too long")
				return
			}
			if containsControlChar(value) {
				writeJSONError(w, http.StatusBadRequest, "bad_value", key+" must not contain control characters")
				return
			}
		}
	}

	if err := s.checkUploadCapCoherence(r, body, parsed); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_value", err.Error())
		return
	}

	for _, key := range keys {
		if err := s.Store.SettingSet(r.Context(), key, body[key]); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

var testIngestURLs = map[string]string{
	"twitter": "https://twitter.com/jack/status/20",
	"youtube": "https://www.youtube.com/watch?v=aqz-KE-bpKQ",
	"tiktok":  "https://www.tiktok.com/@tiktok/video/7106594312292453675",
	"medal":   "https://medal.tv/games/valorant/clips/1",
}

func (s *Server) handleTestIngest(w http.ResponseWriter, r *http.Request) {
	if s.Queue == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "queue_unavailable", "the job queue is not running")
		return
	}
	if !s.requireSameOrigin(w, r) {
		return
	}
	user, ok := CurrentUser(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	var body struct {
		Extractor string `json:"extractor"`
		URL       string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if !isExtractor(body.Extractor) {
		writeJSONError(w, http.StatusBadRequest, "bad_extractor", "unknown extractor")
		return
	}
	if body.URL == "" {
		body.URL = testIngestURLs[body.Extractor]
	}
	jobID, err := s.Queue.Enqueue(r.Context(), jobs.TypeIngestURL, ingest.URLJob{URL: body.URL, UploaderID: user.ID})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID, "extractor": body.Extractor, "status": "queued"})
}

func isExtractor(name string) bool {
	for _, extractor := range ingest.Extractors {
		if name == extractor {
			return true
		}
	}
	return false
}

// checkUploadCapCoherence rejects a save that would leave upload_max_bytes
// below upload_chunk_bytes, an incoherent policy (a per-file cap smaller
// than the size of a single chunk). Either value may come from this request
// or from the currently stored setting, so a request that only touches one
// of the two is still checked against the other's live value.
func (s *Server) checkUploadCapCoherence(r *http.Request, body map[string]string, parsed map[string]int64) error {
	_, touchesMax := body["upload_max_bytes"]
	_, touchesChunk := body["upload_chunk_bytes"]
	if !touchesMax && !touchesChunk {
		return nil
	}
	maxBytes, err := s.resolveByteSetting(r, "upload_max_bytes", parsed)
	if err != nil {
		// The only writer of this setting is this handler, and every write
		// is validated, so a stored value should always parse. If it
		// somehow doesn't, skip the cross-check rather than block on it;
		// the request being saved is itself valid.
		return nil
	}
	chunkBytes, err := s.resolveByteSetting(r, "upload_chunk_bytes", parsed)
	if err != nil {
		return nil
	}
	if maxBytes < chunkBytes {
		return fmt.Errorf("upload_max_bytes must be greater than or equal to upload_chunk_bytes")
	}
	return nil
}

// resolveByteSetting returns the effective value of a byte-count setting:
// the value being saved in this request if present, otherwise the value
// currently stored.
func (s *Server) resolveByteSetting(r *http.Request, key string, parsed map[string]int64) (int64, error) {
	if v, ok := parsed[key]; ok {
		return v, nil
	}
	raw, err := s.Store.SettingGet(r.Context(), key)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(raw, 10, 64)
}

// containsControlChar reports whether s contains a byte below 0x20 (NUL,
// newline, tab, etc). Both ytdlp_format and cookies_twitter are stored as
// plain text and passed on to external tools or read back as a single
// value; neither should ever legitimately need a control character.
func containsControlChar(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 {
			return true
		}
	}
	return false
}
