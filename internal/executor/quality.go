package executor

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// QualityGateDetail represents detailed information about a single gate check.
// This is used to pass gate results from the quality package to the executor
// without creating import cycles.
type QualityGateDetail struct {
	Name       string
	Passed     bool
	Duration   time.Duration
	RetryCount int
	Error      string
	// Command is the command that actually ran (after any toolchain fallback).
	Command string
	// ExitCode is the gate command's exit status.
	ExitCode int
	// Output is the combined stdout+stderr of the final attempt.
	Output string
	// RunnerMissing is true when the gate's runner binary was not found (exit 127).
	RunnerMissing bool
}

// QualityOutcome represents the result of quality gate checks.
// This mirrors quality.ExecutionOutcome to avoid import cycles.
type QualityOutcome struct {
	Passed        bool
	ShouldRetry   bool
	RetryFeedback string // Error feedback to send to Claude for retry
	Attempt       int
	// GateDetails contains detailed results for each gate
	GateDetails []QualityGateDetail
	// TotalDuration is the total time spent running all gates
	TotalDuration time.Duration
}

// QualityChecker is an interface for running quality gate checks.
// This interface allows the executor to run quality gates without
// importing the quality package directly, avoiding import cycles.
type QualityChecker interface {
	// Check runs all quality gates and returns the outcome
	Check(ctx context.Context) (*QualityOutcome, error)
}

// QualityCheckerFactory creates a QualityChecker for a specific task.
// This allows the runner to create quality checkers on demand with
// the correct task context without knowing about the quality package.
// The factory is typically implemented in main.go where both packages
// can be imported.
//
// GH-5577: projectPath is the task's project root (Task.ProjectPath) and is
// what per-project config lookups (e.g. a project's `quality:` override) must
// key on; executionPath is the directory the gates actually run in, which for
// an isolated-worktree execution is a throwaway worktree that matches no
// configured project. The two are equal when the task runs in place.
type QualityCheckerFactory func(taskID, projectPath, executionPath string) QualityChecker

const (
	// gateFailureOutputLines caps the output lines quoted per failed gate.
	gateFailureOutputLines = 20
	// gateFailureLineLen caps the length of each quoted output line.
	gateFailureLineLen = 300
)

// qualityGateFailureSummary lists each failed gate with its command and exit
// code, one bullet per gate. Returns "" when no gate in outcome failed.
func qualityGateFailureSummary(outcome *QualityOutcome) string {
	if outcome == nil {
		return ""
	}
	var sb strings.Builder
	for _, g := range outcome.GateDetails {
		if g.Passed {
			continue
		}
		cmd := g.Command
		if cmd == "" {
			cmd = "(unknown command)"
		}
		fmt.Fprintf(&sb, "- gate `%s`: `%s` exited with code %d", g.Name, cmd, g.ExitCode)
		if g.RunnerMissing {
			sb.WriteString(" (command not found: a required tool is not installed on this box)")
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// qualityGateFailureDetails renders the tail of each failed gate's combined
// output in a collapsed <details> block so the cause (e.g. "make: command not
// found" vs a red test) is visible without box access. Returns "" when there is
// no output to show.
func qualityGateFailureDetails(outcome *QualityOutcome) string {
	if outcome == nil {
		return ""
	}
	var sb strings.Builder
	for _, g := range outcome.GateDetails {
		if g.Passed {
			continue
		}
		tail := tailLines(g.Output, gateFailureOutputLines, gateFailureLineLen)
		if tail == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "<details>\n<summary>%s gate output (exit %d, last %d lines max)</summary>\n\n```\n%s\n```\n\n</details>",
			g.Name, g.ExitCode, gateFailureOutputLines, tail)
	}
	return sb.String()
}

// tailLines returns the last n non-blank lines of s, each clipped to maxLen,
// with code fences defanged so the result can sit inside a markdown fence.
func tailLines(s string, n, maxLen int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, " \t\r")
		if l == "" {
			continue
		}
		if len(l) > maxLen {
			l = l[:maxLen] + "..."
		}
		lines = append(lines, strings.ReplaceAll(l, "```", "'''"))
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// formatQualityGateFailure builds the task error for failed quality gates.
// plain is the headline plus per-gate command/exit-code lines (safe for chat
// alerts and webhooks); full additionally carries the collapsed output block
// for the issue comment and execution record.
func formatQualityGateFailure(headline string, outcome *QualityOutcome) (plain, full string) {
	plain = headline
	if summary := qualityGateFailureSummary(outcome); summary != "" {
		plain = headline + "\n\n" + summary
	}
	full = plain
	if details := qualityGateFailureDetails(outcome); details != "" {
		full = plain + "\n\n" + details
	}
	return plain, full
}
