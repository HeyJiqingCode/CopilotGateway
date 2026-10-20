package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/internal/copilot"
	"github.com/HeyJiqingCode/CopilotGateway/internal/upstream"
)

func TestSupportsReasoningEffort(t *testing.T) {
	tests := []struct {
		name string
		info *Info
		want bool
	}{
		{"nil info returns true", nil, true},
		{
			name: "opus supports effort",
			info: &Info{ID: "claude-opus-4.8", Capabilities: Capabilities{Supports: Supports{ReasoningEffort: []string{"low", "medium", "high", "xhigh", "max"}}}},
			want: true,
		},
		{
			name: "sonnet supports effort",
			info: &Info{ID: "claude-sonnet-4.6", Capabilities: Capabilities{Supports: Supports{ReasoningEffort: []string{"low", "medium", "high", "max"}}}},
			want: true,
		},
		{
			name: "haiku missing field does not support",
			info: &Info{ID: "claude-haiku-4.5"},
			want: false,
		},
		{
			name: "explicit empty array does not support",
			info: &Info{ID: "x", Capabilities: Capabilities{Supports: Supports{ReasoningEffort: []string{}}}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SupportsReasoningEffort(tt.info); got != tt.want {
				t.Errorf("SupportsReasoningEffort = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestInfo_ParsesReasoningEffort 锁定 capabilities.supports.reasoning_effort 的 JSON 路径,
// 防止上游结构变动悄悄破坏能力检测。
func TestInfo_ParsesReasoningEffort(t *testing.T) {
	raw := `{
		"data": [
			{"id": "claude-opus-4.8", "supported_endpoints": ["/v1/messages"], "capabilities": {"supports": {"reasoning_effort": ["low", "high", "max"]}}},
			{"id": "claude-haiku-4.5", "supported_endpoints": ["/v1/messages"], "capabilities": {"supports": {}}}
		]
	}`

	var resp modelsListResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	byID := map[string]*Info{}
	for i := range resp.Data {
		byID[resp.Data[i].ID] = &resp.Data[i]
	}

	if got := byID["claude-opus-4.8"].Capabilities.Supports.ReasoningEffort; len(got) != 3 {
		t.Errorf("opus reasoning_effort = %v, want 3 entries", got)
	}
	if got := byID["claude-haiku-4.5"].Capabilities.Supports.ReasoningEffort; got != nil {
		t.Errorf("haiku reasoning_effort = %v, want nil", got)
	}
}

// TestCacheFetchUsesModelAccessHeaders 锁定 VS Code 模型能力元数据请求头,
// 防止 /models 回退到旧协议导致 1M context 能力不可见。
func TestCacheFetchUsesModelAccessHeaders(t *testing.T) {
	var gotHeaders http.Header
	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"id":                  "claude-opus-4.8",
					"supported_endpoints": []string{"/v1/messages"},
				},
			},
		})
	}))
	defer fakeUpstream.Close()

	tp := &stubTokenProvider{baseURL: fakeUpstream.URL}
	cache := NewCache(upstream.NewClient(tp, nil), time.Minute)

	if _, err := cache.GetRaw(context.Background()); err != nil {
		t.Fatalf("GetRaw failed: %v", err)
	}

	expected := map[string]string{
		"Openai-Intent":        "model-access",
		"X-Interaction-Type":   "model-access",
		"X-Github-Api-Version": copilot.APIVersion,
	}
	for header, want := range expected {
		if got := gotHeaders.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if gotHeaders.Get("X-Request-Id") == "" {
		t.Error("X-Request-Id should be present")
	}
	if got, want := gotHeaders.Get("X-Agent-Task-Id"), gotHeaders.Get("X-Request-Id"); got != want {
		t.Errorf("X-Agent-Task-Id = %q, want the request ID %q", got, want)
	}
}

type stubTokenProvider struct {
	baseURL string
}

func (s *stubTokenProvider) GetToken(_ context.Context) (string, error) {
	return "test-token", nil
}

func (s *stubTokenProvider) GetBaseURL() string {
	return s.baseURL
}

func TestCachedAt(t *testing.T) {
	c := NewCache(nil, time.Minute)
	if !c.CachedAt().IsZero() {
		t.Error("fresh cache should have zero CachedAt")
	}
	now := time.Now()
	c.mu.Lock()
	c.cachedAt = now
	c.mu.Unlock()
	if !c.CachedAt().Equal(now) {
		t.Errorf("CachedAt = %v, want %v", c.CachedAt(), now)
	}
}
