package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubModels 是 ModelsSource 的测试替身。
type stubModels struct {
	raw []byte
	err error
	at  time.Time
}

func (s stubModels) GetRaw(ctx context.Context) ([]byte, error) { return s.raw, s.err }
func (s stubModels) CachedAt() time.Time                        { return s.at }

func testConfig() Config {
	return Config{Username: "admin", Password: "pass123", SessionHours: 1}
}

func newTestMux(t *testing.T, source ModelsSource) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	NewHandler(testConfig(), source).Register(mux)
	return mux
}

// doLogin 执行登录并返回会话 cookie。
func doLogin(t *testing.T, mux *http.ServeMux, username, password string) *http.Cookie {
	t.Helper()
	body := strings.NewReader(`{"username":"` + username + `","password":"` + password + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "copilot2api_session" {
			return c
		}
	}
	t.Fatal("login response missing session cookie")
	return nil
}

func TestConfigFromEnv(t *testing.T) {
	tests := []struct {
		name        string
		user, pass  string
		hours       string
		wantEnabled bool
		wantHours   int
	}{
		{name: "both set enables", user: "u", pass: "p", wantEnabled: true, wantHours: 24},
		{name: "custom hours", user: "u", pass: "p", hours: "8", wantEnabled: true, wantHours: 8},
		{name: "invalid hours falls back", user: "u", pass: "p", hours: "zero", wantEnabled: true, wantHours: 24},
		{name: "missing password disables", user: "u", wantEnabled: false},
		{name: "missing username disables", pass: "p", wantEnabled: false},
		{name: "nothing set disables", wantEnabled: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("COPILOT2API_WEB_USERNAME", tt.user)
			t.Setenv("COPILOT2API_WEB_PASSWORD", tt.pass)
			t.Setenv("COPILOT2API_WEB_SESSION_HOURS", tt.hours)
			cfg, enabled := ConfigFromEnv()
			if enabled != tt.wantEnabled {
				t.Fatalf("enabled = %v, want %v", enabled, tt.wantEnabled)
			}
			if enabled && cfg.SessionHours != tt.wantHours {
				t.Errorf("SessionHours = %d, want %d", cfg.SessionHours, tt.wantHours)
			}
		})
	}
}

func TestLogin(t *testing.T) {
	mux := newTestMux(t, stubModels{})

	t.Run("valid credentials set session cookie", func(t *testing.T) {
		c := doLogin(t, mux, "admin", "pass123")
		if !c.HttpOnly {
			t.Error("session cookie should be HttpOnly")
		}
		if c.SameSite != http.SameSiteLaxMode {
			t.Error("session cookie should be SameSite=Lax")
		}
	})

	t.Run("wrong password rejected", func(t *testing.T) {
		body := strings.NewReader(`{"username":"admin","password":"wrong"}`)
		req := httptest.NewRequest(http.MethodPost, "/auth/login", body)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("malformed body rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader("not json"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})
}

func TestPageGuard(t *testing.T) {
	mux := newTestMux(t, stubModels{})

	t.Run("index without session redirects to login", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d, want 302", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/login" {
			t.Errorf("Location = %q, want /login", loc)
		}
	})

	t.Run("index with session serves page", func(t *testing.T) {
		c := doLogin(t, mux, "admin", "pass123")
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q, want text/html", ct)
		}
	})

	t.Run("login page is public", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})
}

func TestSessionEndpointAndLogout(t *testing.T) {
	mux := newTestMux(t, stubModels{})

	t.Run("session endpoint reports state", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var got struct {
			Authenticated bool `json:"authenticated"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Authenticated {
			t.Error("should be unauthenticated without cookie")
		}
	})

	t.Run("logout invalidates session", func(t *testing.T) {
		c := doLogin(t, mux, "admin", "pass123")

		req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("logout status = %d, want 200", rec.Code)
		}

		req = httptest.NewRequest(http.MethodGet, "/web/api/models", nil)
		req.AddCookie(c)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("after logout status = %d, want 401", rec.Code)
		}
	})
}

func TestConfigEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	cfg := testConfig()
	cfg.APIKeys = []string{"sk-demo-1", "sk-demo-2"}
	NewHandler(cfg, stubModels{}).Register(mux)

	t.Run("without session returns 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/web/api/config", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("with session returns api keys", func(t *testing.T) {
		c := doLogin(t, mux, "admin", "pass123")
		req := httptest.NewRequest(http.MethodGet, "/web/api/config", nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var got struct {
			APIKeys []string `json:"api_keys"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.APIKeys) != 2 || got.APIKeys[0] != "sk-demo-1" {
			t.Errorf("api_keys = %v, want [sk-demo-1 sk-demo-2]", got.APIKeys)
		}
	})
}

func TestModelsEndpoint(t *testing.T) {
	upstreamRaw := []byte(`{"object":"list","data":[{"id":"claude-fable-5","name":"Claude Fable 5"}]}`)
	fixedAt := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)

	t.Run("without session returns 401", func(t *testing.T) {
		mux := newTestMux(t, stubModels{raw: upstreamRaw, at: fixedAt})
		req := httptest.NewRequest(http.MethodGet, "/web/api/models", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("with session returns cached data", func(t *testing.T) {
		mux := newTestMux(t, stubModels{raw: upstreamRaw, at: fixedAt})
		c := doLogin(t, mux, "admin", "pass123")
		req := httptest.NewRequest(http.MethodGet, "/web/api/models", nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		var got struct {
			CachedAt time.Time         `json:"cached_at"`
			Data     []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !got.CachedAt.Equal(fixedAt) {
			t.Errorf("cached_at = %v, want %v", got.CachedAt, fixedAt)
		}
		if len(got.Data) != 1 {
			t.Errorf("data length = %d, want 1", len(got.Data))
		}
	})

	t.Run("upstream failure returns 502 with error", func(t *testing.T) {
		mux := newTestMux(t, stubModels{err: context.DeadlineExceeded})
		c := doLogin(t, mux, "admin", "pass123")
		req := httptest.NewRequest(http.MethodGet, "/web/api/models", nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "error") {
			t.Errorf("body should contain error message, got: %s", rec.Body.String())
		}
	})
}
