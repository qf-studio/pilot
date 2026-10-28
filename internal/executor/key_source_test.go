package executor

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeResultCostUSD is the total_cost_usd the fake CLI reports below.
const fakeResultCostUSD = 0.4321

func TestKeySourceFromEnv(t *testing.T) {
	tests := []struct {
		name string
		set  bool
		val  string
		want string
	}{
		{"platform", true, "platform", "platform"},
		{"own", true, "own", "own"},
		{"unset", false, "", "unknown"},
		{"empty", true, "", "unknown"},
		{"other value", true, "byok", "unknown"},
		{"wrong case", true, "Platform", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(KeySourceEnv, tt.val)
			} else {
				t.Setenv(KeySourceEnv, "")
				_ = os.Unsetenv(KeySourceEnv)
			}
			if got := KeySourceFromEnv(); got != tt.want {
				t.Errorf("KeySourceFromEnv() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClaudeCodeBackendParseReportedCost: the result event's total_cost_usd is
// parsed; absent stays nil (not 0); non-result events never carry it (GH-5676).
func TestClaudeCodeBackendParseReportedCost(t *testing.T) {
	backend := NewClaudeCodeBackend(nil)

	ev := backend.parseStreamEvent(`{"type":"result","result":"Done","total_cost_usd":0.4321}`)
	if ev.ReportedCostUSD == nil || math.Abs(*ev.ReportedCostUSD-fakeResultCostUSD) > 1e-9 {
		t.Fatalf("ReportedCostUSD = %v, want %v", ev.ReportedCostUSD, fakeResultCostUSD)
	}

	ev = backend.parseStreamEvent(`{"type":"result","result":"Done","total_cost_usd":0}`)
	if ev.ReportedCostUSD == nil || *ev.ReportedCostUSD != 0 {
		t.Errorf("reported $0 must stay a non-nil 0, got %v", ev.ReportedCostUSD)
	}

	ev = backend.parseStreamEvent(`{"type":"result","result":"Done"}`)
	if ev.ReportedCostUSD != nil {
		t.Errorf("absent total_cost_usd must stay nil, got %v", *ev.ReportedCostUSD)
	}
}

// fakeCostCLI writes a fake claude CLI that emits a result event carrying
// total_cost_usd and returns its path.
func fakeCostCLI(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI test relies on shell scripts; skipping on windows")
	}
	script := filepath.Join(t.TempDir(), "fake-claude")
	body := `#!/bin/sh
echo '{"type":"result","result":"done","usage":{"input_tokens":100,"output_tokens":50},"model":"claude-sonnet-4-6","total_cost_usd":0.4321}'
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return script
}

func TestClaudeCodeBackendExecuteCapturesReportedCost(t *testing.T) {
	backend := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: fakeCostCLI(t)})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := backend.Execute(ctx, ExecuteOptions{
		Prompt:       "hello",
		ProjectPath:  t.TempDir(),
		EventHandler: func(BackendEvent) {},
	})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.ReportedCostUSD == nil || math.Abs(*result.ReportedCostUSD-fakeResultCostUSD) > 1e-9 {
		t.Fatalf("BackendResult.ReportedCostUSD = %v, want %v", result.ReportedCostUSD, fakeResultCostUSD)
	}
}

// realCLIBackend runs the real ClaudeCodeBackend (fake CLI binary) so the
// total_cost_usd travels the production path: stream-json -> BackendEvent ->
// runner progress state -> ExecutionResult.
type realCLIBackend struct{ inner *ClaudeCodeBackend }

func (b *realCLIBackend) Name() string      { return "real-cli" }
func (b *realCLIBackend) IsAvailable() bool { return true }
func (b *realCLIBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	return b.inner.Execute(ctx, opts)
}

// TestExecution_PersistsKeySourceAndReportedCost is the GH-5676 write-path
// pin: with PILOT_ANTHROPIC_KEY_SOURCE=platform, an execution run end to end
// against a fake CLI reads back key_source=platform and reported_cost_usd
// equal to the CLI's total_cost_usd, while estimated_cost_usd stays the
// list-price estimate.
func TestExecution_PersistsKeySourceAndReportedCost(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv(KeySourceEnv, "platform")

	store, cleanup := setupTestStore(t)
	defer cleanup()

	const branch = "pilot/GH-test-keysource"
	dir := setupPRGuardRepo(t, branch, true)

	runner := NewRunnerWithBackend(&realCLIBackend{inner: NewClaudeCodeBackend(&ClaudeCodeConfig{Command: fakeCostCLI(t)})})
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{UseWorktree: boolPtr(false), SkipSelfReview: true}

	task := &Task{
		ID:          "GH-test-keysource",
		Title:       "key source",
		Description: "persist key source and reported cost",
		ProjectPath: dir,
		Branch:      branch,
	}

	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin() error: %v", err)
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
	if _, err := lifecycle.Finish(execID, result, nil, time.Second); err != nil {
		t.Fatalf("Finish() error: %v", err)
	}

	got, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution() error: %v", err)
	}
	if got.KeySource != "platform" {
		t.Errorf("KeySource = %q, want %q", got.KeySource, "platform")
	}
	if got.ReportedCostUSD == nil || math.Abs(*got.ReportedCostUSD-fakeResultCostUSD) > 1e-9 {
		t.Errorf("ReportedCostUSD = %v, want %v", got.ReportedCostUSD, fakeResultCostUSD)
	}
	if got.EstimatedCostUSD <= 0 || math.Abs(got.EstimatedCostUSD-fakeResultCostUSD) < 1e-9 {
		t.Errorf("EstimatedCostUSD = %v, want the list-price estimate (>0, distinct from the reported cost)", got.EstimatedCostUSD)
	}
}

// TestExecution_KeySourceUnknownWhenEnvUnset: no env var -> "unknown", and a
// run whose CLI reported no cost persists NULL (nil), not 0.
func TestExecution_KeySourceUnknownWhenEnvUnset(t *testing.T) {
	t.Setenv(KeySourceEnv, "")
	_ = os.Unsetenv(KeySourceEnv)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-test-keysource-unknown", ProjectPath: "/tmp/p"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin() error: %v", err)
	}
	if _, err := lifecycle.Finish(execID, &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "abc123"}, nil, time.Second); err != nil {
		t.Fatalf("Finish() error: %v", err)
	}
	got, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution() error: %v", err)
	}
	if got.KeySource != "unknown" {
		t.Errorf("KeySource = %q, want unknown", got.KeySource)
	}
	if got.ReportedCostUSD != nil {
		t.Errorf("ReportedCostUSD = %v, want nil (NULL) when the CLI reported nothing", *got.ReportedCostUSD)
	}
}
