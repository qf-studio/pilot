package quality

import (
	"context"
	"os/exec"
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

func TestRunner_OnProgress(t *testing.T) {
	config := &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:     "progress-gate",
				Type:     GateCustom,
				Command:  "echo 'test'",
				Required: true,
				Timeout:  5 * time.Second,
			},
		},
	}

	runner := NewRunner(config, "/tmp")

	var progressEvents []struct {
		gateName string
		status   GateStatus
		message  string
	}

	runner.OnProgress(func(gateName string, status GateStatus, message string) {
		progressEvents = append(progressEvents, struct {
			gateName string
			status   GateStatus
			message  string
		}{gateName, status, message})
	})

	_, err := runner.RunAll(context.Background(), "progress-task")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(progressEvents) < 2 {
		t.Errorf("expected at least 2 progress events (running + final), got %d", len(progressEvents))
	}

	// Verify first event is running
	if len(progressEvents) > 0 && progressEvents[0].status != StatusRunning {
		t.Errorf("expected first event status Running, got %s", progressEvents[0].status)
	}

	// Verify last event is a terminal status
	if len(progressEvents) > 0 {
		lastStatus := progressEvents[len(progressEvents)-1].status
		if lastStatus != StatusPassed && lastStatus != StatusFailed {
			t.Errorf("expected last event to be terminal status, got %s", lastStatus)
		}
	}
}

func TestRunner_RunGate_Timeout(t *testing.T) {
	config := &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:       "timeout-test",
				Type:       GateCustom,
				Command:    "sleep 10",
				Required:   true,
				Timeout:    100 * time.Millisecond,
				MaxRetries: 0,
			},
		},
	}

	runner := NewRunner(config, "/tmp")
	result, err := runner.RunGate(context.Background(), "timeout-test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// When timeout occurs, the gate should fail with exit code != 0 or error
	if result.Status != StatusFailed {
		// The timeout may result in various failure states - check it didn't pass
		if result.ExitCode == 0 && result.Error == "" {
			t.Errorf("expected gate to fail due to timeout, but it passed")
		}
	}
}

func TestRunner_RunGate_CommandWithOutput(t *testing.T) {
	config := &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:     "output-test",
				Type:     GateCustom,
				Command:  "echo 'stdout message' && echo 'stderr message' >&2",
				Required: true,
				Timeout:  5 * time.Second,
			},
		},
	}

	runner := NewRunner(config, "/tmp")
	result, err := runner.RunGate(context.Background(), "output-test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusPassed {
		t.Errorf("expected status Passed, got %s", result.Status)
	}
	if !strings.Contains(result.Output, "stdout message") {
		t.Error("expected output to contain stdout message")
	}
	if !strings.Contains(result.Output, "stderr message") {
		t.Error("expected output to contain stderr message")
	}
}

func TestRunner_RunGate_RetrySuccess(t *testing.T) {
	// Create a temp file to track attempts
	tmpFile := "/tmp/quality_test_retry_" + time.Now().Format("20060102150405")
	defer func() {
		// Cleanup - ignore error as it's test cleanup
		_ = exec.Command("rm", "-f", tmpFile).Run()
	}()

	// Command that fails first time, succeeds second time
	command := `if [ -f ` + tmpFile + ` ]; then echo "success"; exit 0; else touch ` + tmpFile + `; exit 1; fi`

	config := &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:       "retry-success",
				Type:       GateCustom,
				Command:    command,
				Required:   true,
				Timeout:    5 * time.Second,
				MaxRetries: 2,
				RetryDelay: 10 * time.Millisecond,
			},
		},
	}

	runner := NewRunner(config, "/tmp")
	result, err := runner.RunGate(context.Background(), "retry-success")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusPassed {
		t.Errorf("expected status Passed after retry, got %s", result.Status)
	}
	if result.RetryCount != 1 {
		t.Errorf("expected 1 retry, got %d", result.RetryCount)
	}
}

