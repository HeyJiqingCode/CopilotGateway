package upstream_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/anthropic"
	"github.com/HeyJiqingCode/CopilotGateway/internal/models"
	"github.com/HeyJiqingCode/CopilotGateway/internal/testutil"
	"github.com/HeyJiqingCode/CopilotGateway/internal/upstream"
	"github.com/HeyJiqingCode/CopilotGateway/proxy"
)

var bodyLimitEndpoints = []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"}

// Keep literal boundaries independent of the production constant. Pad valid
// JSON with whitespace: the limit applies to HTTP bytes, not tokens or the
// decoded content length. The mock upstream enforces its own 32 MiB limit;
// the gateway has a separate 64 MiB resource guard. Hash the received body to
// detect any truncation or re-encoding, including loss of whitespace.
func TestRequestBodyLimit(t *testing.T) {
	const upstreamLimit = 32 << 20
	const upstreamError = `{"error":{"message":"upstream-specific body limit","code":"upstream_413"}}`
	sizes := []struct {
		name string
		size int
	}{
		{"ordinary", 0},
		{"10MiB_plus_1", (10 << 20) + 1},
		{"32MiB", 32 << 20},
		{"32MiB_plus_1", (32 << 20) + 1},
		{"64MiB", 64 << 20},
		{"64MiB_plus_1", (64 << 20) + 1},
	}
	// Run sequentially to bound memory use for the large fixtures.
	for _, endpoint := range bodyLimitEndpoints {
		for _, stream := range []bool{false, true} {
			for _, knownLength := range []bool{true, false} {
				for _, size := range sizes {
					name := fmt.Sprintf("%s/stream=%t/known_length=%t/%s", strings.TrimPrefix(endpoint, "/v1/"), stream, knownLength, size.name)
					t.Run(name, func(t *testing.T) {
						body := bodyLimitPayload(endpoint, stream, size.size)
						wantHash := sha256.Sum256(body)
						var calls, posts atomic.Int32
						h := bodyLimitHandler(t, endpoint, func(w http.ResponseWriter, r *http.Request) {
							calls.Add(1)
							if r.URL.Path == "/models" {
								w.Header().Set("Content-Type", "application/json")
								io.WriteString(w, `{"data":[]}`)
								return
							}
							posts.Add(1)
							wantPath := strings.TrimPrefix(endpoint, "/v1")
							if endpoint == "/v1/messages" {
								wantPath = endpoint
							}
							if r.Method != http.MethodPost || r.URL.Path != wantPath {
								t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
								http.Error(w, "unexpected endpoint", http.StatusBadRequest)
								return
							}
							hash := sha256.New()
							n, err := io.Copy(hash, r.Body)
							if err != nil || n != int64(len(body)) || !bytes.Equal(hash.Sum(nil), wantHash[:]) {
								t.Errorf("upstream body changed: got %d bytes / %x, want %d / %x; read error: %v", n, hash.Sum(nil), len(body), wantHash, err)
								http.Error(w, "body mismatch", http.StatusBadRequest)
								return
							}
							w.Header().Set("Content-Type", "application/json")
							if n > upstreamLimit {
								w.WriteHeader(http.StatusRequestEntityTooLarge)
								io.WriteString(w, upstreamError)
								return
							}
							if stream {
								w.Header().Set("Content-Type", "text/event-stream")
							}
							io.WriteString(w, bodyLimitResponse(endpoint, stream))
						})
						req := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
						req.Header.Set("Content-Type", "application/json")
						if !knownLength {
							// Model the decoded body of a chunked HTTP request: its
							// actual byte count must be checked without Content-Length.
							req.ContentLength = -1
							req.TransferEncoding = []string{"chunked"}
						}
						w := httptest.NewRecorder()
						h.ServeHTTP(w, req)
						if len(body) > 64<<20 {
							if w.Code != http.StatusRequestEntityTooLarge {
								t.Fatalf("got HTTP %d, want 413: %s", w.Code, w.Body.String())
							}
							if calls.Load() != 0 {
								t.Fatalf("oversized request made %d upstream calls, including model lookups", calls.Load())
							}
							var result struct {
								Type  string `json:"type"`
								Error struct {
									Type    string `json:"type"`
									Message string `json:"message"`
								} `json:"error"`
							}
							if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
								t.Fatalf("invalid gateway error JSON: %v", err)
							}
							if result.Error.Type != "invalid_request_error" || result.Error.Message != "Request body too large: gateway limit is 64 MiB (67108864 bytes)" {
								t.Fatalf("gateway limit error is unclear or incorrect: %+v", result)
							}
							if endpoint == "/v1/messages" && result.Type != "error" {
								t.Fatal("Anthropic error envelope was lost")
							}
							if w.Header().Get("Content-Type") != "application/json" {
								t.Fatal("gateway 413 must be JSON, including for streaming requests")
							}
							return
						}
						if len(body) > upstreamLimit {
							if posts.Load() != 1 || w.Code != http.StatusRequestEntityTooLarge || w.Body.String() != upstreamError {
								t.Fatalf("upstream-bound request was intercepted or its error changed: POSTs %d, HTTP %d: %s", posts.Load(), w.Code, w.Body.String())
							}
							return
						}
						if w.Code != http.StatusOK || posts.Load() != 1 {
							t.Fatalf("allowed request: HTTP %d, upstream POSTs %d: %s", w.Code, posts.Load(), w.Body.String())
						}
						// Chat Completions keeps its existing [DONE] termination,
						// which may omit the final empty delimiter line.
						if strings.TrimSpace(w.Body.String()) != strings.TrimSpace(bodyLimitResponse(endpoint, stream)) {
							t.Fatalf("upstream response changed: %s", w.Body.String())
						}
					})
				}
			}
		}
	}
}

