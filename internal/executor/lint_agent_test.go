package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

// TASK-651, gates leg part 4: with run_as_user set the lint run and the lint
// fix of autoFixLint run as the agent user in the workspace; a failed restore
// of the settings file fails the run. sudo is the mock of the package
// (mockSudoAgent); no test needs root.

// lintDaemonEnv are the names of the daemon the lint commands never get.
var lintDaemonEnv = map[string]string{
	"DAEMON_ONLY_SECRET":       "daemon-only-secret",
	"ANTHROPIC_API_KEY":        "fake-api-key",
	"ANTHROPIC_AUTH_TOKEN":     "fake-auth-token",
	"CLAUDE_CODE_OAUTH_TOKEN":  "fake-oauth-token",
	"GITHUB_TOKEN":             testutil.FakeGitHubToken,
	"ANTHROPIC_CUSTOM_HEADERS": "X-Daemon: daemon-header",
}

// toolsOnlyPath makes a directory with links to sh and git only and returns
// it: a PATH without the lint program of the host.
func toolsOnlyPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"sh", "git"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// mockLint puts a fake golangci-lint first on PATH. Each call writes its
// working directory and its environment to <out>/<check|fix>.pwd and .env. A
// check reports an issue until a fix ran; a fix exits 0.
func mockLint(t *testing.T) (out string) {
	t.Helper()
	dir := t.TempDir()
	out = t.TempDir()
	body := fmt.Sprintf(`#!/bin/sh
out='%[1]s'
kind=check
for a in "$@"; do [ "$a" = "--fix" ] && kind=fix; done
pwd > "$out/$kind.pwd"
env > "$out/$kind.env"
if [ $kind = fix ]; then
  : > "$out/fixed"
  exit 0
fi
[ -f "$out/fixed" ] && exit 0
echo "a.go:1:1: lint issue"
exit 1
`, out)
	if err := os.WriteFile(filepath.Join(dir, "golangci-lint"), []byte(body), 0o755); err != nil { //nolint:gosec // mock must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return out
}

// lintWorkspace is a git repository with a go.mod in a workspace below a new
// workspace root; it returns the root and the workspace.
func lintWorkspace(t *testing.T) (root, ws string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws = filepath.Join(root, "ws-1")
	writeTestFile(t, filepath.Join(ws, "go.mod"), "module example.com/lint\n\ngo 1.21\n")
	wsGit(t, ws, "init", "-q", "-b", "main")
	// The identity is in the config of the repository: the amend of autoFixLint
	// commits without -c, and a host that cannot name itself gives git no
	// identity to guess (exit 128).
	wsGit(t, ws, "config", "user.email", "t@example.com")
	wsGit(t, ws, "config", "user.name", "T")
	wsGit(t, ws, "add", ".")
	wsGit(t, ws, "commit", "-q", "-m", "init")
	return root, ws
}

func setLintDaemonEnv(t *testing.T) {
	t.Helper()
	clearModelEnvForTest(t)
	for name, v := range lintDaemonEnv {
		t.Setenv(name, v)
	}
}

func TestAutoFixLintRunsAsAgent(t *testing.T) {
	isolateHostGitIdentity(t)
	t.Run("lint and fix run as the agent user", func(t *testing.T) {
		stubRunAsGOOS(t)
		root, ws := lintWorkspace(t)
		out := mockLint(t)
		sudoLog := mockSudoAgent(t, "", "")
		setRunAsStateForTest(t, gitRouteSettings(t, root), "")
		setLintDaemonEnv(t)

		res := NewGitOperations(ws).autoFixLint(context.Background(), "main")
		if !res.FixedAll || !res.Clean {
			t.Fatalf("autoFixLint = %+v, want the fix applied", res)
		}

		calls := strings.Join(sudoCalls(t, sudoLog), "\n")
		for _, want := range []string{
			"-n -u pilot-agent",
			" -- golangci-lint run --new-from-rev=origin/main ./...",
			" -- golangci-lint run --fix ./...",
		} {
			if !strings.Contains(calls, want) {
				t.Errorf("sudo log lacks %q:\n%s", want, calls)
			}
		}
		for _, kind := range []string{"check", "fix"} {
			if got := strings.TrimSpace(readTestFile(t, filepath.Join(out, kind+".pwd"))); got != ws {
				t.Errorf("%s command ran in %q, want the task directory %q", kind, got, ws)
			}
			env := readTestFile(t, filepath.Join(out, kind+".env"))
			for _, line := range strings.Split(env, "\n") {
				name, _, _ := strings.Cut(line, "=")
				if _, daemon := lintDaemonEnv[name]; daemon {
					t.Errorf("%s command environment has %s of the daemon", kind, name)
				}
			}
			for name, v := range lintDaemonEnv {
				if strings.Contains(env, v) {
					t.Errorf("%s command environment has the value of %s of the daemon", kind, name)
				}
			}
		}
	})

	t.Run("a missing lint program gives the result of today", func(t *testing.T) {
		stubRunAsGOOS(t)
		root, ws := lintWorkspace(t)
		t.Setenv("PATH", toolsOnlyPath(t))
		sudoLog := mockSudoAgent(t, "", "")
		g := NewGitOperations(ws)

		want := g.autoFixLint(context.Background(), "main")
		if calls := sudoCalls(t, sudoLog); len(calls) != 0 {
			t.Fatalf("option empty: sudo ran: %q", calls)
		}
		if want.Clean || want.FixedAll {
			t.Fatalf("option empty: a missing program gave %+v", want)
		}

		setRunAsStateForTest(t, gitRouteSettings(t, root), "")
		got := g.autoFixLint(context.Background(), "main")
		if !reflect.DeepEqual(got, want) {
			t.Errorf("option set: autoFixLint = %+v, want the result of the option empty %+v", got, want)
		}
		if calls := strings.Join(sudoCalls(t, sudoLog), "\n"); !strings.Contains(calls, " -- golangci-lint run") {
			t.Errorf("option set: the lint program did not run through sudo:\n%s", calls)
		}
	})

	t.Run("option empty: no sudo call", func(t *testing.T) {
		_, ws := lintWorkspace(t)
		out := mockLint(t)
		sudoLog := mockSudoAgent(t, "", "")
		setRunAsStateForTest(t, nil, "")

		res := NewGitOperations(ws).autoFixLint(context.Background(), "main")
		if !res.FixedAll {
			t.Fatalf("autoFixLint = %+v, want the fix applied", res)
		}
		if calls := sudoCalls(t, sudoLog); len(calls) != 0 {
			t.Errorf("sudo ran with the option empty: %q", calls)
		}
		if got := strings.TrimSpace(readTestFile(t, filepath.Join(out, "fix.pwd"))); got != ws {
			t.Errorf("fix command ran in %q, want %q", got, ws)
		}
	})
}

// TestRestoreHookSettingsFailureFailsRunAsAgent: with the option set a restore
// that fails makes the result of the run a failure that names the file; with
// it empty the warning of before stays and the result is untouched.
func TestRestoreHookSettingsFailureFailsRunAsAgent(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	settingsPath := filepath.Join(t.TempDir(), ".claude", "settings.json")
	failing := func() error {
		return RestoreHookSettings(settingsPath, t.TempDir(), func() error { return errors.New("write as the agent user failed") }, true)
	}

	t.Run("option set", func(t *testing.T) {
		stubRunAsGOOS(t)
		setRunAsStateForTest(t, testRunAsSettings(t), "")
		err := hookSettingsRestore(log, settingsPath, runAsSettingsNow() != nil, failing)()
		if err == nil {
			t.Fatal("the restore error was dropped")
		}
		res, runErr := failRunOnSettingsRestore(log, "GH-1", &ExecutionResult{TaskID: "GH-1", Success: true}, nil, err)
		if res.Success {
			t.Error("the run succeeded after a failed restore")
		}
		if !strings.Contains(res.Error, settingsPath) {
			t.Errorf("result error %q does not name %s", res.Error, settingsPath)
		}
		if runErr == nil || !strings.Contains(runErr.Error(), settingsPath) {
			t.Errorf("run error = %v, want one that names %s", runErr, settingsPath)
		}

		res, _ = failRunOnSettingsRestore(log, "GH-1", nil, errors.New("earlier"), err)
		if res == nil || res.Success || res.TaskID != "GH-1" {
			t.Errorf("a nil result = %+v, want a failure of the task", res)
		}
	})

	t.Run("option empty", func(t *testing.T) {
		setRunAsStateForTest(t, nil, "")
		if err := hookSettingsRestore(log, settingsPath, runAsSettingsNow() != nil, failing)(); err != nil {
			t.Errorf("option empty: restore returned %v, want the warning only", err)
		}
	})
}

// settingsBreakingBackend turns .claude/settings.json of the directory it runs
// in into a directory that holds a file, so the removal of the settings file
// Pilot created for the run fails in the deferred restore of
// executeWithOptions.
type settingsBreakingBackend struct {
	retryPathRecordingBackend
}

func (b *settingsBreakingBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	settingsPath := filepath.Join(opts.ProjectPath, ".claude", "settings.json")
	_ = os.Remove(settingsPath)
	if err := os.MkdirAll(settingsPath, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(settingsPath, "block"), []byte("x"), 0o600); err != nil {
		return nil, err
	}
	return b.retryPathRecordingBackend.Execute(ctx, opts)
}

// mutation pin: the deferred restore of executeWithOptions drops the error of hookRestoreFunc (no failRunOnSettingsRestore) -> this test fails
//
// TestSettingsRestoreFailureFailsRunThroughExecute (GH-6626 item 1): with the
// option set, a failed restore of the settings file in the deferred cleanup of
// executeWithOptions makes the result of the run a failure that names the
// file, and the run error carries it.
func TestSettingsRestoreFailureFailsRunThroughExecute(t *testing.T) {
	e := newAgentWorkspaceEnv(t)
	backend := &settingsBreakingBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	runner.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	task := &Task{ID: "GH-6626", Title: "t", Description: "d", ProjectPath: e.repo, Branch: "pilot/GH-6626", CreatePR: true, BaseBranch: "main"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := runner.executeWithOptions(ctx, task, true)
	if len(backend.paths()) == 0 {
		t.Fatal("the backend never ran")
	}
	const want = "restore of the claude settings file"
	if res == nil || res.Success || !strings.Contains(res.Error, want) || !strings.Contains(res.Error, filepath.Join(".claude", "settings.json")) {
		t.Errorf("result = %+v, want a failure whose Error names the settings file (%q)", res, want)
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("run error = %v, want one with %q", err, want)
	}
}

// settingsBreakingCommitBackend breaks the settings file as
// settingsBreakingBackend does and commits a file, so the run reaches the PR
// creation before the deferred restore fails.
type settingsBreakingCommitBackend struct {
	settingsBreakingBackend
}

func (b *settingsBreakingCommitBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	if _, err := b.settingsBreakingBackend.Execute(ctx, opts); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(opts.ProjectPath, "work.go"), []byte("package work\n"), 0o600); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"add", "work.go"}, {"commit", "-m", "feat: work"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = opts.ProjectPath
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git %v: %v: %s", args, err, out)
		}
	}
	return &BackendResult{Success: true, Output: "done"}, nil
}

