package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

// GH-5072 (PR#5064 residual): the GH-5056 needs-human admission backstop
// holds the park, but the parked issue stays a hot candidate on the SDK
// poller's side — the studio-sdk bridge drops Skipped/SkipReason converting
// core.IssueResult -> github.IssueResult, so the vendor poller reads the
// skip as "failed without PR" and unmarks the issue, re-entering
// handleGithubIssueEventSDK every poll tick. This file covers the host-side
// fix: arming the shared repick backoff on the needs-human skip so a
// same-window re-pick is caught before repeating the GH-4050 issue fetch,
// spec-guard, and pilot-in-progress label/comment writes.

// TestHandleGithubIssueEventSDK_NeedsHumanArmsAndSuppressesRepickBackoff is
// the primary GH-5072 regression: two consecutive dispatches of a
// needs-human-labeled issue must (1) skip on both calls with the same
// Skipped/SkipReason contract GH-5056 established, and (2) arm the shared
// repick backoff on the first call so the second call is caught by the
// allow() gate rather than re-arming (re-incrementing) the same drop.
func TestHandleGithubIssueEventSDK_NeedsHumanArmsAndSuppressesRepickBackoff(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-gh5072-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	dispatcher := executor.NewDispatcher(store, executor.NewRunner(), nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("dispatcher.Start: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	const taskID = "GH-5072"
	const projectPath = "/nonexistent-gh5072-project"
	backoffKey := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(backoffKey) })

	ev := sdkcore.IssueEvent{
		SequenceID: taskID,
		IssueID:    "5072",
		Title:      "needs-human parked issue",
		Labels:     []string{"pilot", "pilot-needs-human"},
	}

	// First dispatch: not yet gated by backoff (fresh key) — falls through to
	// the GH-5056 needs-human check, skips, and arms the backoff.
	if !repickBackoff.allow(backoffKey) {
		t.Fatal("precondition: backoff must be unarmed before the first dispatch")
	}
	result1, err := handleGithubIssueEventSDK(context.Background(), nil, ev, projectPath, "", dispatcher, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("first call: handleGithubIssueEventSDK() error = %v, want nil", err)
	}
	if result1 == nil || !result1.Skipped {
		t.Fatalf("first call: handleGithubIssueEventSDK() = %+v, want Skipped=true", result1)
	}
	if result1.SkipReason != "needs_human" {
		t.Errorf("first call: SkipReason = %q, want %q", result1.SkipReason, "needs_human")
	}
	if repickBackoff.allow(backoffKey) {
		t.Fatal("expected the repick backoff to be armed after the first needs-human skip")
	}
	claimLostDrops, found, err := dispatcher.ClaimLostDropCount(backoffKey)
	if err != nil {
		t.Fatalf("ClaimLostDropCount: %v", err)
	}
	if !found || claimLostDrops != 1 {
		t.Errorf("after first call: claim-lost drops = %d (found=%v), want 1", claimLostDrops, found)
	}

	// Second dispatch (simulating the next ~30s poll tick, still labeled
	// needs-human): must be thrown out by the backoff pre-check before it
	// even re-evaluates the needs-human label — the claim-lost counter must
	// NOT grow a second time.
	result2, err := handleGithubIssueEventSDK(context.Background(), nil, ev, projectPath, "", dispatcher, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("second call: handleGithubIssueEventSDK() error = %v, want nil", err)
	}
	if result2 == nil || !result2.Skipped {
		t.Fatalf("second call: handleGithubIssueEventSDK() = %+v, want Skipped=true", result2)
	}
	if result2.SkipReason != "needs_human" {
		t.Errorf("second call: SkipReason = %q, want %q", result2.SkipReason, "needs_human")
	}
	claimLostDrops2, found2, err := dispatcher.ClaimLostDropCount(backoffKey)
	if err != nil {
		t.Fatalf("ClaimLostDropCount (after second call): %v", err)
	}
	if !found2 || claimLostDrops2 != 1 {
		t.Errorf("after second call: claim-lost drops = %d (found=%v), want still 1 (suppressed by backoff, not re-armed)", claimLostDrops2, found2)
	}

	// Neither call may have created an execution row or an active dispatch —
	// the needs-human skip itself is unchanged (GH-5056 contract).
	exec, err := store.GetLatestExecutionByTaskID(taskID, projectPath)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetLatestExecutionByTaskID: %v", err)
	}
	if exec != nil {
		t.Errorf("expected no task row created for a pilot-needs-human labeled issue, got %+v", exec)
	}
	if dispatcher.IsActive(taskID, projectPath) {
		t.Error("expected the needs-human labeled issue to never become an active dispatch")
	}
}

