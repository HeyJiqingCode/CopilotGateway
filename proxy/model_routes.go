package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ModelRoutes maps a client-requested model ID to the upstream model ID.
// Configure once before serving requests; it is read-only afterwards.
type ModelRoutes map[string]string

// ParseModelRoutes parses a JSON object such as {"codex-auto-review":"gpt-5.6-luna"}.
// An empty value means no routes.
func ParseModelRoutes(raw string) (ModelRoutes, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var routes ModelRoutes
	if err := json.Unmarshal([]byte(raw), &routes); err != nil {
		return nil, fmt.Errorf("model routes must be a JSON object of model IDs: %w", err)
	}
	if routes == nil {
		return nil, fmt.Errorf("model routes must be a JSON object, got null")
	}
	for source, target := range routes {
		if source == "" || source != strings.TrimSpace(source) {
			return nil, fmt.Errorf("model route source %q must be a non-empty model ID without surrounding spaces", source)
		}
		if target == "" || target != strings.TrimSpace(target) {
			return nil, fmt.Errorf("model route target for %q must be a non-empty model ID without surrounding spaces", source)
		}
		if source == target {
			return nil, fmt.Errorf("model route %q cannot target itself", source)
		}
	}
	return routes, nil
}

// rewrite replaces the top-level model when it has a route. Unrouted bodies
// are returned untouched so passthrough stays byte-for-byte.
func (r ModelRoutes) rewrite(body []byte) ([]byte, string, string, bool) {
	if len(r) == 0 {
		return body, "", "", false
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body, "", "", false
	}
	var model string
	if json.Unmarshal(raw["model"], &model) != nil {
		return body, "", "", false
	}
	target, ok := r[model]
	if !ok {
		return body, "", "", false
	}

	raw["model"], _ = json.Marshal(target) // string is always JSON encodable
	rewritten, err := json.Marshal(raw)
	if err != nil {
		return body, "", "", false
	}
	return rewritten, model, target, true
}