// mutation pin: the deferred restore of executeWithOptions fails a run that already opened a PR (failRunOnSettingsRestore not reached with PRUrl set) -> this test fails
//
// TestSettingsRestoreFailureKeepsOpenPRThroughExecute (GH-6646 item 2): with
// the option set, a run that opened a PR keeps Success, its PR and a nil run
// error when the deferred restore of the settings file fails; the failure
// reaches IntentWarning.
func TestSettingsRestoreFailureKeepsOpenPRThroughExecute(t *testing.T) {
	e := newAgentWorkspaceEnv(t)
	backend := &settingsBreakingCommitBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{SkipSelfReview: true}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	runner.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	creator := &bodyCapturingPRCreator{}
	runner.SetPRCreator(creator)
	task := &Task{ID: "GH-6646", Title: "t", Description: "d", ProjectPath: e.repo, Branch: "pilot/GH-6646", CreatePR: true, BaseBranch: "main", SourceAdapter: "linear", SkipQualityGates: true}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := runner.executeWithOptions(ctx, task, true)
	if len(backend.paths()) == 0 {
		t.Fatal("the backend never ran")
	}
	if res == nil || res.PRUrl == "" {
		t.Fatalf("result = %+v, want a run that opened a PR", res)
	}
	creator.only(t)
	if !res.Success {
		t.Errorf("a run with an open PR failed after the settings restore: Error = %q", res.Error)
	}
	if err != nil {
		t.Errorf("run error = %v, want nil on a run with an open PR", err)
	}
	const want = "restore of the claude settings file"
	if !strings.Contains(res.IntentWarning, want) || !strings.Contains(res.IntentWarning, filepath.Join(".claude", "settings.json")) {
		t.Errorf("IntentWarning = %q, want the restore failure naming the settings file", res.IntentWarning)
	}
	if strings.Contains(res.Error, want) {
		t.Errorf("result Error = %q, want the restore failure in IntentWarning only", res.Error)
	}
}

