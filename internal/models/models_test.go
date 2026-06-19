package models

import (
	"encoding/json"
	"testing"
)

func TestPickEndpoint(t *testing.T) {
	tests := []struct {
		name      string
		info      *Info
		preferred []string
		want      string
	}{
		{
			name:      "nil info returns empty",
			info:      nil,
			preferred: []string{"/chat/completions"},
			want:      "",
		},
		{
			name:      "model supports first preferred",
			info:      &Info{ID: "gpt-4", SupportedEndpoints: []string{"/v1/chat/completions", "/v1/responses"}},
			preferred: []string{"/chat/completions", "/responses"},
			want:      "/chat/completions",
		},
		{
			name:      "model supports second preferred",
			info:      &Info{ID: "o3-mini", SupportedEndpoints: []string{"/v1/responses"}},
			preferred: []string{"/chat/completions", "/responses"},
			want:      "/responses",
		},
		{
			name:      "model supports neither",
			info:      &Info{ID: "embedding-model", SupportedEndpoints: []string{"/v1/embeddings"}},
			preferred: []string{"/chat/completions", "/responses"},
			want:      "",
		},
		{
			name:      "empty preferred list",
			info:      &Info{ID: "gpt-4", SupportedEndpoints: []string{"/v1/chat/completions"}},
			preferred: []string{},
			want:      "",
		},
		{
			name:      "normalizes /v1 prefix in preferred",
			info:      &Info{ID: "gpt-4", SupportedEndpoints: []string{"/v1/chat/completions"}},
			preferred: []string{"/v1/chat/completions"},
			want:      "/v1/chat/completions",
		},
		{
			name:      "normalizes no prefix in supported endpoints",
			info:      &Info{ID: "gpt-4", SupportedEndpoints: []string{"/chat/completions"}},
			preferred: []string{"/v1/chat/completions"},
			want:      "/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PickEndpoint(tt.info, tt.preferred)
			if got != tt.want {
				t.Errorf("PickEndpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSupportsEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		info     *Info
		endpoint string
		want     bool
	}{
		{
			name:     "nil info",
			info:     nil,
			endpoint: "/chat/completions",
			want:     false,
		},
		{
			name:     "exact match with /v1 prefix",
			info:     &Info{SupportedEndpoints: []string{"/v1/chat/completions"}},
			endpoint: "/v1/chat/completions",
			want:     true,
		},
		{
			name:     "match without /v1 prefix",
			info:     &Info{SupportedEndpoints: []string{"/v1/chat/completions"}},
			endpoint: "/chat/completions",
			want:     true,
		},
		{
			name:     "no match",
			info:     &Info{SupportedEndpoints: []string{"/v1/responses"}},
			endpoint: "/chat/completions",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SupportsEndpoint(tt.info, tt.endpoint)
			if got != tt.want {
				t.Errorf("SupportsEndpoint() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/v1/chat/completions", "/chat/completions"},
		{"/chat/completions", "/chat/completions"},
		{"chat/completions", "/chat/completions"},
		{"/v1/responses", "/responses"},
		{"", "/"},
		{"  /v1/chat/completions  ", "/chat/completions"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeEndpoint(tt.input)
			if got != tt.want {
				t.Errorf("normalizeEndpoint(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

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