// TestHandleGithubIssueEventSDK_NeedsHumanBackoffExpiresAndReadmits covers
// the GH-5072 acceptance criterion that the park is not permanent: once the
// label is removed AND the backoff window has elapsed, the issue must be
// re-evaluated on its own merits rather than shadow-parked forever by the
// backoff this fix introduces.
func TestHandleGithubIssueEventSDK_NeedsHumanBackoffExpiresAndReadmits(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-gh5072-expiry-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	dispatcher := executor.NewDispatcher(store, executor.NewRunner(), nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("dispatcher.Start: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	const taskID = "GH-5072-EXPIRY"
	const projectPath = "/nonexistent-gh5072-expiry-project"
	backoffKey := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(backoffKey) })

	needsHumanEv := sdkcore.IssueEvent{
		SequenceID: taskID,
		IssueID:    "50720",
		Title:      "needs-human parked issue",
		Labels:     []string{"pilot", "pilot-needs-human"},
	}

	// Park it: arms the backoff.
	result, err := handleGithubIssueEventSDK(context.Background(), nil, needsHumanEv, projectPath, "", dispatcher, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("park call: handleGithubIssueEventSDK() error = %v, want nil", err)
	}
	if result == nil || !result.Skipped || result.SkipReason != "needs_human" {
		t.Fatalf("park call: handleGithubIssueEventSDK() = %+v, want Skipped=true SkipReason=needs_human", result)
	}
	if repickBackoff.allow(backoffKey) {
		t.Fatal("expected the backoff to be armed immediately after the needs-human skip")
	}

	// Simulate the backoff window elapsing (mirrors the existing repick
	// backoff test idiom — handler_common_test.go directly manipulates
	// repickBackoff.entries for the same reason: waiting out a real 30s
	// window would make this test slow and flaky).
	repickBackoff.mu.Lock()
	if e, ok := repickBackoff.entries[backoffKey]; ok {
		e.nextAllowedAt = time.Now().Add(-time.Second)
	} else {
		t.Fatal("expected an in-memory backoff entry for the armed key")
	}
	repickBackoff.mu.Unlock()

	if !repickBackoff.allow(backoffKey) {
		t.Fatal("expected the backoff window to have elapsed")
	}

	// Label removed + window elapsed: the needs-human check no longer fires,
	// and the backoff pre-check no longer gates — the issue proceeds past
	// both GH-5072 checks into normal admission. With no real project
	// directory behind projectPath, the dispatcher genuinely attempts and
	// fails the dispatch (preflight git_clean check) rather than being
	// declined at an admission gate — that failure mode is exactly the
	// proof this test wants: a non-ErrDispatchGated error means the task
	// was NOT shadow-parked by the GH-5072 backoff, it was actually admitted
	// and picked up (mirrors TestHandleIssueGeneric_RepickDoesNotClearBackoff's
	// use of a nonexistent projectPath to exercise real admission cheaply).
	admittedEv := sdkcore.IssueEvent{
		SequenceID: taskID,
		IssueID:    "50720",
		Title:      "no longer needs a human",
		Labels:     []string{"pilot"},
	}
	result2, err := handleGithubIssueEventSDK(context.Background(), &config.Config{}, admittedEv, projectPath, "", dispatcher, nil, nil, nil, nil, nil, nil)
	if result2 != nil && result2.Skipped && result2.SkipReason == "needs_human" {
		t.Errorf("readmit call: still shadow-parked as needs_human after label removal + backoff expiry: %+v", result2)
	}
	if errors.Is(err, executor.ErrDispatchGated) {
		t.Errorf("readmit call: still dispatch-gated after label removal + backoff expiry: %v", err)
	}
	if repickBackoff.allow(backoffKey) == false {
		t.Error("readmit call: expected the backoff pre-check itself to have allowed this attempt (window had elapsed)")
	}
}