// mutation pin: the co-author restore with run_as_user empty logs "Failed to restore Claude hook settings" instead of "Failed to restore Claude settings" -> this test fails
//
// TestCoAuthorSettingsRestoreKeepsLogTextThroughExecute (GH-6626 item 2):
// with the option empty and hooks off, a failed restore of the co-author
// settings file logs the warning text of before and the run result does not
// name the file.
func TestCoAuthorSettingsRestoreKeepsLogTextThroughExecute(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	repo, remote := setupTestRepoWithRemote(t)
	t.Cleanup(func() { _ = os.RemoveAll(repo); _ = os.RemoveAll(remote) })
	backend := &settingsBreakingBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: boolPtr(true)}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	logs := &syncBuffer{}
	runner.log = slog.New(slog.NewTextHandler(logs, nil))
	task := &Task{ID: "GH-6626", Title: "t", Description: "d", ProjectPath: repo, Branch: "pilot/GH-6626", CreatePR: true}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, _ := runner.executeWithOptions(ctx, task, true)
	if len(backend.paths()) == 0 {
		t.Fatal("the backend never ran")
	}
	out := logs.String()
	if !strings.Contains(out, `msg="Failed to restore Claude settings"`) {
		t.Errorf("log lacks the warning \"Failed to restore Claude settings\":\n%s", out)
	}
	if strings.Contains(out, "Failed to restore Claude hook settings") {
		t.Errorf("co-author restore logged the hook settings text:\n%s", out)
	}
	if res != nil && strings.Contains(res.Error, "restore of the claude settings file") {
		t.Errorf("option empty: the restore failure reached the result: %q", res.Error)
	}
}

