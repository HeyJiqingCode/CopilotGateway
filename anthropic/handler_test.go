package anthropic

import (
	"testing"
)

func TestCreateErrorEvent(t *testing.T) {
	event := CreateErrorEvent("Something went wrong")

	if event.Type != "error" {
		t.Errorf("Expected event type 'error', got %q", event.Type)
	}

	if event.Error == nil || event.Error.Type != AnthropicErrorTypeAPI {
		t.Errorf("Expected error with type %q", AnthropicErrorTypeAPI)
	}

	if event.Error.Message != "Something went wrong" {
		t.Errorf("Expected error message 'Something went wrong', got %q", event.Error.Message)
	}
}
