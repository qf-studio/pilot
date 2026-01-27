package budget

import (
	"errors"
	"time"
)

// Errors for budget enforcement
var (
	ErrDailyLimitExceeded   = errors.New("daily budget limit exceeded")
	ErrMonthlyLimitExceeded = errors.New("monthly budget limit exceeded")
	ErrTaskTokenLimit       = errors.New("per-task token limit exceeded")
	ErrTaskDurationLimit    = errors.New("per-task duration limit exceeded")
	ErrBudgetPaused         = errors.New("budget enforcement: new tasks paused")
)

// Config holds cost control configuration
type Config struct {
	Enabled      bool            `yaml:"enabled" json:"enabled"`
	DailyLimit   float64         `yaml:"daily_limit" json:"daily_limit"`     // USD
	MonthlyLimit float64         `yaml:"monthly_limit" json:"monthly_limit"` // USD
	PerTask      PerTaskConfig   `yaml:"per_task" json:"per_task"`
	OnExceed     ExceedAction    `yaml:"on_exceed" json:"on_exceed"`
	Thresholds   ThresholdConfig `yaml:"thresholds" json:"thresholds"`
}

// PerTaskConfig defines per-task limits
type PerTaskConfig struct {
	MaxTokens   int64         `yaml:"max_tokens" json:"max_tokens"`     // Maximum tokens per task
	MaxDuration time.Duration `yaml:"max_duration" json:"max_duration"` // Maximum execution time
}

// ExceedAction defines what to do when limits are exceeded
type ExceedAction struct {
	Daily   Action `yaml:"daily" json:"daily"`
	Monthly Action `yaml:"monthly" json:"monthly"`
	PerTask Action `yaml:"per_task" json:"per_task"`
}

// Action represents the action to take when a limit is exceeded
type Action string

const (
	ActionWarn  Action = "warn"  // Notify but continue
	ActionPause Action = "pause" // Stop new tasks, finish current
	ActionStop  Action = "stop"  // Terminate immediately
)

// ThresholdConfig defines warning thresholds
type ThresholdConfig struct {
	WarnPercent float64 `yaml:"warn_percent" json:"warn_percent"` // Warn at this percentage (e.g., 80)
}

// DefaultConfig returns sensible default budget configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:      false, // Disabled by default
		DailyLimit:   50.00,
		MonthlyLimit: 500.00,
		PerTask: PerTaskConfig{
			MaxTokens:   100000,
			MaxDuration: 30 * time.Minute,
		},
		OnExceed: ExceedAction{
			Daily:   ActionPause,
			Monthly: ActionStop,
			PerTask: ActionStop,
		},
		Thresholds: ThresholdConfig{
			WarnPercent: 80,
		},
	}
}

// Status represents current budget status
type Status struct {
	DailySpent    float64   `json:"daily_spent"`
	DailyLimit    float64   `json:"daily_limit"`
	DailyPercent  float64   `json:"daily_percent"`
	MonthlySpent  float64   `json:"monthly_spent"`
	MonthlyLimit  float64   `json:"monthly_limit"`
	MonthlyPercent float64  `json:"monthly_percent"`
	IsPaused      bool      `json:"is_paused"`
	PauseReason   string    `json:"pause_reason,omitempty"`
	BlockedTasks  int       `json:"blocked_tasks"`
	LastUpdated   time.Time `json:"last_updated"`
}

// IsExceeded returns true if any limit is exceeded
func (s *Status) IsExceeded() bool {
	return s.DailyPercent >= 100 || s.MonthlyPercent >= 100
}

// IsWarning returns true if approaching limits
func (s *Status) IsWarning(warnPercent float64) bool {
	return s.DailyPercent >= warnPercent || s.MonthlyPercent >= warnPercent
}

// CheckResult represents the result of a budget check
type CheckResult struct {
	Allowed     bool   `json:"allowed"`
	Action      Action `json:"action"`
	Reason      string `json:"reason,omitempty"`
	DailyLeft   float64 `json:"daily_left"`
	MonthlyLeft float64 `json:"monthly_left"`
}
