package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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
	handler := &Handler{upstream: uc, modelsCache: models.NewCache(uc, 5*time.Minute)}

	for _, tc := range []struct{ name, method, path, body string }{
		{"non-streaming", "POST", "/v1/responses", `{"model":"gpt-5.5","input":"hi"}`},
		{"streaming", "POST", "/v1/chat/completions", `{"model":"gpt-5.5","messages":[],"stream":true}`},
		{"models", "GET", "/v1/models", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Waiting for sign-in is an expected state, not a server error.
			var logs bytes.Buffer
			defer slog.SetDefault(slog.Default())
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))

			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if logs.Len() > 0 {
				t.Errorf("logged a warning or error for a pending sign-in: %s", logs.String())
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
			}
			var resp OpenAIErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("body is not an OpenAI error: %v (%s)", err, rec.Body.String())
			}
			if resp.Error.Code != "copilot_auth_required" || !strings.Contains(resp.Error.Message, "ABCD-1234") {
				t.Fatalf("error = %+v, want code copilot_auth_required with the device code", resp.Error)
			}
		})
	}
}
