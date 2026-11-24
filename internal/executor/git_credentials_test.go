package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
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

// resetGitAskpassHelper clears the once-state of the askpass helper for the
// test, so the next gitAskpassHelperPath call writes a new helper, and puts
// the state of the process back after the test.
func resetGitAskpassHelper(t *testing.T) {
	t.Helper()
	savedPath, savedErr := gitAskpassPath, gitAskpassErr
	gitAskpassOnce, gitAskpassPath, gitAskpassErr = sync.Once{}, "", nil
	t.Cleanup(func() {
		gitAskpassOnce, gitAskpassPath, gitAskpassErr = sync.Once{}, "", nil
		if savedPath != "" || savedErr != nil {
			gitAskpassOnce.Do(func() { gitAskpassPath, gitAskpassErr = savedPath, savedErr })
		}
	})
}

// GH-6694: an entry with the old fixed name in the shared temp directory —
// here a directory, so a write to that name must fail — does not stop the
// helper: it is written into a private directory of the process.
func TestGitAskpassHelperPathIgnoresAForeignFileWithTheOldName(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if err := os.Mkdir(filepath.Join(tmp, "pilot-git-askpass.sh"), 0o755); err != nil {
		t.Fatalf("creating the foreign entry: %v", err)
	}
	resetGitAskpassHelper(t)

	path, err := gitAskpassHelperPath()
	if err != nil {
		t.Fatalf("gitAskpassHelperPath() error = %v", err)
	}
	dir := filepath.Dir(path)
	if filepath.Dir(dir) != tmp {
		t.Errorf("helper %q is not in a directory directly below TMPDIR %q", path, tmp)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat helper directory: %v", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Errorf("helper directory %q mode = %v, want a directory with mode 0700", dir, info.Mode())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading helper: %v", err)
	}
	if string(content) != gitAskpassScript {
		t.Errorf("helper content = %q, want the askpass script", content)
	}
	again, err := gitAskpassHelperPath()
	if err != nil || again != path {
		t.Errorf("second gitAskpassHelperPath() = %q, %v; want the same path %q", again, err, path)
	}
}

// tokenCommandKinds are the git commands that carry the token: the push, the
// two ls-remote probes and the fetch of the count, by a part of their argv.
var tokenCommandKinds = map[string]string{
	"push":  " push ",
	"probe": " ls-remote ",
	"fetch": " fetch ",
}

// tokenCommandKind is the kind of the git command of r, "" for another one.
func tokenCommandKind(r daemonGitEnvRecord) string {
	for kind, part := range tokenCommandKinds {
		if strings.Contains(" "+r.args+" ", part) {
			return kind
		}
	}
	return ""
}

// basicAuthRemote is a local HTTP server that answers each request with 401
// and WWW-Authenticate: Basic, so git asks for credentials; the URL of a
// repository on it and the number of requests that carried credentials.
func basicAuthRemote(t *testing.T) (url string, authed *atomic.Int64) {
	t.Helper()
	authed = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			authed.Add(1)
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="pilot-test"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/repo.git", authed
}

// markerProgram writes a shell program that appends its arguments to marker.
func markerProgram(t *testing.T, marker string) string {
	t.Helper()
	prog := filepath.Join(t.TempDir(), "program")
	body := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> '%s'\nexit 1\n", marker)
	if err := os.WriteFile(prog, []byte(body), 0o755); err != nil { //nolint:gosec // program must be executable
		t.Fatal(err)
	}
	return prog
}

