package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

type Client struct {
	storage   *TokenStorage
	mu        sync.RWMutex
	creds     *StoredCredentials
	refreshMu sync.Mutex // serializes refresh/device-flow operations

	// Network steps, replaceable in tests.
	fetchCopilotToken func(githubToken string) (*CopilotToken, error)
	startDeviceFlow   func() (*DeviceCodeResponse, error)
	pollDeviceFlow    func(deviceCode string, interval int, timeout time.Duration) (string, error)

	// pending is the background device flow started when no usable GitHub
	// authorization exists; guarded by refreshMu.
	pending *pendingDeviceFlow
}

// NewClient creates a new auth client
func NewClient(tokenDir string) (*Client, error) {
	storage, err := NewTokenStorage(tokenDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create token storage: %w", err)
	}

	creds, err := storage.LoadCredentials()
	if err != nil {
		return nil, fmt.Errorf("failed to load credentials: %w", err)
	}

	return &Client{
		storage:           storage,
		creds:             creds,
		fetchCopilotToken: GetCopilotToken,
		startDeviceFlow:   InitiateDeviceFlow,
		pollDeviceFlow:    PollForAccessToken,
	}, nil
}

// GetValidToken returns a valid Copilot token, performing authentication if necessary
func (c *Client) GetValidToken(ctx context.Context) (*CopilotToken, error) {
	// Fast path: check with read lock only
	if token := c.usableToken(); token != nil {
		return token, nil
	}

	// Slow path: serialize refreshes so only one goroutine does network I/O
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	// Re-check: another goroutine may have refreshed while we waited
	if token := c.usableToken(); token != nil {
		return token, nil
	}

	// Network calls happen here WITHOUT holding mu, so readers are not blocked.

	// If we have a GitHub token, try to refresh Copilot token
	c.mu.RLock()
	hasGitHubToken := c.creds.GitHubToken != ""
	c.mu.RUnlock()

	if hasGitHubToken {
		err := c.refreshCopilotToken()
		if err == nil {
			c.mu.RLock()
			token := c.creds.CopilotToken
			c.mu.RUnlock()
			return token, nil
		}
		slog.Error("failed to refresh copilot token with stored GitHub token", "error", err)
		// A transient failure keeps the stored token; only a rejected one
		// (cleared by refreshCopilotToken) means the user must sign in again.
		if !IsAuthStatusError(err) {
			return nil, err
		}
	}

	return nil, c.reauthRequired()
}

// usableToken returns the cached Copilot token unless it is near expiry.
func (c *Client) usableToken() *CopilotToken {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.creds.CopilotToken.IsTokenUsable() {
		return c.creds.CopilotToken
	}
	return nil
}

// GetToken returns a valid Copilot bearer token string. It satisfies the
// upstream.TokenProvider interface.
func (c *Client) GetToken(ctx context.Context) (string, error) {
	tok, err := c.GetValidToken(ctx)
	if err != nil {
		return "", err
	}
	return tok.Token, nil
}

// GetBaseURL returns the base URL for API calls
func (c *Client) GetBaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.creds.CopilotToken != nil {
		return c.creds.CopilotToken.BaseURL
	}
	return DefaultBaseURL
}

// Start gets the first Copilot token at startup without waiting for the user.
// Without usable GitHub authorization it prints the sign-in prompt to out; the
// device flow polls in the background and requests get a 403 carrying the same
// code until the user signs in. Other failures keep the stored token and are
// retried by the next request.
func (c *Client) Start(ctx context.Context, out io.Writer) {
	_, err := c.GetValidToken(ctx)
	var reauth *ReauthRequiredError
	if !errors.As(err, &reauth) || reauth.UserCode == "" {
		return
	}
	fmt.Fprintf(out, "\n🔐 GitHub Authentication Required\n")
	fmt.Fprintf(out, "Please visit: %s\n", reauth.VerificationURI)
	fmt.Fprintf(out, "Enter code: %s\n\n", reauth.UserCode)
	fmt.Fprintf(out, "Waiting for authorization in the background (code expires %s)...\n\n", reauth.ExpiresAt.UTC().Format("15:04 UTC"))
}

func (c *Client) refreshCopilotToken() error {
	start := time.Now()
	slog.Info("refreshing Copilot token")

	c.mu.RLock()
	githubToken := c.creds.GitHubToken
	c.mu.RUnlock()

	copilotToken, err := c.fetchCopilotToken(githubToken)
	if err != nil {
		if IsAuthStatusError(err) {
			slog.Warn("stored GitHub token is no longer accepted; clearing credentials")
			if clearErr := c.clearStoredTokens(); clearErr != nil {
				slog.Warn("failed to clear invalid credentials", "error", clearErr)
			}
		}
		return err
	}

	c.mu.Lock()
	c.creds.CopilotToken = copilotToken
	credsCopy := *c.creds
	c.mu.Unlock()

	// Save credentials to disk
	if err := c.storage.SaveCredentials(&credsCopy); err != nil {
		slog.Warn("failed to save credentials", "error", err)
	}

	slog.Info("copilot token refreshed", "expires_at", copilotToken.ExpiresAt, "base_url", copilotToken.BaseURL, "duration_ms", time.Since(start).Milliseconds())
	return nil
}

func (c *Client) clearStoredTokens() error {
	c.mu.Lock()
	c.creds.GitHubToken = ""
	c.creds.CopilotToken = nil
	credsCopy := *c.creds
	c.mu.Unlock()

	if err := c.storage.SaveCredentials(&credsCopy); err != nil {
		return fmt.Errorf("failed to save cleared credentials: %w", err)
	}
	return nil
}
