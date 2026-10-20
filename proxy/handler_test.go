package proxy

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/internal/models"
	"github.com/HeyJiqingCode/CopilotGateway/internal/testutil"
	"github.com/HeyJiqingCode/CopilotGateway/internal/upstream"
)

func TestCollectForwardHeaders(t *testing.T) {
	srcReq, err := http.NewRequest("POST", "http://example.com", bytes.NewReader([]byte("test")))
	if err != nil {
		t.Fatalf("Failed to create source request: %v", err)
	}

	srcReq.Header.Set("Content-Type", "application/json")
	srcReq.Header.Set("Accept", "text/event-stream")
	srcReq.Header.Set("Cache-Control", "no-cache")
	srcReq.Header.Set("Accept-Encoding", "gzip, deflate")

	headers := collectForwardHeaders(srcReq)

	expectedHeaders := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	for header, expectedValue := range expectedHeaders {
		actualValue, ok := headers[header]
		if !ok {
			t.Errorf("Header %s: expected %q, but not present", header, expectedValue)
		} else if actualValue != expectedValue {
			t.Errorf("Header %s: expected %q, got %q", header, expectedValue, actualValue)
		}
	}

	// Accept-Encoding should NOT be forwarded
	if _, ok := headers["Accept-Encoding"]; ok {
		t.Error("Accept-Encoding should not be forwarded")
	}
}

func TestHandler_HandleModels(t *testing.T) {
	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"object": "list",
			"data": []map[string]string{
				{"id": "gpt-4", "object": "model"},
				{"id": "gpt-3.5-turbo", "object": "model"},
			},
		})
	}))
	defer fakeUpstream.Close()

	tp := testutil.TokenProvider{BaseURL: fakeUpstream.URL}
	uc := upstream.NewClient(tp, nil)
	handler := &Handler{
		upstream:    uc,
		modelsCache: models.NewCache(uc, 5*time.Minute),
	}

	req := httptest.NewRequest("GET", "/v1/models", nil)
	rec := httptest.NewRecorder()
	handler.handleModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	data, ok := result["data"].([]interface{})
	if !ok {
		t.Fatal("expected 'data' to be a list")
	}
	if len(data) != 2 {
		t.Fatalf("expected 2 models, got %d", len(data))
	}
}

func TestHandler_HandlePassthrough(t *testing.T) {
	want := map[string]interface{}{
		"id":      "chatcmpl-abc",
		"object":  "chat.completion",
		"choices": []interface{}{},
	}

	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(want)
	}))
	defer fakeUpstream.Close()

	tp := testutil.TokenProvider{BaseURL: fakeUpstream.URL}
	uc := upstream.NewClient(tp, nil)
	handler := &Handler{
		upstream:    uc,
		modelsCache: models.NewCache(uc, 5*time.Minute),
	}

	body := `{"model":"gpt-4","messages":[],"stream":false}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.handlePassthrough(rec, req, "/chat/completions")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var got map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	if got["id"] != "chatcmpl-abc" {
		t.Fatalf("expected id 'chatcmpl-abc', got %v", got["id"])
	}
	if got["object"] != "chat.completion" {
		t.Fatalf("expected object 'chat.completion', got %v", got["object"])
	}
}

// TestServeHTTP_UnreachableUpstreamReturns502 checks that a transport failure
// before any response is written is a JSON bad gateway on every path.
func TestServeHTTP_UnreachableUpstreamReturns502(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	uc := upstream.NewClient(testutil.TokenProvider{BaseURL: "http://" + addr}, nil)
	handler := NewHandler(uc, models.NewCache(uc, time.Minute))

	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/v1/chat/completions", `{"model":"gpt-4","messages":[],"stream":false}`},
		{"POST", "/v1/chat/completions", `{"model":"gpt-4","messages":[],"stream":true}`},
		{"GET", "/v1/models", ""},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		var errResp OpenAIErrorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &errResp)
		if rec.Code != http.StatusBadGateway || errResp.Error.Type != OpenAIErrorTypeServerError {
			t.Errorf("%s %s: status = %d, error = %+v; want 502 server_error", tc.method, tc.path, rec.Code, errResp.Error)
		}
	}
}
