package github

import "time"

// ApprovalConfig holds GitHub PR-review approval handler configuration.
type ApprovalConfig struct {
	Enabled      bool          `yaml:"enabled"`
	PollInterval time.Duration `yaml:"poll_interval"`
}

// DefaultApprovalConfig returns a disabled GitHub approval config with a 30-second poll interval.
func DefaultApprovalConfig() *ApprovalConfig {
	return &ApprovalConfig{Enabled: false, PollInterval: 30 * time.Second}
}
