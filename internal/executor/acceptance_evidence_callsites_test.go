package executor

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/webhooks"
)

// GH-6664: each of the six call sites of appendAcceptanceEvidence handles a
// file the mutation check left changed. The three PR creators of Execute and
// the epic path fail with the branch pushed and no PR; the timeout salvage and
// the pre-PR hold park the task with no PR. The epic path puts the file back.

// callSiteOutcome is what one call site left behind.
type callSiteOutcome struct {
	result  *ExecutionResult
	dir     string
	prCalls int
	logs    string
	alerts  *fakeAlertProcessor
	hooks   *webhookRecorder
}

// leftChangedSeams make the restore of the mutation line fail: the mutation
// write passes, each later write and the git checkout of the repair fail.
func leftChangedSeams(t *testing.T) {
	t.Helper()
	setMutationWriteForTest(t, func(n int, path string, data []byte) error {
		if n == 1 {
			return writeOK(path, data)
		}
		return errRestoreTestWrite
	})
	failCheckoutForTest(t)
}

// runLeftChangedThroughExecute drives Execute with the review off, so the
// evidence runs at the PR creator that wire installs. wire returns the number
// of PR calls the creator saw.
func runLeftChangedThroughExecute(t *testing.T, wire func(t *testing.T, e *evidenceExecuteEnv) func() int) callSiteOutcome {
	t.Helper()
	cmd := &sequenceEvidenceRunner{outputs: []string{"--- FAIL: TestMain (0.00s)\nFAIL\n"}, errs: []error{exitOneError(t)}}
	e := newEvidenceExecuteEnv(t, PrePRReviewModeOff, approveWithNotes(), cmd)
	dir := e.task.ProjectPath
	seedMutationDir(t, dir)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "seed")
	e.task.AcceptanceCriteria = append([]string{restoreTestMutationLine}, e.task.AcceptanceCriteria...)
	prCalls := wire(t, e)
	out := callSiteOutcome{dir: dir, alerts: &fakeAlertProcessor{}, hooks: newWebhookRecorder()}
	t.Cleanup(out.hooks.close)
	e.runner.SetAlertProcessor(out.alerts)
	e.runner.SetWebhookManager(out.hooks.manager)
	logs := &syncBuffer{}
	e.runner.log = slog.New(slog.NewTextHandler(logs, nil))
	leftChangedSeams(t)

	result, err := e.runner.Execute(context.Background(), e.task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	out.result, out.prCalls, out.logs = result, prCalls(), logs.String()
	return out
}

// callSiteRepo is a repository on pilot/GH-9 with a bare remote, main.go and
// go.mod committed above main, a fake gh that logs its argv, and a runner whose
// evidence command answers a caught mutation.
type callSiteRepo struct {
	f       *secretGateFixture
	runner  *Runner
	creator *bodyCapturingPRCreator
	ghLog   string
	logs    *syncBuffer
	alerts  *fakeAlertProcessor
	hooks   *webhookRecorder
}

