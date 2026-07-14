package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// gh4300TwoSubtasks returns two subtasks used across the GH-4300 coverage-gap
// tests. Distinct directories keep isSinglePackageScope from consolidating
// them into a single task for the r.Execute-driven recovery test.
func gh4300TwoSubtasks() []PlannedSubtask {
	return []PlannedSubtask{
		{Order: 1, Title: "feat(gateway): add websocket handler", Description: "Implement upgrade handler in internal/gateway/server.go"},
		{Order: 2, Title: "feat(adapters): add telegram bot", Description: "Wire bot client in internal/adapters/telegram/bot.go"},
	}
}

// TestIsTransientSubIssueCreateError covers the classifier that decides
// whether a `gh issue create` failure is worth retrying (GH-4300). The TLS
// handshake timeout case is the exact signature from the 2026-07-14
// pilot-console#1 incident.
func TestIsTransientSubIssueCreateError(t *testing.T) {
	tests := []struct {
		name    string
		errText string
		want    bool
	}{
		{"TLS handshake timeout (incident signature)", `Post "https://api.github.com/graphql": net/http: TLS handshake timeout`, true},
		{"generic handshake timeout", "remote error: handshake timeout", true},
		{"connection reset", "read: connection reset by peer", true},
		{"connection refused", "dial tcp: connection refused", true},
		{"i/o timeout", "read tcp: i/o timeout", true},
		{"context deadline exceeded", "context deadline exceeded", true},
		{"dial tcp failure", "dial tcp 140.82.0.0:443: i/o timeout", true},
		{"no such host", "dial tcp: lookup api.github.com: no such host", true},
		{"network unreachable", "connect: network is unreachable", true},
		{"http 502", "HTTP 502: Bad Gateway", true},
		{"status 503", "gh: status 503", true},
		{"secondary rate limit", "You have exceeded a secondary rate limit", true},
		{"api rate limit exceeded", "API rate limit exceeded for user", true},
		{"case-insensitive", "NET/HTTP: TLS HANDSHAKE TIMEOUT", true},
		{"auth failure — not transient", "HTTP 401: Bad credentials", false},
		{"not found — not transient", "HTTP 404: Not Found", false},
		{"validation failed — not transient", "HTTP 422: Validation Failed", false},
		{"empty string", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientSubIssueCreateError(tt.errText); got != tt.want {
				t.Errorf("isTransientSubIssueCreateError(%q) = %v, want %v", tt.errText, got, tt.want)
			}
		})
	}
}

