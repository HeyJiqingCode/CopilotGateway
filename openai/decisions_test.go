package openai

import (
	"bytes"
	"net/http"
	"testing"
)

func TestDecisions_PassThrough(t *testing.T) {
	var got upstreamCall
	result := `{"model":"gpt-6-luna-decisions","answers":[{"type":"predicate","name":"damaged","probability":0.95}]}`
	h := imageUpstream(t, &got, http.StatusOK, "application/json", result)

	body := []byte(`{"model":"gpt-6-luna-decisions", "input":"The package arrived with a broken screen.",` +
		`"questions":[{"type":"predicate","name":"damaged","instructions":"Does the customer report a damaged item?"}]}`)
	rec := serve(h, "/v1/decisions", "application/json", body)

	if got.path != "/v1/decisions" || !bytes.Equal(got.body, body) {
		t.Fatalf("upstream got %s %s, want /v1/decisions with the original body", got.path, got.body)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != result {
		t.Fatalf("client got %d %s, want 200 with the upstream body", rec.Code, rec.Body.String())
	}
}
