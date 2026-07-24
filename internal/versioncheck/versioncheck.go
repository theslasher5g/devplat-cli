// Package versioncheck compares the running CLI against the latest published
// release, read from the release host's version.txt. It's best-effort and
// quick: every failure is treated as "no update information", never an error
// the user has to see.
package versioncheck

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const versionURL = "https://get.devplat.ch/version.txt"

var semverRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

// Latest fetches the current published version (e.g. "v1.2.0"), normalised to
// the v-prefixed form the release paths use. Returns "" on any failure or
// malformed content — callers treat that as "unknown", not an error.
func Latest(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, versionURL, nil)
	if err != nil {
		return ""
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ""
	}
	// version.txt is a few bytes; cap the read so a misconfigured host serving
	// something huge can't make us buffer it.
	b, err := io.ReadAll(io.LimitReader(res.Body, 64))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(b))
	if !semverRe.MatchString(v) {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

// Outdated reports whether current is a released build strictly older than
// latest. A "dev" or unparseable current, or an empty latest, is never
// outdated — so an unversioned local build and a failed lookup both stay quiet.
func Outdated(current, latest string) bool {
	c, l := parse(current), parse(latest)
	if c == nil || l == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if c[i] != l[i] {
			return c[i] < l[i]
		}
	}
	return false
}

func parse(v string) []int {
	if !semverRe.MatchString(strings.TrimSpace(v)) {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	out := make([]int, 3)
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}
