package db_test

import (
	"context"
	"errors"
	"testing"

	"boobies-media/internal/db"
	"boobies-media/internal/dbtest"
)

func TestSettingGetReturnsBuiltInDefault(t *testing.T) {
	store := dbtest.New(t)
	got, err := store.SettingGet(context.Background(), "upload_max_bytes")
	if err != nil {
		t.Fatalf("SettingGet: %v", err)
	}
	if got != "8589934592" {
		t.Errorf("upload_max_bytes = %q, want \"8589934592\" (8 GiB total cap)", got)
	}
	chunk, err := store.SettingGet(context.Background(), "upload_chunk_bytes")
	if err != nil {
		t.Fatalf("SettingGet(upload_chunk_bytes): %v", err)
	}
	if chunk != "12582912" {
		t.Errorf("upload_chunk_bytes = %q, want \"12582912\" (12 MiB, safely under Cloudflare's 100 MB body cap)", chunk)
	}
}

func TestSettingGetUnknownKey(t *testing.T) {
	store := dbtest.New(t)
	if _, err := store.SettingGet(context.Background(), "not_a_setting"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("SettingGet unknown key returned %v, want ErrNotFound", err)
	}
}

func TestSettingSetOverridesDefaultAndUpserts(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)

	if err := store.SettingSet(ctx, "auto_webp", "off"); err != nil {
		t.Fatalf("SettingSet: %v", err)
	}
	got, err := store.SettingGet(ctx, "auto_webp")
	if err != nil {
		t.Fatalf("SettingGet: %v", err)
	}
	if got != "off" {
		t.Errorf("auto_webp = %q, want \"off\"", got)
	}

	// Setting the same key again must update, not fail on the primary key.
	if err := store.SettingSet(ctx, "auto_webp", "on"); err != nil {
		t.Fatalf("SettingSet (second call): %v", err)
	}
	got, err = store.SettingGet(ctx, "auto_webp")
	if err != nil {
		t.Fatalf("SettingGet: %v", err)
	}
	if got != "on" {
		t.Errorf("auto_webp = %q after re-set, want \"on\"", got)
	}
}

func TestSettingSetRejectsUnknownKey(t *testing.T) {
	store := dbtest.New(t)
	if err := store.SettingSet(context.Background(), "not_a_setting", "x"); err == nil {
		t.Fatal("SettingSet accepted an unknown key, want an error")
	}
}

func TestSettingAllMergesDefaultsAndOverrides(t *testing.T) {
	ctx := context.Background()
	store := dbtest.New(t)
	if err := store.SettingSet(ctx, "auto_webp", "off"); err != nil {
		t.Fatalf("SettingSet: %v", err)
	}
	all, err := store.SettingAll(ctx)
	if err != nil {
		t.Fatalf("SettingAll: %v", err)
	}
	if len(all) != len(db.DefaultSettings) {
		t.Errorf("SettingAll returned %d keys, want %d", len(all), len(db.DefaultSettings))
	}
	if all["auto_webp"] != "off" {
		t.Errorf("auto_webp = %q, want the override \"off\"", all["auto_webp"])
	}
	if all["ytdlp_format"] != db.DefaultSettings["ytdlp_format"] {
		t.Errorf("ytdlp_format = %q, want the default", all["ytdlp_format"])
	}
}

func TestDefaultYtdlpFormatForcesH264(t *testing.T) {
	// Discord only inline-plays H.264/AAC MP4. yt-dlp's own default would
	// pick VP9/AV1 for YouTube and silently break embeds.
	want := `bv*[vcodec^=avc1][height<=1080]+ba[acodec^=mp4a]/b[ext=mp4]/b`
	if got := db.DefaultSettings["ytdlp_format"]; got != want {
		t.Errorf("default ytdlp_format = %q, want %q", got, want)
	}
}
