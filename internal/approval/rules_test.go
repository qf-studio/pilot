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

func TestRuleEvaluator_SpendThreshold_Matches(t *testing.T) {
	rules := []Rule{
		{
			Name:    "spend-5000",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionSpendThreshold,
				Threshold: 5000, // $50.00
			},
		},
	}

	re := NewRuleEvaluator(rules)

	tests := []struct {
		name    string
		spend   int
		wantNil bool
	}{
		{"zero spend", 0, true},
		{"below threshold", 4999, true},
		{"at threshold", 5000, false},
		{"above threshold", 10000, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := RuleContext{
				TaskID:          "TASK-01",
				TotalSpendCents: tt.spend,
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

func TestRuleEvaluator_SpendThreshold_ZeroThreshold(t *testing.T) {
	rules := []Rule{
		{
			Name:    "bad-spend-threshold",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionSpendThreshold,
				Threshold: 0, // Invalid — should never match
			},
		},
	}

	re := NewRuleEvaluator(rules)
	result := re.Evaluate(RuleContext{TotalSpendCents: 99999})
	if result != nil {
		t.Errorf("expected nil for zero threshold, got rule %q", result.Name)
	}
}

func TestRuleEvaluator_SpendThreshold_DisabledSkipped(t *testing.T) {
	rules := []Rule{
		{
			Name:    "disabled-spend",
			Enabled: false,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionSpendThreshold,
				Threshold: 100,
			},
		},
	}

	re := NewRuleEvaluator(rules)
	result := re.Evaluate(RuleContext{TotalSpendCents: 50000})
	if result != nil {
		t.Error("disabled rule should not match")
	}
}

func TestManager_SpendThreshold_RuleTriggered_StageDisabled(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = false // Stage disabled
	config.PreExecution.Timeout = 100 * time.Millisecond
	config.Rules = []Rule{
		{
			Name:    "spend-gate",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionSpendThreshold,
				Threshold: 5000,
			},
		},
	}

	m := NewManager(config)
	m.SetRuleEvaluator(NewRuleEvaluator(config.Rules))

	handler := &mockHandler{
		name: "test",
		respondWith: &Response{
			RequestID:  "spend-triggered",
			Decision:   DecisionApproved,
			ApprovedBy: "reviewer",
		},
	}
	m.RegisterHandler(handler)

	req := &Request{
		ID:     "spend-triggered",
		TaskID: "TASK-01",
		Stage:  StagePreExecution,
		Title:  "Expensive task",
		Metadata: map[string]interface{}{
			"total_spend_cents": 7500, // Exceeds threshold of 5000
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
	if len(handler.sentReqs) != 1 {
		t.Errorf("expected handler to receive request, got %d", len(handler.sentReqs))
	}
}

func TestManager_SpendThreshold_BelowThreshold_AutoApproves(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = false
	config.Rules = []Rule{
		{
			Name:    "spend-gate",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:      ConditionSpendThreshold,
				Threshold: 5000,
			},
		},
	}

	m := NewManager(config)
	m.SetRuleEvaluator(NewRuleEvaluator(config.Rules))

	req := &Request{
		ID:     "spend-below",
		TaskID: "TASK-02",
		Stage:  StagePreExecution,
		Title:  "Cheap task",
		Metadata: map[string]interface{}{
			"total_spend_cents": 1000, // Below threshold
		},
		CreatedAt: time.Now(),
	}

	resp, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Decision != DecisionApproved {
		t.Errorf("expected auto-approve, got %s", resp.Decision)
	}
	if resp.ApprovedBy != "system" {
		t.Errorf("expected system, got %s", resp.ApprovedBy)
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

func TestRuleEvaluator_FilePattern_Matches(t *testing.T) {
	rules := []Rule{
		{
			Name:    "infra-guard",
			Enabled: true,
			Stage:   StagePreMerge,
			Condition: Condition{
				Type:    ConditionFilePattern,
				Pattern: "*.tf",
			},
		},
	}

	re := NewRuleEvaluator(rules)

	tests := []struct {
		name    string
		files   []string
		wantNil bool
	}{
		{"no files", nil, true},
		{"no match", []string{"main.go", "readme.md"}, true},
		{"single match", []string{"main.tf"}, false},
		{"match among others", []string{"main.go", "infra.tf", "readme.md"}, false},
		{"multiple matches", []string{"main.tf", "vars.tf"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := RuleContext{
				TaskID:       "TASK-01",
				ChangedFiles: tt.files,
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

func TestRuleEvaluator_FilePattern_EmptyPattern(t *testing.T) {
	rules := []Rule{
		{
			Name:    "empty-pattern",
			Enabled: true,
			Stage:   StagePreMerge,
			Condition: Condition{
				Type:    ConditionFilePattern,
				Pattern: "",
			},
		},
	}

	re := NewRuleEvaluator(rules)
	result := re.Evaluate(RuleContext{ChangedFiles: []string{"anything.go"}})
	if result != nil {
		t.Errorf("expected nil for empty pattern, got rule %q", result.Name)
	}
}

func TestRuleEvaluator_FilePattern_DisabledSkipped(t *testing.T) {
	rules := []Rule{
		{
			Name:    "disabled-file",
			Enabled: false,
			Stage:   StagePreMerge,
			Condition: Condition{
				Type:    ConditionFilePattern,
				Pattern: "*.tf",
			},
		},
	}

	re := NewRuleEvaluator(rules)
	result := re.Evaluate(RuleContext{ChangedFiles: []string{"main.tf"}})
	if result != nil {
		t.Error("disabled rule should not match")
	}
}

func TestRuleEvaluator_FilePattern_GlobPatterns(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		file    string
		wantNil bool
	}{
		{"star wildcard", "*.go", "main.go", false},
		{"star no match", "*.go", "main.rs", true},
		{"question mark", "test?.go", "test1.go", false},
		{"question mark no match", "test?.go", "test12.go", true},
		{"character class", "[Mm]akefile", "Makefile", false},
		{"character class lower", "[Mm]akefile", "makefile", false},
		{"character class no match", "[Mm]akefile", "xakefile", true},
		{"exact match", "Dockerfile", "Dockerfile", false},
		{"exact no match", "Dockerfile", "Makefile", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := []Rule{
				{
					Name:    "test-rule",
					Enabled: true,
					Stage:   StagePreMerge,
					Condition: Condition{
						Type:    ConditionFilePattern,
						Pattern: tt.pattern,
					},
				},
			}

			re := NewRuleEvaluator(rules)
			ctx := RuleContext{ChangedFiles: []string{tt.file}}
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

func TestRuleEvaluator_FilePattern_InvalidPattern(t *testing.T) {
	rules := []Rule{
		{
			Name:    "bad-pattern",
			Enabled: true,
			Stage:   StagePreMerge,
			Condition: Condition{
				Type:    ConditionFilePattern,
				Pattern: "[invalid", // Unclosed bracket
			},
		},
	}

	re := NewRuleEvaluator(rules)
	result := re.Evaluate(RuleContext{ChangedFiles: []string{"anything.go"}})
	if result != nil {
		t.Errorf("expected nil for invalid pattern, got rule %q", result.Name)
	}
}

func TestManager_FilePattern_RuleTriggered_StageDisabled(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreMerge.Enabled = false // Stage disabled
	config.PreMerge.Timeout = 100 * time.Millisecond
	config.Rules = []Rule{
		{
			Name:    "infra-guard",
			Enabled: true,
			Stage:   StagePreMerge,
			Condition: Condition{
				Type:    ConditionFilePattern,
				Pattern: "*.tf",
			},
		},
	}

	m := NewManager(config)
	m.SetRuleEvaluator(NewRuleEvaluator(config.Rules))

	handler := &mockHandler{
		name: "test",
		respondWith: &Response{
			RequestID:  "file-triggered",
			Decision:   DecisionApproved,
			ApprovedBy: "infra-reviewer",
		},
	}
	m.RegisterHandler(handler)

	req := &Request{
		ID:     "file-triggered",
		TaskID: "TASK-01",
		Stage:  StagePreMerge,
		Title:  "Terraform changes",
		Metadata: map[string]interface{}{
			"changed_files": []string{"main.tf", "variables.tf"},
		},
		CreatedAt: time.Now(),
	}

	resp, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Decision != DecisionApproved {
		t.Errorf("expected approved from handler, got %s", resp.Decision)
	}
	if resp.ApprovedBy != "infra-reviewer" {
		t.Errorf("expected infra-reviewer, got %s", resp.ApprovedBy)
	}
	if len(handler.sentReqs) != 1 {
		t.Errorf("expected handler to receive request, got %d", len(handler.sentReqs))
	}
}

func TestRuleEvaluator_Complexity_Matches(t *testing.T) {
	rules := []Rule{
		{
			Name:    "complex-gate",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:    ConditionComplexity,
				Pattern: "complex", // Trigger at complex or above
			},
		},
	}

	re := NewRuleEvaluator(rules)

	tests := []struct {
		name       string
		complexity string
		wantNil    bool
	}{
		{"empty complexity", "", true},
		{"trivial below threshold", "trivial", true},
		{"simple below threshold", "simple", true},
		{"medium below threshold", "medium", true},
		{"complex at threshold", "complex", false},
		{"epic above threshold", "epic", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := RuleContext{
				TaskID:     "TASK-01",
				Complexity: tt.complexity,
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

func TestRuleEvaluator_Complexity_AllLevels(t *testing.T) {
	// Test each level as threshold — everything at or above matches
	levels := []string{"trivial", "simple", "medium", "complex", "epic"}

	for i, threshold := range levels {
		rules := []Rule{
			{
				Name:    "level-gate",
				Enabled: true,
				Stage:   StagePreExecution,
				Condition: Condition{
					Type:    ConditionComplexity,
					Pattern: threshold,
				},
			},
		}

		re := NewRuleEvaluator(rules)

		for j, actual := range levels {
			ctx := RuleContext{TaskID: "TASK-01", Complexity: actual}
			result := re.Evaluate(ctx)
			shouldMatch := j >= i

			if shouldMatch && result == nil {
				t.Errorf("threshold=%s actual=%s: expected match, got nil", threshold, actual)
			}
			if !shouldMatch && result != nil {
				t.Errorf("threshold=%s actual=%s: expected nil, got match", threshold, actual)
			}
		}
	}
}

func TestRuleEvaluator_Complexity_EmptyPattern(t *testing.T) {
	rules := []Rule{
		{
			Name:    "empty-complexity",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:    ConditionComplexity,
				Pattern: "",
			},
		},
	}

	re := NewRuleEvaluator(rules)
	result := re.Evaluate(RuleContext{Complexity: "complex"})
	if result != nil {
		t.Errorf("expected nil for empty pattern, got rule %q", result.Name)
	}
}

func TestRuleEvaluator_Complexity_UnknownThreshold(t *testing.T) {
	rules := []Rule{
		{
			Name:    "bad-threshold",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:    ConditionComplexity,
				Pattern: "impossible",
			},
		},
	}

	re := NewRuleEvaluator(rules)
	result := re.Evaluate(RuleContext{Complexity: "complex"})
	if result != nil {
		t.Errorf("expected nil for unknown threshold, got rule %q", result.Name)
	}
}

func TestRuleEvaluator_Complexity_UnknownActual(t *testing.T) {
	rules := []Rule{
		{
			Name:    "valid-threshold",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:    ConditionComplexity,
				Pattern: "medium",
			},
		},
	}

	re := NewRuleEvaluator(rules)
	result := re.Evaluate(RuleContext{Complexity: "unknown_level"})
	if result != nil {
		t.Errorf("expected nil for unknown actual complexity, got rule %q", result.Name)
	}
}

func TestRuleEvaluator_Complexity_DisabledSkipped(t *testing.T) {
	rules := []Rule{
		{
			Name:    "disabled-complexity",
			Enabled: false,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:    ConditionComplexity,
				Pattern: "trivial",
			},
		},
	}

	re := NewRuleEvaluator(rules)
	result := re.Evaluate(RuleContext{Complexity: "epic"})
	if result != nil {
		t.Error("disabled rule should not match")
	}
}

func TestManager_Complexity_RuleTriggered_StageDisabled(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = false
	config.PreExecution.Timeout = 100 * time.Millisecond
	config.Rules = []Rule{
		{
			Name:    "complexity-gate",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:    ConditionComplexity,
				Pattern: "complex",
			},
		},
	}

	m := NewManager(config)
	m.SetRuleEvaluator(NewRuleEvaluator(config.Rules))

	handler := &mockHandler{
		name: "test",
		respondWith: &Response{
			RequestID:  "complexity-triggered",
			Decision:   DecisionApproved,
			ApprovedBy: "architect",
		},
	}
	m.RegisterHandler(handler)

	req := &Request{
		ID:     "complexity-triggered",
		TaskID: "TASK-01",
		Stage:  StagePreExecution,
		Title:  "Architecture refactor",
		Metadata: map[string]interface{}{
			"complexity": "complex",
		},
		CreatedAt: time.Now(),
	}

	resp, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Decision != DecisionApproved {
		t.Errorf("expected approved from handler, got %s", resp.Decision)
	}
	if resp.ApprovedBy != "architect" {
		t.Errorf("expected architect, got %s", resp.ApprovedBy)
	}
	if len(handler.sentReqs) != 1 {
		t.Errorf("expected handler to receive request, got %d", len(handler.sentReqs))
	}
}

func TestManager_Complexity_BelowThreshold_AutoApproves(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = false
	config.Rules = []Rule{
		{
			Name:    "complexity-gate",
			Enabled: true,
			Stage:   StagePreExecution,
			Condition: Condition{
				Type:    ConditionComplexity,
				Pattern: "complex",
			},
		},
	}

	m := NewManager(config)
	m.SetRuleEvaluator(NewRuleEvaluator(config.Rules))

	req := &Request{
		ID:     "complexity-below",
		TaskID: "TASK-02",
		Stage:  StagePreExecution,
		Title:  "Simple fix",
		Metadata: map[string]interface{}{
			"complexity": "simple",
		},
		CreatedAt: time.Now(),
	}

	resp, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Decision != DecisionApproved {
		t.Errorf("expected auto-approve, got %s", resp.Decision)
	}
	if resp.ApprovedBy != "system" {
		t.Errorf("expected system, got %s", resp.ApprovedBy)
	}
}

func TestManager_FilePattern_NoMatch_AutoApproves(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreMerge.Enabled = false
	config.Rules = []Rule{
		{
			Name:    "infra-guard",
			Enabled: true,
			Stage:   StagePreMerge,
			Condition: Condition{
				Type:    ConditionFilePattern,
				Pattern: "*.tf",
			},
		},
	}

	m := NewManager(config)
	m.SetRuleEvaluator(NewRuleEvaluator(config.Rules))

	req := &Request{
		ID:     "file-no-match",
		TaskID: "TASK-02",
		Stage:  StagePreMerge,
		Title:  "Go changes only",
		Metadata: map[string]interface{}{
			"changed_files": []string{"main.go", "handler.go"},
		},
		CreatedAt: time.Now(),
	}

	resp, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Decision != DecisionApproved {
		t.Errorf("expected auto-approve, got %s", resp.Decision)
	}
	if resp.ApprovedBy != "system" {
		t.Errorf("expected system, got %s", resp.ApprovedBy)
	}
}
