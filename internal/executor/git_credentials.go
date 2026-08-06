package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/qf-studio/pilot/internal/logging"
)

// GitTokenProvider returns a currently-valid GitHub token to authenticate
// git HTTPS remote operations. Implementations must never log the token.
type GitTokenProvider func(ctx context.Context) (string, error)

var (
	gitCredentialProviderMu sync.RWMutex
	gitCredentialProvider   GitTokenProvider
)

// SetGitCredentialProvider installs the token provider used by
// withGitCredentials to authenticate git push/fetch for pilot worktrees
// (GH-4743). Passing nil clears it, reverting to the ambient environment
// (GITHUB_TOKEN env / gh CLI credential helper) — the default, and the only
// behavior before this ticket. Called once at daemon startup from
// cmd/pilot when adapters.github.app is configured.
func SetGitCredentialProvider(provider GitTokenProvider) {
	gitCredentialProviderMu.Lock()
	defer gitCredentialProviderMu.Unlock()
	gitCredentialProvider = provider
}

func getGitCredentialProvider() GitTokenProvider {
	gitCredentialProviderMu.RLock()
	defer gitCredentialProviderMu.RUnlock()
	return gitCredentialProvider
}

// withGitCredentials sets cmd.Env so a `git` remote operation (push, fetch,
// pull, ls-remote) authenticates over HTTPS with the token from the
// installed GitTokenProvider, using GIT_ASKPASS to supply x-access-token
// basic auth. It is a no-op — cmd.Env stays nil, ambient environment
// inherited — when no provider is installed (the default; GITHUB_TOKEN env /
// gh CLI credential helper keep working exactly as before GH-4743).
//
// The token only ever exists in the child git process's environment
// (PILOT_GIT_TOKEN), never in argv or a log line — the askpass helper
// script itself contains no secret material, it just echoes the env var
// git already handed it.
func withGitCredentials(ctx context.Context, cmd *exec.Cmd) *exec.Cmd {
	provider := getGitCredentialProvider()
	if provider == nil {
		return cmd
	}
	token, err := provider(ctx)
	if err != nil || token == "" {
		// GH-4753: the provider closure installed in cmd/pilot calls
		// mintGitHubAppToken directly, bypassing resolveGitHubToken — the
		// only site that used to log mint failures loudly — so without this
		// call a mint failure here degraded silently to the ambient
		// GITHUB_TOKEN env/gh-CLI credential helper, indefinitely, with no
		// signal. logGitMintFailure logs at ERROR on state change (not
		// per-call) so a persistent failure doesn't flood logs on every git
		// push/fetch while still alerting loudly the moment it starts.
		logGitMintFailure(err)
		return cmd
	}
	askpass, askpassErr := gitAskpassHelperPath()
	if askpassErr != nil {
		return cmd
	}
	gitMintFailureMu.Lock()
	gitMintFailureLast = ""
	gitMintFailureMu.Unlock()
	cmd.Env = append(os.Environ(),
		"GIT_ASKPASS="+askpass,
		"PILOT_GIT_TOKEN="+token,
		"GIT_TERMINAL_PROMPT=0",
	)
	return cmd
}

var (
	gitMintFailureMu   sync.Mutex
	gitMintFailureLast string
)

// logGitMintFailure logs a git credential mint failure at ERROR, once per
// distinct failure reason (GH-4753) — see the comment in withGitCredentials
// for why this call site, and not resolveGitHubToken, is the only place a
// mint failure on this leg gets logged at all.
func logGitMintFailure(err error) {
	reason := "empty token"
	if err != nil {
		reason = err.Error()
	}
	gitMintFailureMu.Lock()
	defer gitMintFailureMu.Unlock()
	if gitMintFailureLast == reason {
		return
	}
	gitMintFailureLast = reason
	logging.WithComponent("github-auth").Error(
		"github app installation token mint failed for git push/fetch — falling back to ambient GITHUB_TOKEN env/gh-CLI credential helper",
		slog.String("reason", reason),
	)
}

// gitAskpassScript is the content of the GIT_ASKPASS helper. It contains no
// secret material — it only ever echoes back the PILOT_GIT_TOKEN env var
// that withGitCredentials sets on the git child process — so it is safe to
// write to disk and check the source into version control.
const gitAskpassScript = `#!/bin/sh
# GH-4743: git credential-prompt helper for GitHub App installation tokens.
# Contains no secret material — it only echoes PILOT_GIT_TOKEN, which is
# set in this process's environment (never argv) by withGitCredentials.
case "$1" in
	Username*) printf '%s' "x-access-token" ;;
	*) printf '%s' "$PILOT_GIT_TOKEN" ;;
esac
`

var (
	gitAskpassOnce sync.Once
	gitAskpassPath string
	gitAskpassErr  error
)

// gitAskpassHelperPath lazily writes the (secret-free, static) askpass
// helper script to a temp file once per process and returns its path.
func gitAskpassHelperPath() (string, error) {
	gitAskpassOnce.Do(func() {
		path := filepath.Join(os.TempDir(), "pilot-git-askpass.sh")
		if err := os.WriteFile(path, []byte(gitAskpassScript), 0o700); err != nil {
			gitAskpassErr = fmt.Errorf("writing git askpass helper: %w", err)
			return
		}
		gitAskpassPath = path
	})
	return gitAskpassPath, gitAskpassErr
}
