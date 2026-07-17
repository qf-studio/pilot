package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// gh3938MultiPackageSubtasks returns two subtasks that reference distinct
// directories so isSinglePackageScope does not consolidate them into a single
// task — mirrors the fixture pattern already used by the epic recovery tests
// in epic_test.go.
func gh3938MultiPackageSubtasks() []PlannedSubtask {
	return []PlannedSubtask{
		{Order: 1, Title: "feat(gateway): add websocket handler", Description: "Implement upgrade handler in internal/gateway/server.go"},
		{Order: 2, Title: "feat(adapters): add telegram bot", Description: "Wire bot client in internal/adapters/telegram/bot.go"},
	}
}

// gh3938RecoveredIssues returns two open CreatedIssues matching
// gh3938MultiPackageSubtasks, used to drive CreateSubIssues down the
// ErrSubIssuesAlreadyExist → recovery path without shelling out to a real
// "gh issue create".
func gh3938RecoveredIssues() []CreatedIssue {
	subs := gh3938MultiPackageSubtasks()
	return []CreatedIssue{
		{Number: 101, State: "open", Subtask: subs[0]},
		{Number: 102, State: "open", Subtask: subs[1]},
	}
}

// gh3938EpicTestRunner builds a Runner wired for an in-process epic run: no
// real gh/git calls (dryRun + injected recovery), a real memory.Store so
// execution_events can be asserted, and a caller-supplied executeFunc
// standing in for the per-child Claude invocation.
func gh3938EpicTestRunner(store *memory.Store, execFn func(ctx context.Context, task *Task) (*ExecutionResult, error)) *Runner {
	r := NewRunner()
	r.skipPreflightChecks = true
	r.dryRun = true
	r.SetLogStore(store)
	r.openSubIssueCheck = func(_ context.Context, _, _ string) (bool, error) {
		return true, nil // force the ErrSubIssuesAlreadyExist → recovery path
	}
	r.recoverSubIssuesFn = func(_ context.Context, _, _ string) ([]CreatedIssue, error) {
		return gh3938RecoveredIssues(), nil
	}
	r.planEpicFn = func(_ context.Context, task *Task, _ string) (*EpicPlan, error) {
		return &EpicPlan{
			ParentTask: task,
			Subtasks:   gh3938MultiPackageSubtasks(),
		}, nil
	}
	r.executeFunc = execFn
	return r
}

