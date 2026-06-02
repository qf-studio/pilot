package executor

import "testing"

// TestTerminalStatus verifies the dispatcher classifier maps execution outcomes
// to the right persisted status so the dashboard's "failed" count reflects
// genuine failures only (TASK-358).
func TestTerminalStatus(t *testing.T) {
	tests := []struct {
		name   string
		result *ExecutionResult
		want   string
	}{
		{"nil result", nil, "failed"},
		{"success", &ExecutionResult{Success: true}, "completed"},
		{"declined flag", &ExecutionResult{Declined: true}, "declined"},
		{"declined outcome", &ExecutionResult{Outcome: "declined"}, "declined"},
		{"no_op outcome", &ExecutionResult{Outcome: "no_op"}, "no_op"},
		{"no_commits outcome", &ExecutionResult{Outcome: "no_commits"}, "no_op"},
		{"stalled outcome", &ExecutionResult{Outcome: "stalled"}, "stalled"},
		{"budget_exceeded outcome", &ExecutionResult{Outcome: "budget_exceeded"}, "stalled"},
		{
			"no-op via error signature (phantom no-op, work already on base)",
			&ExecutionResult{Error: "no new commit produced — post-push SHA matches base branch"},
			"no_op",
		},
		{
			"no-op via no_changes signature",
			&ExecutionResult{Error: "no_changes: Claude completed but made no code changes after retry"},
			"no_op",
		},
		{
			"no-op via empty-branch PR guard",
			&ExecutionResult{Error: "no_changes: branch has no commits relative to base (PR guard)"},
			"no_op",
		},
		{
			"stalled via error signature",
			&ExecutionResult{Error: "session stalled: no agent event for >10m0s"},
			"stalled",
		},
		{
			"budget via error signature",
			&ExecutionResult{Error: "per-task budget limit exceeded: tokens"},
			"stalled",
		},
		{
			"genuine failure falls through",
			&ExecutionResult{Error: "go build: undefined: Foo"},
			"failed",
		},
		{
			"empty non-success is a failure",
			&ExecutionResult{},
			"failed",
		},
		{
			"explicit Outcome wins over a misleading error string",
			&ExecutionResult{Outcome: "stalled", Error: "no new commit produced"},
			"stalled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TerminalStatus(tt.result); got != tt.want {
				t.Errorf("TerminalStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTerminalPhaseLabel verifies the dashboard progress phase matches the status.
func TestTerminalPhaseLabel(t *testing.T) {
	cases := map[string]string{
		"no_op":    "No-op",
		"stalled":  "Stalled",
		"declined": "Declined",
		"failed":   "Failed",
		"anything": "Failed",
	}
	for status, want := range cases {
		if got := terminalPhaseLabel(status); got != want {
			t.Errorf("terminalPhaseLabel(%q) = %q, want %q", status, got, want)
		}
	}
}
