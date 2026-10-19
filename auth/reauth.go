package auth

import (
	"fmt"
	"time"
)

// ReauthRequiredError reports that the stored GitHub authorization is gone
// (missing or rejected), so no Copilot token can be issued until the user
// signs in again. Handlers turn it into a non-retryable 403.
type ReauthRequiredError struct {
	VerificationURI string
	UserCode        string
	ExpiresAt       time.Time
	// Cause explains why no device code is available, if any.
	Cause error
}

func (e *ReauthRequiredError) Error() string {
	if e.UserCode != "" {
		return fmt.Sprintf("GitHub Copilot authorization lost. Visit %s and enter code %s (expires %s) to restore the gateway.",
			e.VerificationURI, e.UserCode, e.ExpiresAt.UTC().Format("15:04 UTC"))
	}
	msg := "GitHub Copilot authorization lost. Restart the gateway to sign in again."
	if e.Cause != nil {
		msg += " Device flow could not start: " + e.Cause.Error()
	}
	return msg
}

func (e *ReauthRequiredError) Unwrap() error { return e.Cause }
