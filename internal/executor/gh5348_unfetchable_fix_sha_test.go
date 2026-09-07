package executor

import (
	"context"
	"os"
	"strings"
	"testing"
)

// GH-5348 (subtask 2): when an autopilot-fix task recorded a base SHA
// (task.FixFromSHA != "") but ResolveFixContinuationBaseRef (git.go) could
// resolve neither a live origin/<branch> nor the exact recorded commit, the
// pre-existing behavior was to fall through to worktree creation with an
// empty base ref — which defaults to origin/main and silently rebuilds the
// "fix" from main, discarding the original PR's commits (the pilot-console
// #275 incident). escalateUnfetchableFixSHA (runner.go) closes that gap: it
// must park the fix issue itself under pilot-needs-human and fail the task
// instead of proceeding, and it must never touch the original issue — only
// delivery evidence (a merged fix PR whose files overlap the original) ever
// justifies marking the original pilot-superseded.

// TestEscalateUnfetchableFixSHA_LabelsFixIssueNeedsHuman covers the primary
// contract: the fix issue (task.GHIssueRef(), not the original issue) gets
// pilot-needs-human applied and pilot-retry-ready shed in the same mutation,
// mirroring escalateBasePresenceHold's never-coexist invariant.
func TestEscalateUnfetchableFixSHA_LabelsFixIssueNeedsHuman(t *testing.T) {
	logFile := setupFakeGhCLI(t)

	runner := NewRunner()
	task := &Task{
		ID:            "GH-9600",
		Title:         "fix issue with unfetchable recorded SHA",
		ProjectPath:   t.TempDir(),
		SourceAdapter: "github",
		SourceIssueID: "9600",
		Branch:        "pilot/GH-9500",
		FixFromSHA:    "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	}

	result, err := runner.escalateUnfetchableFixSHA(context.Background(), task)

	if err == nil {
		t.Fatal("expected a non-nil error so the caller treats this as a failed task, not a silent fallback")
	}
	if result == nil || result.Success {
		t.Fatalf("expected a failed ExecutionResult, got %+v", result)
	}

	data, rerr := os.ReadFile(logFile)
	if rerr != nil {
		t.Fatalf("read gh CLI log: %v", rerr)
	}
	log := string(data)

	if got := strings.Count(log, "issue edit 9600"); got != 1 {
		t.Fatalf("expected exactly one `gh issue edit 9600` call, got %d (log: %q)", got, log)
	}
	if !strings.Contains(log, "--add-label pilot-needs-human") {
		t.Errorf("expected the fix issue to be labeled pilot-needs-human, got log: %q", log)
	}
	if !strings.Contains(log, "--remove-label pilot-retry-ready") {
		t.Errorf("expected pilot-retry-ready to be shed in the same mutation, got log: %q", log)
	}
	if strings.Contains(log, "issue edit ") && !strings.Contains(log, "issue edit 9600") {
		t.Errorf("expected the label mutation to target the fix issue #9600 only, got log: %q", log)
	}
}

// TestEscalateUnfetchableFixSHA_OriginalIssueUntouched is the acceptance
// criterion this subtask exists for: the original issue that spawned the fix
// (identified only by prose inside the fix issue body, which this function
// never parses or touches) must receive zero gh CLI calls. Only the fix
// issue's own number (task.GHIssueRef()) is ever referenced.
func TestEscalateUnfetchableFixSHA_OriginalIssueUntouched(t *testing.T) {
	logFile := setupFakeGhCLI(t)

	runner := NewRunner()
	task := &Task{
		ID:            "GH-9601",
		Title:         "fix issue for original #275",
		ProjectPath:   t.TempDir(),
		SourceAdapter: "github",
		SourceIssueID: "9601",
		Branch:        "pilot/GH-275",
		FixFromSHA:    "cafebabecafebabecafebabecafebabecafebabe",
	}

	if _, err := runner.escalateUnfetchableFixSHA(context.Background(), task); err == nil {
		t.Fatal("expected escalateUnfetchableFixSHA to return an error")
	}

	data, rerr := os.ReadFile(logFile)
	if rerr != nil {
		t.Fatalf("read gh CLI log: %v", rerr)
	}
	log := string(data)
	// The branch name embedded in the explanatory comment ("pilot/GH-275")
	// legitimately quotes "275" as context — what must never happen is a gh
	// CLI call whose *target* issue argument is 275 (edit/comment/close).
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if (f == "edit" || f == "comment" || f == "close") && i+1 < len(fields) && fields[i+1] == "275" {
				t.Errorf("original issue #275 must never be a gh CLI target, got line: %q", line)
			}
		}
	}
}

