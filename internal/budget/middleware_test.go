package budget

import (
	"context"
	"testing"
	"time"
)

func TestTaskLimiter_TokenLimit(t *testing.T) {
	limiter := NewTaskLimiter(1000, 0) // 1000 token limit, no time limit

	// Add tokens under limit
	if !limiter.AddTokens(500) {
		t.Error("expected success adding tokens under limit")
	}
	if limiter.IsExceeded() {
		t.Error("expected not exceeded after adding 500/1000")
	}

	// Add more tokens, still under
	if !limiter.AddTokens(400) {
		t.Error("expected success adding tokens under limit")
	}
	if limiter.IsExceeded() {
		t.Error("expected not exceeded after adding 900/1000")
	}

	// Exceed limit
	if limiter.AddTokens(200) {
		t.Error("expected failure when exceeding limit")
	}
	if !limiter.IsExceeded() {
		t.Error("expected exceeded after adding 1100/1000")
	}

	if limiter.Reason() == "" {
		t.Error("expected reason to be set")
	}
}

func TestTaskLimiter_NoTokenLimit(t *testing.T) {
	limiter := NewTaskLimiter(0, 0) // No limits

	// Should always succeed
	if !limiter.AddTokens(1000000) {
		t.Error("expected success with no token limit")
	}
	if limiter.IsExceeded() {
		t.Error("expected not exceeded with no limit")
	}
}

func TestTaskLimiter_DurationLimit(t *testing.T) {
	limiter := NewTaskLimiter(0, 100*time.Millisecond) // 100ms limit

	// Should not be exceeded immediately
	if !limiter.CheckDuration() {
		t.Error("expected duration not exceeded immediately")
	}

	// Wait for limit to pass
	time.Sleep(150 * time.Millisecond)

	if limiter.CheckDuration() {
		t.Error("expected duration exceeded after sleep")
	}
	if !limiter.IsExceeded() {
		t.Error("expected exceeded flag set")
	}
}

func TestTaskLimiter_NoDurationLimit(t *testing.T) {
	limiter := NewTaskLimiter(0, 0) // No limits

	time.Sleep(10 * time.Millisecond)

	if !limiter.CheckDuration() {
		t.Error("expected success with no duration limit")
	}
}

func TestTaskLimiter_GetMetrics(t *testing.T) {
	limiter := NewTaskLimiter(1000, time.Hour)

	limiter.AddTokens(250)
	limiter.AddTokens(250)

	if limiter.GetTokens() != 500 {
		t.Errorf("expected 500 tokens, got %d", limiter.GetTokens())
	}

	// Duration should be positive
	if limiter.GetDuration() <= 0 {
		t.Error("expected positive duration")
	}
}

func TestTaskLimiter_CreateContext(t *testing.T) {
	limiter := NewTaskLimiter(0, 100*time.Millisecond)

	ctx, cancel := limiter.CreateContext(context.Background())
	defer cancel()

	// Context should have deadline
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Error("expected context to have deadline")
	}

	if time.Until(deadline) > 100*time.Millisecond {
		t.Error("expected deadline within duration limit")
	}
}

func TestTaskLimiter_CreateContext_NoLimit(t *testing.T) {
	limiter := NewTaskLimiter(0, 0)

	ctx, cancel := limiter.CreateContext(context.Background())
	defer cancel()

	// Context should not have deadline
	_, ok := ctx.Deadline()
	if ok {
		t.Error("expected context without deadline when no duration limit")
	}
}

func TestTaskContext(t *testing.T) {
	limiter := NewTaskLimiter(1000, time.Hour)
	taskCtx := NewTaskContext("team1", "user1", "task1", "project1", limiter, 45.50)

	if taskCtx.TeamID != "team1" {
		t.Errorf("expected team1, got %s", taskCtx.TeamID)
	}
	if taskCtx.UserID != "user1" {
		t.Errorf("expected user1, got %s", taskCtx.UserID)
	}
	if taskCtx.TaskID != "task1" {
		t.Errorf("expected task1, got %s", taskCtx.TaskID)
	}
	if taskCtx.ProjectID != "project1" {
		t.Errorf("expected project1, got %s", taskCtx.ProjectID)
	}
	if taskCtx.BudgetLeft != 45.50 {
		t.Errorf("expected 45.50, got %f", taskCtx.BudgetLeft)
	}
	if taskCtx.Limiter != limiter {
		t.Error("expected same limiter")
	}
}

func TestTaskLimiter_AddTokens_AfterExceeded(t *testing.T) {
	limiter := NewTaskLimiter(100, 0)

	// Exceed the limit
	limiter.AddTokens(150)
	if !limiter.IsExceeded() {
		t.Fatal("expected exceeded after adding 150/100")
	}

	// Try adding more tokens after exceeded
	result := limiter.AddTokens(10)
	if result {
		t.Error("expected false when adding tokens after already exceeded")
	}
}

