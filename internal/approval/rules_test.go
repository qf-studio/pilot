package approval

import (
	"context"
	"testing"
	"time"
)

func TestRuleEvaluator_ConsecutiveFailures_Matches(t *testing.T) {
	rules := []Rule{
		{
			Name:    "fail-3",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionConsecutiveFailures,
				Threshold: 3,
			},
		},
	}

	re := NewRuleEvaluator(rules)

	tests := []struct {
		name     string
		failures int
		wantNil  bool
	}{
		{"zero failures", 0, true},
		{"below threshold", 2, true},
		{"at threshold", 3, false},
		{"above threshold", 5, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := RuleContext{
				TaskID:              "TASK-01",
				ConsecutiveFailures: tt.failures,
			}
			result := re.Evaluate(ctx)
			if tt.wantNil && result != nil {
				t.Errorf("expected nil, got rule %q", result.Name)
			}
			if !tt.wantNil && result == nil {
				t.Error("expected matching rule, got nil")
			}
		})
	}
}

func TestRuleEvaluator_ConsecutiveFailures_ZeroThreshold(t *testing.T) {
	rules := []Rule{
		{
			Name:    "bad-threshold",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionConsecutiveFailures,
				Threshold: 0, // Invalid — should never match
			},
		},
	}

	re := NewRuleEvaluator(rules)
	result := re.Evaluate(RuleContext{ConsecutiveFailures: 5})
	if result != nil {
		t.Errorf("expected nil for zero threshold, got rule %q", result.Name)
	}
}

func TestRuleEvaluator_DisabledRulesSkipped(t *testing.T) {
	rules := []Rule{
		{
			Name:    "disabled-rule",
			Enabled: false,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionConsecutiveFailures,
				Threshold: 1,
			},
		},
	}

	re := NewRuleEvaluator(rules)
	result := re.Evaluate(RuleContext{ConsecutiveFailures: 10})
	if result != nil {
		t.Error("disabled rule should not match")
	}
}

func TestRuleEvaluator_ReturnsFirstMatch(t *testing.T) {
	rules := []Rule{
		{
			Name:    "first",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionConsecutiveFailures,
				Threshold: 2,
			},
		},
		{
			Name:    "second",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionConsecutiveFailures,
				Threshold: 3,
			},
		},
	}

	re := NewRuleEvaluator(rules)
	result := re.Evaluate(RuleContext{ConsecutiveFailures: 5})
	if result == nil {
		t.Fatal("expected a match")
	}
	if result.Name != "first" {
		t.Errorf("expected first rule, got %q", result.Name)
	}
}

func TestRuleEvaluator_NoRulesReturnsNil(t *testing.T) {
	re := NewRuleEvaluator(nil)
	result := re.Evaluate(RuleContext{ConsecutiveFailures: 10})
	if result != nil {
		t.Error("expected nil for empty rule set")
	}
}

func TestRuleEvaluator_UnknownConditionType(t *testing.T) {
	rules := []Rule{
		{
			Name:    "unknown",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      "nonexistent_type",
				Threshold: 1,
			},
		},
	}

	re := NewRuleEvaluator(rules)
	result := re.Evaluate(RuleContext{ConsecutiveFailures: 10})
	if result != nil {
		t.Error("unknown condition type should not match")
	}
}

func TestRuleEvaluator_EvaluateForStage_Filters(t *testing.T) {
	rules := []Rule{
		{
			Name:    "pre-merge-rule",
			Enabled: true,
			Stage:   StagePreMerge,
			Condition: Condition{
				Type:      ConditionConsecutiveFailures,
				Threshold: 1,
			},
		},
		{
			Name:    "pre-exec-rule",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionConsecutiveFailures,
				Threshold: 1,
			},
		},
	}

	re := NewRuleEvaluator(rules)
	ctx := RuleContext{ConsecutiveFailures: 5}

	// Filter to pre_execution only
	result := re.EvaluateForStage(ctx, StagePreExecution)
	if result == nil {
		t.Fatal("expected a match for pre_execution")
	}
	if result.Name != "pre-exec-rule" {
		t.Errorf("expected pre-exec-rule, got %q", result.Name)
	}

	// Filter to post_failure — no rules for that stage
	result = re.EvaluateForStage(ctx, StagePostFailure)
	if result != nil {
		t.Errorf("expected nil for post_failure, got %q", result.Name)
	}
}

