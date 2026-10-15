package executor

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

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

	tests := []struct {
		name        string
		gates       *QualityOutcome // nil: no quality checker configured
		ghost       bool            // harvest a SHA equal to base before the guard
		signal      string          // optional pilot-signal emitted by the model
		wantAdopt   bool
		wantGateOut bool
	}{
		{name: "gates green adopts (pre-retry point)", gates: green, wantAdopt: true},
		{name: "gates green adopts (ghost-SHA point)", gates: green, ghost: true, wantAdopt: true},
		{name: "gates red keeps the auto-preserved failure plus gate output", gates: red, wantGateOut: true},
		{name: "gates red keeps the auto-preserved failure (ghost-SHA point)", gates: red, ghost: true, wantGateOut: true},
		{name: "no gates configured is never adopted", gates: nil},
		{
			name:   "no_op+reason evidence is never adopted",
			gates:  green,
			signal: `{"v":2,"type":"exit","exit_signal":true,"success":true,"no_op":true,"reason":"nothing to do"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capturedTitleFile := setUpFakeGhPRCreatePATH(t)

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

			backend := &mockGH4964Backend{
				perCall: func(_ int, opts ExecuteOptions) *BackendResult {
					writeUncommittedFile(t, dir, "feature.go") // staged-but-uncommitted, never committed
					if tt.ghost {
						emitStaleSHA(opts, seedSHA)
					}
					if tt.signal != "" {
						emitSignal(opts, tt.signal)
					}
					return &BackendResult{Success: true, Output: "once it reports, I'll commit"}
				},
			}
			runner := newGH4964Runner(backend)
			runner.SetLogStore(store)
			var logBuf bytes.Buffer
			runner.log = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			if tt.gates != nil {
				outcome := tt.gates
				runner.qualityCheckerFactory = func(string, string, string) QualityChecker {
					return &stubQualityChecker{outcome: outcome}
				}
			}

			task := newGH4964Task("GH-5709", branch, dir)
			task.ExecutionID = execID

			result, err := runner.Execute(context.Background(), task)
			if err != nil {
				t.Fatalf("Execute() returned error: %v", err)
			}

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
				sha := gitOutput(t, dir, "rev-parse", "--short=7", "HEAD")
				if line := logBuf.String(); !strings.Contains(line, "adopted auto-preserved commit") ||
					!strings.Contains(line, "sha="+strings.TrimSpace(sha)) || !strings.Contains(line, "gate_result=passed") ||
					!strings.Contains(line, "task_id=GH-5709") {
					t.Errorf("expected an INFO adoption line naming task, sha and gate result, got:\n%s", line)
				}
				if remote := gitOutput(t, bareDir, "log", "-1", "--format=%s", "refs/heads/"+branch); !strings.Contains(remote, "auto-preserved") {
					t.Errorf("expected the preserved commit on origin, got %q", remote)
				}
				return
			}

			// Not adopted: today's failure, terminal, no PR.
			if result.Success {
				t.Fatalf("expected Success=false, got true")
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
				if !strings.Contains(logBuf.String(), "gate_result=failed") {
					t.Errorf("expected an INFO line with gate_result=failed, got:\n%s", logBuf.String())
				}
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
