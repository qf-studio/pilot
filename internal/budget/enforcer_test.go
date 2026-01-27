package budget

import (
	"context"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
)

// mockUsageProvider implements UsageProvider for testing
type mockUsageProvider struct {
	dailyCost   float64
	monthlyCost float64
}

func (m *mockUsageProvider) GetUsageSummary(query memory.UsageQuery) (*memory.UsageSummary, error) {
	// Determine if this is a daily or monthly query based on the time range
	duration := query.End.Sub(query.Start)

	if duration < 25*time.Hour {
		// Daily query
		return &memory.UsageSummary{
			TotalCost: m.dailyCost,
		}, nil
	}
	// Monthly query
	return &memory.UsageSummary{
		TotalCost: m.monthlyCost,
	}, nil
}

func TestEnforcer_CheckBudget_Disabled(t *testing.T) {
	config := &Config{
		Enabled: false,
	}
	provider := &mockUsageProvider{}
	enforcer := NewEnforcer(config, provider)

	result, err := enforcer.CheckBudget(context.Background(), "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Allowed {
		t.Error("expected task to be allowed when budget is disabled")
	}
}

func TestEnforcer_CheckBudget_UnderLimits(t *testing.T) {
	config := &Config{
		Enabled:      true,
		DailyLimit:   50.0,
		MonthlyLimit: 500.0,
		OnExceed: ExceedAction{
			Daily:   ActionStop,
			Monthly: ActionStop,
		},
		Thresholds: ThresholdConfig{
			WarnPercent: 80,
		},
	}
	provider := &mockUsageProvider{
		dailyCost:   10.0,  // 20% of limit
		monthlyCost: 100.0, // 20% of limit
	}
	enforcer := NewEnforcer(config, provider)

	result, err := enforcer.CheckBudget(context.Background(), "", "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Allowed {
		t.Error("expected task to be allowed under limits")
	}

	if result.DailyLeft != 40.0 {
		t.Errorf("expected daily left to be 40.0, got %v", result.DailyLeft)
	}

	if result.MonthlyLeft != 400.0 {
		t.Errorf("expected monthly left to be 400.0, got %v", result.MonthlyLeft)
	}
}

func TestEnforcer_CheckBudget_DailyLimitExceeded(t *testing.T) {
	config := &Config{
		Enabled:      true,
		DailyLimit:   50.0,
		MonthlyLimit: 500.0,
		OnExceed: ExceedAction{
			Daily:   ActionStop,
			Monthly: ActionStop,
		},
		Thresholds: ThresholdConfig{
			WarnPercent: 80,
		},
	}
	provider := &mockUsageProvider{
		dailyCost:   55.0, // Over daily limit
		monthlyCost: 100.0,
	}
	enforcer := NewEnforcer(config, provider)

	result, err := enforcer.CheckBudget(context.Background(), "", "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Allowed {
		t.Error("expected task to be blocked when daily limit exceeded")
	}

	if result.Action != ActionStop {
		t.Errorf("expected action to be stop, got %v", result.Action)
	}

	if result.DailyLeft != 0 {
		t.Errorf("expected daily left to be 0, got %v", result.DailyLeft)
	}
}

func TestEnforcer_CheckBudget_MonthlyLimitExceeded(t *testing.T) {
	config := &Config{
		Enabled:      true,
		DailyLimit:   50.0,
		MonthlyLimit: 500.0,
		OnExceed: ExceedAction{
			Daily:   ActionStop,
			Monthly: ActionStop,
		},
		Thresholds: ThresholdConfig{
			WarnPercent: 80,
		},
	}
	provider := &mockUsageProvider{
		dailyCost:   10.0,
		monthlyCost: 550.0, // Over monthly limit
	}
	enforcer := NewEnforcer(config, provider)

	result, err := enforcer.CheckBudget(context.Background(), "", "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Allowed {
		t.Error("expected task to be blocked when monthly limit exceeded")
	}

	if result.Action != ActionStop {
		t.Errorf("expected action to be stop, got %v", result.Action)
	}

	if result.MonthlyLeft != 0 {
		t.Errorf("expected monthly left to be 0, got %v", result.MonthlyLeft)
	}
}

