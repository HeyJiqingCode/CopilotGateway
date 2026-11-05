package openai

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HeyJiqingCode/CopilotGateway/copilot"
	"github.com/HeyJiqingCode/CopilotGateway/testutil"
)

type upstreamCall struct {
	path        string
	contentType string
	body        []byte
}

// imageUpstream answers every request with status, contentType and body, and
// records what it received.
func imageUpstream(t *testing.T, got *upstreamCall, status int, contentType, body string) *Handler {
	t.Helper()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*got = upstreamCall{path: r.URL.Path, contentType: r.Header.Get("Content-Type"), body: raw}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(fake.Close)
	uc := copilot.NewClient(testutil.TokenProvider{BaseURL: fake.URL}, nil)
	return NewHandler(uc, copilot.NewModels(uc, time.Minute))
}

func serve(h *Handler, path, contentType string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestImages_GenerationsPassThrough(t *testing.T) {
	var got upstreamCall
	result := `{"created":1,"data":[{"b64_json":"iVBORw0KGgo="}],"output_format":"png"}`
	h := imageUpstream(t, &got, http.StatusOK, "application/json", result)

	body := []byte(`{"model":"gpt-image-2.5-flare", "prompt":"a red dot","size":"1024x1024"}`)
	rec := serve(h, "/v1/images/generations", "application/json", body)

	if got.path != "/v1/images/generations" || !bytes.Equal(got.body, body) {
		t.Fatalf("upstream got %s %s, want /v1/images/generations with the original body", got.path, got.body)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != result {
		t.Fatalf("client got %d %s, want 200 with the upstream body", rec.Code, rec.Body.String())
	}
}

func TestImages_EditsForwardMultipartUnchanged(t *testing.T) {
	var got upstreamCall
	h := imageUpstream(t, &got, http.StatusOK, "application/json", `{"data":[]}`)

	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	mw.WriteField("model", "gpt-image-2.5-flare")
	mw.WriteField("prompt", "make the background darker")
	part, _ := mw.CreateFormFile("image", "icon.png")
	part.Write([]byte("\x89PNG\r\n\x1a\n binary image bytes"))
	mw.Close()

	rec := serve(h, "/v1/images/edits", mw.FormDataContentType(), form.Bytes())

	if got.path != "/v1/images/edits" || got.contentType != mw.FormDataContentType() || !bytes.Equal(got.body, form.Bytes()) {
		t.Fatalf("upstream got %s [%s] %d bytes, want /v1/images/edits with the original multipart body and boundary",
			got.path, got.contentType, len(got.body))
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("client got %d %s", rec.Code, rec.Body.String())
	}
}

func TestImages_StreamRelaysLinesOverOneMiB(t *testing.T) {
	var got upstreamCall
	partial := strings.Repeat("A", 2<<20) // a base64 image is one SSE line of several MiB
	stream := "event: image_generation.partial_image\ndata: {\"b64_json\":\"" + partial + "\"}\n\n" +
		"event: image_generation.completed\ndata: {\"b64_json\":\"" + partial + "\"}\n\n"
	h := imageUpstream(t, &got, http.StatusOK, "text/event-stream", stream)

	rec := serve(h, "/v1/images/generations", "application/json",
		[]byte(`{"model":"gpt-image-2.5-flare","prompt":"a red dot","stream":true,"partial_images":1}`))

	if rec.Code != http.StatusOK || rec.Body.String() != stream {
		t.Fatalf("client got %d and %d bytes, want 200 and the %d-byte upstream stream", rec.Code, rec.Body.Len(), len(stream))
	}
}

func TestImages_UpstreamErrorPassesThrough(t *testing.T) {
	var got upstreamCall
	upstreamErr := `{"error":{"message":"The requested model is not supported.","code":"model_not_supported"}}`
	h := imageUpstream(t, &got, http.StatusBadRequest, "application/json", upstreamErr)

	rec := serve(h, "/v1/images/generations", "application/json", []byte(`{"model":"nope","prompt":"x"}`))

	if rec.Code != http.StatusBadRequest || rec.Body.String() != upstreamErr {
		t.Fatalf("client got %d %s, want the upstream 400 unchanged", rec.Code, rec.Body.String())
	}
}
