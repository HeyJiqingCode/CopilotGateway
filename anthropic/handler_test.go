package anthropic

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/copilot"
	"github.com/HeyJiqingCode/CopilotGateway/testutil"
)

// TestServeHTTP_UnreachableUpstreamReturns502 checks that a transport failure
// is a JSON bad gateway for streaming and non-streaming requests alike: the
// SDK parses a non-2xx body as JSON, so an SSE body would surface as raw text.
func TestServeHTTP_UnreachableUpstreamReturns502(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	uc := copilot.NewClient(testutil.TokenProvider{BaseURL: "http://" + addr}, nil)
	h := NewHandler(uc, copilot.NewModels(uc, time.Minute))

	for _, stream := range []string{"false", "true"} {
		body := `{"model":"claude-opus-5.5","max_tokens":16,"stream":` + stream + `,"messages":[{"role":"user","content":"hi"}]}`
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))

		var resp errorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("stream=%s: body is not JSON: %v (%q)", stream, err, rec.Body.String())
		}
		if rec.Code != http.StatusBadGateway || resp.Error.Type != errTypeAPI {
			t.Errorf("stream=%s: status = %d, error = %+v; want 502 api_error", stream, rec.Code, resp.Error)
		}
	}
}
