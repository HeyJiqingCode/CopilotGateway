package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/auth"
	"github.com/HeyJiqingCode/CopilotGateway/internal/models"
	"github.com/HeyJiqingCode/CopilotGateway/internal/upstream"
)

// reauthTokenProvider simulates a gateway whose GitHub authorization is gone.
type reauthTokenProvider struct{}

func (reauthTokenProvider) GetToken(context.Context) (string, error) {
	return "", &auth.ReauthRequiredError{VerificationURI: "https://github.com/login/device", UserCode: "ABCD-1234", ExpiresAt: time.Now().Add(15 * time.Minute)}
}
func (reauthTokenProvider) GetBaseURL() string { return "http://127.0.0.1:1" }

func TestReauthRequired_Returns403WithDeviceCode(t *testing.T) {
	uc := upstream.NewClient(reauthTokenProvider{}, nil)
	h := &Handler{upstream: uc, models: models.NewCache(uc, 5*time.Minute)}

	for _, stream := range []bool{false, true} {
		body := `{"model":"claude-opus-5.5","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":` + map[bool]string{false: "false", true: "true"}[stream] + `}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("stream=%v: status = %d, want 403; body: %s", stream, rec.Code, rec.Body.String())
		}
		var resp AnthropicErrorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("stream=%v: body is not an Anthropic error: %v (%s)", stream, err, rec.Body.String())
		}
		if resp.Error.Type != AnthropicErrorTypePermission || !strings.Contains(resp.Error.Message, "ABCD-1234") {
			t.Fatalf("stream=%v: error = %+v, want permission_error with the device code", stream, resp.Error)
		}
	}
}
