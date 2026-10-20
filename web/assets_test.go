package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssetsArePublic(t *testing.T) {
	mux := newTestMux(t, stubModels{})

	for path, contentType := range map[string]string{
		"/web/assets/app.css":              "text/css",
		"/web/assets/app.js":               "text/javascript",
		"/web/assets/icons.svg":            "image/svg+xml",
		"/web/assets/favicon.svg":          "image/svg+xml",
		"/web/assets/favicon-32.png":       "image/png",
		"/web/assets/apple-touch-icon.png": "image/png",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200 without a session", path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, contentType) {
			t.Errorf("%s Content-Type = %q, want %s", path, got, contentType)
		}
	}
}

func TestAssetsOnlyServeKnownFiles(t *testing.T) {
	mux := newTestMux(t, stubModels{})

	for _, path := range []string{"/web/assets/index.html", "/web/assets/login.html", "/web/assets/missing.css"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, rec.Code)
		}
	}
}

func TestConfigEndpointIncludesUser(t *testing.T) {
	mux := newTestMux(t, stubModels{})
	c := doLogin(t, mux, "admin", "pass123")
	req := httptest.NewRequest(http.MethodGet, "/web/api/config", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if string(got["username"]) != `"admin"` {
		t.Fatalf("username = %s, want \"admin\"", got["username"])
	}
	if _, ok := got["version"]; ok {
		t.Fatalf("config still exposes version: %s", rec.Body.String())
	}
}

// refreshingModels counts Refresh calls on top of stubModels.
type refreshingModels struct {
	stubModels
	refreshes *int
	err       error
}

func (s refreshingModels) Refresh(context.Context) error { *s.refreshes++; return s.err }

func TestModelsRefreshEndpoint(t *testing.T) {
	refreshes := 0
	mux := http.NewServeMux()
	NewHandler(testConfig(), refreshingModels{stubModels: stubModels{raw: []byte(`{"data":[{"id":"m1"}]}`)}, refreshes: &refreshes}).Register(mux)

	req := httptest.NewRequest(http.MethodPost, "/web/api/models/refresh", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || refreshes != 0 {
		t.Fatalf("without session: status = %d, refreshes = %d; want 401 and no upstream fetch", rec.Code, refreshes)
	}

	c := doLogin(t, mux, "admin", "pass123")
	req = httptest.NewRequest(http.MethodPost, "/web/api/models/refresh", nil)
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || refreshes != 1 || !strings.Contains(rec.Body.String(), `"m1"`) {
		t.Fatalf("with session: status = %d, refreshes = %d, body = %s; want 200 with the fetched catalog", rec.Code, refreshes, rec.Body.String())
	}
}

// The dashboard shows this message under its own localized title, so it must
// be the upstream error itself, not a fixed-language prefix.
func TestModelsErrorReturnsUpstreamMessage(t *testing.T) {
	mux := newTestMux(t, stubModels{err: errors.New("GitHub Copilot authorization required")})
	c := doLogin(t, mux, "admin", "pass123")
	req := httptest.NewRequest(http.MethodGet, "/web/api/models", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var got struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusBadGateway || got.Error != "GitHub Copilot authorization required" {
		t.Fatalf("status = %d, error = %q; want 502 with the upstream message only", rec.Code, got.Error)
	}
}
