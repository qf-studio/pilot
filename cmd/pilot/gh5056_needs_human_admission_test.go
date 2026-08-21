package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

// GH-5056: the SDK poller's own candidate filter (studio-sdk
// poller.go:742-815) checks pilot-in-progress/done/blocked/needs-
// clarification/failed/retry-ready but never pilot-needs-human. A park
// (escalateBasePresenceHold, autopilot's escalateAndHold) strips
// pilot-in-progress while leaving the pilot trigger label standing, so once
// isTaskStillQueued goes false the poller unmarks and re-dispatches after
// its 5-min grace — re-holding/re-escalating on a slow loop. This file
// covers the host-side admission backstop added to close that loop.

// TestGithubEventHasNeedsHumanLabel is a direct unit test of the label-match
// helper handleGithubIssueEventSDK's admission check consults.
func TestGithubEventHasNeedsHumanLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{name: "absent, other pilot labels present", labels: []string{"pilot", "pilot-in-progress"}, want: false},
		{name: "present alongside other labels", labels: []string{"pilot", "pilot-needs-human"}, want: true},
		{name: "present alone", labels: []string{"pilot-needs-human"}, want: true},
		{name: "nil labels", labels: nil, want: false},
		{name: "empty labels", labels: []string{}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := githubEventHasNeedsHumanLabel(tt.labels); got != tt.want {
				t.Errorf("githubEventHasNeedsHumanLabel(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}

// TestHandleGithubIssueEventSDK_NeedsHumanLabelBlocksAdmission is GH-5056's
// primary regression: an issue carrying pilot-needs-human must never be
// (re-)admitted through the SDK-dispatch chokepoint. This drives the real
// function with a real dispatcher/store (mirrors newHandlerTestDispatcher in
// handler_common_test.go) rather than a fake — a fail-when-unwired guard: if
// a future edit moves the check into a helper nothing actually calls, this
// test starts failing because a task row IS created, not because a helper
// unit test alone stops matching.
func TestHandleGithubIssueEventSDK_NeedsHumanLabelBlocksAdmission(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-gh5056-*")
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

	const taskID = "GH-5056"
	const projectPath = "/nonexistent-gh5056-project"

	ev := sdkcore.IssueEvent{
		SequenceID: taskID,
		IssueID:    "5056",
		Title:      "needs-human parked issue",
		Labels:     []string{"pilot", "pilot-needs-human"},
	}

	result, err := handleGithubIssueEventSDK(context.Background(), nil, ev, projectPath, "", dispatcher, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("handleGithubIssueEventSDK() error = %v, want nil", err)
	}
	if result == nil || !result.Skipped {
		t.Fatalf("handleGithubIssueEventSDK() = %+v, want Skipped=true", result)
	}
	if result.SkipReason != "needs_human" {
		t.Errorf("SkipReason = %q, want %q", result.SkipReason, "needs_human")
	}

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

// TestGithubHandlerSDK_NeedsHumanAdmissionCheckWired is a source-level guard
// (mirrors TestGithubHandlerSDK_SpecGuardWired in spec_guard_sdk_test.go):
// the admission check must run inside handleGithubIssueEventSDK's own body,
// before the shared dispatch chokepoint (handleIssueGeneric), not merely
// exist somewhere unreferenced.
func TestGithubHandlerSDK_NeedsHumanAdmissionCheckWired(t *testing.T) {
	body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")

	const checkExpr = "githubEventHasNeedsHumanLabel(ev.Labels)"
	if !strings.Contains(body, checkExpr) {
		t.Fatalf("handleGithubIssueEventSDK must call %s before dispatch (GH-5056)", checkExpr)
	}

	checkIdx := strings.Index(body, checkExpr)
	dispatchIdx := strings.Index(body, "handleIssueGeneric(")
	if dispatchIdx < 0 {
		t.Fatal("handleGithubIssueEventSDK must call handleIssueGeneric to dispatch")
	}
	if checkIdx > dispatchIdx {
		t.Error("handleGithubIssueEventSDK must run the pilot-needs-human admission check before handleIssueGeneric, not after")
	}
}
