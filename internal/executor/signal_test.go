package executor

import (
	"log/slog"
	"os"
	"testing"
)

func TestSignalParser_ParseSignals(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := NewSignalParser(logger)

	tests := []struct {
		name           string
		input          string
		expectedCount  int
		expectedPhase  string
		expectedProg   int
		expectedExit   bool
	}{
		{
			name: "valid status signal",
			input: "Some text\n```pilot-signal\n{\"v\":2,\"type\":\"status\",\"phase\":\"IMPL\",\"progress\":50}\n```\nMore text",
			expectedCount: 1,
			expectedPhase: "IMPL",
			expectedProg:  50,
			expectedExit:  false,
		},
		{
			name: "exit signal",
			input: "```pilot-signal\n{\"v\":2,\"type\":\"exit\",\"exit_signal\":true,\"success\":true}\n```",
			expectedCount: 1,
			expectedPhase: "",
			expectedProg:  -1,
			expectedExit:  true,
		},
		{
			name: "multiple signals",
			input: "```pilot-signal\n{\"v\":2,\"type\":\"status\",\"phase\":\"RESEARCH\",\"progress\":25}\n```\n\n```pilot-signal\n{\"v\":2,\"type\":\"status\",\"phase\":\"IMPL\",\"progress\":75}\n```",
			expectedCount: 2,
			expectedPhase: "IMPL",
			expectedProg:  75,
			expectedExit:  false,
		},
		{
			name:          "no signals",
			input:         "Just regular text without any signals",
			expectedCount: 0,
			expectedPhase: "",
			expectedProg:  -1,
			expectedExit:  false,
		},
		{
			name:          "invalid JSON",
			input:         "```pilot-signal\n{invalid json}\n```",
			expectedCount: 0,
			expectedPhase: "",
			expectedProg:  -1,
			expectedExit:  false,
		},
		{
			name: "signal with indicators",
			input: "```pilot-signal\n{\"v\":2,\"type\":\"status\",\"phase\":\"VERIFY\",\"progress\":90,\"indicators\":{\"tests_pass\":true,\"code_changes\":true}}\n```",
			expectedCount: 1,
			expectedPhase: "VERIFY",
			expectedProg:  90,
			expectedExit:  false,
		},
		{
			name:          "exit type implies exit",
			input:         "```pilot-signal\n{\"v\":2,\"type\":\"exit\",\"success\":true}\n```",
			expectedCount: 1,
			expectedPhase: "",
			expectedProg:  -1, // No status signal, so GetLatestProgress returns -1
			expectedExit:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signals := parser.ParseSignals(tt.input)

			if len(signals) != tt.expectedCount {
				t.Errorf("expected %d signals, got %d", tt.expectedCount, len(signals))
			}

			if tt.expectedCount > 0 {
				phase := parser.GetLatestPhase(signals)
				if phase != tt.expectedPhase {
					t.Errorf("expected phase %q, got %q", tt.expectedPhase, phase)
				}

				prog := parser.GetLatestProgress(signals)
				if prog != tt.expectedProg {
					t.Errorf("expected progress %d, got %d", tt.expectedProg, prog)
				}

				hasExit := parser.HasExitSignal(signals)
				if hasExit != tt.expectedExit {
					t.Errorf("expected exit %v, got %v", tt.expectedExit, hasExit)
				}
			}
		})
	}
}

func TestSignalParser_ProgressClamping(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := NewSignalParser(logger)

	tests := []struct {
		name           string
		input          string
		expectedProg   int
	}{
		{
			name:         "progress over 100 clamped",
			input:        "```pilot-signal\n{\"v\":2,\"type\":\"status\",\"progress\":150}\n```",
			expectedProg: 100,
		},
		{
			name:         "negative progress clamped",
			input:        "```pilot-signal\n{\"v\":2,\"type\":\"status\",\"progress\":-10}\n```",
			expectedProg: 0,
		},
		{
			name:         "normal progress unchanged",
			input:        "```pilot-signal\n{\"v\":2,\"type\":\"status\",\"progress\":50}\n```",
			expectedProg: 50,
		},
		{
			name:         "zero progress valid",
			input:        "```pilot-signal\n{\"v\":2,\"type\":\"status\",\"progress\":0}\n```",
			expectedProg: 0,
		},
		{
			name:         "100 progress valid",
			input:        "```pilot-signal\n{\"v\":2,\"type\":\"status\",\"progress\":100}\n```",
			expectedProg: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signals := parser.ParseSignals(tt.input)
			if len(signals) != 1 {
				t.Fatalf("expected 1 signal, got %d", len(signals))
			}

			if signals[0].Progress != tt.expectedProg {
				t.Errorf("expected progress %d, got %d", tt.expectedProg, signals[0].Progress)
			}
		})
	}
}

func TestSignalParser_Defaults(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := NewSignalParser(logger)

	// Signal with no version or type should get defaults
	input := "```pilot-signal\n{\"phase\":\"INIT\",\"progress\":10}\n```"
	signals := parser.ParseSignals(input)

	if len(signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(signals))
	}

	if signals[0].Version != 2 {
		t.Errorf("expected version 2, got %d", signals[0].Version)
	}

	if signals[0].Type != SignalTypeStatus {
		t.Errorf("expected type %q, got %q", SignalTypeStatus, signals[0].Type)
	}
}

func TestSignalParser_ComplexContent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := NewSignalParser(logger)

	// Simulate real Claude output with multiple code blocks
	input := `I'll implement the feature now.

First, let me create the file:

` + "```go" + `
package main

func main() {}
` + "```" + `

Progress update:

` + "```pilot-signal" + `
{"v":2,"type":"status","phase":"IMPL","progress":60,"iteration":2,"max_iterations":5,"indicators":{"code_changes":true,"tests_pass":false}}
` + "```" + `

Now running tests...`

	signals := parser.ParseSignals(input)

	if len(signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(signals))
	}

	s := signals[0]
	if s.Phase != "IMPL" {
		t.Errorf("expected phase IMPL, got %q", s.Phase)
	}
	if s.Progress != 60 {
		t.Errorf("expected progress 60, got %d", s.Progress)
	}
	if s.Iteration != 2 {
		t.Errorf("expected iteration 2, got %d", s.Iteration)
	}
	if s.MaxIter != 5 {
		t.Errorf("expected max_iterations 5, got %d", s.MaxIter)
	}
	if !s.Indicators["code_changes"] {
		t.Error("expected code_changes indicator to be true")
	}
	if s.Indicators["tests_pass"] {
		t.Error("expected tests_pass indicator to be false")
	}
}

func TestSignalParser_NilLogger(t *testing.T) {
	// Should not panic with nil logger
	parser := NewSignalParser(nil)
	signals := parser.ParseSignals("```pilot-signal\n{\"type\":\"status\"}\n```")

	if len(signals) != 1 {
		t.Errorf("expected 1 signal, got %d", len(signals))
	}
}

func TestTruncateSignalForLog(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer string", 10, "this is..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		result := truncateSignalForLog(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncateSignalForLog(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}
