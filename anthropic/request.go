package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// messagesRequest is a native /v1/messages body parsed only at the top level.
// The gateway reads the few fields it needs; every value keeps its original
// bytes so upstream receives exactly what the client sent unless an edit is due.
type messagesRequest struct {
	body         []byte
	fields       map[string]json.RawMessage
	model        string
	maxTokens    int
	stream       bool
	messageCount int
}

func parseMessagesRequest(body []byte) (*messagesRequest, error) {
	req := &messagesRequest{body: body}
	if err := json.Unmarshal(body, &req.fields); err != nil {
		return nil, err
	}
	for _, f := range []struct {
		name string
		into any
	}{{"model", &req.model}, {"max_tokens", &req.maxTokens}, {"stream", &req.stream}} {
		if raw, ok := req.fields[f.name]; ok {
			if err := json.Unmarshal(raw, f.into); err != nil {
				return nil, fmt.Errorf("field %s: %w", f.name, err)
			}
		}
	}
	if raw, ok := req.fields["messages"]; ok {
		var messages []json.RawMessage
		if err := json.Unmarshal(raw, &messages); err != nil {
			return nil, fmt.Errorf("field messages: %w", err)
		}
		req.messageCount = len(messages)
	}
	return req, nil
}

func (r *messagesRequest) validate() error {
	if r.model == "" {
		return errors.New("model is required")
	}
	if r.maxTokens <= 0 {
		return errors.New("max_tokens must be positive")
	}
	if r.messageCount == 0 && !r.has("system") {
		return errors.New("either messages or system must be provided")
	}
	return nil
}

// has reports whether a top-level field is present and not null.
func (r *messagesRequest) has(name string) bool {
	raw, ok := r.fields[name]
	return ok && string(raw) != "null"
}

func (r *messagesRequest) keys() []string {
	keys := make([]string, 0, len(r.fields))
	for k := range r.fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// rewriteOptions lists the edits Copilot needs on a native /v1/messages body.
type rewriteOptions struct {
	model                 string // upstream model ID; replaces the client's when different
	dropContextManagement bool   // Copilot rejects context_management without its beta
	dropReasoning         bool   // the model accepts no output_config/thinking
}

// rewrite applies opts and drops every cache_control.scope, which Copilot
// rejects. Without any edit the original body is returned unchanged; otherwise
// only the edited top-level values are re-encoded, and numbers stay exact.
func (r *messagesRequest) rewrite(opts rewriteOptions) (body []byte, removedScopes int, err error) {
	changed := false
	if opts.model != r.model {
		r.fields["model"], _ = json.Marshal(opts.model) // a string always encodes
		changed = true
	}
	if opts.dropContextManagement {
		delete(r.fields, "context_management")
		changed = true
	}
	if opts.dropReasoning {
		delete(r.fields, "output_config")
		delete(r.fields, "thinking")
		changed = true
	}
	if bytes.Contains(r.body, []byte(`"scope"`)) {
		for name, raw := range r.fields {
			if !bytes.Contains(raw, []byte(`"scope"`)) {
				continue
			}
			stripped, n, err := stripCacheControlScopes(raw)
			if err != nil {
				return nil, 0, fmt.Errorf("field %s: %w", name, err)
			}
			if n > 0 {
				r.fields[name] = stripped
				removedScopes += n
				changed = true
			}
		}
	}
	if !changed {
		return r.body, 0, nil
	}
	out, err := encodeJSON(r.fields)
	return out, removedScopes, err
}

// stripCacheControlScopes removes cache_control.scope anywhere inside raw.
func stripCacheControlScopes(raw json.RawMessage) (json.RawMessage, int, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep integers beyond 2^53 exact
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, 0, err
	}
	n := stripCacheControlScope(v)
	if n == 0 {
		return raw, 0, nil
	}
	out, err := encodeJSON(v)
	return out, n, err
}

func stripCacheControlScope(v any) int {
	n := 0
	switch node := v.(type) {
	case map[string]any:
		if cacheControl, ok := node["cache_control"].(map[string]any); ok {
			if _, has := cacheControl["scope"]; has {
				delete(cacheControl, "scope")
				n++
			}
		}
		for _, child := range node {
			n += stripCacheControlScope(child)
		}
	case []any:
		for _, child := range node {
			n += stripCacheControlScope(child)
		}
	}
	return n
}

// encodeJSON marshals without HTML escaping, so text such as "<b>" or "&"
// is forwarded as written.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
