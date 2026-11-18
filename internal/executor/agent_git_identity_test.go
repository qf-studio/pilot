package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TASK-651: the git identity of the agent user. No test needs root: sudo is
// the mock of the workspace tests, which runs the command as the present user
// with the environment the daemon hands to sudo.

// isolateHostGitIdentity hides the git identity of the host from the test:
// the global and system config of the test process carry none, and git may
// not guess one from the host name (user.useConfigOnly).
func isolateHostGitIdentity(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tuseConfigOnly = true\n"), 0o644); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

// agentGitRun runs git as the agent user (the agent route of the constructor)
// in dir.
func agentGitRun(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := gitCommandIn(context.Background(), dir, args...)
	if _, ok := agentCmdUser(cmd); !ok {
		t.Fatalf("git %v in %s: not the agent route: %q", args, dir, cmd.Args)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v as the agent user: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRunAsUserGitIdentityCommitAndAmendInTheWorkspace(t *testing.T) {
	e := newAgentWorkspaceEnv(t)
	isolateHostGitIdentity(t)
	wsGit(t, e.repo, "config", "user.name", "Daemon Repo")
	wsGit(t, e.repo, "config", "user.email", "daemon@example.com")
	m := NewWorktreeManager(e.repo)
	m.SetBaseBranch("main")
	ws, cleanup, err := m.createAgentWorkspace(context.Background(), "GH-77", "pilot/GH-77", "")
	if err != nil {
		t.Fatalf("createAgentWorkspace: %v", err)
	}
	t.Cleanup(cleanup)
	// No identity in the config of the workspace, and no guess from the host.
	wsGit(t, ws, "config", "user.useConfigOnly", "true")
	for _, key := range []string{"user.name", "user.email"} {
		if out, err := gitCommandIn(context.Background(), e.repo, "-C", ws, "config", "--get", key).Output(); err == nil {
			t.Fatalf("the workspace config sets %s = %s", key, out)
		}
	}

	if err := os.WriteFile(filepath.Join(ws, "work.txt"), []byte("work\n"), 0o644); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
	agentGitRun(t, ws, "", "add", "work.txt")
	agentGitRun(t, ws, "", "commit", "-q", "-m", "feat: work\n\n"+claudeCoAuthorTrailer)
	const want = "Daemon Repo <daemon@example.com> | Daemon Repo <daemon@example.com>"
	if got := wsGit(t, ws, "log", "-1", "--format=%an <%ae> | %cn <%ce>"); got != want {
		t.Errorf("the commit of the agent user carries %q, want %q", got, want)
	}
	// An amend of the model, as the agent user.
	agentGitRun(t, ws, "", "commit", "-q", "--amend", "--no-edit", "--reset-author")
	if got := wsGit(t, ws, "log", "-1", "--format=%an <%ae> | %cn <%ce>"); got != want {
		t.Errorf("the amend of the model carries %q, want %q", got, want)
	}

	// The amend of the co-author strip, as the agent user.
	g := newGitOperationsForWorkspace(ws, e.repo)
	if err := g.amendHeadWithoutAnthropicCoAuthor(context.Background()); err != nil {
		t.Fatalf("amend as the agent user: %v", err)
	}
	if got := wsGit(t, ws, "log", "-1", "--format=%an <%ae> | %cn <%ce>"); got != want {
		t.Errorf("the amend of the agent user carries %q, want %q", got, want)
	}
	if msg := wsGit(t, ws, "log", "-1", "--format=%B"); strings.Contains(msg, "anthropic.com") {
		t.Errorf("the amend kept the trailer:\n%s", msg)
	}
	if !strings.Contains(strings.Join(sudoCalls(t, e.sudoLog), "\n"), "GIT_AUTHOR_NAME,GIT_AUTHOR_EMAIL,GIT_COMMITTER_NAME,GIT_COMMITTER_EMAIL") {
		t.Errorf("no sudo call keeps the four identity names:\n%s", strings.Join(sudoCalls(t, e.sudoLog), "\n"))
	}
}

func TestRunAsUserGitIdentityEnvDefaults(t *testing.T) {
	isolateHostGitIdentity(t)
	repo := t.TempDir()
	wsGit(t, repo, "init", "-q")
	wantDefault := "GIT_AUTHOR_NAME=Pilot GIT_AUTHOR_EMAIL=pilot@localhost GIT_COMMITTER_NAME=Pilot GIT_COMMITTER_EMAIL=pilot@localhost"
	if got := strings.Join(agentGitIdentityEnv(context.Background(), repo), " "); got != wantDefault {
		t.Errorf("no identity in the daemon's repository: %s, want %s", got, wantDefault)
	}
	wsGit(t, repo, "config", "user.name", "Only Name")
	want := "GIT_AUTHOR_NAME=Only Name GIT_AUTHOR_EMAIL=pilot@localhost GIT_COMMITTER_NAME=Only Name GIT_COMMITTER_EMAIL=pilot@localhost"
	if got := strings.Join(agentGitIdentityEnv(context.Background(), repo), " "); got != want {
		t.Errorf("only user.name set: %s, want %s", got, want)
	}
	if got := strings.Join(agentGitIdentityEnv(context.Background(), ""), " "); got != wantDefault {
		t.Errorf("no daemon's repository: %s, want %s", got, wantDefault)
	}
}

// A model process gets the identity of the workspace it runs in, and the four
// names are on the --preserve-env list of sudo; outside a workspace it gets
// the defaults.
func TestRunAsUserGitIdentityModelProcessAndPreserveList(t *testing.T) {
	stubRunAsGOOS(t)
	root := t.TempDir()
	setRunAsStateForTest(t, gitRouteSettings(t, root), "")
	project := "0123abcd"
	agentGitIdentities.Store(project, gitIdentityEnv("Repo Name", "repo@example.com"))
	t.Cleanup(func() { agentGitIdentities.Delete(project) })
	ws := filepath.Join(root, agentWorkspacePrefix+project+"-GH-5-1700000000")

	for _, tc := range []struct {
		name, dir, wantName, wantEmail string
	}{
		{"in a workspace", filepath.Join(ws, "sub"), "Repo Name", "repo@example.com"},
		{"outside a workspace", t.TempDir(), "Pilot", "pilot@localhost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := agentCommand(context.Background(), "claude", "-p", "x")
			if err != nil {
				t.Fatal(err)
			}
			cmd.Dir = tc.dir
			setAgentEnv(cmd, append(os.Environ(), "GIT_AUTHOR_NAME=daemon-env"))
			env := map[string]string{}
			for _, kv := range cmd.Env {
				k, v, _ := strings.Cut(kv, "=")
				env[k] = v
			}
			var preserve string
			for _, a := range cmd.Args {
				if strings.HasPrefix(a, runAsPreserveFlag) {
					preserve = "," + strings.TrimPrefix(a, runAsPreserveFlag) + ","
				}
			}
			for k, want := range map[string]string{
				"GIT_AUTHOR_NAME": tc.wantName, "GIT_AUTHOR_EMAIL": tc.wantEmail,
				"GIT_COMMITTER_NAME": tc.wantName, "GIT_COMMITTER_EMAIL": tc.wantEmail,
			} {
				if env[k] != want {
					t.Errorf("%s = %q, want %q", k, env[k], want)
				}
				if !strings.Contains(preserve, ","+k+",") {
					t.Errorf("%s is not on the --preserve-env list %q", k, preserve)
				}
			}
		})
	}
}
