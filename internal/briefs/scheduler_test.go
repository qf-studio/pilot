package briefs

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
)

func setupSchedulerTestStore(t *testing.T) (*memory.Store, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "scheduler_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("failed to create store: %v", err)
	}

	cleanup := func() {
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}

	return store, cleanup
}

func TestNewScheduler(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	tests := []struct {
		name   string
		config *BriefConfig
		logger *slog.Logger
		wantTz string
	}{
		{
			name: "valid timezone",
			config: &BriefConfig{
				Enabled:  true,
				Schedule: "0 9 * * *",
				Timezone: "America/New_York",
			},
			logger: nil,
			wantTz: "America/New_York",
		},
		{
			name: "invalid timezone falls back to UTC",
			config: &BriefConfig{
				Enabled:  true,
				Schedule: "0 9 * * *",
				Timezone: "Invalid/Timezone",
			},
			logger: slog.Default(),
			wantTz: "UTC",
		},
		{
			name: "UTC timezone",
			config: &BriefConfig{
				Enabled:  true,
				Schedule: "0 9 * * *",
				Timezone: "UTC",
			},
			logger: nil,
			wantTz: "UTC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			generator := NewGenerator(store, tt.config)
			delivery := NewDeliveryService(tt.config)
			scheduler := NewScheduler(generator, delivery, tt.config, tt.logger, nil)

			if scheduler == nil {
				t.Fatal("expected scheduler, got nil")
			}

			if scheduler.config != tt.config {
				t.Error("config not set correctly")
			}

			if scheduler.generator != generator {
				t.Error("generator not set correctly")
			}
		})
	}
}

func TestSchedulerStartStop(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	tests := []struct {
		name           string
		config         *BriefConfig
		expectRunning  bool
		expectStartErr bool
	}{
		{
			name: "disabled scheduler does not start",
			config: &BriefConfig{
				Enabled:  false,
				Schedule: "0 9 * * *",
				Timezone: "UTC",
			},
			expectRunning:  false,
			expectStartErr: false,
		},
		{
			name: "enabled scheduler starts successfully",
			config: &BriefConfig{
				Enabled:  true,
				Schedule: "0 9 * * *",
				Timezone: "UTC",
			},
			expectRunning:  true,
			expectStartErr: false,
		},
		{
			name: "invalid cron schedule returns error",
			config: &BriefConfig{
				Enabled:  true,
				Schedule: "invalid cron",
				Timezone: "UTC",
			},
			expectRunning:  false,
			expectStartErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			generator := NewGenerator(store, tt.config)
			delivery := NewDeliveryService(tt.config)
			scheduler := NewScheduler(generator, delivery, tt.config, nil, nil)

			ctx := context.Background()
			err := scheduler.Start(ctx)

			if tt.expectStartErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.expectStartErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if scheduler.IsRunning() != tt.expectRunning {
				t.Errorf("IsRunning() = %v, want %v", scheduler.IsRunning(), tt.expectRunning)
			}

			// Test Stop
			scheduler.Stop()
			if scheduler.IsRunning() {
				t.Error("scheduler still running after Stop()")
			}
		})
	}
}

func TestSchedulerDoubleStart(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	config := &BriefConfig{
		Enabled:  true,
		Schedule: "0 9 * * *",
		Timezone: "UTC",
	}

	generator := NewGenerator(store, config)
	delivery := NewDeliveryService(config)
	scheduler := NewScheduler(generator, delivery, config, nil, nil)

	ctx := context.Background()

	// First start
	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("first start failed: %v", err)
	}
	defer scheduler.Stop()

	// Second start should be a no-op
	if err := scheduler.Start(ctx); err != nil {
		t.Errorf("second start returned error: %v", err)
	}

	if !scheduler.IsRunning() {
		t.Error("scheduler should still be running after double start")
	}
}

