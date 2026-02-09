package approval

import (
	"log/slog"
	"path/filepath"

	"github.com/alekspetrov/pilot/internal/logging"
)

// Condition type constants
const (
	ConditionConsecutiveFailures = "consecutive_failures"
	ConditionSpendThreshold      = "spend_threshold"
	ConditionFilePattern         = "file_pattern"
	ConditionComplexity          = "complexity"
)

// RuleEvaluator evaluates approval rules against runtime context
type RuleEvaluator struct {
	rules []Rule
	log   *slog.Logger
}

// NewRuleEvaluator creates a new rule evaluator with the given rules
func NewRuleEvaluator(rules []Rule) *RuleEvaluator {
	return &RuleEvaluator{
		rules: rules,
		log:   logging.WithComponent("approval-rules"),
	}
}

// Evaluate checks all enabled rules against the context and returns the first matching rule, or nil.
// If stage is non-empty, only rules for that stage are considered.
func (re *RuleEvaluator) Evaluate(ctx RuleContext) *Rule {
	return re.EvaluateForStage(ctx, "")
}

// EvaluateForStage checks enabled rules for a specific stage against the context.
// Returns the first matching rule, or nil.
func (re *RuleEvaluator) EvaluateForStage(ctx RuleContext, stage Stage) *Rule {
	for i := range re.rules {
		rule := &re.rules[i]
		if !rule.Enabled {
			continue
		}

		if stage != "" && rule.Stage != stage {
			continue
		}

		if re.matches(rule, ctx) {
			re.log.Info("approval rule matched",
				slog.String("rule", rule.Name),
				slog.String("type", rule.Condition.Type),
				slog.String("stage", string(rule.Stage)),
				slog.String("task_id", ctx.TaskID),
			)
			return rule
		}
	}
	return nil
}

// matches checks if a single rule's condition matches the context
func (re *RuleEvaluator) matches(rule *Rule, ctx RuleContext) bool {
	switch rule.Condition.Type {
	case ConditionConsecutiveFailures:
		return re.matchConsecutiveFailures(rule, ctx)
	case ConditionSpendThreshold:
		return re.matchSpendThreshold(rule, ctx)
	case ConditionFilePattern:
		return re.matchFilePattern(rule, ctx)
	case ConditionComplexity:
		return re.matchComplexity(rule, ctx)
	default:
		re.log.Warn("unknown condition type",
			slog.String("rule", rule.Name),
			slog.String("type", rule.Condition.Type),
		)
		return false
	}
}

// matchConsecutiveFailures returns true when consecutive failures meet or exceed the threshold
func (re *RuleEvaluator) matchConsecutiveFailures(rule *Rule, ctx RuleContext) bool {
	if rule.Condition.Threshold <= 0 {
		return false
	}
	return ctx.ConsecutiveFailures >= rule.Condition.Threshold
}

// matchSpendThreshold returns true when total spend in cents meets or exceeds the threshold
func (re *RuleEvaluator) matchSpendThreshold(rule *Rule, ctx RuleContext) bool {
	if rule.Condition.Threshold <= 0 {
		return false
	}
	return ctx.TotalSpendCents >= rule.Condition.Threshold
}

// complexityOrder defines the hierarchy of complexity levels (lower index = simpler).
var complexityOrder = map[string]int{
	"trivial": 0,
	"simple":  1,
	"medium":  2,
	"complex": 3,
	"epic":    4,
}

// matchComplexity returns true when the task's complexity equals or exceeds the threshold.
// The condition's Pattern field holds the minimum complexity level (e.g., "complex").
func (re *RuleEvaluator) matchComplexity(rule *Rule, ctx RuleContext) bool {
	if rule.Condition.Pattern == "" || ctx.Complexity == "" {
		return false
	}
	thresholdLevel, ok := complexityOrder[rule.Condition.Pattern]
	if !ok {
		re.log.Warn("unknown complexity threshold",
			slog.String("rule", rule.Name),
			slog.String("threshold", rule.Condition.Pattern),
		)
		return false
	}
	actualLevel, ok := complexityOrder[ctx.Complexity]
	if !ok {
		re.log.Warn("unknown task complexity",
			slog.String("rule", rule.Name),
			slog.String("complexity", ctx.Complexity),
		)
		return false
	}
	return actualLevel >= thresholdLevel
}

// matchFilePattern returns true when any changed file matches the glob pattern.
// Uses filepath.Match for glob matching (supports *, ?, []).
func (re *RuleEvaluator) matchFilePattern(rule *Rule, ctx RuleContext) bool {
	if rule.Condition.Pattern == "" {
		return false
	}
	if len(ctx.ChangedFiles) == 0 {
		return false
	}
	for _, file := range ctx.ChangedFiles {
		matched, err := filepath.Match(rule.Condition.Pattern, file)
		if err != nil {
			re.log.Warn("invalid file pattern",
				slog.String("rule", rule.Name),
				slog.String("pattern", rule.Condition.Pattern),
				slog.String("error", err.Error()),
			)
			return false
		}
		if matched {
			return true
		}
	}
	return false
}
