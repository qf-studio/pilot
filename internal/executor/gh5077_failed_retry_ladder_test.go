package executor

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
)

// GH-5077: the pilot-failed-retry-N ladder (adapters/github/types.go:159-169,
// FailedRetryStateLabels) must advance none -> pilot-failed-retry-1 ->
// pilot-failed-retry-2 -> pilot-failed-retry-exhausted every time
// postTitleRejectionEscalation (title_rejection.go) stamps pilot-failed onto
// an issue for the first time, must not advance again on a duplicate
// fail-label event for an issue that already carries pilot-failed, and must
// not advance past the terminal exhausted rung.

// TestNextFailedRetryLabel_RungTransitions is the pure-function unit test for
// the ladder-advancement decision: no GitHub/gh-CLI involved.
func TestNextFailedRetryLabel_RungTransitions(t *testing.T) {
	tests := []struct {
		name       string
		labels     []string
		wantAdd    string
		wantRemove string
	}{
		{
			name:       "no ladder label yet -> retry-1",
			labels:     nil,
			wantAdd:    labelPilotFailedRetry1,
			wantRemove: "",
		},
		{
			name:       "unrelated labels only -> retry-1",
			labels:     []string{"bug", "pilot"},
			wantAdd:    labelPilotFailedRetry1,
			wantRemove: "",
		},
		{
			name:       "retry-1 present -> retry-2, removes retry-1",
			labels:     []string{labelPilotFailedRetry1},
			wantAdd:    labelPilotFailedRetry2,
			wantRemove: labelPilotFailedRetry1,
		},
		{
			name:       "retry-2 present -> exhausted, removes retry-2",
			labels:     []string{labelPilotFailedRetry2},
			wantAdd:    labelPilotFailedRetryExhausted,
			wantRemove: labelPilotFailedRetry2,
		},
		{
			name:       "exhausted present -> no further advancement",
			labels:     []string{labelPilotFailedRetryExhausted},
			wantAdd:    "",
			wantRemove: "",
		},
		{
			name:       "case-insensitive match on existing rung",
			labels:     []string{"PILOT-FAILED-RETRY-1"},
			wantAdd:    labelPilotFailedRetry2,
			wantRemove: labelPilotFailedRetry1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAdd, gotRemove := nextFailedRetryLabel(tt.labels)
			if gotAdd != tt.wantAdd || gotRemove != tt.wantRemove {
				t.Errorf("nextFailedRetryLabel(%v) = (%q, %q), want (%q, %q)",
					tt.labels, gotAdd, gotRemove, tt.wantAdd, tt.wantRemove)
			}
		})
	}
}

// runPostTitleRejectionEscalation drives postTitleRejectionEscalation with a
// stubbed fetchIssueState (existingLabels) and a fake gh CLI, returning every
// gh invocation's argv (one line per call) for assertion.
func runPostTitleRejectionEscalation(t *testing.T, issueNum int, existingLabels []string) string {
	t.Helper()
	logFile := setupFakeGhCLI(t)

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false, Labels: existingLabels}, nil
	})

	r := newSilentRunnerTask359()
	task := &Task{
		ID:            "unused",
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
	return string(logBytes)
}

// TestPostTitleRejectionEscalation_FreshFailure_StampsFirstRung verifies that
// a fresh pilot-failed application (issue not yet labeled pilot-failed, no
// prior ladder rung) applies pilot-failed-retry-1 in the same `gh issue edit`
// call that applies pilot-failed.
func TestPostTitleRejectionEscalation_FreshFailure_StampsFirstRung(t *testing.T) {
	calls := runPostTitleRejectionEscalation(t, 9201, nil)

	if !strings.Contains(calls, "--add-label pilot-failed") {
		t.Errorf("expected pilot-failed to be added, got calls:\n%s", calls)
	}
	if !strings.Contains(calls, "--add-label "+labelPilotFailedRetry1) {
		t.Errorf("expected %s to be added, got calls:\n%s", labelPilotFailedRetry1, calls)
	}
	if strings.Contains(calls, "--remove-label "+labelPilotFailedRetry1) ||
		strings.Contains(calls, "--remove-label "+labelPilotFailedRetry2) ||
		strings.Contains(calls, "--remove-label "+labelPilotFailedRetryExhausted) {
		t.Errorf("expected no ladder label removal on first rung, got calls:\n%s", calls)
	}

	// Single mutation: exactly one `gh issue edit` invocation carries both the
	// pilot-failed label and the ladder rung — not two separate calls. Match
	// on a line *starting with* "issue edit" (the fake gh CLI's echoed argv
	// for a real invocation) rather than merely containing it, since the
	// posted comment's body itself quotes a "gh issue edit ..." snippet as
	// re-dispatch instructions and would otherwise be misidentified as a
	// second call.
	editLines := grepLinesStartingWith(calls, "issue edit")
	if len(editLines) != 1 {
		t.Fatalf("expected exactly 1 `gh issue edit` call, got %d:\n%s", len(editLines), calls)
	}
	if !strings.Contains(editLines[0], "--add-label pilot-failed") || !strings.Contains(editLines[0], "--add-label "+labelPilotFailedRetry1) {
		t.Errorf("expected pilot-failed and %s in the same issue edit call, got:\n%s", labelPilotFailedRetry1, editLines[0])
	}
}