func newCallSiteRepo(t *testing.T) *callSiteRepo {
	t.Helper()
	f := newSecretGateFixture(t)
	seedMutationDir(t, f.local)
	f.run("add", ".")
	f.run("commit", "-m", "feat: main")

	fakeBin := t.TempDir()
	ghLog := filepath.Join(t.TempDir(), "gh-argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + ghLog + "\"\ncase \"$*\" in\n  *\"pr list\"*) echo '[]' ;;\n  *\"pr create\"*) echo https://github.com/o/r/pull/1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))

	c := &callSiteRepo{f: f, creator: &bodyCapturingPRCreator{}, ghLog: ghLog, logs: &syncBuffer{}, alerts: &fakeAlertProcessor{}, hooks: newWebhookRecorder()}
	t.Cleanup(c.hooks.close)
	c.runner = &Runner{log: slog.New(slog.NewTextHandler(c.logs, nil)), config: &BackendConfig{}}
	c.runner.acceptanceRunner = &sequenceEvidenceRunner{outputs: []string{"--- FAIL: TestMain (0.00s)\nFAIL\n"}, errs: []error{exitOneError(t)}}
	c.runner.prCreator = c.creator
	c.runner.SetAlertProcessor(c.alerts)
	c.runner.SetWebhookManager(c.hooks.manager)
	leftChangedSeams(t)
	return c
}

func (c *callSiteRepo) task() *Task {
	return &Task{
		ID: "GH-9", Title: "feat: add feature", Description: "d", Branch: "pilot/GH-9", BaseBranch: "main",
		CreatePR: true, ProjectPath: c.f.local, AcceptanceCriteria: []string{restoreTestMutationLine},
	}
}

func (c *callSiteRepo) outcome(t *testing.T, result *ExecutionResult) callSiteOutcome {
	t.Helper()
	c.creator.mu.Lock()
	calls := len(c.creator.bodies)
	c.creator.mu.Unlock()
	if b, err := os.ReadFile(c.ghLog); err == nil {
		calls += strings.Count(string(b), "pr create")
	}
	return callSiteOutcome{result: result, dir: c.f.local, prCalls: calls, logs: c.logs.String(), alerts: c.alerts, hooks: c.hooks}
}

func TestMutationLeftChangedAtEachCallSite(t *testing.T) {
	const executeBranch = "pilot/GH-6048-evidence"
	atPRCreation := func(branch string) string {
		return "acceptance evidence left main.go changed; the branch " + branch + " is pushed, the run fails, no PR is created"
	}
	const onHold = "acceptance evidence left main.go changed; the task is held, no PR is created"

	tests := []struct {
		name string
		run  func(t *testing.T) callSiteOutcome
		// want is the error text of the path.
		want string
		// hold is set for the paths that park the task under pilot-needs-human.
		hold bool
		// clean is set when git status must be empty at the end.
		clean bool
	}{
		{name: "github sdk site", want: atPRCreation(executeBranch), run: func(t *testing.T) callSiteOutcome {
			return runLeftChangedThroughExecute(t, func(t *testing.T, e *evidenceExecuteEnv) func() int {
				c := &bodyCapturingPRCreator{}
				repo := "o/" + filepath.Base(e.task.ProjectPath)
				e.task.SourceAdapter, e.task.SourceRepo = "github", repo
				e.runner.RegisterPRCreator("github:"+repo, c)
				return func() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.bodies) }
			})
		}},
		{name: "project or shared creator site", want: atPRCreation(executeBranch), run: func(t *testing.T) callSiteOutcome {
			return runLeftChangedThroughExecute(t, func(t *testing.T, e *evidenceExecuteEnv) func() int {
				c := &bodyCapturingPRCreator{}
				e.task.SourceAdapter = "linear"
				e.runner.SetPRCreator(c)
				return func() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.bodies) }
			})
		}},
		{name: "gh cli site", want: atPRCreation(executeBranch), run: func(t *testing.T) callSiteOutcome {
			return runLeftChangedThroughExecute(t, func(t *testing.T, e *evidenceExecuteEnv) func() int {
				file := setUpBodyCapturingGh(t)
				return func() int {
					if _, err := os.Stat(file); err == nil {
						return 1
					}
					return 0
				}
			})
		}},
		{name: "epic site", want: atPRCreation("pilot/GH-9"), clean: true, run: func(t *testing.T) callSiteOutcome {
			c := newCallSiteRepo(t)
			result := &ExecutionResult{TaskID: "GH-9", Success: true, IsEpic: true}
			c.runner.finalizeEpicBranchPR(context.Background(), c.task(), c.f.git, result, []string{"completed"})
			return c.outcome(t, result)
		}},
		{name: "timeout salvage site", want: "timeout salvage: " + onHold, hold: true, run: func(t *testing.T) callSiteOutcome {
			c := newCallSiteRepo(t)
			result := &ExecutionResult{TaskID: "GH-9"}
			var preserve bool
			c.runner.pushAndCreatePRAfterTimeout(context.Background(), c.task(), c.f.git, result, nil, c.runner.log, "", &preserve)
			return c.outcome(t, result)
		}},
		{name: "pre-PR hold site", want: prePRHoldReasonText + "; " + onHold, hold: true, run: func(t *testing.T) callSiteOutcome {
			c := newCallSiteRepo(t)
			review := &PostMergeReview{
				Verdict: PostMergeRequestChanges,
				Defects: []PostMergeDefect{{Severity: PostMergeSeverityBlocking, Location: "main.go:4", Claim: "wrong", Evidence: "--- FAIL: TestMain"}},
			}
			result := &ExecutionResult{TaskID: "GH-9", PrePRReview: &PrePRReviewOutcome{Mode: PrePRReviewModeEnforce, Verdict: PostMergeRequestChanges, Review: review}}
			var preserve bool
			c.runner.holdPrePRReview(context.Background(), c.task(), c.f.git, result, nil, c.runner.log, prePRHoldReasonText, "", nil, &preserve)
			return c.outcome(t, result)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := tt.run(t)
			res := out.result

			if res.Success {
				t.Errorf("Success = true, want the run to fail")
			}
			if res.Error != tt.want {
				t.Errorf("Error = %q, want %q", res.Error, tt.want)
			}
			if out.prCalls != 0 || res.PRUrl != "" {
				t.Errorf("a PR was created (%d calls, url %q)", out.prCalls, res.PRUrl)
			}
			if res.BranchPushed == nil || !*res.BranchPushed {
				t.Errorf("BranchPushed = %v, want true: the branch is on the remote", res.BranchPushed)
			}
			if strings.Contains(res.Error, out.dir) {
				t.Errorf("Error %q holds the task directory", res.Error)
			}
			if tt.hold {
				if res.Outcome != "needs_human" {
					t.Errorf("Outcome = %q, want needs_human (a hold)", res.Outcome)
				}
				// The Error line of the evidence follows the push of the hold.
				held := strings.Index(out.logs, "holding branch for human triage")
				line := strings.Index(out.logs, evidenceLeftChangedOnHoldLog)
				if held < 0 || line < held {
					t.Errorf("the Error line %q is not written after the push of the hold:\n%s", evidenceLeftChangedOnHoldLog, out.logs)
				}
			} else {
				if res.Outcome == "needs_human" {
					t.Errorf("Outcome = needs_human, want a failure")
				}
				if !strings.Contains(out.logs, evidenceLeftChangedAtPRCreationLog) {
					t.Errorf("no Error line %q:\n%s", evidenceLeftChangedAtPRCreationLog, out.logs)
				}
				failed := false
				for _, ev := range out.alerts.events {
					failed = failed || (ev.Type == AlertEventTypeTaskFailed && ev.Error == tt.want)
				}
				if !failed {
					t.Errorf("no TaskFailed alert with the error, got %+v", out.alerts.events)
				}
				if !out.hooks.received(string(webhooks.EventTaskFailed)) {
					t.Errorf("no %s webhook, got %v", webhooks.EventTaskFailed, out.hooks.events)
				}
			}
			if tt.clean {
				if st := gitOutput(t, out.dir, "status", "--porcelain"); strings.TrimSpace(st) != "" {
					t.Errorf("git status --porcelain = %q, want the file put back", st)
				}
				if got := readTestFile(t, filepath.Join(out.dir, "main.go")); got != restoreTestOriginal {
					t.Errorf("main.go = %q, want the first content", got)
				}
			}
		})
	}
}

