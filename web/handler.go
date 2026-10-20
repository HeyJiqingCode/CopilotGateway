package web

import (
	"context"
	"embed"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"time"
)

//go:embed static
var staticFS embed.FS

const cookieName = "copilotgateway_session"

// Config 是 web UI 的启用配置(凭据 + 会话时长)。
type Config struct {
	Username     string
	Password     string
	SessionHours int
	// APIKeys 供页面「接入信息」展示与复制,由 main 注入(与网关鉴权同一份)。
	APIKeys []string
}

// ConfigFromEnv 从环境变量读取配置;用户名或密码缺失时返回 false(web 不启用)。
func ConfigFromEnv() (Config, bool) {
	username := os.Getenv("COPILOTGW_WEB_USERNAME")
	password := os.Getenv("COPILOTGW_WEB_PASSWORD")
	if username == "" || password == "" {
		return Config{}, false
	}
	hours := 24
	if v := os.Getenv("COPILOTGW_WEB_SESSION_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			hours = n
		}
	}
	return Config{Username: username, Password: password, SessionHours: hours}, true
}

// ModelsSource 是数据端点所需的最小数据源接口(*copilot.Models 满足)。
type ModelsSource interface {
	GetRaw(ctx context.Context) ([]byte, error)
	CachedAt() time.Time
	// Refresh 绕过缓存期限,立即从上游重新拉取。
	Refresh(ctx context.Context) error
}

// Handler 承载 web UI 全部路由:页面、登录会话与数据端点。
type Handler struct {
	cfg      Config
	sessions *sessionStore
	models   ModelsSource
}

func NewHandler(cfg Config, source ModelsSource) *Handler {
	return &Handler{cfg: cfg, sessions: newSessionStore(), models: source}
}

// Register 把 web 路由挂到 mux;未调用则所有 web 路径自然 404。
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", h.handleIndex)
	mux.HandleFunc("GET /login", h.handleLoginPage)
	mux.HandleFunc("POST /auth/login", h.handleLogin)
	mux.HandleFunc("POST /auth/logout", h.handleLogout)
	mux.HandleFunc("GET /web/api/models", h.handleModels)
	mux.HandleFunc("POST /web/api/models/refresh", h.handleModelsRefresh)
	mux.HandleFunc("GET /web/api/config", h.handleConfig)
	mux.HandleFunc("GET /web/assets/{file}", handleAsset)
}

// assetTypes 是登录页和目录页共用的静态资源白名单;登录前也要能加载。
var assetTypes = map[string]string{
	"app.css":              "text/css; charset=utf-8",
	"app.js":               "text/javascript; charset=utf-8",
	"icons.svg":            "image/svg+xml",
	"favicon.svg":          "image/svg+xml",
	"favicon-32.png":       "image/png",
	"apple-touch-icon.png": "image/png",
}

func handleAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	contentType, ok := assetTypes[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	// 资源随二进制发布;每次校验,升级后不会拿到旧样式。
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// currentUser 从会话 cookie 解析当前登录用户。
func (h *Handler) currentUser(r *http.Request) (string, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	return h.sessions.Username(c.Value)
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.currentUser(r); !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	serveStatic(w, "static/index.html")
}

func (h *Handler) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, "static/login.html")
}

func serveStatic(w http.ResponseWriter, path string) {
	data, err := staticFS.ReadFile(path)
	if err != nil {
		http.Error(w, "page not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !h.validCredentials(req.Username, req.Password) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid username or password"})
		return
	}
	ttl := time.Duration(h.cfg.SessionHours) * time.Hour
	token := h.sessions.Create(req.Username, ttl)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		h.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSession(w, r); ok {
		h.writeModels(w, r)
	}
}

// handleModelsRefresh 立即从上游重新拉取目录,再返回与 GET 相同的数据。
func (h *Handler) handleModelsRefresh(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSession(w, r); !ok {
		return
	}
	if err := h.models.Refresh(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	h.writeModels(w, r)
}

func (h *Handler) writeModels(w http.ResponseWriter, r *http.Request) {
	raw, err := h.models.GetRaw(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	var upstreamBody struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &upstreamBody); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "invalid upstream models response: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, struct {
		CachedAt time.Time       `json:"cached_at"`
		Data     json.RawMessage `json:"data"`
	}{h.models.CachedAt(), upstreamBody.Data})
}

// handleConfig 返回接入信息(API key 仅对已登录会话下发)和当前用户。
func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	if username, ok := h.requireSession(w, r); ok {
		writeJSON(w, http.StatusOK, map[string]any{"api_keys": h.cfg.APIKeys, "username": username})
	}
}

// requireSession 返回当前登录用户;没有有效会话时直接回 401。
func (h *Handler) requireSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	username, ok := h.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
	}
	return username, ok
}

// validCredentials 用常量时间比较校验凭据,两个字段都比较后再合并结果。
func (h *Handler) validCredentials(username, password string) bool {
	userOK := constantTimeEquals(username, h.cfg.Username)
	passOK := constantTimeEquals(password, h.cfg.Password)
	return userOK && passOK
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// requestIsHTTPS 识别直连 TLS 或反代(ACA)转发的 HTTPS 请求。
func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
