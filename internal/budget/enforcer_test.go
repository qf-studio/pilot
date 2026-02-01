package budget

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
)

// mockUsageProvider implements UsageProvider for testing
type mockUsageProvider struct {
	dailyCost   float64
	monthlyCost float64
	callCount   int
	mu          sync.Mutex
}

func (m *mockUsageProvider) GetUsageSummary(query memory.UsageQuery) (*memory.UsageSummary, error) {
	m.mu.Lock()
	m.callCount++
	isOddCall := m.callCount%2 == 1
	m.mu.Unlock()

	// Enforcer always calls: daily query first, then monthly query
	// So odd calls (1st, 3rd, ...) are daily, even calls (2nd, 4th, ...) are monthly
	// This fixes the bug where on day 1 of month, both queries have same duration
	if isOddCall {
		return &memory.UsageSummary{
			TotalCost: m.dailyCost,
		}, nil
	}
	return &memory.UsageSummary{
		TotalCost: m.monthlyCost,
	}, nil
}

func (m *mockUsageProvider) Reset() {
	m.mu.Lock()
	m.callCount = 0
	m.mu.Unlock()
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

func TestEnforcer_GetPerTaskLimits(t *testing.T) {
	tests := []struct {
		name            string
		config          *Config
		wantMaxTokens   int64
		wantMaxDuration time.Duration
	}{
		{
			name: "disabled returns zero limits",
			config: &Config{
				Enabled: false,
				PerTask: PerTaskConfig{
					MaxTokens:   100000,
					MaxDuration: 30 * time.Minute,
				},
			},
			wantMaxTokens:   0,
			wantMaxDuration: 0,
		},
		{
			name: "enabled returns configured limits",
			config: &Config{
				Enabled: true,
				PerTask: PerTaskConfig{
					MaxTokens:   50000,
					MaxDuration: 15 * time.Minute,
				},
			},
			wantMaxTokens:   50000,
			wantMaxDuration: 15 * time.Minute,
		},
		{
			name: "enabled with zero values returns zeros",
			config: &Config{
				Enabled: true,
				PerTask: PerTaskConfig{
					MaxTokens:   0,
					MaxDuration: 0,
				},
			},
			wantMaxTokens:   0,
			wantMaxDuration: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enforcer := NewEnforcer(tt.config, &mockUsageProvider{})
			gotTokens, gotDuration := enforcer.GetPerTaskLimits()
			if gotTokens != tt.wantMaxTokens {
				t.Errorf("GetPerTaskLimits() maxTokens = %v, want %v", gotTokens, tt.wantMaxTokens)
			}
			if gotDuration != tt.wantMaxDuration {
				t.Errorf("GetPerTaskLimits() maxDuration = %v, want %v", gotDuration, tt.wantMaxDuration)
			}
		})
	}
}

func TestEnforcer_ResetDaily(t *testing.T) {
	tests := []struct {
		name            string
		setupPause      bool
		pauseReason     string
		expectedResumed bool
		expectedBlocked int
	}{
		{
			name:            "resets blocked counter when not paused",
			setupPause:      false,
			pauseReason:     "",
			expectedResumed: true,
			expectedBlocked: 0,
		},
		{
			name:            "resumes if paused due to daily budget exceeded",
			setupPause:      true,
			pauseReason:     "Daily budget exceeded",
			expectedResumed: true,
			expectedBlocked: 0,
		},
		{
			name:            "stays paused if paused for other reason",
			setupPause:      true,
			pauseReason:     "Monthly budget exceeded",
			expectedResumed: false,
			expectedBlocked: 0,
		},
		{
			name:            "stays paused if paused manually",
			setupPause:      true,
			pauseReason:     "manual pause",
			expectedResumed: false,
			expectedBlocked: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Enabled:      true,
				DailyLimit:   50.0,
				MonthlyLimit: 500.0,
			}
			enforcer := NewEnforcer(config, &mockUsageProvider{})

			if tt.setupPause {
				enforcer.Pause(tt.pauseReason)
			}

			// Simulate some blocked tasks
			enforcer.mu.Lock()
			enforcer.blockedTasks = 5
			enforcer.mu.Unlock()

			enforcer.ResetDaily()

			enforcer.mu.RLock()
			gotBlocked := enforcer.blockedTasks
			isPaused := enforcer.paused
			enforcer.mu.RUnlock()

			if gotBlocked != tt.expectedBlocked {
				t.Errorf("ResetDaily() blockedTasks = %v, want %v", gotBlocked, tt.expectedBlocked)
			}
			if tt.expectedResumed && isPaused {
				t.Errorf("ResetDaily() expected to resume but still paused")
			}
			if !tt.expectedResumed && !isPaused {
				t.Errorf("ResetDaily() expected to stay paused but resumed")
			}
		})
	}
}

