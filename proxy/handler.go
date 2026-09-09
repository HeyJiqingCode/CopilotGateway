package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/internal/models"
	"github.com/HeyJiqingCode/CopilotGateway/internal/upstream"
)

type Handler struct {
	upstream    *upstream.Client
	modelsCache *models.Cache

	// DisableResponsesIDCompat restores unmodified upstream SSE forwarding.
	// Configure once before serving requests. No history or cross-request ID
	// mapping is stored by the compatibility layer.
	DisableResponsesIDCompat bool
}

// NewHandler creates a new proxy handler.
// The transport is used for upstream HTTP requests (pass nil to create a new one).
func NewHandler(tokenProvider upstream.TokenProvider, transport *http.Transport, mc *models.Cache) *Handler {
	return &Handler{
		upstream:    upstream.NewClient(tokenProvider, transport),
		modelsCache: mc,
	}
}

// ServeHTTP handles all proxy requests
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Extract endpoint from path
	endpoint := strings.TrimPrefix(r.URL.Path, "/v1")
	defer func() {
		slog.Info("proxy request", "method", r.Method, "endpoint", endpoint, "duration_ms", time.Since(start).Milliseconds())
	}()

	switch endpoint {
	case "/models":
		h.handleModels(w, r)
	case "/chat/completions":
		h.handlePassthrough(w, r, endpoint)
	case "/responses":
		h.handlePassthrough(w, r, endpoint)
	default:
		WriteOpenAIError(w, http.StatusNotFound, OpenAIErrorTypeInvalidRequest, "Endpoint not found")
	}
}

// handleModels handles /v1/models with caching
func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		WriteOpenAIError(w, http.StatusMethodNotAllowed, OpenAIErrorTypeInvalidRequest, "Method not allowed")
		return
	}

	respData, err := h.modelsCache.GetRaw(r.Context())
	if err != nil {
		var upstreamErr *upstream.UpstreamError
		if errors.As(err, &upstreamErr) {
			upstreamErr.WriteRawError(w)
			return
		}
		upstream.LogRequestError("failed to fetch models", err)
		WriteOpenAIError(w, http.StatusInternalServerError, OpenAIErrorTypeServerError, "Internal server error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(respData)
}

// handlePassthrough handles direct passthrough requests
func (h *Handler) handlePassthrough(w http.ResponseWriter, r *http.Request, endpoint string) {
	// Check body size before processing — reject oversized payloads with 413
	var bodyBytes []byte
	if r.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(io.LimitReader(r.Body, upstream.MaxRequestBody+1))
		if err != nil {
			WriteOpenAIError(w, http.StatusBadRequest, OpenAIErrorTypeInvalidRequest, "Failed to read request body")
			return
		}
		if len(bodyBytes) > upstream.MaxRequestBody {
			WriteOpenAIError(w, http.StatusRequestEntityTooLarge, OpenAIErrorTypeInvalidRequest, upstream.RequestBodyTooLargeMessage())
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	h.handlePassthroughBody(w, r, endpoint, bodyBytes)
}

// handlePassthroughBody processes the passthrough request after the body has been read and validated.
// It takes pre-read body bytes to avoid redundant body reading.
func (h *Handler) handlePassthroughBody(w http.ResponseWriter, r *http.Request, endpoint string, bodyBytes []byte) {
	// 该端点固定直通上游:客户端已按模型选好正确端点(Codex→/responses、CC→/v1/messages),
	// 无需再做跨格式智能路由。
	if isStreamingRequest(bodyBytes) {
		if err := h.HandleStreamingRequest(w, r, endpoint); err != nil {
			upstream.LogRequestError("streaming request failed", err, "endpoint", endpoint)
			var hse *headersSentError
			if !errors.As(err, &hse) {
				WriteOpenAIError(w, http.StatusBadGateway, OpenAIErrorTypeServerError, "upstream request failed")
			}
		}
		return
	}

	respData, err := h.doNonStreamingRequest(r, endpoint)
	if err != nil {
		var upstreamErr *upstream.UpstreamError
		if errors.As(err, &upstreamErr) {
			upstreamErr.WriteRawError(w)
			return
		}
		upstream.LogRequestError("passthrough request failed", err, "endpoint", endpoint)
		WriteOpenAIError(w, http.StatusInternalServerError, OpenAIErrorTypeServerError, "Internal server error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(respData)
}

// doNonStreamingRequest makes a non-streaming request to the Copilot API via the shared upstream client.
func (h *Handler) doNonStreamingRequest(r *http.Request, endpoint string) ([]byte, error) {
	var body interface{}
	if r.Body != nil {
		body = r.Body
	}

	_, respData, err := h.upstream.Do(r.Context(), upstream.Request{
		Method:       r.Method,
		Endpoint:     endpoint,
		Body:         body,
		QueryString:  r.URL.RawQuery,
		ExtraHeaders: collectForwardHeaders(r),
	})
	return respData, err
}

// collectForwardHeaders returns headers from the original request that should be
// forwarded to the upstream API.
func collectForwardHeaders(r *http.Request) map[string]string {
	headers := make(map[string]string)
	for _, name := range []string{"Content-Type", "Accept", "Cache-Control"} {
		if v := r.Header.Get(name); v != "" {
			headers[name] = v
		}
	}
	return headers
}

// --- OpenAI error response helpers ---

// OpenAIErrorResponse represents an error response in OpenAI API format
type OpenAIErrorResponse struct {
	Error OpenAIError `json:"error"`
}

// OpenAIError represents the error object in OpenAI API responses
type OpenAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// Error type constants for OpenAI API
const (
	OpenAIErrorTypeServerError    = "server_error"
	OpenAIErrorTypeInvalidRequest = "invalid_request_error"
)

// WriteOpenAIError writes an error response in OpenAI API format
func WriteOpenAIError(w http.ResponseWriter, statusCode int, errorType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	errorResp := OpenAIErrorResponse{
		Error: OpenAIError{
			Message: message,
			Type:    errorType,
		},
	}

	json.NewEncoder(w).Encode(errorResp)
}
