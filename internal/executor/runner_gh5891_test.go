package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	"github.com/qf-studio/pilot/internal/webhooks"
)

// TestFinalizeCtx_GrantsWindowWhenRemainingShorterThanBudget pins GH-5891: a
// ctx whose deadline is still ahead but closer than the budget gets a fresh
// window that outlives that deadline, instead of a child that dies mid-step.
func TestFinalizeCtx_GrantsWindowWhenRemainingShorterThanBudget(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), time.Second)
	defer parentCancel()

	fresh, cancel := finalizeCtx(parent, 5*time.Minute)
	defer cancel()

	<-parent.Done()
	if parent.Err() != context.DeadlineExceeded {
		t.Fatalf("test setup: parent should have hit its deadline, got %v", parent.Err())
	}
	if fresh.Err() != nil {
		t.Errorf("the window must outlive the parent's deadline, got Err()=%v", fresh.Err())
	}
	if deadline, ok := fresh.Deadline(); !ok || time.Until(deadline) < 4*time.Minute {
		t.Errorf("expected a fresh ~5m deadline, got ok=%v remaining=%v", ok, time.Until(deadline))
	}
}

// TestFinalizeCtx_CancelDuringWindowStops pins GH-5891: a real cancellation
// that arrives while the fresh window is open still stops the step.
func TestFinalizeCtx_CancelDuringWindowStops(t *testing.T) {
	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer deadlineCancel()
	parent, parentCancel := context.WithCancel(deadlineCtx)
	defer parentCancel()

	fresh, cancel := finalizeCtx(parent, 5*time.Minute)
	defer cancel()
	if fresh.Err() != nil {
		t.Fatalf("test setup: the window should start live, got %v", fresh.Err())
	}

	parentCancel()
	select {
	case <-fresh.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a cancel during the fresh window must end it")
	}
	if fresh.Err() != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", fresh.Err())
	}
}

// TestFinalizeCtx_NoDeadlinePassesThrough: a ctx with no deadline has nothing
// to run short of, so it is only wrapped.
func TestFinalizeCtx_NoDeadlinePassesThrough(t *testing.T) {
	derived, cancel := finalizeCtx(context.Background(), time.Minute)
	defer cancel()
	if _, ok := derived.Deadline(); ok {
		t.Error("a ctx without a deadline must not gain one")
	}
}

// recordingLearningStore is a fake LearningRecorder that notes the ctx state it
// was called with.
type recordingLearningStore struct {
	mu     sync.Mutex
	calls  int
	ctxErr error
}

func (s *recordingLearningStore) RecordExecution(ctx context.Context, _ *memory.Execution, _ []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.ctxErr = ctx.Err()
	return nil
}

// TestExecute_PostPRTailSurvivesDeadlineBlownDuringEvidence pins GH-5891: the
// finalize window re-derived after the evidence step also covers the
// task-completed webhook and the learning record, so neither is lost when the
// task deadline blew during evidence.
func TestExecute_PostPRTailSurvivesDeadlineBlownDuringEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end: real git/gh, skipped under -short")
	}
	const branch = "pilot/GH-5891-tail"
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
	// Shorter than the 3s task deadline below, so the window derived before
	// the evidence step is a plain child of the task ctx and dies with it —
	// only the window re-derived after evidence is live for the tail.
	runner.config.FinalizeTimeout = time.Second

	var mu sync.Mutex
	var events []string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		events = append(events, req.Header.Get("X-Pilot-Event"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()
	whConfig := webhooks.DefaultConfig()
	whConfig.Enabled = true
	whConfig.Endpoints = []*webhooks.EndpointConfig{{
		ID:      "ep_gh5891",
		Name:    "gh5891 sink",
		URL:     sink.URL,
		Secret:  testutil.FakeWebhookSecret,
		Events:  []webhooks.EventType{webhooks.EventTaskCompleted},
		Enabled: true,
	}}
	runner.SetWebhookManager(webhooks.NewManager(whConfig, nil))

	learning := &recordingLearningStore{}
	runner.SetLearningLoop(learning)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	runner.acceptanceRunner = slowUntilEvidenceRunner{until: deadline.Add(300 * time.Millisecond)}

	creator := &deadlineProbePRCreator{}
	runner.SetPRCreator(creator)

	task := newGH4964Task("GH-5891", branch, dir)
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
	if !result.Success || result.PRUrl == "" {
		t.Fatalf("expected a created PR, got success=%v url=%q error=%q", result.Success, result.PRUrl, result.Error)
	}

	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()
	if len(got) != 1 || got[0] != string(webhooks.EventTaskCompleted) {
		t.Errorf("the task-completed webhook must reach the sink after the deadline blew, got %v", got)
	}
	if learning.calls != 1 {
		t.Fatalf("expected one learning record, got %d", learning.calls)
	}
	if learning.ctxErr != nil {
		t.Errorf("the learning record must run on a live ctx, got Err()=%v", learning.ctxErr)
	}
}
