package anthropic

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/copilot"
	"github.com/HeyJiqingCode/CopilotGateway/testutil"
)

func TestServeHTTP_KeepsLargeIntegersExact(t *testing.T) {
	// The official ID claude-opus-5-5 is rewritten to claude-opus-5.5, so the body is edited.
	got := sendNativeMessages(t, `{"model":"claude-opus-5-5","max_tokens":16,"messages":[{"role":"assistant","content":[`+
		`{"type":"tool_use","id":"t1","name":"Read","input":{"ts":1729340000123456789}}]}]}`)

	if string(got.body["model"]) != `"claude-opus-5.5"` {
		t.Fatalf("model = %s, want \"claude-opus-5.5\"", got.body["model"])
	}
	if !strings.Contains(string(got.raw), "1729340000123456789") {
		t.Fatalf("large integer altered in upstream body: %s", got.raw)
	}
}

func TestServeHTTP_ForwardsUntouchedFieldsByteForByte(t *testing.T) {
	messages := `[{"role":"user","content":[{"type":"text","text":"keep <b> & order"}]}]`
	got := sendNativeMessages(t, `{"model":"claude-opus-5-5","max_tokens":16,"messages":`+messages+`}`)

	if string(got.body["messages"]) != messages {
		t.Fatalf("messages = %s, want unchanged %s", got.body["messages"], messages)
	}
}

func TestServeHTTP_LeavesUnknownFieldShapesToUpstream(t *testing.T) {
	got := sendNativeMessages(t, `{"model":"claude-opus-5.5","max_tokens":16,"messages":[{"role":"user","content":"hi"}],`+
		`"metadata":{"user_id":"u1","trace":{"id":7}}}`)

	if string(got.body["metadata"]) != `{"user_id":"u1","trace":{"id":7}}` {
		t.Fatalf("metadata = %s, want it forwarded unchanged", got.body["metadata"])
	}
}

func TestServeHTTP_ForwardsUnchangedBodyAsIs(t *testing.T) {
	// Nothing to rewrite: spacing, key order and the word "scope" inside text all survive.
	body := `{ "model": "claude-opus-5.5", "max_tokens": 16,
		"messages": [ {"role": "user", "content": "explain the \"scope\" of this change"} ] }`
	got := sendNativeMessages(t, body)

	if string(got.raw) != body {
		t.Fatalf("upstream body = %s, want the original bytes", got.raw)
	}
}

func TestServeHTTP_RejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"not JSON", `{not json`, "Invalid JSON: "},
		{"not an object", `[1,2]`, "Invalid JSON: "},
		{"model wrong type", `{"model":5,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, "Invalid JSON: "},
		{"missing model", `{"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, "Invalid request: model is required"},
		{"zero max_tokens", `{"model":"claude-opus-5.5","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`, "Invalid request: max_tokens must be positive"},
		{"missing max_tokens", `{"model":"claude-opus-5.5","messages":[{"role":"user","content":"hi"}]}`, "Invalid request: max_tokens must be positive"},
		{"no messages or system", `{"model":"claude-opus-5.5","max_tokens":16,"messages":[]}`, "Invalid request: either messages or system must be provided"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := serveNativeMessages(t, tc.body)
			var resp errorResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			if rec.Code != http.StatusBadRequest || resp.Error.Type != errTypeInvalidRequest || !strings.HasPrefix(resp.Error.Message, tc.want) {
				t.Fatalf("status = %d, error = %+v; want 400 invalid_request_error starting with %q", rec.Code, resp.Error, tc.want)
			}
		})
	}
}

func TestServeHTTP_AcceptsSystemWithoutMessages(t *testing.T) {
	got := sendNativeMessages(t, `{"model":"claude-opus-5.5","max_tokens":16,"system":"you are helpful"}`)
	if string(got.body["system"]) != `"you are helpful"` {
		t.Fatalf("system = %s", got.body["system"])
	}
}

