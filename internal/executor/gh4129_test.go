package executor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestDirectPath_ClaudeStartedAndImplementationStartedEvents covers GH-4129
// requirement (1): the direct (non-epic) path must emit StageClaudeStarted
// and StageImplementationStarted right before the real Claude invocation,
// mirroring the epic path's :1919 pattern — previously the direct-path
// execution_events timeline went silent after spec_validated.
func TestDirectPath_ClaudeStartedAndImplementationStartedEvents(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	backend := &mockSelfReviewBackend{output: "done"}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{}
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.SetLogStore(store)

	task := &Task{
		ID:          "GH-4129-DIRECT",
		Title:       "Direct path stage event test",
		Description: "Verify claude_started/implementation_started fire on the direct path",
		ProjectPath: t.TempDir(),
		LocalMode:   true,
	}
	if err := store.SaveExecution(&memory.Execution{ID: task.LogExecutionID(), TaskID: task.ID, Status: "running"}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() not successful: %s", result.Error)
	}

	events, err := store.ListExecutionEvents(task.LogExecutionID())
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}

	var gotStages []memory.Stage
	for _, e := range events {
		gotStages = append(gotStages, e.Stage)
	}

	wantOrder := []memory.Stage{memory.StageSpecValidated, memory.StageClaudeStarted, memory.StageImplementationStarted}
	idx := 0
	for _, want := range wantOrder {
		found := false
		for ; idx < len(gotStages); idx++ {
			if gotStages[idx] == want {
				found = true
				idx++
				break
			}
		}
		if !found {
			t.Fatalf("expected stage %q in order within %v, not found", want, gotStages)
		}
	}
}

// TestRecordQualityGateEvents_PerGateAndSummary covers GH-4129 requirement
// (2): quality.CheckResults per-gate Duration and overall TotalDuration
// (mirrored on the executor side as QualityOutcome.GateDetails[].Duration
// and QualityOutcome.TotalDuration — internal/executor/quality.go) must be
// persisted as execution_events using a detail-JSON convention, since the
// values were previously computed but transient.
func TestRecordQualityGateEvents_PerGateAndSummary(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	runner.SetLogStore(store)

	if err := store.SaveExecution(&memory.Execution{ID: "exec-gh4129-qg", TaskID: "GH-4129-QG", Status: "running"}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	outcome := &QualityOutcome{
		Passed:        true,
		TotalDuration: 5500 * time.Millisecond,
		GateDetails: []QualityGateDetail{
			{Name: "build", Passed: true, Duration: 2 * time.Second, RetryCount: 0},
			{Name: "test", Passed: true, Duration: 3500 * time.Millisecond, RetryCount: 1},
		},
	}

	runner.recordQualityGateEvents("exec-gh4129-qg", outcome)

	events, err := store.ListExecutionEvents("exec-gh4129-qg")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3 (2 gates + 1 summary)", len(events))
	}

	for _, e := range events {
		if e.Stage != memory.StageQualityGate {
			t.Errorf("event.Stage = %q, want %q", e.Stage, memory.StageQualityGate)
		}
	}

	var gate0 struct {
		Gate       string `json:"gate"`
		DurationMS int64  `json:"duration_ms"`
		Passed     bool   `json:"passed"`
		RetryCount int    `json:"retry_count"`
	}
	if err := json.Unmarshal([]byte(events[0].Detail), &gate0); err != nil {
		t.Fatalf("failed to unmarshal gate detail: %v", err)
	}
	if gate0.Gate != "build" || gate0.DurationMS != 2000 || !gate0.Passed {
		t.Errorf("gate0 = %+v, want build/2000ms/passed", gate0)
	}

	var gate1 struct {
		Gate       string `json:"gate"`
		DurationMS int64  `json:"duration_ms"`
		RetryCount int    `json:"retry_count"`
	}
	if err := json.Unmarshal([]byte(events[1].Detail), &gate1); err != nil {
		t.Fatalf("failed to unmarshal gate detail: %v", err)
	}
	if gate1.Gate != "test" || gate1.DurationMS != 3500 || gate1.RetryCount != 1 {
		t.Errorf("gate1 = %+v, want test/3500ms/retry_count=1", gate1)
	}

	var summary struct {
		TotalDurationMS int64 `json:"total_duration_ms"`
		GateCount       int   `json:"gate_count"`
	}
	if err := json.Unmarshal([]byte(events[2].Detail), &summary); err != nil {
		t.Fatalf("failed to unmarshal summary detail: %v", err)
	}
	if summary.TotalDurationMS != 5500 || summary.GateCount != 2 {
		t.Errorf("summary = %+v, want total_duration_ms=5500/gate_count=2", summary)
	}
}

