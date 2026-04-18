package executor

import (
	"fmt"
)

// NewBackend creates a Backend instance based on configuration.
func NewBackend(config *BackendConfig) (Backend, error) {
	if config == nil {
		config = DefaultBackendConfig()
	}

	heartbeatTimeout := config.EffectiveHeartbeatTimeout()

	switch config.Type {
	case BackendTypeClaudeCode, "":
		b := NewClaudeCodeBackend(config.ClaudeCode)
		b.SetHeartbeatTimeout(heartbeatTimeout)
		// GH-2371: single-source provider routing — inject configured
		// api_base_url / api_auth_token / default_model into the CC
		// subprocess env so users don't need to also edit
		// ~/.claude/settings.json.
		b.SetProviderEnv(config.APIBaseURL, config.APIAuthToken, config.DefaultModel)
		return b, nil

	case BackendTypeOpenCode:
		return NewOpenCodeBackend(config.OpenCode), nil

	case BackendTypeQwenCode:
		b := NewQwenCodeBackend(config.QwenCode)
		b.SetHeartbeatTimeout(heartbeatTimeout)
		return b, nil

	case BackendTypeAnthropicAPI:
		return NewAnthropicBackend(config), nil

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