// mutation pin: a failed settings restore with run_as_user set flips Success to false on a run that already opened a PR -> this test fails
//
// TestFailRunOnSettingsRestoreKeepsOpenPR (GH-6626 item 3): a run that has a
// PR keeps Success and its run error; the restore failure goes to the log and
// to IntentWarning.
func TestFailRunOnSettingsRestoreKeepsOpenPR(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	restoreErr := errors.New("restore of the claude settings file /ws/.claude/settings.json failed with run_as_user set: boom")
	in := &ExecutionResult{TaskID: "GH-1", Success: true, PRUrl: "https://github.com/OWNER-A/NAME/pull/7"}

	res, runErr := failRunOnSettingsRestore(log, "GH-1", in, nil, restoreErr)
	if res == nil || !res.Success || res.PRUrl != in.PRUrl {
		t.Fatalf("result = %+v, want the successful run with its PR", res)
	}
	if res.Error != "" {
		t.Errorf("result Error = %q, want none on a run with an open PR", res.Error)
	}
	if runErr != nil {
		t.Errorf("run error = %v, want nil", runErr)
	}
	if !strings.Contains(res.IntentWarning, "/ws/.claude/settings.json") {
		t.Errorf("IntentWarning = %q, want the restore failure", res.IntentWarning)
	}
	if !strings.Contains(buf.String(), "level=ERROR") || !strings.Contains(buf.String(), "pr_url=") {
		t.Errorf("want an Error line with the PR, got:\n%s", buf.String())
	}

	res, _ = failRunOnSettingsRestore(log, "GH-1", &ExecutionResult{TaskID: "GH-1", Success: true}, nil, restoreErr)
	if res.Success {
		t.Error("a run with no PR still succeeds after a failed restore")
	}
}

