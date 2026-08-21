package executor

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// GH-5079: once postTitleRejectionEscalation (title_rejection.go) advances
// the pilot-failed-retry ladder to its terminal pilot-failed-retry-exhausted
// rung (GH-5077/PR#5081), it must also apply pilot-needs-human (so the issue
// parks under the GH-5056 admission check, cmd/pilot/handlers.go:751) and
// fire an escalation-class alert that renders through the PR#5069
// event.Error fallback — not just advance the ladder label silently. Both
// must fire on the exhausted transition only: every earlier rung
// (none->retry-1, retry-1->retry-2) must leave pilot-needs-human/pilot-
// retry-ready untouched and emit no alert.

// runPostTitleRejectionEscalationWithAlerts mirrors
// runPostTitleRejectionEscalation (gh5077_failed_retry_ladder_test.go) but
// also wires a fakeAlertProcessor so tests can assert on emitted alert
// events, not just the gh CLI label mutation.
func runPostTitleRejectionEscalationWithAlerts(t *testing.T, issueNum int, existingLabels []string) (calls string, events []AlertEvent) {
	t.Helper()
	logFile := setupFakeGhCLI(t)

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false, Labels: existingLabels}, nil
	})

	r := newSilentRunnerTask359()
	processor := &fakeAlertProcessor{}
	r.SetAlertProcessor(processor)
	task := &Task{
		ID:            "GH-" + strconv.Itoa(issueNum),
		Title:         "not a conventional title",
		ProjectPath:   t.TempDir(),
		SourceAdapter: "github",
		SourceIssueID: strconv.Itoa(issueNum),
	}

	if err := r.postTitleRejectionEscalation(context.Background(), task); err != nil {
		t.Fatalf("postTitleRejectionEscalation: %v", err)
	}

	logBytes, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("expected gh CLI to be invoked: %v", err)
	}
	return string(logBytes), processor.events
}

// TestPostTitleRejectionEscalation_ExhaustedTransition_ParksNeedsHumanAndAlerts
// covers the retry-2 -> exhausted transition: the same `gh issue edit` call
// that stamps pilot-failed-retry-exhausted must also add pilot-needs-human
// and remove pilot-retry-ready, and exactly one escalation alert must fire
// with non-blank, task-identifying content.
func TestPostTitleRejectionEscalation_ExhaustedTransition_ParksNeedsHumanAndAlerts(t *testing.T) {
	calls, events := runPostTitleRejectionEscalationWithAlerts(t, 9301, []string{labelPilotFailedRetry2})

	if !strings.Contains(calls, "--add-label "+labelPilotFailedRetryExhausted) {
		t.Errorf("expected %s to be added, got calls:\n%s", labelPilotFailedRetryExhausted, calls)
	}
	editLines := grepLinesStartingWith(calls, "issue edit")
	if len(editLines) != 1 {
		t.Fatalf("expected exactly 1 `gh issue edit` call, got %d:\n%s", len(editLines), calls)
	}
	edit := editLines[0]
	if !strings.Contains(edit, "--add-label "+labelPilotFailedRetryExhausted) {
		t.Errorf("expected exhausted rung in the issue edit call, got: %q", edit)
	}
	if !strings.Contains(edit, "--add-label "+labelPilotNeedsHuman) {
		t.Errorf("expected pilot-needs-human to be added in the same mutation, got: %q", edit)
	}
	if !strings.Contains(edit, "--remove-label "+labelPilotRetryReady) {
		t.Errorf("expected pilot-retry-ready to be removed in the same mutation (GH-5042/PR#5048 never-coexist invariant), got: %q", edit)
	}

	if len(events) != 1 {
		t.Fatalf("expected exactly 1 alert event, got %d: %+v", len(events), events)
	}
	event := events[0]
	if event.Type != AlertEventTypeEscalation {
		t.Errorf("event.Type = %q, want %q", event.Type, AlertEventTypeEscalation)
	}
	if event.TaskID != "GH-9301" {
		t.Errorf("event.TaskID = %q, want %q", event.TaskID, "GH-9301")
	}
	if event.Error == "" {
		t.Fatal("event.Error must be populated so handleEscalation's PR#5069 fallback renders real content, not a blank template")
	}
	if !strings.Contains(event.Error, "9301") {
		t.Errorf("event.Error should name the issue, got: %q", event.Error)
	}
	if !strings.Contains(event.Error, labelPilotFailedRetryExhausted) {
		t.Errorf("event.Error should name the exhausted rung, got: %q", event.Error)
	}
}

