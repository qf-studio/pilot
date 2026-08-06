package executor

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// resetGhCredentialProvider clears the package-level provider after each
// test so tests don't leak state into each other or into unrelated tests
// elsewhere in the package (GH-4746: provider is a process-wide singleton,
// same discipline as resetGitCredentialProvider for GH-4743).
func resetGhCredentialProvider(t *testing.T) {
	t.Helper()
	SetGhCredentialProvider(nil)
	t.Cleanup(func() { SetGhCredentialProvider(nil) })
}

func TestWithGhCredentials_NoProviderIsNoOp(t *testing.T) {
	resetGhCredentialProvider(t)

	cmd := exec.CommandContext(context.Background(), "gh", "issue", "view", "1")
	withGhCredentials(context.Background(), cmd)

	if cmd.Env != nil {
		t.Errorf("cmd.Env = %v, want nil (no provider installed => ambient environment inherited)", cmd.Env)
	}
}

func TestWithGhCredentials_InstallsGitHubTokenEnv(t *testing.T) {
	resetGhCredentialProvider(t)
	SetGhCredentialProvider(func(ctx context.Context) (string, error) {
		return testutil.FakeGitHubToken, nil
	})

	cmd := exec.CommandContext(context.Background(), "gh", "issue", "view", "1")
	withGhCredentials(context.Background(), cmd)

	if cmd.Env == nil {
		t.Fatal("cmd.Env is nil, want GITHUB_TOKEN/GH_TOKEN set")
	}
	env := map[string]string{}
	for _, kv := range cmd.Env {
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			env[kv[:idx]] = kv[idx+1:]
		}
	}
	if env["GITHUB_TOKEN"] != testutil.FakeGitHubToken {
		t.Errorf("GITHUB_TOKEN = %q, want %q", env["GITHUB_TOKEN"], testutil.FakeGitHubToken)
	}
	// GH-4753: gh CLI resolves GH_TOKEN with higher precedence than
	// GITHUB_TOKEN, so both must be set to the minted token or the App
	// cutover is silently ineffective.
	if env["GH_TOKEN"] != testutil.FakeGitHubToken {
		t.Errorf("GH_TOKEN = %q, want %q", env["GH_TOKEN"], testutil.FakeGitHubToken)
	}
}

// TestWithGhCredentials_OverridesAmbientGhToken verifies that an ambient
// GH_TOKEN/GITHUB_TOKEN already present in the daemon's environment (e.g. an
// OAuth PAT exported in the shell) is overridden by the minted App token
// (GH-4753). cmd.Env inherits os.Environ() and appends the provider's token
// after it, so os/exec's documented last-value-wins behavior for duplicate
// keys is what makes this safe — this test pins that behavior against
// regression instead of just asserting it in a comment.
func TestWithGhCredentials_OverridesAmbientGhToken(t *testing.T) {
	resetGhCredentialProvider(t)
	t.Setenv("GH_TOKEN", "ambient-gh-token-should-be-overridden")
	t.Setenv("GITHUB_TOKEN", "ambient-github-token-should-be-overridden")
	SetGhCredentialProvider(func(ctx context.Context) (string, error) {
		return testutil.FakeGitHubToken, nil
	})

	cmd := exec.CommandContext(context.Background(), "gh", "issue", "view", "1")
	withGhCredentials(context.Background(), cmd)

	if got := envValue(cmd.Env, "GH_TOKEN"); got != testutil.FakeGitHubToken {
		t.Errorf("GH_TOKEN = %q, want %q (ambient value must be overridden by minted token)", got, testutil.FakeGitHubToken)
	}
	if got := envValue(cmd.Env, "GITHUB_TOKEN"); got != testutil.FakeGitHubToken {
		t.Errorf("GITHUB_TOKEN = %q, want %q (ambient value must be overridden by minted token)", got, testutil.FakeGitHubToken)
	}
}

// TestWithGhCredentials_RotatesAfterRefresh verifies the provider is called
// fresh on every invocation rather than a token being captured once, so a
// token minted before a refresh is never reused after the provider starts
// returning a newer one (GH-4746 acceptance).
func TestWithGhCredentials_RotatesAfterRefresh(t *testing.T) {
	resetGhCredentialProvider(t)
	current := testutil.FakeGitHubToken
	SetGhCredentialProvider(func(ctx context.Context) (string, error) {
		return current, nil
	})

	cmd1 := exec.CommandContext(context.Background(), "gh", "issue", "view", "1")
	withGhCredentials(context.Background(), cmd1)
	if got := envValue(cmd1.Env, "GITHUB_TOKEN"); got != testutil.FakeGitHubToken {
		t.Fatalf("first call GITHUB_TOKEN = %q, want %q", got, testutil.FakeGitHubToken)
	}

	current = testutil.FakeGitHubPAT // stand-in for a rotated/refreshed token

	cmd2 := exec.CommandContext(context.Background(), "gh", "issue", "view", "1")
	withGhCredentials(context.Background(), cmd2)
	if got := envValue(cmd2.Env, "GITHUB_TOKEN"); got != testutil.FakeGitHubPAT {
		t.Errorf("second call GITHUB_TOKEN = %q, want %q (rotated token, not the stale first one)", got, testutil.FakeGitHubPAT)
	}
}

