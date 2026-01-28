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

func TestRecordUsageEvent_Metadata(t *testing.T) {
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

	event := &UsageEvent{
		ID:          "evt_meta_001",
		Timestamp:   time.Now(),
		UserID:      "user_meta",
		ProjectID:   "proj_meta",
		EventType:   EventTypeToken,
		Quantity:    15000,
		UnitCost:    0.001,
		TotalCost:   15.0,
		ExecutionID: "exec_meta",
		Metadata: map[string]interface{}{
			"input_tokens":  10000,
			"output_tokens": 5000,
			"model":         "claude-sonnet-4-5",
		},
	}

	err = store.RecordUsageEvent(event)
	if err != nil {
		t.Errorf("RecordUsageEvent() with metadata error = %v", err)
	}

	// Retrieve and verify
	query := UsageQuery{
		UserID: "user_meta",
		Start:  time.Now().Add(-1 * time.Hour),
		End:    time.Now().Add(1 * time.Hour),
	}

	events, err := store.GetUsageEvents(query, 10)
	if err != nil {
		t.Fatalf("GetUsageEvents failed: %v", err)
	}

	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}

	found := false
	for _, e := range events {
		if e.ID == "evt_meta_001" {
			found = true
			if e.Metadata == nil {
				t.Error("metadata should not be nil")
			}
			break
		}
	}

	if !found {
		t.Error("could not find the recorded event")
	}
}

func TestGetUsageSummary_Empty(t *testing.T) {
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

	query := UsageQuery{
		UserID: "nonexistent_user",
		Start:  time.Now().Add(-24 * time.Hour),
		End:    time.Now().Add(time.Hour),
	}

	summary, err := store.GetUsageSummary(query)
	if err != nil {
		t.Fatalf("GetUsageSummary failed: %v", err)
	}

	if summary.TaskCount != 0 {
		t.Errorf("TaskCount = %d, want 0 for empty result", summary.TaskCount)
	}
	if summary.TotalCost != 0 {
		t.Errorf("TotalCost = %f, want 0 for empty result", summary.TotalCost)
	}
}

func TestGetUsageSummary_WithProjectFilter(t *testing.T) {
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

	// Record usage for different projects
	_ = store.RecordTaskUsage("exec_proj_a", "user_filter", "proj_a", 60000, 5000, 2500)
	_ = store.RecordTaskUsage("exec_proj_a2", "user_filter", "proj_a", 60000, 5000, 2500)
	_ = store.RecordTaskUsage("exec_proj_b", "user_filter", "proj_b", 60000, 5000, 2500)

	// Query with project filter
	query := UsageQuery{
		UserID:    "user_filter",
		ProjectID: "proj_a",
		Start:     time.Now().Add(-1 * time.Hour),
		End:       time.Now().Add(1 * time.Hour),
	}

	summary, err := store.GetUsageSummary(query)
	if err != nil {
		t.Fatalf("GetUsageSummary failed: %v", err)
	}

	if summary.TaskCount != 2 {
		t.Errorf("TaskCount = %d, want 2 for proj_a only", summary.TaskCount)
	}
}

func TestGetUsageEvents_WithTypeFilter(t *testing.T) {
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

	// Record various event types
	_ = store.RecordTaskUsage("exec_type_1", "user_type", "proj_type", 60000, 5000, 2500)

	query := UsageQuery{
		UserID:    "user_type",
		Start:     time.Now().Add(-1 * time.Hour),
		End:       time.Now().Add(1 * time.Hour),
		EventType: EventTypeTask,
	}

	events, err := store.GetUsageEvents(query, 100)
	if err != nil {
		t.Fatalf("GetUsageEvents failed: %v", err)
	}

	for _, e := range events {
		if e.EventType != EventTypeTask {
			t.Errorf("event type = %s, want %s", e.EventType, EventTypeTask)
		}
	}
}

