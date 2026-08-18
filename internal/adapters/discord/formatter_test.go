package discord

import (
	"strings"
	"testing"
)

// TestFormatTaskResultStripsInternalSignals verifies that FormatTaskResult
// strips internal Navigator/Pilot signal markers via comms.CleanInternalSignals
// before the output reaches a Discord user (GH-4967 — previously FormatTaskResult
// never called any signal-stripping function, so signals leaked unconditionally).
func TestFormatTaskResultStripsInternalSignals(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		contains []string
		excludes []string
	}{
		{
			name:     "strips NAVIGATOR_STATUS block",
			output:   "done\nNAVIGATOR_STATUS\n━━━━━━━━━━\nreal output",
			contains: []string{"done", "real output"},
			excludes: []string{"NAVIGATOR_STATUS"},
		},
		{
			name:     "strips bare protocol JSON line",
			output:   "checked issue\n{\"v\":2,\"type\":\"status\"}\nnothing to do",
			contains: []string{"checked issue", "nothing to do"},
			excludes: []string{`{"v":2,"type":"status"}`},
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
