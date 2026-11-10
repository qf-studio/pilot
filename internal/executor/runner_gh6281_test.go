package executor

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// heldBranchFixture is a local clone whose origin carries main and a branch a
// pre-PR hold pushed: one commit past main that main does not contain.
type heldBranchFixture struct {
	local, remote string
	branch        string
	mainSHA       string
	holdSHA       string
}

func newHeldBranchFixture(t *testing.T, branch string) *heldBranchFixture {
	t.Helper()
	local, remote := setupTestRepoWithRemote(t)
	t.Cleanup(func() {
		_ = os.RemoveAll(local)
		_ = os.RemoveAll(remote)
	})
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", local}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	f := &heldBranchFixture{local: local, remote: remote, branch: branch, mainSHA: git("rev-parse", "HEAD")}
	git("checkout", "-b", "held-tmp")
	if err := os.WriteFile(filepath.Join(local, "held.txt"), []byte("held work"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "held work")
	f.holdSHA = git("rev-parse", "HEAD")
	git("push", "origin", "held-tmp:refs/heads/"+branch)
	git("checkout", "main")
	git("branch", "-D", "held-tmp")
	if f.holdSHA == f.mainSHA {
		t.Fatal("test setup: the held commit equals main")
	}
	return f
}

// logBuffer is a goroutine-safe sink for a runner's slog output.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

const (
	holdContinuationResolvedLine   = "Resolved pre-PR hold continuation worktree base"
	holdContinuationUnresolvedLine = "held branch not resolvable on origin"
)

// TestHoldContinuation_QueuePickupWorktreeBase pins GH-6281 through the real
// queue pickup: the row is saved by Begin, then the project worker reads it back
// with GetQueuedTasksForProject, rebuilds the Task and runs it. A pickup query
// that drops continue_branch_sha cuts the worktree from main (GH-6272, GH-6267).
func TestHoldContinuation_QueuePickupWorktreeBase(t *testing.T) {
	const taskID = "GH-6281"
	f := newHeldBranchFixture(t, "pilot/"+taskID)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	queued := &Task{
		ID:                taskID,
		Title:             "retry after a pre-PR hold",
		Description:       "continue the held branch",
		ProjectPath:       f.local,
		Branch:            f.branch,
		CreatePR:          true,
		ContinueBranchSHA: f.holdSHA,
	}
	execID, err := NewExecutionLifecycle(store).Begin(queued, ExecStatusQueued, 0)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false}, nil
	})
	origMergedPR := mergedPRPreflightCheck
	mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	t.Cleanup(func() { mergedPRPreflightCheck = origMergedPR })

	backend := &headRecordingBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: boolPtr(true)}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	logs := &logBuffer{}
	runner.log = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	worker := NewProjectWorker(f.local, store, runner, slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	worker.processQueue(ctx)

	backend.mu.Lock()
	head := backend.head
	backend.mu.Unlock()
	if head == "" {
		t.Fatalf("the backend never ran; execution row: %+v, logs:\n%s", mustExecution(t, store, execID), logs.String())
	}
	if head != f.holdSHA {
		t.Errorf("worktree HEAD = %q, want the hold sha %q (main is %q)", head, f.holdSHA, f.mainSHA)
	}
	if got := logs.String(); strings.Count(got, holdContinuationResolvedLine) != 1 || strings.Contains(got, holdContinuationUnresolvedLine) {
		t.Errorf("want exactly the %q line, got logs:\n%s", holdContinuationResolvedLine, got)
	}
}

func mustExecution(t *testing.T, store *memory.Store, id string) *memory.Execution {
	t.Helper()
	exec, err := store.GetExecution(id)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	return exec
}

// TestHoldContinuation_LogsExactlyOneOutcome pins GH-6281 item 2: a task with a
// continuation SHA logs one of the two outcome lines before the worktree exists.
func TestHoldContinuation_LogsExactlyOneOutcome(t *testing.T) {
	const taskID = "GH-6282"
	tests := []struct {
		name       string
		branch     func(f *heldBranchFixture) // prepares origin
		sha        func(f *heldBranchFixture) string
		wantLine   string
		wantAbsent string
		wantHead   func(f *heldBranchFixture) string
	}{
		{
			name:       "held branch on origin",
			branch:     func(*heldBranchFixture) {},
			sha:        func(f *heldBranchFixture) string { return f.holdSHA },
			wantLine:   holdContinuationResolvedLine,
			wantAbsent: holdContinuationUnresolvedLine,
			wantHead:   func(f *heldBranchFixture) string { return f.holdSHA },
		},
		{
			name: "held branch gone and sha unfetchable",
			branch: func(f *heldBranchFixture) {
				if out, err := exec.Command("git", "-C", f.local, "push", "origin", "--delete", f.branch).CombinedOutput(); err != nil {
					t.Fatalf("delete remote branch: %v\n%s", err, out)
				}
			},
			sha:        func(*heldBranchFixture) string { return "0123456789abcdef0123456789abcdef01234567" },
			wantLine:   holdContinuationUnresolvedLine,
			wantAbsent: holdContinuationResolvedLine,
			wantHead:   func(f *heldBranchFixture) string { return f.mainSHA },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHeldBranchFixture(t, "pilot/"+taskID)
			tt.branch(f)

			backend := &headRecordingBackend{}
			runner := NewRunnerWithBackend(backend)
			runner.config = &BackendConfig{UseWorktree: boolPtr(true)}
			runner.SetSkipPreflightChecks(true)
			runner.SetRecordingEnabled(false)
			logs := &logBuffer{}
			runner.log = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

			task := &Task{
				ID: taskID, Title: "t", Description: "d", ProjectPath: f.local, Branch: f.branch,
				CreatePR: true, ContinueBranchSHA: tt.sha(f),
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_, _ = runner.Execute(ctx, task)

			got := logs.String()
			if strings.Count(got, tt.wantLine) != 1 || strings.Contains(got, tt.wantAbsent) {
				t.Errorf("want exactly one %q line and no %q line, got logs:\n%s", tt.wantLine, tt.wantAbsent, got)
			}
			backend.mu.Lock()
			head := backend.head
			backend.mu.Unlock()
			if want := tt.wantHead(f); head != want {
				t.Errorf("worktree HEAD = %q, want %q", head, want)
			}
		})
	}
}

