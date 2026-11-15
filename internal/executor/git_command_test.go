package executor

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// TASK-651 (design 4.2): the one constructor of every git command of the
// package and its route to the agent user. None of these tests needs root:
// sudo is a mock script on PATH.

// mockSudoExec puts a fake sudo first on PATH that records its argv (its name
// first, NUL separated) in the returned file, drops the sudo flags up to and
// including "--" and runs the rest as the present user. mockSudo and
// mockSudoBackground only record; this one runs the command.
func mockSudoExec(t *testing.T) (logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock sudo is a shell script")
	}
	dir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "sudo-argv")
	body := fmt.Sprintf(`#!/bin/sh
printf '%%s\0' "${0##*/}" "$@" > '%s'
while [ $# -gt 0 ]; do
  a=$1
  shift
  [ "$a" = "--" ] && break
done
exec "$@"
`, logPath)
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(body), 0o755); err != nil { //nolint:gosec // mock must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// mockGitEnv puts a fake git first on PATH that writes its environment to the
// returned file.
func mockGitEnv(t *testing.T) (envPath string) {
	t.Helper()
	dir := t.TempDir()
	envPath = filepath.Join(t.TempDir(), "git-env")
	body := fmt.Sprintf("#!/bin/sh\nenv > '%s'\n", envPath)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(body), 0o755); err != nil { //nolint:gosec // mock must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return envPath
}

// gitRouteSettings is the option set with root as the workspace root.
func gitRouteSettings(t *testing.T, root string) *runAsSettings {
	t.Helper()
	s := testRunAsSettings(t)
	s.workspaceRoot = root
	return s
}

// workspaceDir makes a directory below root and returns it.
func workspaceDir(t *testing.T, root string) string {
	t.Helper()
	ws := filepath.Join(root, "ws-1")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	return ws
}

func joinNUL(s []string) string { return strings.Join(s, "\x00") }

// assertSameCommand checks argv, cmd.Dir and cmd.Env of got against want.
func assertSameCommand(t *testing.T, got, want *exec.Cmd) {
	t.Helper()
	if got.Err != nil {
		t.Fatalf("plain route: Err = %v", got.Err)
	}
	if got.Path != want.Path || joinNUL(got.Args) != joinNUL(want.Args) {
		t.Errorf("argv = %q (%s), want %q (%s)", got.Args, got.Path, want.Args, want.Path)
	}
	if got.Dir != want.Dir {
		t.Errorf("Dir = %q, want %q", got.Dir, want.Dir)
	}
	if (got.Env == nil) != (want.Env == nil) || joinNUL(got.Env) != joinNUL(want.Env) {
		t.Errorf("Env differs from the inline command (nil: got %v, want %v)", got.Env == nil, want.Env == nil)
	}
}

func TestGitCommandPlainRouteIsTheInlineCommand(t *testing.T) {
	stubRunAsGOOS(t)
	root := t.TempDir()
	dir := t.TempDir() // outside root
	g := NewGitOperations(dir)
	hardenedArgs := func(args ...string) []string {
		return append(append(secretpatterns.GitScanConfig(), "-c", "commit.gpgSign=false", "-c", "core.fsmonitor=false"), args...)
	}
	sites := []struct {
		name  string
		build func(ctx context.Context) *exec.Cmd
		want  func(ctx context.Context) *exec.Cmd
	}{
		{"-C site (repo_guardrail)", func(ctx context.Context) *exec.Cmd {
			return gitCommand(ctx, dir, "-C", dir, "remote", "get-url", "origin")
		}, func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin")
		}},
		{"cmd.Dir site (preflight)", func(ctx context.Context) *exec.Cmd {
			return gitCommandIn(ctx, dir, "status", "--porcelain", "-z")
		}, func(ctx context.Context) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "-z")
			cmd.Dir = dir
			return cmd
		}},
		{"GitOperations site (push)", func(ctx context.Context) *exec.Cmd {
			return g.gitCmd(ctx, "push", "-u", "origin", "b")
		}, func(ctx context.Context) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "git", "push", "-u", "origin", "b")
			cmd.Dir = dir
			return cmd
		}},
		{"gitHardened", func(ctx context.Context) *exec.Cmd {
			return g.gitHardened(ctx, "log", "-1")
		}, func(ctx context.Context) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "git", hardenedArgs("log", "--no-show-signature", "-1")...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GIT_NO_REPLACE_OBJECTS=1")
			return cmd
		}},
		{"gitHardened commit-tree with author env", func(ctx context.Context) *exec.Cmd {
			cmd := g.gitHardened(ctx, "commit-tree", "t")
			appendGitEnv(cmd, "GIT_AUTHOR_NAME=a", "GIT_COMMITTER_NAME=c")
			return cmd
		}, func(ctx context.Context) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "git", hardenedArgs("commit-tree", "t")...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GIT_NO_REPLACE_OBJECTS=1")
			cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=a", "GIT_COMMITTER_NAME=c")
			return cmd
		}},
		{"secret gate site", func(ctx context.Context) *exec.Cmd {
			cmd := gitCommandIn(ctx, dir, "rev-parse", "--git-common-dir")
			appendGitEnv(cmd, "GIT_NO_REPLACE_OBJECTS=1")
			return cmd
		}, func(ctx context.Context) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-common-dir")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GIT_NO_REPLACE_OBJECTS=1")
			return cmd
		}},
	}
	states := []struct {
		name string
		s    *runAsSettings
	}{
		{"option empty", nil},
		{"option set, directory outside the root", gitRouteSettings(t, root)},
	}
	for _, st := range states {
		for _, site := range sites {
			t.Run(st.name+"/"+site.name, func(t *testing.T) {
				setRunAsStateForTest(t, st.s, "")
				ctx := context.Background()
				assertSameCommand(t, site.build(ctx), site.want(ctx))
			})
		}
	}
}

