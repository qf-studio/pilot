// Package anthropic provides a shared request builder for the Anthropic
// Messages API (https://api.anthropic.com/v1/messages), used across every
// Pilot call site that talks to Claude. Building requests through a single
// typed constructor keeps required fields (like max_tokens) impossible to
// omit and unsupported ones (like a top-level output_config/effort) impossible
// to add by accident — both have shipped as 400-error regressions before
// (PR #3700, #3703).
package anthropic

import "fmt"

// Message is one turn in an Anthropic Messages API conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is the JSON payload for a Messages API call. Construct one via
// NewRequest so the API's required fields are validated at construction time
// rather than discovered as a 400 at request time.
type Request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
}

// NewRequest builds a Request. max_tokens is mandatory on every Messages API
// call — omitting it returns HTTP 400 "max_tokens: Field required" (PR #3700).
func NewRequest(model string, maxTokens int, messages []Message) (*Request, error) {
	if model == "" {
		return nil, fmt.Errorf("anthropic: model must not be empty")
	}
	if maxTokens <= 0 {
		return nil, fmt.Errorf("anthropic: max_tokens must be > 0, got %d", maxTokens)
	}
	return &Request{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  messages,
	}, nil
}

// WithSystem sets the system prompt and returns the Request for chaining.
func (r *Request) WithSystem(system string) *Request {
	r.System = system
	return r
}
