package copilot

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// start runs c.Start and fails if it waits for the user instead of returning.
func start(t *testing.T, c *Auth) string {
	t.Helper()
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		c.Start(context.Background(), &out)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start blocked waiting for device flow sign-in")
	}
	return out.String()
}

func TestStart_WithoutGitHubTokenPromptsAndKeepsServing(t *testing.T) {
	c := newTestAuth(t, "")
	f := installFakeDeviceFlow(c)
	defer func() { f.results <- pollResult{err: errors.New("test done")} }()

	out := start(t, c)

	if !strings.Contains(out, "Please visit: https://github.com/login/device") || !strings.Contains(out, "Enter code: CODE-0001") {
		t.Fatalf("startup output = %q, want the sign-in link and code", out)
	}
	// Requests get the same code; startup did not open a second flow.
	if code := reauthCode(t, c); code != "CODE-0001" {
		t.Fatalf("request code = %q, want CODE-0001", code)
	}
	if n := f.startCount(); n != 1 {
		t.Fatalf("device flow started %d times, want 1", n)
	}
}

func TestStart_RevokedGitHubTokenPromptsForSignIn(t *testing.T) {
	c := newTestAuth(t, "gho_revoked")
	f := installFakeDeviceFlow(c)
	defer func() { f.results <- pollResult{err: errors.New("test done")} }()
	c.fetchCopilotToken = func(string) (*Token, error) {
		return nil, &TokenStatusError{StatusCode: 401}
	}

	out := start(t, c)

	if !strings.Contains(out, "Enter code: CODE-0001") {
		t.Fatalf("startup output = %q, want the sign-in code", out)
	}
	if c.creds.GitHubToken != "" {
		t.Fatalf("revoked GitHub token was kept")
	}
}

func TestStart_TransientErrorKeepsTokenWithoutPrompt(t *testing.T) {
	c := newTestAuth(t, "gho_valid")
	f := installFakeDeviceFlow(c)
	c.fetchCopilotToken = func(string) (*Token, error) {
		return nil, errors.New("dial tcp: i/o timeout")
	}

	out := start(t, c)

	if strings.Contains(out, "Enter code") {
		t.Fatalf("startup output = %q, want no sign-in prompt for a network error", out)
	}
	if c.creds.GitHubToken != "gho_valid" || f.startCount() != 0 {
		t.Fatalf("GitHub token = %q, device flows = %d; want the token kept and no flow", c.creds.GitHubToken, f.startCount())
	}
}

func TestStart_ValidTokenPrintsNothing(t *testing.T) {
	c := newTestAuth(t, "gho_valid")
	c.fetchCopilotToken = func(string) (*Token, error) {
		return &Token{Token: "copilot", ExpiresAt: time.Now().Add(30 * time.Minute), BaseURL: DefaultBaseURL}, nil
	}

	if out := start(t, c); out != "" {
		t.Fatalf("startup output = %q, want none", out)
	}
}
