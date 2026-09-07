package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/internal/upstream"
)

// Opt-in, billable Copilot integration test. It never reads the user's Codex
// sessions or credentials on disk. Supply an existing Copilot bearer token in
// COPILOTGW_LIVE_TOKEN, its endpoint in COPILOTGW_LIVE_URL and comma-separated
// model names in COPILOTGW_LIVE_MODELS. No secrets/ciphertext are logged.
func TestResponsesIDs_LiveCopilotReplay(t *testing.T) {
	token, baseURL, models := os.Getenv("COPILOTGW_LIVE_TOKEN"), os.Getenv("COPILOTGW_LIVE_URL"), os.Getenv("COPILOTGW_LIVE_MODELS")
	if token == "" || baseURL == "" || models == "" {
		t.Skip("requires COPILOTGW_LIVE_TOKEN, COPILOTGW_LIVE_URL and COPILOTGW_LIVE_MODELS")
	}
	for _, model := range strings.Split(models, ",") {
		t.Run(strings.TrimSpace(model), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			uc := upstream.NewClient(liveResponsesToken{token: token, baseURL: baseURL}, nil)
			user := map[string]interface{}{"role": "user", "content": "Find the smallest positive integer x with x modulo 7 = 3, modulo 11 = 5 and modulo 13 = 8. Check the congruences, then call diagnostic_ping."}
			request := map[string]interface{}{
				"model": strings.TrimSpace(model), "stream": true, "store": false,
				"include": []string{"reasoning.encrypted_content"}, "max_output_tokens": 2048,
				"reasoning":    map[string]string{"effort": "medium", "summary": "detailed"},
				"instructions": "This is a transport test. Think before using diagnostic_ping. Send a short commentary message saying Checking before calling the tool once. Wait for the tool result before providing a final answer.",
				"input":        []interface{}{user},
				"tools": []interface{}{map[string]interface{}{
					"type": "function", "name": "diagnostic_ping", "description": "Return a test marker.", "strict": true,
					"parameters": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}, "required": []string{}, "additionalProperties": false},
				}},
			}
			resp, _, err := uc.Do(ctx, upstream.Request{Endpoint: "/responses", Body: request, Stream: true})
			if err != nil {
				t.Fatalf("initial Copilot request failed (%T); error body withheld", err)
			}
			raw, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
			resp.Body.Close()
			if err != nil {
				t.Fatalf("read initial Copilot stream: %v", err)
			}
			original := responseEvents(t, string(raw))
			w := httptest.NewRecorder()
			if err := streamResponses(w, bytes.NewReader(raw)); err != nil {
				t.Fatal(err)
			}
			processed := responseEvents(t, w.Body.String())
			if len(original) != len(processed) {
				t.Fatal("compatibility changed the number of upstream events")
			}
			firstIDs := make(map[string]string)
			driftCount := 0
			for n, event := range processed {
				if index, ok := event["output_index"].(json.Number); ok {
					id, _ := event["item_id"].(string)
					if item, ok := event["item"].(map[string]interface{}); ok {
						id, _ = item["id"].(string)
					}
					if id != "" {
						if firstIDs[index.String()] == "" {
							firstIDs[index.String()] = id
						} else if id != firstIDs[index.String()] {
							t.Fatalf("processed item ID still drifted at event %d", n)
						}
					}
				}
				// Verify all non-ID fields against the raw upstream, including
				// each complete encrypted blob, stable call_id and response.id.
				if !reflect.DeepEqual(original[n], event) {
					driftCount++
				}
				removeOutputIDs(original[n])
				removeOutputIDs(event)
				if !reflect.DeepEqual(original[n], event) {
					t.Fatalf("non-ID upstream fields changed at event %d", n)
				}
			}
			// Reparse because the comparison above removed IDs from its copies.
			processed = responseEvents(t, w.Body.String())
			last := processed[len(processed)-1]
			if last["type"] != "response.completed" {
				t.Fatalf("initial response did not complete: %v", last["type"])
			}
			final := last["response"].(map[string]interface{})
			output := final["output"].([]interface{})
			var doneItems []interface{}
			for _, event := range processed {
				if event["type"] == "response.output_item.done" {
					doneItems = append(doneItems, event["item"])
				}
			}
			cipherBytes, toolCalls := 0, 0
			var results []interface{}
			for i, raw := range output {
				item := raw.(map[string]interface{})
				if id := firstIDs[fmt.Sprint(i)]; id != "" && item["id"] != id {
					t.Fatalf("terminal output ID differs at index %d", i)
				}
				if cipher, ok := item["encrypted_content"].(string); ok {
					cipherBytes += len(cipher)
				}
				if item["type"] == "function_call" {
					toolCalls++
					results = append(results, map[string]interface{}{"type": "function_call_output", "call_id": item["call_id"], "output": "diagnostic marker OK"})
				}
			}
			if cipherBytes == 0 || toolCalls == 0 {
				t.Fatal("probe did not produce encrypted reasoning and a tool call; replay not verified")
			}
			t.Logf("normalized events=%d; preserved final ciphertext bytes=%d; tool calls=%d", driftCount, cipherBytes, toolCalls)
			// Cover both Codex's done-item history and final response.output,
			// with a fresh handler and compatibility disabled on continuation.
			for _, history := range []struct {
				name  string
				items []interface{}
			}{{"output_item.done", doneItems}, {"response.output", output}} {
				input := append([]interface{}{user}, history.items...)
				input = append(input, results...)
				request["input"] = input
				request["instructions"] = "The diagnostic tool succeeded. Do not call tools again. Reply only OK."
				encoded, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				h := &Handler{upstream: uc, DisableResponsesIDCompat: true}
				r := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(encoded)).WithContext(ctx)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusOK {
					t.Fatalf("%s replay rejected: HTTP %d (body withheld)", history.name, w.Code)
				}
				events := responseEvents(t, w.Body.String())
				if len(events) == 0 || events[len(events)-1]["type"] != "response.completed" {
					t.Fatalf("%s replay did not complete", history.name)
				}
				t.Logf("%s history accepted after restart with compatibility disabled", history.name)
			}
		})
	}
}

func removeOutputIDs(event map[string]interface{}) {
	delete(event, "item_id")
	if item, ok := event["item"].(map[string]interface{}); ok {
		delete(item, "id")
	}
	if response, ok := event["response"].(map[string]interface{}); ok {
		if output, ok := response["output"].([]interface{}); ok {
			for _, raw := range output {
				delete(raw.(map[string]interface{}), "id")
			}
		}
	}
}

type liveResponsesToken struct{ token, baseURL string }

func (p liveResponsesToken) GetToken(context.Context) (string, error) { return p.token, nil }
func (p liveResponsesToken) GetBaseURL() string                       { return p.baseURL }
