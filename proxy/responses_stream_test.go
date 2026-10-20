package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/internal/testutil"
	"github.com/HeyJiqingCode/CopilotGateway/internal/upstream"
)

func responseFrame(kind, fields string) string {
	return "event: " + kind + "\ndata: {\"type\":\"" + kind + "\"," + fields + "}\n\n"
}

// Deliberately interleave reasoning, two identical messages and a function.
// The terminal reasoning blob differs from both added and done: no snapshot's
// ciphertext may be replaced with an earlier snapshot's value.
func responsesFixture(prefix, terminal string, drift bool) string {
	id := func(index int, stage string) string {
		if !drift {
			stage = "added"
		}
		return fmt.Sprintf("%s_item_%d_%s", prefix, index, stage)
	}
	item := func(index int, stage string) string {
		switch index {
		case 0:
			return fmt.Sprintf(`{"id":%q,"type":"reasoning","summary":[],"encrypted_content":%q,"extension":{"number":9007199254740993}}`, id(index, stage), "opaque-"+stage+"-full-ciphertext==")
		case 2:
			return fmt.Sprintf(`{"id":%q,"type":"function_call","call_id":%q,"name":"ping","arguments":"{\"value\":1}","status":"completed"}`, id(index, stage), prefix+"_call")
		default:
			return fmt.Sprintf(`{"id":%q,"type":"message","role":"assistant","phase":"commentary","status":"completed","content":[{"type":"output_text","text":"CheckingChecking","annotations":[]}]}`, id(index, stage))
		}
	}
	var b strings.Builder
	b.WriteString(": keepalive\nretry: 500\n\n")
	b.WriteString(responseFrame("response.created", fmt.Sprintf(`"response":{"id":%q,"output":[]}`, prefix+"_response_start")))
	progressID := prefix + "_response_start"
	if drift {
		progressID = prefix + "_response_progress"
	}
	b.WriteString(responseFrame("response.in_progress", fmt.Sprintf(`"response":{"id":%q,"output":[]}`, progressID)))
	for i := range 4 {
		b.WriteString(responseFrame("response.output_item.added", fmt.Sprintf(`"output_index":%d,"item":%s`, i, item(i, "added"))))
	}
	for _, i := range []int{1, 3} {
		b.WriteString(responseFrame("response.content_part.added", fmt.Sprintf(`"output_index":%d,"item_id":%q,"content_index":0,"part":{"type":"output_text","text":""}`, i, id(i, "part_added"))))
	}
	b.WriteString(responseFrame("response.reasoning_summary_text.delta", fmt.Sprintf(`"output_index":0,"item_id":%q,"summary_index":0,"delta":"Thought"`, id(0, "delta"))))
	for _, i := range []int{1, 3, 1, 3} {
		b.WriteString(responseFrame("response.output_text.delta", fmt.Sprintf(`"output_index":%d,"item_id":%q,"content_index":0,"delta":"Checking","extension_number":9007199254740993`, i, id(i, "delta"))))
	}
	b.WriteString(responseFrame("response.function_call_arguments.delta", fmt.Sprintf(`"output_index":2,"item_id":%q,"delta":"{\"value\":1}"`, id(2, "delta"))))
	b.WriteString(responseFrame("response.function_call_arguments.done", fmt.Sprintf(`"output_index":2,"item_id":%q,"arguments":"{\"value\":1}"`, id(2, "args_done"))))
	for _, i := range []int{3, 1} {
		b.WriteString(responseFrame("response.output_text.done", fmt.Sprintf(`"output_index":%d,"item_id":%q,"text":"CheckingChecking"`, i, id(i, "text_done"))))
		b.WriteString(responseFrame("response.content_part.done", fmt.Sprintf(`"output_index":%d,"item_id":%q,"part":{"type":"output_text","text":"CheckingChecking"}`, i, id(i, "part_done"))))
	}
	for _, i := range []int{0, 3, 2, 1} {
		b.WriteString(responseFrame("response.output_item.done", fmt.Sprintf(`"output_index":%d,"item":%s`, i, item(i, "done"))))
	}
	b.WriteString("data: [DONE]\n\n") // Copilot can send this before the real terminal event.
	finalID := prefix + "_response_start"
	if drift {
		finalID = prefix + "_response_final"
	}
	b.WriteString(responseFrame(terminal, fmt.Sprintf(`"response":{"id":%q,"previous_response_id":"previous-original","status":%q,"output":[%s,%s,%s,%s],"error":{"code":"preserve-me"},"incomplete_details":{"reason":"max_output_tokens"}}`, finalID, strings.TrimPrefix(terminal, "response."), item(0, "final"), item(1, "final"), item(2, "final"), item(3, "final"))))
	return b.String()
}

