// Package deps probes for the external command-line tools the ingestion
// pipeline shells out to. Every failure here is soft: a home server must keep
// serving its existing library when a distro upgrade breaks a tool.
package deps

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Required lists the tools the server uses. yt-dlp and gallery-dl handle
// remote ingestion, ffmpeg/ffprobe handle probing and poster frames, and cwebp
// handles lossless webp conversion.
var Required = []string{"yt-dlp", "gallery-dl", "ffmpeg", "ffprobe", "cwebp"}

// probeTimeout bounds a single `<tool> --version` invocation.
const probeTimeout = 3 * time.Second

// Status is the result of probing one tool.
type Status struct {
	Name    string
	Path    string
	Version string
	OK      bool
	Err     string
}

// Probe looks up each name on PATH and reads its version. It never returns an
// error: callers record the statuses and show them in the admin banner.
func Probe(ctx context.Context, names []string) []Status {
	statuses := make([]Status, 0, len(names))
	for _, name := range names {
		statuses = append(statuses, probeOne(ctx, name))
	}
	return statuses
}

func probeOne(ctx context.Context, name string) Status {
	status := Status{Name: name}

	path, err := exec.LookPath(name)
	if err != nil {
		status.Err = name + " was not found on PATH; features that need it will fail with a clear message"
		return status
	}
	status.Path = path

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	flag := versionFlag(name)
	// Always argv arrays, never shell strings.
	out, err := exec.CommandContext(ctx, path, flag).CombinedOutput()
	if err != nil {
		status.Err = name + " is installed at " + path + " but `" + flag + "` failed: " + err.Error()
		return status
	}
	status.Version = strings.TrimSpace(firstLine(string(out)))
	status.OK = true
	return status
}

// versionFlag returns the flag that prints name's version. ffmpeg, ffprobe
// and cwebp share FFmpeg/libwebp's minimal argv parser, which has never
// accepted GNU-style double-dash long options: it strips exactly one leading
// dash, so "--version" arrives as the unregistered option "-version" (note
// the leftover dash) and the tool exits nonzero, misreporting a present tool
// as missing. yt-dlp and gallery-dl are Python argparse tools and need the
// double dash. Reproduced against ffmpeg/ffprobe n8.1.2.
func versionFlag(name string) string {
	switch name {
	case "ffmpeg", "ffprobe", "cwebp":
		return "-version"
	default:
		return "--version"
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// AllOK reports whether every probed tool is present and working.
func AllOK(statuses []Status) bool {
	for _, s := range statuses {
		if !s.OK {
			return false
		}
	}
	return true
}
