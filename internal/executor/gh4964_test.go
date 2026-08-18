package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// mockGH4964Backend is a scriptable Backend for exercising GH-4964's
// decline-vs-preserve ordering through the real Runner.Execute() path (not
// just the free helper functions). perCall is invoked once per Execute()
// call (1-indexed) and controls what the "model" does that attempt: which
// files it leaves on disk, which pilot-signal it emits, and what it returns.
type mockGH4964Backend struct {
	mu        sync.Mutex
	execCount int
	perCall   func(call int, opts ExecuteOptions) *BackendResult
}

func (m *mockGH4964Backend) Name() string      { return "mock-gh4964" }
func (m *mockGH4964Backend) IsAvailable() bool { return true }

func (m *mockGH4964Backend) Execute(_ context.Context, opts ExecuteOptions) (*BackendResult, error) {
	m.mu.Lock()
	m.execCount++
	call := m.execCount
	m.mu.Unlock()
	if m.perCall == nil {
		return &BackendResult{Success: true}, nil
	}
	return m.perCall(call, opts), nil
}

func (m *mockGH4964Backend) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.execCount
}

// emitSignal simulates the model emitting a pilot-signal block on the
// EventHandler, exactly as the real streaming backend would when it sees a
// ```pilot-signal fenced block in the assistant's text output. Only the
// initial (pre-retry) EventHandler routes text through the signal parser —
// the GH-916 retry's EventHandler only tracks tokens/commit SHAs — so a
// signal emitted on a retry call is intentionally inert; tests that need
// retry-call signal handling must account for this via the runner's actual
// wiring, not assume symmetry with the first call.
func emitSignal(opts ExecuteOptions, jsonBody string) {
	if opts.EventHandler != nil {
		opts.EventHandler(BackendEvent{Type: EventTypeText, Message: "```pilot-signal\n" + jsonBody + "\n```"})
	}
}

// emitStaleSHA simulates a tool_result event that mentions a commit SHA
// which is, in fact, already an ancestor of the base branch — the ghost-SHA
// scenario (GH-3126): the harvester "recovers" a SHA that turns out to be
// stale, e.g. because the model ran `git log` rather than `git commit`.
func emitStaleSHA(opts ExecuteOptions, sha string) {
	if opts.EventHandler != nil {
		opts.EventHandler(BackendEvent{Type: EventTypeToolResult, ToolResult: fmt.Sprintf("[pilot/test %s] wip: stale\n", sha)})
	}
}

func writeUncommittedFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write uncommitted file: %v", err)
	}
}

func newGH4964Task(id, branch, dir string) *Task {
	return &Task{
		ID:          id,
		Title:       "fix(executor): GH-4964 test task",
		Description: "exercise the no_op decline contract",
		ProjectPath: dir,
		Branch:      branch,
		BaseBranch:  "main",
		CreatePR:    true,
	}
}

func newGH4964Runner(backend Backend) *Runner {
	runner := NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	return runner
}

// TestRunner_NoOpSignal_CleanTree_Declines is the core positive case for the
// new opt-in contract: an explicit no_op:true + reason exit signal, with a
// genuinely clean worktree (no commits, nothing uncommitted), declines the
// task without spending a retry.
func TestRunner_NoOpSignal_CleanTree_Declines(t *testing.T) {
	const branch = "pilot/GH-4964-noop-clean"
	dir, _ := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &mockGH4964Backend{
		perCall: func(_ int, opts ExecuteOptions) *BackendResult {
			emitSignal(opts, `{"v":2,"type":"exit","exit_signal":true,"success":true,"no_op":true,"reason":"feature already exists in internal/auth/jwt.go"}`)
			return &BackendResult{Success: true, Output: "no changes needed"}
		},
	}
	runner := newGH4964Runner(backend)
	task := newGH4964Task("GH-4964A", branch, dir)

	result, err := runner.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Declined {
		t.Errorf("expected Declined=true, got result: %+v", result)
	}
	if result.DeclinedReason != "feature already exists in internal/auth/jwt.go" {
		t.Errorf("unexpected DeclinedReason: %q", result.DeclinedReason)
	}
	if result.Success {
		t.Error("expected Success=false for a declined task")
	}
	if backend.callCount() != 1 {
		t.Errorf("expected backend called exactly once (decline skips the retry), got %d", backend.callCount())
	}
}

