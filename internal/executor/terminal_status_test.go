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
		{"rate_limited outcome", &ExecutionResult{Outcome: "rate_limited"}, "rate_limited"},
		{"infra outcome", &ExecutionResult{Outcome: "infra"}, "infra"},
		{"skipped outcome", &ExecutionResult{Outcome: "skipped"}, "skipped"},
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
			"no-op via legacy 'made no code changes' (no prefix)",
			&ExecutionResult{Error: "Claude completed but made no code changes after retry"},
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
			"rate-limited via error signature",
			&ExecutionResult{Error: "You've hit your limit · resets 3pm (Europe/Podgorica)"},
			"rate_limited",
		},
		{
			"infra via OOM kill",
			&ExecutionResult{Error: "oom_killed: Process killed by SIGKILL (exit code 137)"},
			"infra",
		},
		{
			"infra via signal killed",
			&ExecutionResult{Error: "unknown: signal: killed"},
			"infra",
		},
		{
			"infra via push failure",
			&ExecutionResult{Error: "push failed: failed to push: exit status 1: To https://github.com/o/r"},
			"infra",
		},
		{
			"infra via PR creation failure",
			&ExecutionResult{Error: "PR creation failed: failed to create PR: exit status 1"},
			"infra",
		},
		{
			"skipped via stale-queued",
			&ExecutionResult{Error: "stale queued task recovered (no worker picked up)"},
			"skipped",
		},
		{
			"skipped via context canceled",
			&ExecutionResult{Error: "failed to start Claude Code: context canceled"},
			"skipped",
		},
		{
			"skipped via stale-queued epic parent-done refusal (GH-3764)",
			&ExecutionResult{Error: "execution failed: failed to create sub-issues: parent task is already done; refusing to create sub-issues"},
			"skipped",
		},
		{
			"PR creation REFUSED (title guard) stays a genuine failure",
			&ExecutionResult{Error: "PR creation refused: title is not a conventional commit"},
			"failed",
		},
		{
			"quality gates failure stays failed",
			&ExecutionResult{Error: "quality gates failed after 2 auto-retries"},
			"failed",
		},
		{
			"unknown exit status stays failed",
			&ExecutionResult{Error: "unknown: exit status 1"},
			"failed",
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
		{
			"no-op precedence beats infra when both signatures present",
			&ExecutionResult{Error: "no new commit produced; push failed"},
			"no_op",
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
