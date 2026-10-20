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
	"github.com/HeyJiqingCode/CopilotGateway/internal/testutil"
	"github.com/HeyJiqingCode/CopilotGateway/internal/upstream"
)

// captureUpstream records the body of the last request it receives.
func captureUpstream(t *testing.T, got *[]byte) *Handler {
	t.Helper()
	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"resp_1"}`))
	}))
	t.Cleanup(fakeUpstream.Close)

	uc := upstream.NewClient(testutil.TokenProvider{BaseURL: fakeUpstream.URL}, nil)
	return NewHandler(uc, models.NewCache(uc, 5*time.Minute))
}

func postResponses(t *testing.T, handler *Handler, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestAutoReviewModel_RewritesPseudoModel(t *testing.T) {
	var got []byte
	handler := captureUpstream(t, &got)
	handler.AutoReviewModel = "gpt-6-luna"

	postResponses(t, handler, `{"model":"codex-auto-review","input":"review","reasoning":{"effort":"low"},"stream":false}`)

	var sent map[string]json.RawMessage
	if err := json.Unmarshal(got, &sent); err != nil {
		t.Fatalf("upstream body is not JSON: %v (%s)", err, got)
	}
	if string(sent["model"]) != `"gpt-6-luna"` {
		t.Fatalf("upstream model = %s, want \"gpt-6-luna\"", sent["model"])
	}
	if string(sent["reasoning"]) != `{"effort":"low"}` || string(sent["input"]) != `"review"` {
		t.Fatalf("other fields changed: %s", got)
	}
}

func TestAutoReviewModel_OtherModelsStayByteForByte(t *testing.T) {
	var got []byte
	handler := captureUpstream(t, &got)
	handler.AutoReviewModel = "gpt-6-luna"

	body := `{"stream": false, "model": "gpt-5.5", "input": "hi"}`
	postResponses(t, handler, body)

	if string(got) != body {
		t.Fatalf("upstream body = %s, want unchanged %s", got, body)
	}
}

func TestAutoReviewModel_UnsetForwardsPseudoModel(t *testing.T) {
	var got []byte
	handler := captureUpstream(t, &got)

	body := `{"model":"codex-auto-review","input":"review","stream":false}`
	postResponses(t, handler, body)

	if string(got) != body {
		t.Fatalf("upstream body = %s, want unchanged %s", got, body)
	}
}