func TestServeHTTP_StripsReasoningForModelsWithoutEffort(t *testing.T) {
	got := sendNativeMessages(t, `{"model":"claude-haiku-4.5","max_tokens":16,"output_config":{"effort":"medium"},`+
		`"thinking":{"type":"enabled","budget_tokens":4000},"system":"you are helpful","messages":[{"role":"user","content":"hi"}]}`)

	for _, field := range []string{"output_config", "thinking"} {
		if _, ok := got.body[field]; ok {
			t.Errorf("%s forwarded to a model without reasoning effort", field)
		}
	}
	if string(got.body["system"]) != `"you are helpful"` || string(got.body["max_tokens"]) != "16" {
		t.Fatalf("other fields altered: %s", got.raw)
	}
}

func TestServeHTTP_KeepsReasoningForModelsWithEffort(t *testing.T) {
	got := sendNativeMessages(t, `{"model":"claude-opus-5.5","max_tokens":16,"output_config":{"effort":"high"},`+
		`"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hi"}]}`)

	if string(got.body["output_config"]) != `{"effort":"high"}` || string(got.body["thinking"]) != `{"type":"adaptive"}` {
		t.Fatalf("reasoning fields altered for a supported model: %s", got.raw)
	}
}

func TestServeHTTP_StripsCacheControlScope(t *testing.T) {
	got := sendNativeMessages(t, `{"model":"claude-opus-5.5","max_tokens":16,`+
		`"system":[{"type":"text","text":"one"},{"type":"text","text":"two","cache_control":{"type":"ephemeral","ttl":"1h","scope":"workspace"}}],`+
		`"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","scope":"tool"}}]}]}`)

	if strings.Contains(string(got.raw), `"scope"`) {
		t.Fatalf("cache_control.scope still forwarded: %s", got.raw)
	}
	if !strings.Contains(string(got.body["system"]), `"ttl":"1h"`) || !strings.Contains(string(got.body["messages"]), `"type":"ephemeral"`) {
		t.Fatalf("cache_control lost more than scope: %s", got.raw)
	}
}

// BenchmarkServeHTTPLongSession measures the gateway's own work on a ~1.5 MB
// request shaped like a long Claude Code session (upstream discards the body).
func BenchmarkServeHTTPLongSession(b *testing.B) {
	body := longSessionBody(400)
	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[{"id":"claude-opus-5.5","capabilities":{"supports":{"reasoning_effort":["high"]}}}]}`))
			return
		}
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{}`))
	}))
	defer fakeUpstream.Close()
	uc := copilot.NewClient(testutil.TokenProvider{BaseURL: fakeUpstream.URL}, nil)
	h := &Handler{upstream: uc, models: copilot.NewModels(uc, time.Hour)}

	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))

	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

func longSessionBody(turns int) string {
	var tools []map[string]any
	for i := 0; i < 30; i++ {
		tools = append(tools, map[string]any{"name": fmt.Sprintf("Tool%d", i), "description": strings.Repeat("Describe the tool. ", 40),
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}})
	}
	var msgs []map[string]any
	for i := 0; i < turns; i++ {
		msgs = append(msgs,
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": strings.Repeat("Please fix the file. ", 30)}}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "", "signature": strings.Repeat("c2lnbmF0dXJl", 60)},
				map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_%d", i), "name": "Read", "input": map[string]any{"path": "/src/main.go"}}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprintf("toolu_%d", i),
				"content": strings.Repeat("func main() { fmt.Println(\"hello\") }\n", 40), "cache_control": map[string]any{"type": "ephemeral"}}}})
	}
	b, _ := json.Marshal(map[string]any{"model": "claude-opus-5-5", "max_tokens": 128000, "stream": false,
		"system": []any{map[string]any{"type": "text", "text": strings.Repeat("You are Claude Code. ", 1500), "cache_control": map[string]any{"type": "ephemeral"}}},
		"tools":  tools, "messages": msgs, "thinking": map[string]any{"type": "adaptive"}, "output_config": map[string]any{"effort": "xhigh"}})
	return string(b)
}
