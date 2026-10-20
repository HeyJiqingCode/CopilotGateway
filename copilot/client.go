// Package copilot is everything the gateway does against GitHub Copilot:
// GitHub sign-in, Copilot tokens, API requests and the model catalog.
package copilot

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// TokenProvider supplies the Copilot token and API base URL; *Auth implements it.
type TokenProvider interface {
	// GetToken returns a valid bearer token for the upstream API.
	GetToken(ctx context.Context) (string, error)
	// GetBaseURL returns the base URL for the upstream API (e.g. "https://...").
	GetBaseURL() string
}

// Client makes requests to the upstream Copilot API.
type Client struct {
	TokenProvider TokenProvider
	HTTPClient    *http.Client
}

// NewTransport creates a shared http.Transport suitable for upstream requests.
// A custom dialer or TLS config disables Go's automatic HTTP/2, so it is
// requested explicitly: concurrent streams then share one Copilot connection.
func NewTransport() *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2:     true,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       120 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
	}
}

// NewClient creates a new upstream Client.
// If transport is non-nil it is used for the underlying http.Client;
// otherwise a new Transport is created.
func NewClient(tp TokenProvider, transport *http.Transport) *Client {
	if transport == nil {
		transport = NewTransport()
	}
	return &Client{
		TokenProvider: tp,
		HTTPClient: &http.Client{
			// No client-level Timeout — it kills long-running streaming requests.
			// Non-streaming requests use per-request context timeouts instead.
			Transport: transport,
		},
	}
}

// Request configures a single upstream request.
type Request struct {
	Method       string
	Endpoint     string
	Body         []byte            // nil for no body
	QueryString  string            // raw query string to append (without leading '?')
	Stream       bool              // if true, returns *http.Response instead of reading body
	ExtraHeaders map[string]string // additional headers to set after copilot headers
}

const (
	defaultNonStreamTimeout = 5 * time.Minute
	maxErrBody              = 1 << 20  // 1MB for error bodies
	maxRespBody             = 50 << 20 // 50MB for response bodies
	// Gateway resource guard on HTTP body bytes. Leave room above the observed
	// upstream limit so the upstream can reject requests with its own error.
	MaxRequestBody = 64 << 20 // 64 MiB; independent of model token limits
)

// Do executes a request against the upstream Copilot API. An upstream status
// of 400 or above becomes an *UpstreamError carrying the response body.
//
// Without Stream it returns the response body bytes. With Stream it returns
// the live *http.Response, whose body the caller must close.
func (c *Client) Do(ctx context.Context, r Request) (*http.Response, []byte, error) {
	token, err := c.TokenProvider.GetToken(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get valid token: %w", err)
	}

	var bodyReader io.Reader
	if r.Body != nil {
		bodyReader = bytes.NewReader(r.Body)
	}

	// Build URL
	baseURL := c.TokenProvider.GetBaseURL()
	upstreamURL := baseURL + r.Endpoint
	if r.QueryString != "" {
		upstreamURL += "?" + r.QueryString
	}

	// Apply timeout for non-streaming requests
	reqCtx := ctx
	var cancel context.CancelFunc
	if !r.Stream {
		reqCtx, cancel = context.WithTimeout(ctx, defaultNonStreamTimeout)
		defer cancel()
	}

	method := r.Method
	if method == "" {
		method = "POST"
	}

	req, err := http.NewRequestWithContext(reqCtx, method, upstreamURL, bodyReader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	AddHeaders(req, token)

	if r.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}

	for k, v := range r.ExtraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxErrBody)+1))
		if len(errBody) > maxErrBody {
			return nil, nil, fmt.Errorf("upstream error response too large (exceeds %d bytes)", maxErrBody)
		}
		return nil, nil, &UpstreamError{
			StatusCode: resp.StatusCode,
			Body:       errBody,
		}
	}

	if r.Stream {
		return resp, nil, nil
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxRespBody)+1))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response: %w", err)
	}
	if len(respData) > maxRespBody {
		return nil, nil, fmt.Errorf("upstream response too large (exceeds %d bytes)", maxRespBody)
	}
	return nil, respData, nil
}