// TestPostTitleRejectionEscalation_NonExhaustedTransitions_NoEscalation covers
// the earlier rungs (none -> retry-1, retry-1 -> retry-2): neither must apply
// pilot-needs-human/remove pilot-retry-ready, nor fire any alert — escalation
// is reserved for the exhausted transition only.
func TestPostTitleRejectionEscalation_NonExhaustedTransitions_NoEscalation(t *testing.T) {
	tests := []struct {
		name           string
		existingLabels []string
	}{
		{name: "no ladder label yet -> retry-1", existingLabels: nil},
		{name: "retry-1 present -> retry-2", existingLabels: []string{labelPilotFailedRetry1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, events := runPostTitleRejectionEscalationWithAlerts(t, 9302, tt.existingLabels)

			if strings.Contains(calls, "--add-label "+labelPilotNeedsHuman) {
				t.Errorf("expected no pilot-needs-human on a non-exhausted transition, got calls:\n%s", calls)
			}
			if strings.Contains(calls, "--remove-label "+labelPilotRetryReady) {
				t.Errorf("expected no pilot-retry-ready removal on a non-exhausted transition, got calls:\n%s", calls)
			}
			if len(events) != 0 {
				t.Errorf("expected no alert events on a non-exhausted transition, got %d: %+v", len(events), events)
			}
		})
	}
}

// TestPostTitleRejectionEscalation_AlreadyExhausted_NoDuplicateEscalation
// covers the terminal-rung idempotence guard: a repeat pilot-failed
// application against an issue already at pilot-failed-retry-exhausted must
// not re-fire the escalation (it already fired the first time the ladder
// reached that rung) — nextFailedRetryLabel returns no further advancement
// past exhaustion, so `exhausted` must stay false here too.
func TestPostTitleRejectionEscalation_AlreadyExhausted_NoDuplicateEscalation(t *testing.T) {
	calls, events := runPostTitleRejectionEscalationWithAlerts(t, 9303, []string{labelPilotFailedRetryExhausted})

	if strings.Contains(calls, "--add-label "+labelPilotNeedsHuman) {
		t.Errorf("expected no duplicate pilot-needs-human application, got calls:\n%s", calls)
	}
	if len(events) != 0 {
		t.Errorf("expected no duplicate escalation alert, got %d: %+v", len(events), events)
	}
}

// setupFakeGhCLI_FailEditOnly installs a fake `gh` binary that succeeds for
// every invocation except `gh issue edit` (which exits 1, simulating a
// transient GitHub API error on the label mutation specifically) —
// postTitleRejectionEscalation's comment step must still go through so
// execution reaches the ladder/escalation logic below it.
func setupFakeGhCLI_FailEditOnly(t *testing.T) {
	t.Helper()
	fakeBin := t.TempDir()
	script := filepath.Join(fakeBin, "gh")
	content := "#!/bin/sh\n" +
		`if [ "$1" = "issue" ] && [ "$2" = "edit" ]; then echo "simulated gh issue edit failure" >&2; exit 1; fi` +
		"\nexit 0\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+origPATH)
}

// TestPostTitleRejectionEscalation_ExhaustedAlertFiresEvenWhenLabelCallFails
// mirrors TestEscalateBasePresenceHold_AlertFiresEvenWhenLabelCallFails
// (base_presence_escalation_gh5056_test.go): the escalation alert must not
// be gated on the best-effort `gh issue edit` mutation actually succeeding —
// a labeling failure must not also silently swallow operator visibility.
func TestPostTitleRejectionEscalation_ExhaustedAlertFiresEvenWhenLabelCallFails(t *testing.T) {
	setupFakeGhCLI_FailEditOnly(t)

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false, Labels: []string{labelPilotFailedRetry2}}, nil
	})

	r := newSilentRunnerTask359()
	processor := &fakeAlertProcessor{}
	r.SetAlertProcessor(processor)
	task := &Task{
		ID:            "GH-9304",
		Title:         "not a conventional title",
		ProjectPath:   t.TempDir(),
		SourceAdapter: "github",
		SourceIssueID: "9304",
	}

	if err := r.postTitleRejectionEscalation(context.Background(), task); err != nil {
		t.Fatalf("postTitleRejectionEscalation: %v", err)
	}

	if len(processor.events) != 1 {
		t.Fatalf("expected the alert to still fire despite the label call failing, got %d events: %+v", len(processor.events), processor.events)
	}
	if processor.events[0].TaskID != task.ID {
		t.Errorf("event.TaskID = %q, want %q", processor.events[0].TaskID, task.ID)
	}
}
