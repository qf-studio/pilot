package executor

import (
	"strings"
	"testing"
	"time"
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

	// SourceAdapter left empty so surfaceStalledIssue skips the gh CLI.
	task := &Task{ID: "GH-6033", ProjectPath: "/project-gh-6033-stall"}
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

// TestDispatcher_StallCommentCarriesDeclineReason: the posted comment quotes
// the decline reason verbatim and tells the operator how to re-arm.
func TestDispatcher_StallCommentCarriesDeclineReason(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-6033", ProjectPath: "/project-gh-6033-comment"}
	seedNoProgressRow(t, store, task, 0, ExecStatusDeclined, declinedErrorPrefix+declineReasonForTest)
	dispatcher := NewDispatcher(store, NewRunner(), nil)
	if execID, err := dispatcher.beginWithGenerationRetry(task, ExecStatusQueued); err != nil || execID != "" {
		t.Fatalf("pick must stall, got execID=%q err=%v", execID, err)
	}
	_, claimed := latestClaimStatus(t, store, task)

	want := "Pilot declined this issue after one run: " + declineReasonForTest +
		". Rescope the issue body or close it; re-add the `pilot` label to re-arm."
	if got := stalledIssueComment(claimed.Error, 6033); got != want {
		t.Errorf("comment = %q, want %q", got, want)
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
