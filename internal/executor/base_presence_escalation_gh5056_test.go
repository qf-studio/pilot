package executor

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH-5056 (PR#5054 review residuals 2 & 3): escalateBasePresenceHold must
// shed pilot-retry-ready in the same mutation that applies
// pilot-needs-human (mirroring autopilot's escalateAndHold, controller.go,
// and the GH-5042/PR#5048 never-coexist invariant), and must emit an
// alerts-engine event so operator visibility doesn't rest entirely on
// label-watching.

// TestEscalateBasePresenceHold_ShedsRetryReadyInSameMutation covers defect 2:
// the escalation `gh issue edit` call must add pilot-needs-human AND remove
// pilot-retry-ready in one mutation, not just apply the needs-human label.
func TestEscalateBasePresenceHold_ShedsRetryReadyInSameMutation(t *testing.T) {
	logFile := setupFakeGhCLI(t)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	worker := NewProjectWorker(t.TempDir(), store, runner, slog.Default())

	task := &Task{
		ID:            "GH-9500",
		Title:         "escalation retry-ready shed",
		ProjectPath:   t.TempDir(),
		SourceAdapter: "github",
	}

	worker.escalateBasePresenceHold(context.Background(), task, "referenced PR #1 is still open (not merged)")

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read gh CLI log: %v", err)
	}
	log := string(data)
	if got := strings.Count(log, "issue edit 9500"); got != 1 {
		t.Fatalf("expected exactly one `gh issue edit 9500` call, got %d (log: %q)", got, log)
	}
	if !strings.Contains(log, "--add-label pilot-needs-human") {
		t.Errorf("expected the escalation call to add pilot-needs-human, got log: %q", log)
	}
	if !strings.Contains(log, "--remove-label pilot-retry-ready") {
		t.Errorf("expected the escalation call to remove pilot-retry-ready in the same mutation (GH-5042/PR#5048 never-coexist invariant), got log: %q", log)
	}
	// Both label mutations must ride the single `gh issue edit` call this
	// test already asserted fires exactly once above — a second, separate
	// invocation would defeat the "same mutation" guarantee even if both
	// substrings were individually present in the log.
	editLine := ""
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if strings.HasPrefix(line, "issue edit 9500") {
			editLine = line
			break
		}
	}
	if !strings.Contains(editLine, "--add-label pilot-needs-human") || !strings.Contains(editLine, "--remove-label pilot-retry-ready") {
		t.Errorf("expected a single `gh issue edit 9500` call carrying both label mutations, got line: %q", editLine)
	}
}

// TestEscalateBasePresenceHold_EmitsAlertsEngineEvent covers defect 3: the
// escalation must fire an alerts-engine event (mirroring escalateAndHold's
// AlertEventTypeTaskFailed emission), not just label + log + EmitProgress.
func TestEscalateBasePresenceHold_EmitsAlertsEngineEvent(t *testing.T) {
	setupFakeGhCLI(t)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)
	worker := NewProjectWorker(t.TempDir(), store, runner, slog.Default())

	task := &Task{
		ID:            "GH-9501",
		Title:         "escalation alert coverage",
		ProjectPath:   t.TempDir(),
		SourceAdapter: "github",
	}
	reason := "referenced PR #2 is still open (not merged)"

	worker.escalateBasePresenceHold(context.Background(), task, reason)

	if len(processor.events) != 1 {
		t.Fatalf("expected exactly 1 alert event, got %d: %+v", len(processor.events), processor.events)
	}
	event := processor.events[0]
	if event.Type != AlertEventTypeTaskFailed {
		t.Errorf("event.Type = %q, want %q", event.Type, AlertEventTypeTaskFailed)
	}
	if event.TaskID != task.ID {
		t.Errorf("event.TaskID = %q, want %q", event.TaskID, task.ID)
	}
	if event.Project != task.ProjectPath {
		t.Errorf("event.Project = %q, want %q", event.Project, task.ProjectPath)
	}
	if event.Error != reason {
		t.Errorf("event.Error = %q, want %q", event.Error, reason)
	}
}

// TestEscalateBasePresenceHold_AlertFiresEvenWhenLabelCallFails documents
// that the alert is not gated on the (best-effort, non-fatal per this
// function's own doc comment) label mutation succeeding — a labeling
// failure must not also silently swallow operator visibility into the
// escalation itself.
func TestEscalateBasePresenceHold_AlertFiresEvenWhenLabelCallFails(t *testing.T) {
	setupFailingFakeGhCLI(t)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)
	worker := NewProjectWorker(t.TempDir(), store, runner, slog.Default())

	task := &Task{
		ID:            "GH-9502",
		Title:         "escalation alert survives label failure",
		ProjectPath:   t.TempDir(),
		SourceAdapter: "github",
	}
	reason := "referenced PR #3 is still open (not merged)"

	worker.escalateBasePresenceHold(context.Background(), task, reason)

	if len(processor.events) != 1 {
		t.Fatalf("expected the alert to still fire despite the label call failing, got %d events: %+v", len(processor.events), processor.events)
	}
	if processor.events[0].TaskID != task.ID {
		t.Errorf("event.TaskID = %q, want %q", processor.events[0].TaskID, task.ID)
	}
}

// setupFailingFakeGhCLI installs a fake `gh` binary on PATH that exits 1 for
// the duration of the test — mirrors setupFakeGhCLI (gh4817_issue_open_state_test.go)
// but simulates every `gh` invocation failing (e.g. a transient API error).
func setupFailingFakeGhCLI(t *testing.T) {
	t.Helper()
	fakeBin := t.TempDir()
	script := filepath.Join(fakeBin, "gh")
	content := "#!/bin/sh\necho \"simulated gh failure\" >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write failing fake gh: %v", err)
	}
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+origPATH)
}