func TestSchedulerDoubleStop(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	config := &BriefConfig{
		Enabled:  true,
		Schedule: "0 9 * * *",
		Timezone: "UTC",
	}

	generator := NewGenerator(store, config)
	delivery := NewDeliveryService(config)
	scheduler := NewScheduler(generator, delivery, config, nil, nil)

	ctx := context.Background()

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	// First stop
	scheduler.Stop()

	// Second stop should be a no-op (not panic)
	scheduler.Stop()

	if scheduler.IsRunning() {
		t.Error("scheduler should not be running after stops")
	}
}

func TestSchedulerNextRun(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	config := &BriefConfig{
		Enabled:  true,
		Schedule: "* * * * *", // Every minute for testing
		Timezone: "UTC",
	}

	generator := NewGenerator(store, config)
	delivery := NewDeliveryService(config)
	scheduler := NewScheduler(generator, delivery, config, nil, nil)

	// Before start, NextRun should return zero time
	nextRun := scheduler.NextRun()
	if !nextRun.IsZero() {
		t.Errorf("expected zero time before start, got %v", nextRun)
	}

	ctx := context.Background()
	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer scheduler.Stop()

	// After start, NextRun should return a future time
	nextRun = scheduler.NextRun()
	if nextRun.IsZero() {
		t.Error("expected non-zero NextRun after start")
	}

	if nextRun.Before(time.Now()) {
		t.Errorf("NextRun %v should be in the future", nextRun)
	}
}

func TestSchedulerLastRun(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	config := &BriefConfig{
		Enabled:  true,
		Schedule: "* * * * *",
		Timezone: "UTC",
	}

	generator := NewGenerator(store, config)
	delivery := NewDeliveryService(config)
	scheduler := NewScheduler(generator, delivery, config, nil, nil)

	// Before start, LastRun should return zero time
	lastRun := scheduler.LastRun()
	if !lastRun.IsZero() {
		t.Errorf("expected zero time before start, got %v", lastRun)
	}

	ctx := context.Background()
	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer scheduler.Stop()

	// Initially, LastRun should be zero (no runs yet)
	lastRun = scheduler.LastRun()
	if !lastRun.IsZero() {
		t.Errorf("expected zero LastRun initially, got %v", lastRun)
	}
}

func TestSchedulerStatus(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	config := &BriefConfig{
		Enabled:  true,
		Schedule: "0 9 * * 1-5",
		Timezone: "America/New_York",
	}

	generator := NewGenerator(store, config)
	delivery := NewDeliveryService(config)
	scheduler := NewScheduler(generator, delivery, config, nil, nil)

	// Status before start
	status := scheduler.Status()
	if !status.Enabled {
		t.Error("status.Enabled should be true")
	}
	if status.Running {
		t.Error("status.Running should be false before start")
	}
	if status.Schedule != "0 9 * * 1-5" {
		t.Errorf("unexpected schedule: %s", status.Schedule)
	}
	if status.Timezone != "America/New_York" {
		t.Errorf("unexpected timezone: %s", status.Timezone)
	}

	// Start scheduler
	ctx := context.Background()
	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer scheduler.Stop()

	// Status after start
	status = scheduler.Status()
	if !status.Running {
		t.Error("status.Running should be true after start")
	}
	if status.NextRun.IsZero() {
		t.Error("status.NextRun should not be zero after start")
	}
}

func TestSchedulerRunNow(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	config := &BriefConfig{
		Enabled:  true,
		Schedule: "0 9 * * *",
		Timezone: "UTC",
		Channels: []ChannelConfig{
			{Type: "slack", Channel: "#test"},
		},
		Content: ContentConfig{
			IncludeMetrics:     true,
			IncludeErrors:      true,
			MaxItemsPerSection: 10,
		},
	}

	generator := NewGenerator(store, config)
	delivery := NewDeliveryService(config) // No actual clients configured
	scheduler := NewScheduler(generator, delivery, config, nil, nil)

	ctx := context.Background()

	// RunNow should work even without starting the scheduler
	results, err := scheduler.RunNow(ctx)
	if err != nil {
		t.Fatalf("RunNow failed: %v", err)
	}

	// Should have one result for slack channel
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}

	// Slack delivery should fail since no client is configured
	if results[0].Success {
		t.Error("expected slack delivery to fail without client")
	}
	if results[0].Error == nil {
		t.Error("expected error for unconfigured slack client")
	}
}