// mutation pin: the co-author restore with run_as_user set only logs a failed removal of the empty .claude directory -> this test fails
//
// TestCoAuthorSettingsRestoreDirRemovalFailure (GH-6626 item 4): with the
// option set, a failed removal of the empty .claude directory fails the
// restore as on the hooks path; with it empty the warning of before stays.
func TestCoAuthorSettingsRestoreDirRemovalFailure(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	// .claude is a file: the read of the directory fails.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".claude"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	restored := func() error { return nil }

	t.Run("option set", func(t *testing.T) {
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		err := coAuthorSettingsRestore(log, settingsPath, true, restored, false)()
		if err == nil || !strings.Contains(err.Error(), "failed to remove empty .claude directory") || !strings.Contains(err.Error(), settingsPath) {
			t.Errorf("restore error = %v, want the directory removal failure naming %s", err, settingsPath)
		}
		if hookErr := RestoreHookSettings(settingsPath, t.TempDir(), restored, false); hookErr == nil {
			t.Error("the hooks path no longer fails on the directory removal; the two paths should agree")
		}
	})

	t.Run("option empty", func(t *testing.T) {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))
		if err := coAuthorSettingsRestore(log, settingsPath, false, restored, false)(); err != nil {
			t.Errorf("option empty: restore returned %v, want the warning only", err)
		}
		if !strings.Contains(buf.String(), `msg="Failed to remove empty .claude directory"`) {
			t.Errorf("log lacks the warning of before:\n%s", buf.String())
		}
	})
}

// mutation pin: RestoreHookSettings returns on a failed restoreFunc before it removes the hook script directory -> this test fails
//
// TestRestoreHookSettingsFailureRemovesScriptDir (GH-6626 item 5): a failed
// restore still removes the temporary hook script directory.
func TestRestoreHookSettingsFailureRemovesScriptDir(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	scriptDir := filepath.Join(t.TempDir(), "pilot-hooks-x")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "pilot-bash-guard.sh"), []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(t.TempDir(), ".claude", "settings.json")

	err := RestoreHookSettings(settingsPath, scriptDir, func() error { return errors.New("write failed") }, true)
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Errorf("err = %v, want the restore failure", err)
	}
	if _, statErr := os.Stat(scriptDir); !os.IsNotExist(statErr) {
		t.Errorf("hook script directory %s left after a failed restore (stat: %v)", scriptDir, statErr)
	}
}

// declDoc is the doc comment of the function or struct field name in the Go
// file path, with its white space folded to single spaces. A field is named
// Type.Field.
func declDoc(t *testing.T, path, name string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	typeName, fieldName, isField := strings.Cut(name, ".")
	var doc *ast.CommentGroup
	ast.Inspect(file, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.FuncDecl:
			if !isField && d.Recv == nil && d.Name.Name == name {
				doc = d.Doc
			}
		case *ast.TypeSpec:
			st, ok := d.Type.(*ast.StructType)
			if !isField || !ok || d.Name.Name != typeName {
				return true
			}
			for _, f := range st.Fields.List {
				for _, id := range f.Names {
					if id.Name == fieldName {
						doc = f.Doc
					}
				}
			}
		}
		return true
	})
	if doc == nil {
		t.Fatalf("%s: no doc comment on %s", path, name)
	}
	return strings.Join(strings.Fields(doc.Text()), " ")
}

