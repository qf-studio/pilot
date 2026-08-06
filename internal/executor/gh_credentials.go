package executor

import (
	"context"
	"os"
	"os/exec"
	"sync"
)

// GhTokenProvider returns a currently-valid GitHub token to authenticate
// `gh` CLI subprocess calls (PR creation, issue comments, label ops).
// Implementations must never log the token.
type GhTokenProvider func(ctx context.Context) (string, error)

var (
	ghCredentialProviderMu sync.RWMutex
	ghCredentialProvider   GhTokenProvider
)

// SetGhCredentialProvider installs the token provider used by
// withGhCredentials to authenticate `gh` CLI subprocess calls with a GitHub
// App installation token (GH-4746). Passing nil clears it, reverting to the
// ambient environment (GITHUB_TOKEN env / gh CLI login) — the default, and
// the only behavior before this ticket. Called once at daemon startup from
// cmd/pilot when adapters.github.app is configured, mirroring
// SetGitCredentialProvider (GH-4743), which wired the same minted token
// into raw `git` HTTPS operations — this closes the matching gap for `gh`
// CLI subprocesses (PR creation, issue comments, label ops), the daemon's
// highest-volume writes.
func SetGhCredentialProvider(provider GhTokenProvider) {
	ghCredentialProviderMu.Lock()
	defer ghCredentialProviderMu.Unlock()
	ghCredentialProvider = provider
}

func getGhCredentialProvider() GhTokenProvider {
	ghCredentialProviderMu.RLock()
	defer ghCredentialProviderMu.RUnlock()
	return ghCredentialProvider
}

// withGhCredentials sets cmd.Env so a `gh` CLI subprocess authenticates
// with the current GitHub App installation token via GITHUB_TOKEN, resolved
// fresh at spawn time through the installed GhTokenProvider — refresh-aware,
// since the provider (mintGitHubAppToken's shared TokenSource cache) is
// asked for the current token on every call rather than a value captured
// once at startup, so a token minted an hour ago is never reused after
// rotation. It is a no-op — cmd.Env stays nil, ambient environment
// inherited — when no provider is installed (the default; GITHUB_TOKEN env
// / gh CLI login keep working exactly as before GH-4746).
//
// The token only ever exists in the child `gh` process's environment, never
// in argv or a log line.
func withGhCredentials(ctx context.Context, cmd *exec.Cmd) *exec.Cmd {
	provider := getGhCredentialProvider()
	if provider == nil {
		return cmd
	}
	token, err := provider(ctx)
	if err != nil || token == "" {
		// Mint failure is already logged loudly by the provider (see
		// mintGitHubAppToken / resolveGitHubToken in cmd/pilot); falling
		// back to the ambient environment here lets a transient mint
		// failure degrade to the pre-GH-4746 behavior instead of breaking
		// every `gh` CLI call outright.
		return cmd
	}
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+token)
	return cmd
}
