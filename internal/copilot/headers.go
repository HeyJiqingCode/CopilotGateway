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
	defaultCopilotUserAgent    = "GitHubCopilotChat/0.52.0"
	defaultEditorVersion       = "vscode/1.124.2"
	defaultEditorPluginVersion = "copilot-chat/0.52.0"
)

// 伪装的客户端版本头。进程内全局唯一,启动时由 ConfigureFromEnv 一次性覆盖,
// 之后只读,无并发写风险。
var (
	CopilotUserAgent    = defaultCopilotUserAgent
	EditorVersion       = defaultEditorVersion
	EditorPluginVersion = defaultEditorPluginVersion
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
}

// AddHeaders adds required Copilot headers to the request
func AddHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", CopilotUserAgent)
	req.Header.Set("Editor-Version", EditorVersion)
	req.Header.Set("Editor-Plugin-Version", EditorPluginVersion)
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("Openai-Intent", "conversation-agent")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Github-Api-Version", "2025-04-01")

	// Generate request ID if not present
	if req.Header.Get("X-Request-Id") == "" {
		req.Header.Set("X-Request-Id", GenerateRequestID())
	}
}

// GenerateRequestID generates a unique request ID using crypto/rand
func GenerateRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		slog.Error("crypto/rand.Read failed", "error", err)
		return fmt.Sprintf("req_fallback_%d", time.Now().UnixNano())
	}
	return "req_" + hex.EncodeToString(b)
}
