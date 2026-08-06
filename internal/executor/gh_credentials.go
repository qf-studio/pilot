package executor

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"sync"

	"github.com/qf-studio/pilot/internal/logging"
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
// with the current GitHub App installation token via GITHUB_TOKEN and
// GH_TOKEN, resolved fresh at spawn time through the installed
// GhTokenProvider — refresh-aware, since the provider (mintGitHubAppToken's
// shared TokenSource cache) is asked for the current token on every call
// rather than a value captured once at startup, so a token minted an hour
// ago is never reused after rotation. It is a no-op — cmd.Env stays nil,
// ambient environment inherited — when no provider is installed (the
// default; GITHUB_TOKEN/GH_TOKEN env / gh CLI login keep working exactly as
// before GH-4746).
//
// GH-4753: both GITHUB_TOKEN and GH_TOKEN are set — gh CLI resolves GH_TOKEN
// with higher precedence than GITHUB_TOKEN, and cmd.Env inherits the
// daemon's full ambient environment via os.Environ(). Setting only
// GITHUB_TOKEN left an ambient GH_TOKEN (e.g. an OAuth PAT exported in the
// daemon's shell) silently winning over the minted App token on every `gh`
// invocation, with app auth looking active and no signal that it wasn't
// actually being used.
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
		// GH-4753: the provider closure installed in cmd/pilot calls
		// mintGitHubAppToken directly, bypassing resolveGitHubToken — the
		// only site that used to log mint failures loudly — so without this
		// call a mint failure here degraded silently to the ambient
		// GITHUB_TOKEN/GH_TOKEN/gh-CLI-login, indefinitely, with no signal.
		// logGhMintFailure logs at ERROR on state change (not per-call) so a
		// persistent failure doesn't flood logs on every `gh` invocation
		// while still alerting loudly the moment it starts.
		logGhMintFailure(err)
		return cmd
	}
	ghMintFailureMu.Lock()
	ghMintFailureLast = ""
	ghMintFailureMu.Unlock()
	cmd.Env = append(os.Environ(),
		"GITHUB_TOKEN="+token,
		"GH_TOKEN="+token,
	)
	return cmd
}

var (
	ghMintFailureMu   sync.Mutex
	ghMintFailureLast string
)

// logGhMintFailure logs a `gh` CLI credential mint failure at ERROR, once
// per distinct failure reason (GH-4753) — see the comment in
// withGhCredentials for why this call site, and not resolveGitHubToken, is
// the only place a mint failure on this leg gets logged at all.
func logGhMintFailure(err error) {
	reason := "empty token"
	if err != nil {
		reason = err.Error()
	}
	ghMintFailureMu.Lock()
	defer ghMintFailureMu.Unlock()
	if ghMintFailureLast == reason {
		return
	}
	ghMintFailureLast = reason
	logging.WithComponent("github-auth").Error(
		"github app installation token mint failed for gh CLI — falling back to ambient GITHUB_TOKEN/GH_TOKEN/gh-CLI-login",
		slog.String("reason", reason),
	)
}
