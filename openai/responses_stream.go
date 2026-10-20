package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// A Responses request has its own output-index namespace. Never keep these
// associations on Handler: it is shared by concurrent requests.
type responsesIDs struct {
	items map[int]*responseItemID
}

type responseItemID struct {
	id       string
	kind     string
	callID   string
	drifted  bool
	conflict bool
}

const (
	maxResponsesSSELine  = 1 << 20  // retain the legacy per-line limit
	maxResponsesSSEEvent = 50 << 20 // allow valid multiline events larger than one line
)

// streamResponses buffers one SSE event, not the response. Normal events keep
// their original bytes (including multiline data, comments and CRLF framing).
func streamResponses(w http.ResponseWriter, body io.Reader) error {
	scanner := bufio.NewScanner(body)
	scanner.Split(splitSSEEvents)
	scanner.Buffer(make([]byte, 64*1024), maxResponsesSSEEvent)
	ids := responsesIDs{items: make(map[int]*responseItemID)}
	flusher, canFlush := w.(http.Flusher)
	for scanner.Scan() {
		frame := scanner.Bytes()
		data, event := sseEventData(frame)
		terminal := isResponsesTerminationEvent("event: " + event)
		if len(data) > 0 && !bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(data, &obj); err != nil {
				slog.Warn("cannot normalize Responses SSE JSON; forwarding event", "error", err)
			} else if obj != nil {
				kind := jsonString(obj["type"])
				if kind != "" {
					event = kind
				}
				// An explicit new response starts a new output-index namespace,
				// even if a nonstandard upstream multiplexes responses on a stream.
				if event == "response.created" {
					ids.items = make(map[int]*responseItemID)
				}
				changed, err := ids.normalize(obj, event)
				if err != nil {
					return fmt.Errorf("normalize Responses event %s: %w", event, err)
				}
				if changed {
					encoded, err := json.Marshal(obj)
					if err != nil {
						return fmt.Errorf("encode Responses event %s: %w", event, err)
					}
					frame = replaceSSEData(frame, encoded)
				}
			}
		}
		if _, err := w.Write(frame); err != nil {
			return fmt.Errorf("write Responses event: %w", err)
		}
		if canFlush {
			flusher.Flush()
		}
		if terminal || isResponsesTerminationEvent("event: "+event) {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Responses SSE event: %w", err)
	}
	return nil
}

func (s *responsesIDs) normalize(event map[string]json.RawMessage, kind string) (bool, error) {
	if !strings.HasPrefix(kind, "response.") {
		return false, nil
	}
	changed := false
	var index int
	if raw, exists := event["output_index"]; exists && string(raw) != "null" && json.Unmarshal(raw, &index) == nil && index >= 0 {
		if raw, exists := event["item"]; exists {
			item, modified, err := s.normalizeItem(raw, index)
			if err != nil {
				return false, err
			}
			if modified {
				event["item"] = item
				changed = true
			}
		}
		if id := jsonString(event["item_id"]); id != "" {
			if canonical := s.associate(index, id, "", ""); canonical != id {
				event["item_id"], _ = json.Marshal(canonical) // string is always JSON encodable
				changed = true
			}
		}
	}

	// The array position is the output_index for response snapshots, including
	// completed, failed and incomplete. Keep the upstream response.id and any
	// response_id/previous_response_id unchanged: these are server-side handles,
	// not UI item identities. In particular, never replace the final response ID
	// with response.created's ID or introduce an in-memory replay alias.
	if raw := event["response"]; len(raw) > 0 {
		var response map[string]json.RawMessage
		if err := json.Unmarshal(raw, &response); err != nil {
			return false, fmt.Errorf("decode response: %w", err)
		}
		var output []json.RawMessage
		if raw := response["output"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &output); err != nil {
				return false, fmt.Errorf("decode response.output: %w", err)
			}
		}
		outputChanged := false
		for index, raw := range output {
			item, modified, err := s.normalizeItem(raw, index)
			if err != nil {
				return false, err
			}
			if modified {
				output[index] = item
				outputChanged = true
			}
		}
		if outputChanged {
			encoded, err := json.Marshal(output)
			if err != nil {
				return false, fmt.Errorf("encode response.output: %w", err)
			}
			response["output"] = encoded
			encoded, err = json.Marshal(response)
			if err != nil {
				return false, fmt.Errorf("encode response: %w", err)
			}
			event["response"] = encoded
			changed = true
		}
	}
	return changed, nil
}

