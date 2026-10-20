// Package testutil holds test doubles shared by several packages' tests.
package testutil

import "context"

// TokenProvider is a copilot.TokenProvider with a fixed token and base URL.
type TokenProvider struct {
	BaseURL string
	Token   string // "test-token" when empty
	Err     error  // returned by GetToken instead of a token
}

func (p TokenProvider) GetToken(context.Context) (string, error) {
	if p.Err != nil {
		return "", p.Err
	}
	if p.Token == "" {
		return "test-token", nil
	}
	return p.Token, nil
}

func (p TokenProvider) GetBaseURL() string { return p.BaseURL }
