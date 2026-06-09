package executor

import "testing"

func TestIsNoOpResult(t *testing.T) {
	tests := []struct {
		name string
		res  *ExecutionResult
		want bool
	}{
		{"nil", nil, false},
		{"success is never no-op", &ExecutionResult{Success: true}, false},
		{
			"ghost-SHA worktree no-op",
			&ExecutionResult{Success: false, Error: "no new commit produced — worktree HEAD matches base branch parent"},
			true,
		},
		{
			"ghost-SHA post-push no-op",
			&ExecutionResult{Success: false, Error: "no new commit produced — post-push SHA matches base branch"},
			true,
		},
		{
			"wrapped subtask no-op still detected (substring)",
			&ExecutionResult{Success: false, Error: "subtask 1/5 failed: no new commit produced — worktree HEAD matches base branch parent"},
			true,
		},
		{
			"explicit no_op outcome",
			&ExecutionResult{Success: false, Outcome: "no_op", Error: "made no code changes"},
			true,
		},
		{
			"real failure is NOT a no-op",
			&ExecutionResult{Success: false, Error: "PR creation refused: title rejected"},
			false,
		},
		{
			"build failure is NOT a no-op",
			&ExecutionResult{Success: false, Error: "execution failed: exit status 1"},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNoOpResult(tt.res); got != tt.want {
				t.Errorf("isNoOpResult() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAggregateSubtaskCost(t *testing.T) {
	agg := &ExecutionResult{TokensInput: 100, TokensOutput: 10}
	sub := &ExecutionResult{
		TokensInput:              50,
		TokensOutput:             5,
		TokensTotal:              55,
		CacheReadInputTokens:     20,
		CacheCreationInputTokens: 3,
		ResearchTokens:           7,
		ModelName:                "claude-opus-4-8",
	}
	aggregateSubtaskCost(agg, sub)

	if agg.TokensInput != 150 || agg.TokensOutput != 15 || agg.TokensTotal != 55 {
		t.Errorf("token aggregation wrong: in=%d out=%d total=%d", agg.TokensInput, agg.TokensOutput, agg.TokensTotal)
	}
	if agg.CacheReadInputTokens != 20 || agg.ResearchTokens != 7 {
		t.Errorf("cache/research aggregation wrong: cacheRead=%d research=%d", agg.CacheReadInputTokens, agg.ResearchTokens)
	}
	if agg.ModelName != "claude-opus-4-8" {
		t.Errorf("ModelName = %q, want claude-opus-4-8", agg.ModelName)
	}
	// Cost helper must not fabricate a commit/delivery from a no-op subtask.
	if agg.CommitSHA != "" || agg.PRUrl != "" {
		t.Errorf("aggregateSubtaskCost must not set CommitSHA/PRUrl, got sha=%q pr=%q", agg.CommitSHA, agg.PRUrl)
	}
}