func (s *responsesIDs) normalizeItem(raw json.RawMessage, index int) (json.RawMessage, bool, error) {
	var item map[string]json.RawMessage
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, false, fmt.Errorf("decode output item %d: %w", index, err)
	}
	id := jsonString(item["id"])
	if id == "" {
		return raw, false, nil
	}
	canonical := s.associate(index, id, jsonString(item["type"]), jsonString(item["call_id"]))
	if canonical == id {
		return raw, false, nil
	}
	item["id"], _ = json.Marshal(canonical)
	// Change only id. In particular the completed item's encrypted_content,
	// call_id, arguments, phase and unknown fields must survive unmodified.
	// Live Copilot replay was verified with the first upstream item ID and the
	// complete terminal ciphertext; never generate an ID or reuse added's blob.
	encoded, err := json.Marshal(item)
	if err != nil {
		return nil, false, fmt.Errorf("encode output item %d: %w", index, err)
	}
	return encoded, true, nil
}

func (s *responsesIDs) associate(index int, id, kind, callID string) string {
	item := s.items[index]
	if item == nil {
		s.items[index] = &responseItemID{id: id, kind: kind, callID: callID}
		return id
	}
	if item.conflict {
		return id
	}
	// An index alone is insufficient if the upstream supplies contradictory
	// item types or tool call IDs. Stop correlating this slot; never join calls.
	if (kind != "" && item.kind != "" && kind != item.kind) ||
		(callID != "" && item.callID != "" && callID != item.callID) {
		item.conflict = true
		slog.Warn("conflicting Responses output identity; leaving slot unchanged", "output_index", index)
		return id
	}
	if kind != "" {
		item.kind = kind
	}
	if callID != "" {
		item.callID = callID
	}
	if id != item.id && !item.drifted {
		item.drifted = true
		slog.Debug("normalized Responses output ID drift", "output_index", index, "item_type", item.kind)
	}
	return item.id
}

func jsonString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// splitSSEEvents retains the exact framing, including the delimiter. Like the
// existing passthrough it also forwards an unterminated last event at EOF.
func splitSSEEvents(data []byte, atEOF bool) (int, []byte, error) {
	start := 0
	for i, b := range data {
		if b != '\n' {
			continue
		}
		if i-start >= maxResponsesSSELine {
			return 0, nil, fmt.Errorf("Responses SSE line exceeds %d bytes", maxResponsesSSELine)
		}
		if i == start || (i == start+1 && data[start] == '\r') {
			return i + 1, data[:i+1], nil
		}
		start = i + 1
	}
	if len(data)-start >= maxResponsesSSELine {
		return 0, nil, fmt.Errorf("Responses SSE line exceeds %d bytes", maxResponsesSSELine)
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func sseEventData(frame []byte) ([]byte, string) {
	var data [][]byte
	var event string
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "data":
			data = append(data, value)
		case "event":
			event = string(value)
		}
	}
	return bytes.Join(data, []byte("\n")), event
}

func replaceSSEData(frame, data []byte) []byte {
	var out bytes.Buffer
	written := false
	for len(frame) > 0 {
		line, rest, hasNewline := bytes.Cut(frame, []byte("\n"))
		frame = rest
		field, _, _ := bytes.Cut(line, []byte(":"))
		if string(field) == "data" {
			if written {
				continue
			}
			out.WriteString("data: ")
			out.Write(data)
			if bytes.HasSuffix(line, []byte("\r")) {
				out.WriteByte('\r')
			}
			written = true
		} else {
			out.Write(line)
		}
		if hasNewline {
			out.WriteByte('\n')
		}
	}
	return out.Bytes()
}
