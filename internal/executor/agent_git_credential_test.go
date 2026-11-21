package executor

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/quality"
)

// TASK-651: git of the agent side reads the token file of the run through a
// credential helper; the environment holds the path of the helper, never the
// token.

const agentGitTestToken = "test-read-token-1"

// agentGitConfigEntries returns the GIT_CONFIG_* entries of env by name.
func agentGitConfigEntries(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		if name, v, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "GIT_CONFIG_COUNT") ||
			strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
			out[name] = v
		}
	}
	return out
}

// startTestAgentTokenFile sets the option and starts the token file of a run
// with agentGitTestToken.
func startTestAgentTokenFile(t *testing.T) *agentTokenFile {
	t.Helper()
	stubRunAsGOOS(t)
	tokenTestRunAs(t, true)
	p := &fakeReadTokenProvider{tokens: []string{agentGitTestToken}, expiry: 1 << 40}
	setAgentReadTokenProviderForTest(t, p.provide)
	f := startAgentTokenFile(context.Background(), slog.Default(), "qf-studio/pilot")
	if f == nil {
		t.Fatal("no token file with the option set and a provider")
	}
	t.Cleanup(f.Stop)
	return f
}

// agentCmdEnv builds an agent command in ctx and sets env on it with
// setAgentEnvIn.
func agentCmdEnv(t *testing.T, ctx context.Context, env []string) *exec.Cmd {
	t.Helper()
	cmd, err := agentCommand(ctx, "git", "fetch", "origin")
	if err != nil {
		t.Fatal(err)
	}
	setAgentEnvIn(cmd, env, t.TempDir())
	return cmd
}

// assertNoToken fails when any entry of env or argv holds a part of the token.
func assertNoToken(t *testing.T, what string, list []string) {
	t.Helper()
	for _, kv := range list {
		if strings.Contains(kv, agentGitTestToken) || strings.Contains(kv, "read-token") {
			t.Errorf("%s holds the token: %q", what, kv)
		}
	}
}

