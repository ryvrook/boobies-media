package web

import (
	"net/http"
	"strings"

	"boobies-media/internal/db"
	"boobies-media/internal/deps"
	"boobies-media/internal/ingest"
)

type adminUserRow struct {
	ID          int64
	Username    string
	DisplayName string
	IsAdmin     bool
	ItemCount   int
	HasKey      bool
}

type adminSetting struct {
	Key   string
	Label string
	Value string
}

type adminData struct {
	Users      []adminUserRow
	Settings   []adminSetting
	Jobs       []*db.Job
	Trash      []map[string]any
	Deps       []deps.Status
	DepsAllOK  bool
	Extractors []string
}

// handleAdmin renders the admin dashboard. Every mutation is a JSON endpoint
// (Tasks 8 to 10) driven by the admin island; this handler is read-only.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	user, _ := CurrentUser(r)
	ctx := r.Context()

	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rows := make([]adminUserRow, 0, len(users))
	for _, u := range users {
		count, err := s.Store.CountItemsByUploader(ctx, u.ID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		rows = append(rows, adminUserRow{
			ID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
			IsAdmin: u.IsAdmin, ItemCount: count, HasKey: u.APIKeyHash != "",
		})
	}

	all, err := s.Store.SettingAll(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	settings := []adminSetting{
		{"auto_webp", "Auto WebP conversion (on/off)", all["auto_webp"]},
		{"upload_max_bytes", "Total upload size cap (bytes)", all["upload_max_bytes"]},
		{"upload_chunk_bytes", "Upload chunk size (bytes), must stay under 100 MB", all["upload_chunk_bytes"]},
		{"download_max_bytes", "Download size cap (bytes)", all["download_max_bytes"]},
		{"ytdlp_format", "yt-dlp format string", all["ytdlp_format"]},
		{"cookies_twitter", "Twitter/X cookie file path", all["cookies_twitter"]},
		{"cookies_youtube", "YouTube cookie file path", all["cookies_youtube"]},
		{"cookies_tiktok", "TikTok cookie file path", all["cookies_tiktok"]},
		{"cookies_medal", "Medal cookie file path", all["cookies_medal"]},
		{"min_free_disk_bytes", "Minimum free disk (bytes)", all["min_free_disk_bytes"]},
	}

	jobs, err := s.Store.ListJobs(ctx, 50)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	trashItems, err := s.Store.ListDeletedItems(ctx, 100)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	trash, err := s.itemsPayload(r, trashItems)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := PageData{
		Title:   "Admin",
		SiteURL: strings.TrimRight(s.Cfg.BaseURL, "/"),
		User:    user,
		Data: adminData{
			Users:      rows,
			Settings:   settings,
			Jobs:       jobs,
			Trash:      trash,
			Deps:       s.Deps,
			DepsAllOK:  deps.AllOK(s.Deps),
			Extractors: ingest.Extractors,
		},
	}
	if err := s.Renderer.Render(w, http.StatusOK, "admin", data); err != nil {
		s.serverError(w, r, err)
	}
}
