package auth

import (
	"fmt"
	"log/slog"
	"time"
)

// ReauthRequiredError reports that the stored GitHub authorization is gone
// (missing or rejected), so no Copilot token can be issued until the user
// signs in. Handlers turn it into a non-retryable 403.
type ReauthRequiredError struct {
	VerificationURI string
	UserCode        string
	ExpiresAt       time.Time
	// Cause explains why no device code is available, if any.
	Cause error
}

func (e *ReauthRequiredError) Error() string {
	if e.UserCode != "" {
		return fmt.Sprintf("GitHub Copilot authorization required. Visit %s and enter code %s (expires %s) to authorize the gateway.",
			e.VerificationURI, e.UserCode, e.ExpiresAt.UTC().Format("15:04 UTC"))
	}
	msg := "GitHub Copilot authorization required. Send another request to get a sign-in code."
	if e.Cause != nil {
		msg += " Device flow could not start: " + e.Cause.Error()
	}
	return msg
}

func (e *ReauthRequiredError) Unwrap() error { return e.Cause }

type pendingDeviceFlow struct {
	resp      *DeviceCodeResponse
	expiresAt time.Time
}

func (p *pendingDeviceFlow) reauthError() *ReauthRequiredError {
	return &ReauthRequiredError{VerificationURI: p.resp.VerificationURI, UserCode: p.resp.UserCode, ExpiresAt: p.expiresAt}
}

// reauthRequired reuses the live device flow or starts a new one in the
// background, so requests can show the user how to sign in again without a
// restart. Callers must hold refreshMu.
func (c *Client) reauthRequired() *ReauthRequiredError {
	if p := c.pending; p != nil && time.Now().Before(p.expiresAt) {
		return p.reauthError()
	}

	resp, err := c.startDeviceFlow()
	if err != nil {
		slog.Error("failed to start device flow for GitHub sign-in", "error", err)
		return &ReauthRequiredError{Cause: err}
	}
	p := &pendingDeviceFlow{resp: resp, expiresAt: time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)}
	c.pending = p
	slog.Warn("GitHub authorization required; waiting for device flow sign-in",
		"verification_uri", resp.VerificationURI, "user_code", resp.UserCode, "expires_at", p.expiresAt)
	go c.awaitDeviceFlow(p)
	return p.reauthError()
}

// awaitDeviceFlow polls without holding refreshMu, then installs the new
// GitHub token. A failed or expired flow is dropped; the next request
// starts a fresh one.
func (c *Client) awaitDeviceFlow(p *pendingDeviceFlow) {
	githubToken, err := c.pollDeviceFlow(p.resp.DeviceCode, p.resp.Interval, time.Until(p.expiresAt))

	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if c.pending == p {
		c.pending = nil
	}
	if err != nil {
		slog.Warn("device flow sign-in did not complete", "user_code", p.resp.UserCode, "error", err)
		return
	}

	c.mu.Lock()
	c.creds.GitHubToken = githubToken
	c.mu.Unlock()
	if err := c.refreshCopilotToken(); err != nil {
		slog.Error("failed to get copilot token after device flow sign-in", "error", err)
		return
	}
	slog.Info("GitHub authorization completed")
}
