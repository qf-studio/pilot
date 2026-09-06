package executor

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestDispatcher_NextRetryGeneration_AdvancesPastFailedThenStalledClaims is
// the GH-5272 acceptance-test step 1: two terminal executions (a "failed" row
// at generation 0, then a "stalled" row at generation 1 — the exact shape
// GH-493 reproduced: two 1h hard-timeout kills in a row) must not permanently
// occupy the claim ledger. nextRetryGeneration is expected to advance past
// the highest terminal-bound generation (both "failed" and "stalled" count as
// terminal per isTerminalExecutionStatus) and beginWithGenerationRetry must
// turn that into a genuine generation-2 claim — the pilot-retry-ready re-arm
// path's actual dispatch mechanism.
//
// This passes on main: the claim-race logic itself (LatestClaimGeneration +
// nextRetryGeneration + ClaimExecution) already does the right thing here —
// GH-4372's dead-owner carve-out already treats a stalled claim as
// terminal-not-done and hands out generation+1. Kept as a regression guard;
// see TestDispatcher_BeginWithGenerationRetry_HardCapEscalatedTaskCanReArm
// below for the actual GH-5272 defect, which lives one layer up in the
// persisted repick-backoff hard-cap counter, not in the claim ledger itself.
func TestDispatcher_NextRetryGeneration_AdvancesPastFailedThenStalledClaims(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	taskID, projectPath := "GH-493", "/project-gh-493"

	// Generation 0: a real execution that ran and hit the 1h hard-timeout
	// cap, recorded as "failed" (mirrors 77f7265f from the incident).
	gen0Task := &Task{ID: taskID, ProjectPath: projectPath}
	execID0, err := NewExecutionLifecycle(store).Begin(gen0Task, ExecStatusRunning, 0)
	if err != nil {
		t.Fatalf("setup: generation 0 Begin failed: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID0, string(ExecStatusFailed), "execution exceeded 1h hard cap"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
	}

	// Generation 1: the retry also ran and hit the same 1h hard-timeout cap,
	// this time recorded as "stalled" by the watchdog (mirrors a1c3aaeb).
	gen1Task := &Task{ID: taskID, ProjectPath: projectPath}
	execID1, err := NewExecutionLifecycle(store).Begin(gen1Task, ExecStatusRunning, 1)
	if err != nil {
		t.Fatalf("setup: generation 1 Begin failed: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID1, string(ExecStatusStalled), "execution exceeded 1h hard cap"); err != nil {
		t.Fatalf("setup: failed to mark generation 1 as stalled: %v", err)
	}

	dispatcher := NewDispatcher(store, NewRunner(), nil)

	gen, retry, err := dispatcher.nextRetryGeneration(taskID, projectPath)
	if err != nil {
		t.Fatalf("nextRetryGeneration: %v", err)
	}
	if !retry {
		t.Fatal("expected retry=true — neither a failed nor a stalled row is a live owner or a done task")
	}
	if gen != 2 {
		t.Errorf("expected nextRetryGeneration to advance past the highest terminal-bound generation (1) to 2, got %d", gen)
	}

	// The pilot-retry-ready re-arm path's actual dispatch mechanism: the next
	// poller pickup for the same task_id.
	freshTask := &Task{ID: taskID, ProjectPath: projectPath}
	execID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry: %v", err)
	}
	if execID == "" {
		t.Fatal("expected the pilot-retry-ready re-arm to claim generation 2, got empty execID (claim-lost loop reproduced)")
	}

	genCheck, execCheck, found, err := store.LatestClaimGeneration(taskID, projectPath)
	if err != nil {
		t.Fatalf("LatestClaimGeneration: %v", err)
	}
	if !found || genCheck != 2 {
		t.Errorf("expected latest claim generation 2, found=%v got=%d", found, genCheck)
	}
	if execCheck != execID {
		t.Errorf("expected the generation 2 claim to reference %q, got %q", execID, execCheck)
	}
}

// TestDispatcher_BeginWithGenerationRetry_HardCapEscalatedTaskStaysDroppedWithoutExternalRearm
// documents the actual defect behind the GH-493 incident and where its fix
// does NOT belong. Once a task trips the repick hard cap
// (dispatcherRepickHardCap) and is escalated-and-held
// (stallTaskAfterRepickHardCap marks its claim "stalled" and persists the
// consecutive-drops counter at/above the cap), beginWithGenerationRetry must
// keep dropping every subsequent pickup attempt on its own — it has no
// GitHub-label visibility, so it cannot tell a genuine pilot-retry-ready
// re-arm apart from an ordinary poll tick that re-offers the same wedged
// task_id. TestDispatcher_BeginWithGenerationRetry_HardCapIsIdempotent
// already covers exactly this invariant for the "nothing external happened"
// case; this test is the GH-5272 sibling proving it holds even with the
// exact GH-493 end state (claimed execution failed, then escalated to
// stalled, persisted hard-cap counter) reproduced directly.
//
// The actual fix lives one layer up, in cmd/pilot's terminalCompletionChecker
// (rearm_stalled.go's tryRearmStalled/sweepStalledRearm, GH-5212's re-arm
// probe extended by GH-5272) — the only place with GitHub API access to
// confirm a genuine post-stall pilot-retry-ready label add or issue reopen
// happened. That probe's two GH-5272 fixes are exercised by
// cmd/pilot/gh5272_stalled_rearm_test.go:
//  1. sweepStalledRearm's candidate query no longer requires pilot-blocked —
//     the bot's own posted re-arm recipe removes that label in the same edit
//     that adds pilot-retry-ready, so a pilot-blocked-only query missed
//     every issue an operator had just re-armed correctly.
//  2. tryRearmStalled's re-arm evidence check now also accepts a labeled
//     event for pilot-retry-ready/-1/-2 (not just the base trigger label),
//     since the documented recipe never touches the base label at all.
//
// Once tryRearmStalled confirms re-arm evidence, it demotes the stalled row
// to failed (store.ReclassifyStalledForRearm — verified below to correctly
// unblock the ordinary retry path with no dispatcher-side bypass needed) and
// calls repickBackoff.recordSuccess, which clears the exact persisted
// consecutive-drops counter this test proves beginWithGenerationRetry cannot
// clear on its own.
func TestDispatcher_BeginWithGenerationRetry_HardCapEscalatedTaskStaysDroppedWithoutExternalRearm(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// SourceAdapter left empty (not "github") so stallTaskAfterRepickHardCap's
	// surfaceStalledIssue step skips shelling out to the gh CLI entirely —
	// mirrors TestDispatcher_StallTaskAfterRepickHardCap_NonGitHubTaskSkipsGHCLI.
	// The escalation-and-hard-cap-counter mechanics under test are identical
	// either way.
	taskID, projectPath := "GH-493", "/project-gh-493-hardcap"
	task := &Task{ID: taskID, ProjectPath: projectPath}

	dispatcher := NewDispatcher(store, NewRunner(), nil)
	var buf bytes.Buffer
	dispatcher.log = slog.New(slog.NewTextHandler(&buf, nil))
	key := repickBackoffKey(projectPath, taskID)

	// Reproduce the GH-493 end state directly: a claimed execution and a
	// persisted repick-backoff counter already at the hard cap, then drive
	// the actual escalation path (stallTaskAfterRepickHardCap) exactly as
	// beginWithGenerationRetry's hard-cap branch would — this is what
	// happened to GH-493 before the two terminal executions the issue's
	// timeline calls out explicitly.
	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, string(ExecStatusFailed), "boom"); err != nil {
		t.Fatalf("setup: failed to mark execution failed: %v", err)
	}
	if err := dispatcher.SetRepickBackoffState(key, dispatcherRepickHardCap, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("setup SetRepickBackoffState: %v", err)
	}
	dispatcher.stallTaskAfterRepickHardCap(task, 0, dispatcherRepickHardCap)

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != string(ExecStatusStalled) {
		t.Fatalf("expected the hard-cap escalation to mark the claimed execution stalled, got %q", exec.Status)
	}
	if !strings.Contains(exec.Error, "repick backoff hard cap reached") {
		t.Fatalf("expected the hard-cap escalation reason on the claimed execution, got %q", exec.Error)
	}

	consecutiveDrops, _, found, err := dispatcher.RepickBackoffState(key)
	if err != nil {
		t.Fatalf("RepickBackoffState: %v", err)
	}
	if !found || consecutiveDrops < dispatcherRepickHardCap {
		t.Fatalf("expected the persisted hard-cap counter to remain at/above the cap after escalation, found=%v got=%d", found, consecutiveDrops)
	}

	// Simulate an ordinary poller pickup with NO external re-arm signal
	// available to this layer (no GitHub label was checked, no operator
	// action occurred) — beginWithGenerationRetry must stay dropped, exactly
	// like TestDispatcher_BeginWithGenerationRetry_HardCapIsIdempotent
	// requires. Inferring a re-arm from claim state alone here would be
	// unsafe — see that test's own doc comment.
	buf.Reset()
	rearmedTask := &Task{ID: taskID, ProjectPath: projectPath}
	execID, err = dispatcher.beginWithGenerationRetry(rearmedTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry: %v", err)
	}
	if execID != "" {
		t.Fatalf("expected the pickup to stay dropped with no external re-arm evidence, got execID=%q", execID)
	}

	// Now apply exactly what tryRearmStalled does once it confirms GitHub-side
	// re-arm evidence (store.ReclassifyStalledForRearm + repickBackoff's
	// ClearRepickBackoffState, proxied here directly since this package has
	// no GitHub client) — proving those two calls are a sufficient, correct
	// unblock with no bypass of beginWithGenerationRetry's own invariants.
	if err := store.ReclassifyStalledForRearm(taskID, projectPath, "test: simulated GH-5272 re-arm"); err != nil {
		t.Fatalf("ReclassifyStalledForRearm: %v", err)
	}
	if err := dispatcher.ClearRepickBackoffState(key); err != nil {
		t.Fatalf("ClearRepickBackoffState: %v", err)
	}

	buf.Reset()
	execID, err = dispatcher.beginWithGenerationRetry(rearmedTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry after simulated re-arm: %v", err)
	}
	if execID == "" {
		t.Fatalf("expected the simulated re-arm to claim a fresh generation, got dropped pickup: log=%s", buf.String())
	}
}

