package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/internal/models"
	"github.com/HeyJiqingCode/CopilotGateway/internal/upstream"
)

func TestParseModelRoutes(t *testing.T) {
	routes, err := ParseModelRoutes("")
	if err != nil || len(routes) != 0 {
		t.Fatalf("empty value: routes=%v err=%v, want no routes and no error", routes, err)
	}

	routes, err = ParseModelRoutes(` {"codex-auto-review":"gpt-5.6-luna"} `)
	if err != nil {
		t.Fatalf("valid JSON: unexpected error %v", err)
	}
	if got := routes["codex-auto-review"]; got != "gpt-5.6-luna" {
		t.Fatalf("codex-auto-review routes to %q, want gpt-5.6-luna", got)
	}

	for _, raw := range []string{
		`not json`,
		`null`,
		`["codex-auto-review"]`,
		`{"":"gpt-5.6-luna"}`,
		`{"codex-auto-review":""}`,
		`{" codex-auto-review":"gpt-5.6-luna"}`,
		`{"gpt-5.6-luna":"gpt-5.6-luna"}`,
	} {
		if _, err := ParseModelRoutes(raw); err == nil {
			t.Errorf("ParseModelRoutes(%s) succeeded, want error", raw)
		}
	}
}

// captureUpstream records the body of the last request it receives.
func captureUpstream(t *testing.T, got *[]byte) *Handler {
	t.Helper()
	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"resp_1"}`))
	}))
	t.Cleanup(fakeUpstream.Close)

	uc := upstream.NewClient(&stubTokenProvider{baseURL: fakeUpstream.URL}, nil)
	return &Handler{upstream: uc, modelsCache: models.NewCache(uc, 5*time.Minute)}
}

func TestHandlePassthrough_RewritesRoutedModel(t *testing.T) {
	var got []byte
	handler := captureUpstream(t, &got)
	handler.ModelRoutes = ModelRoutes{"codex-auto-review": "gpt-5.6-luna"}

	body := `{"model":"codex-auto-review","input":"review","reasoning":{"effort":"low"},"stream":false}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.handlePassthrough(rec, req, "/responses")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(got, &sent); err != nil {
		t.Fatalf("upstream body is not JSON: %v (%s)", err, got)
	}
	if string(sent["model"]) != `"gpt-5.6-luna"` {
		t.Fatalf("upstream model = %s, want \"gpt-5.6-luna\"", sent["model"])
	}
	if string(sent["reasoning"]) != `{"effort":"low"}` || string(sent["input"]) != `"review"` {
		t.Fatalf("other fields changed: %s", got)
	}
}

func TestHandlePassthrough_UnroutedModelBodyUnchanged(t *testing.T) {
	var got []byte
	handler := captureUpstream(t, &got)
	handler.ModelRoutes = ModelRoutes{"codex-auto-review": "gpt-5.6-luna"}

	// Key order and spacing must survive: unrouted requests stay byte-for-byte passthrough.
	body := `{"stream": false, "model": "gpt-5.5", "input": "hi"}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.handlePassthrough(rec, req, "/responses")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if string(got) != body {
		t.Fatalf("upstream body = %s, want unchanged %s", got, body)
	}
}
