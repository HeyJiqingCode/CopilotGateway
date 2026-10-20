package anthropic

// AnthropicErrorResponse represents an error response from the Anthropic API
type AnthropicErrorResponse struct {
	Type  string         `json:"type"` // "error"
	Error AnthropicError `json:"error"`
}

// AnthropicError represents the error object in Anthropic API responses
type AnthropicError struct {
	Type    string `json:"type"`    // error type (required)
	Message string `json:"message"` // error message (required)
}

// Error type constants for Anthropic API
const (
	AnthropicErrorTypeInvalidRequest = "invalid_request_error"
	AnthropicErrorTypePermission     = "permission_error"
	AnthropicErrorTypeAPI            = "api_error"
)
