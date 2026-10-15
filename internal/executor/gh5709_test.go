package executor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// gh5718Checker is a QualityChecker whose Check is a closure (handed the
// 1-based call number), so a row can return a checker error or break the next
// git call from inside the gate run.
type gh5718Checker struct {
	calls int
	fn    func(call int) (*QualityOutcome, error)
}

func (c *gh5718Checker) Check(context.Context) (*QualityOutcome, error) {
	c.calls++
	return c.fn(c.calls)
}

// gh5718BackendErr is a classified BackendError for the retry-failure row.
type gh5718BackendErr struct{}

func (gh5718BackendErr) Error() string        { return "backend exploded" }
func (gh5718BackendErr) ErrorType() string    { return "api_error" }
func (gh5718BackendErr) ErrorMessage() string { return "backend exploded" }
func (gh5718BackendErr) ErrorStderr() string  { return "" }

// adoptionInfoLines isolates the finishAdoption INFO lines from the log. The
// preserve WARN lines (git_freshness.go) also carry a `sha` key, so asserting
// `sha=` on the whole buffer proves nothing — pick the line by its message.
func adoptionInfoLines(logs string) []string {
	var out []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "level=INFO") && strings.Contains(line, "auto-preserved commit") {
			out = append(out, line)
		}
	}
	return out
}

// assertAdoptionInfoLine requires exactly one adoption INFO line carrying the
// task id, the short sha and the gate result.
func assertAdoptionInfoLine(t *testing.T, logs, taskID, sha, gateResult string) {
	t.Helper()
	lines := adoptionInfoLines(logs)
	if len(lines) != 1 {
		t.Fatalf("expected exactly one adoption INFO line, got %d: %q", len(lines), lines)
	}
	line := lines[0]
	for _, want := range []string{"task_id=" + taskID, "sha=" + sha, "gate_result=" + gateResult} {
		if !strings.Contains(line, want) {
			t.Errorf("adoption INFO line missing %q: %s", want, line)
		}
	}
	wantMsg := "adopted auto-preserved commit as the deliverable"
	if gateResult != "passed" {
		wantMsg = "auto-preserved commit failed quality gates"
	}
	if !strings.Contains(line, wantMsg) {
		t.Errorf("adoption INFO line missing message %q: %s", wantMsg, line)
	}
}