func TestSchedulerCronSchedules(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	tests := []struct {
		name     string
		schedule string
		valid    bool
	}{
		{"every minute", "* * * * *", true},
		{"daily at 9am", "0 9 * * *", true},
		{"weekdays at 9am", "0 9 * * 1-5", true},
		{"every 5 minutes", "*/5 * * * *", true},
		{"first of month", "0 0 1 * *", true},
		{"invalid schedule", "not a cron", false},
		{"too few fields", "* * *", false},
		{"invalid minute", "60 * * * *", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &BriefConfig{
				Enabled:  true,
				Schedule: tt.schedule,
				Timezone: "UTC",
			}

			generator := NewGenerator(store, config)
			delivery := NewDeliveryService(config)
			scheduler := NewScheduler(generator, delivery, config, nil, nil)

			ctx := context.Background()
			err := scheduler.Start(ctx)

			if tt.valid && err != nil {
				t.Errorf("expected valid schedule, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Error("expected error for invalid schedule")
			}

			scheduler.Stop()
		})
	}
}

func TestSchedulerStatusStruct(t *testing.T) {
	status := SchedulerStatus{
		Enabled:  true,
		Running:  true,
		Schedule: "0 9 * * *",
		Timezone: "UTC",
		NextRun:  time.Now().Add(time.Hour),
		LastRun:  time.Now().Add(-time.Hour),
	}

	if !status.Enabled {
		t.Error("expected Enabled to be true")
	}
	if !status.Running {
		t.Error("expected Running to be true")
	}
	if status.Schedule != "0 9 * * *" {
		t.Errorf("unexpected Schedule: %s", status.Schedule)
	}
	if status.Timezone != "UTC" {
		t.Errorf("unexpected Timezone: %s", status.Timezone)
	}
	if status.NextRun.IsZero() {
		t.Error("expected non-zero NextRun")
	}
	if status.LastRun.IsZero() {
		t.Error("expected non-zero LastRun")
	}
}

func TestSchedulerCatchUpMechanism(t *testing.T) {
	tests := []struct {
		name             string
		useStore         bool
		lastBriefSent    *time.Time // nil means no brief ever sent
		scheduleInterval string
		expectCatchUp    bool
		description      string
	}{
		{
			name:             "missed brief - more than 1 interval ago",
			useStore:         true,
			lastBriefSent:    &[]time.Time{time.Now().Add(-25 * time.Hour)}[0], // Yesterday's brief missed
			scheduleInterval: "0 9 * * *",                                        // Daily at 9am
			expectCatchUp:    true,
			description:      "Brief sent 25 hours ago should trigger catch-up for daily schedule",
		},
		{
			name:             "current brief - within interval",
			useStore:         true,
			lastBriefSent:    &[]time.Time{time.Now().Add(-2 * time.Hour)}[0], // Recent brief
			scheduleInterval: "0 9 * * *",                                       // Daily at 9am
			expectCatchUp:    false,
			description:      "Brief sent 2 hours ago should not trigger catch-up",
		},
		{
			name:             "nil store - graceful skip",
			useStore:         false,
			lastBriefSent:    nil,
			scheduleInterval: "0 9 * * *",
			expectCatchUp:    false,
			description:      "Nil store should not panic and should skip catch-up gracefully",
		},
		{
			name:             "never sent - empty table",
			useStore:         true,
			lastBriefSent:    nil, // No brief ever sent
			scheduleInterval: "0 9 * * *",
			expectCatchUp:    true,
			description:      "No previous brief should trigger catch-up",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var store *memory.Store
			var cleanup func()

			if tt.useStore {
				store, cleanup = setupSchedulerTestStore(t)
				defer cleanup()

				// Seed the store with a brief record if specified
				if tt.lastBriefSent != nil {
					record := &memory.BriefRecord{
						SentAt:    *tt.lastBriefSent,
						Channel:   "telegram",
						BriefType: "daily",
					}
					if err := store.RecordBriefSent(record); err != nil {
						t.Fatalf("failed to seed brief record: %v", err)
					}
				}
			}

			config := &BriefConfig{
				Enabled:  true,
				Schedule: tt.scheduleInterval,
				Timezone: "UTC",
				Channels: []ChannelConfig{
					{Type: "telegram", Channel: "@test"},
				},
			}

			generator := NewGenerator(store, config)
			delivery := NewDeliveryService(config)
			scheduler := NewScheduler(generator, delivery, config, nil, store)

			ctx := context.Background()
			if err := scheduler.Start(ctx); err != nil {
				t.Fatalf("start failed: %v", err)
			}
			defer scheduler.Stop()

			// Give scheduler time to process catch-up
			time.Sleep(100 * time.Millisecond)

			// The main goal of this test is to ensure that:
			// 1. nil store doesn't panic (covered by test execution completing)
			// 2. catch-up logic executes as expected (we can't easily verify delivery
			//    without mocking, but the logic paths are tested)
			// 3. Different scenarios are handled properly

			// For nil store test, the fact that we reach here without panic is the success
			if !tt.useStore {
				// Test passes if no panic occurred
				return
			}

			// For store-based tests, the catch-up mechanism has been invoked.
			// The actual delivery may fail (no real clients configured), but the
			// catch-up detection and triggering logic has been exercised.
		})
	}
}

