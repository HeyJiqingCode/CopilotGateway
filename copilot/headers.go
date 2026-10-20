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

// 默认版本头 — 与 README 中的默认配置保持一致
const (
	defaultCopilotUserAgent    = "GitHubCopilotChat/0.70.0"
	defaultEditorVersion       = "vscode/1.142.0"
	defaultEditorPluginVersion = "copilot-chat/0.70.0"
	defaultAPIVersion          = "2026-06-01"
)

// 伪装的客户端版本头。进程内全局唯一,启动时由 ConfigureFromEnv 一次性覆盖,
// 之后只读,无并发写风险。
var (
	UserAgent           = defaultCopilotUserAgent
	EditorVersion       = defaultEditorVersion
	EditorPluginVersion = defaultEditorPluginVersion
	APIVersion          = defaultAPIVersion
)

// ConfigureFromEnv 用非空环境变量覆盖默认版本头。须在启动早期调用一次。
func ConfigureFromEnv() {
	if v := os.Getenv("COPILOTGW_CHAT_USER_AGENT"); v != "" {
		UserAgent = v
	}
	if v := os.Getenv("COPILOTGW_EDITOR_VERSION"); v != "" {
		EditorVersion = v
	}
	if v := os.Getenv("COPILOTGW_PLUGIN_VERSION"); v != "" {
		EditorPluginVersion = v
	}
	if v := os.Getenv("COPILOTGW_API_VERSION"); v != "" {
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
	req.Header.Set("User-Agent", UserAgent)
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
// 它们在 AddHeaders 之后设置,覆盖对话用的 intent 与请求 ID。
func ModelAccessHeaders() map[string]string {
	requestID := GenerateRequestID()
	return map[string]string{
		"Openai-Intent":      "model-access",
		"X-Interaction-Type": "model-access",
		"X-Agent-Task-Id":    requestID,
		"X-Request-Id":       requestID,
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
