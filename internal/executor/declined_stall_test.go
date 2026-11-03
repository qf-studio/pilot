package executor

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/qf-studio/pilot/internal/memory"
)

const declineReasonForTest = "the issue body names a file that does not exist on main"

// TestWorker_DeclinedRowPersistsReason covers GH-6033: the terminal row a
// declined run leaves behind carries `declined: <reason>`, written through the
// same Classify/Persist chokepoint the worker uses, while a refusal-classed
// decline keeps its own text so IsRefusalFailure still matches.
func TestWorker_DeclinedRowPersistsReason(t *testing.T) {
	tests := []struct {
		name      string
		result    *ExecutionResult
		wantError func(string) bool
	}{
		{
			name: "decline with reason replaces the no-commit text",
			result: &ExecutionResult{
				Declined:       true,
				DeclinedReason: declineReasonForTest,
				Outcome:        "declined",
				Error:          ghostSHACleanNoCommitError,
			},
			wantError: func(e string) bool {
				return e == "declined: "+declineReasonForTest
			},
		},
		{
			name: "refusal-classed decline keeps its error text",
			result: &ExecutionResult{
				Declined:       true,
				DeclinedReason: declineReasonForTest,
				Outcome:        "declined",
				Error:          "refusal: policy: not allowed",
			},
			wantError: IsRefusalFailure,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			task := &Task{ID: "GH-6033", ProjectPath: "/project-gh-6033-row"}
			lifecycle := NewExecutionLifecycle(store)
			execID, err := lifecycle.Begin(task, ExecStatusRunning, 0)
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			outcome := lifecycle.Classify(tt.result, nil)
			if outcome.Status != ExecStatusDeclined {
				t.Fatalf("outcome status = %q, want declined", outcome.Status)
			}
			if err := lifecycle.Persist(execID, outcome, tt.result, time.Second); err != nil {
				t.Fatalf("Persist: %v", err)
			}
			exec, err := store.GetExecution(execID)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if exec.Status != string(ExecStatusDeclined) {
				t.Fatalf("row status = %q, want declined", exec.Status)
			}
			if !tt.wantError(exec.Error) {
				t.Errorf("row error = %q", exec.Error)
			}
			if tt.result.Error == ghostSHACleanNoCommitError && !strings.HasPrefix(exec.Error, "declined: ") {
				t.Errorf("row error %q must start with %q", exec.Error, "declined: ")
			}
		})
	}
}

// TestDispatcher_DeclinedRunStallsWithoutRepick covers GH-6033: one declined
// row with a reason stalls the task on the next pick and no second
// generation is claimed; later ticks stay dropped without rewriting the hold.
func TestDispatcher_DeclinedRunStallsWithoutRepick(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// A non-GitHub SourceAdapter makes surfaceStalledIssue return before the
	// gh CLI (an empty adapter does not); the fake gh proves it is never run.
	logFile := setupFakeGhCLI(t)
	task := &Task{ID: "GH-6033", ProjectPath: t.TempDir(), SourceAdapter: "linear"}
	seedNoProgressRow(t, store, task, 0, ExecStatusDeclined, declinedErrorPrefix+declineReasonForTest)
	dispatcher := NewDispatcher(store, NewRunner(), nil)

	for i := 0; i < 3; i++ {
		execID, err := dispatcher.beginWithGenerationRetry(task, ExecStatusQueued)
		if err != nil || execID != "" {
			t.Fatalf("pick %d: want dropped, got execID=%q err=%v", i, execID, err)
		}
	}
	gen, claimed := latestClaimStatus(t, store, task)
	if gen != 0 {
		t.Errorf("no generation 1 may be claimed, latest generation = %d", gen)
	}
	if claimed.Status != string(ExecStatusStalled) {
		t.Fatalf("claimed execution status = %q, want stalled", claimed.Status)
	}
	if want := declinedStalledReasonPrefix + declineReasonForTest; claimed.Error != want {
		t.Errorf("stall reason = %q, want %q", claimed.Error, want)
	}
	// GH-6042 item 5 mutation pin: with an empty SourceAdapter the stall
	// reaches the gh path, so this fails.
	if _, err := os.ReadFile(logFile); err == nil {
		t.Error("a non-GitHub SourceAdapter must not invoke the gh CLI")
	}
}