func TestEnforcer_CheckBudget_WarnAction(t *testing.T) {
	config := &Config{
		Enabled:      true,
		DailyLimit:   50.0,
		MonthlyLimit: 500.0,
		OnExceed: ExceedAction{
			Daily:   ActionWarn, // Warn only, don't block
			Monthly: ActionWarn,
		},
		Thresholds: ThresholdConfig{
			WarnPercent: 80,
		},
	}
	provider := &mockUsageProvider{
		dailyCost:   55.0, // Over limit but action is warn
		monthlyCost: 100.0,
	}
	enforcer := NewEnforcer(config, provider)

	result, err := enforcer.CheckBudget(context.Background(), "", "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Warn action should still allow tasks
	if !result.Allowed {
		t.Error("expected task to be allowed with warn action")
	}
}

func TestEnforcer_Pause_Resume(t *testing.T) {
	config := &Config{
		Enabled:      true,
		DailyLimit:   50.0,
		MonthlyLimit: 500.0,
	}
	provider := &mockUsageProvider{
		dailyCost:   10.0,
		monthlyCost: 100.0,
	}
	enforcer := NewEnforcer(config, provider)

	// Initially not paused
	if enforcer.IsPaused() {
		t.Error("expected not paused initially")
	}

	// Pause
	enforcer.Pause("manual pause")
	if !enforcer.IsPaused() {
		t.Error("expected paused after Pause()")
	}

	// Check budget should fail when paused
	result, err := enforcer.CheckBudget(context.Background(), "", "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Allowed {
		t.Error("expected task blocked when paused")
	}
	if result.Action != ActionPause {
		t.Errorf("expected action pause, got %v", result.Action)
	}

	// Resume
	enforcer.Resume()
	if enforcer.IsPaused() {
		t.Error("expected not paused after Resume()")
	}

	// Check budget should work after resume
	result, err = enforcer.CheckBudget(context.Background(), "", "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Error("expected task allowed after resume")
	}
}

func TestEnforcer_GetStatus(t *testing.T) {
	config := &Config{
		Enabled:      true,
		DailyLimit:   50.0,
		MonthlyLimit: 500.0,
		Thresholds: ThresholdConfig{
			WarnPercent: 80,
		},
	}
	provider := &mockUsageProvider{
		dailyCost:   25.0,  // 50%
		monthlyCost: 250.0, // 50%
	}
	enforcer := NewEnforcer(config, provider)

	status, err := enforcer.GetStatus(context.Background(), "", "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.DailySpent != 25.0 {
		t.Errorf("expected daily spent 25.0, got %v", status.DailySpent)
	}
	if status.DailyLimit != 50.0 {
		t.Errorf("expected daily limit 50.0, got %v", status.DailyLimit)
	}
	if status.DailyPercent != 50.0 {
		t.Errorf("expected daily percent 50.0, got %v", status.DailyPercent)
	}

	if status.MonthlySpent != 250.0 {
		t.Errorf("expected monthly spent 250.0, got %v", status.MonthlySpent)
	}
	if status.MonthlyLimit != 500.0 {
		t.Errorf("expected monthly limit 500.0, got %v", status.MonthlyLimit)
	}
	if status.MonthlyPercent != 50.0 {
		t.Errorf("expected monthly percent 50.0, got %v", status.MonthlyPercent)
	}

	if status.IsExceeded() {
		t.Error("expected not exceeded at 50%")
	}
	if status.IsWarning(80) {
		t.Error("expected no warning at 50%")
	}
}

func TestEnforcer_GetStatus_Warning(t *testing.T) {
	config := &Config{
		Enabled:      true,
		DailyLimit:   50.0,
		MonthlyLimit: 500.0,
		Thresholds: ThresholdConfig{
			WarnPercent: 80,
		},
	}
	provider := &mockUsageProvider{
		dailyCost:   45.0,  // 90%
		monthlyCost: 250.0, // 50%
	}
	enforcer := NewEnforcer(config, provider)

	status, err := enforcer.GetStatus(context.Background(), "", "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !status.IsWarning(80) {
		t.Error("expected warning at 90% daily")
	}
}
