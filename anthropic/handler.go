package anthropic

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

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
// The transport is used for upstream HTTP requests (pass nil to create a new one).
func NewHandler(authClient upstream.TokenProvider, transport *http.Transport, mc *models.Cache) *Handler {
	return &Handler{
		upstream: upstream.NewClient(authClient, transport),
		models:   mc,
	}
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

	// Parse Anthropic request
	var anthropicReq AnthropicMessagesRequest
	if err := json.Unmarshal(reqBody, &anthropicReq); err != nil {
		WriteAnthropicError(w, http.StatusBadRequest, AnthropicErrorTypeInvalidRequest, fmt.Sprintf("Invalid JSON: %v", err))
		return
	}

	// Validate request
	if err := h.validateRequest(anthropicReq); err != nil {
		WriteAnthropicError(w, http.StatusBadRequest, AnthropicErrorTypeInvalidRequest, fmt.Sprintf("Invalid request: %v", err))
		return
	}

	// Resolve model alias (e.g. claude-haiku-4-5-20251001 -> claude-haiku-4.5)
	resolvedModel := resolveModelAlias(anthropicReq.Model)

	modelChanged := resolvedModel != anthropicReq.Model
	if modelChanged {
		slog.Debug("resolved model alias", "from", anthropicReq.Model, "to", resolvedModel)
		anthropicReq.Model = resolvedModel
	}

	defer func() {
		slog.Info("anthropic request", "endpoint", "/v1/messages", "model", anthropicReq.Model, "stream", anthropicReq.Stream, "messages", len(anthropicReq.Messages), "route", "native", "duration_ms", time.Since(start).Milliseconds())
	}()

	// 能力信息仅用于判断模型是否支持 reasoning effort(驱动下方的剥离逻辑)。
	// 该端点只服务原生支持 /v1/messages 的 Claude 模型,一律直通上游。
	modelInfo, _ := h.getModelInfo(r.Context(), anthropicReq.Model)

	cacheControlInfo := inspectCacheControl(reqBody)
	topLevelInfo := inspectTopLevelFields(reqBody)
	reasoningInfo := inspectReasoningFields(reqBody)
	// 仅当模型确定不支持 reasoning effort 且客户端确实发了 output_config/thinking 时才剥离。
	// 两个守卫缺一不可:无能力检查会破坏 opus/sonnet;无存在检查会让每个请求白白重编码。
	stripReasoning := reasoningInfo.Any() && !modelSupportsReasoningEffort(modelInfo)
	slog.Debug("native /messages passthrough request", "model", anthropicReq.Model, "top_level_keys", topLevelInfo.Keys, "has_context_management", topLevelInfo.HasContextManagement, "cache_control_count", cacheControlInfo.Count, "cache_control_scope_count", cacheControlInfo.ScopeCount, "cache_control_paths", cacheControlInfo.Paths, "cache_control_scope_paths", cacheControlInfo.ScopePaths, "strip_reasoning", stripReasoning)
	// Re-encode the body only when we actually need to mutate it.
	if modelChanged || cacheControlInfo.ScopeCount > 0 || topLevelInfo.HasContextManagement || stripReasoning {
		newBody, err := normalizeNativeMessagesBody(reqBody, resolvedModel, modelChanged, stripReasoning)
		if err != nil {
			WriteAnthropicError(w, http.StatusBadRequest, AnthropicErrorTypeInvalidRequest, fmt.Sprintf("Invalid JSON: %v", err))
			return
		}
		if cacheControlInfo.ScopeCount > 0 {
			slog.Debug("normalized native /messages request", "removed_cache_control_scope_paths", cacheControlInfo.ScopePaths)
		}
		if topLevelInfo.HasContextManagement {
			slog.Debug("normalized native /messages request", "removed_top_level_field", "context_management")
		}
		if stripReasoning {
			slog.Debug("normalized native /messages request", "removed_reasoning_fields_output_config", reasoningInfo.HasOutputConfig, "removed_reasoning_fields_thinking", reasoningInfo.HasThinking)
		}
		reqBody = newBody
	}
	h.handleNativeMessagesPassthrough(w, r, reqBody, anthropicReq.Stream)
}

func (h *Handler) validateRequest(req AnthropicMessagesRequest) error {
	if req.Model == "" {
		return fmt.Errorf("model is required")
	}

	if req.MaxTokens <= 0 {
		return fmt.Errorf("max_tokens must be positive")
	}

	if len(req.Messages) == 0 && req.System == nil {
		return fmt.Errorf("either messages or system must be provided")
	}

	return nil
}