func TestRunner_RunGate_ContextCancelDuringRetryDelay(t *testing.T) {
	config := &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:       "cancel-retry-delay",
				Type:       GateCustom,
				Command:    "exit 1",
				Required:   true,
				Timeout:    5 * time.Second,
				MaxRetries: 5,
				RetryDelay: 2 * time.Second, // Long delay to allow cancellation
			},
		},
	}

	runner := NewRunner(config, "/tmp")

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short time (during retry delay)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	result, err := runner.RunGate(ctx, "cancel-retry-delay")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusFailed {
		t.Errorf("expected status Failed after cancellation, got %s", result.Status)
	}
	if !strings.Contains(result.Error, "cancelled") {
		t.Errorf("expected error to mention cancellation, got: %s", result.Error)
	}
}

func TestFormatErrorFeedback_TruncatesLongOutput(t *testing.T) {
	// Create output longer than 2000 characters
	longOutput := strings.Repeat("x", 3000)

	results := &CheckResults{
		TaskID:    "truncate-test",
		AllPassed: false,
		Results: []*Result{
			{
				GateName: "long-output",
				Status:   StatusFailed,
				Output:   longOutput,
			},
		},
	}

	feedback := FormatErrorFeedback(results)

	if len(feedback) >= len(longOutput) {
		t.Error("expected feedback to be truncated")
	}
	if !strings.Contains(feedback, "truncated") {
		t.Error("expected truncation notice in feedback")
	}
}

func TestFormatErrorFeedback_NoFailedGates(t *testing.T) {
	results := &CheckResults{
		TaskID:    "all-passed",
		AllPassed: true,
		Results: []*Result{
			{GateName: "build", Status: StatusPassed},
			{GateName: "test", Status: StatusPassed},
		},
	}

	feedback := FormatErrorFeedback(results)

	// Should still have header but no gate sections
	if !strings.Contains(feedback, "Quality Gate Failures") {
		t.Error("expected header in feedback")
	}
	if strings.Contains(feedback, "build Gate") {
		t.Error("should not contain passed gates")
	}
}

func TestParseCoverageOutput_MultiplePatterns(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected float64
	}{
		{
			name: "go coverage multiline",
			output: `=== RUN TestSomething
--- PASS: TestSomething
PASS
coverage: 78.5% of statements
ok  	github.com/test/pkg	1.234s`,
			expected: 78.5,
		},
		{
			name: "jest lines coverage",
			output: `Lines        : 65.2% ( 45/69 )
Statements   : 70.1% ( 100/142 )`,
			expected: 70.1, // Should get statements (last match)
		},
		{
			name: "python coverage with file breakdown",
			output: `Name                      Stmts   Miss  Cover
---------------------------------------------
mymodule/file1.py            50     10    80%
mymodule/file2.py            30      5    83%
---------------------------------------------
TOTAL                        80     15    81%`,
			expected: 81.0,
		},
		{
			name:     "empty output",
			output:   "",
			expected: 0.0,
		},
		{
			name:     "unrelated output",
			output:   "Build successful\nNo errors found",
			expected: 0.0,
		},
		{
			name:     "coverage at 0 percent",
			output:   "coverage: 0.0% of statements",
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

func TestRunner_CoverageGate_NoThreshold(t *testing.T) {
	config := &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:      "coverage-no-threshold",
				Type:      GateCoverage,
				Command:   "echo 'coverage: 45.0% of statements'",
				Required:  true,
				Timeout:   10 * time.Second,
				Threshold: 0, // No threshold set
			},
		},
	}

	runner := NewRunner(config, "/tmp")
	result, err := runner.RunGate(context.Background(), "coverage-no-threshold")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should pass since no threshold is set
	if result.Status != StatusPassed {
		t.Errorf("expected status Passed with no threshold, got %s", result.Status)
	}
	if result.Coverage < 44.9 || result.Coverage > 45.1 {
		t.Errorf("expected coverage ~45.0%%, got %.1f%%", result.Coverage)
	}
}

func TestShouldRetry_ActionWarn(t *testing.T) {
	config := &Config{
		OnFailure: FailureConfig{
			Action:     ActionWarn,
			MaxRetries: 5,
		},
	}
	results := &CheckResults{AllPassed: false}

	got := ShouldRetry(config, results, 0)
	if got {
		t.Error("expected ShouldRetry to be false for ActionWarn")
	}
}
