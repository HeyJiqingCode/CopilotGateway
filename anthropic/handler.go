package anthropic

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/auth"
	"github.com/HeyJiqingCode/CopilotGateway/internal/models"
	"github.com/HeyJiqingCode/CopilotGateway/internal/sse"
	"github.com/HeyJiqingCode/CopilotGateway/internal/upstream"
)

// Handler handles Anthropic Messages API requests
type Handler struct {
	upstream *upstream.Client
	models   *models.Cache
}

// NewHandler creates a new Anthropic handler.
func NewHandler(uc *upstream.Client, mc *models.Cache) *Handler {
	return &Handler{upstream: uc, models: mc}
}

// ServeHTTP handles /v1/messages requests
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	if r.Method != "POST" {
		WriteAnthropicError(w, http.StatusMethodNotAllowed, AnthropicErrorTypeInvalidRequest, "Method not allowed")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, upstream.MaxRequestBody) // Shared gateway HTTP body byte limit.
	reqBody, err := io.ReadAll(r.Body)
	if err != nil {
		var limitErr *http.MaxBytesError
		if errors.As(err, &limitErr) {
			WriteAnthropicError(w, http.StatusRequestEntityTooLarge, AnthropicErrorTypeInvalidRequest, upstream.RequestBodyTooLargeMessage())
			return
		}
		WriteAnthropicError(w, http.StatusBadRequest, AnthropicErrorTypeInvalidRequest, fmt.Sprintf("Invalid request body: %v", err))
		return
	}

	req, err := parseMessagesRequest(reqBody)
	if err != nil {
		WriteAnthropicError(w, http.StatusBadRequest, AnthropicErrorTypeInvalidRequest, fmt.Sprintf("Invalid JSON: %v", err))
		return
	}
	if err := req.validate(); err != nil {
		WriteAnthropicError(w, http.StatusBadRequest, AnthropicErrorTypeInvalidRequest, fmt.Sprintf("Invalid request: %v", err))
		return
	}

	// Resolve model alias (e.g. claude-haiku-4-5-20251001 -> claude-haiku-4.5)
	model := resolveModelAlias(req.model)
	if model != req.model {
		slog.Debug("resolved model alias", "from", req.model, "to", model)
	}

	defer func() {
		slog.Info("anthropic request", "endpoint", "/v1/messages", "model", model, "stream", req.stream, "messages", req.messageCount, "duration_ms", time.Since(start).Milliseconds())
	}()

	// 能力信息仅用于判断模型是否支持 reasoning effort(驱动下方的剥离逻辑)。
	// 该端点只服务原生支持 /v1/messages 的 Claude 模型,一律直通上游。
	modelInfo := h.getModelInfo(r.Context(), model)

	// 仅当模型确定不支持 reasoning effort 且客户端确实发了 output_config/thinking 时才剥离。
	dropReasoning := (req.has("output_config") || req.has("thinking")) && !models.SupportsReasoningEffort(modelInfo)
	// 客户端的 anthropic-beta 原样转发;上游仅在带 context-management beta 时接受 context_management。
	betas := strings.Join(r.Header.Values("anthropic-beta"), ",")
	dropContextManagement := req.has("context_management") && !hasContextManagementBeta(betas)

	body, removedScopes, err := req.rewrite(rewriteOptions{model: model, dropContextManagement: dropContextManagement, dropReasoning: dropReasoning})
	if err != nil {
		WriteAnthropicError(w, http.StatusBadRequest, AnthropicErrorTypeInvalidRequest, fmt.Sprintf("Invalid JSON: %v", err))
		return
	}
	if slog.Default().Enabled(r.Context(), slog.LevelDebug) { // keys() allocates and sorts
		slog.Debug("native /messages passthrough request", "model", model, "top_level_keys", req.keys(), "anthropic_beta", betas,
			"dropped_context_management", dropContextManagement, "dropped_reasoning", dropReasoning, "removed_cache_control_scopes", removedScopes)
	}
	h.handleNativeMessagesPassthrough(w, r, body, req.stream, betas)
}

// hasContextManagementBeta reports whether the comma-separated anthropic-beta
// value enables context management (e.g. context-management-2025-06-27).
func hasContextManagementBeta(betas string) bool {
	for _, beta := range strings.Split(betas, ",") {
		if strings.HasPrefix(strings.TrimSpace(beta), "context-management-") {
			return true
		}
	}
	return false
}

func (h *Handler) handleNativeMessagesPassthrough(w http.ResponseWriter, r *http.Request, body []byte, stream bool, betas string) {
	req := upstream.Request{Endpoint: "/v1/messages", Body: body, Stream: stream, QueryString: r.URL.RawQuery}
	if betas != "" {
		req.ExtraHeaders = map[string]string{"anthropic-beta": betas}
	}
	if !stream {
		_, respData, err := h.upstream.Do(r.Context(), req)
		if err != nil {
			writeUpstreamFailure(w, "native /messages request failed", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respData)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteAnthropicError(w, http.StatusInternalServerError, AnthropicErrorTypeAPI, "Streaming unsupported")
		return
	}
	resp, _, err := h.upstream.Do(r.Context(), req)
	if err != nil {
		writeUpstreamFailure(w, "native /messages streaming request failed", err)
		return
	}
	defer resp.Body.Close()

	sse.BeginSSE(w)
	reader := bufio.NewReaderSize(resp.Body, 32*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, writeErr := w.Write(line); writeErr != nil {
				slog.Error("failed to write native /messages stream", "error", writeErr)
				return
			}
			// Flush at SSE event boundaries (blank lines) instead of every line
			// to reduce syscall overhead while maintaining correct SSE delivery.
			if isBlankSSELine(line) {
				flusher.Flush()
			}
		}

		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			slog.Error("error reading native /messages stream", "error", err)
			return
		}
	}
}

// writeUpstreamFailure forwards an upstream error response as-is, answers a
// lost GitHub authorization with its 403, and reports anything else (the
// upstream could not be reached) as a retryable 502.
func writeUpstreamFailure(w http.ResponseWriter, msg string, err error) {
	var upstreamErr *upstream.UpstreamError
	if errors.As(err, &upstreamErr) {
		upstreamErr.WriteRawError(w)
		return
	}
	if writeReauthRequired(w, err) {
		return
	}
	upstream.LogRequestError(msg, err)
	WriteAnthropicError(w, http.StatusBadGateway, AnthropicErrorTypeAPI, "Upstream request failed")
}

// isBlankSSELine reports whether line consists solely of newline characters
// (\r and/or \n). SSE uses blank lines to delimit events; flushing only at
// these boundaries reduces syscall overhead while keeping delivery prompt.
func isBlankSSELine(line []byte) bool {
	for _, b := range line {
		if b != '\r' && b != '\n' {
			return false
		}
	}
	return len(line) > 0
}

// writeReauthRequired answers a lost GitHub authorization with a 403
// permission_error that clients do not retry, carrying the sign-in instructions.
func writeReauthRequired(w http.ResponseWriter, err error) bool {
	var reauth *auth.ReauthRequiredError
	if !errors.As(err, &reauth) {
		return false
	}
	WriteAnthropicError(w, http.StatusForbidden, AnthropicErrorTypePermission, reauth.Error())
	return true
}

// WriteAnthropicError writes an error response in Anthropic API format
func WriteAnthropicError(w http.ResponseWriter, statusCode int, errorType string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	errorResp := AnthropicErrorResponse{
		Type: "error",
		Error: AnthropicError{
			Type:    errorType,
			Message: message,
		},
	}

	json.NewEncoder(w).Encode(errorResp)
}