func TestGitCommandAgentRouteRunsGitThroughSudo(t *testing.T) {
	stubRunAsGOOS(t)
	logPath := mockSudoExec(t)
	root := t.TempDir()
	ws := workspaceDir(t, root)
	setRunAsStateForTest(t, gitRouteSettings(t, root), "")

	cmd := gitCommandIn(context.Background(), ws, "init", "-q")
	if cmd.Err != nil {
		t.Fatalf("agent route: Err = %v", cmd.Err)
	}
	if u, ok := agentCmdUser(cmd); !ok || u != "pilot-agent" {
		t.Fatalf("argv = %q, want sudo -n -u pilot-agent ...", cmd.Args)
	}
	if cmd.Dir != ws {
		t.Errorf("Dir = %q, want %q", cmd.Dir, ws)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git through the mock sudo: %v: %s", err, out)
	}
	argv := sudoArgv(t, logPath)
	assertSudoSpawn(t, argv, "pilot-agent", "git")
	if tail := argv[len(argv)-2:]; joinNUL(tail) != joinNUL([]string{"init", "-q"}) {
		t.Errorf("git arguments after -- = %q, want init -q", tail)
	}
	if _, err := os.Stat(filepath.Join(ws, ".git")); err != nil {
		t.Errorf("git did not run in the workspace: %v", err)
	}
}

func TestGitCommandRouteInsideRoot(t *testing.T) {
	realRoot := t.TempDir()
	ws := workspaceDir(t, realRoot)
	linkDir := t.TempDir()
	linkRoot := filepath.Join(linkDir, "root-link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink: %v", err)
	}
	outside := t.TempDir()
	tests := []struct {
		name      string
		root      string
		dir       string
		wantAgent bool
	}{
		{"directory inside the root", realRoot, ws, true},
		{"the root itself", realRoot, realRoot, true},
		{"root behind a symbolic link", linkRoot, ws, true},
		{"directory reached through the link", realRoot, filepath.Join(linkRoot, "ws-1"), true},
		{"trailing slash on root and directory", realRoot + "/", ws + "/", true},
		{"directory outside the root", realRoot, outside, false},
		{"sibling with the root as name prefix", realRoot, realRoot + "-other", false},
		{"root that does not exist", filepath.Join(outside, "gone"), ws, false},
	}
	if err := os.MkdirAll(realRoot+"-other", 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(realRoot + "-other") })
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubRunAsGOOS(t)
			setRunAsStateForTest(t, gitRouteSettings(t, tc.root), "")
			cmd := gitCommand(context.Background(), tc.dir, "-C", tc.dir, "status")
			if cmd.Err != nil {
				t.Fatalf("Err = %v", cmd.Err)
			}
			_, agent := agentCmdUser(cmd)
			if agent != tc.wantAgent {
				t.Errorf("agent route = %v, want %v (argv %q)", agent, tc.wantAgent, cmd.Args)
			}
			if !agent && joinNUL(cmd.Args) != joinNUL([]string{"git", "-C", tc.dir, "status"}) {
				t.Errorf("plain argv = %q", cmd.Args)
			}
		})
	}
}