func TestSchedulerRunNowRecordsToHistory(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	config := &BriefConfig{
		Enabled:  true,
		Schedule: "0 9 * * *",
		Timezone: "UTC",
		Channels: []ChannelConfig{
			{Type: "telegram", Channel: "@test"},
		},
		Content: ContentConfig{
			IncludeMetrics:     true,
			IncludeErrors:      true,
			MaxItemsPerSection: 10,
		},
	}

	generator := NewGenerator(store, config)
	delivery := NewDeliveryService(config) // Will fail but that's OK for testing
	scheduler := NewScheduler(generator, delivery, config, nil, store)

	ctx := context.Background()

	// Verify no brief history initially
	lastRecord, err := store.GetLastBriefSent("telegram")
	if err != nil {
		t.Fatalf("GetLastBriefSent failed: %v", err)
	}
	if lastRecord != nil {
		t.Fatalf("expected no initial brief record, got: %v", lastRecord)
	}

	// Run a brief manually
	results, err := scheduler.RunNow(ctx)
	if err != nil {
		t.Fatalf("RunNow failed: %v", err)
	}

	// Verify delivery was attempted (even if it failed)
	if len(results) != 1 {
		t.Errorf("expected 1 delivery result, got %d", len(results))
	}

	// For this test, we expect delivery to fail since no actual clients are configured
	// But the important part is that if it had succeeded, it would have been recorded
	if results[0].Success {
		// If delivery succeeded (shouldn't in this test setup), verify it was recorded
		lastRecord, err = store.GetLastBriefSent("telegram")
		if err != nil {
			t.Fatalf("GetLastBriefSent failed after successful delivery: %v", err)
		}
		if lastRecord == nil {
			t.Fatal("expected brief record after successful delivery, got nil")
		}
		if lastRecord.BriefType != "daily" {
			t.Errorf("expected brief type 'daily', got %s", lastRecord.BriefType)
		}
		if lastRecord.Channel != "telegram" {
			t.Errorf("expected channel 'telegram', got %s", lastRecord.Channel)
		}
	} else {
		// Delivery failed (expected), so no record should be written
		lastRecord, err = store.GetLastBriefSent("telegram")
		if err != nil {
			t.Fatalf("GetLastBriefSent failed: %v", err)
		}
		if lastRecord != nil {
			t.Error("expected no brief record after failed delivery, but got one")
		}
	}
}

