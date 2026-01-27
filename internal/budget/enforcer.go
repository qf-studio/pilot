package budget

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/alekspetrov/pilot/internal/memory"
)

// UsageProvider interface for getting usage data
type UsageProvider interface {
	GetUsageSummary(query memory.UsageQuery) (*memory.UsageSummary, error)
}

// AlertCallback is called when budget thresholds are crossed
type AlertCallback func(alertType string, message string, severity string)

// Enforcer checks and enforces budget limits
type Enforcer struct {
	config   *Config
	provider UsageProvider
	onAlert  AlertCallback

	mu           sync.RWMutex
	paused       bool
	pauseReason  string
	blockedTasks int
	lastStatus   *Status

	log *slog.Logger
}

// NewEnforcer creates a new budget enforcer
func NewEnforcer(config *Config, provider UsageProvider) *Enforcer {
	return &Enforcer{
		config:   config,
		provider: provider,
		log:      logging.WithComponent("budget"),
	}
}

// OnAlert sets the alert callback
func (e *Enforcer) OnAlert(callback AlertCallback) {
	e.onAlert = callback
}

// CheckBudget checks if a new task can be started
func (e *Enforcer) CheckBudget(ctx context.Context, teamID, userID string) (*CheckResult, error) {
	if !e.config.Enabled {
		return &CheckResult{Allowed: true}, nil
	}

	e.mu.RLock()
	paused := e.paused
	pauseReason := e.pauseReason
	e.mu.RUnlock()

	if paused {
		return &CheckResult{
			Allowed: false,
			Action:  ActionPause,
			Reason:  pauseReason,
		}, nil
	}

	status, err := e.GetStatus(ctx, teamID, userID)
	if err != nil {
		e.log.Error("Failed to get budget status", slog.String("error", err.Error()))
		// On error, allow task but log warning
		return &CheckResult{Allowed: true}, nil
	}

	// Check monthly limit first (more severe)
	if status.MonthlyPercent >= 100 {
		action := e.config.OnExceed.Monthly
		if action == ActionStop || action == ActionPause {
			e.incrementBlocked()
			return &CheckResult{
				Allowed:     false,
				Action:      action,
				Reason:      fmt.Sprintf("Monthly budget exceeded: $%.2f / $%.2f", status.MonthlySpent, status.MonthlyLimit),
				DailyLeft:   status.DailyLimit - status.DailySpent,
				MonthlyLeft: 0,
			}, nil
		}
	}

	// Check daily limit
	if status.DailyPercent >= 100 {
		action := e.config.OnExceed.Daily
		if action == ActionStop || action == ActionPause {
			e.incrementBlocked()
			return &CheckResult{
				Allowed:     false,
				Action:      action,
				Reason:      fmt.Sprintf("Daily budget exceeded: $%.2f / $%.2f", status.DailySpent, status.DailyLimit),
				DailyLeft:   0,
				MonthlyLeft: status.MonthlyLimit - status.MonthlySpent,
			}, nil
		}
	}

	// Check warning thresholds
	if status.IsWarning(e.config.Thresholds.WarnPercent) {
		e.fireAlert(status)
	}

	return &CheckResult{
		Allowed:     true,
		DailyLeft:   status.DailyLimit - status.DailySpent,
		MonthlyLeft: status.MonthlyLimit - status.MonthlySpent,
	}, nil
}

// GetStatus returns the current budget status
func (e *Enforcer) GetStatus(ctx context.Context, teamID, userID string) (*Status, error) {
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)

	// Get daily usage
	dailyQuery := memory.UsageQuery{
		UserID: userID,
		Start:  dayStart,
		End:    now,
	}
	dailySummary, err := e.provider.GetUsageSummary(dailyQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to get daily usage: %w", err)
	}

	// Get monthly usage
	monthlyQuery := memory.UsageQuery{
		UserID: userID,
		Start:  monthStart,
		End:    now,
	}
	monthlySummary, err := e.provider.GetUsageSummary(monthlyQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to get monthly usage: %w", err)
	}

	e.mu.RLock()
	paused := e.paused
	pauseReason := e.pauseReason
	blockedTasks := e.blockedTasks
	e.mu.RUnlock()

	status := &Status{
		DailySpent:     dailySummary.TotalCost,
		DailyLimit:     e.config.DailyLimit,
		DailyPercent:   (dailySummary.TotalCost / e.config.DailyLimit) * 100,
		MonthlySpent:   monthlySummary.TotalCost,
		MonthlyLimit:   e.config.MonthlyLimit,
		MonthlyPercent: (monthlySummary.TotalCost / e.config.MonthlyLimit) * 100,
		IsPaused:       paused,
		PauseReason:    pauseReason,
		BlockedTasks:   blockedTasks,
		LastUpdated:    now,
	}

	e.mu.Lock()
	e.lastStatus = status
	e.mu.Unlock()

	return status, nil
}

