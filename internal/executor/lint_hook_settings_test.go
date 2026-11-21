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

// GH-6668, GH-6676: the settings file into which the daemon writes its hook
// entries for a session never enters a commit of the run: the amend of the
// lint fix and the push of uncommitted work take the file out of the index
// again while it differs from its state before the run, and every model
// session before the push runs with the hook entries of the daemon.

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

// hookSettingsRepoKinds are the repositories of
// TestLintAmendNeverCommitsTheHookSettings: setup makes the base commit of
// the project in repo, outside is a directory outside the workspace; tracked
// is whether the branch holds the settings file with trackedHookSettings.
var hookSettingsRepoKinds = []struct {
	name    string
	tracked bool
	setup   func(t *testing.T, repo, outside string)
}{
	{name: "file untracked", setup: func(t *testing.T, repo, outside string) {}},
	{name: "file tracked", tracked: true, setup: func(t *testing.T, repo, outside string) {
		writeTestFile(t, filepath.Join(repo, ".claude", "settings.json"), trackedHookSettings)
	}},
	{name: ".claude ignored", setup: func(t *testing.T, repo, outside string) {
		writeTestFile(t, filepath.Join(repo, ".gitignore"), ".claude/\n")
	}},
	{name: ".claude a symlink to a directory outside", setup: func(t *testing.T, repo, outside string) {
		if err := os.MkdirAll(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(repo, ".claude")); err != nil {
			t.Fatal(err)
		}
	}},
}