// TestDispatcher_QueueSingleTask_ClaimLostLogsBlockingClaimDetails is the
// GH-5272 acceptance-test step 3 regression test: the "dispatch claim lost"
// drop log must carry the blocking claim's generation and the owning
// execution's status, so an incident like GH-493's can be diagnosed from the
// log alone instead of requiring a direct SQL query against execution_claims.
func TestDispatcher_QueueSingleTask_ClaimLostLogsBlockingClaimDetails(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	taskID, projectPath := "GH-5272-LOGFIELDS", "/project-gh-5272-logfields"

	liveTask := &Task{ID: taskID, ProjectPath: projectPath}
	if _, err := NewExecutionLifecycle(store).Begin(liveTask, ExecStatusRunning, 0); err != nil {
		t.Fatalf("setup: winning Begin failed: %v", err)
	}

	dispatcher := NewDispatcher(store, NewRunner(), nil)
	var buf bytes.Buffer
	dispatcher.log = slog.New(slog.NewTextHandler(&buf, nil))

	dupTask := &Task{ID: taskID, ProjectPath: projectPath}
	execID, err := dispatcher.queueSingleTask(context.Background(), dupTask)
	if err != nil {
		t.Fatalf("expected the duplicate pickup against a live owner to drop silently, got error: %v", err)
	}
	if execID != "" {
		t.Errorf("expected empty execID for a live-owner duplicate pickup, got %q", execID)
	}

	logged := buf.String()
	if !strings.Contains(logged, "dispatch claim lost") {
		t.Fatalf("expected a log noting the dropped claim, got: %s", logged)
	}
	if !strings.Contains(logged, "blocking_claim_generation=0") {
		t.Errorf("expected the claim-lost log to carry the blocking claim's generation, got: %s", logged)
	}
	if !strings.Contains(logged, "blocking_execution_status=running") {
		t.Errorf("expected the claim-lost log to carry the owning execution's status, got: %s", logged)
	}
}