// TestHoldContinuation_DefaultBranchStartReplacesStaleHead pins GH-6281 item 3:
// a run that starts from the default branch while origin holds a head from an
// earlier hold (hold comment superseded, no continuation SHA) records that head
// at worktree creation, so the final push leases on it instead of failing
// non-fast-forward after the whole run.
func TestHoldContinuation_DefaultBranchStartReplacesStaleHead(t *testing.T) {
	const taskID = "GH-6283"
	f := newHeldBranchFixture(t, "pilot/"+taskID)
	setUpFakeGhPATH(t, []byte(`[]`), []byte(`[]`))

	backend := &commitAndRecordBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: boolPtr(true), SkipSelfReview: true}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)

	task := &Task{
		ID: taskID, Title: "redo after a superseded hold", Description: "d",
		ProjectPath: f.local, Branch: f.branch, CreatePR: true, // no ContinueBranchSHA
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	result, _ := runner.Execute(ctx, task)

	if result != nil && strings.Contains(result.Error, nonFastForwardTaskBranchMarker) {
		t.Fatalf("the final push failed non-fast-forward: %s", result.Error)
	}
	out, err := exec.Command("git", "-C", f.remote, "rev-parse", "refs/heads/"+f.branch).CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse remote branch: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got == f.holdSHA {
		t.Errorf("origin %s still at the held head %s: the run's work was never pushed (result error: %s)", f.branch, got, resultError(result))
	}
	// The replaced head stays recoverable: the lease was taken once, on that SHA.
	if task.inheritedHeadSHA != "" {
		t.Errorf("inheritedHeadSHA = %q, want it cleared by the one lease push", task.inheritedHeadSHA)
	}
}

func resultError(r *ExecutionResult) string {
	if r == nil {
		return "<nil result>"
	}
	return r.Error
}

// TestHoldContinuation_RecordReplaceableBranchHead covers the decision's edges:
// no remote branch, an autopilot-fix continuation and an already recorded head
// are all left alone.
func TestHoldContinuation_RecordReplaceableBranchHead(t *testing.T) {
	f := newHeldBranchFixture(t, "pilot/GH-6284")
	tests := []struct {
		name string
		task Task
		want string
	}{
		{"remote branch with a foreign head", Task{ID: "GH-6284", Branch: f.branch}, f.holdSHA},
		{"no such branch on origin", Task{ID: "GH-6285", Branch: "pilot/GH-6285"}, ""},
		{"autopilot-fix continuation", Task{ID: "GH-6284", Branch: f.branch, FromPR: 7}, ""},
		{"fix SHA recorded", Task{ID: "GH-6284", Branch: f.branch, FixFromSHA: f.mainSHA}, ""},
		{"dispatcher already recorded a head", Task{ID: "GH-6284", Branch: f.branch, inheritedHeadSHA: f.mainSHA}, f.mainSHA},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := tt.task
			task.ProjectPath = f.local
			NewRunnerWithBackend(&mockFixedBackend{}).recordReplaceableBranchHead(context.Background(), &task)
			if task.inheritedHeadSHA != tt.want {
				t.Errorf("inheritedHeadSHA = %q, want %q", task.inheritedHeadSHA, tt.want)
			}
		})
	}
}

// TestHoldContinuation_NonFastForwardErrorSurvivesRestartBackfill pins GH-6281
// item 5 across packages: the error a rejected push really produces keeps its
// "failed" status through the startup backfill in internal/memory, whose SQL
// matches the marker as a literal.
func TestHoldContinuation_NonFastForwardErrorSurvivesRestartBackfill(t *testing.T) {
	s := newPushScenario(t, "pilot/GH-6281-c", false)
	result := s.run(t, "GH-6281-c", 0, "")
	if result.Success || !strings.Contains(result.Error, nonFastForwardTaskBranchMarker) {
		t.Fatalf("setup: want the non-fast-forward escalation, got success=%v error=%q", result.Success, result.Error)
	}
	if got := TerminalStatus(result); got != "failed" {
		t.Fatalf("TerminalStatus = %q, want failed", got)
	}

	dir := t.TempDir()
	store, err := memory.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.SaveExecution(&memory.Execution{ID: "e1", TaskID: "GH-6281-c", ProjectPath: "/p", Status: "failed", Error: result.Error}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	_ = store.Close()

	store, err = memory.NewStore(dir) // a daemon restart: migrate() runs the backfill
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = store.Close() }()
	got, err := store.GetExecution("e1")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("status after restart = %q, want failed (not infra)", got.Status)
	}
}