func TestAgentGitConfigEntriesCarryNoToken(t *testing.T) {
	t.Run("agent command: helper entry, no token, inherited entries dropped", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
		t.Setenv("GIT_CONFIG_VALUE_0", "/inherited/hooks")
		f := startTestAgentTokenFile(t)
		ctx := withAgentTokenFile(context.Background(), f)

		cmd := agentCmdEnv(t, ctx, append(gitAgentBaseEnv(), "GIT_CONFIG_KEY_1=core.sshCommand"))
		got := agentGitConfigEntries(cmd.Env)
		want := map[string]string{
			"GIT_CONFIG_COUNT":   "1",
			"GIT_CONFIG_KEY_0":   "credential.https://github.com.helper",
			"GIT_CONFIG_VALUE_0": "!" + shellQuote(filepath.Join(f.dir, "git-credential-pilot")),
		}
		if len(got) != len(want) {
			t.Errorf("config entries = %v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %q, want %q", k, got[k], v)
			}
		}
		assertNoToken(t, "env", cmd.Env)
		assertNoToken(t, "argv", cmd.Args)
		preserve := ""
		for _, a := range cmd.Args {
			if strings.HasPrefix(a, runAsPreserveFlag) {
				preserve = a
			}
		}
		for name := range want {
			if !strings.Contains(preserve, name) {
				t.Errorf("--preserve-env %q lacks %s", preserve, name)
			}
		}

		fi, err := os.Stat(f.helper)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o750 {
			t.Errorf("helper mode = %o, want 750", fi.Mode().Perm())
		}
		if gid, dirGID := fileGID(t, f.helper), fileGID(t, f.dir); gid != dirGID {
			t.Errorf("helper gid = %d, want the gid %d of the token directory", gid, dirGID)
		}
		raw, err := os.ReadFile(f.helper)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), agentGitTestToken) || !strings.Contains(string(raw), f.path) {
			t.Errorf("helper script must hold the path of the token file, not the token:\n%s", raw)
		}
	})

	t.Run("gate command builder: the same entries", func(t *testing.T) {
		f := startTestAgentTokenFile(t)
		ctx := withAgentTokenFile(context.Background(), f)
		cmd, err := gateCommandBuilder{}.Build(ctx, t.TempDir(), nil, "make", "test")
		if err != nil {
			t.Fatal(err)
		}
		got := agentGitConfigEntries(cmd.Env)
		if got["GIT_CONFIG_COUNT"] != "1" || got["GIT_CONFIG_KEY_0"] != "credential.https://github.com.helper" || got["GIT_CONFIG_VALUE_0"] != "!"+shellQuote(f.helper) {
			t.Errorf("gate config entries = %v", got)
		}
		assertNoToken(t, "gate env", cmd.Env)
	})

	t.Run("entry of the code first: the helper takes the next index", func(t *testing.T) {
		f := &agentTokenFile{helper: "/run/git-credential-pilot"}
		env := appendAgentGitCredentialEntries([]string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/dev/null"}, f)
		got := agentGitConfigEntries(env)
		if got["GIT_CONFIG_COUNT"] != "2" || got["GIT_CONFIG_KEY_0"] != "core.hooksPath" ||
			got["GIT_CONFIG_KEY_1"] != "credential.https://github.com.helper" || got["GIT_CONFIG_VALUE_1"] != "!"+shellQuote(f.helper) {
			t.Errorf("config entries = %v", got)
		}
	})

	t.Run("helper script: name and token for get, nothing for store and erase", func(t *testing.T) {
		f := startTestAgentTokenFile(t)
		for _, tc := range []struct{ op, want string }{
			{"get", "username=x-access-token\npassword=" + agentGitTestToken + "\n"},
			{"store", ""},
			{"erase", ""},
		} {
			cmd := exec.Command(f.helper, tc.op)
			cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\nusername=x-access-token\npassword=other\n\n")
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%s: %v", tc.op, err)
			}
			if string(out) != tc.want {
				t.Errorf("%s printed %q, want %q", tc.op, out, tc.want)
			}
		}
	})

	t.Run("git with the entries sends the token of the file to a server that asks for basic auth", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		var mu sync.Mutex
		var user, pass string
		var authed bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, p, ok := r.BasicAuth()
			if !ok {
				w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mu.Lock()
			user, pass, authed = u, p, true
			mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()
		prevURL := agentGitCredentialURL
		agentGitCredentialURL = srv.URL
		t.Cleanup(func() { agentGitCredentialURL = prevURL })

		f := startTestAgentTokenFile(t)
		agent := agentCmdEnv(t, withAgentTokenFile(context.Background(), f), gitAgentBaseEnv())
		env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
		for k, v := range agentGitConfigEntries(agent.Env) {
			env = append(env, k+"="+v)
		}
		cmd := exec.Command("git", "ls-remote", srv.URL+"/owner/repo.git")
		cmd.Env = env
		out, _ := cmd.CombinedOutput() // the server answers 404 once authenticated
		mu.Lock()
		defer mu.Unlock()
		if !authed || user != "x-access-token" || pass != agentGitTestToken {
			t.Errorf("server got auth=%v user=%q password=%q, want x-access-token and the token of the file; git:\n%s", authed, user, pass, out)
		}
	})

	t.Run("no token file: no entry", func(t *testing.T) {
		startTestAgentTokenFile(t)
		cmd := agentCmdEnv(t, context.Background(), gitAgentBaseEnv())
		if got := agentGitConfigEntries(cmd.Env); len(got) != 0 {
			t.Errorf("config entries with no token file = %v", got)
		}
		gate, err := gateCommandBuilder{}.Build(context.Background(), t.TempDir(), nil, "make", "test")
		if err != nil {
			t.Fatal(err)
		}
		if got := agentGitConfigEntries(gate.Env); len(got) != 0 {
			t.Errorf("gate config entries with no token file = %v", got)
		}
	})

	t.Run("option empty: no entry", func(t *testing.T) {
		tokenTestRunAs(t, false)
		f := &agentTokenFile{dir: t.TempDir(), helper: "/run/git-credential-pilot"}
		ctx := withAgentTokenFile(context.Background(), f)
		cmd := agentCmdEnv(t, ctx, []string{"PATH=/usr/bin"})
		if got := agentGitConfigEntries(cmd.Env); len(got) != 0 {
			t.Errorf("config entries with the option empty = %v", got)
		}
		gate, err := gateCommandBuilder{}.Build(ctx, t.TempDir(), []string{"PATH=/usr/bin"}, "make", "test")
		if err != nil {
			t.Fatal(err)
		}
		if got := agentGitConfigEntries(gate.Env); len(got) != 0 {
			t.Errorf("gate config entries with the option empty = %v", got)
		}
	})
}

// agentGitAuthServer is a local server that asks for basic auth and answers
// 404 once a request carries it; creds returns the last credentials it got.
func agentGitAuthServer(t *testing.T) (url string, creds func() (user, pass string, authed bool)) {
	t.Helper()
	var mu sync.Mutex
	var user, pass string
	var authed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		user, pass, authed = u, p, true
		mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	prevURL := agentGitCredentialURL
	agentGitCredentialURL = srv.URL
	t.Cleanup(func() { agentGitCredentialURL = prevURL })
	return srv.URL, func() (string, string, bool) {
		mu.Lock()
		defer mu.Unlock()
		return user, pass, authed
	}
}