// TestEpicPath_TokenHarvestAndFullEventSequence covers GH-3938 acceptance (a):
// a fake Claude stream (via executeFunc, standing in for each child's real
// Claude invocation) produces tokens_output > 0 on the epic-parent's own
// result, and the execution_events timeline records the full lifecycle past
// spec_validated instead of going silent.
func TestEpicPath_TokenHarvestAndFullEventSequence(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	childCalls := 0
	r := gh3938EpicTestRunner(store, func(_ context.Context, task *Task) (*ExecutionResult, error) {
		childCalls++
		return &ExecutionResult{
			TaskID:       task.ID,
			Success:      true,
			TokensInput:  1000,
			TokensOutput: 2000,
			TokensTotal:  3000,
			FilesChanged: 3,
			CommitSHA:    "deadbeef" + task.ID,
			PRUrl:        "https://github.com/owner/repo/pull/" + task.ID,
		}, nil
	})

	task := &Task{ID: "GH-3938TOK", Title: "[epic] fake claude stream token harvest test"}
	if err := store.SaveExecution(&memory.Execution{ID: task.ID, TaskID: task.ID, Status: "running"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	result, err := r.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected successful epic result, got: %+v", result)
	}
	if !result.IsEpic {
		t.Error("expected IsEpic=true")
	}
	if childCalls != 2 {
		t.Fatalf("expected 2 child executions, got %d", childCalls)
	}

	// The core regression: a real (simulated) Claude invocation per child must
	// roll up onto the epic-parent's own row instead of persisting as zero.
	if result.TokensOutput <= 0 {
		t.Errorf("TokensOutput = %d, want > 0", result.TokensOutput)
	}
	if result.TokensInput <= 0 {
		t.Errorf("TokensInput = %d, want > 0", result.TokensInput)
	}
	if result.TokensOutput != 4000 {
		t.Errorf("TokensOutput = %d, want 4000 (2 children x 2000)", result.TokensOutput)
	}
	if result.FilesChanged != 6 {
		t.Errorf("FilesChanged = %d, want 6 (2 children x 3)", result.FilesChanged)
	}

	events, err := store.ListExecutionEvents(task.LogExecutionID())
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	wantStages := []memory.Stage{
		memory.StageSpecValidated,
		memory.StageClaudeStarted,
		memory.StageDecomposed,
		memory.StageCompleted,
	}
	if len(events) != len(wantStages) {
		var gotStages []memory.Stage
		for _, e := range events {
			gotStages = append(gotStages, e.Stage)
		}
		t.Fatalf("got %d events %v, want %d %v", len(events), gotStages, len(wantStages), wantStages)
	}
	for i, want := range wantStages {
		if events[i].Stage != want {
			t.Errorf("event[%d].Stage = %q, want %q", i, events[i].Stage, want)
		}
	}
	if !strings.Contains(events[2].Detail, "decomposed into 2 children: #101, #102") {
		t.Errorf("decomposed event detail = %q, want it to mention decomposed into 2 children: #101, #102", events[2].Detail)
	}
}

// TestEpicPath_ZeroDeliveryTripsNoOpGuard covers GH-3938 acceptance (b): an
// epic whose children all report Success=true but ship zero tokens, zero
// file changes, and no commit/PR anywhere must not silently persist as
// "completed" — the ghost-SHA-shaped no-op guard must reclassify it.
func TestEpicPath_ZeroDeliveryTripsNoOpGuard(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	r := gh3938EpicTestRunner(store, func(_ context.Context, task *Task) (*ExecutionResult, error) {
		// Every child "completes" but delivers nothing: no tokens, no files,
		// no commit, no PR — a ghost success.
		return &ExecutionResult{TaskID: task.ID, Success: true}, nil
	})

	task := &Task{ID: "GH-3938ZERO", Title: "[epic] zero delivery no-op guard test"}
	if err := store.SaveExecution(&memory.Execution{ID: task.ID, TaskID: task.ID, Status: "running"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	result, err := r.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Success {
		t.Errorf("expected Success=false after zero-delivery reclassification, got true")
	}
	if result.Outcome != "no_op" {
		t.Errorf("Outcome = %q, want %q", result.Outcome, "no_op")
	}
	if result.Error == "" {
		t.Error("expected a descriptive no-op reason, got empty Error")
	}

	events, err := store.ListExecutionEvents(task.LogExecutionID())
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected execution events to be recorded")
	}
	last := events[len(events)-1]
	if last.Stage != memory.StageNoOp {
		t.Errorf("terminal event stage = %q, want %q (must NOT be StageCompleted)", last.Stage, memory.StageNoOp)
	}
}

// TestClassifyZeroDeliveryEpicCompletion is a focused table-driven unit test
// for the guard predicate itself, independent of the full Execute() path.
func TestClassifyZeroDeliveryEpicCompletion(t *testing.T) {
	tests := []struct {
		name           string
		result         *ExecutionResult
		wantSuccess    bool
		wantOutcome    string
		wantUnaffected bool // if true, the result must be byte-for-byte unchanged
	}{
		{
			name:        "zero everything on an epic result is reclassified as no_op",
			result:      &ExecutionResult{Success: true, IsEpic: true},
			wantSuccess: false,
			wantOutcome: "no_op",
		},
		{
			name:           "nonzero TokensOutput leaves an epic result untouched",
			result:         &ExecutionResult{Success: true, IsEpic: true, TokensOutput: 5},
			wantSuccess:    true,
			wantUnaffected: true,
		},
		{
			name:           "nonzero FilesChanged leaves an epic result untouched",
			result:         &ExecutionResult{Success: true, IsEpic: true, FilesChanged: 1},
			wantSuccess:    true,
			wantUnaffected: true,
		},
		{
			name:           "a commit SHA leaves an epic result untouched",
			result:         &ExecutionResult{Success: true, IsEpic: true, CommitSHA: "abc123"},
			wantSuccess:    true,
			wantUnaffected: true,
		},
		{
			name:           "a PR URL leaves an epic result untouched",
			result:         &ExecutionResult{Success: true, IsEpic: true, PRUrl: "https://github.com/o/r/pull/1"},
			wantSuccess:    true,
			wantUnaffected: true,
		},
		{
			name: "non-epic zero-delivery completions are legitimate and untouched " +
				"(GH-3846 synthetic-dispatch coverage relies on this)",
			result:         &ExecutionResult{Success: true, IsEpic: false},
			wantSuccess:    true,
			wantUnaffected: true,
		},
		{
			name:           "an already-failed result is untouched",
			result:         &ExecutionResult{Success: false, IsEpic: true, Error: "some other failure"},
			wantSuccess:    false,
			wantUnaffected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := *tt.result
			classifyZeroDeliveryEpicCompletion(tt.result)
			if tt.wantUnaffected {
				if tt.result.Success != before.Success || tt.result.Outcome != before.Outcome || tt.result.Error != before.Error {
					t.Errorf("result mutated: got %+v, want unchanged %+v", tt.result, before)
				}
				return
			}
			if tt.result.Success != tt.wantSuccess {
				t.Errorf("Success = %v, want %v", tt.result.Success, tt.wantSuccess)
			}
			if tt.result.Outcome != tt.wantOutcome {
				t.Errorf("Outcome = %q, want %q", tt.result.Outcome, tt.wantOutcome)
			}
		})
	}

	// nil must not panic.
	classifyZeroDeliveryEpicCompletion(nil)
}

// ghAppendLogScript writes a fake "gh" executable to a temp bin dir that
// appends every invocation's arguments to logPath (one field per arg,
// \x1f-separated; \x1e-separated between invocations) and always exits 0.
// Mirrors the fake-gh-with-captured-args pattern already used in epic_test.go
// (TestCreateSubIssuesViaGitHub_PropagatesNoDecomposeLabel), extended to
// accumulate across multiple calls instead of truncating on each one.
func ghAppendLogScript(t *testing.T, logPath string) {
	t.Helper()
	fakeBin := t.TempDir()
	script := filepath.Join(fakeBin, "gh")
	content := "#!/bin/sh\n" +
		"{ for arg in \"$@\"; do printf '%s\\037' \"$arg\"; done; printf '\\036'; } >> \"$GH3938_TEST_LOG\"\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("GH3938_TEST_LOG", logPath)
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+origPATH)
}