// TestEscalateUnfetchableFixSHA_PostsExplanatoryComment mirrors GH-5301's
// standing rule (escalateBasePresenceHold, dispatcher.go): every
// pilot-needs-human application must carry a comment naming the cause, not a
// bare label mutation an operator has to reverse-engineer.
func TestEscalateUnfetchableFixSHA_PostsExplanatoryComment(t *testing.T) {
	logFile := setupFakeGhCLI(t)

	runner := NewRunner()
	task := &Task{
		ID:            "GH-9602",
		Title:         "fix issue explanatory comment",
		ProjectPath:   t.TempDir(),
		SourceAdapter: "github",
		SourceIssueID: "9602",
		Branch:        "pilot/GH-9502",
		FixFromSHA:    "0123456789abcdef0123456789abcdef01234567",
	}

	if _, err := runner.escalateUnfetchableFixSHA(context.Background(), task); err == nil {
		t.Fatal("expected escalateUnfetchableFixSHA to return an error")
	}

	data, rerr := os.ReadFile(logFile)
	if rerr != nil {
		t.Fatalf("read gh CLI log: %v", rerr)
	}
	log := string(data)
	if !strings.Contains(log, "issue comment 9602") {
		t.Fatalf("expected a `gh issue comment 9602` call, got log: %q", log)
	}
	if !strings.Contains(log, "unfetchable") {
		t.Errorf("expected the comment to name the unfetchable-SHA cause, got log: %q", log)
	}
}

// TestEscalateUnfetchableFixSHA_EmitsAlertsEngineEvent ensures operator
// visibility does not rest solely on label-watching, mirroring
// escalateBasePresenceHold's AlertEventTypeTaskFailed emission.
func TestEscalateUnfetchableFixSHA_EmitsAlertsEngineEvent(t *testing.T) {
	setupFakeGhCLI(t)

	runner := NewRunner()
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)

	task := &Task{
		ID:            "GH-9603",
		Title:         "fix issue alert coverage",
		ProjectPath:   t.TempDir(),
		SourceAdapter: "github",
		SourceIssueID: "9603",
		Branch:        "pilot/GH-9503",
		FixFromSHA:    "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
	}

	if _, err := runner.escalateUnfetchableFixSHA(context.Background(), task); err == nil {
		t.Fatal("expected escalateUnfetchableFixSHA to return an error")
	}

	// reportProgress (called for dashboard visibility) also emits its own
	// AlertEventTypeTaskProgress event, so this asserts the TaskFailed event
	// is present among whatever else fired, rather than requiring it to be
	// the only event.
	var failedEvents []AlertEvent
	for _, e := range processor.events {
		if e.Type == AlertEventTypeTaskFailed {
			failedEvents = append(failedEvents, e)
		}
	}
	if len(failedEvents) != 1 {
		t.Fatalf("expected exactly 1 %s alert event, got %d (all events: %+v)", AlertEventTypeTaskFailed, len(failedEvents), processor.events)
	}
	event := failedEvents[0]
	if event.TaskID != task.ID {
		t.Errorf("event.TaskID = %q, want %q", event.TaskID, task.ID)
	}
}
