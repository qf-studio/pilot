package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
)

// newGitlabIssueLabelServer returns a fake GitLab API server that answers the
// GetIssue/ListIssues/AddIssueLabels/RemoveIssueLabel calls processIssueAsync
// makes for a single issue — enough to drive it end-to-end without a real
// GitLab instance.
func newGitlabIssueLabelServer(t *testing.T, issue *Issue) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, fmt.Sprintf("/issues/%d", issue.IID)) {
			_ = json.NewEncoder(w).Encode(issue)
			return
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]*Issue{issue})
			return
		}
		// PUT (AddIssueLabels/RemoveIssueLabel) or POST (AddIssueNote) — any 2xx is fine, the
		// label-mutation callers here don't read the response body.
		_ = json.NewEncoder(w).Encode(issue)
	}))
	t.Cleanup(server.Close)
	return server
}

// dispatchOneIssueSync mimics the semaphore/waitgroup bookkeeping
// checkForNewIssues' parallel-dispatch loop performs before spawning
// processIssueAsync as a goroutine (poller.go's dispatchIssue path), but
// invokes it synchronously so the test can assert on state immediately
// after it returns instead of needing WaitForActive().
func dispatchOneIssueSync(p *Poller, issue *Issue) {
	p.semaphore <- struct{}{}
	p.activeWg.Add(1)
	p.processIssueAsync(context.Background(), issue)
}

// TestProcessIssueAsync_LiveExecutionOwner_DoesNotUnmarkForRetry is the
// GH-4587 regression test for internal/adapters/gitlab/poller.go's "Execution
// failed without MR, unmarking for retry" branch: when the task_id derived
// from the issue (GL-<iid>) has a live (non-terminal) execution owner per the
// real execution_claims + executions ledger, a failed-looking IssueResult
// (Success=false, MRNumber=0 — indistinguishable from a genuine failure from
// this callback's return value alone) must NOT be unmarked for retry. Doing
// so would re-offer a task another channel/generation is still actively
// working.
func TestProcessIssueAsync_LiveExecutionOwner_DoesNotUnmarkForRetry(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	issue := &Issue{IID: 4587, Title: "live owner", CreatedAt: time.Now()}
	taskID := fmt.Sprintf("GL-%d", issue.IID)
	projectPath := "/tmp/pilot-gh-4587-gitlab-live-owner-does-not-exist"

	seed := &executor.Task{ID: taskID, ProjectPath: projectPath}
	if _, err := executor.NewExecutionLifecycle(store).Begin(seed, executor.ExecStatusRunning); err != nil {
		t.Fatalf("setup Begin: %v", err)
	}

	server := newGitlabIssueLabelServer(t, issue)
	client := NewClientWithBaseURL(testutil.FakeGitHubToken, "namespace/project", server.URL)

	poller := NewPoller(client, "pilot", 30*time.Second,
		WithOnIssueWithResult(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			return &IssueResult{Success: false, MRNumber: 0}, nil
		}),
		WithStore(store),
		WithProjectPath(projectPath),
	)
	poller.markProcessed(issue.IID)

	dispatchOneIssueSync(poller, issue)

	if !poller.IsProcessed(issue.IID) {
		t.Error("expected the issue to remain marked processed — a live execution owner exists, " +
			"so this must not be unmarked for retry")
	}
}

// TestProcessIssueAsync_NoLiveOwner_StillUnmarksForRetry is the control case
// for TestProcessIssueAsync_LiveExecutionOwner_DoesNotUnmarkForRetry: with no
// store wired at all (the pre-GH-4587 shape, and the common case for
// deployments that never call WithStore), a failed-looking IssueResult must
// still unmark the issue for retry exactly as before — the new ledger check
// only suppresses the unmark when it has evidence of a live owner, it never
// changes the genuine-failure path.
func TestProcessIssueAsync_NoLiveOwner_StillUnmarksForRetry(t *testing.T) {
	issue := &Issue{IID: 4588, Title: "no store wired", CreatedAt: time.Now()}

	server := newGitlabIssueLabelServer(t, issue)
	client := NewClientWithBaseURL(testutil.FakeGitHubToken, "namespace/project", server.URL)

	poller := NewPoller(client, "pilot", 30*time.Second,
		WithOnIssueWithResult(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			return &IssueResult{Success: false, MRNumber: 0}, nil
		}),
	)
	poller.markProcessed(issue.IID)

	dispatchOneIssueSync(poller, issue)

	if poller.IsProcessed(issue.IID) {
		t.Error("expected the issue to be unmarked for retry — no store is wired, so there is no ledger " +
			"evidence of a live owner, and the pre-GH-4587 behavior must be preserved")
	}
}

// TestProcessIssueAsync_StoreWiredButTaskDone_StillUnmarksForRetry verifies
// the ledger check is genuinely status-aware, not just "does a store exist":
// a task whose only execution row is already terminal (failed) has no live
// owner, so a failed-looking IssueResult must still unmark for retry even
// though a store IS wired.
func TestProcessIssueAsync_StoreWiredButTaskDone_StillUnmarksForRetry(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	issue := &Issue{IID: 4589, Title: "terminal owner", CreatedAt: time.Now()}
	taskID := fmt.Sprintf("GL-%d", issue.IID)
	projectPath := "/tmp/pilot-gh-4587-gitlab-terminal-owner-does-not-exist"

	seed := &executor.Task{ID: taskID, ProjectPath: projectPath}
	execID, err := executor.NewExecutionLifecycle(store).Begin(seed, executor.ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "failed"); err != nil {
		t.Fatalf("setup: failed to mark seed execution failed: %v", err)
	}

	server := newGitlabIssueLabelServer(t, issue)
	client := NewClientWithBaseURL(testutil.FakeGitHubToken, "namespace/project", server.URL)

	poller := NewPoller(client, "pilot", 30*time.Second,
		WithOnIssueWithResult(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			return &IssueResult{Success: false, MRNumber: 0}, nil
		}),
		WithStore(store),
		WithProjectPath(projectPath),
	)
	poller.markProcessed(issue.IID)

	dispatchOneIssueSync(poller, issue)

	if poller.IsProcessed(issue.IID) {
		t.Error("expected the issue to be unmarked for retry — the only execution row is terminal " +
			"(failed), so there is no live owner despite a store being wired")
	}
}