func TestGetDailyUsage_MultipleTypes(t *testing.T) {
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

	// Record multiple tasks today
	for i := 0; i < 3; i++ {
		_ = store.RecordTaskUsage(
			"exec_daily_"+string(rune('a'+i)),
			"user_daily",
			"proj_daily",
			60000,
			5000,
			2500,
		)
	}

	query := UsageQuery{
		UserID: "user_daily",
		Start:  time.Now().Add(-24 * time.Hour),
		End:    time.Now().Add(24 * time.Hour),
	}

	daily, err := store.GetDailyUsage(query)
	if err != nil {
		t.Fatalf("GetDailyUsage failed: %v", err)
	}

	if len(daily) == 0 {
		t.Fatal("expected at least one day of usage")
	}

	today := daily[0]
	if today.TaskCount != 3 {
		t.Errorf("TaskCount = %d, want 3", today.TaskCount)
	}
	if today.TotalCost <= 0 {
		t.Error("TotalCost should be positive")
	}
}

func TestCheckUsageThresholds(t *testing.T) {
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

	// Record many tasks to trigger threshold
	for i := 0; i < 150; i++ {
		_ = store.RecordTaskUsage(
			"exec_threshold_"+string(rune(i)),
			"user_threshold",
			"proj_threshold",
			60000,
			10000,
			5000,
		)
	}

	alerts, err := store.CheckUsageThresholds("user_threshold")
	if err != nil {
		t.Fatalf("CheckUsageThresholds failed: %v", err)
	}

	// Should have cost threshold alert (150 tasks * $1/task = $150 > $100)
	if len(alerts) == 0 {
		t.Error("expected at least one alert for high usage")
	}

	hasMonthlyAlert := false
	for _, alert := range alerts {
		if alert != "" {
			hasMonthlyAlert = true
			break
		}
	}

	if !hasMonthlyAlert {
		t.Log("Note: threshold alerts may depend on month boundaries")
	}
}

func TestCalculateTokenCost_EdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		inputTokens  int64
		outputTokens int64
		wantZero     bool
	}{
		{"both zero", 0, 0, true},
		{"negative input", -1000, 0, false}, // Should handle gracefully
		{"very large input", 1000000000, 0, false},
		{"very large output", 0, 1000000000, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost := CalculateTokenCost(tt.inputTokens, tt.outputTokens)
			if tt.wantZero && cost != 0 {
				t.Errorf("CalculateTokenCost() = %f, want 0", cost)
			}
		})
	}
}

func TestRecordTaskUsage_ZeroTokens(t *testing.T) {
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

	// Should not fail with zero tokens
	err = store.RecordTaskUsage("exec_zero_tokens", "user_zero", "proj_zero", 60000, 0, 0)
	if err != nil {
		t.Errorf("RecordTaskUsage() with zero tokens error = %v", err)
	}

	// Verify only task and compute events were recorded (no token event)
	query := UsageQuery{
		UserID: "user_zero",
		Start:  time.Now().Add(-1 * time.Hour),
		End:    time.Now().Add(1 * time.Hour),
	}

	events, _ := store.GetUsageEvents(query, 100)
	hasTokenEvent := false
	for _, e := range events {
		if e.EventType == EventTypeToken {
			hasTokenEvent = true
			break
		}
	}

	if hasTokenEvent {
		t.Error("should not record token event when tokens are zero")
	}
}

func TestRecordTaskUsage_ZeroDuration(t *testing.T) {
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

	// Should not fail with zero duration
	err = store.RecordTaskUsage("exec_zero_dur", "user_zero_dur", "proj_zero_dur", 0, 1000, 500)
	if err != nil {
		t.Errorf("RecordTaskUsage() with zero duration error = %v", err)
	}

	// Verify no compute event was recorded
	query := UsageQuery{
		UserID: "user_zero_dur",
		Start:  time.Now().Add(-1 * time.Hour),
		End:    time.Now().Add(1 * time.Hour),
	}

	events, _ := store.GetUsageEvents(query, 100)
	hasComputeEvent := false
	for _, e := range events {
		if e.EventType == EventTypeCompute {
			hasComputeEvent = true
			break
		}
	}

	if hasComputeEvent {
		t.Error("should not record compute event when duration is zero")
	}
}