// GetPerTaskLimits returns the per-task limits for executor
func (e *Enforcer) GetPerTaskLimits() (maxTokens int64, maxDuration time.Duration) {
	if !e.config.Enabled {
		return 0, 0 // No limits
	}
	return e.config.PerTask.MaxTokens, e.config.PerTask.MaxDuration
}

// Pause pauses new task execution
func (e *Enforcer) Pause(reason string) {
	e.mu.Lock()
	e.paused = true
	e.pauseReason = reason
	e.mu.Unlock()

	e.log.Warn("Budget enforcement paused new tasks", slog.String("reason", reason))
}

// Resume resumes task execution
func (e *Enforcer) Resume() {
	e.mu.Lock()
	e.paused = false
	e.pauseReason = ""
	e.blockedTasks = 0
	e.mu.Unlock()

	e.log.Info("Budget enforcement resumed")
}

// IsPaused returns whether new tasks are paused
func (e *Enforcer) IsPaused() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.paused
}

// ResetDaily resets the blocked tasks counter (called at day start)
func (e *Enforcer) ResetDaily() {
	e.mu.Lock()
	e.blockedTasks = 0
	// Only resume if paused due to daily limit
	if e.paused && e.pauseReason == "Daily budget exceeded" {
		e.paused = false
		e.pauseReason = ""
	}
	e.mu.Unlock()

	e.log.Info("Daily budget counters reset")
}

func (e *Enforcer) incrementBlocked() {
	e.mu.Lock()
	e.blockedTasks++
	e.mu.Unlock()
}

func (e *Enforcer) fireAlert(status *Status) {
	if e.onAlert == nil {
		return
	}

	if status.DailyPercent >= e.config.Thresholds.WarnPercent && status.DailyPercent < 100 {
		e.onAlert(
			"daily_budget_warning",
			fmt.Sprintf("Daily budget at %.0f%%: $%.2f / $%.2f", status.DailyPercent, status.DailySpent, status.DailyLimit),
			"warning",
		)
	}

	if status.MonthlyPercent >= e.config.Thresholds.WarnPercent && status.MonthlyPercent < 100 {
		e.onAlert(
			"monthly_budget_warning",
			fmt.Sprintf("Monthly budget at %.0f%%: $%.2f / $%.2f", status.MonthlyPercent, status.MonthlySpent, status.MonthlyLimit),
			"warning",
		)
	}

	if status.DailyPercent >= 100 {
		e.onAlert(
			"daily_budget_exceeded",
			fmt.Sprintf("Daily budget exceeded: $%.2f / $%.2f", status.DailySpent, status.DailyLimit),
			"critical",
		)
	}

	if status.MonthlyPercent >= 100 {
		e.onAlert(
			"monthly_budget_exceeded",
			fmt.Sprintf("Monthly budget exceeded: $%.2f / $%.2f", status.MonthlySpent, status.MonthlyLimit),
			"critical",
		)
	}
}

// UpdateConfig updates the enforcer configuration
func (e *Enforcer) UpdateConfig(config *Config) {
	e.mu.Lock()
	e.config = config
	e.mu.Unlock()

	e.log.Info("Budget configuration updated",
		slog.Bool("enabled", config.Enabled),
		slog.Float64("daily_limit", config.DailyLimit),
		slog.Float64("monthly_limit", config.MonthlyLimit),
	)
}

// GetConfig returns the current configuration
func (e *Enforcer) GetConfig() *Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config
}