// parseGhInvocations splits a ghAppendLogScript log file into one []string of
// args per "gh" invocation, in call order.
func parseGhInvocations(t *testing.T, logPath string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read gh log: %v", err)
	}
	var calls [][]string
	for _, record := range strings.Split(string(raw), "\x1e") {
		if record == "" {
			continue
		}
		fields := strings.Split(record, "\x1f")
		// Trailing separator leaves one empty trailing field.
		if len(fields) > 0 && fields[len(fields)-1] == "" {
			fields = fields[:len(fields)-1]
		}
		calls = append(calls, fields)
	}
	return calls
}

// TestExecuteSubIssuesTracked_PostsDecomposedChildrenComment covers GH-3938
// acceptance (c): decomposition posts an honest, evidence-sourced
// "decomposed into N children: #a, #b, #c" comment on the parent issue,
// sourced from the actual created sub-issues — not a generic placeholder.
func TestExecuteSubIssuesTracked_PostsDecomposedChildrenComment(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "gh-calls.log")
	ghAppendLogScript(t, logPath)

	runner := newTestRunnerWithExecFunc(func(_ context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			CommitSHA: "sha-" + task.ID,
			PRUrl:     "https://github.com/owner/repo/pull/" + task.ID,
		}, nil
	})
	runner.dryRun = false // this test needs the real (faked) gh CLI path

	subs := gh3938MultiPackageSubtasks()
	issues := []CreatedIssue{
		{Number: 501, Subtask: subs[0]},
		{Number: 502, Subtask: subs[1]},
	}
	parent := &Task{ID: "GH-3938PARENT", ProjectPath: t.TempDir()}

	_, metrics, err := runner.executeSubIssuesTracked(context.Background(), parent, issues, "", "")
	if err != nil {
		t.Fatalf("executeSubIssuesTracked failed: %v", err)
	}
	if metrics == nil {
		t.Fatal("expected non-nil metrics")
	}

	// GH-4405: gh CLI's positional issue argument is the bare number
	// ("3938PARENT" once "GH-" is stripped), not the human-readable
	// prefixed parent.ID ("GH-3938PARENT") gh CLI rejects.
	parentRef := parent.GHIssueRef()
	calls := parseGhInvocations(t, logPath)
	var found string
	for _, args := range calls {
		if len(args) >= 4 && args[0] == "issue" && args[1] == "comment" && args[2] == parentRef {
			// args: issue comment <id> --body <message>
			for i, a := range args {
				if a == "--body" && i+1 < len(args) {
					found = args[i+1]
				}
			}
			if found != "" {
				break
			}
		}
	}
	if found == "" {
		t.Fatalf("no 'gh issue comment %s --body ...' call captured; calls = %v", parentRef, calls)
	}
	want := "decomposed into 2 children: #501, #502"
	if !strings.Contains(found, want) {
		t.Errorf("decomposition comment = %q, want it to contain %q", found, want)
	}
}

