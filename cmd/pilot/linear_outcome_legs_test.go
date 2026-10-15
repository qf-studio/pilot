package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	linearSDK "github.com/qf-studio/studio-sdk/sdk/integrations/linear"

	"github.com/qf-studio/pilot/internal/adapters/linear"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

// countingLegNotifier counts NotifyTaskFailed calls on top of the real notifier
// (which posts to the fake Linear API).
type countingLegNotifier struct {
	*linearLegNotifier
	failed atomic.Int32
}

func (n *countingLegNotifier) NotifyTaskFailed(ctx context.Context, issueID, reason string) error {
	n.failed.Add(1)
	return n.linearLegNotifier.NotifyTaskFailed(ctx, issueID, reason)
}

// TestLinearOutcomeDrivesCommentAndState (GH-5705) drives the real chain
// handleLinearIssueWithResult → handleIssueGeneric → Dispatcher.WaitForExecution
// → runLinearStateLegs against a real store row in each persisted status and a
// fake Linear API, and asserts the exact comment, the state moves and the
// restored state. Admission is paused so no worker touches the queued row; the
// post-claim hook writes the terminal status the executor would have.
func TestLinearOutcomeDrivesCommentAndState(t *testing.T) {
	const (
		backlog  = testLinearBacklogID
		started  = testLinearStartedFirst
		inReview = testLinearInReviewID
		prURL    = "https://github.com/o/r/pull/77"
	)

	tests := []struct {
		name        string
		status      string // persisted execution status ("" = gated decline)
		execError   string
		prURL       string
		transitions linear.Transitions
		// wantComment is a substring of the single comment posted after the
		// start comment; "" means no comment at all.
		wantComment string
		// wantAlso / wantNot are further substrings the comment must / must not contain.
		wantAlso   []string
		wantNot    []string
		wantStates []string // issueUpdate stateIds in order: start first
		wantFailed int32    // NotifyTaskFailed calls
	}{
		{
			name: "completed with PR: completion comment, in_review", status: "completed", prURL: prURL,
			transitions: linear.Transitions{InReview: inReview},
			wantComment: "Pilot completed this task", wantStates: []string{started, inReview},
		},
		{
			name: "no_op: no-changes comment, restores prior state, no failure", status: "no_op",
			wantComment: "No changes were needed", wantStates: []string{started, backlog},
		},
		{
			name: "needs_human without in_review: held comment names the branch, stays started", status: "needs_human",
			wantComment: "Branch `pilot/LIN-QF-5705` is pushed and held for human review", wantStates: []string{started},
		},
		{
			name: "needs_human, push failed: names the reason and says the branch is not on the remote", status: "needs_human",
			execError:   "timeout salvage (branch push also failed: remote rejected: protected branch)",
			wantComment: "Branch `pilot/LIN-QF-5705` is NOT on the remote",
			wantAlso:    []string{"**Reason**: timeout salvage (branch push also failed: remote rejected: protected branch)"},
			wantNot:     []string{"is pushed and held"},
			wantStates:  []string{started},
		},
		{
			name: "needs_human, pushed: carries the row error as the reason", status: "needs_human",
			execError:   "timeout salvage: ran out of budget",
			wantComment: "is pushed and held for human review",
			wantAlso:    []string{"**Reason**: timeout salvage: ran out of budget"},
			wantNot:     []string{"NOT on the remote"},
			wantStates:  []string{started},
		},
		{
			name: "needs_human with in_review configured: moves to in_review", status: "needs_human",
			transitions: linear.Transitions{InReview: inReview},
			wantComment: "held for human review", wantStates: []string{started, inReview},
		},
		{
			name: "canceled: names the cause, restores prior state", status: "canceled",
			wantComment: "it was canceled", wantStates: []string{started, backlog},
		},
		{
			name: "superseded: one comment, no state move, no reopen advice", status: "superseded",
			wantComment: "it was superseded", wantNot: []string{"Reopen", "re-label"}, wantStates: []string{started},
		},
		{
			name: "completed without a PR: says no PR was produced and names the branch", status: "completed",
			wantComment: "The run completed on branch `pilot/LIN-QF-5705` but produced no pull request",
			wantNot:     []string{"issue was closed during the run", "was canceled"},
			wantStates:  []string{started, backlog},
		},
		{
			name: "failed: status is the class, failed leg", status: "failed", execError: "boom in /tmp/wt/x.go",
			transitions: linear.Transitions{Failed: inReview},
			wantComment: "**Reason**: failed: boom in <path>", wantNot: []string{"execution failed:"}, wantStates: []string{started, inReview}, wantFailed: 1,
		},
		{
			name: "rate_limited: comment starts with rate_limited:", status: "rate_limited", execError: "429 from https://api.example.test/v1, retry later",
			transitions: linear.Transitions{Failed: inReview},
			wantComment: "**Reason**: rate_limited: 429 from https://api.example.test/v1, retry later", wantStates: []string{started, inReview}, wantFailed: 1,
		},
		{
			name: "stalled: failure comment, not 'unknown error'", status: "stalled", execError: "no output for 10m",
			transitions: linear.Transitions{Failed: inReview},
			wantComment: "**Reason**: stalled: no output for 10m", wantStates: []string{started, inReview}, wantFailed: 1,
		},
		{
			name: "gated decline: nothing at all", status: "",
			wantComment: "", wantStates: nil,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newLinearStateFake()
			defer f.server.Close()
			c := f.client()
			mover := newLinearStateMover(c, linearSDK.NewSyncAdapter(c), tt.transitions)
			notifier := &countingLegNotifier{linearLegNotifier: newLinearLegNotifier(c)}

			store, err := memory.NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			dispatcher := executor.NewDispatcher(store, executor.NewRunner(), nil)
			dispatcher.PauseAdmission() // no worker picks the queued row up
			if err := dispatcher.Start(context.Background()); err != nil {
				t.Fatalf("dispatcher.Start: %v", err)
			}
			t.Cleanup(dispatcher.Stop)

			const taskID = "LIN-QF-5705"
			projectPath := "/tmp/pilot-gh-5705-outcome-does-not-exist-" + string(rune('a'+i))
			t.Cleanup(func() { repickBackoff.recordSuccess(repickBackoffKey(projectPath, taskID)) })

			if tt.status == "" {
				// A terminal completion already exists: the admission gate declines.
				seed := &executor.Task{ID: taskID, ProjectPath: projectPath}
				execID, err := executor.NewExecutionLifecycle(store).Begin(seed, executor.ExecStatusRunning, 0)
				if err != nil {
					t.Fatalf("seed Begin: %v", err)
				}
				if err := store.UpdateExecutionStatus(execID, "no_op"); err != nil {
					t.Fatalf("seed status: %v", err)
				}
			}

			cfg := &config.Config{Adapters: &config.AdaptersConfig{Linear: &linear.Config{}}}
			ev := sdkcore.IssueEvent{IssueID: testLinearIssueUUID, SequenceID: taskID, Title: "outcome"}

			res, err := runLinearStateLegs(context.Background(), ev, notifier, mover, func(admitted func()) (*linearRun, error) {
				claimed := func() {
					admitted()
					if tt.status == "" {
						return
					}
					row, gerr := store.GetLatestExecutionByTaskID(taskID, projectPath)
					if gerr != nil || row == nil {
						t.Errorf("queued row missing after claim: %v", gerr)
						return
					}
					if tt.prURL != "" {
						if uerr := store.UpdateExecutionResult(row.ID, tt.prURL, "abc123", 1000); uerr != nil {
							t.Errorf("UpdateExecutionResult: %v", uerr)
						}
					}
					if uerr := store.UpdateExecutionStatus(row.ID, tt.status, tt.execError); uerr != nil {
						t.Errorf("UpdateExecutionStatus: %v", uerr)
					}
				}
				return handleLinearIssueWithResult(context.Background(), cfg, ev, projectPath, dispatcher, executor.NewRunner(), executor.NewMonitor(), nil, nil, nil, claimed)
			})
			if tt.status == "failed" || tt.status == "" {
				// failed rows surface as a handler error; gated declines as none.
				if (tt.status == "failed") != (err != nil) {
					t.Fatalf("handler error = %v, want error only for a failed row", err)
				}
			} else if err != nil {
				t.Fatalf("handler error = %v, want none", err)
			}
			if res == nil {
				t.Fatal("nil result")
			}

			f.mu.Lock()
			defer f.mu.Unlock()
			var states []string
			for _, u := range f.updates {
				if u["id"] != testLinearIssueUUID {
					t.Errorf("issueUpdate id = %s, want the issue UUID", u["id"])
				}
				states = append(states, u["stateId"])
			}
			if strings.Join(states, ",") != strings.Join(tt.wantStates, ",") {
				t.Errorf("state moves = %v, want %v", states, tt.wantStates)
			}
			if tt.wantComment == "" {
				if len(f.comments) != 0 {
					t.Errorf("comments = %q, want none", f.comments)
				}
			} else if len(f.comments) != 1 || !strings.Contains(f.comments[0], tt.wantComment) {
				t.Errorf("comments = %q, want exactly one containing %q", f.comments, tt.wantComment)
			} else {
				for _, w := range tt.wantAlso {
					if !strings.Contains(f.comments[0], w) {
						t.Errorf("comment = %q, want it to contain %q", f.comments[0], w)
					}
				}
				for _, w := range tt.wantNot {
					if strings.Contains(f.comments[0], w) {
						t.Errorf("comment = %q, must not contain %q", f.comments[0], w)
					}
				}
			}
			if got := notifier.failed.Load(); got != tt.wantFailed {
				t.Errorf("NotifyTaskFailed calls = %d, want %d", got, tt.wantFailed)
			}
		})
	}
}