// TestRunner_NoOpSignal_DirtyTree_PreserveWins is GH-4964's core safety
// guarantee: even when the model explicitly opts into no_op+reason, a dirty
// worktree always wins — real uncommitted diffs contradict any claim that
// nothing needed to change, so the work is auto-preserved instead of the
// task being declined.
func TestRunner_NoOpSignal_DirtyTree_PreserveWins(t *testing.T) {
	const branch = "pilot/GH-4964-noop-dirty"
	dir, bareDir := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &mockGH4964Backend{
		perCall: func(_ int, opts ExecuteOptions) *BackendResult {
			writeUncommittedFile(t, dir, "uncommitted.go")
			emitSignal(opts, `{"v":2,"type":"exit","exit_signal":true,"success":true,"no_op":true,"reason":"nothing to do"}`)
			return &BackendResult{Success: true}
		},
	}
	runner := newGH4964Runner(backend)
	task := newGH4964Task("GH-4964B", branch, dir)

	result, err := runner.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if result.Declined {
		t.Errorf("expected Declined=false — a dirty tree must never be declined, got result: %+v", result)
	}
	if result.Success {
		t.Error("expected Success=false — not a completed classification")
	}
	if !strings.Contains(result.Error, "auto-preserved") {
		t.Errorf("expected result.Error to mention auto-preserved, got: %q", result.Error)
	}
	if result.CommitSHA == "" {
		t.Error("expected a preserved commit SHA")
	}
	if backend.callCount() != 1 {
		t.Errorf("expected backend called exactly once — preserve wins before any retry, got %d", backend.callCount())
	}

	out := gitOutput(t, bareDir, "log", "-1", "--format=%s", "refs/heads/"+branch)
	if !strings.Contains(out, "auto-preserved") {
		t.Errorf("expected pushed commit on origin, got log: %q", out)
	}
}

// TestRunner_BareExitSignal_ZeroCommits_RetriesNotDeclined is the central
// regression guard for GH-4964: the bare mandatory exit signal
// ({"type":"exit","exit_signal":true,"success":true}, with no no_op field)
// must NEVER be treated as decline evidence on its own. A model that
// believes it finished but made no commits (the GH-916 failure class) must
// still go through the no-commit retry, and — if the retry also produces no
// commits — must classify as a generic no_op failure, not a decline.
func TestRunner_BareExitSignal_ZeroCommits_RetriesNotDeclined(t *testing.T) {
	const branch = "pilot/GH-4964-bare-retry"
	dir, _ := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &mockGH4964Backend{
		perCall: func(call int, opts ExecuteOptions) *BackendResult {
			emitSignal(opts, `{"v":2,"type":"exit","exit_signal":true,"success":true}`)
			return &BackendResult{Success: true, Output: fmt.Sprintf("attempt %d, all done", call)}
		},
	}
	runner := newGH4964Runner(backend)
	task := newGH4964Task("GH-4964C", branch, dir)

	result, err := runner.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if result.Declined {
		t.Errorf("expected Declined=false — the bare mandatory exit signal is never decline evidence, got result: %+v", result)
	}
	if result.Success {
		t.Error("expected Success=false — no commits were ever produced")
	}
	if !strings.Contains(result.Error, "no_changes:") {
		t.Errorf("expected generic no_changes classification, got: %q", result.Error)
	}
	if backend.callCount() != 2 {
		t.Errorf("expected backend called twice (initial + GH-916 retry), got %d", backend.callCount())
	}
}