func TestEnforcer_UpdateConfig(t *testing.T) {
	initialConfig := &Config{
		Enabled:      false,
		DailyLimit:   10.0,
		MonthlyLimit: 100.0,
	}
	enforcer := NewEnforcer(initialConfig, &mockUsageProvider{})

	newConfig := &Config{
		Enabled:      true,
		DailyLimit:   50.0,
		MonthlyLimit: 500.0,
		PerTask: PerTaskConfig{
			MaxTokens:   100000,
			MaxDuration: 30 * time.Minute,
		},
	}

	enforcer.UpdateConfig(newConfig)

	gotConfig := enforcer.GetConfig()
	if gotConfig.Enabled != true {
		t.Error("expected enabled after update")
	}
	if gotConfig.DailyLimit != 50.0 {
		t.Errorf("expected daily limit 50.0, got %v", gotConfig.DailyLimit)
	}
	if gotConfig.MonthlyLimit != 500.0 {
		t.Errorf("expected monthly limit 500.0, got %v", gotConfig.MonthlyLimit)
	}
}

func TestEnforcer_GetConfig(t *testing.T) {
	config := &Config{
		Enabled:      true,
		DailyLimit:   75.0,
		MonthlyLimit: 750.0,
		PerTask: PerTaskConfig{
			MaxTokens:   200000,
			MaxDuration: 45 * time.Minute,
		},
		OnExceed: ExceedAction{
			Daily:   ActionWarn,
			Monthly: ActionPause,
			PerTask: ActionStop,
		},
		Thresholds: ThresholdConfig{
			WarnPercent: 85,
		},
	}
	enforcer := NewEnforcer(config, &mockUsageProvider{})

	got := enforcer.GetConfig()

	if got != config {
		t.Error("GetConfig should return the same config reference")
	}
}

func TestEnforcer_OnAlert(t *testing.T) {
	tests := []struct {
		name         string
		dailyCost    float64
		monthlyCost  float64
		warnPercent  float64
		expectAlerts []string
	}{
		{
			name:         "daily warning alert",
			dailyCost:    45.0, // 90% of 50
			monthlyCost:  100.0,
			warnPercent:  80,
			expectAlerts: []string{"daily_budget_warning"},
		},
		{
			name:         "monthly warning alert",
			dailyCost:    10.0,
			monthlyCost:  450.0, // 90% of 500
			warnPercent:  80,
			expectAlerts: []string{"monthly_budget_warning"},
		},
		{
			name:         "both daily and monthly warning",
			dailyCost:    45.0,  // 90%
			monthlyCost:  450.0, // 90%
			warnPercent:  80,
			expectAlerts: []string{"daily_budget_warning", "monthly_budget_warning"},
		},
		{
			name:         "daily exceeded alert",
			dailyCost:    55.0, // 110%
			monthlyCost:  100.0,
			warnPercent:  80,
			expectAlerts: []string{"daily_budget_exceeded"},
		},
		{
			name:         "monthly exceeded alert",
			dailyCost:    10.0,
			monthlyCost:  550.0, // 110%
			warnPercent:  80,
			expectAlerts: []string{"monthly_budget_exceeded"},
		},
		{
			name:         "both exceeded alerts",
			dailyCost:    55.0,  // 110%
			monthlyCost:  550.0, // 110%
			warnPercent:  80,
			expectAlerts: []string{"daily_budget_exceeded", "monthly_budget_exceeded"},
		},
		{
			name:         "no alert below warning threshold",
			dailyCost:    30.0, // 60%
			monthlyCost:  300.0,
			warnPercent:  80,
			expectAlerts: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Enabled:      true,
				DailyLimit:   50.0,
				MonthlyLimit: 500.0,
				OnExceed: ExceedAction{
					Daily:   ActionWarn, // Use warn to allow execution
					Monthly: ActionWarn,
				},
				Thresholds: ThresholdConfig{
					WarnPercent: tt.warnPercent,
				},
			}
			provider := &mockUsageProvider{
				dailyCost:   tt.dailyCost,
				monthlyCost: tt.monthlyCost,
			}
			enforcer := NewEnforcer(config, provider)

			var receivedAlerts []string
			enforcer.OnAlert(func(alertType, message, severity string) {
				receivedAlerts = append(receivedAlerts, alertType)
			})

			// Trigger budget check which fires alerts
			_, _ = enforcer.CheckBudget(context.Background(), "", "user1")

			// Verify expected alerts
			if len(receivedAlerts) != len(tt.expectAlerts) {
				t.Errorf("expected %d alerts, got %d: %v", len(tt.expectAlerts), len(receivedAlerts), receivedAlerts)
				return
			}

			for i, expected := range tt.expectAlerts {
				if receivedAlerts[i] != expected {
					t.Errorf("alert[%d] = %v, want %v", i, receivedAlerts[i], expected)
				}
			}
		})
	}
}

