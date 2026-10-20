package anthropic

// errorResponse represents an error response from the Anthropic API
type errorResponse struct {
	Type  string      `json:"type"` // "error"
	Error errorDetail `json:"error"`
}

// errorDetail represents the error object in Anthropic API responses
type errorDetail struct {
	Type    string `json:"type"`    // error type (required)
	Message string `json:"message"` // error message (required)
}

// Error type constants for Anthropic API
const (
	errTypeInvalidRequest = "invalid_request_error"
	errTypePermission     = "permission_error"
	errTypeAPI            = "api_error"
)
