package quality

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunner_RunAll_Disabled(t *testing.T) {
	config := &Config{
		Enabled: false,
		Gates:   []*Gate{},
	}

	runner := NewRunner(config, "/tmp")
	results, err := runner.RunAll(context.Background(), "test-task")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !results.AllPassed {
		t.Error("expected AllPassed to be true when disabled")
	}
	if len(results.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results.Results))
	}
}

func TestRunner_RunAll_PassingGates(t *testing.T) {
	config := &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:     "echo",
				Type:     GateCustom,
				Command:  "echo 'hello'",
				Required: true,
				Timeout:  10 * time.Second,
			},
			{
				Name:     "true",
				Type:     GateCustom,
				Command:  "true",
				Required: true,
				Timeout:  10 * time.Second,
			},
		},
	}

	runner := NewRunner(config, "/tmp")
	results, err := runner.RunAll(context.Background(), "test-task")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !results.AllPassed {
		t.Error("expected AllPassed to be true for passing gates")
	}
	if len(results.Results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results.Results))
	}

	for _, r := range results.Results {
		if r.Status != StatusPassed {
			t.Errorf("gate %s: expected status Passed, got %s", r.GateName, r.Status)
		}
	}
}

func TestRunner_RunAll_FailingRequiredGate(t *testing.T) {
	config := &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:       "failing",
				Type:       GateCustom,
				Command:    "exit 1",
				Required:   true,
				Timeout:    10 * time.Second,
				MaxRetries: 0,
			},
		},
	}

	runner := NewRunner(config, "/tmp")
	results, err := runner.RunAll(context.Background(), "test-task")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results.AllPassed {
		t.Error("expected AllPassed to be false for failing required gate")
	}
	if len(results.Results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results.Results))
	}
	if results.Results[0].Status != StatusFailed {
		t.Errorf("expected status Failed, got %s", results.Results[0].Status)
	}
}

func TestRunner_RunAll_FailingOptionalGate(t *testing.T) {
	config := &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:       "optional-failing",
				Type:       GateCustom,
				Command:    "exit 1",
				Required:   false, // Not required
				Timeout:    10 * time.Second,
				MaxRetries: 0,
			},
		},
	}

	runner := NewRunner(config, "/tmp")
	results, err := runner.RunAll(context.Background(), "test-task")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !results.AllPassed {
		t.Error("expected AllPassed to be true for failing optional gate")
	}
}

func TestRunner_RunGate_WithRetry(t *testing.T) {
	// Use a file to track attempts
	config := &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:       "retry-test",
				Type:       GateCustom,
				Command:    "exit 1",
				Required:   true,
				Timeout:    5 * time.Second,
				MaxRetries: 2,
				RetryDelay: 10 * time.Millisecond,
			},
		},
	}

	runner := NewRunner(config, "/tmp")
	result, err := runner.RunGate(context.Background(), "retry-test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusFailed {
		t.Errorf("expected status Failed after retries, got %s", result.Status)
	}
	if result.RetryCount != 2 {
		t.Errorf("expected 2 retries, got %d", result.RetryCount)
	}
}

func TestRunner_RunGate_ContextCancellation(t *testing.T) {
	config := &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:     "long-running",
				Type:     GateCustom,
				Command:  "sleep 30",
				Required: true,
				Timeout:  60 * time.Second,
			},
		},
	}

	runner := NewRunner(config, "/tmp")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result, err := runner.RunGate(ctx, "long-running")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusFailed {
		t.Errorf("expected status Failed after cancellation, got %s", result.Status)
	}
}

func TestRunner_RunGate_NotFound(t *testing.T) {
	config := &Config{
		Enabled: true,
		Gates:   []*Gate{},
	}

	runner := NewRunner(config, "/tmp")
	_, err := runner.RunGate(context.Background(), "nonexistent")

	if err != ErrGateNotFound {
		t.Errorf("expected ErrGateNotFound, got %v", err)
	}
}

func TestRunner_CoverageGate(t *testing.T) {
	config := &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:      "coverage-test",
				Type:      GateCoverage,
				Command:   "echo 'coverage: 85.3% of statements'",
				Required:  true,
				Timeout:   10 * time.Second,
				Threshold: 80.0,
			},
		},
	}

	runner := NewRunner(config, "/tmp")
	result, err := runner.RunGate(context.Background(), "coverage-test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusPassed {
		t.Errorf("expected status Passed, got %s", result.Status)
	}
	if result.Coverage < 85.0 || result.Coverage > 86.0 {
		t.Errorf("expected coverage ~85.3%%, got %.1f%%", result.Coverage)
	}
}

