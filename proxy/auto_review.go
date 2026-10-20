package proxy

import "encoding/json"

// autoReviewPseudoModel is the model Codex sends for automatic approval
// review; Copilot has no model by that name.
const autoReviewPseudoModel = "codex-auto-review"

// rewriteAutoReviewModel replaces the top-level codex-auto-review model with
// target. Other bodies are returned untouched so passthrough stays
// byte-for-byte.
func rewriteAutoReviewModel(body []byte, target string) ([]byte, bool) {
	if target == "" {
		return body, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body, false
	}
	var model string
	if json.Unmarshal(raw["model"], &model) != nil || model != autoReviewPseudoModel {
		return body, false
	}

	raw["model"], _ = json.Marshal(target) // a string always encodes
	rewritten, err := json.Marshal(raw)
	if err != nil {
		return body, false
	}
	return rewritten, true
}
