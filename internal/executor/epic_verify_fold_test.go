package executor

import (
	"strings"
	"testing"
)

func TestFoldVerifyOnlySubtasks(t *testing.T) {
	t.Run("N-subtask plan with one verify-only entry collapses to N-1", func(t *testing.T) {
		subtasks := []PlannedSubtask{
			{
				Order:       1,
				Title:       "feat(executor): add dependency detector",
				Description: "Implement detectChildDependency in internal/executor/dependency_detector.go.",
			},
			{
				Order:       2,
				Title:       "feat(executor): wire merge-wait callback",
				Description: "Add the wait_for_merge wiring in internal/executor/epic.go.",
			},
			{
				Order:       3,
				Title:       "test(executor): verify merge-wait wiring",
				Description: "Verify the merge-wait wiring in internal/executor/epic.go behaves correctly. Confirm zero regressions.",
			},
		}

		got := foldVerifyOnlySubtasks(subtasks)

		if len(got) != len(subtasks)-1 {
			t.Fatalf("expected %d subtasks after folding, got %d", len(subtasks)-1, len(got))
		}
		if got[len(got)-1].Order != 2 {
			t.Fatalf("expected the verify-only entry to fold into its immediate predecessor (order 2), got order %d", got[len(got)-1].Order)
		}
		if !strings.Contains(got[len(got)-1].Description, "## Acceptance Criteria (folded)") {
			t.Fatalf("expected folded AC section in predecessor description, got: %s", got[len(got)-1].Description)
		}
		if !strings.Contains(got[len(got)-1].Description, "Confirm zero regressions") {
			t.Fatalf("expected verify-only subtask's criteria merged into predecessor, got: %s", got[len(got)-1].Description)
		}
	})

	t.Run("verify-only entry with checkbox ACs merges each into predecessor's AC list", func(t *testing.T) {
		subtasks := []PlannedSubtask{
			{
				Order:       1,
				Title:       "feat(memory): add store method",
				Description: "Implement Store.Get in internal/memory/store.go.",
			},
			{
				Order:       2,
				Title:       "test(memory): verify store method",
				Description: "Verify Store.Get in internal/memory/store.go.\n\n- [ ] returns nil on missing key\n- [ ] returns value on hit",
			},
		}

		got := foldVerifyOnlySubtasks(subtasks)

		if len(got) != 1 {
			t.Fatalf("expected 1 subtask after folding, got %d", len(got))
		}
		desc := got[0].Description
		if !strings.Contains(desc, "returns nil on missing key") || !strings.Contains(desc, "returns value on hit") {
			t.Fatalf("expected both checkbox ACs folded into predecessor, got: %s", desc)
		}
	})

	t.Run("preserves ordering and only folds into the immediate predecessor", func(t *testing.T) {
		subtasks := []PlannedSubtask{
			{Order: 1, Title: "feat(a): implement a", Description: "Implement internal/a/a.go."},
			{Order: 2, Title: "test(a): verify a", Description: "Verify internal/a/a.go works."},
			{Order: 3, Title: "feat(b): implement b", Description: "Implement internal/b/b.go."},
		}

		got := foldVerifyOnlySubtasks(subtasks)

		if len(got) != 2 {
			t.Fatalf("expected 2 subtasks after folding, got %d", len(got))
		}
		if got[0].Order != 1 || got[1].Order != 3 {
			t.Fatalf("expected order [1,3], got [%d,%d]", got[0].Order, got[1].Order)
		}
	})

	t.Run("verify-only subtask that introduces a new file is not folded", func(t *testing.T) {
		subtasks := []PlannedSubtask{
			{Order: 1, Title: "feat(a): implement a", Description: "Implement internal/a/a.go."},
			{Order: 2, Title: "test(b): verify b", Description: "Verify the new behavior in internal/b/b.go."},
		}

		got := foldVerifyOnlySubtasks(subtasks)

		if len(got) != 2 {
			t.Fatalf("expected verify-only subtask referencing a new file to survive folding, got %d subtasks", len(got))
		}
	})

	t.Run("subtask with implementation language is not folded despite verify wording", func(t *testing.T) {
		subtasks := []PlannedSubtask{
			{Order: 1, Title: "feat(a): implement a", Description: "Implement internal/a/a.go."},
			{Order: 2, Title: "fix(a): verify and fix edge case", Description: "Verify internal/a/a.go and fix the edge case found."},
		}

		got := foldVerifyOnlySubtasks(subtasks)

		if len(got) != 2 {
			t.Fatalf("expected subtask with implementation language to survive folding, got %d subtasks", len(got))
		}
	})

	t.Run("single subtask plan is unchanged", func(t *testing.T) {
		subtasks := []PlannedSubtask{
			{Order: 1, Title: "feat(a): implement a", Description: "Implement internal/a/a.go."},
		}

		got := foldVerifyOnlySubtasks(subtasks)

		if len(got) != 1 {
			t.Fatalf("expected single-subtask plan unchanged, got %d subtasks", len(got))
		}
	})
}

func TestIsVerifyOnlySubtask(t *testing.T) {
	prev := PlannedSubtask{
		Title:       "feat(executor): add detector",
		Description: "Implement detectChildDependency in internal/executor/dependency_detector.go.",
	}

	tests := []struct {
		name string
		st   PlannedSubtask
		want bool
	}{
		{
			name: "pure verification of predecessor's file",
			st: PlannedSubtask{
				Title:       "test(executor): verify detector",
				Description: "Verify detectChildDependency in internal/executor/dependency_detector.go behaves correctly.",
			},
			want: true,
		},
		{
			name: "verification with no implementation surface at all",
			st: PlannedSubtask{
				Title:       "test(executor): run the acceptance suite",
				Description: "Run the acceptance criteria for the previous subtask end to end.",
			},
			want: true,
		},
		{
			name: "introduces a new file beyond predecessor's targets",
			st: PlannedSubtask{
				Title:       "test(executor): verify new file",
				Description: "Verify internal/executor/other_file.go behaves correctly.",
			},
			want: false,
		},
		{
			name: "implementation language overrides verification wording",
			st: PlannedSubtask{
				Title:       "fix(executor): verify and fix bug",
				Description: "Verify detectChildDependency in internal/executor/dependency_detector.go and fix the bug found.",
			},
			want: false,
		},
		{
			name: "no verification wording at all",
			st: PlannedSubtask{
				Title:       "feat(executor): add another function",
				Description: "Add anotherFunc to internal/executor/dependency_detector.go.",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isVerifyOnlySubtask(tt.st, prev); got != tt.want {
				t.Errorf("isVerifyOnlySubtask() = %v, want %v", got, tt.want)
			}
		})
	}
}
