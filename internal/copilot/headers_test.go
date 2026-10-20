package copilot

import (
	"net/http"
	"strings"
	"testing"
)

func TestAddCopilotHeaders(t *testing.T) {
	req, err := http.NewRequest("POST", "http://example.com", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	token := "test-token-123"
	AddHeaders(req, token)

	// Test required headers
	expectedHeaders := map[string]string{
		"Authorization":          "Bearer " + token,
		"User-Agent":             CopilotUserAgent,
		"Editor-Version":         EditorVersion,
		"Editor-Plugin-Version":  EditorPluginVersion,
		"Copilot-Integration-Id": "vscode-chat",
		"Openai-Intent":          "conversation-agent",
		"Content-Type":           "application/json",
		"X-Github-Api-Version":   APIVersion,
	}

	for header, expectedValue := range expectedHeaders {
		actualValue := req.Header.Get(header)
		if actualValue != expectedValue {
			t.Errorf("Header %s: expected %q, got %q", header, expectedValue, actualValue)
		}
	}

	// Test that X-Request-Id is present
	requestID := req.Header.Get("X-Request-Id")
	if requestID == "" {
		t.Error("X-Request-Id header should be present")
	}
	if interactionType := req.Header.Get("X-Interaction-Type"); interactionType != "conversation-agent" {
		t.Errorf("X-Interaction-Type = %q, want conversation-agent", interactionType)
	}
	if taskID := req.Header.Get("X-Agent-Task-Id"); taskID != requestID {
		t.Errorf("X-Agent-Task-Id = %q, want request ID %q", taskID, requestID)
	}
}

func TestGenerateRequestID(t *testing.T) {
	id1 := GenerateRequestID()
	id2 := GenerateRequestID()

	if id1 == "" {
		t.Error("GenerateRequestID should return a non-empty string")
	}

	if !strings.HasPrefix(id1, "req_") {
		t.Errorf("GenerateRequestID should start with 'req_', got %s", id1)
	}

	if id1 == id2 {
		t.Error("GenerateRequestID should generate unique IDs")
	}
}
