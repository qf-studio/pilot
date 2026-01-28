package quality

import (
	"context"
	"testing"
	"time"
)

func TestNewExecutor(t *testing.T) {
	cfg := &ExecutorConfig{
		Config:      DefaultConfig(),
		ProjectPath: "/tmp/test",
		TaskID:      "test-task-1",
	}

	executor := NewExecutor(cfg)

	if executor == nil {
		t.Fatal("expected non-nil executor")
	}
	if executor.config != cfg.Config {
		t.Error("config not set correctly")
	}
	if executor.taskID != cfg.TaskID {
		t.Error("taskID not set correctly")
	}
	if executor.runner == nil {
		t.Error("runner not initialized")
	}
}

func TestExecutor_Check_Disabled(t *testing.T) {
	cfg := &ExecutorConfig{
		Config: &Config{
			Enabled: false,
		},
		ProjectPath: "/tmp",
		TaskID:      "task-1",
	}

	executor := NewExecutor(cfg)
	outcome, err := executor.Check(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !outcome.Passed {
		t.Error("expected Passed to be true when disabled")
	}
	if outcome.Attempt != 0 {
		t.Errorf("expected Attempt 0, got %d", outcome.Attempt)
	}
}

func TestExecutor_Check_Passing(t *testing.T) {
	cfg := &ExecutorConfig{
		Config: &Config{
			Enabled: true,
			Gates: []*Gate{
				{
					Name:     "echo",
					Type:     GateCustom,
					Command:  "echo 'test'",
					Required: true,
					Timeout:  5 * time.Second,
				},
			},
		},
		ProjectPath: "/tmp",
		TaskID:      "task-2",
	}

	executor := NewExecutor(cfg)
	outcome, err := executor.Check(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !outcome.Passed {
		t.Error("expected Passed to be true for passing gates")
	}
	if outcome.ShouldRetry {
		t.Error("expected ShouldRetry to be false when passed")
	}
	if outcome.Results == nil {
		t.Error("expected non-nil Results")
	}
}

func TestExecutor_Check_Failing(t *testing.T) {
	cfg := &ExecutorConfig{
		Config: &Config{
			Enabled: true,
			Gates: []*Gate{
				{
					Name:       "fail",
					Type:       GateCustom,
					Command:    "exit 1",
					Required:   true,
					Timeout:    5 * time.Second,
					MaxRetries: 0,
				},
			},
			OnFailure: FailureConfig{
				Action:     ActionRetry,
				MaxRetries: 2,
			},
		},
		ProjectPath: "/tmp",
		TaskID:      "task-3",
	}

	executor := NewExecutor(cfg)
	outcome, err := executor.Check(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Passed {
		t.Error("expected Passed to be false for failing gates")
	}
	if !outcome.ShouldRetry {
		t.Error("expected ShouldRetry to be true on first failure")
	}
	if outcome.RetryFeedback == "" {
		t.Error("expected RetryFeedback to be non-empty")
	}
}

func TestExecutor_CheckWithAttempt(t *testing.T) {
	tests := []struct {
		name          string
		attempt       int
		maxRetries    int
		shouldRetry   bool
		gatePasses    bool
	}{
		{
			name:        "first attempt failure should retry",
			attempt:     0,
			maxRetries:  2,
			shouldRetry: true,
			gatePasses:  false,
		},
		{
			name:        "last attempt failure should not retry",
			attempt:     2,
			maxRetries:  2,
			shouldRetry: false,
			gatePasses:  false,
		},
		{
			name:        "passing gate should not retry",
			attempt:     0,
			maxRetries:  2,
			shouldRetry: false,
			gatePasses:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := "exit 1"
			if tt.gatePasses {
				command = "true"
			}

			cfg := &ExecutorConfig{
				Config: &Config{
					Enabled: true,
					Gates: []*Gate{
						{
							Name:       "test",
							Type:       GateCustom,
							Command:    command,
							Required:   true,
							Timeout:    5 * time.Second,
							MaxRetries: 0,
						},
					},
					OnFailure: FailureConfig{
						Action:     ActionRetry,
						MaxRetries: tt.maxRetries,
					},
				},
				ProjectPath: "/tmp",
				TaskID:      "task",
			}

			executor := NewExecutor(cfg)
			outcome, err := executor.CheckWithAttempt(context.Background(), tt.attempt)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if outcome.Attempt != tt.attempt {
				t.Errorf("expected Attempt %d, got %d", tt.attempt, outcome.Attempt)
			}
			if outcome.ShouldRetry != tt.shouldRetry {
				t.Errorf("expected ShouldRetry %v, got %v", tt.shouldRetry, outcome.ShouldRetry)
			}
		})
	}
}

func TestExecutor_OnProgress(t *testing.T) {
	cfg := &ExecutorConfig{
		Config: &Config{
			Enabled: true,
			Gates: []*Gate{
				{
					Name:     "progress-test",
					Type:     GateCustom,
					Command:  "echo 'hello'",
					Required: true,
					Timeout:  5 * time.Second,
				},
			},
		},
		ProjectPath: "/tmp",
		TaskID:      "task-progress",
	}

	executor := NewExecutor(cfg)

	var progressCalls []struct {
		gateName string
		status   GateStatus
		message  string
	}

	executor.OnProgress(func(gateName string, status GateStatus, message string) {
		progressCalls = append(progressCalls, struct {
			gateName string
			status   GateStatus
			message  string
		}{gateName, status, message})
	})

	_, err := executor.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(progressCalls) == 0 {
		t.Error("expected progress callback to be called")
	}

	// Should have at least running and passed/failed status
	hasRunning := false
	hasFinal := false
	for _, call := range progressCalls {
		if call.status == StatusRunning {
			hasRunning = true
		}
		if call.status == StatusPassed || call.status == StatusFailed {
			hasFinal = true
		}
	}

	if !hasRunning {
		t.Error("expected running status in progress calls")
	}
	if !hasFinal {
		t.Error("expected final status in progress calls")
	}
}

func TestGenerateReport(t *testing.T) {
	tests := []struct {
		name           string
		results        *CheckResults
		attempt        int
		expectPassed   bool
		expectSummary  string
		expectGateLen  int
	}{
		{
			name: "all passed",
			results: &CheckResults{
				AllPassed: true,
				TotalTime: 5 * time.Second,
				Results: []*Result{
					{GateName: "build", Status: StatusPassed, Duration: 2 * time.Second},
					{GateName: "test", Status: StatusPassed, Duration: 3 * time.Second},
				},
			},
			attempt:       0,
			expectPassed:  true,
			expectSummary: "All 2 quality gates passed",
			expectGateLen: 2,
		},
		{
			name: "mixed results",
			results: &CheckResults{
				AllPassed: false,
				TotalTime: 10 * time.Second,
				Results: []*Result{
					{GateName: "build", Status: StatusPassed, Duration: 2 * time.Second},
					{GateName: "test", Status: StatusFailed, Duration: 5 * time.Second, Error: "tests failed"},
					{GateName: "lint", Status: StatusSkipped, Duration: 0},
				},
			},
			attempt:       1,
			expectPassed:  false,
			expectSummary: "1 passed, 1 failed, 1 skipped",
			expectGateLen: 3,
		},
		{
			name: "all failed",
			results: &CheckResults{
				AllPassed: false,
				TotalTime: 3 * time.Second,
				Results: []*Result{
					{GateName: "build", Status: StatusFailed, Duration: 2 * time.Second, Error: "compile error"},
					{GateName: "test", Status: StatusFailed, Duration: 1 * time.Second, Error: "test error"},
				},
			},
			attempt:       2,
			expectPassed:  false,
			expectSummary: "0 passed, 2 failed, 0 skipped",
			expectGateLen: 2,
		},
		{
			name: "with coverage",
			results: &CheckResults{
				AllPassed: true,
				TotalTime: 8 * time.Second,
				Results: []*Result{
					{GateName: "coverage", Status: StatusPassed, Duration: 8 * time.Second, Coverage: 85.5},
				},
			},
			attempt:       0,
			expectPassed:  true,
			expectSummary: "All 1 quality gates passed",
			expectGateLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := GenerateReport("task-123", tt.results, tt.attempt)

			if report.TaskID != "task-123" {
				t.Errorf("expected TaskID 'task-123', got '%s'", report.TaskID)
			}
			if report.Passed != tt.expectPassed {
				t.Errorf("expected Passed %v, got %v", tt.expectPassed, report.Passed)
			}
			if report.Summary != tt.expectSummary {
				t.Errorf("expected Summary '%s', got '%s'", tt.expectSummary, report.Summary)
			}
			if len(report.Gates) != tt.expectGateLen {
				t.Errorf("expected %d gates, got %d", tt.expectGateLen, len(report.Gates))
			}
			if report.Attempt != tt.attempt {
				t.Errorf("expected Attempt %d, got %d", tt.attempt, report.Attempt)
			}
			if report.TotalTime != tt.results.TotalTime {
				t.Errorf("expected TotalTime %v, got %v", tt.results.TotalTime, report.TotalTime)
			}
		})
	}
}

func TestGenerateReport_GateDetails(t *testing.T) {
	results := &CheckResults{
		AllPassed: false,
		TotalTime: 10 * time.Second,
		Results: []*Result{
			{
				GateName: "coverage",
				Status:   StatusPassed,
				Duration: 5 * time.Second,
				Coverage: 92.5,
			},
			{
				GateName: "test",
				Status:   StatusFailed,
				Duration: 3 * time.Second,
				Error:    "assertion failed",
			},
		},
	}

	report := GenerateReport("task-detail", results, 0)

	// Check coverage gate details
	coverageGate := report.Gates[0]
	if coverageGate.Name != "coverage" {
		t.Errorf("expected gate name 'coverage', got '%s'", coverageGate.Name)
	}
	if coverageGate.Status != string(StatusPassed) {
		t.Errorf("expected status 'passed', got '%s'", coverageGate.Status)
	}
	if coverageGate.Coverage != 92.5 {
		t.Errorf("expected coverage 92.5, got %f", coverageGate.Coverage)
	}

	// Check failed gate details
	testGate := report.Gates[1]
	if testGate.Error != "assertion failed" {
		t.Errorf("expected error 'assertion failed', got '%s'", testGate.Error)
	}
}

func TestFormatReportForNotification(t *testing.T) {
	tests := []struct {
		name             string
		report           *GateReport
		expectContains   []string
		expectNotContain []string
	}{
		{
			name: "passed report",
			report: &GateReport{
				TaskID:    "task-1",
				Passed:    true,
				Summary:   "All 2 quality gates passed",
				TotalTime: 5 * time.Second,
				Gates: []GateReportItem{
					{Name: "build", Status: string(StatusPassed), Duration: 2 * time.Second},
					{Name: "test", Status: string(StatusPassed), Duration: 3 * time.Second},
				},
			},
			expectContains:   []string{"Quality Gates Passed", "task-1", "All 2 quality gates passed", "build", "test"},
			expectNotContain: []string{"Quality Gates Failed"},
		},
		{
			name: "failed report",
			report: &GateReport{
				TaskID:    "task-2",
				Passed:    false,
				Summary:   "1 passed, 1 failed",
				TotalTime: 8 * time.Second,
				Gates: []GateReportItem{
					{Name: "build", Status: string(StatusPassed), Duration: 2 * time.Second},
					{Name: "test", Status: string(StatusFailed), Duration: 6 * time.Second, Error: "tests failed"},
				},
			},
			expectContains:   []string{"Quality Gates Failed", "task-2", "tests failed"},
			expectNotContain: []string{"Quality Gates Passed"},
		},
		{
			name: "report with coverage",
			report: &GateReport{
				TaskID:    "task-3",
				Passed:    true,
				Summary:   "All gates passed",
				TotalTime: 10 * time.Second,
				Gates: []GateReportItem{
					{Name: "coverage", Status: string(StatusPassed), Duration: 10 * time.Second, Coverage: 85.3},
				},
			},
			expectContains: []string{"85.3%"},
		},
		{
			name: "report with skipped gate",
			report: &GateReport{
				TaskID:    "task-4",
				Passed:    true,
				Summary:   "1 passed, 0 failed, 1 skipped",
				TotalTime: 5 * time.Second,
				Gates: []GateReportItem{
					{Name: "build", Status: string(StatusPassed), Duration: 5 * time.Second},
					{Name: "optional", Status: string(StatusSkipped), Duration: 0},
				},
			},
			expectContains: []string{"optional"},
		},
		{
			name: "report with pending gate",
			report: &GateReport{
				TaskID:    "task-5",
				Passed:    false,
				Summary:   "0 passed, 0 failed",
				TotalTime: 0,
				Gates: []GateReportItem{
					{Name: "pending-gate", Status: string(StatusPending), Duration: 0},
				},
			},
			expectContains: []string{"pending-gate"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := FormatReportForNotification(tt.report)

			for _, expected := range tt.expectContains {
				if !containsString(output, expected) {
					t.Errorf("expected output to contain '%s', got: %s", expected, output)
				}
			}

			for _, notExpected := range tt.expectNotContain {
				if containsString(output, notExpected) {
					t.Errorf("expected output NOT to contain '%s', got: %s", notExpected, output)
				}
			}
		})
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