func TestGitCommandFailsClosed(t *testing.T) {
	stubRunAsGOOS(t)
	root := t.TempDir()
	ws := workspaceDir(t, root)
	runs := []struct {
		name string
		run  func(*exec.Cmd) error
	}{
		{"Run", func(c *exec.Cmd) error { return c.Run() }},
		{"Output", func(c *exec.Cmd) error { _, err := c.Output(); return err }},
		{"CombinedOutput", func(c *exec.Cmd) error { _, err := c.CombinedOutput(); return err }},
		{"Start", func(c *exec.Cmd) error {
			err := c.Start()
			if err == nil {
				_ = c.Wait()
			}
			return err
		}},
	}
	tests := []struct {
		name    string
		failed  string
		dir     string
		wantErr string
	}{
		{"failed preflight", "sudo -n -u pilot-agent true", ws, "run_as_user preflight failed: sudo -n -u pilot-agent true"},
		{"directory that cannot be resolved", "", filepath.Join(root, "missing"), "run_as_user: resolve git directory"},
	}
	for _, tc := range tests {
		for _, r := range runs {
			t.Run(tc.name+"/"+r.name, func(t *testing.T) {
				logPath := mockSudoExec(t)
				setRunAsStateForTest(t, gitRouteSettings(t, root), tc.failed)
				cmd := gitCommandIn(context.Background(), tc.dir, "--version")
				err := r.run(cmd)
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("%s error = %v, want one containing %q", r.name, err, tc.wantErr)
				}
				if got := sudoArgv(t, logPath); got != nil {
					t.Errorf("a process started: %q", got)
				}
			})
		}
	}
}

func TestGitCommandCredentialsRefusedOnAgentRoute(t *testing.T) {
	stubRunAsGOOS(t)
	logPath := mockSudoExec(t)
	root := t.TempDir()
	ws := workspaceDir(t, root)
	called := 0
	SetGitCredentialProvider(func(context.Context) (string, error) {
		called++
		return "fake-installation-token", nil
	})
	t.Cleanup(func() { SetGitCredentialProvider(nil) })

	tests := []struct {
		name    string
		failed  string
		wantErr string
	}{
		{"agent route", "", "hand-over leg"},
		{"failed-closed command", "sudo -n -u pilot-agent true", "run_as_user preflight failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called = 0
			setRunAsStateForTest(t, gitRouteSettings(t, root), tc.failed)
			ctx := context.Background()
			cmd := withGitCredentials(ctx, NewGitOperations(ws).gitCmd(ctx, "push", "origin", "b"))
			if cmd.Err == nil || !strings.Contains(cmd.Err.Error(), tc.wantErr) {
				t.Errorf("Err = %v, want one containing %q", cmd.Err, tc.wantErr)
			}
			if tc.failed == "" && !errors.Is(cmd.Err, errGitCredentialsAgentRoute) {
				t.Errorf("Err = %v, want errGitCredentialsAgentRoute", cmd.Err)
			}
			if called != 0 {
				t.Errorf("the token provider was called %d times", called)
			}
			for _, kv := range cmd.Env {
				if strings.HasPrefix(kv, "PILOT_GIT_TOKEN=") || strings.HasPrefix(kv, "GIT_ASKPASS=") {
					t.Errorf("a credential variable was set: %s", kv)
				}
			}
			if err := cmd.Run(); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Run error = %v, want one containing %q", err, tc.wantErr)
			}
			if got := sudoArgv(t, logPath); got != nil {
				t.Errorf("a process started: %q", got)
			}
		})
	}

	// The plain route keeps the token as before.
	t.Run("plain route", func(t *testing.T) {
		called = 0
		setRunAsStateForTest(t, gitRouteSettings(t, root), "")
		ctx := context.Background()
		cmd := withGitCredentials(ctx, NewGitOperations(t.TempDir()).gitCmd(ctx, "push", "origin", "b"))
		if cmd.Err != nil || called != 1 || !envHas(cmd.Env, "PILOT_GIT_TOKEN=fake-installation-token") {
			t.Errorf("plain route: Err=%v called=%d token set=%v", cmd.Err, called, envHas(cmd.Env, "PILOT_GIT_TOKEN=fake-installation-token"))
		}
	})
}