func TestRequestBodyReadErrorRemains400(t *testing.T) {
	for _, endpoint := range bodyLimitEndpoints {
		t.Run(strings.TrimPrefix(endpoint, "/v1/"), func(t *testing.T) {
			var calls atomic.Int32
			h := bodyLimitHandler(t, endpoint, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				http.Error(w, "unexpected upstream request", http.StatusInternalServerError)
			})
			r := httptest.NewRequest(http.MethodPost, endpoint, io.NopCloser(bodyReadError{}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest || calls.Load() != 0 || strings.Contains(w.Body.String(), "gateway limit") {
				t.Fatalf("body read failure was misclassified: HTTP %d, upstream calls %d: %s", w.Code, calls.Load(), w.Body.String())
			}
		})
	}
}

func TestUpstream413RemainsUnchanged(t *testing.T) {
	const upstreamError = `{"error":{"message":"upstream-specific body limit","code":"upstream_413"}}`
	for _, endpoint := range bodyLimitEndpoints {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", strings.TrimPrefix(endpoint, "/v1/"), stream), func(t *testing.T) {
				var posts atomic.Int32
				h := bodyLimitHandler(t, endpoint, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/models" {
						io.WriteString(w, `{"data":[]}`)
						return
					}
					posts.Add(1)
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						t.Errorf("read upstream request: %v", err)
					}
					w.WriteHeader(http.StatusRequestEntityTooLarge)
					io.WriteString(w, upstreamError)
				})
				r := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(bodyLimitPayload(endpoint, stream, 0)))
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if posts.Load() != 1 || w.Code != http.StatusRequestEntityTooLarge || w.Body.String() != upstreamError {
					t.Fatalf("upstream 413 was relabeled: POSTs %d, HTTP %d: %s", posts.Load(), w.Code, w.Body.String())
				}
			})
		}
	}
}

func bodyLimitPayload(endpoint string, stream bool, size int) []byte {
	template := `{"model":"test-model","input":"你好","stream":%t}`
	if endpoint == "/v1/chat/completions" {
		template = `{"model":"test-model","messages":[{"role":"user","content":"你好"}],"stream":%t}`
	} else if endpoint == "/v1/messages" {
		template = `{"model":"claude-opus-5.1","max_tokens":16,"messages":[{"role":"user","content":"你好"}],"stream":%t}`
	}
	base := []byte(fmt.Sprintf(template, stream))
	if size == 0 {
		return base
	}
	body := bytes.Repeat([]byte(" "), size)
	copy(body, base)
	return body
}

func bodyLimitResponse(endpoint string, stream bool) string {
	if !stream {
		return `{"ok":true}`
	}
	if endpoint == "/v1/responses" {
		return "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"output\":[]}}\n\n"
	}
	if endpoint == "/v1/messages" {
		return "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	}
	return "data: {\"choices\":[]}\n\ndata: [DONE]\n\n"
}

func bodyLimitHandler(t *testing.T, endpoint string, serve http.HandlerFunc) http.Handler {
	t.Helper()
	server := httptest.NewServer(serve)
	t.Cleanup(server.Close)
	transport := upstream.NewTransport()
	t.Cleanup(transport.CloseIdleConnections)
	provider := testutil.TokenProvider{BaseURL: server.URL}
	client := upstream.NewClient(provider, transport)
	cache := models.NewCache(client, time.Minute)
	if endpoint == "/v1/messages" {
		return anthropic.NewHandler(client, cache)
	}
	return proxy.NewHandler(client, cache)
}

type bodyReadError struct{}

func (bodyReadError) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