// TestCreateSubIssues_HappyPathUnchanged asserts the no-failure case still
// creates every planned issue in one pass with no retries and no coverage
// gap — GH-4300 acceptance: "No behavior change on the happy path."
func TestCreateSubIssues_HappyPathUnchanged(t *testing.T) {
	fakeBin := t.TempDir()
	countFile := filepath.Join(t.TempDir(), "count")
	script := filepath.Join(fakeBin, "gh")
	content := "#!/bin/sh\n" +
		`if [ "$1" = "issue" ] && [ "$2" = "create" ]; then
  COUNT_FILE="` + countFile + `"
  if [ -f "$COUNT_FILE" ]; then N=$(cat "$COUNT_FILE"); else N=0; fi
  N=$((N+1))
  echo $N > "$COUNT_FILE"
  echo "https://github.com/owner/repo/issues/$((200+N))"
fi
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+origPATH)

	runner := NewRunner()
	plan := &EpicPlan{
		ParentTask: &Task{ID: "GH-4300H"},
		Subtasks:   gh4300TwoSubtasks(),
	}

	created, err := runner.CreateSubIssues(context.Background(), plan, t.TempDir())
	if err != nil {
		t.Fatalf("CreateSubIssues returned error, want nil: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %d, want 2", len(created))
	}

	data, _ := os.ReadFile(countFile)
	if strings.TrimSpace(string(data)) != "2" {
		t.Errorf("gh issue create invocation count = %s, want 2 (one per subtask, no retries)", data)
	}
}

// TestCreateSubIssues_RetriesTransientCreateFailureThenSucceeds is the
// GH-4300 acceptance case: a transient failure on the Kth sub-issue
// creation retries and, once it succeeds, all N issues exist and
// CreateSubIssues returns no error — reproducing the exact TLS handshake
// timeout signature from the 2026-07-14 pilot-console#1 incident, except
// this time the retry recovers instead of silently dropping the subtask.
func TestCreateSubIssues_RetriesTransientCreateFailureThenSucceeds(t *testing.T) {
	fakeBin := t.TempDir()
	countFile := filepath.Join(t.TempDir(), "count")
	script := filepath.Join(fakeBin, "gh")
	// Invocation 1 (subtask 1): succeeds immediately.
	// Invocation 2 (subtask 2, attempt 1): transient TLS handshake timeout.
	// Invocation 3 (subtask 2, attempt 2 / retry): succeeds.
	content := "#!/bin/sh\n" +
		`if [ "$1" = "issue" ] && [ "$2" = "create" ]; then
  COUNT_FILE="` + countFile + `"
  if [ -f "$COUNT_FILE" ]; then N=$(cat "$COUNT_FILE"); else N=0; fi
  N=$((N+1))
  echo $N > "$COUNT_FILE"
  if [ "$N" = "2" ]; then
    echo 'Post "https://api.github.com/graphql": net/http: TLS handshake timeout' >&2
    exit 1
  fi
  echo "https://github.com/owner/repo/issues/$((200+N))"
fi
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+origPATH)

	runner := NewRunner()
	runner.subIssueCreateRetryDelay = time.Millisecond // keep the test fast

	plan := &EpicPlan{
		ParentTask: &Task{ID: "GH-4300B"},
		Subtasks:   gh4300TwoSubtasks(),
	}

	created, err := runner.CreateSubIssues(context.Background(), plan, t.TempDir())
	if err != nil {
		t.Fatalf("CreateSubIssues returned error, want nil after retry recovers: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %d, want 2 (both subtasks, including the one that needed a retry)", len(created))
	}

	data, _ := os.ReadFile(countFile)
	if strings.TrimSpace(string(data)) != "3" {
		t.Errorf("gh issue create invocation count = %s, want 3 (subtask1 success + subtask2 fail + subtask2 retry success)", data)
	}
}