// Decode independently of the production frame splitter, preserving numeric
// literals so that corruption of unknown/large numeric fields is detectable.
func responseEvents(t *testing.T, stream string) []map[string]interface{} {
	t.Helper()
	var events []map[string]interface{}
	var data []string
	consume := func() {
		joined := strings.Join(data, "\n")
		data = nil
		if strings.TrimSpace(joined) == "" || strings.TrimSpace(joined) == "[DONE]" {
			return
		}
		var event map[string]interface{}
		decoder := json.NewDecoder(strings.NewReader(joined))
		decoder.UseNumber()
		if err := decoder.Decode(&event); err != nil {
			t.Fatalf("decode test event: %v", err)
		}
		events = append(events, event)
	}
	scanner := bufio.NewScanner(strings.NewReader(stream))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			consume()
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	consume()
	return events
}

func TestResponsesIDs_NormalUpstreamByteExact(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.failed", "response.incomplete"} {
		for _, crlf := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/crlf=%v", terminal, crlf), func(t *testing.T) {
				input := responsesFixture("normal", terminal, false)
				// Exercise multiline JSON and SSE metadata in the normal path.
				input = strings.ReplaceAll(input, `,"output_index":`, ",\ndata: \"output_index\":")
				input = "id: transport-cursor\n" + input
				if crlf {
					input = strings.ReplaceAll(input, "\n", "\r\n")
				}
				if got := streamResponseHelper(t, input, "/responses"); got != input {
					t.Fatal("normal upstream must pass through byte-for-byte")
				}
			})
		}
	}
}