func TestEnforcer_CheckBudget_PauseAction(t *testing.T) {
	tests := []struct {
		name        string
		dailyCost   float64
		monthlyCost float64
		onExceed    ExceedAction
		wantAllowed bool
		wantAction  Action
	}{
		{
			name:        "daily limit with pause action",
			dailyCost:   55.0,
			monthlyCost: 100.0,
			onExceed: ExceedAction{
				Daily:   ActionPause,
				Monthly: ActionStop,
			},
			wantAllowed: false,
			wantAction:  ActionPause,
		},
		{
			name:        "monthly limit with pause action",
			dailyCost:   10.0,
			monthlyCost: 550.0,
			onExceed: ExceedAction{
				Daily:   ActionStop,
				Monthly: ActionPause,
			},
			wantAllowed: false,
			wantAction:  ActionPause,
		},
		{
			name:        "monthly exceeds but daily has warn action",
			dailyCost:   55.0,
			monthlyCost: 550.0,
			onExceed: ExceedAction{
				Daily:   ActionWarn,
				Monthly: ActionStop,
			},
			wantAllowed: false,
			wantAction:  ActionStop,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Enabled:      true,
				DailyLimit:   50.0,
				MonthlyLimit: 500.0,
				OnExceed:     tt.onExceed,
				Thresholds:   ThresholdConfig{WarnPercent: 80},
			}
			provider := &mockUsageProvider{
				dailyCost:   tt.dailyCost,
				monthlyCost: tt.monthlyCost,
			}
			enforcer := NewEnforcer(config, provider)

			result, err := enforcer.CheckBudget(context.Background(), "", "user1")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Allowed != tt.wantAllowed {
				t.Errorf("Allowed = %v, want %v", result.Allowed, tt.wantAllowed)
			}
			if result.Action != tt.wantAction {
				t.Errorf("Action = %v, want %v", result.Action, tt.wantAction)
			}
		})
	}
}

func TestEnforcer_BlockedTasksCounter(t *testing.T) {
	config := &Config{
		Enabled:      true,
		DailyLimit:   50.0,
		MonthlyLimit: 500.0,
		OnExceed: ExceedAction{
			Daily:   ActionStop,
			Monthly: ActionStop,
		},
		Thresholds: ThresholdConfig{WarnPercent: 80},
	}
	provider := &mockUsageProvider{
		dailyCost:   55.0, // Over limit
		monthlyCost: 100.0,
	}
	enforcer := NewEnforcer(config, provider)

	// Make multiple budget checks that should be blocked
	for i := 0; i < 3; i++ {
		_, _ = enforcer.CheckBudget(context.Background(), "", "user1")
	}

	status, err := enforcer.GetStatus(context.Background(), "", "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.BlockedTasks != 3 {
		t.Errorf("expected 3 blocked tasks, got %d", status.BlockedTasks)
	}
}

// errorUsageProvider returns an error for testing error handling
type errorUsageProvider struct{}

func (e *errorUsageProvider) GetUsageSummary(query memory.UsageQuery) (*memory.UsageSummary, error) {
	return nil, context.DeadlineExceeded
}

func TestEnforcer_CheckBudget_ProviderError(t *testing.T) {
	config := &Config{
		Enabled:      true,
		DailyLimit:   50.0,
		MonthlyLimit: 500.0,
	}
	provider := &errorUsageProvider{}
	enforcer := NewEnforcer(config, provider)

	// On error, should still allow (fail-open)
	result, err := enforcer.CheckBudget(context.Background(), "", "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Allowed {
		t.Error("expected task allowed on provider error (fail-open)")
	}
}

func TestEnforcer_GetStatus_ProviderError(t *testing.T) {
	config := &Config{
		Enabled:      true,
		DailyLimit:   50.0,
		MonthlyLimit: 500.0,
	}
	provider := &errorUsageProvider{}
	enforcer := NewEnforcer(config, provider)

	_, err := enforcer.GetStatus(context.Background(), "", "user1")
	if err == nil {
		t.Error("expected error from GetStatus when provider fails")
	}
}
