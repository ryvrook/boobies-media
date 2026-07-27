package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// DefaultSettings holds the built-in value for every admin-tunable setting.
// The settings table stores overrides only, so changing a default is a code
// change rather than a migration.
var DefaultSettings = map[string]string{
	// Lossless webp conversion, narrowed to static 8-bit RGB/RGBA PNG.
	"auto_webp": "on",
	// Total per-file cap. This is a policy limit the admin picks, not an
	// infrastructure one: uploads are chunked, so Cloudflare's 100 MB
	// request-body cap constrains upload_chunk_bytes and nothing else.
	"upload_max_bytes": "8589934592", // 8 GiB
	// One chunk = one HTTP request. Must stay under Cloudflare's 100 MB body
	// cap *and* finish inside its 125 s proxy read timeout on a slow upstream.
	"upload_chunk_bytes": "12582912", // 12 MiB
	// 2 GiB ceiling for a single remote download.
	"download_max_bytes": "2147483648",
	// Force H.264/AAC MP4 <=1080p so Discord embeds play inline.
	"ytdlp_format": `bv*[vcodec^=avc1][height<=1080]+ba[acodec^=mp4a]/b[ext=mp4]/b`,
	// Per-extractor exported cookie files; empty uses data/cookies/<name>.txt
	// when present.
	"cookies_twitter":     "",
	"cookies_youtube":     "",
	"cookies_tiktok":      "",
	"cookies_medal":       "",
	"min_free_disk_bytes": "1073741824",
}

// SettingGet returns the stored override for key, falling back to the built-in
// default. Unknown keys return ErrNotFound.
func (s *Store) SettingGet(ctx context.Context, key string) (string, error) {
	def, known := DefaultSettings[key]
	if !known {
		return "", fmt.Errorf("db: unknown setting %q: %w", key, ErrNotFound)
	}
	var value string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return def, nil
	case err != nil:
		return "", fmt.Errorf("db: read setting %q: %w", key, err)
	}
	return value, nil
}

// SettingSet stores an override. Unknown keys are rejected so a typo in the
// admin UI cannot silently write dead rows.
func (s *Store) SettingSet(ctx context.Context, key, value string) error {
	if _, known := DefaultSettings[key]; !known {
		return fmt.Errorf("db: unknown setting %q", key)
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("db: write setting %q: %w", key, err)
	}
	return nil
}

// SettingAll returns every known setting with overrides applied.
func (s *Store) SettingAll(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string, len(DefaultSettings))
	for k, v := range DefaultSettings {
		out[k] = v
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("db: read settings: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("db: scan setting: %w", err)
		}
		if _, known := DefaultSettings[k]; known {
			out[k] = v
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate settings: %w", err)
	}
	return out, nil
}
