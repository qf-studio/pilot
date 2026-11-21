package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// GH-6668: the settings file into which the daemon writes its hook entries for
// a session never enters a commit of the run: the restore runs before the
// first lint fix, the amend of the lint fix leaves the file out while it
// differs from its state before the run, and the push of uncommitted work
// leaves .claude/ out.

// trackedHookSettings is the settings file a project tracks in
// TestLintAmendNeverCommitsTheHookSettings.
const trackedHookSettings = "{\n  \"permissions\": {\n    \"allow\": [\"Bash(ls)\"]\n  }\n}\n"

// hookSettingsWorkBackend commits work.go in the directory it runs in and
// records whether the settings file held the hook entries of the daemon
// during the session.
type hookSettingsWorkBackend struct {
	retryPathRecordingBackend
	mu        sync.Mutex
	sawHooks  bool
	sawFileOK bool
}

func (b *hookSettingsWorkBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	if _, err := b.retryPathRecordingBackend.Execute(ctx, opts); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(opts.ProjectPath, ".claude", "settings.json"))
	b.mu.Lock()
	b.sawFileOK = err == nil
	b.sawHooks = bytes.Contains(data, []byte("pilot-"))
	b.mu.Unlock()
	if err := os.WriteFile(filepath.Join(opts.ProjectPath, "work.go"), []byte("package work\n"), 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command("git", "add", "work.go")
	cmd.Dir = opts.ProjectPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git add: %v: %s", err, out)
	}
	if err := gitTestCommitIn(opts.ProjectPath, "-m", "feat: work"); err != nil {
		return nil, err
	}
	return &BackendResult{Success: true, Output: "done"}, nil
}

