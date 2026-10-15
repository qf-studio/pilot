package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		name      string
		status    string // persisted execution status ("" = gated decline)
		execError string
		prURL     string
		commitSHA string // recorded commit ("" = none)
		pushed    *bool  // persisted branch_pushed (nil = NULL)
		// issueStateAtEnd is the state type Linear reports when the run ends
		// ("" = backlog, i.e. whatever the start move left).
		issueStateAtEnd string
		transitions     linear.Transitions
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
			name: "needs_human, pushed: names the branch and carries the row error as the reason", status: "needs_human",
			pushed:      boolPtr(true),
			execError:   "timeout salvage: ran out of budget",
			wantComment: "Branch `pilot/LIN-QF-5705` is pushed and held for human review",
			wantAlso:    []string{"**Reason**: timeout salvage: ran out of budget"},
			wantNot:     []string{"NOT on the remote", "No branch was pushed"},
			wantStates:  []string{started},
		},
		{
			name: "needs_human, push failed: the [remote rejected] line is the reason, not the To header", status: "needs_human",
			pushed:      boolPtr(false),
			execError:   "timeout salvage (branch push also failed: " + executor.PushFailureReason(fmt.Errorf("failed to push: exit status 1: %s", multiLinePushStderr)) + ")",
			wantComment: "Branch `pilot/LIN-QF-5705` is NOT on the remote",
			wantAlso:    []string{"! [remote rejected] pilot/LIN-QF-5705 -> pilot/LIN-QF-5705 (protected branch hook declined)"},
			wantNot:     []string{"is pushed and held", "To <path>", "github.com/o/r.git"},
			wantStates:  []string{started},
		},
		{
			name: "needs_human, nothing pushed: says no branch was pushed", status: "needs_human",
			execError:   "timeout salvage: ran out of budget",
			wantComment: "No branch was pushed",
			wantNot:     []string{"is pushed and held", "NOT on the remote", "`pilot/LIN-QF-5705`"},
			wantStates:  []string{started},
		},
		{
			name: "needs_human with in_review configured: moves to in_review", status: "needs_human",
			pushed:      boolPtr(true),
			transitions: linear.Transitions{InReview: inReview},
			wantComment: "held for human review", wantStates: []string{started, inReview},
		},
		{
			name: "canceled: names the cause, restores prior state", status: "canceled",
			wantComment: "it was canceled", wantStates: []string{started, backlog},
		},
		{
			name: "superseded, issue already Done by a human: comment only, zero moves", status: "superseded",
			issueStateAtEnd: "completed",
			wantComment:     "it was superseded", wantNot: []string{"Reopen", "re-label"}, wantStates: []string{started},
		},
		{
			name: "superseded, issue already canceled by a human: comment only, zero moves", status: "superseded",
			issueStateAtEnd: "canceled",
			wantComment:     "it was superseded", wantStates: []string{started},
		},
		{
			name: "superseded mechanically, issue still In Progress: restores the prior state", status: "superseded",
			issueStateAtEnd: "started",
			wantComment:     "it was superseded", wantNot: []string{"Reopen", "re-label"}, wantStates: []string{started, backlog},
		},
		{
			name: "completed without a PR and no commit: says no PR was produced and names no branch", status: "completed",
			wantComment: "The run completed; no branch was pushed, but produced no pull request",
			wantNot:     []string{"`pilot/LIN-QF-5705`", "issue was closed during the run", "was canceled"},
			wantStates:  []string{started, backlog},
		},
		{
			name: "completed without a PR but with a commit: names the branch", status: "completed",
			commitSHA:   "abc123",
			wantComment: "The run completed on branch `pilot/LIN-QF-5705` but produced no pull request",
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
					commit := tt.commitSHA
					if tt.prURL != "" && commit == "" {
						commit = "abc123"
					}
					if tt.prURL != "" || commit != "" {
						if uerr := store.UpdateExecutionResult(row.ID, tt.prURL, commit, 1000); uerr != nil {
							t.Errorf("UpdateExecutionResult: %v", uerr)
						}
					}
					if tt.pushed != nil {
						if uerr := store.SetExecutionBranchPushed(row.ID, *tt.pushed); uerr != nil {
							t.Errorf("SetExecutionBranchPushed: %v", uerr)
						}
					}
					if tt.issueStateAtEnd != "" {
						f.setStateType(tt.issueStateAtEnd)
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
			// GH-5715: every leg ends the prior-state bookkeeping for the issue.
			mover.mu.Lock()
			defer mover.mu.Unlock()
			if len(mover.priorState) != 0 {
				t.Errorf("priorState = %v, want empty after the run", mover.priorState)
			}
		})
	}
}

// multiLinePushStderr is what `git push` prints on a protected-branch
// rejection: the URL header first, the reason on the "!" line.
const multiLinePushStderr = `To https://github.com/o/r.git
 ! [remote rejected] pilot/LIN-QF-5705 -> pilot/LIN-QF-5705 (protected branch hook declined)
error: failed to push some refs to 'https://github.com/o/r.git'`

// TestLinearPushMarkerGone (GH-5715) pins that the adapter no longer infers the
// push state from the row-error substring holdPushedBranch used to write: the
// state is the persisted branch_pushed column.
func TestLinearPushMarkerGone(t *testing.T) {
	const marker = "branch push also failed"
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		// This file names the marker in order to assert its absence.
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || e.Name() == "linear_outcome_legs_test.go" {
			continue
		}
		b, err := os.ReadFile(filepath.Clean(e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), marker) {
			t.Errorf("%s still contains %q", e.Name(), marker)
		}
	}
}

// TestLinearSupersededForgetsPrior (GH-5715) drives MoveSuperseded directly:
// both branches (human-closed, restored) forget the recorded prior state; a
// failed state read keeps it (the issue would otherwise stay started forever)
// and moves nothing.
func TestLinearSupersededForgetsPrior(t *testing.T) {
	for _, tt := range []struct {
		name, stateType string
		failRead        bool
		wantUpdates     int
		wantPrior       int
	}{
		{"closed by a human", "completed", false, 0, 0},
		{"still started", "started", false, 1, 0},
		{"state read fails", "started", true, 0, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newLinearStateFake()
			defer f.server.Close()
			m := f.mover(linear.Transitions{})
			ctx := context.Background()
			if err := m.Move(ctx, testLinearIssueUUID, "LIN-QF-295", legStart); err != nil {
				t.Fatalf("start: %v", err)
			}
			f.setStateType(tt.stateType)
			f.mu.Lock()
			f.failGetIssue = tt.failRead
			f.mu.Unlock()
			err := m.MoveSuperseded(ctx, testLinearIssueUUID, "LIN-QF-295")
			if tt.failRead && err == nil {
				t.Error("MoveSuperseded error = nil, want the read error")
			}
			if len(m.priorState) != tt.wantPrior {
				t.Errorf("priorState = %v, want %d entries", m.priorState, tt.wantPrior)
			}
			if got := f.count("issueUpdate") - 1; got != tt.wantUpdates {
				t.Errorf("moves after start = %d, want %d", got, tt.wantUpdates)
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
