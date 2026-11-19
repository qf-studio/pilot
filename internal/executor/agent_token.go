package executor

import (
	"context"
	"sync"
	"time"
)

// AgentReadTokenProvider mints a GitHub token that can only read repo
// ("owner/name"), for the agent user of a run_as_user run, which holds no
// GitHub credential of its own (TASK-651). It returns the token with its
// expiry. Implementations must never log the token.
type AgentReadTokenProvider func(ctx context.Context, repo string) (token string, expires time.Time, err error)

var (
	agentReadTokenProviderMu sync.RWMutex
	agentReadTokenProvider   AgentReadTokenProvider
)

// SetAgentReadTokenProvider installs the provider of read tokens for one
// repository. cmd/pilot calls it at daemon startup with a provider that
// mints a scoped GitHub App installation token when adapters.github.app is
// configured, and with nil otherwise. Passing nil clears it.
//
// Decision: there is no fallback to the token of the daemon. With no
// provider the agent gets no token, since the daemon's token can write to
// each repository it reaches.
func SetAgentReadTokenProvider(provider AgentReadTokenProvider) {
	agentReadTokenProviderMu.Lock()
	defer agentReadTokenProviderMu.Unlock()
	agentReadTokenProvider = provider
}

// GetAgentReadTokenProvider returns the installed provider, or nil when no
// GitHub App is configured.
func GetAgentReadTokenProvider() AgentReadTokenProvider {
	agentReadTokenProviderMu.RLock()
	defer agentReadTokenProviderMu.RUnlock()
	return agentReadTokenProvider
}
