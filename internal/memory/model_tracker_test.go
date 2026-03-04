package memory

import (
	"os"
	"testing"
	"time"
)

func newTestTracker(t *testing.T) (*ModelOutcomeTracker, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "model-tracker-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	store, err := NewStore(tmpDir)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("failed to create store: %v", err)
	}
	tracker := NewModelOutcomeTracker(store)
	cleanup := func() {
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return tracker, cleanup
}

func TestRecordOutcome(t *testing.T) {
	tr, cleanup := newTestTracker(t)
	defer cleanup()

	err := tr.RecordOutcome("bug-fix", "haiku", "success", 1000, 5*time.Second)
	if err != nil {
		t.Fatalf("RecordOutcome failed: %v", err)
	}

	// Verify it was stored by checking failure rate (should be 0)
	rate := tr.GetFailureRate("bug-fix", "haiku")
	if rate != 0.0 {
		t.Errorf("expected failure rate 0.0, got %f", rate)
	}
}

func TestFailureRateCalculation(t *testing.T) {
	tests := []struct {
		name     string
		outcomes []string // "success" or "failure"
		wantRate float64
	}{
		{
			name:     "all success",
			outcomes: []string{"success", "success", "success"},
			wantRate: 0.0,
		},
		{
			name:     "all failure",
			outcomes: []string{"failure", "failure", "failure"},
			wantRate: 1.0,
		},
		{
			name:     "mixed 50%",
			outcomes: []string{"success", "failure", "success", "failure"},
			wantRate: 0.5,
		},
		{
			name:     "one of three",
			outcomes: []string{"success", "failure", "success"},
			wantRate: 1.0 / 3.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, cl := newTestTracker(t)
			defer cl()

			for _, o := range tt.outcomes {
				if err := tr.RecordOutcome("test-task", "sonnet", o, 500, time.Second); err != nil {
					t.Fatalf("RecordOutcome: %v", err)
				}
			}

			got := tr.GetFailureRate("test-task", "sonnet")
			if diff := got - tt.wantRate; diff > 0.001 || diff < -0.001 {
				t.Errorf("failure rate = %f, want %f", got, tt.wantRate)
			}
		})
	}
}

func TestFailureRateWindowLimit(t *testing.T) {
	tracker, cleanup := newTestTracker(t)
	defer cleanup()

	// Record 10 successes then 10 failures; window=10 should only see failures
	for i := 0; i < 10; i++ {
		if err := tracker.RecordOutcome("task", "haiku", "success", 100, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		if err := tracker.RecordOutcome("task", "haiku", "failure", 100, time.Second); err != nil {
			t.Fatal(err)
		}
	}

	rate := tracker.GetFailureRate("task", "haiku")
	if rate != 1.0 {
		t.Errorf("expected 1.0 (last 10 all failures), got %f", rate)
	}
}

func TestFailureRateEmptyData(t *testing.T) {
	tracker, cleanup := newTestTracker(t)
	defer cleanup()

	rate := tracker.GetFailureRate("nonexistent", "haiku")
	if rate != 0.0 {
		t.Errorf("expected 0.0 for empty data, got %f", rate)
	}
}

func TestShouldEscalate(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		outcomes    []string
		wantEscal   bool
		wantTarget  string
	}{
		{
			name:       "haiku high failure -> sonnet",
			model:      "haiku",
			outcomes:   []string{"failure", "failure", "failure", "success"},
			wantEscal:  true,
			wantTarget: "sonnet",
		},
		{
			name:       "sonnet high failure -> opus",
			model:      "sonnet",
			outcomes:   []string{"failure", "failure", "failure", "success"},
			wantEscal:  true,
			wantTarget: "opus",
		},
		{
			name:       "opus high failure -> no escalation",
			model:      "opus",
			outcomes:   []string{"failure", "failure", "failure"},
			wantEscal:  false,
			wantTarget: "",
		},
		{
			name:       "haiku low failure -> no escalation",
			model:      "haiku",
			outcomes:   []string{"success", "success", "success", "failure"},
			wantEscal:  false,
			wantTarget: "",
		},
		{
			name:       "unknown model -> no escalation",
			model:      "unknown",
			outcomes:   []string{"failure", "failure", "failure"},
			wantEscal:  false,
			wantTarget: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker, cleanup := newTestTracker(t)
			defer cleanup()

			for _, o := range tt.outcomes {
				if err := tracker.RecordOutcome("task", tt.model, o, 500, time.Second); err != nil {
					t.Fatal(err)
				}
			}

			gotEscal, gotTarget := tracker.ShouldEscalate("task", tt.model)
			if gotEscal != tt.wantEscal {
				t.Errorf("ShouldEscalate = %v, want %v", gotEscal, tt.wantEscal)
			}
			if gotTarget != tt.wantTarget {
				t.Errorf("escalation target = %q, want %q", gotTarget, tt.wantTarget)
			}
		})
	}
}

func TestThresholdBoundary(t *testing.T) {
	tracker, cleanup := newTestTracker(t)
	defer cleanup()

	// Exactly 30% failure (3/10) — should NOT escalate (threshold is >30%, not >=)
	for i := 0; i < 7; i++ {
		if err := tracker.RecordOutcome("task", "haiku", "success", 100, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := tracker.RecordOutcome("task", "haiku", "failure", 100, time.Second); err != nil {
			t.Fatal(err)
		}
	}

	escalate, _ := tracker.ShouldEscalate("task", "haiku")
	if escalate {
		t.Error("should not escalate at exactly 30% threshold")
	}

	// Add one more failure (4/10 = 40%) — should escalate
	if err := tracker.RecordOutcome("task", "haiku", "failure", 100, time.Second); err != nil {
		t.Fatal(err)
	}

	escalate, target := tracker.ShouldEscalate("task", "haiku")
	if !escalate {
		t.Error("should escalate at 40% failure rate")
	}
	if target != "sonnet" {
		t.Errorf("expected sonnet, got %q", target)
	}
}

func TestCustomThreshold(t *testing.T) {
	tracker, cleanup := newTestTracker(t)
	defer cleanup()

	tracker.WithFailureThreshold(0.5)

	// 40% failure — below custom 50% threshold
	for i := 0; i < 6; i++ {
		if err := tracker.RecordOutcome("task", "haiku", "success", 100, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4; i++ {
		if err := tracker.RecordOutcome("task", "haiku", "failure", 100, time.Second); err != nil {
			t.Fatal(err)
		}
	}

	escalate, _ := tracker.ShouldEscalate("task", "haiku")
	if escalate {
		t.Error("should not escalate at 40% with 50% threshold")
	}
}

func TestShouldEscalateEmptyData(t *testing.T) {
	tracker, cleanup := newTestTracker(t)
	defer cleanup()

	escalate, target := tracker.ShouldEscalate("nonexistent", "haiku")
	if escalate {
		t.Error("should not escalate with no data")
	}
	if target != "" {
		t.Errorf("expected empty target, got %q", target)
	}
}
