package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// fakeSideEffectSearcher is a controllable GithubSideEffectSearcher test
// double — the "fake client" acceptance criterion 4(b) calls for. Mirrors
// the fakeAlertProcessor idiom (emit_alert_event_test.go): a small struct
// recording calls and returning canned results, no gh CLI or network I/O.
type fakeSideEffectSearcher struct {
	hits  []GithubSideEffectIssue
	err   error
	calls int
}

func (f *fakeSideEffectSearcher) SearchClosedOrReopenedSince(_ context.Context, _, _ string, _ time.Time) ([]GithubSideEffectIssue, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}

// TestAuditGithubSideEffects covers GH-4670 acceptance criterion 4(b): the
// post-run audit against a fake GithubSideEffectSearcher must flag a sibling
// issue closed/reopened in the run window, must NOT flag the dispatched
// issue itself (expected lifecycle activity), and must fail open — no event,
// no alert, no panic — when the search itself errors.
func TestAuditGithubSideEffects(t *testing.T) {
	tests := []struct {
		name       string
		hits       []GithubSideEffectIssue
		searchErr  error
		wantEvents int
		wantAlerts int
	}{
		{
			name: "sibling issue closed in window flags event and alert",
			hits: []GithubSideEffectIssue{
				{Number: 4649, Title: "Some sibling issue", State: "closed", URL: "https://github.com/qf-studio/pilot/issues/4649"},
			},
			wantEvents: 1,
			wantAlerts: 1,
		},
		{
			name: "own dispatched issue closure is not flagged",
			hits: []GithubSideEffectIssue{
				{Number: 4670, Title: "This very issue", State: "closed", URL: "https://github.com/qf-studio/pilot/issues/4670"},
			},
			wantEvents: 0,
			wantAlerts: 0,
		},
		{
			name:       "search error fails open with no event and no alert",
			searchErr:  errors.New("gh search issues: rate limited"),
			wantEvents: 0,
			wantAlerts: 0,
		},
		{
			name:       "no hits at all is a no-op",
			hits:       nil,
			wantEvents: 0,
			wantAlerts: 0,
		},
		{
			name: "mixed hits only flags the non-dispatched issue",
			hits: []GithubSideEffectIssue{
				{Number: 4670, Title: "This very issue", State: "closed"},
				{Number: 9001, Title: "Unrelated issue", State: "closed"},
			},
			wantEvents: 1,
			wantAlerts: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			runner := NewRunner()
			runner.SetLogStore(store)
			processor := &fakeAlertProcessor{}
			runner.SetAlertProcessor(processor)

			searcher := &fakeSideEffectSearcher{hits: tt.hits, err: tt.searchErr}
			runner.SetGithubSideEffectSearcher(searcher)

			task := &Task{
				ID:            "GH-4670",
				Title:         "Scope sessions to their own issue",
				ProjectPath:   t.TempDir(),
				SourceRepo:    "qf-studio/pilot",
				SourceIssueID: "4670",
			}
			if err := store.SaveExecution(&memory.Execution{ID: task.LogExecutionID(), TaskID: task.ID, Status: "running"}); err != nil {
				t.Fatalf("SaveExecution failed: %v", err)
			}

			runStart := time.Now().Add(-time.Minute)
			runner.auditGithubSideEffects(context.Background(), task, runStart)

			if searcher.calls != 1 {
				t.Fatalf("SearchClosedOrReopenedSince called %d times, want exactly 1", searcher.calls)
			}

			events, err := store.ListExecutionEvents(task.LogExecutionID())
			if err != nil {
				t.Fatalf("ListExecutionEvents failed: %v", err)
			}
			var sideEffectEvents int
			for _, e := range events {
				if e.Stage == memory.StageGithubSideEffect {
					sideEffectEvents++
				}
			}
			if sideEffectEvents != tt.wantEvents {
				t.Errorf("got %d executor.github_sideeffect events, want %d", sideEffectEvents, tt.wantEvents)
			}

			var sideEffectAlerts int
			for _, e := range processor.events {
				if e.Type == AlertEventTypeGithubSideEffect {
					sideEffectAlerts++
				}
			}
			if sideEffectAlerts != tt.wantAlerts {
				t.Errorf("got %d github_sideeffect alerts, want %d", sideEffectAlerts, tt.wantAlerts)
			}
		})
	}
}

// TestAuditGithubSideEffects_NoSearcherIsNoOp covers the safe-default guard:
// a Runner with no searcher wired (SetGithubSideEffectSearcher never called,
// the default for any call path that hasn't opted in) must not attempt a
// GitHub call or panic.
func TestAuditGithubSideEffects_NoSearcherIsNoOp(t *testing.T) {
	runner := NewRunner()
	task := &Task{
		ID:            "GH-4670-NOOP",
		SourceRepo:    "qf-studio/pilot",
		SourceIssueID: "4670",
	}
	if runner.HasGithubSideEffectSearcher() {
		t.Fatalf("expected no searcher wired by default")
	}
	runner.auditGithubSideEffects(context.Background(), task, time.Now())
}

// TestAuditGithubSideEffects_NonGithubTaskIsNoOp covers the adapter guard:
// tasks without a GitHub SourceRepo (or sourced from a different adapter)
// must never trigger a search call, even with a searcher wired.
func TestAuditGithubSideEffects_NonGithubTaskIsNoOp(t *testing.T) {
	runner := NewRunner()
	searcher := &fakeSideEffectSearcher{hits: []GithubSideEffectIssue{{Number: 1, State: "closed"}}}
	runner.SetGithubSideEffectSearcher(searcher)

	task := &Task{
		ID:            "LIN-123",
		SourceAdapter: "linear",
	}
	runner.auditGithubSideEffects(context.Background(), task, time.Now())

	if searcher.calls != 0 {
		t.Errorf("SearchClosedOrReopenedSince called %d times for non-GitHub task, want 0", searcher.calls)
	}
}