// TestDispatcher_DeclinedWithoutReasonUsesGenericCap: a decline without a
// parsed reason is not stalled at once; it still counts toward the generic
// no-progress cap.
func TestDispatcher_DeclinedWithoutReasonUsesGenericCap(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-6033", ProjectPath: "/project-gh-6033-noreason"}
	seedNoProgressRow(t, store, task, 0, ExecStatusDeclined, "")
	dispatcher := NewDispatcher(store, NewRunner(), nil)

	execID, err := dispatcher.beginWithGenerationRetry(task, ExecStatusQueued)
	if err != nil || execID == "" {
		t.Fatalf("a reasonless decline must still be re-picked once, got execID=%q err=%v", execID, err)
	}
}

// TestDispatcher_StallCommentCarriesDeclineReason: the comment surfaceStalledIssue
// actually posts through the gh CLI quotes the decline reason verbatim and
// tells the operator how to re-arm. GH-6042 item 4 mutation pin: it reads the
// posted `gh issue comment` argv, so dropping the reason at the real sink fails it.
func TestDispatcher_StallCommentCarriesDeclineReason(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	logFile := setupFakeGhCLI(t)
	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false}, nil
	})

	task := &Task{ID: "GH-6033", ProjectPath: t.TempDir(), SourceAdapter: "github"}
	seedNoProgressRow(t, store, task, 0, ExecStatusDeclined, declinedErrorPrefix+declineReasonForTest)
	dispatcher := NewDispatcher(store, NewRunner(), nil)
	if execID, err := dispatcher.beginWithGenerationRetry(task, ExecStatusQueued); err != nil || execID != "" {
		t.Fatalf("pick must stall, got execID=%q err=%v", execID, err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read gh CLI log: %v", err)
	}
	want := "issue comment 6033 --body Pilot declined this issue after one run: " + declineReasonForTest +
		". Rescope the issue body or close it; re-add the `pilot` label to re-arm."
	var got string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "issue comment ") {
			got = line
		}
	}
	if got != want {
		t.Errorf("posted gh call = %q, want %q", got, want)
	}
}

// TestDispatcher_DeclineStallJudgesClaimedRow: the decline check reads the row
// the claim ledger points at, the one escalateStalledTask would stall, not the
// newest row for the task. GH-6042 item 1 mutation pin: with the newest
// (unclaimed, declined) row judged instead, the claimed failed row is stalled.
func TestDispatcher_DeclineStallJudgesClaimedRow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-6033", ProjectPath: "/project-gh-6033-claimed", SourceAdapter: "linear"}
	claimedID := seedNoProgressRow(t, store, task, 0, ExecStatusFailed, "transient: connection reset")
	newer := &memory.Execution{
		ID:          uuid.New().String(),
		TaskID:      task.ID,
		ProjectPath: task.ProjectPath,
		Status:      string(ExecStatusDeclined),
		CreatedAt:   time.Now().Add(time.Hour),
	}
	if err := store.SaveExecution(newer); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.UpdateExecutionStatus(newer.ID, string(ExecStatusDeclined), declinedErrorPrefix+declineReasonForTest); err != nil {
		t.Fatalf("UpdateExecutionStatus: %v", err)
	}
	dispatcher := NewDispatcher(store, NewRunner(), nil)

	_, _ = dispatcher.beginWithGenerationRetry(task, ExecStatusQueued)

	claimed, err := store.GetExecution(claimedID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if claimed.Status == string(ExecStatusStalled) || strings.HasPrefix(claimed.Error, declinedStalledReasonPrefix) {
		t.Errorf("claimed non-declined row was stalled as a decline: status=%q error=%q", claimed.Status, claimed.Error)
	}
}

// TestDispatcher_DeclineStallAlertMetadata: the task-failed alert escalateStalledTask
// emits for a decline stall carries reason=declined_stalled and the decline
// reason. GH-6042 item 6 mutation pin: dropping either key fails it.
func TestDispatcher_DeclineStallAlertMetadata(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)

	task := &Task{ID: "GH-6033", ProjectPath: "/project-gh-6033-alert", SourceAdapter: "linear"}
	seedNoProgressRow(t, store, task, 0, ExecStatusDeclined, declinedErrorPrefix+declineReasonForTest)
	dispatcher := NewDispatcher(store, runner, nil)
	if execID, err := dispatcher.beginWithGenerationRetry(task, ExecStatusQueued); err != nil || execID != "" {
		t.Fatalf("pick must stall, got execID=%q err=%v", execID, err)
	}

	if len(processor.events) != 1 {
		t.Fatalf("want exactly 1 alert event, got %d: %+v", len(processor.events), processor.events)
	}
	ev := processor.events[0]
	if ev.Type != AlertEventTypeTaskFailed {
		t.Errorf("alert type = %q, want %q", ev.Type, AlertEventTypeTaskFailed)
	}
	if got := ev.Metadata["reason"]; got != "declined_stalled" {
		t.Errorf("metadata reason = %q, want declined_stalled", got)
	}
	if got := ev.Metadata["decline_reason"]; got != declineReasonForTest {
		t.Errorf("metadata decline_reason = %q, want %q", got, declineReasonForTest)
	}
}

