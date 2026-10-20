package anthropic

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/copilot"
	"github.com/HeyJiqingCode/CopilotGateway/testutil"
)

type capturedRequest struct {
	header http.Header
	raw    []byte
	body   map[string]json.RawMessage
}

// sendNativeMessages posts body to the handler, requires a 200 and returns
// what the fake upstream /v1/messages endpoint received.
func sendNativeMessages(t *testing.T, body string, betas ...string) capturedRequest {
	t.Helper()
	rec, got := serveNativeMessages(t, body, betas...)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	return got
}

// serveNativeMessages posts body to the handler against a fake upstream whose
// catalog has one model with reasoning effort (claude-opus-5.5) and one without
// (claude-haiku-4.5).
func serveNativeMessages(t *testing.T, body string, betas ...string) (*httptest.ResponseRecorder, capturedRequest) {
	t.Helper()
	var got capturedRequest
	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[{"id":"claude-opus-5.5","supported_endpoints":["/v1/messages"],"capabilities":{"supports":{"reasoning_effort":["low","high"]}}},` +
				`{"id":"claude-haiku-4.5","supported_endpoints":["/v1/messages"],"capabilities":{"supports":{}}}]}`))
			return
		}
		got.header = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		got.raw = raw
		if err := json.Unmarshal(raw, &got.body); err != nil {
			t.Errorf("upstream body is not JSON: %v (%s)", err, raw)
		}
		w.Write([]byte(`{"id":"msg_1","type":"message","content":[]}`))
	}))
	t.Cleanup(fakeUpstream.Close)

	uc := copilot.NewClient(testutil.TokenProvider{BaseURL: fakeUpstream.URL}, nil)
	h := &Handler{upstream: uc, models: copilot.NewModels(uc, 5*time.Minute)}

	req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(body))
	for _, beta := range betas {
		req.Header.Add("anthropic-beta", beta)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, got
}

const contextManagementBody = `{"model":"claude-opus-5.5","max_tokens":16,` +
	`"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},` +
	`"messages":[{"role":"user","content":"hi"}]}`

func TestServeHTTP_ForwardsAnthropicBeta(t *testing.T) {
	got := sendNativeMessages(t, contextManagementBody,
		"claude-code-20250219,context-management-2025-06-27", "per-turn-control-2026-07-01")

	want := "claude-code-20250219,context-management-2025-06-27,per-turn-control-2026-07-01"
	if beta := got.header.Get("anthropic-beta"); beta != want {
		t.Fatalf("upstream anthropic-beta = %q, want %q", beta, want)
	}
}

func TestServeHTTP_KeepsContextManagementWithBeta(t *testing.T) {
	got := sendNativeMessages(t, contextManagementBody, "context-management-2025-06-27")

	if string(got.body["context_management"]) != `{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}` {
		t.Fatalf("context_management = %s, want it forwarded unchanged", got.body["context_management"])
	}
}

func TestServeHTTP_StripsContextManagementWithoutBeta(t *testing.T) {
	// Copilot rejects context_management unless the context-management beta is present.
	got := sendNativeMessages(t, contextManagementBody)

	if _, ok := got.body["context_management"]; ok {
		t.Fatalf("context_management forwarded without the beta: %s", got.body["context_management"])
	}
	if beta := got.header.Get("anthropic-beta"); beta != "" {
		t.Fatalf("upstream anthropic-beta = %q, want none", beta)
	}
}
