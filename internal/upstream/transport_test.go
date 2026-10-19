package upstream

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Copilot serves HTTP/2; the gateway's custom transport must negotiate it
// instead of silently falling back to HTTP/1.1.
func TestNewTransportNegotiatesHTTP2(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	tr := NewTransport()
	tr.TLSClientConfig.RootCAs = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	resp, err := (&http.Client{Transport: tr}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.Proto != "HTTP/2.0" {
		t.Fatalf("protocol = %s, want HTTP/2.0", resp.Proto)
	}
}