// TestRunner_AdoptAutoPreservedCommit is the GH-5709 table test: a backend
// that ends its turn successfully with staged-but-uncommitted work (the #5701
// incident — it backgrounded a long `go test` and never ran `git commit`)
// has that work auto-preserved, and the preserved commit is then either
// adopted as the deliverable (gates green → PR with the task title,
// completed) or failed exactly as before with the gate output appended
// (gates red / not configured / decline evidence present). Covers both
// no-op classification points: the GH-916 pre-retry count (clean harvest)
// and the ghost-SHA guard (harvested SHA equals base).
func TestRunner_AdoptAutoPreservedCommit(t *testing.T) {
	const redOutput = "--- FAIL: TestPreservedWork (0.00s)\nFAIL\texample.com/x\t0.01s"
	green := &QualityOutcome{Passed: true}
	red := &QualityOutcome{
		Passed:        false,
		ShouldRetry:   false,
		RetryFeedback: "tests failed",
		GateDetails: []QualityGateDetail{
			{Name: "test", Command: "go test ./...", ExitCode: 1, Output: redOutput},
		},
	}

	retryable := &QualityOutcome{Passed: false, ShouldRetry: true, RetryFeedback: "tests failed"}

	tests := []struct {
		name          string
		gates         *QualityOutcome // nil: no quality checker configured
		check         func(dir string, call int) (*QualityOutcome, error)
		ghost         bool   // harvest a SHA equal to base before the guard
		signal        string // optional pilot-signal emitted by the model
		lastText      string // BackendResult.LastAssistantText
		backendResult *BackendResult
		retryErr      error // returned by the backend on its second call (the gate retry)
		cancelOnAdopt bool  // cancel the task ctx as soon as the adoption begins (deadline passed)
		wantAdopt     bool
		wantGateOut   bool
		wantFinish    string // gate_result of the single adoption INFO line ("" = no adoption)
		wantCalls     int    // backend invocations proving the intended exit was reached (0 = don't check)
		plainFailure  bool   // fails before any preserve: no auto-preserved text expected
	}{
		{name: "gates green adopts (pre-retry point)", gates: green, wantAdopt: true, wantFinish: "passed"},
		{name: "gates green adopts (ghost-SHA point)", gates: green, ghost: true, wantAdopt: true, wantFinish: "passed"},
		{name: "gates red keeps the auto-preserved failure plus gate output", gates: red, wantGateOut: true, wantFinish: "failed"},
		{name: "gates red keeps the auto-preserved failure (ghost-SHA point)", gates: red, ghost: true, wantGateOut: true, wantFinish: "failed"},
		{name: "no gates configured is never adopted", gates: nil},
		{
			name:   "no_op+reason evidence is never adopted",
			gates:  green,
			signal: `{"v":2,"type":"exit","exit_signal":true,"success":true,"no_op":true,"reason":"nothing to do"}`,
		},
		// GH-5718: the gate section's early returns used to leave a begun
		// adoption unfinished — marker absent, no INFO line.
		{
			name: "gate checker error keeps the auto-preserved failure",
			check: func(string, int) (*QualityOutcome, error) {
				return nil, errors.New("gate infrastructure down")
			},
			wantFinish: "failed",
			wantCalls:  1,
		},
		{
			name:  "gate checker error keeps the auto-preserved failure (ghost-SHA point)",
			ghost: true,
			check: func(string, int) (*QualityOutcome, error) {
				return nil, errors.New("gate infrastructure down")
			},
			wantFinish: "failed",
		},
		{
			name:          "task ctx done with the branch held keeps the auto-preserved failure",
			gates:         retryable,
			cancelOnAdopt: true,
			wantFinish:    "failed",
			wantCalls:     1, // held, not re-invoked
		},
		{
			name:       "retry backend failure (plain error) keeps the auto-preserved failure",
			gates:      retryable,
			retryErr:   errors.New("connection reset"),
			wantFinish: "failed",
			wantCalls:  2,
		},
		{
			name:       "retry backend failure (classified BackendError) keeps the auto-preserved failure",
			gates:      retryable,
			retryErr:   gh5718BackendErr{},
			wantFinish: "failed",
			wantCalls:  2,
		},
		{
			name: "pre-retry reset failure keeps the auto-preserved failure",
			check: func(dir string, _ int) (*QualityOutcome, error) {
				// A held index.lock makes the pre-retry hard reset fail.
				if err := os.WriteFile(filepath.Join(dir, ".git", "index.lock"), nil, 0o644); err != nil {
					return nil, err
				}
				return retryable, nil
			},
			wantFinish: "failed",
			wantCalls:  1, // reset failed before the retry was invoked
		},
		// Never adopted: backend evidence contradicts "finished work".
		{
			name:          "unsuccessful backend result is never adopted",
			gates:         green,
			backendResult: &BackendResult{Success: false, Error: "backend gave up"},
			plainFailure:  true,
		},
		{
			name:          "classified backend error type is never adopted",
			gates:         green,
			backendResult: &BackendResult{Success: true, ErrorType: "api_error"},
		},
		{
			name:     "DECLINED marker is never adopted",
			gates:    green,
			lastText: "DECLINED: the ticket is not actionable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capturedTitleFile, _ := setUpFakeGhPRCreateAndLabelPATH(t)

			const branch = "pilot/GH-5709-adopt"
			dir, bareDir := setupFreshnessRepo(t)
			seedSHA := strings.TrimSpace(gitOutput(t, dir, "rev-parse", "HEAD"))
			runGit(t, dir, "checkout", "-b", branch)

			store, err := memory.NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()
			const execID = "exec-gh5709"
			if err := store.SaveExecution(&memory.Execution{ID: execID, TaskID: "GH-5709", ProjectPath: dir, Status: "running"}); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			backend := &ctxRespectingBackend{
				run: func(_ context.Context, call int, opts ExecuteOptions) (*BackendResult, error) {
					if call > 1 { // the gate-retry re-invocation
						if tt.retryErr != nil {
							return &BackendResult{}, tt.retryErr
						}
						return &BackendResult{Success: true, Output: "retried"}, nil
					}
					writeUncommittedFile(t, dir, "feature.go") // staged-but-uncommitted, never committed
					if tt.ghost {
						emitStaleSHA(opts, seedSHA)
					}
					if tt.signal != "" {
						emitSignal(opts, tt.signal)
					}
					if tt.backendResult != nil {
						return tt.backendResult, nil
					}
					return &BackendResult{Success: true, Output: "once it reports, I'll commit", LastAssistantText: tt.lastText}, nil
				},
			}
			runner := newGH4964Runner(backend)
			runner.SetLogStore(store)
			var logBuf bytes.Buffer
			runner.log = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			if tt.cancelOnAdopt {
				// beginAdoption reports "Adopting" right after the preserve:
				// cancelling here makes the deadline pass between adoption and
				// the gate section, deterministically.
				runner.OnProgress(func(_, phase string, _ int, _ string) {
					if phase == "Adopting" {
						cancel()
					}
				})
			}
			switch {
			case tt.check != nil:
				checker := &gh5718Checker{fn: func(call int) (*QualityOutcome, error) { return tt.check(dir, call) }}
				runner.qualityCheckerFactory = func(string, string, string) QualityChecker { return checker }
			case tt.gates != nil:
				outcome := tt.gates
				runner.qualityCheckerFactory = func(string, string, string) QualityChecker {
					return &stubQualityChecker{outcome: outcome}
				}
			}

			task := newGH4964Task("GH-5709", branch, dir)
			task.ExecutionID = execID

			result, err := runner.Execute(ctx, task)
			if err != nil {
				t.Fatalf("Execute() returned error: %v", err)
			}

			shortHEAD := strings.TrimSpace(gitOutput(t, dir, "rev-parse", "--short=7", "HEAD"))

			events, err := store.ListExecutionEvents(execID)
			if err != nil {
				t.Fatalf("ListExecutionEvents: %v", err)
			}
			hasStage := func(s memory.Stage) bool {
				for _, e := range events {
					if e.Stage == s {
						return true
					}
				}
				return false
			}

			if tt.ghost {
				// GH-5718: the ghost guard already recorded work_preserved;
				// beginAdoption must not record a second one.
				n := 0
				for _, e := range events {
					if e.Stage == memory.StageWorkPreserved {
						n++
					}
				}
				if n != 1 {
					t.Errorf("ghost-SHA path recorded %d work_preserved events, want exactly 1", n)
				}
			}

			if tt.wantAdopt {
				if !result.Success || TerminalStatus(result) != "completed" {
					t.Fatalf("expected a completed execution, got success=%v status=%q error=%q", result.Success, TerminalStatus(result), result.Error)
				}
				if strings.Contains(result.Error, "auto-preserved") || strings.Contains(result.Error, "needs manual review") {
					t.Errorf("adopted run must not carry the auto-preserved failure, got %q", result.Error)
				}
				if result.PRUrl == "" {
					t.Error("expected a PR URL")
				}
				title, readErr := os.ReadFile(capturedTitleFile)
				if readErr != nil {
					t.Fatalf("gh pr create was never invoked: %v", readErr)
				}
				if !strings.Contains(string(title), "GH-4964 test task") || strings.Contains(string(title), "wip(") {
					t.Errorf("PR title = %q, want the task title and not the wip subject", title)
				}
				if !hasStage(memory.StageWorkPreserved) || !hasStage(memory.StageWorkAdopted) {
					t.Errorf("expected work_preserved and work_adopted events, got %+v", events)
				}
				assertAdoptionInfoLine(t, logBuf.String(), "GH-5709", shortHEAD, "passed")
				if remote := gitOutput(t, bareDir, "log", "-1", "--format=%s", "refs/heads/"+branch); !strings.Contains(remote, "auto-preserved") {
					t.Errorf("expected the preserved commit on origin, got %q", remote)
				}
				return
			}

			// Not adopted (or adopted and then failed): today's failure,
			// terminal, no PR.
			if result.Success {
				t.Fatalf("expected Success=false, got true")
			}
			if tt.wantCalls != 0 && backend.callCount() != tt.wantCalls {
				t.Errorf("backend invoked %d times, want %d (the intended exit was not the one reached)", backend.callCount(), tt.wantCalls)
			}
			if tt.plainFailure {
				if strings.Contains(result.Error, "auto-preserved") {
					t.Errorf("a run that fails before any preserve must not carry the marker, got %q", result.Error)
				}
				if hasStage(memory.StageWorkAdopted) {
					t.Error("work_adopted must not be recorded for a run that was not adopted")
				}
				if lines := adoptionInfoLines(logBuf.String()); len(lines) != 0 {
					t.Errorf("a run that was never adopted must log no adoption INFO line, got %q", lines)
				}
				return
			}
			if want := "auto-preserved as " + shortHEAD + " on branch " + branch; !strings.Contains(result.Error, want) {
				t.Errorf("result.Error must contain %q, got %q", want, result.Error)
			}
			if !strings.Contains(result.Error, "auto-preserved as ") || !strings.Contains(result.Error, "needs manual review, not a genuine no-op") {
				t.Errorf("expected today's auto-preserved failure text, got %q", result.Error)
			}
			if !IsPermanentFailure(result.Error) {
				t.Errorf("auto-preserved failure must stay terminal (IsPermanentFailure), got %q", result.Error)
			}
			if sha, branchOut, ok := parseAutoPreservedSHABranch(result.Error); !ok || sha == "" || branchOut != branch {
				t.Errorf("parseAutoPreservedSHABranch = (%q, %q, %v), want sha and %q", sha, branchOut, ok, branch)
			}
			if tt.wantGateOut {
				for _, want := range []string{"quality gates failed on the preserved commit", "go test ./...", "--- FAIL: TestPreservedWork"} {
					if !strings.Contains(result.Error, want) {
						t.Errorf("failure text missing gate output %q:\n%s", want, result.Error)
					}
				}
				if TerminalStatus(result) != "failed" {
					t.Errorf("TerminalStatus = %q, want failed", TerminalStatus(result))
				}
			}
			if tt.wantFinish != "" {
				assertAdoptionInfoLine(t, logBuf.String(), "GH-5709", shortHEAD, tt.wantFinish)
			} else if lines := adoptionInfoLines(logBuf.String()); len(lines) != 0 {
				t.Errorf("a run that was never adopted must log no adoption INFO line, got %q", lines)
			}
			if hasStage(memory.StageWorkAdopted) {
				t.Error("work_adopted must not be recorded for a run that was not adopted")
			}
			if !hasStage(memory.StageWorkPreserved) {
				t.Errorf("expected a work_preserved event, got %+v", events)
			}
			if _, statErr := os.Stat(capturedTitleFile); statErr == nil {
				t.Error("gh pr create must not be invoked for a non-adopted run")
			}
		})
	}
}