// TestCreateSubIssues_PermanentFailureLeavesParentFlaggedNotClosed is the
// GH-4300 acceptance case: when sub-issue creation exhausts its retries, the
// parent must not be treated as done. CreateSubIssues must return a
// *SubIssueCoverageGapError naming the uncreated subtask, label the parent
// pilot-needs-clarification, comment the gap, and record a ledger event
// with planned/created counts.
func TestCreateSubIssues_PermanentFailureLeavesParentFlaggedNotClosed(t *testing.T) {
	fakeBin := t.TempDir()
	countFile := filepath.Join(t.TempDir(), "count")
	logFile := filepath.Join(t.TempDir(), "gh-calls.log")
	script := filepath.Join(fakeBin, "gh")
	// Invocation 1 (subtask 1): succeeds. Every "issue create" invocation
	// after that (subtask 2, every retry attempt) fails with a transient
	// signature that never clears — simulating an outage that outlasts the
	// bounded retry budget. Non-create subcommands (issue edit/comment, used
	// by the coverage-gap label/comment) always succeed.
	content := "#!/bin/sh\n" +
		`echo "$@" >> "` + logFile + `"
if [ "$1" = "issue" ] && [ "$2" = "create" ]; then
  COUNT_FILE="` + countFile + `"
  if [ -f "$COUNT_FILE" ]; then N=$(cat "$COUNT_FILE"); else N=0; fi
  N=$((N+1))
  echo $N > "$COUNT_FILE"
  if [ "$N" = "1" ]; then
    echo "https://github.com/owner/repo/issues/201"
  else
    echo 'Post "https://api.github.com/graphql": net/http: TLS handshake timeout' >&2
    exit 1
  fi
fi
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+origPATH)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	runner.SetLogStore(store)
	runner.subIssueCreateRetryAttempts = 2 // bound the outage-outlasts-retries loop
	runner.subIssueCreateRetryDelay = time.Millisecond

	parent := &Task{ID: "GH-4300P"}
	if err := store.SaveExecution(&memory.Execution{ID: parent.LogExecutionID(), TaskID: parent.ID, Status: "running"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	plan := &EpicPlan{ParentTask: parent, Subtasks: gh4300TwoSubtasks()}

	created, err := runner.CreateSubIssues(context.Background(), plan, t.TempDir())
	if err == nil {
		t.Fatal("CreateSubIssues returned nil error, want a coverage-gap error")
	}
	if len(created) != 1 {
		t.Fatalf("created = %d, want 1 (only the subtask that succeeded)", len(created))
	}

	var gapErr *SubIssueCoverageGapError
	if !errors.As(err, &gapErr) {
		t.Fatalf("error is not *SubIssueCoverageGapError: %v", err)
	}
	if gapErr.Planned != 2 {
		t.Errorf("gapErr.Planned = %d, want 2", gapErr.Planned)
	}
	if gapErr.Created != 1 {
		t.Errorf("gapErr.Created = %d, want 1", gapErr.Created)
	}
	if len(gapErr.Missing) != 1 || gapErr.Missing[0] != "feat(adapters): add telegram bot" {
		t.Errorf("gapErr.Missing = %v, want the second subtask's title", gapErr.Missing)
	}

	// Parent must be labeled pilot-needs-clarification and commented — not closed.
	logData, _ := os.ReadFile(logFile)
	logStr := string(logData)
	if !strings.Contains(logStr, "issue edit") || !strings.Contains(logStr, "pilot-needs-clarification") {
		t.Errorf("expected a `gh issue edit ... --add-label pilot-needs-clarification` call, log:\n%s", logStr)
	}
	if !strings.Contains(logStr, "issue comment") {
		t.Errorf("expected a `gh issue comment` call naming the gap, log:\n%s", logStr)
	}
	if strings.Contains(logStr, "issue close") {
		t.Errorf("parent must NOT be closed on a coverage gap, log:\n%s", logStr)
	}

	// Ledger event: planned=2 created=1.
	events, err := store.ListExecutionEvents(parent.LogExecutionID())
	if err != nil {
		t.Fatalf("ListExecutionEvents: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Stage == memory.StageSubIssuesIncomplete {
			found = true
			if !strings.Contains(e.Detail, "planned=2 created=1") {
				t.Errorf("event detail = %q, want it to contain %q", e.Detail, "planned=2 created=1")
			}
		}
	}
	if !found {
		t.Errorf("expected a StageSubIssuesIncomplete ledger event, got stages: %+v", events)
	}
}

// TestCreateSubIssues_NonTransientErrorFailsFastWithoutRetry asserts a 4xx
// auth/validation-style failure is not retried — GH-4300's retry loop must
// only spend attempts on errors retrying can plausibly fix.
func TestCreateSubIssues_NonTransientErrorFailsFastWithoutRetry(t *testing.T) {
	fakeBin := t.TempDir()
	countFile := filepath.Join(t.TempDir(), "count")
	script := filepath.Join(fakeBin, "gh")
	content := "#!/bin/sh\n" +
		`if [ "$1" = "issue" ] && [ "$2" = "create" ]; then
  COUNT_FILE="` + countFile + `"
  if [ -f "$COUNT_FILE" ]; then N=$(cat "$COUNT_FILE"); else N=0; fi
  N=$((N+1))
  echo $N > "$COUNT_FILE"
  if [ "$N" = "1" ]; then
    echo "https://github.com/owner/repo/issues/201"
  else
    echo "HTTP 422: Validation Failed" >&2
    exit 1
  fi
fi
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+origPATH)

	runner := NewRunner()
	runner.subIssueCreateRetryAttempts = 3
	runner.subIssueCreateRetryDelay = time.Millisecond

	plan := &EpicPlan{
		ParentTask: &Task{ID: "GH-4300N"},
		Subtasks:   gh4300TwoSubtasks(),
	}

	created, err := runner.CreateSubIssues(context.Background(), plan, t.TempDir())
	if err == nil {
		t.Fatal("CreateSubIssues returned nil error, want a coverage-gap error")
	}
	if len(created) != 1 {
		t.Fatalf("created = %d, want 1", len(created))
	}

	data, _ := os.ReadFile(countFile)
	if strings.TrimSpace(string(data)) != "2" {
		t.Errorf("gh issue create invocation count = %s, want 2 (subtask1 success + subtask2's single, non-retried failure)", data)
	}
}

