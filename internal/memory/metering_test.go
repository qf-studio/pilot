package memory

import (
	"os"
	"testing"
	"time"
)

func TestMeteringCostCalculations(t *testing.T) {
	t.Run("CalculateTokenCost", func(t *testing.T) {
		tests := []struct {
			name         string
			inputTokens  int64
			outputTokens int64
			wantMin      float64
			wantMax      float64
		}{
			{"zero tokens", 0, 0, 0, 0.001},
			{"small input only", 1000, 0, 0.003, 0.004},
			{"small output only", 0, 1000, 0.017, 0.019},
			{"balanced usage", 10000, 5000, 0.126, 0.128}, // 10K input + 5K output
			{"large usage", 100000, 50000, 1.26, 1.28},    // 100K input + 50K output
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cost := CalculateTokenCost(tt.inputTokens, tt.outputTokens)
				if cost < tt.wantMin || cost > tt.wantMax {
					t.Errorf("CalculateTokenCost(%d, %d) = %f, want between %f and %f",
						tt.inputTokens, tt.outputTokens, cost, tt.wantMin, tt.wantMax)
				}
			})
		}
	})

	t.Run("CalculateComputeCost", func(t *testing.T) {
		tests := []struct {
			name       string
			durationMs int64
			want       float64
		}{
			{"zero duration", 0, 0},
			{"one minute", 60000, 0.01},
			{"five minutes", 300000, 0.05},
			{"one hour", 3600000, 0.60},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cost := CalculateComputeCost(tt.durationMs)
				if cost < tt.want-0.001 || cost > tt.want+0.001 {
					t.Errorf("CalculateComputeCost(%d) = %f, want %f",
						tt.durationMs, cost, tt.want)
				}
			})
		}
	})
}

func TestMeteringStore(t *testing.T) {
	// Create temporary directory for test database
	tmpDir, err := os.MkdirTemp("", "metering_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer func() { _ = store.Close() }()

	t.Run("RecordUsageEvent", func(t *testing.T) {
		event := &UsageEvent{
			ID:          "evt_test_001",
			Timestamp:   time.Now(),
			UserID:      "user_123",
			ProjectID:   "proj_456",
			EventType:   EventTypeTask,
			Quantity:    1,
			UnitCost:    PricePerTask,
			TotalCost:   PricePerTask,
			ExecutionID: "exec_789",
		}

		err := store.RecordUsageEvent(event)
		if err != nil {
			t.Errorf("RecordUsageEvent() error = %v", err)
		}
	})

	t.Run("RecordTaskUsage", func(t *testing.T) {
		err := store.RecordTaskUsage(
			"exec_001", // executionID
			"user_123", // userID
			"proj_456", // projectID
			120000,     // durationMs (2 minutes)
			10000,      // tokensInput
			5000,       // tokensOutput
		)
		if err != nil {
			t.Errorf("RecordTaskUsage() error = %v", err)
		}
	})

	t.Run("GetUsageSummary", func(t *testing.T) {
		// Record some test data
		for i := 0; i < 3; i++ {
			err := store.RecordTaskUsage(
				"exec_summary_"+string(rune('a'+i)),
				"user_summary",
				"proj_summary",
				60000,
				5000,
				2500,
			)
			if err != nil {
				t.Fatalf("Failed to record test data: %v", err)
			}
		}

		// Query summary
		query := UsageQuery{
			UserID: "user_summary",
			Start:  time.Now().Add(-24 * time.Hour),
			End:    time.Now().Add(time.Hour),
		}

		summary, err := store.GetUsageSummary(query)
		if err != nil {
			t.Errorf("GetUsageSummary() error = %v", err)
			return
		}

		if summary.TaskCount != 3 {
			t.Errorf("GetUsageSummary() TaskCount = %d, want 3", summary.TaskCount)
		}

		if summary.TaskCost < 3.0 {
			t.Errorf("GetUsageSummary() TaskCost = %f, want >= 3.0", summary.TaskCost)
		}

		if summary.TotalCost < summary.TaskCost {
			t.Errorf("GetUsageSummary() TotalCost (%f) < TaskCost (%f)", summary.TotalCost, summary.TaskCost)
		}
	})

	t.Run("GetUsageByProject", func(t *testing.T) {
		query := UsageQuery{
			Start: time.Now().Add(-24 * time.Hour),
			End:   time.Now().Add(time.Hour),
		}

		usage, err := store.GetUsageByProject(query)
		if err != nil {
			t.Errorf("GetUsageByProject() error = %v", err)
			return
		}

		if len(usage) == 0 {
			t.Error("GetUsageByProject() returned empty results, want at least 1 project")
		}
	})

	t.Run("GetUsageEvents", func(t *testing.T) {
		query := UsageQuery{
			Start: time.Now().Add(-24 * time.Hour),
			End:   time.Now().Add(time.Hour),
		}

		events, err := store.GetUsageEvents(query, 100)
		if err != nil {
			t.Errorf("GetUsageEvents() error = %v", err)
			return
		}

		if len(events) == 0 {
			t.Error("GetUsageEvents() returned empty results")
		}
	})

	t.Run("GetDailyUsage", func(t *testing.T) {
		query := UsageQuery{
			Start: time.Now().Add(-7 * 24 * time.Hour),
			End:   time.Now().Add(time.Hour),
		}

		daily, err := store.GetDailyUsage(query)
		if err != nil {
			t.Errorf("GetDailyUsage() error = %v", err)
			return
		}

		// Should have at least today's data
		if len(daily) == 0 {
			t.Error("GetDailyUsage() returned empty results")
		}
	})
}

func TestUsageEventTypes(t *testing.T) {
	// Verify event type constants
	types := []UsageEventType{
		EventTypeTask,
		EventTypeToken,
		EventTypeCompute,
		EventTypeStorage,
		EventTypeAPICall,
	}

	expected := []string{"task", "token", "compute", "storage", "api_call"}

	for i, eventType := range types {
		if string(eventType) != expected[i] {
			t.Errorf("EventType %d = %q, want %q", i, eventType, expected[i])
		}
	}
}

func TestPricingConstants(t *testing.T) {
	// Verify pricing is reasonable
	if PricePerTask <= 0 {
		t.Error("PricePerTask should be positive")
	}

	if TokenInputPricePerMillion <= 0 {
		t.Error("TokenInputPricePerMillion should be positive")
	}

	if TokenOutputPricePerMillion <= TokenInputPricePerMillion {
		t.Error("TokenOutputPricePerMillion should be greater than input price (output is more expensive)")
	}

	if PricePerComputeMinute <= 0 {
		t.Error("PricePerComputeMinute should be positive")
	}
}
