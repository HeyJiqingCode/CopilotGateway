package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/web"
)

// latestReleaseURL is GitHub's API for the newest published release; drafts
// and pre-releases are never returned.
const latestReleaseURL = "https://api.github.com/repos/HeyJiqingCode/CopilotGateway/releases/latest"

// updateChecker periodically asks GitHub for the latest release, so the logs
// and the dashboard can point to an upgrade.
type updateChecker struct {
	current string // this build's version, e.g. "1.2.0"
	url     string
	client  *http.Client

	mu     sync.RWMutex
	latest web.Release
	found  bool // latest is newer than current
}

func newUpdateChecker(current string) *updateChecker {
	return &updateChecker{current: current, url: latestReleaseURL, client: &http.Client{Timeout: 15 * time.Second}}
}

// enabled reports whether this build has a release version to compare.
// Development builds ("dev") never contact GitHub.
func (u *updateChecker) enabled() bool {
	_, ok := parseVersion(u.current)
	return ok
}

// run checks now and then every interval until ctx ends. A failed check is
// logged and keeps the previous result.
func (u *updateChecker) run(ctx context.Context, interval time.Duration) {
	if !u.enabled() {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := u.check(ctx); err != nil {
			slog.Warn("release check failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (u *updateChecker) check(ctx context.Context) error {
	if !u.enabled() {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "copilotgateway/"+u.current)
	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("latest release request failed with status %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return fmt.Errorf("parse latest release: %w", err)
	}

	found := newerVersion(release.TagName, u.current)
	u.mu.Lock()
	announce := found && !(u.found && u.latest.Version == release.TagName)
	u.latest = web.Release{Version: release.TagName, URL: release.HTMLURL, Current: u.current}
	u.found = found
	u.mu.Unlock()
	if announce {
		slog.Info("new CopilotGateway release available", "version", release.TagName, "current", u.current, "url", release.HTMLURL)
	}
	return nil
}

// newer returns the latest release when it is newer than this build.
func (u *updateChecker) newer() (web.Release, bool) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.latest, u.found
}

// newerVersion reports whether latest (a tag such as "v1.3.0") is a higher
// MAJOR.MINOR.PATCH than current. Anything else, such as "dev", compares false.
func newerVersion(latest, current string) bool {
	l, ok1 := parseVersion(latest)
	c, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}