// mockLintFixWrites is a golangci-lint in PATH that reports one issue until
// --fix ran; the fix writes lintfix.go in the directory it runs in, and each
// check records `git status --porcelain` there in <out>/status.
func mockLintFixWrites(t *testing.T) (out string) {
	t.Helper()
	dir := t.TempDir()
	out = t.TempDir()
	body := fmt.Sprintf(`#!/bin/sh
out='%[1]s'
for a in "$@"; do
  if [ "$a" = "--fix" ]; then
    printf 'package work\n\n// lint fix\n' > lintfix.go
    : > "$out/fixed"
    exit 0
  fi
done
git status --porcelain > "$out/status" 2>&1
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

// mutation pin: stageAndAmendCommit without the reset of the settings file after the stage -> this test fails
//
// TestLintAmendNeverCommitsTheHookSettings (GH-6668, GH-6676): through
// executeWithOptions with hooks on and the pre-push lint on, a lint program
// that reports one issue until --fix ran (the fix writes lintfix.go) and a
// backend that commits, the pushed branch holds the lint fix and does not
// change .claude/settings.json, and the lint check after the amend sees no
// staged leftover: in a repository that does not track the file, one that
// tracks it (there the file on the branch is the tracked content), one that
// ignores .claude/ and one where .claude is a symlink to a directory outside,
// with run_as_user empty and set.
func TestLintAmendNeverCommitsTheHookSettings(t *testing.T) {
	for _, runAs := range []bool{false, true} {
		for _, kind := range hookSettingsRepoKinds {
			name := fmt.Sprintf("run_as_user set %v, %s", runAs, kind.name)
			t.Run(name, func(t *testing.T) {
				repo, remote := setupTestRepoWithRemote(t)
				t.Cleanup(func() { _ = os.RemoveAll(repo); _ = os.RemoveAll(remote) })
				writeTestFile(t, filepath.Join(repo, "go.mod"), "module example.com/lint\n\ngo 1.21\n")
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				// With run_as_user set a path of the workspace that resolves
				// out of the workspace root is refused; the directory outside
				// the workspace is then one inside the root.
				outside := filepath.Join(t.TempDir(), "claude")
				if runAs {
					outside = filepath.Join(root, "claude-outside")
				}
				kind.setup(t, repo, outside)
				wsGit(t, repo, "add", ".")
				if err := gitTestCommitIn(repo, "-q", "-m", "chore: project files"); err != nil {
					t.Fatal(err)
				}
				wsGit(t, repo, "push", "-q", "origin", "main")

				lintOut := mockLintFixWrites(t)
				if runAs {
					stubRunAsGOOS(t)
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
				const branch = "pilot/GH-6676"
				task := &Task{ID: "GH-6676", Title: "t", Description: "d", ProjectPath: repo, Branch: branch, CreatePR: true, BaseBranch: "main", SourceAdapter: "linear", SkipQualityGates: true}
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()

				res, runErr := runner.executeWithOptions(ctx, task, true)
				if len(backend.paths()) == 0 {
					t.Fatalf("the backend never ran: %+v %v", res, runErr)
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
				if !strings.Contains(string(changed), "lintfix.go") {
					t.Errorf("the pushed branch lacks the lint fix: %q", changed)
				}
				if strings.Contains(string(changed), ".claude") {
					t.Errorf("the pushed branch changes .claude: %q", changed)
				}
				onBranch, err := exec.Command("git", "--git-dir", remote, "show", "refs/heads/"+branch+":.claude/settings.json").CombinedOutput()
				if kind.tracked && (err != nil || string(onBranch) != trackedHookSettings) {
					t.Errorf("settings file on the branch = %q (%v), want the tracked content", onBranch, err)
				}
				if !kind.tracked && err == nil {
					t.Errorf("the pushed branch holds .claude/settings.json: %q", onBranch)
				}

				status, err := os.ReadFile(filepath.Join(lintOut, "status"))
				if err != nil {
					t.Fatalf("no git status of the lint check after the amend: %v", err)
				}
				for _, line := range strings.Split(strings.TrimRight(string(status), "\n"), "\n") {
					if line != "" && line[0] != ' ' && line[0] != '?' {
						t.Errorf("staged leftover after the amend: %q in\n%s", line, status)
					}
				}
			})
		}
	}
}

// contractSessionBackend commits work.ts, an interface with the field
// specVersion, in the directory it runs in: the diff touches a contract file
// and the contract evidence session runs.
type contractSessionBackend struct{ hookSettingsWorkBackend }

func (b *contractSessionBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	if _, err := b.retryPathRecordingBackend.Execute(ctx, opts); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(opts.ProjectPath, "work.ts"), []byte("export interface Instance {\n  specVersion: number;\n}\n"), 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command("git", "add", "work.ts")
	cmd.Dir = opts.ProjectPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git add: %v: %s", err, out)
	}
	if err := gitTestCommitIn(opts.ProjectPath, "-m", "feat: work"); err != nil {
		return nil, err
	}
	return &BackendResult{Success: true, Output: "done"}, nil
}

// mutation pin: a restore of the settings file before the first autoFixLint call -> this test fails
//
// TestModelSessionsBeforeThePushHaveTheHookSettings (GH-6676 item 1): through
// executeWithOptions with hooks on and the pre-push lint on, the lint fix runs
// first and the contract evidence session after it still finds the hook
// entries of the daemon in .claude/settings.json of the workspace, with
// run_as_user empty and set; the pushed branch never holds the file.
func TestModelSessionsBeforeThePushHaveTheHookSettings(t *testing.T) {
	for _, runAs := range []bool{false, true} {
		t.Run(fmt.Sprintf("run_as_user set %v", runAs), func(t *testing.T) {
			repo, remote := setupTestRepoWithRemote(t)
			t.Cleanup(func() { _ = os.RemoveAll(repo); _ = os.RemoveAll(remote) })
			writeTestFile(t, filepath.Join(repo, "go.mod"), "module example.com/lint\n\ngo 1.21\n")
			wsGit(t, repo, "add", ".")
			if err := gitTestCommitIn(repo, "-q", "-m", "chore: project files"); err != nil {
				t.Fatal(err)
			}
			wsGit(t, repo, "push", "-q", "origin", "main")

			lintOut := mockLintFixWrites(t)
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

			backend := &contractSessionBackend{}
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
			deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"*.ts"}}}
			runner.SetContractDependencyLookup(func(string) []ContractDependency { return deps })
			runner.SetContractContentFetcher(&fakeContractContentFetcher{content: map[string]string{
				"qf-studio/pilot-console/internal/instances/handlers.go": specVersionProducerContent(),
			}})
			var (
				mu           sync.Mutex
				sessions     int
				lintFixFirst bool
				settings     []byte
				settingsErr  error
			)
			runner.contractEvidenceFetchFn = func(ctx context.Context, dir string, fields []string) ([]ContractEvidence, error) {
				_, fixErr := os.Stat(filepath.Join(lintOut, "fixed"))
				data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
				mu.Lock()
				sessions++
				lintFixFirst = fixErr == nil
				settings, settingsErr = data, err
				mu.Unlock()
				return []ContractEvidence{{
					Field:         "specVersion",
					ProducerRepo:  "qf-studio/pilot-console",
					ProducerFile:  "internal/instances/handlers.go",
					ProducerLine:  4,
					ProducingExpr: "ConfigGeneration int",
				}}, nil
			}
			const branch = "pilot/GH-6676"
			task := &Task{ID: "GH-6676", Title: "t", Description: "d", ProjectPath: repo, Branch: branch, CreatePR: true, BaseBranch: "main", SourceAdapter: "linear", SkipQualityGates: true}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			res, runErr := runner.executeWithOptions(ctx, task, true)
			if len(backend.paths()) == 0 {
				t.Fatal("the backend never ran")
			}
			mu.Lock()
			defer mu.Unlock()
			if sessions != 1 {
				t.Fatalf("contract evidence sessions = %d, want 1", sessions)
			}
			if !lintFixFirst {
				t.Fatal("the contract evidence session ran before the lint fix")
			}
			if settingsErr != nil || !bytes.Contains(settings, []byte("pilot-")) {
				t.Errorf("settings file in the contract evidence session = %q (%v), want the hook entries of the daemon", settings, settingsErr)
			}
			if res == nil || !res.Success || runErr != nil {
				t.Fatalf("result = %+v, run error = %v, want a run that pushed", res, runErr)
			}
			changed, err := exec.Command("git", "--git-dir", remote, "diff", "--name-only", "main", "refs/heads/"+branch).CombinedOutput()
			if err != nil {
				t.Fatalf("diff of the pushed branch: %v: %s", err, changed)
			}
			if !strings.Contains(string(changed), "lintfix.go") || strings.Contains(string(changed), ".claude") {
				t.Errorf("pushed branch changes %q, want the lint fix and no .claude", changed)
			}
		})
	}
}

// mutation pin: stageAndAmendCommit stages .claude/settings.json while hookSettingsLeftChanged reports true -> this test fails
//
// TestLintAmendLeavesOutChangedHookSettings (GH-6668 item 2, GH-6676): while
// the settings file differs from its state before the run, the amend of the
// lint fix takes it out of the index after the stage with one Debug line and
// still commits the fix.
func TestLintAmendLeavesOutChangedHookSettings(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	isolateHostGitIdentity(t)
	_, ws := lintWorkspace(t)
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
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
	if n := strings.Count(logs.String(), "level=DEBUG"); n != 1 || !strings.Contains(logs.String(), "the commit keeps it out") {
		t.Errorf("want one Debug line on the left-out file, got %d:\n%s", n, logs.String())
	}
	if staged := wsGit(t, ws, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("staged leftover after the amend: %q", staged)
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
	if strings.Contains(logs.String(), "level=DEBUG") {
		t.Errorf("a Debug line with the file back:\n%s", logs.String())
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

// mutation pin: preserveDirtyWorktreeAsWIP stages .claude/settings.json, or Commit keeps a settings file that the session staged in the index -> this test fails
//
// TestWIPPreserveNeverCommitsTheHookSettings (GH-6668 item 3, GH-6676): the
// push of uncommitted work leaves the settings file of the daemon out, tracked
// or not, and staged by the session or not.
func TestWIPPreserveNeverCommitsTheHookSettings(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	for _, tracked := range []bool{false, true} {
		for _, staged := range []bool{false, true} {
			t.Run(fmt.Sprintf("file tracked %v, staged %v", tracked, staged), func(t *testing.T) {
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
				if staged {
					wsGit(t, repo, "add", ".claude/settings.json")
				}
				log := slog.New(slog.NewTextHandler(io.Discard, nil))
				g := NewGitOperations(repo)
				g.hookSettingsLeftChanged = func() bool { return true }

				sha, preserved := preserveDirtyWorktreeAsWIP(context.Background(), g, &Task{ID: "GH-6668", Branch: "pilot/GH-6668"}, log)
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
}