// TestRunner_AdoptedPreservedRun_DirectModeRetryPushesFastForward is the
// GH-5718 direct-mode (non-worktree) guard. preserveDirtyWorktreeAsWIP pushes
// the wip commit before the gates run; when the first gate pass asks for a
// retry, the clone is hard-reset to the retry baseline. That baseline must be
// the adopted commit, not preAttemptSHA: rewinding past the already-pushed
// commit makes the retry's fresh commit diverge from origin and the final
// plain push non-fast-forward.
func TestRunner_AdoptedPreservedRun_DirectModeRetryPushesFastForward(t *testing.T) {
	setUpFakeGhPRCreatePATH(t)

	const branch = "pilot/GH-5718-direct-retry"
	dir, bareDir := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &ctxRespectingBackend{
		run: func(_ context.Context, call int, _ ExecuteOptions) (*BackendResult, error) {
			if call == 1 {
				writeUncommittedFile(t, dir, "feature.go") // never committed
				return &BackendResult{Success: true, Output: "once it reports, I'll commit"}, nil
			}
			writeUncommittedFile(t, dir, "retry-fix.go")
			runGit(t, dir, "add", "retry-fix.go")
			runGit(t, dir, "commit", "-m", "fix: address gate feedback")
			return &BackendResult{Success: true, Output: "fixed"}, nil
		},
	}
	runner := newGH4964Runner(backend)
	checker := &gh5718Checker{fn: func(call int) (*QualityOutcome, error) {
		if call == 1 {
			// The wip commit is already on origin by the time the gates run.
			if got := gitOutput(t, bareDir, "log", "-1", "--format=%s", "refs/heads/"+branch); !strings.Contains(got, "auto-preserved") {
				t.Errorf("expected the wip commit on origin before the gates, got %q", got)
			}
			return &QualityOutcome{Passed: false, ShouldRetry: true, RetryFeedback: "fix the build"}, nil
		}
		return &QualityOutcome{Passed: true}, nil
	}}
	runner.qualityCheckerFactory = func(string, string, string) QualityChecker { return checker }

	task := newGH4964Task("GH-5718", branch, dir)
	result, err := runner.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected the adopted run to complete after the retry, got error %q", result.Error)
	}
	if backend.callCount() != 2 {
		t.Fatalf("backend invoked %d times, want 2 (initial + gate retry)", backend.callCount())
	}

	localHead := strings.TrimSpace(gitOutput(t, dir, "rev-parse", "HEAD"))
	if remoteHead := strings.TrimSpace(gitOutput(t, bareDir, "rev-parse", "refs/heads/"+branch)); remoteHead != localHead {
		t.Errorf("origin/%s = %s, want the local head %s", branch, remoteHead, localHead)
	}
	// Fast-forward: every commit the wip push put on origin is still in the
	// final history (wip commit, then the retry commit on top of it).
	history := gitOutput(t, bareDir, "log", "--format=%s", "refs/heads/"+branch)
	for _, want := range []string{"auto-preserved", "fix: address gate feedback"} {
		if !strings.Contains(history, want) {
			t.Errorf("origin history missing %q:\n%s", want, history)
		}
	}
}