// TestEpicCoverageGapDecline_DoesNotStallOnFirstRun: an epic coverage-gap decline
// can be transient, so it carries no DeclinedReason and Classify leaves its
// error text unprefixed (it keeps riding the generic cap). GH-6042 item 2
// mutation pin: setting DeclinedReason on the result fails it.
func TestEpicCoverageGapDecline_DoesNotStallOnFirstRun(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	gapErr := &SubIssueCoverageGapError{}
	result := epicCoverageGapDeclinedResult("GH-6033", &EpicPlan{}, gapErr, time.Now())
	if !result.Declined {
		t.Fatal("coverage-gap result must be declined")
	}

	task := &Task{ID: "GH-6033", ProjectPath: "/project-gh-6033-gap"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning, 0)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	outcome := lifecycle.Classify(result, nil)
	if err := lifecycle.Persist(execID, outcome, result, time.Second); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != string(ExecStatusDeclined) {
		t.Fatalf("row status = %q, want declined", exec.Status)
	}
	if reason, ok := declinedStallReason(exec); ok {
		t.Errorf("coverage-gap decline must not be a first-run stall, got reason %q", reason)
	}
	if strings.HasPrefix(exec.Error, declinedErrorPrefix) {
		t.Errorf("row error %q must not carry %q", exec.Error, declinedErrorPrefix)
	}
}

// TestTerminalOutcomeText_DeclinedPrefixNotDoubled: progress text for a decline
// reads "declined: <reason>", not "declined: declined: <reason>". GH-6042 item 3
// mutation pin: rendering outcome.Error unstripped fails it.
func TestTerminalOutcomeText_DeclinedPrefixNotDoubled(t *testing.T) {
	tests := []struct {
		name    string
		outcome FinishOutcome
		want    string
	}{
		{"declined with persisted prefix", FinishOutcome{Status: ExecStatusDeclined, Error: declinedErrorPrefix + declineReasonForTest}, "declined: " + declineReasonForTest},
		{"refusal-classed decline unchanged", FinishOutcome{Status: ExecStatusDeclined, Error: "refusal: policy: not allowed"}, "declined: refusal: policy: not allowed"},
		{"other status unchanged", FinishOutcome{Status: ExecStatusFailed, Error: "declined: kept"}, "failed: declined: kept"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := terminalOutcomeText(tt.outcome, 200); got != tt.want {
				t.Errorf("terminalOutcomeText = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDispatcher_RefusalDeclineStillEscalatesAsRefusal: a refusal-classed
// failure takes escalateRefusal (status declined, original text kept), not the
// declined-stall path, and a declined row with refusal text is not a
// declinedStallReason match.
func TestDispatcher_RefusalDeclineStillEscalatesAsRefusal(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	const refusalErr = "refusal: policy: not allowed"
	task := &Task{ID: "GH-6033", ProjectPath: "/project-gh-6033-refusal"}
	seedNoProgressRow(t, store, task, 0, ExecStatusFailed, refusalErr)
	dispatcher := NewDispatcher(store, NewRunner(), nil)

	if execID, err := dispatcher.beginWithGenerationRetry(task, ExecStatusQueued); err != nil || execID != "" {
		t.Fatalf("pick must be dropped, got execID=%q err=%v", execID, err)
	}
	_, claimed := latestClaimStatus(t, store, task)
	if claimed.Status != string(ExecStatusDeclined) || claimed.Error != refusalErr {
		t.Errorf("refusal must flip to declined keeping its text, got status=%q error=%q", claimed.Status, claimed.Error)
	}
	if _, ok := declinedStallReason(claimed); ok {
		t.Error("a refusal-classed declined row must not match declinedStallReason")
	}
	// Later ticks stay dropped via priorClaimWasEscalatedRefusal.
	if execID, err := dispatcher.beginWithGenerationRetry(task, ExecStatusQueued); err != nil || execID != "" {
		t.Fatalf("later tick must be dropped, got execID=%q err=%v", execID, err)
	}
}