// TestRunner_GhostSHA_NoOpSignal_CleanTree_Declines exercises the earliest
// decline insertion point: a ghost-SHA rejection (the harvested SHA is
// already an ancestor of the base branch — no new commit was actually made)
// combined with a genuinely clean worktree and an explicit no_op+reason
// signal declines immediately, without ever entering the GH-916 retry block.
func TestRunner_GhostSHA_NoOpSignal_CleanTree_Declines(t *testing.T) {
	const branch = "pilot/GH-4964-ghost-noop"
	dir, _ := setupFreshnessRepo(t)
	seedSHA := strings.TrimSpace(gitOutput(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "checkout", "-b", branch)

	backend := &mockGH4964Backend{
		perCall: func(_ int, opts ExecuteOptions) *BackendResult {
			emitStaleSHA(opts, seedSHA)
			emitSignal(opts, `{"v":2,"type":"exit","exit_signal":true,"success":true,"no_op":true,"reason":"already fixed upstream"}`)
			return &BackendResult{Success: true}
		},
	}
	runner := newGH4964Runner(backend)
	task := newGH4964Task("GH-4964D", branch, dir)

	result, err := runner.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Declined {
		t.Errorf("expected Declined=true, got result: %+v", result)
	}
	if result.DeclinedReason != "already fixed upstream" {
		t.Errorf("unexpected DeclinedReason: %q", result.DeclinedReason)
	}
	if backend.callCount() != 1 {
		t.Errorf("expected backend called exactly once — ghost-SHA decline is the earliest exit, no retry, got %d", backend.callCount())
	}
}

// TestRunner_GhostSHA_DirtyTree_PreserveWins verifies the ghost-SHA
// insertion point defers to the GH-4517 preserve backstop exactly like the
// GH-916 insertion points do: a ghost SHA plus a dirty worktree must
// auto-preserve, never decline (even with only the bare signal as
// "evidence").
func TestRunner_GhostSHA_DirtyTree_PreserveWins(t *testing.T) {
	const branch = "pilot/GH-4964-ghost-dirty"
	dir, bareDir := setupFreshnessRepo(t)
	seedSHA := strings.TrimSpace(gitOutput(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "checkout", "-b", branch)

	backend := &mockGH4964Backend{
		perCall: func(_ int, opts ExecuteOptions) *BackendResult {
			writeUncommittedFile(t, dir, "uncommitted.go")
			emitStaleSHA(opts, seedSHA)
			return &BackendResult{Success: true}
		},
	}
	runner := newGH4964Runner(backend)
	task := newGH4964Task("GH-4964E", branch, dir)

	result, err := runner.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if result.Declined {
		t.Errorf("expected Declined=false — dirty tree must preserve, not decline, got result: %+v", result)
	}
	if result.Success {
		t.Error("expected Success=false — not a completed classification")
	}
	if !strings.Contains(result.Error, "auto-preserved") {
		t.Errorf("expected result.Error to mention auto-preserved, got: %q", result.Error)
	}
	if result.CommitSHA == "" || result.CommitSHA == seedSHA {
		t.Errorf("expected a new preserved commit SHA distinct from the ghost SHA, got %q", result.CommitSHA)
	}
	if backend.callCount() != 1 {
		t.Errorf("expected backend called exactly once, got %d", backend.callCount())
	}

	out := gitOutput(t, bareDir, "log", "-1", "--format=%s", "refs/heads/"+branch)
	if !strings.Contains(out, "auto-preserved") {
		t.Errorf("expected pushed commit on origin, got log: %q", out)
	}
}

// TestRunner_DeclinedMarker_DirtyTree_PostRetry_PreserveWins closes the
// latent ordering gap GH-4964 fixes at the post-retry insertion point: if
// the GH-916 retry leaves the worktree dirty AND the model's retry response
// contains an explicit DECLINED:<reason> marker, the dirty tree must still
// win — a real uncommitted diff contradicts any decline claim, regardless of
// which decline evidence produced it.
func TestRunner_DeclinedMarker_DirtyTree_PostRetry_PreserveWins(t *testing.T) {
	const branch = "pilot/GH-4964-declined-dirty-postretry"
	dir, bareDir := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &mockGH4964Backend{
		perCall: func(call int, _ ExecuteOptions) *BackendResult {
			if call == 1 {
				// Initial attempt: clean tree, no commit — triggers the GH-916 retry.
				return &BackendResult{Success: true, Output: "looked at it, nothing yet"}
			}
			// Retry: leaves real uncommitted work on disk AND claims DECLINED.
			writeUncommittedFile(t, dir, "uncommitted.go")
			return &BackendResult{
				Success:           true,
				LastAssistantText: "DECLINED: requirement already satisfied upstream",
			}
		},
	}
	runner := newGH4964Runner(backend)
	task := newGH4964Task("GH-4964F", branch, dir)

	result, err := runner.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if result.Declined {
		t.Errorf("expected Declined=false — dirty tree must preserve, not honor the DECLINED marker, got result: %+v", result)
	}
	if result.Success {
		t.Error("expected Success=false — not a completed classification")
	}
	if !strings.Contains(result.Error, "auto-preserved") {
		t.Errorf("expected result.Error to mention auto-preserved, got: %q", result.Error)
	}
	if result.CommitSHA == "" {
		t.Error("expected a preserved commit SHA")
	}
	if backend.callCount() != 2 {
		t.Errorf("expected backend called twice (initial + GH-916 retry), got %d", backend.callCount())
	}

	out := gitOutput(t, bareDir, "log", "-1", "--format=%s", "refs/heads/"+branch)
	if !strings.Contains(out, "auto-preserved") {
		t.Errorf("expected pushed commit on origin, got log: %q", out)
	}
}
