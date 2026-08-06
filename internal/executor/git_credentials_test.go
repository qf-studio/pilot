package executor

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// resetGitCredentialProvider clears the package-level provider after each
// test so tests don't leak state into each other or into unrelated tests
// elsewhere in the package (GH-4743: provider is a process-wide singleton).
func resetGitCredentialProvider(t *testing.T) {
	t.Helper()
	SetGitCredentialProvider(nil)
	t.Cleanup(func() { SetGitCredentialProvider(nil) })
}

func TestWithGitCredentials_NoProviderIsNoOp(t *testing.T) {
	resetGitCredentialProvider(t)

	cmd := exec.CommandContext(context.Background(), "git", "fetch")
	withGitCredentials(context.Background(), cmd)

	if cmd.Env != nil {
		t.Errorf("cmd.Env = %v, want nil (no provider installed => ambient environment inherited)", cmd.Env)
	}
}

func TestWithGitCredentials_InstallsAskpassEnv(t *testing.T) {
	resetGitCredentialProvider(t)
	SetGitCredentialProvider(func(ctx context.Context) (string, error) {
		return testutil.FakeGitHubToken, nil
	})

	cmd := exec.CommandContext(context.Background(), "git", "fetch")
	withGitCredentials(context.Background(), cmd)

	if cmd.Env == nil {
		t.Fatal("cmd.Env is nil, want GIT_ASKPASS/PILOT_GIT_TOKEN set")
	}
	env := map[string]string{}
	for _, kv := range cmd.Env {
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			env[kv[:idx]] = kv[idx+1:]
		}
	}
	if env["GIT_ASKPASS"] == "" {
		t.Error("GIT_ASKPASS not set on cmd.Env")
	}
	if env["PILOT_GIT_TOKEN"] != testutil.FakeGitHubToken {
		t.Errorf("PILOT_GIT_TOKEN = %q, want %q", env["PILOT_GIT_TOKEN"], testutil.FakeGitHubToken)
	}
	if env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q, want %q", env["GIT_TERMINAL_PROMPT"], "0")
	}
}

func TestWithGitCredentials_ProviderErrorFallsBackToAmbientEnv(t *testing.T) {
	resetGitCredentialProvider(t)
	SetGitCredentialProvider(func(ctx context.Context) (string, error) {
		return "", errors.New("mint failed")
	})

	cmd := exec.CommandContext(context.Background(), "git", "fetch")
	withGitCredentials(context.Background(), cmd)

	if cmd.Env != nil {
		t.Errorf("cmd.Env = %v, want nil (mint failure should degrade to ambient environment, not break the command)", cmd.Env)
	}
}

func TestWithGitCredentials_EmptyTokenFallsBackToAmbientEnv(t *testing.T) {
	resetGitCredentialProvider(t)
	SetGitCredentialProvider(func(ctx context.Context) (string, error) {
		return "", nil
	})

	cmd := exec.CommandContext(context.Background(), "git", "fetch")
	withGitCredentials(context.Background(), cmd)

	if cmd.Env != nil {
		t.Errorf("cmd.Env = %v, want nil (empty token should degrade to ambient environment)", cmd.Env)
	}
}

// resetGitMintFailureState clears the package-level mint-failure dedup
// state so tests don't leak into each other (GH-4753).
func resetGitMintFailureState(t *testing.T) {
	t.Helper()
	gitMintFailureMu.Lock()
	gitMintFailureLast = ""
	gitMintFailureMu.Unlock()
	t.Cleanup(func() {
		gitMintFailureMu.Lock()
		gitMintFailureLast = ""
		gitMintFailureMu.Unlock()
	})
}

// TestLogGitMintFailure_DedupsByReason verifies the ERROR log fires on state
// change (a new/different failure reason) but not on a repeat of the same
// reason — the non-spammy behavior GH-4753 requires so a persistent mint
// failure doesn't flood logs on every git push/fetch.
func TestLogGitMintFailure_DedupsByReason(t *testing.T) {
	resetGitMintFailureState(t)

	logGitMintFailure(errors.New("mint failed: 401"))
	if got := gitMintFailureLast; got != "mint failed: 401" {
		t.Fatalf("gitMintFailureLast = %q, want %q after first failure", got, "mint failed: 401")
	}

	logGitMintFailure(errors.New("mint failed: 401"))
	if got := gitMintFailureLast; got != "mint failed: 401" {
		t.Fatalf("gitMintFailureLast = %q, want unchanged %q on repeat failure", got, "mint failed: 401")
	}

	logGitMintFailure(errors.New("mint failed: installation suspended"))
	if got := gitMintFailureLast; got != "mint failed: installation suspended" {
		t.Fatalf("gitMintFailureLast = %q, want %q after distinct failure", got, "mint failed: installation suspended")
	}
}

// TestWithGitCredentials_ResetsMintFailureStateOnSuccess verifies a
// subsequent successful mint clears the dedup state, so if the same failure
// reason recurs later it logs again instead of staying silently deduped
// forever.
func TestWithGitCredentials_ResetsMintFailureStateOnSuccess(t *testing.T) {
	resetGitCredentialProvider(t)
	resetGitMintFailureState(t)

	failing := true
	SetGitCredentialProvider(func(ctx context.Context) (string, error) {
		if failing {
			return "", errors.New("mint failed: 401")
		}
		return testutil.FakeGitHubToken, nil
	})

	withGitCredentials(context.Background(), exec.CommandContext(context.Background(), "git", "fetch"))
	if got := gitMintFailureLast; got != "mint failed: 401" {
		t.Fatalf("gitMintFailureLast = %q, want %q after failure", got, "mint failed: 401")
	}

	failing = false
	withGitCredentials(context.Background(), exec.CommandContext(context.Background(), "git", "fetch"))
	if got := gitMintFailureLast; got != "" {
		t.Fatalf("gitMintFailureLast = %q, want empty after a successful mint", got)
	}
}

func TestGitAskpassHelperPath_ScriptContainsNoSecret(t *testing.T) {
	path, err := gitAskpassHelperPath()
	if err != nil {
		t.Fatalf("gitAskpassHelperPath() error = %v", err)
	}
	if path == "" {
		t.Fatal("gitAskpassHelperPath() returned empty path")
	}
	if strings.Contains(gitAskpassScript, testutil.FakeGitHubToken) {
		t.Error("askpass script must never contain literal token material — it should only read PILOT_GIT_TOKEN from the environment")
	}
}