func TestRunner_CoverageGate_BelowThreshold(t *testing.T) {
	config := &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:       "coverage-low",
				Type:       GateCoverage,
				Command:    "echo 'coverage: 50.0% of statements'",
				Required:   true,
				Timeout:    10 * time.Second,
				Threshold:  80.0,
				MaxRetries: 0,
			},
		},
	}

	runner := NewRunner(config, "/tmp")
	result, err := runner.RunGate(context.Background(), "coverage-low")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusFailed {
		t.Errorf("expected status Failed for low coverage, got %s", result.Status)
	}
	if !strings.Contains(result.Error, "below threshold") {
		t.Errorf("expected error about threshold, got: %s", result.Error)
	}
}

func TestParseCoverageOutput(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected float64
	}{
		{
			name:     "go coverage",
			output:   "ok  	github.com/test/pkg	0.123s	coverage: 85.3% of statements",
			expected: 85.3,
		},
		{
			name:     "go coverage simple",
			output:   "coverage: 100.0% of statements",
			expected: 100.0,
		},
		{
			name:     "jest coverage",
			output:   "Statements   : 75.5% ( 100/132 )",
			expected: 75.5,
		},
		{
			name:     "python coverage",
			output:   "TOTAL                                              85%",
			expected: 85.0,
		},
		{
			name:     "no coverage",
			output:   "all tests passed",
			expected: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCoverageOutput(tt.output)
			if got != tt.expected {
				t.Errorf("parseCoverageOutput() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFormatErrorFeedback(t *testing.T) {
	results := &CheckResults{
		TaskID:    "test-task",
		AllPassed: false,
		Results: []*Result{
			{
				GateName: "build",
				Status:   StatusPassed,
			},
			{
				GateName: "test",
				Status:   StatusFailed,
				Output:   "FAIL: TestSomething\nexpected 5, got 10",
			},
			{
				GateName: "lint",
				Status:   StatusFailed,
				Output:   "main.go:10: unused variable 'x'",
			},
		},
	}

	feedback := FormatErrorFeedback(results)

	if !strings.Contains(feedback, "Quality Gate Failures") {
		t.Error("expected feedback to contain header")
	}
	if !strings.Contains(feedback, "test Gate") {
		t.Error("expected feedback to contain test gate")
	}
	if !strings.Contains(feedback, "lint Gate") {
		t.Error("expected feedback to contain lint gate")
	}
	if strings.Contains(feedback, "build Gate") {
		t.Error("feedback should not contain passing build gate")
	}
	if !strings.Contains(feedback, "expected 5, got 10") {
		t.Error("expected feedback to contain test error output")
	}
}

func TestShouldRetry(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		results  *CheckResults
		attempt  int
		expected bool
	}{
		{
			name: "should retry on first failure",
			config: &Config{
				OnFailure: FailureConfig{
					Action:     ActionRetry,
					MaxRetries: 2,
				},
			},
			results:  &CheckResults{AllPassed: false},
			attempt:  0,
			expected: true,
		},
		{
			name: "should not retry when passed",
			config: &Config{
				OnFailure: FailureConfig{
					Action:     ActionRetry,
					MaxRetries: 2,
				},
			},
			results:  &CheckResults{AllPassed: true},
			attempt:  0,
			expected: false,
		},
		{
			name: "should not retry when exhausted",
			config: &Config{
				OnFailure: FailureConfig{
					Action:     ActionRetry,
					MaxRetries: 2,
				},
			},
			results:  &CheckResults{AllPassed: false},
			attempt:  2,
			expected: false,
		},
		{
			name: "should not retry when action is fail",
			config: &Config{
				OnFailure: FailureConfig{
					Action:     ActionFail,
					MaxRetries: 2,
				},
			},
			results:  &CheckResults{AllPassed: false},
			attempt:  0,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldRetry(tt.config, tt.results, tt.attempt)
			if got != tt.expected {
				t.Errorf("ShouldRetry() = %v, want %v", got, tt.expected)
			}
		})
	}
}