func TestManager_RuleTriggered_StageDisabled(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = false // Stage disabled
	config.PreExecution.Timeout = 100 * time.Millisecond
	config.Rules = []Rule{
		{
			Name:    "fail-gate",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionConsecutiveFailures,
				Threshold: 3,
			},
		},
	}

	m := NewManager(config)
	m.SetRuleEvaluator(NewRuleEvaluator(config.Rules))

	handler := &mockHandler{
		name: "test",
		respondWith: &Response{
			RequestID:  "rule-triggered",
			Decision:   DecisionApproved,
			ApprovedBy: "reviewer",
		},
	}
	m.RegisterHandler(handler)

	req := &Request{
		ID:     "rule-triggered",
		TaskID: "TASK-01",
		Stage:  StagePreExecution,
		Title:  "Task after failures",
		Metadata: map[string]interface{}{
			"consecutive_failures": 5, // Exceeds threshold of 3
		},
		CreatedAt: time.Now(),
	}

	resp, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT auto-approve — rule triggered
	if resp.Decision != DecisionApproved {
		t.Errorf("expected approved from handler, got %s", resp.Decision)
	}
	if resp.ApprovedBy != "reviewer" {
		t.Errorf("expected reviewer, got %s", resp.ApprovedBy)
	}

	// Handler should have received the request (not auto-approved)
	if len(handler.sentReqs) != 1 {
		t.Errorf("expected handler to receive request, got %d", len(handler.sentReqs))
	}
}

func TestManager_NoRuleMatch_StageDisabled_AutoApproves(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = false // Stage disabled
	config.Rules = []Rule{
		{
			Name:    "fail-gate",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionConsecutiveFailures,
				Threshold: 3,
			},
		},
	}

	m := NewManager(config)
	m.SetRuleEvaluator(NewRuleEvaluator(config.Rules))

	req := &Request{
		ID:     "no-rule-match",
		TaskID: "TASK-02",
		Stage:  StagePreExecution,
		Title:  "Task with few failures",
		Metadata: map[string]interface{}{
			"consecutive_failures": 1, // Below threshold
		},
		CreatedAt: time.Now(),
	}

	resp, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should auto-approve — stage disabled and rule didn't match
	if resp.Decision != DecisionApproved {
		t.Errorf("expected auto-approve, got %s", resp.Decision)
	}
	if resp.ApprovedBy != "system" {
		t.Errorf("expected system, got %s", resp.ApprovedBy)
	}
}

func TestManager_NoRuleEvaluator_StageDisabled_AutoApproves(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = false

	m := NewManager(config)
	// No SetRuleEvaluator call

	req := &Request{
		ID:     "no-evaluator",
		TaskID: "TASK-03",
		Stage:  StagePreExecution,
		Title:  "Normal task",
		Metadata: map[string]interface{}{
			"consecutive_failures": 10,
		},
		CreatedAt: time.Now(),
	}

	resp, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Decision != DecisionApproved {
		t.Errorf("expected auto-approve without evaluator, got %s", resp.Decision)
	}
}

func TestManager_RuleTriggered_NoMetadata_AutoApproves(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = false
	config.Rules = []Rule{
		{
			Name:    "fail-gate",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionConsecutiveFailures,
				Threshold: 3,
			},
		},
	}

	m := NewManager(config)
	m.SetRuleEvaluator(NewRuleEvaluator(config.Rules))

	req := &Request{
		ID:        "no-metadata",
		TaskID:    "TASK-04",
		Stage:     StagePreExecution,
		Title:     "Task without metadata",
		CreatedAt: time.Now(),
		// No Metadata — checkRuleTriggers returns nil
	}

	resp, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Decision != DecisionApproved {
		t.Errorf("expected auto-approve without metadata, got %s", resp.Decision)
	}
}

func TestManager_ShouldRequireApproval(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.Rules = []Rule{
		{
			Name:    "fail-gate",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionConsecutiveFailures,
				Threshold: 3,
			},
		},
	}

	m := NewManager(config)
	m.SetRuleEvaluator(NewRuleEvaluator(config.Rules))

	// Should match
	rule := m.ShouldRequireApproval(RuleContext{
		TaskID:              "TASK-01",
		ConsecutiveFailures: 5,
	})
	if rule == nil {
		t.Fatal("expected rule to match")
	}
	if rule.Name != "fail-gate" {
		t.Errorf("expected fail-gate, got %q", rule.Name)
	}

	// Should not match
	rule = m.ShouldRequireApproval(RuleContext{
		TaskID:              "TASK-02",
		ConsecutiveFailures: 1,
	})
	if rule != nil {
		t.Errorf("expected nil, got %q", rule.Name)
	}
}

func TestManager_ShouldRequireApproval_NoEvaluator(t *testing.T) {
	m := NewManager(nil)
	rule := m.ShouldRequireApproval(RuleContext{ConsecutiveFailures: 100})
	if rule != nil {
		t.Error("expected nil without evaluator")
	}
}