func (h *Handler) handleNativeMessagesPassthrough(w http.ResponseWriter, r *http.Request, body []byte, stream bool) {
	if stream {
		resp, _, err := h.upstream.Do(r.Context(), upstream.Request{Endpoint: "/v1/messages", Body: body, Stream: true, QueryString: r.URL.RawQuery})
		if err != nil {
			var upstreamErr *upstream.UpstreamError
			if errors.As(err, &upstreamErr) {
				h.writeRawUpstreamError(w, upstreamErr)
				return
			}
			upstream.LogRequestError("native /messages streaming request failed", err)
			sse.BeginSSE(w)
			w.WriteHeader(http.StatusBadGateway)
			h.writeSSEError(w, "Upstream streaming request failed")
			return
		}
		defer resp.Body.Close()

		flusher, ok := w.(http.Flusher)
		if !ok {
			WriteAnthropicError(w, http.StatusInternalServerError, AnthropicErrorTypeAPI, "Streaming unsupported")
			return
		}

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
				break
			}
			if err != nil {
				slog.Error("error reading native /messages stream", "error", err)
				return
			}
		}

		return
	}

	_, respData, err := h.upstream.Do(r.Context(), upstream.Request{Endpoint: "/v1/messages", Body: body, QueryString: r.URL.RawQuery})
	if err != nil {
		var upstreamErr *upstream.UpstreamError
		if errors.As(err, &upstreamErr) {
			h.writeRawUpstreamError(w, upstreamErr)
			return
		}
		upstream.LogRequestError("native /messages request failed", err)
		WriteAnthropicError(w, http.StatusInternalServerError, AnthropicErrorTypeAPI, "Upstream request failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(respData)
}

// --- SSE helpers ---

func (h *Handler) writeSSEEvent(w io.Writer, event AnthropicStreamEvent) error {
	eventData, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, eventData)
	return err
}

func (h *Handler) writeSSEError(w io.Writer, message string) {
	h.writeSSETypedError(w, AnthropicErrorTypeAPI, message)
}

func (h *Handler) writeSSETypedError(w io.Writer, errorType, message string) {
	errorEvent := CreateErrorEvent(message)
	if errorEvent.Error != nil {
		errorEvent.Error.Type = errorType
	}
	h.writeSSEEvent(w, errorEvent)
}

// CreateErrorEvent creates an error event for stream failures
func CreateErrorEvent(message string) AnthropicStreamEvent {
	return AnthropicStreamEvent{
		Type: "error",
		Error: &AnthropicError{
			Type:    AnthropicErrorTypeAPI,
			Message: message,
		},
	}
}

func (h *Handler) writeRawUpstreamError(w http.ResponseWriter, upstreamErr *upstream.UpstreamError) {
	upstreamErr.WriteRawError(w)
}

// replaceModelInBody replaces the top-level "model" key in a JSON body via a
// targeted round-trip, preserving all other fields unchanged.
func replaceModelInBody(body []byte, newModel string) ([]byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	modelJSON, _ := json.Marshal(newModel)
	raw["model"] = modelJSON
	return json.Marshal(raw)
}

func normalizeNativeMessagesBody(body []byte, newModel string, replaceModel, stripReasoning bool) ([]byte, error) {
	var raw interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	obj, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("request body must be a JSON object")
	}

	if replaceModel {
		obj["model"] = newModel
	}

	delete(obj, "context_management")
	if stripReasoning {
		// 模型不支持 reasoning effort;移除会被上游拒绝的字段
		// (如 haiku 上的 output_config.effort)。output_config 当前仅承载 effort,整删够用。
		delete(obj, "output_config")
		delete(obj, "thinking")
	}
	stripCacheControlScope(obj)
	return json.Marshal(obj)
}

type topLevelFieldInspection struct {
	Keys                 []string
	HasContextManagement bool
}

func inspectTopLevelFields(body []byte) topLevelFieldInspection {
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return topLevelFieldInspection{}
	}

	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	_, hasContextManagement := raw["context_management"]
	return topLevelFieldInspection{Keys: keys, HasContextManagement: hasContextManagement}
}

// reasoningFieldInspection 记录请求体顶层是否含 reasoning 相关字段。
type reasoningFieldInspection struct {
	HasOutputConfig bool
	HasThinking     bool
}

func (i reasoningFieldInspection) Any() bool { return i.HasOutputConfig || i.HasThinking }

// inspectReasoningFields 检测请求体顶层是否带 output_config / thinking
// (Anthropic 的 reasoning effort 载体)。用轻量 RawMessage 解码避免全量解析。
func inspectReasoningFields(body []byte) reasoningFieldInspection {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return reasoningFieldInspection{}
	}
	_, hasOutputConfig := raw["output_config"]
	_, hasThinking := raw["thinking"]
	return reasoningFieldInspection{HasOutputConfig: hasOutputConfig, HasThinking: hasThinking}
}

type cacheControlInspection struct {
	Count      int
	ScopeCount int
	Paths      []string
	ScopePaths []string
}

func inspectCacheControl(body []byte) cacheControlInspection {
	var raw interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return cacheControlInspection{}
	}

	result := cacheControlInspection{}
	inspectCacheControlValue(raw, "$", &result)
	return result
}

func inspectCacheControlValue(v interface{}, path string, result *cacheControlInspection) {
	switch node := v.(type) {
	case map[string]interface{}:
		if cacheControl, ok := node["cache_control"].(map[string]interface{}); ok {
			result.Count++
			result.Paths = append(result.Paths, path+".cache_control")
			if _, hasScope := cacheControl["scope"]; hasScope {
				result.ScopeCount++
				result.ScopePaths = append(result.ScopePaths, path+".cache_control.scope")
			}
		}
		for key, child := range node {
			inspectCacheControlValue(child, path+"."+key, result)
		}
	case []interface{}:
		for i, child := range node {
			inspectCacheControlValue(child, fmt.Sprintf("%s[%d]", path, i), result)
		}
	}
}

func stripCacheControlScope(v interface{}) {
	switch node := v.(type) {
	case map[string]interface{}:
		if cacheControl, ok := node["cache_control"].(map[string]interface{}); ok {
			delete(cacheControl, "scope")
		}
		for _, child := range node {
			stripCacheControlScope(child)
		}
	case []interface{}:
		for _, child := range node {
			stripCacheControlScope(child)
		}
	}
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
