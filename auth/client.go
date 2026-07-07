package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/whtsky/copilot2api/internal/copilot"
)

type Client struct {
	storage   *TokenStorage
	mu        sync.RWMutex
	creds     *StoredCredentials
	refreshMu sync.Mutex // serializes refresh/device-flow operations
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
		storage: storage,
		creds:   creds,
	}, nil
}

// GetValidToken returns a valid Copilot token, performing authentication if necessary
func (c *Client) GetValidToken(ctx context.Context) (*CopilotToken, error) {
	// Fast path: check with read lock only
	c.mu.RLock()
	if c.creds.CopilotToken != nil && c.creds.CopilotToken.IsTokenUsable() {
		token := c.creds.CopilotToken
		c.mu.RUnlock()
		return token, nil
	}
	c.mu.RUnlock()

	// Slow path: serialize refreshes so only one goroutine does network I/O
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	// Re-check: another goroutine may have refreshed while we waited
	c.mu.RLock()
	if c.creds.CopilotToken != nil && c.creds.CopilotToken.IsTokenUsable() {
		token := c.creds.CopilotToken
		c.mu.RUnlock()
		return token, nil
	}
	c.mu.RUnlock()

	// Network calls happen here WITHOUT holding mu, so readers are not blocked.

	// If we have a GitHub token, try to refresh Copilot token
	c.mu.RLock()
	hasGitHubToken := c.creds.GitHubToken != ""
	c.mu.RUnlock()

	if hasGitHubToken {
		if err := c.refreshCopilotToken(); err == nil {
			c.mu.RLock()
			token := c.creds.CopilotToken
			c.mu.RUnlock()
			return token, nil
		} else {
			slog.Error("failed to refresh copilot token with stored GitHub token", "error", err)
		}
	}

	// Device flow is interactive and must not block request serving.
	// It should only be triggered at startup via RunDeviceFlowIfNeeded.
	return nil, fmt.Errorf("authentication required: no valid GitHub token available (run device flow at startup)")
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

// EnsureAuthenticated runs the interactive device flow if needed and verifies
// that a valid Copilot token can be obtained. Call this at startup only.
func (c *Client) EnsureAuthenticated(ctx context.Context) error {
	if err := c.RunDeviceFlowIfNeeded(); err != nil {
		return fmt.Errorf("device flow failed: %w", err)
	}
	if _, err := c.GetValidToken(ctx); err != nil {
		return fmt.Errorf("failed to obtain valid token: %w", err)
	}
	return nil
}

// RunDeviceFlowIfNeeded performs the interactive OAuth device flow if no
// valid GitHub token is stored. This must be called at startup — not during
// request serving, since it blocks waiting for user interaction.
func (c *Client) RunDeviceFlowIfNeeded() error {
	c.mu.RLock()
	hasGitHubToken := c.creds.GitHubToken != ""
	c.mu.RUnlock()

	if hasGitHubToken {
		return nil // already have a token, refresh path will handle expiry
	}

	return c.performDeviceFlow()
}

func (c *Client) performDeviceFlow() error {
	slog.Info("starting GitHub Device Flow OAuth")

	// Step 1: Get device code
	deviceResp, err := InitiateDeviceFlow()
	if err != nil {
		return fmt.Errorf("failed to initiate device flow: %w", err)
	}

	// Step 2: Display user code
	fmt.Printf("\n🔐 GitHub Authentication Required\n")
	fmt.Printf("Please visit: %s\n", deviceResp.VerificationURI)
	fmt.Printf("Enter code: %s\n\n", deviceResp.UserCode)
	fmt.Printf("Waiting for authorization...")

	// Step 3: Poll for access token
	timeout := time.Duration(deviceResp.ExpiresIn) * time.Second
	accessToken, err := PollForAccessToken(deviceResp.DeviceCode, deviceResp.Interval, timeout)
	if err != nil {
		return fmt.Errorf("failed to get access token: %w", err)
	}

	fmt.Printf("\n✅ Authentication successful!\n\n")

	// Store GitHub token
	c.mu.Lock()
	c.creds.GitHubToken = accessToken
	c.mu.Unlock()

	// Get Copilot token
	if err := c.refreshCopilotToken(); err != nil {
		return fmt.Errorf("failed to get copilot token: %w", err)
	}

	return nil
}

func (c *Client) refreshCopilotToken() error {
	start := time.Now()
	slog.Info("refreshing Copilot token")

	c.mu.RLock()
	githubToken := c.creds.GitHubToken
	c.mu.RUnlock()

	copilotToken, err := GetCopilotToken(githubToken)
	if err != nil {
		return fmt.Errorf("failed to get copilot token: %w", err)
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

// UsageInfo contains Copilot usage and quota information
type UsageInfo struct {
	Login                 string                   `json:"login,omitempty"`
	SKU                   string                   `json:"sku,omitempty"`
	AccessTypeSKU         string                   `json:"access_type_sku,omitempty"`
	CopilotPlan           string                   `json:"copilot_plan,omitempty"`
	TokenBasedBilling     bool                     `json:"token_based_billing"`
	QuotaResetDate        string                   `json:"quota_reset_date,omitempty"`
	QuotaResetDateUTC     string                   `json:"quota_reset_date_utc,omitempty"`
	QuotaSnapshots        map[string]QuotaSnapshot `json:"quota_snapshots,omitempty"`
	OrganizationList      []string                 `json:"organization_list,omitempty"`
	OrganizationLoginList []string                 `json:"organization_login_list,omitempty"`
	EnterpriseList        []int                    `json:"enterprise_list,omitempty"`
	LimitedUserQuotas     interface{}              `json:"limited_user_quotas,omitempty"`
	LimitedUserResetDate  interface{}              `json:"limited_user_reset_date,omitempty"`
}

// QuotaSnapshot 描述 Copilot 返回的单类额度快照,例如 chat 或 premium_interactions。
type QuotaSnapshot struct {
	QuotaID            string   `json:"quota_id,omitempty"`
	Entitlement        float64  `json:"entitlement"`
	Remaining          float64  `json:"remaining"`
	QuotaRemaining     float64  `json:"quota_remaining"`
	PercentRemaining   *float64 `json:"percent_remaining,omitempty"`
	Unlimited          bool     `json:"unlimited"`
	HasQuota           bool     `json:"has_quota"`
	TokenBasedBilling  bool     `json:"token_based_billing"`
	OveragePermitted   bool     `json:"overage_permitted"`
	OverageCount       float64  `json:"overage_count"`
	OverageEntitlement float64  `json:"overage_entitlement"`
	QuotaResetAt       int64    `json:"quota_reset_at,omitempty"`
	TimestampUTC       string   `json:"timestamp_utc,omitempty"`
}

const CopilotUserInfoURL = "https://api.github.com/copilot_internal/user"

// GetUsageInfo fetches Copilot plan and quota information.
func (c *Client) GetUsageInfo(ctx context.Context) (*UsageInfo, error) {
	c.mu.RLock()
	githubToken := c.creds.GitHubToken
	c.mu.RUnlock()

	if githubToken == "" {
		return nil, fmt.Errorf("no GitHub token available")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", CopilotUserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+githubToken)
	req.Header.Set("User-Agent", copilot.CopilotUserAgent)
	req.Header.Set("X-GitHub-Api-Version", "2025-04-01")
	req.Header.Set("Accept", "application/json")

	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usage request failed with status %d", resp.StatusCode)
	}

	var info UsageInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	normalizeUsageInfo(&info)

	return &info, nil
}

// normalizeUsageInfo 补齐兼容字段,避免调用方只认旧的 sku 字段。
func normalizeUsageInfo(info *UsageInfo) {
	if info != nil && info.SKU == "" {
		info.SKU = info.AccessTypeSKU
	}
}
