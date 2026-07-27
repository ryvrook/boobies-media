package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type SettingsReader interface {
	SettingGet(context.Context, string) (string, error)
}

func CookieSettingKey(extractor string) string { return "cookies_" + extractor }

func ResolveCookieFile(ctx context.Context, settings SettingsReader, cookiesDir, extractor string) (string, error) {
	if !isKnownExtractor(extractor) {
		return "", fmt.Errorf("ingest: unknown extractor %q", extractor)
	}
	if settings != nil {
		configured, err := settings.SettingGet(ctx, CookieSettingKey(extractor))
		if err != nil {
			return "", fmt.Errorf("ingest: read cookie setting for %s: %w", extractor, err)
		}
		if configured = strings.TrimSpace(configured); configured != "" {
			if err := checkCookieFile(configured); err != nil {
				return "", fmt.Errorf("ingest: configured %s cookie file is unusable: %w", extractor, err)
			}
			return configured, nil
		}
	}
	conventional := filepath.Join(cookiesDir, extractor+".txt")
	if err := checkCookieFile(conventional); err == nil {
		return conventional, nil
	}
	return "", nil
}

func checkCookieFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	return nil
}

func isKnownExtractor(extractor string) bool {
	for _, known := range Extractors {
		if extractor == known {
			return true
		}
	}
	return false
}