// TestAgentGitCredentialHelperPathIsQuoted: with a temporary directory of the
// daemon whose name holds a space or the text $(touch MARK), real git with the
// config entries of an agent command gets the token of the file from a server
// that asks for basic auth, and no file MARK is made.
// mutation pin: appendAgentGitCredentialEntries without the quoting of the helper value -> this test fails
// mutation pin: agentGitCredentialHelperScript without the quoting of the token path -> this test fails
func TestAgentGitCredentialHelperPathIsQuoted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, tc := range []struct{ test, name string }{
		{"space", "tmp dir"},
		{"command substitution", "tmp$(touch MARK)dir"},
	} {
		name := tc.name
		t.Run(tc.test, func(t *testing.T) {
			url, creds := agentGitAuthServer(t)
			stubRunAsGOOS(t)
			_, tmp := tokenTestRunAs(t, true)
			base := filepath.Join(tmp, name)
			if err := os.Mkdir(base, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", base)
			p := &fakeReadTokenProvider{tokens: []string{agentGitTestToken}, expiry: 1 << 40}
			setAgentReadTokenProviderForTest(t, p.provide)
			f := startAgentTokenFile(context.Background(), slog.Default(), "qf-studio/pilot")
			if f == nil {
				t.Fatal("no token file with the option set and a provider")
			}
			t.Cleanup(f.Stop)
			if !strings.HasPrefix(f.helper, base+string(filepath.Separator)) {
				t.Fatalf("helper %q is not in %q", f.helper, base)
			}

			agent := agentCmdEnv(t, withAgentTokenFile(context.Background(), f), gitAgentBaseEnv())
			work := t.TempDir()
			env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
				"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
			for k, v := range agentGitConfigEntries(agent.Env) {
				env = append(env, k+"="+v)
			}
			cmd := exec.Command("git", "ls-remote", url+"/owner/repo.git")
			cmd.Dir = work
			cmd.Env = env
			out, _ := cmd.CombinedOutput() // the server answers 404 once authenticated
			if user, pass, authed := creds(); !authed || user != "x-access-token" || pass != agentGitTestToken {
				t.Errorf("server got auth=%v user=%q password=%q, want x-access-token and the token of the file; git:\n%s", authed, user, pass, out)
			}
			for _, dir := range []string{work, tmp, base} {
				if _, err := os.Lstat(filepath.Join(dir, "MARK")); err == nil {
					t.Errorf("a file MARK was made in %s: the path ran as a command", dir)
				}
			}
		})
	}
}

// TestAgentGitEnvNoPromptAndNoDaemonConfig: with the option set, each agent
// command (setAgentEnvIn, setAgentToolEnvIn, the gate builder) has
// GIT_TERMINAL_PROMPT=0 once, named in --preserve-env, and none of
// GIT_CONFIG_GLOBAL, GIT_CONFIG_SYSTEM, GIT_CONFIG_NOSYSTEM and
// GIT_CONFIG_PARAMETERS, also when the test process sets them, the prompt
// name included, and when the caller appends one. With the option empty the
// env is unchanged.
func TestAgentGitEnvNoPromptAndNoDaemonConfig(t *testing.T) {
	denied := []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_PARAMETERS"}
	setDaemonEnv := func(t *testing.T) {
		for _, name := range denied {
			t.Setenv(name, "/daemon/"+name)
		}
		t.Setenv("GIT_TERMINAL_PROMPT", "1")
	}
	check := func(t *testing.T, what string, cmd *exec.Cmd) {
		t.Helper()
		prompts := 0
		for _, kv := range cmd.Env {
			name, v, _ := strings.Cut(kv, "=")
			if name == "GIT_TERMINAL_PROMPT" {
				prompts++
				if v != "0" {
					t.Errorf("%s: GIT_TERMINAL_PROMPT=%q, want 0", what, v)
				}
			}
			for _, d := range denied {
				if name == d {
					t.Errorf("%s: env holds %q", what, kv)
				}
			}
		}
		if prompts != 1 {
			t.Errorf("%s: %d GIT_TERMINAL_PROMPT entries, want 1", what, prompts)
		}
		preserve := ""
		for _, a := range cmd.Args {
			if strings.HasPrefix(a, runAsPreserveFlag) {
				preserve = a
			}
		}
		if !strings.Contains(preserve, "GIT_TERMINAL_PROMPT") {
			t.Errorf("%s: --preserve-env %q lacks GIT_TERMINAL_PROMPT", what, preserve)
		}
		for _, d := range denied {
			if strings.Contains(preserve, d) {
				t.Errorf("%s: --preserve-env %q names %s", what, preserve, d)
			}
		}
	}

	t.Run("option set", func(t *testing.T) {
		setDaemonEnv(t)
		f := startTestAgentTokenFile(t)
		ctx := withAgentTokenFile(context.Background(), f)
		check(t, "agent command", agentCmdEnv(t, ctx, os.Environ()))
		check(t, "agent command, caller entries", agentCmdEnv(t, ctx,
			append(os.Environ(), "GIT_CONFIG_GLOBAL=/caller/config", "GIT_CONFIG_PARAMETERS='core.hookspath'='/caller'", "GIT_TERMINAL_PROMPT=1")))
		check(t, "agent command, no token file", agentCmdEnv(t, context.Background(), os.Environ()))
		tool, err := agentCommand(ctx, "git", "status")
		if err != nil {
			t.Fatal(err)
		}
		setAgentToolEnvIn(tool, os.Environ(), t.TempDir())
		check(t, "tool command", tool)
		gate, err := gateCommandBuilder{}.Build(ctx, t.TempDir(), nil, "make", "test")
		if err != nil {
			t.Fatal(err)
		}
		check(t, "gate command", gate)
	})

	t.Run("option empty: env unchanged", func(t *testing.T) {
		setDaemonEnv(t)
		tokenTestRunAs(t, false)
		env := []string{"PATH=/usr/bin", "GIT_CONFIG_GLOBAL=/caller/config"}
		cmd := agentCmdEnv(t, context.Background(), env)
		if strings.Join(cmd.Env, "\n") != strings.Join(env, "\n") {
			t.Errorf("env with the option empty = %q, want %q", cmd.Env, env)
		}
	})
}

