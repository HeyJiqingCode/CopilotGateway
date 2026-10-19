package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newTestClient(t *testing.T, githubToken string) *Client {
	t.Helper()
	c, err := NewClient(t.TempDir())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.creds.GitHubToken = githubToken
	return c
}

func TestGetValidToken_NoGitHubTokenReturnsReauthRequired(t *testing.T) {
	c := newTestClient(t, "")

	_, err := c.GetValidToken(context.Background())

	var reauth *ReauthRequiredError
	if !errors.As(err, &reauth) {
		t.Fatalf("GetValidToken error = %v, want *ReauthRequiredError", err)
	}
}

func TestGetValidToken_RevokedGitHubTokenReturnsReauthRequired(t *testing.T) {
	c := newTestClient(t, "gho_revoked")
	c.fetchCopilotToken = func(string) (*CopilotToken, error) {
		return nil, &CopilotTokenStatusError{StatusCode: 401}
	}

	_, err := c.GetValidToken(context.Background())

	var reauth *ReauthRequiredError
	if !errors.As(err, &reauth) {
		t.Fatalf("GetValidToken error = %v, want *ReauthRequiredError", err)
	}
	if c.creds.GitHubToken != "" {
		t.Fatalf("revoked GitHub token was kept")
	}
}

func TestGetValidToken_TransientRefreshErrorIsNotReauth(t *testing.T) {
	c := newTestClient(t, "gho_valid")
	c.fetchCopilotToken = func(string) (*CopilotToken, error) {
		return nil, errors.New("dial tcp: i/o timeout")
	}

	_, err := c.GetValidToken(context.Background())

	var reauth *ReauthRequiredError
	if err == nil || errors.As(err, &reauth) {
		t.Fatalf("GetValidToken error = %v, want a plain refresh error", err)
	}
	if c.creds.GitHubToken != "gho_valid" {
		t.Fatalf("GitHub token was cleared on a transient error")
	}
}

func TestReauthRequiredError_MessageCarriesDeviceCode(t *testing.T) {
	err := &ReauthRequiredError{
		VerificationURI: "https://github.com/login/device",
		UserCode:        "ABCD-1234",
		ExpiresAt:       time.Date(2026, 10, 19, 12, 30, 0, 0, time.UTC),
	}

	want := "GitHub Copilot authorization lost. Visit https://github.com/login/device and enter code ABCD-1234 (expires 12:30 UTC) to restore the gateway."
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