func TestLinearOutcomeDetail(t *testing.T) {
	hr := &HandlerResult{Result: &executor.ExecutionResult{Error: "  stalled for 10m "}}
	if got := linearOutcomeDetail(hr); got != "  stalled for 10m " {
		t.Errorf("detail = %q, want the row's error text", got)
	}
	if got := linearOutcomeDetail(&HandlerResult{Error: errors.New("boom"), Result: &executor.ExecutionResult{Error: "x"}}); got != "" {
		t.Errorf("detail = %q, want none when hr.Error is set", got)
	}
	if got := linearOutcomeDetail(&HandlerResult{}); got != "" {
		t.Errorf("detail = %q, want none", got)
	}
}

func TestLinearOutcome(t *testing.T) {
	tests := []struct {
		name string
		hr   *HandlerResult
		want string
	}{
		{"result outcome (runner tag or row status)", &HandlerResult{Result: &executor.ExecutionResult{Outcome: "no_op"}}, "no_op"},
		{"outcome wins over success", &HandlerResult{Success: true, Result: &executor.ExecutionResult{Outcome: "needs_human"}}, "needs_human"},
		{"failed row has no result", &HandlerResult{}, ""},
		{"direct-runner success", &HandlerResult{Success: true}, "completed"},
		{"direct-runner failure", &HandlerResult{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := linearOutcome(tt.hr); got != tt.want {
				t.Errorf("outcome = %q, want %q", got, tt.want)
			}
		})
	}
}
