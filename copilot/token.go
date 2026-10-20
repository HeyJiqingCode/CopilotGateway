package copilot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	TokenURL       = "https://api.github.com/copilot_internal/v2/token"
	DefaultBaseURL = "https://api.individual.githubcopilot.com"
)

type TokenStatusError struct {
	StatusCode int
}

func (e *TokenStatusError) Error() string {
	return fmt.Sprintf("copilot token request failed with status %d", e.StatusCode)
}

func IsAuthStatusError(err error) bool {
	var statusErr *TokenStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden
}

// sharedHTTPClient is reused by the GitHub sign-in and token requests to pool connections.
var sharedHTTPClient = &http.Client{Timeout: 30 * time.Second}

type tokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

type Token struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	BaseURL   string    `json:"base_url"`
}

// FetchToken retrieves a Copilot token using the GitHub access token
func FetchToken(accessToken string) (*Token, error) {
	req, err := http.NewRequest("GET", TokenURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", UserAgent)

	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get copilot token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &TokenStatusError{StatusCode: resp.StatusCode}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	// Parse expires_at (can be seconds or milliseconds)
	expiresAt := time.Unix(tokenResp.ExpiresAt, 0)
	if tokenResp.ExpiresAt > 1e10 {
		// Treat as milliseconds
		expiresAt = time.Unix(0, tokenResp.ExpiresAt*int64(time.Millisecond))
	}

	// Extract base URL from token
	baseURL := ExtractBaseURLFromToken(tokenResp.Token)

	return &Token{
		Token:     tokenResp.Token,
		ExpiresAt: expiresAt,
		BaseURL:   baseURL,
	}, nil
}

// ExtractBaseURLFromToken parses the proxy-ep from the token and derives the API base URL
func ExtractBaseURLFromToken(token string) string {
	// Token format: "tid=...;exp=...;sku=...;proxy-ep=proxy.individual.githubcopilot.com;..."
	parts := strings.Split(token, ";")
	for _, part := range parts {
		if strings.HasPrefix(part, "proxy-ep=") {
			proxyEP := strings.TrimPrefix(part, "proxy-ep=")
			// Replace proxy. prefix with api.
			if strings.HasPrefix(proxyEP, "proxy.") {
				apiEP := "api." + strings.TrimPrefix(proxyEP, "proxy.")
				return "https://" + apiEP
			}
		}
	}

	// Fallback to default
	slog.Warn("no proxy-ep found in token, using default base URL", "default", DefaultBaseURL)
	return DefaultBaseURL
}

// IsTokenUsable checks if token is valid and has at least 5 minutes before expiry
func (t *Token) IsTokenUsable() bool {
	if t == nil || t.Token == "" {
		return false
	}

	// Token is considered usable if it expires in more than 5 minutes
	return time.Until(t.ExpiresAt) > 5*time.Minute
}