func TestWithGhCredentials_ProviderErrorFallsBackToAmbientEnv(t *testing.T) {
	resetGhCredentialProvider(t)
	SetGhCredentialProvider(func(ctx context.Context) (string, error) {
		return "", errors.New("mint failed")
	})

	cmd := exec.CommandContext(context.Background(), "gh", "issue", "view", "1")
	withGhCredentials(context.Background(), cmd)

	if cmd.Env != nil {
		t.Errorf("cmd.Env = %v, want nil (mint failure should degrade to ambient environment, not break the command)", cmd.Env)
	}
}

func TestWithGhCredentials_EmptyTokenFallsBackToAmbientEnv(t *testing.T) {
	resetGhCredentialProvider(t)
	SetGhCredentialProvider(func(ctx context.Context) (string, error) {
		return "", nil
	})

	cmd := exec.CommandContext(context.Background(), "gh", "issue", "view", "1")
	withGhCredentials(context.Background(), cmd)

	if cmd.Env != nil {
		t.Errorf("cmd.Env = %v, want nil (empty token should degrade to ambient environment)", cmd.Env)
	}
}

// resetGhMintFailureState clears the package-level mint-failure dedup state
// so tests don't leak into each other (GH-4753).
func resetGhMintFailureState(t *testing.T) {
	t.Helper()
	ghMintFailureMu.Lock()
	ghMintFailureLast = ""
	ghMintFailureMu.Unlock()
	t.Cleanup(func() {
		ghMintFailureMu.Lock()
		ghMintFailureLast = ""
		ghMintFailureMu.Unlock()
	})
}

// TestLogGhMintFailure_DedupsByReason verifies the ERROR log fires on state
// change (a new/different failure reason) but not on a repeat of the same
// reason — the non-spammy behavior GH-4753 requires so a persistent mint
// failure doesn't flood logs on every `gh` CLI invocation.
func TestLogGhMintFailure_DedupsByReason(t *testing.T) {
	resetGhMintFailureState(t)

	logGhMintFailure(errors.New("mint failed: 401"))
	if got := ghMintFailureLast; got != "mint failed: 401" {
		t.Fatalf("ghMintFailureLast = %q, want %q after first failure", got, "mint failed: 401")
	}

	// Same reason again — dedup state must not change (this is the
	// non-spammy guarantee; the log call itself is idempotent to invoke).
	logGhMintFailure(errors.New("mint failed: 401"))
	if got := ghMintFailureLast; got != "mint failed: 401" {
		t.Fatalf("ghMintFailureLast = %q, want unchanged %q on repeat failure", got, "mint failed: 401")
	}

	// Different reason — state must change, i.e. this failure logs again.
	logGhMintFailure(errors.New("mint failed: installation suspended"))
	if got := ghMintFailureLast; got != "mint failed: installation suspended" {
		t.Fatalf("ghMintFailureLast = %q, want %q after distinct failure", got, "mint failed: installation suspended")
	}
}

// TestWithGhCredentials_ResetsMintFailureStateOnSuccess verifies a
// subsequent successful mint clears the dedup state, so if the same failure
// reason recurs later it logs again instead of staying silently deduped
// forever.
func TestWithGhCredentials_ResetsMintFailureStateOnSuccess(t *testing.T) {
	resetGhCredentialProvider(t)
	resetGhMintFailureState(t)

	failing := true
	SetGhCredentialProvider(func(ctx context.Context) (string, error) {
		if failing {
			return "", errors.New("mint failed: 401")
		}
		return testutil.FakeGitHubToken, nil
	})

	withGhCredentials(context.Background(), exec.CommandContext(context.Background(), "gh", "issue", "view", "1"))
	if got := ghMintFailureLast; got != "mint failed: 401" {
		t.Fatalf("ghMintFailureLast = %q, want %q after failure", got, "mint failed: 401")
	}

	failing = false
	withGhCredentials(context.Background(), exec.CommandContext(context.Background(), "gh", "issue", "view", "1"))
	if got := ghMintFailureLast; got != "" {
		t.Fatalf("ghMintFailureLast = %q, want empty after a successful mint", got)
	}
}

// envValue returns the value of the last occurrence of key in env — matching
// os/exec.Cmd's own documented behavior ("If Env contains duplicate
// environment keys, only the last value in the slice for each duplicate key
// is used"), since withGhCredentials appends its GITHUB_TOKEN after
// os.Environ(), which may already contain an ambient GITHUB_TOKEN.
func envValue(env []string, key string) string {
	prefix := key + "="
	value := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			value = strings.TrimPrefix(kv, prefix)
		}
	}
	return value
}
