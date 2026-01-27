// Package approval provides human approval workflows for task execution stages.
package approval

import (
	"context"
	"time"
)

// Stage represents the execution stage requiring approval
type Stage string

const (
	// StagePreExecution requires approval before task execution starts
	StagePreExecution Stage = "pre_execution"

	// StagePreMerge requires approval before PR merge/auto-merge
	StagePreMerge Stage = "pre_merge"

	// StagePostFailure requires approval to retry or escalate after failure
	StagePostFailure Stage = "post_failure"
)

// Decision represents the user's approval decision
type Decision string

const (
	DecisionApproved Decision = "approved"
	DecisionRejected Decision = "rejected"
	DecisionTimeout  Decision = "timeout"
)

// Request represents an approval request
type Request struct {
	ID          string                 // Unique request identifier
	TaskID      string                 // Associated task ID
	Stage       Stage                  // Approval stage
	Title       string                 // Short description
	Description string                 // Detailed description
	Metadata    map[string]interface{} // Additional context (PR URL, error details, etc.)
	CreatedAt   time.Time              // When request was created
	ExpiresAt   time.Time              // When request expires (timeout)
	Approvers   []string               // Required approvers (user IDs, handles)
}

// Response represents an approval response
type Response struct {
	RequestID   string    // ID of the request being responded to
	Decision    Decision  // The decision made
	ApprovedBy  string    // Who approved/rejected
	Comment     string    // Optional comment
	RespondedAt time.Time // When response was given
}

// Handler is the interface for approval channel handlers
// Each channel (Telegram, Slack, etc.) implements this interface
type Handler interface {
	// SendApprovalRequest sends an approval request to the channel
	// Returns a channel that receives the response when user responds
	SendApprovalRequest(ctx context.Context, req *Request) (<-chan *Response, error)

	// CancelRequest cancels a pending approval request
	CancelRequest(ctx context.Context, requestID string) error

	// Name returns the handler name (e.g., "telegram", "slack")
	Name() string
}

// Config holds approval workflow configuration
type Config struct {
	Enabled bool `yaml:"enabled"`

	// Stage-specific configurations
	PreExecution *StageConfig `yaml:"pre_execution"`
	PreMerge     *StageConfig `yaml:"pre_merge"`
	PostFailure  *StageConfig `yaml:"post_failure"`

	// Default timeout for all stages (can be overridden per-stage)
	DefaultTimeout time.Duration `yaml:"default_timeout"`

	// Default action when timeout occurs
	DefaultAction Decision `yaml:"default_action"` // "approved" or "rejected"
}

// StageConfig holds configuration for a specific approval stage
type StageConfig struct {
	Enabled       bool          `yaml:"enabled"`
	Approvers     []string      `yaml:"approvers"`      // User IDs/handles who can approve
	Timeout       time.Duration `yaml:"timeout"`        // Timeout for this stage
	DefaultAction Decision      `yaml:"default_action"` // Action on timeout
	RequireAll    bool          `yaml:"require_all"`    // Require all approvers (vs any one)
}

// DefaultConfig returns default approval configuration (disabled by default)
func DefaultConfig() *Config {
	return &Config{
		Enabled:        false,
		DefaultTimeout: 1 * time.Hour,
		DefaultAction:  DecisionRejected,
		PreExecution: &StageConfig{
			Enabled:       false,
			Timeout:       1 * time.Hour,
			DefaultAction: DecisionRejected,
		},
		PreMerge: &StageConfig{
			Enabled:       false,
			Timeout:       24 * time.Hour,
			DefaultAction: DecisionRejected,
		},
		PostFailure: &StageConfig{
			Enabled:       false,
			Timeout:       1 * time.Hour,
			DefaultAction: DecisionRejected,
		},
	}
}
