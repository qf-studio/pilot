package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// seedGH4211ExecutionTimes sets explicit created_at/started_at on an executions
// row via a direct SQL connection, bypassing UpdateExecutionStatus's
// CURRENT_TIMESTAMP (whole-second resolution, which would make a short-lived
// test's queue-wait delta flaky/zero).
func seedGH4211ExecutionTimes(t *testing.T, dbPath string, executionID string, createdAt, startedAt time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open failed: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(
		`UPDATE executions SET created_at = ?, started_at = ? WHERE id = ?`,
		createdAt, startedAt, executionID,
	); err != nil {
		t.Fatalf("seed execution times failed: %v", err)
	}
}

// TestGithubOnPRCreatedHandler_ObservesThroughputHistograms drives the real
// production wiring (pollerDeps.OnPRCreated -> githubOnPRCreatedHandler ->
// Controller.OnPRCreated) with an actual sdkcore.PRCreatedEvent and a real
// SQLite-backed memory.Store, instead of calling ctrl.OnPRCreated directly.
//
// GH-4130's own test (internal/autopilot/gh4130_test.go) only ever called
// OnPRCreated directly against a hand-built *memory.Execution with StartedAt
// already set — it never went through GetLatestExecutionByTaskID's real SQL
// scan. That's exactly why GH-4211's root cause (executionDetailColumns/
// scanExecutionDetail never selected the started_at column, so exec.StartedAt
// was always nil for every real lookup) shipped invisible: the fake test
// double bypassed the buggy code path entirely. This test exercises the real
// DB round trip through the real live entry point so that gap can't recur.
func TestGithubOnPRCreatedHandler_ObservesThroughputHistograms(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	execID := "exec-gh4211"
	if err := store.SaveExecution(&memory.Execution{
		ID: execID, TaskID: "GH-4211", ProjectPath: "/p", Status: "queued",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "running"); err != nil {
		t.Fatalf("UpdateExecutionStatus(running): %v", err)
	}
	// Pin explicit, second-resolution-safe timestamps: created_at 6m before
	// started_at (queue wait) and started_at 4m in the past (time-to-PR), so
	// assertions below aren't flaky against CURRENT_TIMESTAMP's whole-second
	// resolution.
	startedAt := time.Now().Add(-4 * time.Minute)
	createdAt := startedAt.Add(-6 * time.Minute)
	seedGH4211ExecutionTimes(t, filepath.Join(tmpDir, "pilot.db"), execID, createdAt, startedAt)

	cfg := autopilot.DefaultConfig()
	ghClient := githubSDK.NewClient(testutil.FakeGitHubToken)
	ctrl := autopilot.NewController(cfg, ghClient, nil, "owner", "repo", autopilot.WithMemoryStore(store))

	// This is the exact function production wires into pollerDeps.OnPRCreated
	// (poller_github.go), not a re-implementation of it.
	handler := githubOnPRCreatedHandler(ctrl)
	handler(sdkcore.PRCreatedEvent{
		PRNumber:   4212,
		PRURL:      "https://github.com/owner/repo/pull/4212",
		IssueID:    "4211",
		HeadSHA:    "abc1234",
		BranchName: "pilot/GH-4211",
	})

	hist := ctrl.Metrics().HistogramSnapshot()
	if len(hist.TimeToPRDurations) != 1 {
		t.Fatalf("TimeToPRDurations = %v, want 1 sample recorded via the live entry point", hist.TimeToPRDurations)
	}
	if len(hist.QueueWaitDurations) != 1 {
		t.Fatalf("QueueWaitDurations = %v, want 1 sample recorded via the live entry point", hist.QueueWaitDurations)
	}
	if got := hist.QueueWaitDurations[0]; got < 5*time.Minute || got > 7*time.Minute {
		t.Errorf("QueueWaitDurations[0] = %v, want ~6m", got)
	}
	if hist.TimeToPRDurations[0] < 3*time.Minute {
		t.Errorf("TimeToPRDurations[0] = %v, want >= 3m (started_at was ~4m ago)", hist.TimeToPRDurations[0])
	}
}

// TestGithubOnPRCreatedHandler_MissingStartedAt_SkipsObservation pins the
// fail-loud guard added in controller.go: an execution row with no
// started_at (e.g. never picked up by a worker) must not be observed as a
// zero/negative-duration sample, but the lookup itself must not panic or
// error out the handler.
func TestGithubOnPRCreatedHandler_MissingStartedAt_SkipsObservation(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-gh4211-b", TaskID: "GH-9999", ProjectPath: "/p", Status: "queued",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	cfg := autopilot.DefaultConfig()
	ghClient := githubSDK.NewClient(testutil.FakeGitHubToken)
	ctrl := autopilot.NewController(cfg, ghClient, nil, "owner", "repo", autopilot.WithMemoryStore(store))

	handler := githubOnPRCreatedHandler(ctrl)
	handler(sdkcore.PRCreatedEvent{
		PRNumber:   4213,
		PRURL:      "https://github.com/owner/repo/pull/4213",
		IssueID:    "9999",
		HeadSHA:    "def5678",
		BranchName: "pilot/GH-9999",
	})

	hist := ctrl.Metrics().HistogramSnapshot()
	if len(hist.TimeToPRDurations) != 0 || len(hist.QueueWaitDurations) != 0 {
		t.Errorf("expected no histogram samples for an execution with no started_at, got TimeToPR=%v QueueWait=%v",
			hist.TimeToPRDurations, hist.QueueWaitDurations)
	}
}