// gateRetryHelperChecker fails its first check with ShouldRetry; each later
// check builds a gate command on its context and records the config entries
// of git of that command.
type gateRetryHelperChecker struct {
	dir     string
	mu      sync.Mutex
	calls   int
	entries []map[string]string
}

func (c *gateRetryHelperChecker) Check(ctx context.Context) (*QualityOutcome, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	if n == 1 {
		return &QualityOutcome{Passed: false, ShouldRetry: true, RetryFeedback: "synthetic gate failure", Attempt: n}, nil
	}
	cmd, err := gateCommandBuilder{}.Build(ctx, c.dir, nil, "true")
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.entries = append(c.entries, agentGitConfigEntries(cmd.Env))
	c.mu.Unlock()
	return &QualityOutcome{Passed: true, Attempt: n}, nil
}

// TestAgentGitRetryGateGetsTheHelperOfTheRun: the gate re-check of the quality
// gate retry (runner.go) runs on a fresh context that keeps the token file of
// the run, so its gate command gets the helper entry of the run.
// mutation pin: the retry gate check of runner.go on context.Background() in place of withAgentTokenFile(context.Background(), ...) -> this test fails
func TestAgentGitRetryGateGetsTheHelperOfTheRun(t *testing.T) {
	e := newAgentWorkspaceEnv(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	// stackingAttemptBackend commits as the daemon user in the agent workspace,
	// a clone with no user.name or user.email; a CI runner has no global
	// identity either, and the commit fails there with "empty ident name".
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	p := &repoTokenProvider{}
	setAgentReadTokenProviderForTest(t, p.provide)
	prevBuilder := quality.CurrentCommandBuilder()
	quality.SetCommandBuilder(gateCommandBuilder{})
	t.Cleanup(func() { quality.SetCommandBuilder(prevBuilder) })

	backend := &stackingAttemptBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{SkipSelfReview: true}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	runner.SetAgentTokenRepoLookup(func(path string) (string, bool) {
		if path == e.repo {
			return "OWNER-A/NAME", true
		}
		return "", false
	})
	checker := &gateRetryHelperChecker{dir: t.TempDir()}
	runner.SetQualityCheckerFactory(func(string, string, string) QualityChecker { return checker })

	task := &Task{ID: "GH-6665", Title: "t", Description: "d", ProjectPath: e.repo, Branch: "pilot/GH-6665", BaseBranch: "main"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = runner.Execute(ctx, task)

	checker.mu.Lock()
	defer checker.mu.Unlock()
	if checker.calls < 2 || len(checker.entries) == 0 {
		t.Fatalf("gate checks = %d, re-checks recorded = %d, want a re-check after the retry", checker.calls, len(checker.entries))
	}
	for i, got := range checker.entries {
		if got["GIT_CONFIG_KEY_0"] != "credential.https://github.com.helper" ||
			!strings.HasPrefix(got["GIT_CONFIG_VALUE_0"], "!'"+filepath.Join(tmp, "pilot-ghtoken-")) ||
			!strings.HasSuffix(got["GIT_CONFIG_VALUE_0"], string(filepath.Separator)+agentGitCredentialHelperName+"'") {
			t.Errorf("re-check %d: config entries = %v, want the helper entry of the run", i+1, got)
		}
	}
}