// TestGH3938_ExistingCloseGuardsUnchanged verifies the two pre-existing
// epic-parent close guards this change sits next to are unaffected:
//   - GH-3513 text-search confirmation: CreateSubIssues still refuses to
//     spawn duplicate sub-issues when the dedup checker reports a recent
//     sibling.
//   - Open-PR / work-loss veto: a child that commits real work but produces
//     no PR still halts the epic instead of silently discarding the work,
//     and still does so with the executeSubIssuesTracked signature change
//     (metrics is returned alongside the error, not swallowed).
func TestGH3938_ExistingCloseGuardsUnchanged(t *testing.T) {
	t.Run("GH-3513 text-search confirmation still blocks duplicate creation", func(t *testing.T) {
		r := NewRunner()
		r.dryRun = true
		r.openSubIssueCheck = func(_ context.Context, _, _ string) (bool, error) {
			return true, nil // simulate a recently-created sibling found via text search
		}
		plan := &EpicPlan{
			ParentTask: &Task{ID: "GH-3513CHECK", Title: "feat(scope): some epic", State: "open"},
			Subtasks:   []PlannedSubtask{{Order: 1, Title: "feat(scope): sub-task one", Description: "do it"}},
		}
		_, err := r.CreateSubIssues(context.Background(), plan, "")
		if !errors.Is(err, ErrSubIssuesAlreadyExist) {
			t.Errorf("CreateSubIssues error = %v, want ErrSubIssuesAlreadyExist", err)
		}
	})

	t.Run("open-PR veto still halts the epic on commit-without-PR child", func(t *testing.T) {
		runner := newTestRunnerWithExecFunc(func(_ context.Context, task *Task) (*ExecutionResult, error) {
			return &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "committed-but-stranded", PRUrl: ""}, nil
		})
		issues := []CreatedIssue{
			{Number: 601, Subtask: PlannedSubtask{Order: 1, Title: "feat(scope): sub-task", Description: "do it"}},
		}
		parent := &Task{ID: "GH-8888", ProjectPath: t.TempDir()}

		_, metrics, err := runner.executeSubIssuesTracked(context.Background(), parent, issues, "", t.TempDir())
		if err == nil {
			t.Fatal("expected the work-loss guard to return an error, got nil")
		}
		if !strings.Contains(err.Error(), "produced no PR") {
			t.Errorf("error = %v, want it to mention 'produced no PR'", err)
		}
		if metrics == nil {
			t.Error("expected non-nil metrics even when the guard vetoes the epic (signature change regression check)")
		}
	})
}
