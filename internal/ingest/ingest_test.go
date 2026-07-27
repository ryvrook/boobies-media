package ingest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"boobies-media/internal/media"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		raw       string
		kind      SourceKind
		extractor string
	}{
		{"https://x.com/user/status/1", KindYtDlp, "twitter"},
		{"https://WWW.YouTube.com/watch?v=x#fragment", KindYtDlp, "youtube"},
		{"https://cdn.discordapp.com/attachments/1/2/a.png", KindDiscordCDN, ""},
		{"https://example.com/a.png", KindDirect, ""},
		{"https://youtube.com.evil.test/a", KindDirect, ""},
	}
	for _, tc := range cases {
		got, err := Classify(tc.raw)
		if err != nil {
			t.Fatalf("Classify(%q): %v", tc.raw, err)
		}
		if got.Kind != tc.kind || got.Extractor != tc.extractor || strings.Contains(got.URL, "#") {
			t.Errorf("Classify(%q) = %+v", tc.raw, got)
		}
	}
	for _, raw := range []string{"", "file:///etc/passwd", "ftp://example.com/a", "not a url"} {
		if _, err := Classify(raw); !errors.Is(err, ErrUnsupportedSource) {
			t.Errorf("Classify(%q) = %v, want ErrUnsupportedSource", raw, err)
		}
	}
}

func TestAddressPolicy(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "fc00::1", "::ffff:127.0.0.1"} {
		if !IsBlockedAddr(netip.MustParseAddr(raw)) {
			t.Errorf("%s was not blocked", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if IsBlockedAddr(netip.MustParseAddr(raw)) {
			t.Errorf("%s was blocked", raw)
		}
	}
	if !errors.Is(CheckDialAddress("127.0.0.1:80"), ErrBlockedAddress) {
		t.Error("loopback dial address was allowed")
	}
}

func TestGuardedClientAndFetcher(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/large" {
			w.Header().Set("Content-Length", "100")
		}
		w.Header().Set("Content-Disposition", `attachment; filename="../cat.png"`)
		_, _ = io.WriteString(w, "fixture")
	}))
	defer server.Close()

	if _, err := NewGuardedClient(ClientOptions{}).Get(server.URL); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("guard error = %v, want ErrBlockedAddress", err)
	}
	tmp := t.TempDir()
	fetcher := NewFetcher(tmp, ClientOptions{AllowPrivateAddressesForTests: true})
	fetcher.DiskHeadroom = 0
	fetcher.FreeSpace = func(string) (uint64, error) { return 1 << 30, nil }
	result, err := fetcher.Fetch(context.Background(), server.URL, 32)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer os.Remove(result.Path)
	if result.Filename != "cat.png" || result.Size != 7 {
		t.Errorf("result = %+v", result)
	}
	if _, err := fetcher.Fetch(context.Background(), server.URL+"/large", 8); !errors.Is(err, ErrDownloadTooLarge) {
		t.Errorf("oversize error = %v", err)
	}
}

type testSettings map[string]string

func (s testSettings) SettingGet(_ context.Context, key string) (string, error) {
	value, ok := s[key]
	if !ok {
		return "", errors.New("missing")
	}
	return value, nil
}

func TestCookieResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "twitter.txt")
	if err := os.WriteFile(path, []byte("# Netscape"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveCookieFile(context.Background(), testSettings{"cookies_twitter": ""}, dir, "twitter")
	if err != nil || got != path {
		t.Fatalf("ResolveCookieFile = %q, %v", got, err)
	}
	if _, err := ResolveCookieFile(context.Background(), testSettings{}, dir, "unknown"); err == nil {
		t.Error("unknown extractor was accepted")
	}
}

type recordingRunner struct {
	name string
	args []string
	run  func(string, []string) error
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name, r.args = name, append([]string(nil), args...)
	if r.run != nil {
		return nil, r.run(name, args)
	}
	return nil, nil
}

func TestRipperUsesArgvAndCollectsOutput(t *testing.T) {
	runner := &recordingRunner{}
	runner.run = func(_ string, args []string) error {
		output := args[len(args)-3]
		dir := filepath.Dir(output)
		return os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("media"), 0o600)
	}
	ripper := NewRipper(runner, t.TempDir())
	result, err := ripper.RipWithYtDlp(context.Background(), RipRequest{
		URL: "https://youtube.com/watch?v=x", Extractor: "youtube", MaxBytes: 123,
	})
	if err != nil {
		t.Fatalf("RipWithYtDlp: %v", err)
	}
	defer os.RemoveAll(result.Dir)
	if runner.name != "yt-dlp" || !slices.Contains(runner.args, "--max-filesize") {
		t.Errorf("command = %s %v", runner.name, runner.args)
	}
	if runner.args[len(runner.args)-2] != "--" || len(result.Files) != 1 {
		t.Errorf("unsafe or incomplete result: args=%v files=%v", runner.args, result.Files)
	}
}

func TestToolErrorsAndPublicBoundary(t *testing.T) {
	if !errors.Is(TranslateToolError("yt-dlp", errors.New("exit"), "ERROR: Sign in to confirm"), ErrNeedsCookies) {
		t.Error("cookie failure not translated")
	}
	if _, ok := PublicError("media: ffmpeg leaked /data/private"); ok {
		t.Error("unmarked internal error was public")
	}
	message, ok := PublicError(publicErrorPrefix + "download too large")
	if !ok || message != "download too large" {
		t.Errorf("PublicError = %q, %v", message, ok)
	}
}

var _ media.Runner = (*recordingRunner)(nil)