func envHas(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func TestGitCommandAgentRouteEnvironment(t *testing.T) {
	stubRunAsGOOS(t)
	_ = mockSudoExec(t)
	envPath := mockGitEnv(t)
	root := t.TempDir()
	ws := workspaceDir(t, root)
	t.Setenv("DAEMON_ONLY_SECRET", "daemon-only-value")
	t.Setenv("GIT_DIR", "/daemon/git-dir")
	setRunAsStateForTest(t, gitRouteSettings(t, root), "")

	ctx := context.Background()
	cmd := NewGitOperations(ws).gitHardened(ctx, "commit-tree", "t")
	appendGitEnv(cmd, "GIT_AUTHOR_NAME=a", "GIT_COMMITTER_DATE=d", "GIT_EXEC_PATH=/evil")
	if _, ok := agentCmdUser(cmd); !ok {
		t.Fatalf("not the agent route: %q", cmd.Args)
	}
	var preserve string
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, runAsPreserveFlag) {
			preserve = strings.TrimPrefix(a, runAsPreserveFlag)
		}
	}
	if preserve != strings.Join(agentEnvNames(cmd.Env), ",") {
		t.Errorf("--preserve-env=%s does not list the names of cmd.Env %q", preserve, agentEnvNames(cmd.Env))
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run: %v: %s", err, out)
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("git did not run: %v", err)
	}
	gitEnv := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for _, want := range []string{"GIT_NO_REPLACE_OBJECTS=1", "GIT_AUTHOR_NAME=a", "GIT_COMMITTER_DATE=d"} {
		if !envHas(gitEnv, want) {
			t.Errorf("%s missing from the environment of git", want)
		}
	}
	for _, kv := range gitEnv {
		for _, name := range []string{"DAEMON_ONLY_SECRET=", "GIT_DIR=", "GIT_EXEC_PATH="} {
			if strings.HasPrefix(kv, name) {
				t.Errorf("daemon variable reached git: %s", kv)
			}
		}
	}
}

func TestRunAsUserPreflightWorkspaceRoot(t *testing.T) {
	tests := []struct {
		name      string
		dir       string
		wantRoot  string
		wantCheck string
	}{
		{"empty is the default root", "", defaultRunAsWorkspaceDir, ""},
		{"configured root", "/srv/agent-ws", "/srv/agent-ws", ""},
		{"relative root fails closed", "agent-ws", "agent-ws", "run_as_workspace_dir is not an absolute path"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRunAsStateForTest(t, nil, "")
			_ = mockSudo(t, 0)
			stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
			PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents", RunAsWorkspaceDir: tc.dir}, "")
			if s := runAsSettingsNow(); s == nil || s.workspaceRoot != tc.wantRoot {
				t.Fatalf("settings = %+v, want workspace root %q", s, tc.wantRoot)
			}
			err := runAsSpawnError()
			if tc.wantCheck == "" && err != nil || tc.wantCheck != "" && (err == nil || !strings.Contains(err.Error(), tc.wantCheck)) {
				t.Errorf("preflight error = %v, want %q", err, tc.wantCheck)
			}
		})
	}
}

func TestRunAsUserPreflightReRunsWhenWorkspaceRootChanges(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	logPath := mockSudo(t, 0)
	stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
	cfg := &ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents"}
	PreflightRunAsUser(cfg, "")
	_ = sudoArgv(t, logPath)

	PreflightRunAsUser(cfg, "")
	if got := sudoArgv(t, logPath); got != nil {
		t.Errorf("same settings ran the preflight again: %q", got)
	}
	cfg.RunAsWorkspaceDir = "/srv/other-root"
	PreflightRunAsUser(cfg, "")
	if got := sudoArgv(t, logPath); got == nil {
		t.Error("a new workspace root did not run the preflight again")
	}
	if s := runAsSettingsNow(); s == nil || s.workspaceRoot != "/srv/other-root" {
		t.Errorf("settings = %+v, want the new root", s)
	}
}

// TestGitSourceNoInlineGitCommand fails for every call of exec.Command or
// exec.CommandContext with the literal "git" as command in a non-test file of
// the package other than git_command.go.
func TestGitSourceNoInlineGitCommand(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "git_command.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++
		for _, line := range inlineGitCommandLines(f, fset) {
			t.Errorf("%s:%d: inline git command; build it with gitCommand, gitCommandIn or gitCmd (git_command.go)", name, line)
		}
	}
	if checked == 0 {
		t.Fatal("no source file checked")
	}
}

// inlineGitCommandLines returns the lines of the exec.Command(..."git"...) and
// exec.CommandContext(<any context>, "git", ...) calls in f.
func inlineGitCommandLines(f *ast.File, fset *token.FileSet) []int {
	execName := ""
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "os/exec" {
			execName = "exec"
			if imp.Name != nil {
				execName = imp.Name.Name
			}
		}
	}
	if execName == "" {
		return nil
	}
	var lines []int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != execName {
			return true
		}
		argIdx := -1
		switch sel.Sel.Name {
		case "Command":
			argIdx = 0
		case "CommandContext":
			argIdx = 1
		}
		if argIdx < 0 || len(call.Args) <= argIdx {
			return true
		}
		if lit, ok := call.Args[argIdx].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if v, err := strconv.Unquote(lit.Value); err == nil && v == "git" {
				lines = append(lines, fset.Position(call.Pos()).Line)
			}
		}
		return true
	})
	return lines
}
