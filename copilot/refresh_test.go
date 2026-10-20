package copilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/testutil"
)

// countingCatalog serves a catalog naming the current call count; fail makes it return 502.
func countingCatalog(t *testing.T, calls *atomic.Int32, fail *atomic.Bool) *Models {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"model-` + string(rune('0'+n)) + `"}]}`))
	}))
	t.Cleanup(srv.Close)
	return NewModels(NewClient(testutil.TokenProvider{BaseURL: srv.URL}, nil), time.Hour)
}

func TestCacheRefreshBypassesTTL(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	c := countingCatalog(t, &calls, &fail)

	if _, err := c.GetRaw(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstAt := c.CachedAt()
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	raw, _ := c.GetRaw(context.Background())
	if calls.Load() != 2 || !strings.Contains(string(raw), "model-2") {
		t.Fatalf("calls = %d, raw = %s; want a second upstream fetch served from the cache", calls.Load(), raw)
	}
	if !c.CachedAt().After(firstAt) {
		t.Fatalf("CachedAt not updated by Refresh")
	}
}

func TestCacheRefreshFailureKeepsCachedCopy(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	c := countingCatalog(t, &calls, &fail)
	if _, err := c.GetRaw(context.Background()); err != nil {
		t.Fatal(err)
	}

	fail.Store(true)
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh succeeded against a failing upstream")
	}

	raw, err := c.GetRaw(context.Background())
	if err != nil || !strings.Contains(string(raw), "model-1") {
		t.Fatalf("GetRaw after failed Refresh = %s, %v; want the previous catalog", raw, err)
	}
}