func TestResponsesIDs_LargeMultilineEventPassesThrough(t *testing.T) {
	text := strings.Repeat("x", 600*1024)
	input := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"final","output":[]},` + "\n" +
		`data: "first":` + fmt.Sprintf("%q,", text) + "\n" +
		`data: "second":` + fmt.Sprintf("%q", text) + "}\n\n"
	if got := streamResponseHelper(t, input, "/responses"); got != input {
		t.Fatal("valid multiline event exceeding 1 MiB was not preserved")
	}
}

func TestResponsesIDs_DriftOnlyChangesItemIDs(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.failed", "response.incomplete"} {
		t.Run(terminal, func(t *testing.T) {
			input := responsesFixture("drift", terminal, true)
			got := responseEvents(t, streamResponseHelper(t, input, "/responses"))
			want := responseEvents(t, input)
			for _, event := range want {
				if index, ok := event["output_index"].(json.Number); ok {
					canonical := "drift_item_" + index.String() + "_added"
					if _, ok := event["item_id"]; ok {
						event["item_id"] = canonical
					}
					if item, ok := event["item"].(map[string]interface{}); ok {
						item["id"] = canonical
					}
				}
				if response, ok := event["response"].(map[string]interface{}); ok {
					for i, raw := range response["output"].([]interface{}) {
						raw.(map[string]interface{})["id"] = fmt.Sprintf("drift_item_%d_added", i)
					}
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("normalization changed fields other than correlated item IDs\ngot: %#v\nwant: %#v", got, want)
			}
		})
	}
}

func TestResponsesIDs_DisabledAndOtherEndpoints(t *testing.T) {
	input := responsesFixture("disabled", "response.completed", true)
	for _, test := range []struct {
		endpoint string
		disabled bool
	}{
		{endpoint: "/responses", disabled: true},
		{endpoint: "/chat/completions"},
	} {
		t.Run(test.endpoint, func(t *testing.T) {
			// The legacy Chat Completions path terminates at [DONE].
			input := strings.ReplaceAll(input, "data: [DONE]\n\n", "")
			h := &Handler{DisableResponsesIDCompat: test.disabled}
			w := httptest.NewRecorder()
			if err := h.streamResponse(w, io.NopCloser(strings.NewReader(input)), test.endpoint); err != nil {
				t.Fatal(err)
			}
			if w.Body.String() != input {
				t.Fatal("disabled/other endpoint stream was modified")
			}
		})
	}
}

func TestResponsesIDs_UnknownAndMalformedEvents(t *testing.T) {
	input := ": comment\n\ndata: [DONE]\n\ndata: {bad-json}\n\n" +
		responseFrame("response.output_item.added", `"output_index":0,"item":{"id":"start","type":"message"}`)
	for _, index := range []string{"null", "-1", "0.5", `"0"`, "999999999999999999999"} {
		input += responseFrame("response.output_text.delta", `"output_index":`+index+`,"item_id":"unknown","delta":"keep"`)
	}
	input += responseFrame("response.output_text.delta", `"item_id":"no-index","delta":"keep"`) +
		responseFrame("extension.event", `"output_index":0,"item_id":"extension-id","data":"keep"`)
	if got := streamResponseHelper(t, input, "/responses"); got != input {
		t.Fatal("uncorrelated or malformed data must pass through unchanged")
	}
}

func TestResponsesIDs_IdentityConflictsDoNotJoinTools(t *testing.T) {
	for _, item := range []string{
		`{"id":"different-call","type":"function_call","call_id":"call-other"}`,
		`{"id":"different-type","type":"message"}`,
	} {
		input := responseFrame("response.output_item.added", `"output_index":0,"item":{"id":"first","type":"function_call","call_id":"call-first"}`) +
			responseFrame("response.output_item.done", `"output_index":0,"item":`+item) +
			responseFrame("response.function_call_arguments.delta", `"output_index":0,"item_id":"later","delta":"{}"`)
		if got := streamResponseHelper(t, input, "/responses"); got != input {
			t.Fatal("conflicting identities must not be merged")
		}
	}
}

func TestResponsesIDs_MultilineRewriteAndDataOnlyTermination(t *testing.T) {
	input := "event: response.output_item.added\r\ndata: {\"type\":\"response.output_item.added\",\r\ndata: \"output_index\":0,\"item\":{\"id\":\"first\",\"type\":\"message\"}}\r\n\r\n" +
		"id: cursor\r\n: keep\r\ndata: {\"type\":\"response.output_text.delta\",\r\ndata: \"output_index\":0,\"item_id\":\"later\",\"delta\":\"你好\"}\r\n\r\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"native-final\",\"output\":[]}}\r\n\r\n"
	got := streamResponseHelper(t, input+"data: should-not-arrive\n\n", "/responses")
	if !strings.Contains(got, "id: cursor\r\n: keep\r\n") || strings.Contains(got, "should-not-arrive") {
		t.Fatal("SSE metadata or data-only termination was lost")
	}
	events := responseEvents(t, got)
	if len(events) != 3 || events[1]["item_id"] != "first" || events[1]["delta"] != "你好" {
		t.Fatalf("incorrect multiline event rewrite: %#v", events)
	}
}

func TestResponsesIDs_AllTerminalEventsStopAfterFrame(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.failed", "response.incomplete", "error"} {
		t.Run(terminal, func(t *testing.T) {
			input := responseFrame(terminal, `"message":"original terminal payload","code":"original_code"`)
			if got := streamResponseHelper(t, input+"data: trailing-data\n\n", "/responses"); got != input {
				t.Fatalf("terminal event was lost or forwarded trailing bytes: %s", got)
			}
		})
	}
}

func TestResponsesIDs_RequestIsolationAndUpstreamRecovery(t *testing.T) {
	h := &Handler{}
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prefix := fmt.Sprintf("request-%d", i)
			input := responsesFixture(prefix, "response.completed", true)
			w := httptest.NewRecorder()
			if err := h.streamResponse(w, io.NopCloser(strings.NewReader(input)), "/responses"); err != nil {
				t.Error(err)
				return
			}
			if strings.Contains(w.Body.String(), "_item_1_final") || !strings.Contains(w.Body.String(), prefix+"_item_1_added") {
				t.Error("concurrent request used incorrect item identity")
			}
		}()
	}
	wg.Wait()
	// On the SAME handler, a repaired upstream must immediately return to byte
	// exact forwarding. No model-level or process-level 'broken provider' flag.
	input := responsesFixture("recovered", "response.completed", false)
	w := httptest.NewRecorder()
	if err := h.streamResponse(w, io.NopCloser(strings.NewReader(input)), "/responses"); err != nil {
		t.Fatal(err)
	}
	if w.Body.String() != input {
		t.Fatal("normal upstream remained rewritten after a previous drifting request")
	}
}

func TestResponsesIDs_ExplicitNewResponseResetsIndices(t *testing.T) {
	input := responseFrame("response.created", `"response":{"id":"one"}`) +
		responseFrame("response.output_item.added", `"output_index":0,"item":{"id":"one-item"}`) +
		responseFrame("response.created", `"response":{"id":"two"}`) +
		responseFrame("response.output_item.added", `"output_index":0,"item":{"id":"two-item"}`)
	if got := streamResponseHelper(t, input, "/responses"); got != input {
		t.Fatal("different response lifecycles were associated")
	}
}

func TestResponsesIDs_FlushesBeforeUpstreamCompletes(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	flushed := make(chan struct{}, 1)
	w := &notifyingResponseWriter{ResponseRecorder: httptest.NewRecorder(), flushed: flushed}
	finished := make(chan error, 1)
	go func() { finished <- streamResponses(w, reader) }()
	first := responseFrame("response.output_item.added", `"output_index":0,"item":{"id":"first"}`)
	if _, err := io.WriteString(writer, first); err != nil {
		t.Fatal(err)
	}
	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("first event was buffered until response completion")
	}
	writer.Close()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if w.Body.String() != first {
		t.Fatal("first event changed")
	}
}

type notifyingResponseWriter struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
}

func (w *notifyingResponseWriter) Flush() {
	w.ResponseRecorder.Flush()
	select {
	case w.flushed <- struct{}{}:
	default:
	}
}

type responseErrorReader struct{ err error }

func (r responseErrorReader) Read([]byte) (int, error) { return 0, r.err }

type responseErrorWriter struct{ *httptest.ResponseRecorder }

func (w responseErrorWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestResponsesIDs_IOFailuresAndEOF(t *testing.T) {
	w := httptest.NewRecorder()
	if err := streamResponses(w, responseErrorReader{io.ErrUnexpectedEOF}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read error hidden: %v", err)
	}
	if err := streamResponses(responseErrorWriter{w}, strings.NewReader("data: {}\n\n")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error hidden: %v", err)
	}
	if err := streamResponses(w, strings.NewReader("data: "+strings.Repeat("x", 1024*1024))); err == nil {
		t.Fatal("oversized frame should fail explicitly")
	}
	input := `data: {"type":"response.completed","response":{"id":"final","output":[]}}`
	if got := streamResponseHelper(t, input, "/responses"); got != input {
		t.Fatal("EOF event lost or synthesized an extra newline")
	}
}

func TestResponsesIDs_HistoryToolContinuationAndRestart(t *testing.T) {
	fixture := responsesFixture("replay", "response.completed", true)
	var wantRequest []byte
	var received []byte
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if wantRequest == nil {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, fixture)
			return
		}
		received = body
		if !bytes.Equal(body, wantRequest) {
			http.Error(w, "history or previous_response_id was modified", http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"id":"native-followup","output":[]}`)
	}))
	defer fake.Close()
	newHandler := func(disabled bool) *Handler {
		return &Handler{upstream: upstream.NewClient(testutil.TokenProvider{BaseURL: fake.URL}, nil), DisableResponsesIDCompat: disabled}
	}
	h := newHandler(false)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"any-model","stream":true,"input":"ping"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("initial stream failed: %d", w.Code)
	}
	events := responseEvents(t, w.Body.String())
	final := events[len(events)-1]["response"].(map[string]interface{})
	output := final["output"].([]interface{})
	if output[0].(map[string]interface{})["encrypted_content"] != "opaque-final-full-ciphertext==" {
		t.Fatal("final reasoning ciphertext was truncated or replaced")
	}
	callID := output[2].(map[string]interface{})["call_id"]
	if callID != "replay_call" || final["id"] != "replay_response_final" {
		t.Fatal("upstream call_id or final response.id was replaced")
	}
	input := append(output, map[string]interface{}{"type": "function_call_output", "call_id": callID, "output": "OK"})
	for _, stream := range []bool{false, true} {
		for _, previous := range []bool{false, true} {
			request := map[string]interface{}{"model": "any-model", "stream": stream, "input": input}
			if previous {
				request["previous_response_id"] = final["id"]
			}
			var err error
			wantRequest, err = json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			// A fresh Handler models a restart. Disabling compatibility must
			// also leave the saved history and native previous_response_id intact.
			for _, handler := range []*Handler{h, newHandler(false), newHandler(true)} {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(wantRequest)))
				if w.Code != http.StatusOK || !bytes.Equal(received, wantRequest) {
					t.Fatalf("saved history failed after restart/toggle: %d", w.Code)
				}
			}
		}
	}
}
