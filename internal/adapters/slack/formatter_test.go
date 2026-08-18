package slack

import (
	"strings"
	"testing"
)

// TestFormatTaskResultStripsInternalSignals verifies that FormatTaskResult
// strips internal Navigator/Pilot signal markers via comms.CleanInternalSignals
// before the output reaches a Slack user (GH-4967 — previously FormatTaskResult
// never called any signal-stripping function, so signals leaked unconditionally).
func TestFormatTaskResultStripsInternalSignals(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		contains []string
		excludes []string
	}{
		{
			name:     "strips bracketed exit signal",
			output:   "done\n[EXIT_SIGNAL]\nreal output",
			contains: []string{"real output"},
			excludes: []string{"[EXIT_SIGNAL]"},
		},
		{
			name:     "strips fenced pilot-signal block",
			output:   "done\n```pilot-signal\n{\"v\":2,\"type\":\"exit\",\"exit_signal\":true,\"success\":true}\n```\nreal output",
			contains: []string{"done", "real output"},
			excludes: []string{"pilot-signal", "exit_signal"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTaskResult(tt.output, true, "")
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("FormatTaskResult() = %q, want to contain %q", got, want)
				}
			}
			for _, exclude := range tt.excludes {
				if strings.Contains(got, exclude) {
					t.Errorf("FormatTaskResult() = %q, should NOT contain %q", got, exclude)
				}
			}
		})
	}
}