// mutation pin: the hookSettingsRestore doc says the run fails (failRunOnSettingsRestore) with no word of a run that opened a PR -> this test fails
//
// TestHookSettingsRestoreDocNamesTheOpenPRCase (GH-6646 item 1): with the
// option set a failed restore fails only a run with no PR; the doc says so.
func TestHookSettingsRestoreDocNamesTheOpenPRCase(t *testing.T) {
	doc := declDoc(t, "hooks.go", "hookSettingsRestore")
	if strings.Contains(doc, "the run fails (failRunOnSettingsRestore)") {
		t.Errorf("hookSettingsRestore doc still says every run fails:\n%s", doc)
	}
	for _, want := range []string{"a run with no PR fails", "PRUrl set", "IntentWarning"} {
		if !strings.Contains(doc, want) {
			t.Errorf("hookSettingsRestore doc lacks %q:\n%s", want, doc)
		}
	}
}

// mutation pin: the IntentWarning field doc or the buildExecutionComment comment says the warning is lint issues only -> this test fails
//
// TestIntentWarningDocNamesTheSettingsRestore (GH-6646 item 3): IntentWarning
// also carries a failed settings restore of a run with an open PR
// (failRunOnSettingsRestore); the field doc and its consumer comment say so.
func TestIntentWarningDocNamesTheSettingsRestore(t *testing.T) {
	const want = "failed restore of the settings file"
	if doc := declDoc(t, "runner.go", "ExecutionResult.IntentWarning"); !strings.Contains(doc, want) {
		t.Errorf("IntentWarning field doc lacks %q:\n%s", want, doc)
	}

	path := filepath.Join("..", "..", "cmd", "pilot", "handlers.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "buildExecutionComment" {
			fn = f
		}
	}
	if fn == nil {
		t.Fatalf("%s: no buildExecutionComment", path)
	}
	// The comment that ends on the line above the IntentWarning check.
	checkLine := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "IntentWarning" && checkLine == 0 {
			checkLine = fset.Position(sel.Pos()).Line
		}
		return true
	})
	var comment string
	for _, cg := range file.Comments {
		if fset.Position(cg.End()).Line == checkLine-1 {
			comment = strings.Join(strings.Fields(cg.Text()), " ")
		}
	}
	if !strings.Contains(comment, want) {
		t.Errorf("%s: the comment above the IntentWarning line lacks %q:\n%s", path, want, comment)
	}
}

// mutation pin: the failRunOnSettingsRestore doc says the completion message shows the restore warning -> this test fails
//
// TestFailRunOnSettingsRestoreDocNamesTheExecutionComment (GH-6646 item 4):
// the restore runs in the defer, after the runner stored its completion
// memory, so only the execution comment of the caller shows the warning.
func TestFailRunOnSettingsRestoreDocNamesTheExecutionComment(t *testing.T) {
	doc := declDoc(t, "hooks.go", "failRunOnSettingsRestore")
	if strings.Contains(doc, "the completion message shows") {
		t.Errorf("failRunOnSettingsRestore doc still names the completion message:\n%s", doc)
	}
	for _, want := range []string{"completion memory the runner stores there never holds it", "execution comment", "buildExecutionComment"} {
		if !strings.Contains(doc, want) {
			t.Errorf("failRunOnSettingsRestore doc lacks %q:\n%s", want, doc)
		}
	}
}

// mutation pin: the failRunOnSettingsRestore doc opens with "makes the result of a run a failure for err" -> this test fails
//
// TestFailRunOnSettingsRestoreDocOpensWithBothOutcomes (GH-6646 item 5): the
// first paragraph of the doc gives the outcome with no PR and the outcome with
// PRUrl set.
func TestFailRunOnSettingsRestoreDocOpensWithBothOutcomes(t *testing.T) {
	doc := declDoc(t, "hooks.go", "failRunOnSettingsRestore")
	first, _, _ := strings.Cut(doc, "Decision (GH-6626)")
	if strings.HasPrefix(first, "failRunOnSettingsRestore makes the result of a run a failure for err") {
		t.Errorf("failRunOnSettingsRestore doc still opens with the failure for every run:\n%s", first)
	}
	for _, want := range []string{"A run with no PR (PRUrl empty) becomes a failure", "opened a PR (PRUrl set) keeps its result"} {
		if !strings.Contains(first, want) {
			t.Errorf("first paragraph of the failRunOnSettingsRestore doc lacks %q:\n%s", want, first)
		}
	}
}
