// Package update tells whether a newer release of Rocket Tracker is published
// on GitHub.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// LatestURL is the GitHub API endpoint of the latest release.
const LatestURL = "https://api.github.com/repos/KcivazB/rocket-tracker/releases/latest"

// Release is a published release newer than the running version.
type Release struct {
	Version  string `json:"version"` // without the leading "v"
	URL      string `json:"url"`     // release page
	Download string `json:"-"`       // the Asset file, "" when the release lacks it
}

// Asset is the release file of the desktop app.
const Asset = "rltracker.exe"

// Checker polls the latest release in the background.
type Checker struct {
	Current  string
	URL      string        // default LatestURL
	Every    time.Duration // default 12h
	Client   *http.Client  // default: 15s timeout
	Log      *slog.Logger
	OnNewer  func(Release) // called once per newer version found; may be nil
	newer    atomic.Pointer[Release]
	notified string
}

// Newer returns the newer release found by the last check, or nil.
func (c *Checker) Newer() *Release { return c.newer.Load() }

// Run checks now, then every c.Every, until ctx is done. It does nothing for
// a development build (a version that is not x.y.z).
func (c *Checker) Run(ctx context.Context) {
	if _, ok := parse(c.Current); !ok {
		return
	}
	every := c.Every
	if every <= 0 {
		every = 12 * time.Hour
	}
	for {
		c.check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

func (c *Checker) check(ctx context.Context) {
	r, err := c.fetch(ctx)
	if err != nil {
		if c.Log != nil && ctx.Err() == nil {
			c.Log.Debug("update check failed", "err", err)
		}
		return
	}
	if !IsNewer(r.Version, c.Current) {
		return
	}
	c.newer.Store(r)
	if c.notified != r.Version {
		c.notified = r.Version
		if c.Log != nil {
			c.Log.Info("a newer version is available", "version", r.Version, "url", r.URL)
		}
		if c.OnNewer != nil {
			c.OnNewer(*r)
		}
	}
}

func (c *Checker) fetch(ctx context.Context) (*Release, error) {
	u := c.URL
	if u == "" {
		u = LatestURL
	}
	cl := c.Client
	if cl == nil {
		cl = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "rltracker/"+c.Current)
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	var body struct {
		Tag        string `json:"tag_name"`
		URL        string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Draft || body.Prerelease {
		return nil, fmt.Errorf("latest release %s is not final", body.Tag)
	}
	r := &Release{Version: strings.TrimPrefix(body.Tag, "v"), URL: body.URL}
	for _, a := range body.Assets {
		if strings.EqualFold(a.Name, Asset) {
			r.Download = a.URL
		}
	}
	return r, nil
}

// IsNewer reports whether version a (x.y.z, optional "v") is above b.
// Unparsable versions are never newer.
func IsNewer(a, b string) bool {
	va, ok1 := parse(a)
	vb, ok2 := parse(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
