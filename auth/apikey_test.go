package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const testKey = "secret-key-123"

// okHandler 是一个会标记自己被调用的下游 handler,用于断言中间件是否放行。
func okHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestAPIKeyAuth(t *testing.T) {
	tests := []struct {
		name       string
		keys       []string
		setup      func(r *http.Request)
		path       string
		wantStatus int
		wantPassed bool
	}{
		{
			name:       "authorization bearer with correct key",
			keys:       []string{testKey},
			setup:      func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testKey) },
			path:       "/v1/models",
			wantStatus: http.StatusOK,
			wantPassed: true,
		},
		{
			name:       "x-api-key with correct key (anthropic)",
			keys:       []string{testKey},
			setup:      func(r *http.Request) { r.Header.Set("x-api-key", testKey) },
			path:       "/v1/messages",
			wantStatus: http.StatusOK,
			wantPassed: true,
		},
		{
			name:       "x-goog-api-key with correct key (gemini)",
			keys:       []string{testKey},
			setup:      func(r *http.Request) { r.Header.Set("x-goog-api-key", testKey) },
			path:       "/v1beta/models",
			wantStatus: http.StatusOK,
			wantPassed: true,
		},
		{
			name:       "query key with correct key (gemini fallback)",
			keys:       []string{testKey},
			setup:      func(r *http.Request) {},
			path:       "/v1beta/models?key=" + testKey,
			wantStatus: http.StatusOK,
			wantPassed: true,
		},
		{
			name:       "wrong key is rejected",
			keys:       []string{testKey},
			setup:      func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") },
			path:       "/v1/models",
			wantStatus: http.StatusUnauthorized,
			wantPassed: false,
		},
		{
			name:       "missing key is rejected",
			keys:       []string{testKey},
			setup:      func(r *http.Request) {},
			path:       "/v1/models",
			wantStatus: http.StatusUnauthorized,
			wantPassed: false,
		},
		{
			name:       "health endpoint bypasses auth",
			keys:       []string{testKey},
			setup:      func(r *http.Request) {},
			path:       "/health",
			wantStatus: http.StatusOK,
			wantPassed: true,
		},
		{
			name:       "multiple keys - second matches",
			keys:       []string{"first-key", testKey},
			setup:      func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testKey) },
			path:       "/v1/models",
			wantStatus: http.StatusOK,
			wantPassed: true,
		},
		{
			name:       "bearer prefix without key is rejected",
			keys:       []string{testKey},
			setup:      func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") },
			path:       "/v1/models",
			wantStatus: http.StatusUnauthorized,
			wantPassed: false,
		},
		{
			name:       "web root bypasses api key auth",
			keys:       []string{testKey},
			setup:      func(r *http.Request) {},
			path:       "/",
			wantStatus: http.StatusOK,
			wantPassed: true,
		},
		{
			name:       "login page bypasses api key auth",
			keys:       []string{testKey},
			setup:      func(r *http.Request) {},
			path:       "/login",
			wantStatus: http.StatusOK,
			wantPassed: true,
		},
		{
			name:       "auth endpoints bypass api key auth",
			keys:       []string{testKey},
			setup:      func(r *http.Request) {},
			path:       "/auth/login",
			wantStatus: http.StatusOK,
			wantPassed: true,
		},
		{
			name:       "web data endpoint bypasses api key auth",
			keys:       []string{testKey},
			setup:      func(r *http.Request) {},
			path:       "/web/api/models",
			wantStatus: http.StatusOK,
			wantPassed: true,
		},
		{
			name:       "prefix-similar path still requires key",
			keys:       []string{testKey},
			setup:      func(r *http.Request) {},
			path:       "/authx",
			wantStatus: http.StatusUnauthorized,
			wantPassed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			passed := false
			handler := APIKeyAuth(tt.keys, okHandler(&passed))

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			tt.setup(req)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if passed != tt.wantPassed {
				t.Errorf("downstream called = %v, want %v", passed, tt.wantPassed)
			}
		})
	}
}

func TestParseAPIKeys(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "single key", raw: "abc", want: []string{"abc"}},
		{name: "comma separated", raw: "abc,def", want: []string{"abc", "def"}},
		{name: "trims whitespace", raw: " abc , def ", want: []string{"abc", "def"}},
		{name: "skips empty entries", raw: "abc,,def,", want: []string{"abc", "def"}},
		{name: "empty string yields none", raw: "", want: nil},
		{name: "only commas yields none", raw: ",,,", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAPIKeys(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d (%v), want %d (%v)", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("key[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
