package executor

import (
	"fmt"
	"log/slog"
	"os/exec"
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
		// GH-3028: wire RSS telemetry + optional memory cap.
		b.SetSubprocessLimits(config.SubprocessLimits)
		// GH-4671: resolve the real `gh` binary exactly once, here, at
		// daemon/backend construction time — never per-spawn, so the
		// gh-guard shim (which is itself named `gh` and prepended onto the
		// subprocess PATH) can never be found by this lookup and recurse
		// into itself.
		ghGuardEnabled := config.ClaudeCode.GhGuardEnabled()
		realGh, ghErr := exec.LookPath("gh")
		if ghErr != nil {
			realGh = ""
		}
		b.SetGhGuard(ghGuardEnabled, realGh)
		if ghGuardEnabled {
			if realGh == "" {
				slog.Warn("gh-guard enabled but no gh binary found on PATH at startup; mutations still blocked via fallback PATH search",
					slog.String("component", "executor.backend_factory"),
				)
			} else {
				slog.Info("gh-guard enabled for Claude Code subprocess spawns",
					slog.String("component", "executor.backend_factory"),
					slog.String("real_gh", realGh),
				)
			}
		} else {
			slog.Warn("gh-guard disabled via claude_code.gh_guard: false; gh CLI calls are unrestricted",
				slog.String("component", "executor.backend_factory"),
			)
		}
		return b, nil

	case BackendTypeOpenCode:
		return NewOpenCodeBackend(config.OpenCode), nil

	case BackendTypeQwenCode:
		b := NewQwenCodeBackend(config.QwenCode)
		b.SetHeartbeatTimeout(heartbeatTimeout)
		return b, nil

	case BackendTypeAnthropicAPI:
		return NewAnthropicBackend(config), nil

	case BackendTypeOpenAIAPI:
		return NewOpenAIBackend(config), nil

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