// TestRecordResearchPhaseEvent_PersistsDurationAndTokens covers GH-4129
// requirement (3): parallel-research phase Duration/TotalTokens (previously
// logged via slog.Info only at runner.go / parallel.go's ExecuteResearchPhase)
// must be persisted as an execution_event.
func TestRecordResearchPhaseEvent_PersistsDurationAndTokens(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	runner.SetLogStore(store)

	if err := store.SaveExecution(&memory.Execution{ID: "exec-gh4129-research", TaskID: "GH-4129-RESEARCH", Status: "running"}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	research := &ResearchResult{
		TaskID:      "GH-4129-RESEARCH",
		Findings:    []string{"finding A", "finding B"},
		Duration:    4200 * time.Millisecond,
		TotalTokens: 12345,
	}

	runner.recordResearchPhaseEvent("exec-gh4129-research", research)

	events, err := store.ListExecutionEvents("exec-gh4129-research")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Stage != memory.StageResearchPhase {
		t.Fatalf("event.Stage = %q, want %q", events[0].Stage, memory.StageResearchPhase)
	}

	var detail struct {
		DurationMS  int64 `json:"duration_ms"`
		TotalTokens int64 `json:"total_tokens"`
		Findings    int   `json:"findings"`
	}
	if err := json.Unmarshal([]byte(events[0].Detail), &detail); err != nil {
		t.Fatalf("failed to unmarshal detail: %v", err)
	}
	if detail.DurationMS != 4200 || detail.TotalTokens != 12345 || detail.Findings != 2 {
		t.Errorf("detail = %+v, want duration_ms=4200/total_tokens=12345/findings=2", detail)
	}

	// nil research must not write an event.
	runner.recordResearchPhaseEvent("exec-gh4129-research-nil", nil)
	nilEvents, err := store.ListExecutionEvents("exec-gh4129-research-nil")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(nilEvents) != 0 {
		t.Errorf("got %d events for nil research, want 0", len(nilEvents))
	}
}

// TestRecordRetryAttemptEvent_NamesTheLoop covers GH-4129 requirement (4):
// retried backend attempts (smart retry, quality-gate retry, intent-judge
// self-correct) must be tagged with a retry_attempt event naming which loop
// fired, so retry wall-clock share is queryable.
func TestRecordRetryAttemptEvent_NamesTheLoop(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	runner.SetLogStore(store)

	if err := store.SaveExecution(&memory.Execution{ID: "exec-gh4129-retry", TaskID: "GH-4129-RETRY", Status: "running"}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	runner.recordRetryAttemptEvent("exec-gh4129-retry", "smart_retry", 1)
	runner.recordRetryAttemptEvent("exec-gh4129-retry", "quality_gate_retry", 2)
	runner.recordRetryAttemptEvent("exec-gh4129-retry", "intent_judge_retry", 1)

	events, err := store.ListExecutionEvents("exec-gh4129-retry")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	wantLoops := []struct {
		loop    string
		attempt int
	}{
		{"smart_retry", 1},
		{"quality_gate_retry", 2},
		{"intent_judge_retry", 1},
	}
	for i, want := range wantLoops {
		if events[i].Stage != memory.StageRetryAttempt {
			t.Errorf("event[%d].Stage = %q, want %q", i, events[i].Stage, memory.StageRetryAttempt)
		}
		var detail struct {
			Loop    string `json:"loop"`
			Attempt int    `json:"attempt"`
		}
		if err := json.Unmarshal([]byte(events[i].Detail), &detail); err != nil {
			t.Fatalf("event[%d]: failed to unmarshal detail: %v", i, err)
		}
		if detail.Loop != want.loop || detail.Attempt != want.attempt {
			t.Errorf("event[%d] detail = %+v, want loop=%q/attempt=%d", i, detail, want.loop, want.attempt)
		}
	}
}

// TestRecordMemoryGuardRestoreEvents covers GH-4398: once
// GitOperations.RestoreDeletedIndexedMemoryDocs has fail-safe-restored
// protected memory docs on disk, the Runner must record one
// memory_guard_restore execution event per restored file (naming its path
// and graph node id) so the intervention is visible in `pilot trace` / the
// execution_events ledger — otherwise a guard that silently rewrites the
// branch would be invisible to anyone reviewing the PR.
func TestRecordMemoryGuardRestoreEvents(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	runner.SetLogStore(store)

	if err := store.SaveExecution(&memory.Execution{ID: "exec-gh4398-restore", TaskID: "GH-4398-RESTORE", Status: "running"}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	restored := []RestoredMemoryDoc{
		{Path: ".agent/knowledge/memories/pitfalls/mem-158.md", NodeID: "mem-158"},
		{Path: ".agent/knowledge/memories/learnings/mem-160.md", NodeID: "mem-160"},
	}
	runner.recordMemoryGuardRestoreEvents("exec-gh4398-restore", restored)

	events, err := store.ListExecutionEvents("exec-gh4398-restore")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	for i, want := range restored {
		if events[i].Stage != memory.StageMemoryGuardRestore {
			t.Errorf("event[%d].Stage = %q, want %q", i, events[i].Stage, memory.StageMemoryGuardRestore)
		}
		var detail struct {
			Path   string `json:"path"`
			NodeID string `json:"node_id"`
		}
		if err := json.Unmarshal([]byte(events[i].Detail), &detail); err != nil {
			t.Fatalf("event[%d]: failed to unmarshal detail: %v", i, err)
		}
		if detail.Path != want.Path || detail.NodeID != want.NodeID {
			t.Errorf("event[%d] detail = %+v, want path=%q/node_id=%q", i, detail, want.Path, want.NodeID)
		}
	}

	// Empty input must not write any events.
	runner.recordMemoryGuardRestoreEvents("exec-gh4398-restore-empty", nil)
	emptyEvents, err := store.ListExecutionEvents("exec-gh4398-restore-empty")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(emptyEvents) != 0 {
		t.Errorf("got %d events for empty restored slice, want 0", len(emptyEvents))
	}
}

// statefulQualityChecker fails once with ShouldRetry, then passes with
// GateDetails/TotalDuration set — used to drive the quality-gate retry loop
// through runner.Execute end to end (GH-4129).
type statefulQualityChecker struct {
	calls int
}

func (m *statefulQualityChecker) Check(_ context.Context) (*QualityOutcome, error) {
	m.calls++
	if m.calls == 1 {
		return &QualityOutcome{
			Passed:        false,
			ShouldRetry:   true,
			RetryFeedback: "fix the build",
			Attempt:       1,
		}, nil
	}
	return &QualityOutcome{
		Passed:        true,
		TotalDuration: 1200 * time.Millisecond,
		GateDetails: []QualityGateDetail{
			{Name: "build", Passed: true, Duration: 1200 * time.Millisecond},
		},
	}, nil
}

// TestDirectPath_QualityGateRetryEndToEnd drives a real quality-gate-retry
// loop through runner.Execute (GH-4129): the first Check() fails with
// ShouldRetry, triggering a re-invocation of the backend and a
// retry_attempt(quality_gate_retry) event; the second Check() passes and
// its GateDetails/TotalDuration land as quality_gate events.
func TestDirectPath_QualityGateRetryEndToEnd(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	backend := &mockSelfReviewBackend{output: "done"}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{}
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.SetLogStore(store)

	checker := &statefulQualityChecker{}
	runner.SetQualityCheckerFactory(func(_, _ string) QualityChecker {
		return checker
	})

	task := &Task{
		ID:          "GH-4129-QG-RETRY",
		Title:       "Quality gate retry end-to-end test",
		Description: "First check fails and retries, second check passes",
		ProjectPath: t.TempDir(),
		LocalMode:   true,
		CreatePR:    true,
	}
	if err := store.SaveExecution(&memory.Execution{ID: task.LogExecutionID(), TaskID: task.ID, Status: "running"}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() not successful: %s", result.Error)
	}
	if checker.calls != 2 {
		t.Fatalf("quality checker called %d times, want 2 (fail then pass)", checker.calls)
	}

	events, err := store.ListExecutionEvents(task.LogExecutionID())
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}

	var sawRetry, sawGate, sawSummary bool
	for _, e := range events {
		switch e.Stage {
		case memory.StageRetryAttempt:
			if strings.Contains(e.Detail, "quality_gate_retry") {
				sawRetry = true
			}
		case memory.StageQualityGate:
			if strings.Contains(e.Detail, "\"gate\":\"build\"") {
				sawGate = true
			}
			if strings.Contains(e.Detail, "total_duration_ms") {
				sawSummary = true
			}
		}
	}
	if !sawRetry {
		t.Errorf("expected a retry_attempt event naming quality_gate_retry, got events: %+v", events)
	}
	if !sawGate {
		t.Errorf("expected a quality_gate event for the build gate, got events: %+v", events)
	}
	if !sawSummary {
		t.Errorf("expected a quality_gate summary event with total_duration_ms, got events: %+v", events)
	}
}
