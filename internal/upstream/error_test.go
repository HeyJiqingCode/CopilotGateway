package upstream

import "testing"

func TestUpstreamError(t *testing.T) {
	err := &UpstreamError{
		StatusCode: 429,
		Body:       []byte(`{"error": "rate limited"}`),
	}

	expected := "upstream error: status 429, body: {\"error\": \"rate limited\"}"
	if err.Error() != expected {
		t.Errorf("Expected %q, got %q", expected, err.Error())
	}
}
