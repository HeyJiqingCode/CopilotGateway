package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
	// Never reach GitHub from tests; tests that need these steps install fakes.
	c.fetchCopilotToken = func(string) (*CopilotToken, error) {
		return nil, errors.New("copilot token fetch disabled in tests")
	}
	c.startDeviceFlow = func() (*DeviceCodeResponse, error) {
		return nil, errors.New("device flow disabled in tests")
	}
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

	want := "GitHub Copilot authorization required. Visit https://github.com/login/device and enter code ABCD-1234 (expires 12:30 UTC) to authorize the gateway."
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestReauthRequiredError_WithoutCodeAsksForAnotherRequest(t *testing.T) {
	// The next request starts a new device flow, so a restart is never needed.
	err := &ReauthRequiredError{Cause: errors.New("device code request failed with status 503")}

	want := "GitHub Copilot authorization required. Send another request to get a sign-in code. Device flow could not start: device code request failed with status 503"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// fakeDeviceFlow scripts the GitHub device flow: each start issues the next
// code, and polls block until the test releases a result.
type fakeDeviceFlow struct {
	mu      sync.Mutex
	starts  int
	results chan pollResult
}

type pollResult struct {
	token string
	err   error
}

func installFakeDeviceFlow(c *Client) *fakeDeviceFlow {
	f := &fakeDeviceFlow{results: make(chan pollResult, 1)}
	c.startDeviceFlow = func() (*DeviceCodeResponse, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.starts++
		return &DeviceCodeResponse{
			DeviceCode:      fmt.Sprintf("device-%d", f.starts),
			UserCode:        fmt.Sprintf("CODE-%04d", f.starts),
			VerificationURI: "https://github.com/login/device",
			ExpiresIn:       900,
			Interval:        5,
		}, nil
	}
	c.pollDeviceFlow = func(string, int, time.Duration) (string, error) {
		r := <-f.results
		return r.token, r.err
	}
	return f
}

func (f *fakeDeviceFlow) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func reauthCode(t *testing.T, c *Client) string {
	t.Helper()
	_, err := c.GetValidToken(context.Background())
	var reauth *ReauthRequiredError
	if !errors.As(err, &reauth) {
		t.Fatalf("GetValidToken error = %v, want *ReauthRequiredError", err)
	}
	return reauth.UserCode
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGetValidToken_ReusesOneDeviceFlowCode(t *testing.T) {
	c := newTestClient(t, "")
	f := installFakeDeviceFlow(c)

	first, second := reauthCode(t, c), reauthCode(t, c)

	if first != "CODE-0001" || second != first {
		t.Fatalf("codes = %q, %q; want CODE-0001 twice", first, second)
	}
	if n := f.startCount(); n != 1 {
		t.Fatalf("device flow started %d times, want 1", n)
	}
	f.results <- pollResult{err: errors.New("test done")}
}

func TestGetValidToken_RecoversAfterDeviceFlowCompletes(t *testing.T) {
	c := newTestClient(t, "")
	f := installFakeDeviceFlow(c)
	c.fetchCopilotToken = func(githubToken string) (*CopilotToken, error) {
		if githubToken != "ghu_new" {
			return nil, &CopilotTokenStatusError{StatusCode: 401}
		}
		return &CopilotToken{Token: "copilot-new", ExpiresAt: time.Now().Add(30 * time.Minute), BaseURL: DefaultBaseURL}, nil
	}

	reauthCode(t, c)
	f.results <- pollResult{token: "ghu_new"}

	waitFor(t, "recovered token", func() bool {
		tok, err := c.GetValidToken(context.Background())
		return err == nil && tok.Token == "copilot-new"
	})
	saved, err := c.storage.LoadCredentials()
	if err != nil || saved.GitHubToken != "ghu_new" {
		t.Fatalf("saved GitHub token = %q (err %v), want ghu_new", saved.GitHubToken, err)
	}
}

func TestGetValidToken_RestartsDeviceFlowAfterFailure(t *testing.T) {
	c := newTestClient(t, "")
	f := installFakeDeviceFlow(c)

	reauthCode(t, c)
	f.results <- pollResult{err: errors.New("token error: expired_token")}

	waitFor(t, "a fresh device code", func() bool { return reauthCode(t, c) == "CODE-0002" })
	f.results <- pollResult{err: errors.New("test done")}
}

func TestGetValidToken_DeviceFlowStartFailureIsReported(t *testing.T) {
	c := newTestClient(t, "")
	c.startDeviceFlow = func() (*DeviceCodeResponse, error) {
		return nil, errors.New("device code request failed with status 503")
	}

	_, err := c.GetValidToken(context.Background())

	var reauth *ReauthRequiredError
	if !errors.As(err, &reauth) || reauth.UserCode != "" || reauth.Cause == nil {
		t.Fatalf("GetValidToken error = %#v, want *ReauthRequiredError with Cause and no code", err)
	}
}