// mutation pin: no restore before the first autoFixLint call and no exclude of the settings file in stageAndAmendCommit -> this test fails
//
// TestLintAmendNeverCommitsTheHookSettings (GH-6668 items 1, 2): through
// executeWithOptions with hooks on and the pre-push lint on, a lint program
// that reports one issue until --fix ran and a backend that commits, the
// pushed branch does not change .claude/settings.json: in a repository that
// does not track the file and in one that tracks it (there the file on the
// branch is the tracked content), with run_as_user empty and set.
func TestLintAmendNeverCommitsTheHookSettings(t *testing.T) {
	for _, runAs := range []bool{false, true} {
		for _, tracked := range []bool{false, true} {
			name := fmt.Sprintf("run_as_user set %v, file tracked %v", runAs, tracked)
			t.Run(name, func(t *testing.T) {
				repo, remote := setupTestRepoWithRemote(t)
				t.Cleanup(func() { _ = os.RemoveAll(repo); _ = os.RemoveAll(remote) })
				writeTestFile(t, filepath.Join(repo, "go.mod"), "module example.com/lint\n\ngo 1.21\n")
				if tracked {
					writeTestFile(t, filepath.Join(repo, ".claude", "settings.json"), trackedHookSettings)
				}
				wsGit(t, repo, "add", ".")
				if err := gitTestCommitIn(repo, "-q", "-m", "chore: project files"); err != nil {
					t.Fatal(err)
				}
				wsGit(t, repo, "push", "-q", "origin", "main")

				lintOut := mockLint(t)
				if runAs {
					stubRunAsGOOS(t)
					root, err := filepath.EvalSymlinks(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					_ = mockSudoAgent(t, repo, "")
					setRunAsStateForTest(t, gitRouteSettings(t, root), "")
				} else {
					setRunAsStateForTest(t, nil, "")
				}

				backend := &hookSettingsWorkBackend{}
				runner := NewRunnerWithBackend(backend)
				runner.config = &BackendConfig{
					SkipSelfReview: true,
					PrePushLint:    boolPtr(true),
					Hooks:          &HooksConfig{Enabled: true},
				}
				runner.SetSkipPreflightChecks(true)
				runner.SetRecordingEnabled(false)
				runner.log = slog.New(slog.NewTextHandler(io.Discard, nil))
				runner.SetPRCreator(&bodyCapturingPRCreator{})
				const branch = "pilot/GH-6668"
				task := &Task{ID: "GH-6668", Title: "t", Description: "d", ProjectPath: repo, Branch: branch, CreatePR: true, BaseBranch: "main", SourceAdapter: "linear", SkipQualityGates: true}
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()

				res, runErr := runner.executeWithOptions(ctx, task, true)
				if len(backend.paths()) == 0 {
					t.Fatal("the backend never ran")
				}
				backend.mu.Lock()
				sawHooks := backend.sawFileOK && backend.sawHooks
				backend.mu.Unlock()
				if !sawHooks {
					t.Fatal("the settings file held no hook entries of the daemon during the session")
				}
				if _, err := os.Stat(filepath.Join(lintOut, "fixed")); err != nil {
					t.Fatal("the lint fix never ran")
				}
				if res == nil || !res.Success || runErr != nil {
					t.Fatalf("result = %+v, run error = %v, want a run that pushed", res, runErr)
				}

				changed, err := exec.Command("git", "--git-dir", remote, "diff", "--name-only", "main", "refs/heads/"+branch).CombinedOutput()
				if err != nil {
					t.Fatalf("diff of the pushed branch: %v: %s", err, changed)
				}
				if !strings.Contains(string(changed), "work.go") {
					t.Fatalf("the pushed branch lacks the work of the backend: %q", changed)
				}
				if strings.Contains(string(changed), ".claude/settings.json") {
					t.Errorf("the pushed branch changes .claude/settings.json: %q", changed)
				}
				onBranch, err := exec.Command("git", "--git-dir", remote, "show", "refs/heads/"+branch+":.claude/settings.json").CombinedOutput()
				if tracked && (err != nil || string(onBranch) != trackedHookSettings) {
					t.Errorf("settings file on the branch = %q (%v), want the tracked content", onBranch, err)
				}
				if !tracked && err == nil {
					t.Errorf("the pushed branch holds .claude/settings.json: %q", onBranch)
				}
			})
		}
	}
}

// mutation pin: stageAndAmendCommit stages .claude/settings.json while hookSettingsLeftChanged reports true -> this test fails
//
// TestLintAmendLeavesOutChangedHookSettings (GH-6668 item 2): while the
// settings file differs from its state before the run, the amend of the lint
// fix leaves it out with one Warn line and still commits the fix.
func TestLintAmendLeavesOutChangedHookSettings(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	isolateHostGitIdentity(t)
	_, ws := lintWorkspace(t)
	logs := captureLog(t)
	writeTestFile(t, filepath.Join(ws, ".claude", "settings.json"), `{"hooks":{"Stop":[]}}`)
	writeTestFile(t, filepath.Join(ws, "fix.go"), "package fix\n")
	g := NewGitOperations(ws)
	g.hookSettingsLeftChanged = func() bool { return true }

	if err := g.stageAndAmendCommit(context.Background(), "lint fix"); err != nil {
		t.Fatalf("stageAndAmendCommit: %v", err)
	}
	files := wsGit(t, ws, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(files, "fix.go") {
		t.Errorf("the amend lacks the fix: %q", files)
	}
	if strings.Contains(files, "settings.json") {
		t.Errorf("the amend holds the settings file: %q", files)
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 || !strings.Contains(logs.String(), "the amend leaves it out") {
		t.Errorf("want one Warn line on the left-out file, got %d:\n%s", n, logs.String())
	}

	// The file is back: the amend stages every change as before.
	logs.Reset()
	g.hookSettingsLeftChanged = func() bool { return false }
	if err := g.stageAndAmendCommit(context.Background(), "lint fix"); err != nil {
		t.Fatalf("stageAndAmendCommit: %v", err)
	}
	if files := wsGit(t, ws, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(files, "settings.json") {
		t.Errorf("the amend without the guard left out a file of the project: %q", files)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("a Warn line with the file back:\n%s", logs.String())
	}
}

// mutation pin: runLintCheck reads a refused lint command as a lint failure (no LintResult.Err) -> this test fails
//
// TestLintRefusedCommandFailsTheStep (GH-6668 item 4): with the option set, a
// lint command that is not a sudo command of the agent user is an error of
// the lint step: the run result fails with the text of the refusal and one
// Error line; it is not the result of a missing program.
func TestLintRefusedCommandFailsTheStep(t *testing.T) {
	stubRunAsGOOS(t)
	root, ws := lintWorkspace(t)
	_ = mockLint(t)
	_ = mockSudoAgent(t, "", "")
	setRunAsStateForTest(t, gitRouteSettings(t, root), "")
	runAsGOOS = "darwin" // agentCommand builds a plain command; stubRunAsGOOS restores
	const refusal = "is not a sudo command of the agent user"

	g := NewGitOperations(ws)
	if check := g.runLintCheck(context.Background(), "main"); check.Err == nil || !strings.Contains(check.Err.Error(), refusal) {
		t.Fatalf("runLintCheck = %+v, want the refusal in Err", check)
	}
	lr := g.autoFixLint(context.Background(), "main")
	if lr.Err == nil || !strings.Contains(lr.Err.Error(), refusal) {
		t.Fatalf("autoFixLint Err = %v, want the refusal", lr.Err)
	}

	var logs bytes.Buffer
	runner := NewRunnerWithBackend(&retryPathRecordingBackend{})
	log := slog.New(slog.NewTextHandler(&logs, nil))
	result := &ExecutionResult{TaskID: "GH-6668", Success: true}
	if runner.runPrePushLint(context.Background(), &Task{ID: "GH-6668"}, g, result, log) {
		t.Fatal("runPrePushLint went on after a refused lint command")
	}
	if result.Success || !strings.Contains(result.Error, refusal) {
		t.Errorf("result = %+v, want a failure with the text of the refusal", result)
	}
	if strings.Contains(result.IntentWarning, "not auto-fixable") {
		t.Errorf("IntentWarning = %q, want no lint issues for a refused command", result.IntentWarning)
	}
	if n := strings.Count(logs.String(), "level=ERROR"); n != 1 || !strings.Contains(logs.String(), refusal) {
		t.Errorf("want one Error line with the refusal, got %d:\n%s", n, logs.String())
	}
}

// mutation pin: preserveDirtyWorktreeAsWIP stages .claude/settings.json -> this test fails
//
// TestWIPPreserveNeverCommitsTheHookSettings (GH-6668 item 3): the push of
// uncommitted work leaves the settings file of the daemon out, tracked or not.
func TestWIPPreserveNeverCommitsTheHookSettings(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	for _, tracked := range []bool{false, true} {
		t.Run(fmt.Sprintf("file tracked %v", tracked), func(t *testing.T) {
			repo, remote := setupTestRepoWithRemote(t)
			t.Cleanup(func() { _ = os.RemoveAll(repo); _ = os.RemoveAll(remote) })
			if tracked {
				writeTestFile(t, filepath.Join(repo, ".claude", "settings.json"), trackedHookSettings)
				wsGit(t, repo, "add", ".")
				if err := gitTestCommitIn(repo, "-q", "-m", "chore: settings"); err != nil {
					t.Fatal(err)
				}
			}
			wsGit(t, repo, "checkout", "-q", "-b", "pilot/GH-6668")
			writeTestFile(t, filepath.Join(repo, ".claude", "settings.json"), `{"hooks":{"Stop":[{"hooks":[{"command":"/tmp/pilot-hooks-1/pilot-stop-gate.sh"}]}]}}`)
			writeTestFile(t, filepath.Join(repo, "work.go"), "package work\n")
			log := slog.New(slog.NewTextHandler(io.Discard, nil))

			sha, preserved := preserveDirtyWorktreeAsWIP(context.Background(), NewGitOperations(repo), &Task{ID: "GH-6668", Branch: "pilot/GH-6668"}, log)
			if !preserved {
				t.Fatal("the uncommitted work was not preserved")
			}
			files := wsGit(t, repo, "show", "--name-only", "--format=", sha)
			if !strings.Contains(files, "work.go") {
				t.Errorf("the WIP commit lacks work.go: %q", files)
			}
			if strings.Contains(files, "settings.json") {
				t.Errorf("the WIP commit holds the settings file: %q", files)
			}
		})
	}
}
