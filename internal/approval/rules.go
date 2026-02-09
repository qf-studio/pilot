package approval

import (
	"log/slog"

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
