package executor

import (
	"context"
	"testing"
)

// mockQualityChecker implements QualityChecker for testing
type mockQualityChecker struct {
	outcome *QualityOutcome
	err     error
}

func (m *mockQualityChecker) Check(ctx context.Context) (*QualityOutcome, error) {
	return m.outcome, m.err
}

func TestQualityCheckerFactory(t *testing.T) {
	tests := []struct {
		name    string
		outcome *QualityOutcome
		wantErr bool
	}{
		{
			name: "all gates pass",
			outcome: &QualityOutcome{
				Passed:      true,
				ShouldRetry: false,
				Attempt:     0,
			},
			wantErr: false,
		},
		{
			name: "gates fail with retry",
			outcome: &QualityOutcome{
				Passed:        false,
				ShouldRetry:   true,
				RetryFeedback: "test failed: expected true got false",
				Attempt:       0,
			},
			wantErr: false,
		},
		{
			name: "gates fail no retry",
			outcome: &QualityOutcome{
				Passed:      false,
				ShouldRetry: false,
				Attempt:     2,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := &mockQualityChecker{
				outcome: tt.outcome,
			}

			result, err := checker.Check(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("Check() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if result.Passed != tt.outcome.Passed {
				t.Errorf("Check() Passed = %v, want %v", result.Passed, tt.outcome.Passed)
			}
			if result.ShouldRetry != tt.outcome.ShouldRetry {
				t.Errorf("Check() ShouldRetry = %v, want %v", result.ShouldRetry, tt.outcome.ShouldRetry)
			}
		})
	}
}

func TestRunnerSetQualityCheckerFactory(t *testing.T) {
	runner := NewRunner()

	// Initially nil
	if runner.qualityCheckerFactory != nil {
		t.Error("Expected qualityCheckerFactory to be nil initially")
	}

	// Set factory
	factory := func(taskID, projectPath string) QualityChecker {
		return &mockQualityChecker{
			outcome: &QualityOutcome{Passed: true},
		}
	}
	runner.SetQualityCheckerFactory(factory)

	if runner.qualityCheckerFactory == nil {
		t.Error("Expected qualityCheckerFactory to be set")
	}

	// Test factory creates checker
	checker := runner.qualityCheckerFactory("task-1", "/tmp/project")
	if checker == nil {
		t.Error("Expected factory to create a checker")
	}

	outcome, err := checker.Check(context.Background())
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !outcome.Passed {
		t.Error("Expected outcome to pass")
	}
}

func TestQualityOutcomeFields(t *testing.T) {
	outcome := &QualityOutcome{
		Passed:        false,
		ShouldRetry:   true,
		RetryFeedback: "Build failed:\n  go build ./...\n  undefined: foo",
		Attempt:       1,
	}

	if outcome.Passed {
		t.Error("Expected Passed to be false")
	}
	if !outcome.ShouldRetry {
		t.Error("Expected ShouldRetry to be true")
	}
	if outcome.RetryFeedback == "" {
		t.Error("Expected RetryFeedback to be set")
	}
	if outcome.Attempt != 1 {
		t.Errorf("Expected Attempt to be 1, got %d", outcome.Attempt)
	}
}
