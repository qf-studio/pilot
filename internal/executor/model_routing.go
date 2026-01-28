package executor

import (
	"time"
)

// ModelRouter selects the appropriate model and timeout based on task complexity.
// It uses configuration to map complexity levels to model names and timeout durations.
type ModelRouter struct {
	modelConfig   *ModelRoutingConfig
	timeoutConfig *TimeoutConfig
}

// NewModelRouter creates a new ModelRouter with the given configuration.
// If configs are nil, defaults are used.
func NewModelRouter(modelConfig *ModelRoutingConfig, timeoutConfig *TimeoutConfig) *ModelRouter {
	if modelConfig == nil {
		modelConfig = DefaultModelRoutingConfig()
	}
	if timeoutConfig == nil {
		timeoutConfig = DefaultTimeoutConfig()
	}
	return &ModelRouter{
		modelConfig:   modelConfig,
		timeoutConfig: timeoutConfig,
	}
}

// SelectModel returns the appropriate model name for a task based on its complexity.
// If model routing is disabled, returns empty string (use backend default).
func (r *ModelRouter) SelectModel(task *Task) string {
	if r.modelConfig == nil || !r.modelConfig.Enabled {
		return ""
	}

	complexity := DetectComplexity(task)
	return r.GetModelForComplexity(complexity)
}

// GetModelForComplexity returns the model name for a given complexity level.
func (r *ModelRouter) GetModelForComplexity(complexity Complexity) string {
	if r.modelConfig == nil {
		return ""
	}

	switch complexity {
	case ComplexityTrivial:
		return r.modelConfig.Trivial
	case ComplexitySimple:
		return r.modelConfig.Simple
	case ComplexityMedium:
		return r.modelConfig.Medium
	case ComplexityComplex:
		return r.modelConfig.Complex
	default:
		return r.modelConfig.Medium
	}
}

// SelectTimeout returns the appropriate timeout duration for a task based on its complexity.
func (r *ModelRouter) SelectTimeout(task *Task) time.Duration {
	complexity := DetectComplexity(task)
	return r.GetTimeoutForComplexity(complexity)
}

// GetTimeoutForComplexity returns the timeout duration for a given complexity level.
func (r *ModelRouter) GetTimeoutForComplexity(complexity Complexity) time.Duration {
	if r.timeoutConfig == nil {
		return 30 * time.Minute // Fallback default
	}

	var timeoutStr string
	switch complexity {
	case ComplexityTrivial:
		timeoutStr = r.timeoutConfig.Trivial
	case ComplexitySimple:
		timeoutStr = r.timeoutConfig.Simple
	case ComplexityMedium:
		timeoutStr = r.timeoutConfig.Medium
	case ComplexityComplex:
		timeoutStr = r.timeoutConfig.Complex
	default:
		timeoutStr = r.timeoutConfig.Default
	}

	// Parse duration string
	duration, err := time.ParseDuration(timeoutStr)
	if err != nil {
		// Fallback to default if parse fails
		if r.timeoutConfig.Default != "" {
			duration, err = time.ParseDuration(r.timeoutConfig.Default)
			if err != nil {
				return 30 * time.Minute
			}
		} else {
			return 30 * time.Minute
		}
	}

	return duration
}

// IsRoutingEnabled returns true if model routing is enabled.
func (r *ModelRouter) IsRoutingEnabled() bool {
	return r.modelConfig != nil && r.modelConfig.Enabled
}
