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
			"GIT_CONFIG_VALUE_0": filepath.Join(f.dir, "git-credential-pilot"),
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
		if got["GIT_CONFIG_COUNT"] != "1" || got["GIT_CONFIG_KEY_0"] != "credential.https://github.com.helper" || got["GIT_CONFIG_VALUE_0"] != f.helper {
			t.Errorf("gate config entries = %v", got)
		}
		assertNoToken(t, "gate env", cmd.Env)
	})

	t.Run("entry of the code first: the helper takes the next index", func(t *testing.T) {
		f := &agentTokenFile{helper: "/run/git-credential-pilot"}
		env := appendAgentGitCredentialEntries([]string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/dev/null"}, f)
		got := agentGitConfigEntries(env)
		if got["GIT_CONFIG_COUNT"] != "2" || got["GIT_CONFIG_KEY_0"] != "core.hooksPath" ||
			got["GIT_CONFIG_KEY_1"] != "credential.https://github.com.helper" || got["GIT_CONFIG_VALUE_1"] != f.helper {
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
