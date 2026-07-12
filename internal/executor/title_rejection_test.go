package executor

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestTitleRejectionTracker_SameTitleIncrements(t *testing.T) {
	tr := newTitleRejectionTracker()
	if got := tr.record("GH-1", "bad title"); got != 1 {
		t.Fatalf("first record = %d, want 1", got)
	}
	if got := tr.record("GH-1", "bad title"); got != 2 {
		t.Fatalf("second record = %d, want 2", got)
	}
	if got := tr.record("GH-1", "bad title"); got != 3 {
		t.Fatalf("third record = %d, want 3", got)
	}
}

func TestTitleRejectionTracker_TitleChangeResets(t *testing.T) {
	tr := newTitleRejectionTracker()
	tr.record("GH-1", "bad title one")
	tr.record("GH-1", "bad title one")
	if got := tr.record("GH-1", "bad title two"); got != 1 {
		t.Fatalf("title change record = %d, want 1 (reset)", got)
	}
}

func TestTitleRejectionTracker_WhitespaceInsensitive(t *testing.T) {
	tr := newTitleRejectionTracker()
	tr.record("GH-1", "bad title")
	if got := tr.record("GH-1", "  bad title  "); got != 2 {
		t.Fatalf("whitespace-only diff record = %d, want 2", got)
	}
}

func TestTitleRejectionTracker_Clear(t *testing.T) {
	tr := newTitleRejectionTracker()
	tr.record("GH-1", "bad title")
	tr.clear("GH-1")
	if got := tr.record("GH-1", "bad title"); got != 1 {
		t.Fatalf("after clear, record = %d, want 1", got)
	}
}

func TestTitleRejectionTracker_PerIssueIsolation(t *testing.T) {
	tr := newTitleRejectionTracker()
	tr.record("GH-1", "title a")
	tr.record("GH-1", "title a")
	if got := tr.record("GH-2", "title a"); got != 1 {
		t.Fatalf("cross-issue record = %d, want 1", got)
	}
}

func TestSuggestConventionalTitle(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		labels  []string
		wantPfx string // prefix we expect the suggestion to start with
	}{
		{"fix verb", "Fix null pointer in handler", nil, "fix"},
		{"add verb", "Add rate limiting", nil, "feat(repo): "},
		{"migrate verb", "Migrate alekspetrov/pilot references", nil, "chore(repo): "},
		{"label bug", "improve validation", []string{"bug"}, "fix: "},
		{"label enhancement", "retries", []string{"enhancement"}, "feat: "},
		{"unknown verb falls back to chore", "xyzzy the thing", nil, "chore(repo): "},
		{"empty title", "", nil, "chore"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := suggestConventionalTitle(tt.title, tt.labels)
			if !strings.HasPrefix(got, tt.wantPfx) {
				t.Fatalf("suggest(%q,%v) = %q, want prefix %q", tt.title, tt.labels, got, tt.wantPfx)
			}
			// The suggestion itself should satisfy the conventional-commit regex
			// (otherwise we'd loop users back to the same error).
			if tt.title != "" {
				if err := validatePRTitle(got); err != nil {
					t.Fatalf("suggestion %q does not pass validation: %v", got, err)
				}
			}
		})
	}
}

func TestBuildTitleRejectionComment_ContainsKeyElements(t *testing.T) {
	comment := buildTitleRejectionComment(2175, "Migrate all alekspetrov/pilot references to qf-studio/pilot", nil)

	musts := []string{
		"Pilot can't open a PR",
		"Current title",
		"Migrate all alekspetrov/pilot references to qf-studio/pilot",
		"Suggested rewrite",
		"gh issue edit 2175",
		"--remove-label pilot-failed",
		"--remove-label pilot-title-rejected",
		"--add-label pilot-retry-ready",
		"conventionalcommits.org",
	}
	for _, m := range musts {
		if !strings.Contains(comment, m) {
			t.Errorf("comment missing %q\n---\n%s", m, comment)
		}
	}
}

func TestHashTitle_StableAndTrimmed(t *testing.T) {
	if hashTitle("hello") != hashTitle("  hello  ") {
		t.Error("hashTitle should trim whitespace")
	}
	if hashTitle("hello") == hashTitle("Hello") {
		t.Error("hashTitle should be case-sensitive")
	}
}

// TestRecordTitleRejection_EscalatesOnSecondFailure is the GH-4220 (e)
// regression guard for the shared helper: the first titleErr for a given
// task/title records but does not escalate, and the second (matching)
// titleErr escalates and sets result.TitleRejected. This is the exact
// record→escalate contract the direct path already had (GH-2363) — the
// point of extracting it into title_rejection.go was so finalizeEpicBranchPR
// and finalizeDecomposedParentPR could share it instead of retrying forever.
func TestRecordTitleRejection_EscalatesOnSecondFailure(t *testing.T) {
	fakeBin := t.TempDir()
	if err := os.WriteFile(fakeBin+"/gh", []byte("#!/bin/sh\necho '[]'\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	r := newSilentRunnerTask359()
	r.titleRejections = newTitleRejectionTracker()
	task := &Task{ID: "GH-4220", Title: "bad title", SourceAdapter: "github"}

	result := &ExecutionResult{TaskID: task.ID}
	r.recordTitleRejection(context.Background(), task, result)
	if result.TitleRejected {
		t.Error("first rejection must not escalate yet")
	}

	result2 := &ExecutionResult{TaskID: task.ID}
	r.recordTitleRejection(context.Background(), task, result2)
	if !result2.TitleRejected {
		t.Error("second consecutive rejection of the same title must escalate")
	}
}

// TestRecordTitleRejection_NilTrackerIsNoOp guards the nil-tracker short
// circuit: Runners built without a titleRejections tracker (e.g. most unit
// test fixtures) must not panic when a finalize path hits titleErr.
func TestRecordTitleRejection_NilTrackerIsNoOp(t *testing.T) {
	r := newSilentRunnerTask359() // titleRejections left nil
	task := &Task{ID: "GH-1", Title: "bad title"}
	result := &ExecutionResult{TaskID: task.ID}

	r.recordTitleRejection(context.Background(), task, result)

	if result.TitleRejected {
		t.Error("nil tracker must never escalate")
	}
}

// TestClearTitleRejectionState_NilTrackerIsNoOp mirrors the nil-tracker guard
// for the success-path clear helper.
func TestClearTitleRejectionState_NilTrackerIsNoOp(t *testing.T) {
	r := newSilentRunnerTask359() // titleRejections left nil
	r.clearTitleRejectionState(&Task{ID: "GH-1"}) // must not panic
}

// TestClearTitleRejectionState_DropsTrackedCount verifies the success-path
// helper actually resets the counter (mirrors TestTitleRejectionTracker_Clear
// but through the Runner-level wrapper finalize paths call).
func TestClearTitleRejectionState_DropsTrackedCount(t *testing.T) {
	r := newSilentRunnerTask359()
	r.titleRejections = newTitleRejectionTracker()
	task := &Task{ID: "GH-1", Title: "bad title"}

	r.titleRejections.record(task.ID, task.Title)
	r.clearTitleRejectionState(task)

	if got := r.titleRejections.record(task.ID, task.Title); got != 1 {
		t.Errorf("after clearTitleRejectionState, record = %d, want 1 (reset)", got)
	}
}
