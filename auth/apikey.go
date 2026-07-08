package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// APIKeyAuth 是一个鉴权中间件:校验请求是否携带有效密钥。
// keys 为允许的密钥集合(启动时从配置加载,调用方保证非空)。
// /health 与 web UI 路径直接放行(见 isAuthExempt)。
func APIKeyAuth(keys []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAuthExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		presented := extractAPIKey(r)
		if presented != "" && matchesAnyKey(presented, keys) {
			next.ServeHTTP(w, r)
			return
		}

		// OpenAI 风格 401 — 各客户端(OpenAI/Anthropic/Gemini)都能识别该结构
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key","type":"authentication_error"}}`))
	})
}

// extractAPIKey 从不同客户端放置密钥的位置提取,按优先级返回首个非空值:
//
//	Authorization: Bearer xxx   (OpenAI / Codex)
//	x-api-key: xxx              (Anthropic / Claude Code)
//	x-goog-api-key: xxx         (Gemini)
//	?key=xxx                    (Gemini CLI 备用)
func extractAPIKey(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
			if key := strings.TrimSpace(after); key != "" {
				return key
			}
		}
	}
	if key := strings.TrimSpace(r.Header.Get("x-api-key")); key != "" {
		return key
	}
	if key := strings.TrimSpace(r.Header.Get("x-goog-api-key")); key != "" {
		return key
	}
	if key := strings.TrimSpace(r.URL.Query().Get("key")); key != "" {
		return key
	}
	return ""
}

// matchesAnyKey 用常量时间比较防止时序侧信道,任一密钥匹配即通过。
func matchesAnyKey(presented string, keys []string) bool {
	for _, k := range keys {
		if subtle.ConstantTimeCompare([]byte(presented), []byte(k)) == 1 {
			return true
		}
	}
	return false
}

// ParseAPIKeys 把逗号分隔的密钥字符串解析为去空后的非空密钥列表。
func ParseAPIKeys(raw string) []string {
	var keys []string
	for _, part := range strings.Split(raw, ",") {
		if k := strings.TrimSpace(part); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

// isAuthExempt 判断路径是否豁免 API key 校验。
// /health 供云端探针;web UI 路径(页面/登录/会话/数据端点)由 web 包的
// Cookie 会话守卫,web 未启用时这些路径在 mux 落到 404。
func isAuthExempt(path string) bool {
	switch path {
	case "/health", "/", "/login":
		return true
	}
	return strings.HasPrefix(path, "/auth/") || strings.HasPrefix(path, "/web/")
}
