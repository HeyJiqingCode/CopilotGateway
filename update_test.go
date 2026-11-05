package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// releaseAPI serves latestJSON as GitHub's latest-release response and counts requests.
func releaseAPI(t *testing.T, status int, latestJSON string, calls *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(latestJSON))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func checker(current, url string) *updateChecker {
	u := newUpdateChecker(current)
	u.url = url
	return u
}

func TestUpdateChecker_ReportsNewerRelease(t *testing.T) {
	var calls atomic.Int32
	url := releaseAPI(t, http.StatusOK,
		`{"tag_name":"v1.3.0","html_url":"https://github.com/HeyJiqingCode/CopilotGateway/releases/tag/v1.3.0"}`, &calls)
	u := checker("1.2.0", url)

	if err := u.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	rel, ok := u.newer()
	if !ok || rel.Version != "v1.3.0" || rel.URL != "https://github.com/HeyJiqingCode/CopilotGateway/releases/tag/v1.3.0" || rel.Current != "1.2.0" {
		t.Fatalf("newer() = %+v, %v; want v1.3.0 with its release page and current 1.2.0", rel, ok)
	}
}

func TestUpdateChecker_SameOrOlderReleaseIsNotNewer(t *testing.T) {
	for _, tag := range []string{"v1.2.0", "v1.1.9"} {
		var calls atomic.Int32
		u := checker("1.2.0", releaseAPI(t, http.StatusOK, `{"tag_name":"`+tag+`","html_url":"https://example.com"}`, &calls))
		if err := u.check(context.Background()); err != nil {
			t.Fatal(err)
		}
		if rel, ok := u.newer(); ok {
			t.Errorf("latest %s: newer() = %+v, want none for current 1.2.0", tag, rel)
		}
	}
}

func TestUpdateChecker_DevBuildNeverCallsGitHub(t *testing.T) {
	var calls atomic.Int32
	u := checker("dev", releaseAPI(t, http.StatusOK, `{"tag_name":"v9.9.9","html_url":"https://example.com"}`, &calls))

	if u.enabled() {
		t.Fatal("a dev build has no version to compare, so checking should be off")
	}
	if err := u.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := u.newer(); ok || calls.Load() != 0 {
		t.Fatalf("newer ok = %v, GitHub calls = %d; want no notice and no request", ok, calls.Load())
	}
}

func TestUpdateChecker_FailedCheckKeepsLastResult(t *testing.T) {
	var calls atomic.Int32
	u := checker("1.2.0", releaseAPI(t, http.StatusOK, `{"tag_name":"v1.3.0","html_url":"https://example.com/v1.3.0"}`, &calls))
	if err := u.check(context.Background()); err != nil {
		t.Fatal(err)
	}

	u.url = releaseAPI(t, http.StatusForbidden, `{"message":"API rate limit exceeded"}`, &calls)
	if err := u.check(context.Background()); err == nil {
		t.Fatal("check() = nil, want the GitHub error")
	}
	if rel, ok := u.newer(); !ok || rel.Version != "v1.3.0" {
		t.Fatalf("newer() = %+v, %v; want the earlier v1.3.0 kept", rel, ok)
	}
}

func TestNewerVersion(t *testing.T) {
	for _, tc := range []struct {
		latest, current string
		want            bool
	}{
		{"v1.3.0", "1.2.0", true},
		{"v1.10.0", "1.9.3", true}, // numeric, not string, comparison
		{"v2.0.0", "1.99.99", true},
		{"v1.2.0", "1.2.0", false},
		{"v1.2.0", "v1.2.0", false},
		{"v1.9.0", "1.10.0", false},
		{"v1.3.0", "dev", false},
		{"nightly", "1.2.0", false},
		{"v1.3", "1.2.0", false},
	} {
		if got := newerVersion(tc.latest, tc.current); got != tc.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
}
