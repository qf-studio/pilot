package executor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// deadlineProbePRCreator records the ctx state CreatePR is called with.
type deadlineProbePRCreator struct {
	mu          sync.Mutex
	calls       int
	ctxErr      error
	hasDeadline bool
	remaining   time.Duration
	err         error
}

func (p *deadlineProbePRCreator) CreatePR(ctx context.Context, _, _, _, _ string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.ctxErr = ctx.Err()
	if dl, ok := ctx.Deadline(); ok {
		p.hasDeadline = true
		p.remaining = time.Until(dl)
	}
	if p.err != nil {
		return "", p.err
	}
	return "https://example.test/pr/1", nil
}

// slowUntilEvidenceRunner stands in for a paste-output command that outlives
// the task deadline: it sleeps until the given instant has passed.
type slowUntilEvidenceRunner struct{ until time.Time }

func (s slowUntilEvidenceRunner) RunCommand(_ context.Context, _, _ string) (string, error) {
	time.Sleep(time.Until(s.until))
	return "evidence output", nil
}

// TestExecute_PRCreateSurvivesDeadlineBlownDuringEvidence pins GH-5867: when
// the task deadline blows while the acceptance-evidence step runs, PR creation
// (and the recovery ref when PR creation fails for another reason) must still
// get a live finalize window instead of failing instantly on the dead ctx.
func TestExecute_PRCreateSurvivesDeadlineBlownDuringEvidence(t *testing.T) {
	run := func(t *testing.T, createErr error) (*ExecutionResult, *deadlineProbePRCreator) {
		t.Helper()
		const branch = "pilot/GH-5867-evidence"
		dir, _ := setupFreshnessRepo(t)
		runGit(t, dir, "checkout", "-b", branch)

		backend := &mockGH4964Backend{
			perCall: func(_ int, _ ExecuteOptions) *BackendResult {
				writeUncommittedFile(t, dir, "real-work.go")
				runGit(t, dir, "add", "real-work.go")
				runGit(t, dir, "commit", "-m", "real work")
				return &BackendResult{Success: true, Output: "done"}
			},
		}
		runner := newGH4964Runner(backend)

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		deadline, _ := ctx.Deadline()
		runner.acceptanceRunner = slowUntilEvidenceRunner{until: deadline.Add(300 * time.Millisecond)}

		creator := &deadlineProbePRCreator{err: createErr}
		runner.SetPRCreator(creator)

		task := newGH4964Task("GH-5867", branch, dir)
		task.SkipQualityGates = true
		task.SourceAdapter = "linear" // routes PR creation through the shared PRCreator slot
		task.AcceptanceCriteria = []string{"paste the output of `go test ./a/` into the PR body"}

		result, err := runner.Execute(ctx, task)
		if err != nil {
			t.Fatalf("Execute() returned error: %v", err)
		}
		if ctx.Err() == nil {
			t.Fatal("test setup: the task deadline should have blown during evidence")
		}
		return result, creator
	}

	t.Run("PR is created on a live ctx", func(t *testing.T) {
		result, creator := run(t, nil)
		if !result.Success || result.PRUrl == "" {
			t.Fatalf("expected a created PR, got success=%v url=%q error=%q", result.Success, result.PRUrl, result.Error)
		}
		if creator.calls != 1 {
			t.Fatalf("expected one CreatePR call, got %d", creator.calls)
		}
		if creator.ctxErr != nil || !creator.hasDeadline || creator.remaining < time.Minute {
			t.Errorf("CreatePR must see a live ctx with a fresh deadline: err=%v hasDeadline=%v remaining=%v",
				creator.ctxErr, creator.hasDeadline, creator.remaining)
		}
	})

	t.Run("recovery ref is created when PR creation fails", func(t *testing.T) {
		result, creator := run(t, errors.New("boom: api unavailable"))
		if result.Success {
			t.Fatal("expected a failure when PR creation fails")
		}
		if creator.calls != prCreateRetryAttempts {
			t.Errorf("expected %d CreatePR attempts, got %d", prCreateRetryAttempts, creator.calls)
		}
		if creator.ctxErr != nil {
			t.Errorf("CreatePR saw a dead ctx: %v", creator.ctxErr)
		}
		if !strings.Contains(result.Error, "recovery_ref=refs/pilot-recovery/") || strings.Contains(result.Error, "recovery ref also failed") {
			t.Errorf("expected the recovery ref to be created on a live ctx, got: %s", result.Error)
		}
	})
}

// TestExecute_PRCreateSurvivesDeadline_RecoveryRefOnExpiredCtx: the recovery
// ref helper re-derives its own window, so it works on an already-expired ctx
// but does not extend a real cancellation (GH-5342).
func TestExecute_PRCreateSurvivesDeadline_RecoveryRefOnExpiredCtx(t *testing.T) {
	dir := initRepoWithRemoteAndFeatureBranch(t, "pilot/GH-5867-recovery")
	git := NewGitOperations(dir)

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()
	msg := formatGitStepFailureWithRecovery(expired, git, "pr-create", 3, errors.New("boom"), "GH-5867", "pilot/GH-5867-recovery", "abcdef1234567")
	if !strings.Contains(msg, "recovery_ref=") || strings.Contains(msg, "recovery ref also failed") {
		t.Errorf("expected a recovery ref on an expired ctx, got: %s", msg)
	}

	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	msg = formatGitStepFailureWithRecovery(canceled, git, "pr-create", 3, errors.New("boom"), "GH-5867", "pilot/GH-5867-recovery", "abcdef1234567")
	if !strings.Contains(msg, "recovery ref also failed") {
		t.Errorf("a real cancellation must not be extended, got: %s", msg)
	}
}
