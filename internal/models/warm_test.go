package models

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
)

// warmLogs runs Warm against the counting catalog and returns what it logged.
func warmLogs(t *testing.T, failUpstream bool) string {
	t.Helper()
	var calls atomic.Int32
	var fail atomic.Bool
	fail.Store(failUpstream)
	c := countingCatalog(t, &calls, &fail)

	var logs bytes.Buffer
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	c.Warm(context.Background())
	return logs.String()
}

func TestCacheWarmLogsSuccess(t *testing.T) {
	if logs := warmLogs(t, false); !strings.Contains(logs, "models cache warmed") {
		t.Fatalf("logs = %q, want a warmed message", logs)
	}
}

func TestCacheWarmFailureIsNotReportedAsWarmed(t *testing.T) {
	logs := warmLogs(t, true)
	if !strings.Contains(logs, "failed to warm models cache") || strings.Contains(logs, "models cache warmed") {
		t.Fatalf("logs = %q, want only the failure", logs)
	}
}