// TestExecute_EpicRecoveryCoverageGap_ParentStaysOpen is the GH-4300
// regression test for the actual 2026-07-14 pilot-console#1 incident
// mechanism: a prior run's `gh issue create` failed partway through, leaving
// only one of two planned subtasks with an issue. A later run's
// ErrSubIssuesAlreadyExist recovery pass finds that one issue already
// closed. Before this fix, allChildrenDone(recovered) short-circuited
// straight to "epic complete" without ever comparing against the plan's
// subtask count, so the parent closed with the second subtask never
// dispatched. This test drives the real r.Execute entry point end-to-end.
func TestExecute_EpicRecoveryCoverageGap_ParentStaysOpen(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	r := NewRunner()
	r.skipPreflightChecks = true
	r.dryRun = true // no real gh CLI needed: label/comment side effects are skipped in dry-run
	r.SetLogStore(store)
	r.openSubIssueCheck = func(_ context.Context, _, _ string) (bool, error) {
		return true, nil // force the ErrSubIssuesAlreadyExist → recovery path
	}
	r.recoverSubIssuesFn = func(_ context.Context, _, _ string) ([]CreatedIssue, error) {
		subs := gh4300TwoSubtasks()
		// Only the first subtask's issue was ever created; it's already
		// closed — the exact shape that previously fooled allChildrenDone.
		return []CreatedIssue{{Number: 201, State: "closed", Subtask: subs[0]}}, nil
	}
	r.planEpicFn = func(_ context.Context, task *Task, _ string) (*EpicPlan, error) {
		return &EpicPlan{ParentTask: task, Subtasks: gh4300TwoSubtasks()}, nil
	}

	task := &Task{ID: "GH-4300R", Title: "[epic] recovery coverage gap test"}
	if err := store.SaveExecution(&memory.Execution{ID: task.LogExecutionID(), TaskID: task.ID, Status: "running"}); err != nil {
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
		t.Error("expected Success=false — the partial recovered set must not be treated as a complete epic")
	}
	if !result.Declined {
		t.Error("expected Declined=true so callers add pilot-needs-clarification instead of pilot-failed")
	}
	if !strings.Contains(result.Error, "planned=2 created=1") {
		t.Errorf("result.Error = %q, want it to mention planned=2 created=1", result.Error)
	}

	events, err := store.ListExecutionEvents(task.LogExecutionID())
	if err != nil {
		t.Fatalf("ListExecutionEvents: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Stage == memory.StageSubIssuesIncomplete {
			found = true
			if !strings.Contains(e.Detail, "planned=2 created=1") {
				t.Errorf("event detail = %q, want it to contain %q", e.Detail, "planned=2 created=1")
			}
		}
		// The parent must never reach StageCompleted from this path.
		if e.Stage == memory.StageCompleted {
			t.Errorf("parent must not record StageCompleted on a coverage gap, got event: %+v", e)
		}
	}
	if !found {
		t.Error("expected a StageSubIssuesIncomplete ledger event")
	}
}
