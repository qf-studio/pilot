package executor

import (
	"fmt"
)

// NewBackend creates a Backend instance based on configuration.
func NewBackend(config *BackendConfig) (Backend, error) {
	if config == nil {
		config = DefaultBackendConfig()
	}

	switch config.Type {
	case BackendTypeClaudeCode, "":
		// Default to Claude Code
		return NewClaudeCodeBackend(config.ClaudeCode), nil

	case BackendTypeOpenCode:
		return NewOpenCodeBackend(config.OpenCode), nil

	default:
		return nil, fmt.Errorf("unknown backend type: %s", config.Type)
	}
}

// NewBackendFromType creates a Backend instance using default config for the type.
func NewBackendFromType(backendType string) (Backend, error) {
	config := DefaultBackendConfig()
	config.Type = backendType
	return NewBackend(config)
}
