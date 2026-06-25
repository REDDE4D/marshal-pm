// Package updatecheck reports whether a newer Marshal release is available. It
// is deliberately minimal and privacy-respecting: a single anonymous request to
// GitHub's /releases/latest redirect (no API token, not rate-limited, no
// identifiers sent), used by the server to surface an "update available" hint in
// the dashboard. It never downloads or replaces anything.
package updatecheck

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultReleasesURL is GitHub's "latest release" endpoint, which 302-redirects
// to /releases/tag/vX.Y.Z. Hitting the redirect avoids the rate-limited API.
const DefaultReleasesURL = "https://github.com/REDDE4D/marshal-pm/releases/latest"

// Result is the outcome of an update check.
type Result struct {
	Current   string    `json:"current"`
	Latest    string    `json:"latest"`
	Outdated  bool      `json:"outdated"`
	CheckedAt time.Time `json:"checked_at"`
}

// Outdated reports whether latest is a newer release than current. It compares
// the leading numeric major.minor.patch only; a current version that can't be
// parsed (e.g. the "0.0.0-dev" default or an empty string) returns false so we
// never nag local/dev builds, and a git-describe build like "v0.4.1-3-gabc" is
// treated as its base release v0.4.1.
func Outdated(current, latest string) bool {
	cMaj, cMin, cPatch, cDev, ok := parseVersion(current)
	if !ok || cDev {
		return false
	}
	lMaj, lMin, lPatch, _, ok := parseVersion(latest)
	if !ok {
		return false
	}
	if lMaj != cMaj {
		return lMaj > cMaj
	}
	if lMin != cMin {
		return lMin > cMin
	}
	return lPatch > cPatch
}

// parseVersion extracts major/minor/patch from a "vX.Y.Z" string (the leading
// v is optional). dev reports whether the version carries a pre-release/build
// suffix that means "not a clean release" — specifically the "0.0.0-dev" default
// (X.Y.Z all zero with a suffix). A git-describe suffix on a real version (e.g.
// "v0.4.1-3-gabc") parses to its base X.Y.Z with dev=false.
func parseVersion(s string) (maj, min, patch int, dev, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return 0, 0, 0, false, false
	}
	// Split off any pre-release/build suffix introduced by "-".
	core := s
	suffix := ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core, suffix = s[:i], s[i+1:]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return 0, 0, 0, false, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, 0, 0, false, false
		}
		nums[i] = n
	}
	// The in-source default 0.0.0-dev (or any all-zero version) is a dev build.
	dev = suffix != "" && nums[0] == 0 && nums[1] == 0 && nums[2] == 0
	return nums[0], nums[1], nums[2], dev, true
}

// fetchLatest does one non-redirect-following GET to releasesURL and extracts
// the version from the Location header's .../releases/tag/<version> path.
func fetchLatest(ctx context.Context, client *http.Client, releasesURL string) (string, error) {
	// Copy the client so we can disable redirect-following without mutating the
	// caller's client.
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("update check: no redirect from %s (status %d)", releasesURL, resp.StatusCode)
	}
	const marker = "/releases/tag/"
	i := strings.LastIndex(loc, marker)
	if i < 0 {
		return "", errors.New("update check: unexpected redirect target " + loc)
	}
	v := loc[i+len(marker):]
	if v == "" {
		return "", errors.New("update check: empty version in redirect")
	}
	return v, nil
}
