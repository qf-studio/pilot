package executor

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

type decliningBackend struct {
	calls atomic.Int32
}

func (b *decliningBackend) Name() string      { return "declining" }
func (b *decliningBackend) IsAvailable() bool { return true }

func (b *decliningBackend) Execute(_ context.Context, _ ExecuteOptions) (*BackendResult, error) {
	b.calls.Add(1)
	return &BackendResult{
		Success:           true,
		Output:            "analysis complete\nDECLINED: the requested feature already exists in internal/auth/jwt.go",
		LastAssistantText: "analysis complete\nDECLINED: the requested feature already exists in internal/auth/jwt.go",
	}, nil
}

type ghostSHANoOpBackend struct {
	baseSHA string
	calls   atomic.Int32
}

func (b *ghostSHANoOpBackend) Name() string      { return "ghost-sha-noop" }
func (b *ghostSHANoOpBackend) IsAvailable() bool { return true }

func (b *ghostSHANoOpBackend) Execute(_ context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.calls.Add(1)
	if opts.EventHandler != nil {
		opts.EventHandler(BackendEvent{
			Type:       EventTypeToolResult,
			ToolResult: "[main " + b.baseSHA[:7] + "] chore: prior commit inspected",
		})
		opts.EventHandler(BackendEvent{
			Type:    EventTypeText,
			Message: "Checked the watch ticket. No state change upstream.\n{\"v\":2,\"type\":\"exit\",\"exit_signal\":true,\"success\":true,\"reason\":\"watch ticket unchanged, no commit needed\"}",
		})
	}
	return &BackendResult{
		Success:           true,
		Output:            "No commit made — correct outcome for a watch ticket with no state change.",
		LastAssistantText: "No commit made — correct outcome for a watch ticket with no state change.",
	}, nil
}

func TestRunner_GhostSHANoOpWithExitSignal_ClassifiesAsDecline(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	backend := &ghostSHANoOpBackend{baseSHA: headSHA(t, localRepo)}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: false}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)

	task := &Task{
		ID:          "GH-4875-3",
		Title:       "check a watch-only ticket via ghost-SHA path",
		Description: "harvested SHA is the base HEAD; model signals a bare exit success",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-4875-3",
		CreatePR:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result == nil || !result.Declined {
		t.Fatalf("expected declined result on the ghost-SHA no-op path, got %+v", result)
	}
	if result.Success {
		t.Error("declined result must not report Success")
	}
	if result.Error != "" {
		t.Errorf("declined result must not carry the ghost-SHA failure Error, got %q", result.Error)
	}
	if result.DeclinedReason != "watch ticket unchanged, no commit needed" {
		t.Errorf("DeclinedReason = %q, want the bare exit signal's reason", result.DeclinedReason)
	}
	if got := backend.calls.Load(); got != 1 {
		t.Errorf("backend invoked %d times, want 1", got)
	}
}

type exitSignalNoOpBackend struct {
	calls atomic.Int32
}

func (b *exitSignalNoOpBackend) Name() string      { return "exit-signal-noop" }
func (b *exitSignalNoOpBackend) IsAvailable() bool { return true }

func (b *exitSignalNoOpBackend) Execute(_ context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.calls.Add(1)
	if opts.EventHandler != nil {
		opts.EventHandler(BackendEvent{
			Type:    EventTypeText,
			Message: "NO-OP RATIONALE: watch-only ticket, trigger unmet.\n```pilot-signal\n{\"v\":2,\"type\":\"exit\",\"exit_signal\":true,\"success\":true,\"reason\":\"watch-only ticket, no code change required\"}\n```",
		})
	}
	return &BackendResult{
		Success:           true,
		Output:            "checked the upstream issue; unchanged, nothing to do",
		LastAssistantText: "checked the upstream issue; unchanged, nothing to do",
	}, nil
}

func TestRunner_ExitSignalSuccessNoCommit_ClassifiesAsDecline(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	backend := &exitSignalNoOpBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: false}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)

	task := &Task{
		ID:          "GH-4875-2",
		Title:       "check a watch-only ticket",
		Description: "no code change expected; executor signals success with no commit",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-4875-2",
		CreatePR:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result == nil || !result.Declined {
		t.Fatalf("expected declined result for exit-signal no-op, got %+v", result)
	}
	if result.Success {
		t.Error("declined result must not report Success")
	}
	if result.Error != "" {
		t.Errorf("declined result must not carry a failure Error, got %q", result.Error)
	}
	if result.DeclinedReason != "watch-only ticket, no code change required" {
		t.Errorf("DeclinedReason = %q, want the exit signal's reason", result.DeclinedReason)
	}
	if got := backend.calls.Load(); got != 1 {
		t.Errorf("backend invoked %d times, want 1 — a signalled no-op must not trigger the no-commit retry", got)
	}
}

func TestRunner_FirstPassDecline_SkipsNoCommitRetry(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	backend := &decliningBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: false}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)

	task := &Task{
		ID:          "GH-4875-1",
		Title:       "add auth module",
		Description: "already exists; executor declines on the first pass",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-4875-1",
		CreatePR:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result == nil || !result.Declined {
		t.Fatalf("expected declined result, got %+v", result)
	}
	if result.Success {
		t.Error("declined result must not report Success")
	}
	if result.Outcome != "declined" {
		t.Errorf("Outcome = %q, want \"declined\"", result.Outcome)
	}
	if result.DeclinedReason == "" {
		t.Error("DeclinedReason must carry the DECLINED: text")
	}
	if got := backend.calls.Load(); got != 1 {
		t.Errorf("backend invoked %d times, want 1 — a first-pass decline must not trigger the no-commit retry", got)
	}
}