// TestPostTitleRejectionEscalation_RungTransitions covers retry-1 -> retry-2
// and retry-2 -> exhausted through the full escalation path (not just the
// pure function), confirming the previous rung label is removed in the same
// call the next rung is added.
func TestPostTitleRejectionEscalation_RungTransitions(t *testing.T) {
	t.Run("retry-1 -> retry-2", func(t *testing.T) {
		calls := runPostTitleRejectionEscalation(t, 9202, []string{labelPilotFailedRetry1})

		if !strings.Contains(calls, "--add-label "+labelPilotFailedRetry2) {
			t.Errorf("expected %s to be added, got calls:\n%s", labelPilotFailedRetry2, calls)
		}
		if !strings.Contains(calls, "--remove-label "+labelPilotFailedRetry1) {
			t.Errorf("expected %s to be removed, got calls:\n%s", labelPilotFailedRetry1, calls)
		}
	})

	t.Run("retry-2 -> exhausted", func(t *testing.T) {
		calls := runPostTitleRejectionEscalation(t, 9203, []string{labelPilotFailedRetry2})

		if !strings.Contains(calls, "--add-label "+labelPilotFailedRetryExhausted) {
			t.Errorf("expected %s to be added, got calls:\n%s", labelPilotFailedRetryExhausted, calls)
		}
		if !strings.Contains(calls, "--remove-label "+labelPilotFailedRetry2) {
			t.Errorf("expected %s to be removed, got calls:\n%s", labelPilotFailedRetry2, calls)
		}
	})
}

// TestPostTitleRejectionEscalation_ExhaustedTerminal_NoFurtherAdvancement
// verifies that once the ladder has reached pilot-failed-retry-exhausted, a
// fresh pilot-failed application does not add or remove any ladder label —
// exhausted is terminal.
func TestPostTitleRejectionEscalation_ExhaustedTerminal_NoFurtherAdvancement(t *testing.T) {
	calls := runPostTitleRejectionEscalation(t, 9204, []string{labelPilotFailedRetryExhausted})

	if !strings.Contains(calls, "--add-label pilot-failed") {
		t.Errorf("expected pilot-failed to still be added, got calls:\n%s", calls)
	}
	for _, l := range []string{labelPilotFailedRetry1, labelPilotFailedRetry2, labelPilotFailedRetryExhausted} {
		if strings.Contains(calls, "--add-label "+l) {
			t.Errorf("expected no further --add-label %s past exhaustion, got calls:\n%s", l, calls)
		}
		if strings.Contains(calls, "--remove-label "+l) {
			t.Errorf("expected no --remove-label %s past exhaustion, got calls:\n%s", l, calls)
		}
	}
}

// TestPostTitleRejectionEscalation_DuplicateFailEvent_DoesNotAdvanceLadder is
// the idempotence guard: an issue that already carries pilot-failed (a repeat
// escalation for the same still-broken title, e.g. the 3rd/4th consecutive
// rejection) must not advance the ladder again, even though a ladder rung is
// already present.
func TestPostTitleRejectionEscalation_DuplicateFailEvent_DoesNotAdvanceLadder(t *testing.T) {
	calls := runPostTitleRejectionEscalation(t, 9205, []string{labelPilotFailed, labelPilotFailedRetry1})

	if strings.Contains(calls, "--add-label "+labelPilotFailedRetry2) {
		t.Errorf("duplicate fail-label event must not advance the ladder, got calls:\n%s", calls)
	}
	if strings.Contains(calls, "--remove-label "+labelPilotFailedRetry1) {
		t.Errorf("duplicate fail-label event must not remove the current rung, got calls:\n%s", calls)
	}
	// pilot-failed re-application itself is still fine (idempotent add).
	if !strings.Contains(calls, "--add-label pilot-failed") {
		t.Errorf("expected pilot-failed re-applied (idempotent), got calls:\n%s", calls)
	}
}

// grepLinesStartingWith returns every line of calls that begins with prefix.
func grepLinesStartingWith(calls, prefix string) []string {
	var out []string
	for _, line := range strings.Split(calls, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			out = append(out, line)
		}
	}
	return out
}
