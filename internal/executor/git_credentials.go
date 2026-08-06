package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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
		// Mint failure is already logged loudly by the provider (see
		// mintGitHubAppToken / resolveGitHubToken in cmd/pilot); falling
		// back to the ambient environment here lets a transient mint
		// failure degrade to the pre-GH-4743 behavior instead of breaking
		// every push/fetch outright.
		return cmd
	}
	askpass, askpassErr := gitAskpassHelperPath()
	if askpassErr != nil {
		return cmd
	}
	cmd.Env = append(os.Environ(),
		"GIT_ASKPASS="+askpass,
		"PILOT_GIT_TOKEN="+token,
		"GIT_TERMINAL_PROMPT=0",
	)
	return cmd
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
