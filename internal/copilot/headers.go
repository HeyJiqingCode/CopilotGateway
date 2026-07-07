package copilot

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// 默认版本头 — 取自真实的 VSCode + Copilot Chat 客户端实测值
const (
	defaultCopilotUserAgent    = "GitHubCopilotChat/0.55.0"
	defaultEditorVersion       = "vscode/1.127.0"
	defaultEditorPluginVersion = "copilot-chat/0.55.0"
	defaultAPIVersion          = "2026-06-01"
)

// 伪装的客户端版本头。进程内全局唯一,启动时由 ConfigureFromEnv 一次性覆盖,
// 之后只读,无并发写风险。
var (
	CopilotUserAgent    = defaultCopilotUserAgent
	EditorVersion       = defaultEditorVersion
	EditorPluginVersion = defaultEditorPluginVersion
	APIVersion          = defaultAPIVersion
)

// ConfigureFromEnv 用非空环境变量覆盖默认版本头。须在启动早期调用一次。
func ConfigureFromEnv() {
	if v := os.Getenv("COPILOT2API_CHAT_USER_AGENT"); v != "" {
		CopilotUserAgent = v
	}
	if v := os.Getenv("COPILOT2API_EDITOR_VERSION"); v != "" {
		EditorVersion = v
	}
	if v := os.Getenv("COPILOT2API_PLUGIN_VERSION"); v != "" {
		EditorPluginVersion = v
	}
	if v := os.Getenv("COPILOT2API_API_VERSION"); v != "" {
		APIVersion = v
	}
}

// AddHeaders 为所有上游请求补齐 VS Code Copilot 客户端头。
func AddHeaders(req *http.Request, token string) {
	requestID := req.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = GenerateRequestID()
		req.Header.Set("X-Request-Id", requestID)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", CopilotUserAgent)
	req.Header.Set("Editor-Version", EditorVersion)
	req.Header.Set("Editor-Plugin-Version", EditorPluginVersion)
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("Openai-Intent", "conversation-agent")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Github-Api-Version", APIVersion)
	req.Header.Set("X-Interaction-Type", "conversation-agent")
	req.Header.Set("X-Agent-Task-Id", requestID)
}

// ModelAccessHeaders 返回模型目录请求专用头,用于拿到 VS Code 同款模型能力元数据。
func ModelAccessHeaders(requestID string) map[string]string {
	if requestID == "" {
		requestID = GenerateRequestID()
	}
	return map[string]string{
		"Openai-Intent":        "model-access",
		"X-Interaction-Type":   "model-access",
		"X-Agent-Task-Id":      requestID,
		"X-Request-Id":         requestID,
		"X-Github-Api-Version": APIVersion,
	}
}

// GenerateRequestID 生成上游请求相关性 ID。
func GenerateRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		slog.Error("crypto/rand.Read failed", "error", err)
		return fmt.Sprintf("req_fallback_%d", time.Now().UnixNano())
	}
	return "req_" + hex.EncodeToString(b)
}