// TestTokenCommandsRunNoConfigProgram (TASK-651): a credential.helper of the
// repository config runs for none of the git commands that carry the token
// (the two probes, the fetch of the count, the push), with the option set and
// with it empty; each of them has -c protocol.ext.allow=never, -c
// core.gitProxy= and GIT_SSH_COMMAND=ssh. With no token, the remote commands
// of an agent workspace have GIT_TERMINAL_PROMPT=0. The count and the
// co-author read of an agent workspace get no token and no inherited
// GIT_ASKPASS; the push gets the helper of the code.
// mutation pin: withGitCredentials without -c credential.helper= -> this test fails
// mutation pin: withDaemonGitEnv takes the whole environment as the set part -> this test fails
func TestTokenCommandsRunNoConfigProgram(t *testing.T) {
	// run drives the token commands of g with credential.helper=!PROGRAM in
	// the config of repo and its origin on a server that asks for basic auth.
	run := func(t *testing.T, g *GitOperations, repo, branch string) {
		t.Helper()
		SetGitCredentialProvider(func(context.Context) (string, error) { return testutil.FakeGitHubToken, nil })
		t.Cleanup(func() { SetGitCredentialProvider(nil) })
		url, authed := basicAuthRemote(t)
		wsGit(t, repo, "remote", "set-url", "origin", url)
		marker := filepath.Join(t.TempDir(), "marker")
		wsGit(t, repo, "config", "credential.helper", "!"+markerProgram(t, marker))
		flag, logDir := installGitEnvWrapper(t)
		if err := os.WriteFile(flag, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = g.RemoteBranchHead(ctx, branch)
		_, _ = g.ProbeRemoteBranch(ctx, branch)
		_, _ = g.CountNewCommitsAgainstOrigin(ctx, "main")
		if err := g.Push(ctx, branch, handOverTestTask); err == nil {
			t.Error("Push to a remote that refuses each credential succeeded")
		}
		if err := os.Remove(flag); err != nil {
			t.Fatal(err)
		}

		if raw, err := os.ReadFile(marker); err == nil {
			t.Errorf("the credential.helper of the repository config ran: %q", raw)
		}
		if authed.Load() == 0 {
			t.Error("no request with credentials reached the remote: the askpass helper was not asked")
		}
		seen := map[string]int{}
		for _, r := range readGitEnvRecords(t, logDir) {
			kind := tokenCommandKind(r)
			if kind == "" {
				continue
			}
			seen[kind]++
			for _, arg := range []string{"protocol.ext.allow=never", "core.gitProxy=", "credential.helper="} {
				if !strings.Contains(r.args, "-c "+arg+" ") {
					t.Errorf("%s without -c %s: %s", kind, arg, r.args)
				}
			}
			if got := r.env["GIT_SSH_COMMAND"]; got != "ssh" {
				t.Errorf("%s: GIT_SSH_COMMAND = %q, want ssh", kind, got)
			}
		}
		for kind := range tokenCommandKinds {
			if seen[kind] == 0 {
				t.Errorf("the wrapper logged no %s command", kind)
			}
		}
	}

	t.Run("option set", func(t *testing.T) {
		e := newHandOverPushEnv(t)
		commitFile(t, e.ws, "a.txt", "one")
		run(t, e.git, e.repo, e.branch)
	})

	t.Run("option empty", func(t *testing.T) {
		secretpatterns.ResetSecretValuesForTest()
		ResetSecretGateConfigForTest()
		t.Cleanup(func() {
			secretpatterns.ResetSecretValuesForTest()
			ResetSecretGateConfigForTest()
		})
		setRunAsStateForTest(t, nil, "")
		repo, remote := setupTestRepoWithRemote(t)
		t.Cleanup(func() { _ = os.RemoveAll(repo); _ = os.RemoveAll(remote) })
		branch := "pilot/GH-1"
		wsGit(t, repo, "checkout", "-q", "-b", branch)
		commitFile(t, repo, "a.txt", "one")
		g := newGitOperationsForWorkspace(repo, repo)
		if g.daemonRepo != "" {
			t.Fatalf("option empty: daemonRepo = %q, want empty", g.daemonRepo)
		}
		run(t, g, repo, branch)
	})

	t.Run("no token: no terminal prompt", func(t *testing.T) {
		e := newHandOverPushEnv(t)
		SetGitCredentialProvider(func(context.Context) (string, error) { return "", nil })
		t.Cleanup(func() { SetGitCredentialProvider(nil) })
		ctx := context.Background()
		head := commitFile(t, e.ws, "a.txt", "one")
		flag, logDir := installGitEnvWrapper(t)
		t.Setenv("GIT_ASKPASS", "/inherited")
		t.Setenv("GIT_TERMINAL_PROMPT", "1")
		if err := os.WriteFile(flag, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		pushErr := e.git.Push(ctx, e.branch, handOverTestTask)
		_, _ = e.git.RemoteBranchHead(ctx, e.branch)
		_, _ = e.git.ProbeRemoteBranch(ctx, e.branch)
		_, _ = e.git.CountNewCommitsAgainstOrigin(ctx, "main")
		if err := os.Remove(flag); err != nil {
			t.Fatal(err)
		}
		if pushErr != nil {
			t.Fatalf("Push: %v", pushErr)
		}
		if got := e.remoteHead(t, e.branch); got != head {
			t.Errorf("remote head = %q, want %q", got, head)
		}
		seen := map[string]int{}
		for _, r := range readGitEnvRecords(t, logDir) {
			kind := tokenCommandKind(r)
			if kind == "" {
				continue
			}
			seen[kind]++
			if got := r.env["GIT_TERMINAL_PROMPT"]; got != "0" {
				t.Errorf("%s with no token: GIT_TERMINAL_PROMPT = %q, want 0", kind, got)
			}
			if r.has("GIT_ASKPASS") {
				t.Errorf("%s with no token has GIT_ASKPASS=%q", kind, r.env["GIT_ASKPASS"])
			}
		}
		for kind := range tokenCommandKinds {
			if seen[kind] == 0 {
				t.Errorf("the wrapper logged no %s command", kind)
			}
		}
	})

	t.Run("local commands: no token", func(t *testing.T) {
		e := newHandOverPushEnv(t)
		SetGitCredentialProvider(func(context.Context) (string, error) { return testutil.FakeGitHubToken, nil })
		t.Cleanup(func() { SetGitCredentialProvider(nil) })
		askpass, err := gitAskpassHelperPath()
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		commitFile(t, e.ws, "a.txt", "one")
		flag, logDir := installGitEnvWrapper(t)
		t.Setenv("GIT_ASKPASS", "/inherited")
		t.Setenv("GIT_TERMINAL_PROMPT", "1")
		t.Setenv("GITHUB_TOKEN", testutil.FakeGitHubToken)
		if err := os.WriteFile(flag, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		pushErr := e.git.Push(ctx, e.branch, handOverTestTask)
		count, countErr := e.git.CountNewCommitsAgainstOrigin(ctx, "main")
		if err := os.Remove(flag); err != nil {
			t.Fatal(err)
		}
		if pushErr != nil {
			t.Fatalf("Push: %v", pushErr)
		}
		if countErr != nil || count != 1 {
			t.Errorf("CountNewCommitsAgainstOrigin = %d, %v; want 1", count, countErr)
		}
		seen := map[string]int{}
		for _, r := range readGitEnvRecords(t, logDir) {
			var kind string
			switch {
			case strings.Contains(r.args, "rev-list --count"):
				kind = "count"
			case strings.Contains(r.args, "--format=%H%x00%B%x00"):
				kind = "coauthor"
			case strings.Contains(" "+r.args+" ", " push "):
				seen["push"]++
				if got := r.env["GIT_ASKPASS"]; got != askpass {
					t.Errorf("push: GIT_ASKPASS = %q, want the helper %q", got, askpass)
				}
				if got := r.env["GIT_TERMINAL_PROMPT"]; got != "0" {
					t.Errorf("push: GIT_TERMINAL_PROMPT = %q, want 0", got)
				}
				continue
			default:
				continue
			}
			seen[kind]++
			for _, name := range []string{"GIT_ASKPASS", "GITHUB_TOKEN", "PILOT_GIT_TOKEN"} {
				if r.has(name) {
					t.Errorf("%s has %s=%q", kind, name, r.env[name])
				}
			}
			if got := r.env["GIT_NO_REPLACE_OBJECTS"]; got != "1" {
				t.Errorf("%s: GIT_NO_REPLACE_OBJECTS = %q, want 1", kind, got)
			}
		}
		for _, kind := range []string{"push", "count", "coauthor"} {
			if seen[kind] == 0 {
				t.Errorf("the wrapper logged no %s command", kind)
			}
		}
	})
}

// TestTokenCommandsRunNoTransportProgram: with a token, a probe runs neither
// the core.gitProxy of a git:// remote, nor the program of an ext:: remote
// that the repository config allows, nor the core.sshCommand of an ssh remote,
// with the option empty and with it set (the probe runs in the repository of
// the daemon, with the environment of daemonGitEnv).
// mutation pin: GIT_PROXY_COMMAND out of daemonGitEnvSet -> this test fails
func TestTokenCommandsRunNoTransportProgram(t *testing.T) {
	cases := []struct {
		name   string
		config func(prog, marker string) (url string, kv [][2]string)
	}{
		{"core.gitProxy", func(prog, _ string) (string, [][2]string) {
			return "git://127.0.0.1:9/x", [][2]string{{"core.gitProxy", prog}}
		}},
		{"ext remote", func(_, marker string) (string, [][2]string) {
			return "ext::sh -c touch% " + marker, [][2]string{{"protocol.ext.allow", "always"}}
		}},
		{"core.sshCommand", func(prog, _ string) (string, [][2]string) {
			return "ssh://127.0.0.1:9/x", [][2]string{{"core.sshCommand", prog}}
		}},
	}
	// run probes origin of repo through g with each program in its config.
	run := func(t *testing.T, g *GitOperations, repo string) {
		t.Helper()
		SetGitCredentialProvider(func(context.Context) (string, error) { return testutil.FakeGitHubToken, nil })
		t.Cleanup(func() { SetGitCredentialProvider(nil) })
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				marker := filepath.Join(t.TempDir(), "marker")
				url, kv := tc.config(markerProgram(t, marker), marker)
				wsGit(t, repo, "remote", "set-url", "origin", url)
				for _, c := range kv {
					wsGit(t, repo, "config", c[0], c[1])
					t.Cleanup(func() { _ = exec.Command("git", "-C", repo, "config", "--unset-all", c[0]).Run() })
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				_, _ = g.ProbeRemoteBranch(ctx, "main")
				if raw, err := os.ReadFile(marker); err == nil {
					t.Errorf("the %s program of the repository config ran: %q", tc.name, raw)
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			})
		}
	}

	t.Run("option empty", func(t *testing.T) {
		setRunAsStateForTest(t, nil, "")
		repo, remote := setupTestRepoWithRemote(t)
		t.Cleanup(func() { _ = os.RemoveAll(repo); _ = os.RemoveAll(remote) })
		run(t, NewGitOperations(repo), repo)
	})

	t.Run("option set", func(t *testing.T) {
		e := newHandOverPushEnv(t)
		run(t, e.git, e.repo)
	})
}

// TestWithDaemonGitEnvSplitsInheritedAndSet: withDaemonGitEnv drops each
// inherited GIT_ name (GIT_ASKPASS=/inherited, GIT_TERMINAL_PROMPT=1) and keeps
// the values the code set after os.Environ(); with no GIT_TERMINAL_PROMPT in
// the set part it appends GIT_TERMINAL_PROMPT=0.
func TestWithDaemonGitEnvSplitsInheritedAndSet(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	t.Setenv("GIT_ASKPASS", "/inherited")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	envOf := func(cmd *exec.Cmd) map[string][]string {
		env := map[string][]string{}
		for _, kv := range cmd.Env {
			name, value, _ := strings.Cut(kv, "=")
			env[name] = append(env[name], value)
		}
		return env
	}

	set := exec.Command("git", "fetch")
	set.Env = append(os.Environ(), "GIT_ASKPASS=/code", "GIT_NO_REPLACE_OBJECTS=1")
	env := envOf(withDaemonGitEnv(set))
	if got := env["GIT_ASKPASS"]; len(got) != 1 || got[0] != "/code" {
		t.Errorf("set part: GIT_ASKPASS = %q, want only /code", got)
	}
	if got := env["GIT_TERMINAL_PROMPT"]; len(got) != 1 || got[0] != "0" {
		t.Errorf("set part: GIT_TERMINAL_PROMPT = %q, want only 0", got)
	}

	none := exec.Command("git", "fetch")
	env = envOf(withDaemonGitEnv(none))
	if got := env["GIT_ASKPASS"]; len(got) != 0 {
		t.Errorf("nil env: GIT_ASKPASS = %q, want none", got)
	}
	if got := env["GIT_TERMINAL_PROMPT"]; len(got) != 1 || got[0] != "0" {
		t.Errorf("nil env: GIT_TERMINAL_PROMPT = %q, want only 0", got)
	}

	prompt := exec.Command("git", "fetch")
	prompt.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if got := envOf(withDaemonGitEnv(prompt))["GIT_TERMINAL_PROMPT"]; len(got) != 1 || got[0] != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT in the set part: %q, want only 0", got)
	}
}
