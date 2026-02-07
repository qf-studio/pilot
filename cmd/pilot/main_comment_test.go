package main

import (
	"strings"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/executor"
)

func TestBuildExecutionComment_FullResult(t *testing.T) {
	result := &executor.ExecutionResult{
		Success:          true,
		Duration:         2*time.Minute + 34*time.Second,
		ModelName:        "claude-opus-4-6",
		TokensInput:      32100,
		TokensOutput:     13100,
		TokensTotal:      45200,
		EstimatedCostUSD: 0.23,
		FilesChanged:     5,
		LinesAdded:       42,
		LinesRemoved:     8,
		PRUrl:            "https://github.com/org/repo/pull/456",
	}
	comment := buildExecutionComment(result, "pilot/GH-123")

	checks := []string{
		"✅ Pilot completed!",
		"| Duration | 2m34s |",
		"| Model | `claude-opus-4-6` |",
		"45.2K",
		"~$0.23",
		"5 changed (+42 -8)",
		"`pilot/GH-123`",
		"pull/456",
	}
	for _, check := range checks {
		if !strings.Contains(comment, check) {
			t.Errorf("comment missing %q\nGot:\n%s", check, comment)
		}
	}
}

func TestBuildExecutionComment_MinimalResult(t *testing.T) {
	result := &executor.ExecutionResult{
		Success:  true,
		Duration: 30 * time.Second,
	}
	comment := buildExecutionComment(result, "")

	if !strings.Contains(comment, "✅ Pilot completed!") {
		t.Error("missing header")
	}
	if strings.Contains(comment, "Tokens") {
		t.Error("should not contain Tokens row for zero tokens")
	}
	if strings.Contains(comment, "Cost") {
		t.Error("should not contain Cost row for zero cost")
	}
}

func TestBuildExecutionComment_WithIntentWarning(t *testing.T) {
	result := &executor.ExecutionResult{
		Success:       true,
		Duration:      1 * time.Minute,
		IntentWarning: "Diff adds logging but issue asks for rate limiting",
	}
	comment := buildExecutionComment(result, "pilot/GH-99")

	if !strings.Contains(comment, "⚠️ **Intent Warning:**") {
		t.Error("missing intent warning")
	}
	if !strings.Contains(comment, "rate limiting") {
		t.Error("missing warning reason")
	}
}

func TestBuildFailureComment(t *testing.T) {
	result := &executor.ExecutionResult{
		Error:            "build failed: undefined method foo",
		Duration:         45 * time.Second,
		ModelName:        "claude-haiku",
		EstimatedCostUSD: 0.01,
	}
	comment := buildFailureComment(result)

	if !strings.Contains(comment, "❌") {
		t.Error("missing failure icon")
	}
	if !strings.Contains(comment, "<details>") {
		t.Error("missing collapsible details")
	}
	if !strings.Contains(comment, "undefined method foo") {
		t.Error("missing error message")
	}
}

func TestBuildFailureComment_NilResult(t *testing.T) {
	comment := buildFailureComment(nil)
	if !strings.Contains(comment, "❌ Pilot execution failed") {
		t.Error("missing failure header")
	}
	if strings.Contains(comment, "<details>") {
		t.Error("should not have details for nil result")
	}
}

func TestFormatTokenCountComment(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0K"},
		{45200, "45.2K"},
		{1500000, "1.5M"},
	}
	for _, tt := range tests {
		got := formatTokenCountComment(tt.input)
		if got != tt.want {
			t.Errorf("formatTokenCountComment(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
