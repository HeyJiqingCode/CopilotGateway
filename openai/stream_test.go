package openai

import (
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsStreamingRequest(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected bool
	}{
		{
			name:     "streaming request with stream true",
			body:     `{"model": "gpt-4", "messages": [], "stream": true}`,
			expected: true,
		},
		{
			name:     "streaming request with stream true and spaces",
			body:     `{"model": "gpt-4", "messages": [], "stream": true }`,
			expected: true,
		},
		{
			name:     "non-streaming request with stream false",
			body:     `{"model": "gpt-4", "messages": [], "stream": false}`,
			expected: false,
		},
		{
			name:     "non-streaming request without stream parameter",
			body:     `{"model": "gpt-4", "messages": []}`,
			expected: false,
		},
		{
			name:     "empty body",
			body:     "",
			expected: false,
		},
		{
			name:     "malformed JSON",
			body:     `{"model": "gpt-4", "messages": [], "stream":`,
			expected: false,
		},
		{
			name:     "stream true in message content should not match",
			body:     `{"model": "gpt-4", "messages": [{"role": "user", "content": "set \"stream\": true in the body"}], "stream": false}`,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isStreamingRequest([]byte(tt.body))
			if result != tt.expected {
				t.Errorf("isStreamingRequest() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestIsResponsesTerminationEvent(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{name: "completed exact", line: "event: response.completed", want: true},
		{name: "completed with whitespace", line: "  event: response.completed\r\n", want: true},
		{name: "error exact", line: "event: error", want: true},
		{name: "non termination", line: "event: response.output_text.delta", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isResponsesTerminationEvent(tt.line); got != tt.want {
				t.Fatalf("isResponsesTerminationEvent(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// streamResponseHelper calls streamResponse on a zero-value Handler to keep tests simple.
func streamResponseHelper(t *testing.T, ssePayload string, endpoint string) string {
	t.Helper()
	h := &Handler{}
	rec := httptest.NewRecorder()
	body := io.NopCloser(strings.NewReader(ssePayload))
	if err := h.streamResponse(rec, body, endpoint); err != nil {
		t.Fatalf("streamResponse returned error: %v", err)
	}
	return rec.Body.String()
}

func TestStreamResponse_ChatCompletions_DataDone(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n"
	got := streamResponseHelper(t, sse, "/chat/completions")
	if !strings.Contains(got, "data: [DONE]") {
		t.Fatal("expected data: [DONE] in output")
	}
}

func TestHeadersSentError(t *testing.T) {
	inner := errors.New("read: connection reset")
	hse := &headersSentError{err: inner}

	if hse.Error() != inner.Error() {
		t.Fatalf("Error() = %q, want %q", hse.Error(), inner.Error())
	}
	if !errors.Is(hse, inner) {
		t.Fatal("Unwrap should expose the inner error")
	}

	// Callers recognize it through further wrapping.
	var target *headersSentError
	if !errors.As(fmt.Errorf("stream: %w", hse), &target) || target != hse {
		t.Fatal("errors.As should find the wrapped *headersSentError")
	}
}
