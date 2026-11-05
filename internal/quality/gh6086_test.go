package quality

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"
)

// Mutation pins for the post-merge minors of PR #6085 (GH-6086). Each test
// names the item it guards; reverting that item makes it fail.

// Item 1: Err is cleared per attempt, so a timeout on attempt 1 does not
// outlive attempt 2.
func TestRunGate_ErrDoesNotLeakAcrossAttempts(t *testing.T) {
	tests := []struct {
		name       string
		attempt2   string
		wantStatus GateStatus
		wantError  string
	}{
		{"timeout then exit 1", "exit 1", StatusFailed, "command exited with code 1"},
		{"timeout then pass", "exit 0", StatusPassed, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Enabled: true,
				Gates: []*Gate{{
					Name:       "flaky",
					Type:       GateCustom,
					Command:    "if [ -f ran ]; then " + tt.attempt2 + "; fi; touch ran; sleep 30",
					Required:   true,
					Timeout:    500 * time.Millisecond,
					MaxRetries: 1,
				}},
			}

			result, err := NewRunner(config, t.TempDir()).RunGate(context.Background(), "flaky")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Status != tt.wantStatus {
				t.Fatalf("Status = %q, want %q (Error=%q)", result.Status, tt.wantStatus, result.Error)
			}
			if result.RetryCount != 1 {
				t.Fatalf("RetryCount = %d, want 1 (the gate must have timed out first)", result.RetryCount)
			}
			if errors.Is(result.Err, ErrGateTimeout) {
				t.Errorf("Err = %v, still wraps the attempt-1 timeout", result.Err)
			}
			if tt.wantError != "" && result.Error != tt.wantError {
				t.Errorf("Error = %q, want %q", result.Error, tt.wantError)
			}
		})
	}
}

// Item 2: a timed-out gate is called a timeout in the retry feedback rather
// than rendering as a bare failure.
func TestFormatErrorFeedback_NamesTimeout(t *testing.T) {
	timedOut := &Result{
		GateName: "slow",
		Status:   StatusFailed,
		Err:      fmt.Errorf("%w: after 5m0s", ErrGateTimeout),
		Error:    "quality gate timed out: after 5m0s",
		ExitCode: -1,
	}
	failed := &Result{GateName: "red", Status: StatusFailed, Error: "command exited with code 1", ExitCode: 1}

	got := FormatErrorFeedback(&CheckResults{Results: []*Result{timedOut}})
	if !strings.Contains(got, "Timed out") || !strings.Contains(got, "after 5m0s") {
		t.Errorf("feedback for a timed-out gate does not say so:\n%s", got)
	}

	got = FormatErrorFeedback(&CheckResults{Results: []*Result{failed}})
	if strings.Contains(got, "Timed out") {
		t.Errorf("feedback for an ordinary failure claims a timeout:\n%s", got)
	}
}

// Item 3: the Err field doc lists every error the runner stores in it.
func TestResultErrDocNamesTimeout(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "types.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse types.go: %v", err)
	}

	var doc string
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Result" {
			return true
		}
		for _, f := range ts.Type.(*ast.StructType).Fields.List {
			if len(f.Names) == 1 && f.Names[0].Name == "Err" && f.Doc != nil {
				doc = f.Doc.Text()
			}
		}
		return false
	})

	for _, want := range []string{"ErrGateRunnerMissing", "ErrGateTargetMissing", "ErrGateTimeout"} {
		if !strings.Contains(doc, want) {
			t.Errorf("Result.Err doc %q does not mention %s", doc, want)
		}
	}
}

// Item 4: the all-required-passed decision is a plain function of gates and
// results, tested without a seam on the production Runner.
func TestRequiredGatesPassed(t *testing.T) {
	gates := []*Gate{
		{Name: "req", Required: true},
		{Name: "opt", Required: false},
	}
	tests := []struct {
		name    string
		results []*Result
		want    bool
	}{
		{"all passed", []*Result{{Status: StatusPassed}, {Status: StatusPassed}}, true},
		{"required retrying", []*Result{{Status: StatusRetrying}, {Status: StatusPassed}}, false},
		{"required failed", []*Result{{Status: StatusFailed}, {Status: StatusPassed}}, false},
		{"optional failed", []*Result{{Status: StatusPassed}, {Status: StatusFailed}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := NewRunner(&Config{Enabled: true, Gates: gates}, t.TempDir())
			if got := runner.requiredGatesPassed(gates, tt.results); got != tt.want {
				t.Errorf("requiredGatesPassed = %v, want %v", got, tt.want)
			}
		})
	}
}

// Item 5: a required gate that is not passed and not failed still reaches the
// retry feedback and counts as fixable.
func TestNonFailedNonPassedRequiredGate(t *testing.T) {
	config := &Config{Gates: []*Gate{
		{Name: "missing", Required: true},
		{Name: "stuck", Required: true},
	}}
	results := &CheckResults{Results: []*Result{
		{GateName: "missing", Status: StatusFailed, Err: ErrGateRunnerMissing},
		{GateName: "stuck", Status: StatusRunning, Output: "still going"},
	}}

	if RunnerMissingOnly(config, results) {
		t.Error("RunnerMissingOnly = true, want false: the running required gate is not a missing runner")
	}

	feedback := FormatErrorFeedback(results)
	if !strings.Contains(feedback, "stuck Gate") || !strings.Contains(feedback, "still going") {
		t.Errorf("feedback omits the not-passed required gate:\n%s", feedback)
	}
}
