package web

import (
	"fmt"
	"html/template"
	"math"
	"net/url"
	"strconv"
)

// templateFuncs are available to every page template. They are pure
// formatting helpers only (no I/O, no per-request state), so a template can
// safely call them while rendering.
var templateFuncs = template.FuncMap{
	"fmtBytes":    fmtBytes,
	"fmtDuration": fmtDuration,
	"aspectRatio": aspectRatio,
	"queryString": queryString,
	"indent":      indent,
	"add1":        add1,
}

// fmtBytes renders a byte count the way the browse grid's caption bar and the
// lightbox metadata panel show file size: decimal units (not GiB/MiB), one
// decimal place unless the value is a whole number.
func fmtBytes(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	value := float64(n)
	idx := -1
	for value >= 1000 && idx < len(units)-1 {
		value /= 1000
		idx++
	}
	if value == math.Trunc(value) {
		return fmt.Sprintf("%.0f %s", value, units[idx])
	}
	return fmt.Sprintf("%.1f %s", value, units[idx])
}

// fmtDuration renders seconds as mm:ss, matching the design's video runtime
// display. An hour or more (unlikely for a friend-group clip) falls back to
// h:mm:ss instead of an unbounded minute count.
func fmtDuration(seconds float64) string {
	total := max(int(math.Round(seconds)), 0)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// aspectRatio is width/height, used as the CSS flex-grow ratio that drives
// the justified grid (see main.css's .tile). It falls back to a square tile
// when dimensions are not known yet (an item still processing).
func aspectRatio(width, height int64) float64 {
	if width <= 0 || height <= 0 {
		return 1
	}
	return float64(width) / float64(height)
}

// queryString builds a browse-page query string ("?tag=holiday&sort=size")
// from alternating key/value pairs. A zero-valued pair (an empty string, or
// a numeric 0, the sentinel this package uses for "no folder"/"no uploader")
// is dropped rather than rendered as "key=". That is what lets every rail
// link pass its whole current filter state and just override the one
// dimension it changes: passing 0 for "folder" alongside the rest clears the
// folder filter while leaving tag/uploader/q/sort exactly as they were.
// Returns "/" (the plain browse URL) when every pair drops.
func queryString(pairs ...any) string {
	values := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			continue
		}
		if val := stringifyQueryValue(pairs[i+1]); val != "" {
			values.Set(key, val)
		}
	}
	qs := values.Encode()
	if qs == "" {
		return "/"
	}
	return "/?" + qs
}

func stringifyQueryValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int64:
		if t == 0 {
			return ""
		}
		return strconv.FormatInt(t, 10)
	case int:
		if t == 0 {
			return ""
		}
		return strconv.Itoa(t)
	default:
		return ""
	}
}

// indent turns a folder tree depth into a left-padding pixel value for the
// Folders rail.
func indent(depth int) int { return 16 + depth*14 }

// add1 lets the breadcrumb template detect "this is the last path segment"
// by comparing a 0-based range index against len(path).
func add1(i int) int { return i + 1 }
