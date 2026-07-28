// Package ingest safely acquires remote media and hands it to the media store.
package ingest

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var ErrUnsupportedSource = errors.New("ingest: unsupported source")

type SourceKind int

const (
	KindUnsupported SourceKind = iota
	KindDirect
	KindDiscordCDN
	KindYtDlp
)

func (k SourceKind) String() string {
	switch k {
	case KindDirect:
		return "direct"
	case KindDiscordCDN:
		return "discord-cdn"
	case KindYtDlp:
		return "yt-dlp"
	default:
		return "unsupported"
	}
}

var Extractors = []string{"twitter", "youtube", "tiktok", "medal"}
var DiscordCDNHosts = []string{"cdn.discordapp.com", "media.discordapp.net"}

var extractorHosts = map[string]string{
	"twitter.com": "twitter",
	"x.com":       "twitter",
	"youtube.com": "youtube",
	"youtu.be":    "youtube",
	"tiktok.com":  "tiktok",
	"medal.tv":    "medal",
}

type Classification struct {
	Kind      SourceKind
	Extractor string
	URL       string
}

func Classify(raw string) (Classification, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Classification{}, fmt.Errorf("%w: no URL given", ErrUnsupportedSource)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return Classification{}, fmt.Errorf("%w: invalid URL", ErrUnsupportedSource)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return Classification{}, fmt.Errorf("%w: only http and https links can be ingested", ErrUnsupportedSource)
	}
	parsed.Scheme = scheme
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return Classification{}, fmt.Errorf("%w: URL has no host", ErrUnsupportedSource)
	}
	parsed.Fragment = ""
	if hostMatches(host, "fixupx.com") {
		normalized, ok := normalizeFixupX(parsed)
		if !ok {
			return Classification{}, fmt.Errorf("%w: FixupX URL is not a Twitter status", ErrUnsupportedSource)
		}
		return Classification{Kind: KindYtDlp, Extractor: "twitter", URL: normalized}, nil
	}
	normalized := parsed.String()
	for _, candidate := range DiscordCDNHosts {
		if hostMatches(host, candidate) {
			return Classification{Kind: KindDiscordCDN, URL: normalized}, nil
		}
	}
	for candidate, extractor := range extractorHosts {
		if hostMatches(host, candidate) {
			return Classification{Kind: KindYtDlp, Extractor: extractor, URL: normalized}, nil
		}
	}
	return Classification{Kind: KindDirect, URL: normalized}, nil
}

func hostMatches(host, want string) bool {
	return host == want || strings.HasSuffix(host, "."+want)
}

func normalizeFixupX(parsed *url.URL) (string, bool) {
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 3 || !validTwitterUsername(parts[0]) || parts[1] != "status" || !allDigits(parts[2]) {
		return "", false
	}
	parsed.Scheme = "https"
	parsed.Host = "x.com"
	parsed.Path = "/" + parts[0] + "/status/" + parts[2]
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), true
}

func validTwitterUsername(value string) bool {
	if value == "" || len(value) > 15 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '_' {
			return false
		}
	}
	return true
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
