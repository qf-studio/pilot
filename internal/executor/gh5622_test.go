package executor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/quality"
)

// GH-5622: failed quality gates must surface name, command, exit code and a
// bounded slice of output; a missing runner (exit 127) fails once, unretried.

func TestFormatQualityGateFailure(t *testing.T) {
	var many strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&many, "line %d\n", i)
	}

	tests := []struct {
		name         string
		outcome      *QualityOutcome
		wantFull     []string
		wantNotFull  []string
		wantPlain    []string
		wantNotPlain []string
	}{
		{
			name: "red test names gate, command, exit code and output",
			outcome: &QualityOutcome{GateDetails: []QualityGateDetail{
				{Name: "build", Passed: true, Command: "go build ./..."},
				{Name: "test", Command: "go test ./...", ExitCode: 1, Output: "--- FAIL: TestX\nFAIL"},
			}},
			wantFull:     []string{"gate `test`", "`go test ./...`", "exited with code 1", "<details>", "--- FAIL: TestX", "</details>"},
			wantNotFull:  []string{"gate `build`"},
			wantPlain:    []string{"gate `test`", "exited with code 1"},
			wantNotPlain: []string{"<details>", "--- FAIL: TestX"},
		},
		{
			name: "runner missing is called out",
			outcome: &QualityOutcome{GateDetails: []QualityGateDetail{
				{Name: "test", Command: "make test", ExitCode: 127, RunnerMissing: true, Output: "sh: make: command not found"},
			}},
			wantFull:  []string{"`make test`", "exited with code 127", "command not found", "sh: make: command not found"},
			wantPlain: []string{"exited with code 127", "command not found"},
		},
		{
			name: "output capped to the last 20 lines",
			outcome: &QualityOutcome{GateDetails: []QualityGateDetail{
				{Name: "test", Command: "go test ./...", ExitCode: 1, Output: many.String()},
			}},
			wantFull:    []string{"line 50", "line 31"},
			wantNotFull: []string{"line 30\n", "line 1\n"},
		},
		{
			name: "code fence in output cannot break the block",
			outcome: &QualityOutcome{GateDetails: []QualityGateDetail{
				{Name: "test", Command: "x", ExitCode: 1, Output: "before\n```\nafter"},
			}},
			wantNotFull: []string{"\n```\nafter"},
		},
		{
			name: "no output means no details block",
			outcome: &QualityOutcome{GateDetails: []QualityGateDetail{
				{Name: "test", Command: "x", ExitCode: 2},
			}},
			wantFull:    []string{"exited with code 2"},
			wantNotFull: []string{"<details>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plain, full := formatQualityGateFailure("quality gates failed after 2 auto-retries", tt.outcome)
			if !strings.HasPrefix(full, "quality gates failed after 2 auto-retries") {
				t.Errorf("headline missing: %q", full)
			}
			for _, w := range tt.wantFull {
				if !strings.Contains(full, w) {
					t.Errorf("full missing %q:\n%s", w, full)
				}
			}
			for _, w := range tt.wantNotFull {
				if strings.Contains(full, w) {
					t.Errorf("full should not contain %q:\n%s", w, full)
				}
			}
			for _, w := range tt.wantPlain {
				if !strings.Contains(plain, w) {
					t.Errorf("plain missing %q:\n%s", w, plain)
				}
			}
			for _, w := range tt.wantNotPlain {
				if strings.Contains(plain, w) {
					t.Errorf("plain should not contain %q:\n%s", w, plain)
				}
			}
		})
	}
}

func TestTerminalStatus_QualityGateFailedIgnoresQuotedOutput(t *testing.T) {
	res := &ExecutionResult{
		Outcome: OutcomeQualityGateFailed,
		// Quoted gate output that would otherwise match skipped/infra signatures.
		Error: "quality gates failed\n\ncontext canceled\nsignal: killed\npush failed",
	}
	if got := TerminalStatus(res); got != "failed" {
		t.Errorf("TerminalStatus() = %q, want failed", got)
	}
}

// runnerMissingChecker reports a single required gate failing with exit 127 and
// counts calls, so the test can prove no retry pass happened.
type runnerMissingChecker struct{ calls int }

func (c *runnerMissingChecker) Check(_ context.Context) (*QualityOutcome, error) {
	c.calls++
	return &QualityOutcome{
		Passed:      false,
		ShouldRetry: false,
		GateDetails: []QualityGateDetail{{
			Name: "test", Command: "make test", ExitCode: 127, RunnerMissing: true,
			Output: "sh: make: command not found",
		}},
	}, nil
}

func TestExecute_RunnerMissingGateFailsOnceWithCommandInError(t *testing.T) {
	backend := &mockSelfReviewBackend{output: "done"}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{}
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true

	checker := &runnerMissingChecker{}
	runner.SetQualityCheckerFactory(func(_, _, _ string) QualityChecker { return checker })

	task := &Task{
		ID:          "GH-5622-RUNNER-MISSING",
		Title:       "gate runner missing",
		Description: "make is not installed",
		ProjectPath: t.TempDir(),
		LocalMode:   true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure")
	}
	if checker.calls != 1 {
		t.Errorf("quality checker called %d times, want 1 (no auto-retries)", checker.calls)
	}
	for _, w := range []string{"gate `test`", "`make test`", "exited with code 127", "<details>", "make: command not found"} {
		if !strings.Contains(result.Error, w) {
			t.Errorf("result.Error missing %q:\n%s", w, result.Error)
		}
	}
	if strings.Contains(result.Error, "auto-retries") {
		t.Errorf("a non-retried failure must not claim auto-retries:\n%s", result.Error)
	}
	if got := TerminalStatus(result); got != "failed" {
		t.Errorf("TerminalStatus() = %q, want failed", got)
	}
}

func TestSimpleQualityChecker_RunnerMissingNotRetried(t *testing.T) {
	cfg := &quality.Config{
		Enabled: true,
		Gates: []*quality.Gate{{
			Name: "build", Type: quality.GateCustom, Command: "exit 127",
			Required: true, Timeout: time.Minute,
		}},
		OnFailure: quality.FailureConfig{Action: quality.ActionRetry, MaxRetries: 2},
	}
	c := &simpleQualityChecker{config: cfg, projectPath: t.TempDir(), taskID: "GH-5622"}

	outcome, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if outcome.Passed || outcome.ShouldRetry {
		t.Errorf("Passed=%v ShouldRetry=%v, want false/false", outcome.Passed, outcome.ShouldRetry)
	}
	g := outcome.GateDetails[0]
	if !g.RunnerMissing || g.ExitCode != 127 || g.Command != "exit 127" {
		t.Errorf("gate detail = %+v", g)
	}
}
