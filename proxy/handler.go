package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/auth"
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

	// AutoReviewModel replaces Codex's codex-auto-review pseudo-model, which
	// Copilot rejects; empty forwards it unchanged.
	AutoReviewModel string
}

// NewHandler creates a new proxy handler.
func NewHandler(uc *upstream.Client, mc *models.Cache) *Handler {
	return &Handler{upstream: uc, modelsCache: mc}
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
	case "/chat/completions", "/responses":
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
		writeUpstreamFailure(w, "failed to fetch models", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(respData)
}

// handlePassthrough forwards the request body to the same upstream endpoint;
// the client already picked the right one for its model.
func (h *Handler) handlePassthrough(w http.ResponseWriter, r *http.Request, endpoint string) {
	// Check body size before processing — reject oversized payloads with 413
	body, err := io.ReadAll(io.LimitReader(r.Body, upstream.MaxRequestBody+1))
	if err != nil {
		WriteOpenAIError(w, http.StatusBadRequest, OpenAIErrorTypeInvalidRequest, "Failed to read request body")
		return
	}
	if len(body) > upstream.MaxRequestBody {
		WriteOpenAIError(w, http.StatusRequestEntityTooLarge, OpenAIErrorTypeInvalidRequest, upstream.RequestBodyTooLargeMessage())
		return
	}
	if routed, ok := rewriteAutoReviewModel(body, h.AutoReviewModel); ok {
		slog.Info("auto-review model applied", "endpoint", endpoint, "to", h.AutoReviewModel)
		body = routed
	}

	req := upstream.Request{
		Method:       r.Method,
		Endpoint:     endpoint,
		Body:         body,
		QueryString:  r.URL.RawQuery,
		Stream:       isStreamingRequest(body),
		ExtraHeaders: collectForwardHeaders(r),
	}
	if req.Stream {
		err := h.handleStreamingRequest(w, r, req)
		var hse *headersSentError
		if errors.As(err, &hse) {
			// The status line is out; all that is left is to record why the stream ended.
			upstream.LogRequestError("streaming response interrupted", err, "endpoint", endpoint)
		} else if err != nil {
			writeUpstreamFailure(w, "streaming request failed", err, "endpoint", endpoint)
		}
		return
	}

	_, respData, err := h.upstream.Do(r.Context(), req)
	if err != nil {
		writeUpstreamFailure(w, "passthrough request failed", err, "endpoint", endpoint)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(respData)
}

// writeUpstreamFailure forwards an upstream error response as-is, answers a
// lost GitHub authorization with its 403, and reports anything else (the
// upstream could not be reached) as a retryable 502.
func writeUpstreamFailure(w http.ResponseWriter, msg string, err error, logArgs ...any) {
	var upstreamErr *upstream.UpstreamError
	if errors.As(err, &upstreamErr) {
		upstreamErr.WriteRawError(w)
		return
	}
	if writeReauthRequired(w, err) {
		return
	}
	upstream.LogRequestError(msg, err, logArgs...)
	WriteOpenAIError(w, http.StatusBadGateway, OpenAIErrorTypeServerError, "Upstream request failed")
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
	OpenAIErrorTypePermission     = "permission_error"
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

// writeReauthRequired answers a lost GitHub authorization with a 403 that
// clients do not retry, carrying the sign-in instructions in the message.
func writeReauthRequired(w http.ResponseWriter, err error) bool {
	var reauth *auth.ReauthRequiredError
	if !errors.As(err, &reauth) {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	json.NewEncoder(w).Encode(OpenAIErrorResponse{Error: OpenAIError{
		Message: reauth.Error(),
		Type:    OpenAIErrorTypePermission,
		Code:    "copilot_auth_required",
	}})
	return true
}
