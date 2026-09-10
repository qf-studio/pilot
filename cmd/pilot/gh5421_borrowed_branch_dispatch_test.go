package main

import (
	"context"
	"os"
	"strings"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

// TestHandleGithubIssueEventSDK_BorrowedBranch_RecordsRegistryEntry is the
// GH-5421 regression test for dispatch-time recording: when
// resolveAutopilotFixBranch resolves a fix issue's branch to its origin PR's
// branch (the autopilot-meta footer flow), handleGithubIssueEventSDK must
// call runner.RecordBorrowedBranch so the primary SDK OnPRCreated hook
// (githubOnPRCreatedHandler) can later find it. This drives the real
// production function (mirrors the GH-5072/GH-5056 idiom: a nonexistent
// projectPath makes the dispatcher fail cheaply at its preflight git_clean
// check rather than actually running a backend) instead of asserting on a
// re-implementation, so a future edit that stops wiring the call is caught
// here, not just by a source-level grep.
func TestHandleGithubIssueEventSDK_BorrowedBranch_RecordsRegistryEntry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-gh5421-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	runner := executor.NewRunner()
	dispatcher := executor.NewDispatcher(store, runner, nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("dispatcher.Start: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	const taskID = "GH-54210"
	const projectPath = "/nonexistent-gh5421-project"

	ev := sdkcore.IssueEvent{
		SequenceID: taskID,
		IssueID:    "54210",
		Title:      "Fix the failing check",
		// GH-5379 autopilot-meta footer: resolveAutopilotFixBranch resolves
		// this to branch=pilot/GH-54209 (the ORIGIN issue), fromPR=9001 — a
		// borrowed branch, distinct from this task's own default
		// "pilot/GH-54210" branch.
		Body:   "Please fix the failing check.\n\n<!-- autopilot-meta branch:pilot/GH-54209 pr:9001 -->",
		Labels: []string{"pilot"},
	}

	// Dispatch genuinely proceeds past admission (no needs-human label) and
	// fails only once the dispatcher's own preflight can't find projectPath
	// on disk — by then RecordBorrowedBranch has already run, since it sits
	// before task construction/dispatch in handleGithubIssueEventSDK.
	_, _ = handleGithubIssueEventSDK(context.Background(), &config.Config{}, ev, projectPath, "", dispatcher, runner, nil, nil, nil, nil, nil)

	branch, fromPR, ok := runner.BorrowedBranch(taskID)
	if !ok {
		t.Fatalf("runner.BorrowedBranch(%q) recorded no entry, want the autopilot-meta footer's borrowed branch", taskID)
	}
	if branch != "pilot/GH-54209" {
		t.Errorf("BorrowedBranch branch = %q, want %q", branch, "pilot/GH-54209")
	}
	if fromPR != 9001 {
		t.Errorf("BorrowedBranch fromPR = %d, want 9001", fromPR)
	}
}

// TestHandleGithubIssueEventSDK_OwnBranch_NoRegistryEntry is the negative
// control: an issue with no autopilot-meta footer (the common case) resolves
// to its own default "pilot/<taskID>" branch and must NOT record a
// borrowed-branch entry — RecordBorrowedBranch is dispatch-time noise
// otherwise, and would make FixIssueForBranch/BorrowedBranch's "found" case
// meaningless for ordinary issues.
func TestHandleGithubIssueEventSDK_OwnBranch_NoRegistryEntry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-gh5421-b-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	runner := executor.NewRunner()
	dispatcher := executor.NewDispatcher(store, runner, nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("dispatcher.Start: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	const taskID = "GH-54211"
	const projectPath = "/nonexistent-gh5421-project-b"

	ev := sdkcore.IssueEvent{
		SequenceID: taskID,
		IssueID:    "54211",
		Title:      "Implement a normal feature",
		Body:       "Please implement this feature.",
		Labels:     []string{"pilot"},
	}

	_, _ = handleGithubIssueEventSDK(context.Background(), &config.Config{}, ev, projectPath, "", dispatcher, runner, nil, nil, nil, nil, nil)

	if branch, fromPR, ok := runner.BorrowedBranch(taskID); ok {
		t.Errorf("runner.BorrowedBranch(%q) = (%q, %d, true), want ok=false for an issue with no autopilot-meta footer", taskID, branch, fromPR)
	}
}

// TestGithubHandlerSDK_RecordBorrowedBranchWired is a source-level regression
// guard (mirrors TestGithubHandlerSDK_FieldsWired in
// github_sdk_task_fields_test.go), scoped to handleGithubIssueEventSDK's own
// body: the RecordBorrowedBranch call must exist inside this function, not
// merely somewhere unreferenced in the package.
func TestGithubHandlerSDK_RecordBorrowedBranchWired(t *testing.T) {
	body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")

	for _, want := range []string{
		"RecordBorrowedBranch(",
		"fromPR > 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("handleGithubIssueEventSDK body must contain %q (GH-5421: borrowed-branch registry must be populated at dispatch time)", want)
		}
	}
}