func TestTaskLimiter_CheckDuration_AfterExceeded(t *testing.T) {
	limiter := NewTaskLimiter(100, time.Hour)

	// Exceed via tokens first
	limiter.AddTokens(150)
	if !limiter.IsExceeded() {
		t.Fatal("expected exceeded")
	}

	// CheckDuration should return false when already exceeded
	result := limiter.CheckDuration()
	if result {
		t.Error("expected false when checking duration after already exceeded")
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config == nil {
		t.Fatal("DefaultConfig returned nil")
	}

	if config.Enabled {
		t.Error("expected Enabled to be false by default")
	}

	if config.DailyLimit != 50.00 {
		t.Errorf("expected DailyLimit 50.00, got %v", config.DailyLimit)
	}

	if config.MonthlyLimit != 500.00 {
		t.Errorf("expected MonthlyLimit 500.00, got %v", config.MonthlyLimit)
	}

	if config.PerTask.MaxTokens != 100000 {
		t.Errorf("expected PerTask.MaxTokens 100000, got %v", config.PerTask.MaxTokens)
	}

	if config.PerTask.MaxDuration != 30*time.Minute {
		t.Errorf("expected PerTask.MaxDuration 30m, got %v", config.PerTask.MaxDuration)
	}

	if config.OnExceed.Daily != ActionPause {
		t.Errorf("expected OnExceed.Daily to be pause, got %v", config.OnExceed.Daily)
	}

	if config.OnExceed.Monthly != ActionStop {
		t.Errorf("expected OnExceed.Monthly to be stop, got %v", config.OnExceed.Monthly)
	}

	if config.OnExceed.PerTask != ActionStop {
		t.Errorf("expected OnExceed.PerTask to be stop, got %v", config.OnExceed.PerTask)
	}

	if config.Thresholds.WarnPercent != 80 {
		t.Errorf("expected Thresholds.WarnPercent 80, got %v", config.Thresholds.WarnPercent)
	}
}

func TestStatus_IsExceeded(t *testing.T) {
	tests := []struct {
		name           string
		dailyPercent   float64
		monthlyPercent float64
		want           bool
	}{
		{
			name:           "not exceeded when both under 100",
			dailyPercent:   50.0,
			monthlyPercent: 75.0,
			want:           false,
		},
		{
			name:           "exceeded when daily at 100",
			dailyPercent:   100.0,
			monthlyPercent: 50.0,
			want:           true,
		},
		{
			name:           "exceeded when monthly at 100",
			dailyPercent:   50.0,
			monthlyPercent: 100.0,
			want:           true,
		},
		{
			name:           "exceeded when both at 100",
			dailyPercent:   100.0,
			monthlyPercent: 100.0,
			want:           true,
		},
		{
			name:           "exceeded when over 100",
			dailyPercent:   120.0,
			monthlyPercent: 110.0,
			want:           true,
		},
		{
			name:           "not exceeded at exactly 99.99",
			dailyPercent:   99.99,
			monthlyPercent: 99.99,
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := &Status{
				DailyPercent:   tt.dailyPercent,
				MonthlyPercent: tt.monthlyPercent,
			}
			if got := status.IsExceeded(); got != tt.want {
				t.Errorf("IsExceeded() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStatus_IsWarning(t *testing.T) {
	tests := []struct {
		name           string
		dailyPercent   float64
		monthlyPercent float64
		warnPercent    float64
		want           bool
	}{
		{
			name:           "no warning when both below threshold",
			dailyPercent:   50.0,
			monthlyPercent: 50.0,
			warnPercent:    80.0,
			want:           false,
		},
		{
			name:           "warning when daily at threshold",
			dailyPercent:   80.0,
			monthlyPercent: 50.0,
			warnPercent:    80.0,
			want:           true,
		},
		{
			name:           "warning when monthly at threshold",
			dailyPercent:   50.0,
			monthlyPercent: 80.0,
			warnPercent:    80.0,
			want:           true,
		},
		{
			name:           "warning when both at threshold",
			dailyPercent:   80.0,
			monthlyPercent: 80.0,
			warnPercent:    80.0,
			want:           true,
		},
		{
			name:           "warning when over threshold",
			dailyPercent:   90.0,
			monthlyPercent: 60.0,
			warnPercent:    80.0,
			want:           true,
		},
		{
			name:           "no warning just below threshold",
			dailyPercent:   79.99,
			monthlyPercent: 79.99,
			warnPercent:    80.0,
			want:           false,
		},
		{
			name:           "warning with different threshold",
			dailyPercent:   75.0,
			monthlyPercent: 50.0,
			warnPercent:    70.0,
			want:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := &Status{
				DailyPercent:   tt.dailyPercent,
				MonthlyPercent: tt.monthlyPercent,
			}
			if got := status.IsWarning(tt.warnPercent); got != tt.want {
				t.Errorf("IsWarning(%v) = %v, want %v", tt.warnPercent, got, tt.want)
			}
		})
	}
}

func TestCheckResult_Fields(t *testing.T) {
	result := &CheckResult{
		Allowed:     false,
		Action:      ActionStop,
		Reason:      "Budget exceeded",
		DailyLeft:   0.0,
		MonthlyLeft: 100.0,
	}

	if result.Allowed {
		t.Error("expected Allowed to be false")
	}
	if result.Action != ActionStop {
		t.Errorf("expected Action to be stop, got %v", result.Action)
	}
	if result.Reason != "Budget exceeded" {
		t.Errorf("expected Reason 'Budget exceeded', got %v", result.Reason)
	}
	if result.DailyLeft != 0.0 {
		t.Errorf("expected DailyLeft 0.0, got %v", result.DailyLeft)
	}
	if result.MonthlyLeft != 100.0 {
		t.Errorf("expected MonthlyLeft 100.0, got %v", result.MonthlyLeft)
	}
}