// TestMutationRootWriteStaysInTheWorkspace: with the option empty a parent
// directory swapped for a link to an outside directory at the write seam makes
// no file outside (GH-6664), for the mutation write and for the restore write,
// and the text of the stop does not say that nothing was written.
func TestMutationRootWriteStaysInTheWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write int
	}{
		{"the mutation write", 1},
		{"the restore write", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			outside := t.TempDir()
			pkg := filepath.Join(dir, "pkg")
			writeTestFile(t, filepath.Join(pkg, "main.go"), restoreTestOriginal)
			seedToolchainMarker(t, dir, "go.mod")
			setMutationWriteModeForTest(t, func(n int, _ string, _ []byte, _ os.FileMode, write func() error) error {
				if n == tc.write {
					if err := os.Rename(pkg, filepath.Join(dir, "pkg-moved")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, pkg); err != nil {
						t.Fatal(err)
					}
				}
				return write()
			})

			res := runMutationItem(t.Context(), newRecordingEvidenceRunner(t), dir, ClassifyAcceptanceItem("delete line 4 in pkg/main.go -> TestMain fails"), defaultTestAllowedCommands)

			if got := dirEntries(t, outside); len(got) != 0 {
				t.Errorf("the directory outside the workspace holds %q, want no file", got)
			}
			var lc *mutationLeftChangedError
			if !errors.As(res.Err, &lc) || !lc.LeftWorkspace {
				t.Fatalf("Err = %v, want the stop of a path that left the workspace", res.Err)
			}
			if msg := lc.Error(); strings.Contains(msg, "nothing was written") {
				t.Errorf("Err = %q says nothing was written, but a write was tried", msg)
			}
		})
	}
}

// TestMutationRestoreCheckResolvesThePathAgain fails when the check of the
// restore stops resolving the path again (GH-6652): the command replaces the
// parent directory by a link to an outside directory whose file holds the
// first content, so a check at the first path reads the first content there
// while the real file is still mutated. The run must stop.
func TestMutationRestoreCheckResolvesThePathAgain(t *testing.T) {
	runMutationModeEnvs(t, func(t *testing.T, e mutationModeEnv, seed func(func())) {
		outside := t.TempDir()
		writeTestFile(t, filepath.Join(outside, "main.go"), restoreTestOriginal)
		pkg := filepath.Join(e.dir, "pkg")
		moved := filepath.Join(e.dir, "pkg-moved")
		seed(func() {
			writeTestFile(t, filepath.Join(pkg, "main.go"), restoreTestOriginal)
			seedToolchainMarker(t, e.dir, "go.mod")
		})
		runner := newRecordingEvidenceRunner(t)
		runner.during = func(context.Context) error {
			if _, err := os.Lstat(moved); err == nil {
				return nil // a later line: the swap is done
			}
			e.asAgent(func() {
				if err := os.Rename(pkg, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, pkg); err != nil {
					t.Fatal(err)
				}
			})
			return nil
		}
		items := []AcceptanceItem{
			ClassifyAcceptanceItem("delete line 4 in pkg/main.go -> TestMain fails"),
			ClassifyAcceptanceItem("`go test ./pkg/` passes; paste the output into the PR body"),
		}

		results := RunAcceptanceEvidenceItems(t.Context(), runner, e.dir, items, defaultTestAllowedCommands)

		var lc *mutationLeftChangedError
		if len(results) != 1 || !errors.As(results[0].Err, &lc) || !lc.LeftWorkspace {
			t.Fatalf("results = %+v, want the run to stop on the mutation line", results)
		}
		if runner.count() != 1 {
			t.Errorf("commands ran %d times, want 1: the run went on with the real file mutated", runner.count())
		}
		if got := readTestFile(t, filepath.Join(outside, "main.go")); got != restoreTestOriginal {
			t.Errorf("the file outside the workspace = %q, want it unchanged", got)
		}
	})
}
