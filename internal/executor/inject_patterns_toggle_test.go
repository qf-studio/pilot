package executor

import (
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestShouldInjectPatterns_DefaultEnabled verifies injection defaults to ON
// when SetInjectPatterns has never been called (backward compat for all
// existing callers that don't know about the toggle).
func TestShouldInjectPatterns_DefaultEnabled(t *testing.T) {
	r := &Runner{}
	if !r.shouldInjectPatterns() {
		t.Fatal("shouldInjectPatterns() = false when unset; want true (backward compat default)")
	}
}

// TestShouldInjectPatterns_ExplicitFalse verifies the bench-compliance path:
// SetInjectPatterns(false) disables injection.
func TestShouldInjectPatterns_ExplicitFalse(t *testing.T) {
	r := &Runner{}
	r.SetInjectPatterns(false)
	if r.shouldInjectPatterns() {
		t.Fatal("shouldInjectPatterns() = true after SetInjectPatterns(false); want false")
	}
}

// TestShouldInjectPatterns_ExplicitTrue verifies explicit-true path still works.
func TestShouldInjectPatterns_ExplicitTrue(t *testing.T) {
	r := &Runner{}
	r.SetInjectPatterns(true)
	if !r.shouldInjectPatterns() {
		t.Fatal("shouldInjectPatterns() = false after SetInjectPatterns(true); want true")
	}
}

// TestBuildLocalModePrompt_InjectionDisabled verifies that when the toggle is
// off, the LocalMode prompt does NOT contain any "## Learned Patterns" or
// "## Related Learnings" block — even though a pattern store with matching
// content is wired. This is the Harbor H2 compliance guarantee.
func TestBuildLocalModePrompt_InjectionDisabled(t *testing.T) {
	store, err := memory.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	// Seed a high-confidence pattern that SHOULD appear if injection were on.
	// Content mimics the disqualifying bench-seed-dc2a9153fa20 pattern.
	p := &memory.CrossPattern{
		ID:            "test-oracle-hint",
		Type:          "workflow",
		Title:         "Read test files before implementation",
		Description:   "Always read /tests/test_outputs.py FIRST to understand expected outputs",
		Confidence:    0.95,
		Scope:         "org",
		Occurrences:   1,
		IsAntiPattern: false,
	}
	if err := store.SaveCrossPattern(p); err != nil {
		t.Fatalf("SaveCrossPattern: %v", err)
	}

	r := &Runner{}
	r.SetPatternContext(NewPatternContext(store))
	r.SetInjectPatterns(false) // Harbor bench compliance

	task := &Task{
		ID:          "test-1",
		Title:       "feat: solve cryptanalysis task",
		Description: "Implement a chosen plaintext attack on FEAL.",
		LocalMode:   true,
		ProjectPath: "/app",
	}

	prompt := r.BuildPrompt(task, "/app")

	if strings.Contains(prompt, "## Learned Patterns") {
		t.Error("LocalMode prompt contains '## Learned Patterns' block despite inject_patterns=false")
	}
	if strings.Contains(prompt, "Read test files before implementation") {
		t.Error("LocalMode prompt contains TB2-targeted pattern title despite inject_patterns=false")
	}
	if strings.Contains(prompt, "/tests/test_outputs.py FIRST") {
		t.Error("LocalMode prompt contains oracle-hinting description despite inject_patterns=false")
	}
	if strings.Contains(prompt, "## Related Learnings") {
		t.Error("LocalMode prompt contains '## Related Learnings' KG block despite inject_patterns=false")
	}
}

// TestBuildLocalModePrompt_InjectionEnabled is the positive control: when
// injection is ON (default), the pattern block DOES appear. Confirms the
// disable path is actually doing work.
func TestBuildLocalModePrompt_InjectionEnabled(t *testing.T) {
	store, err := memory.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	p := &memory.CrossPattern{
		ID:            "test-positive",
		Type:          "workflow",
		Title:         "Example learned pattern",
		Description:   "This should appear in prompts when injection is on.",
		Confidence:    0.95,
		Scope:         "org",
		Occurrences:   1,
		IsAntiPattern: false,
	}
	if err := store.SaveCrossPattern(p); err != nil {
		t.Fatalf("SaveCrossPattern: %v", err)
	}

	r := &Runner{}
	r.SetPatternContext(NewPatternContext(store))
	// Do NOT call SetInjectPatterns — default (nil) should mean enabled.

	task := &Task{
		ID:          "test-2",
		Title:       "feat: do something",
		Description: "Do the thing",
		LocalMode:   true,
		ProjectPath: "/app",
	}

	prompt := r.BuildPrompt(task, "/app")

	if !strings.Contains(prompt, "## Learned Patterns") {
		t.Error("LocalMode prompt missing '## Learned Patterns' block when injection enabled (default)")
	}
	if !strings.Contains(prompt, "Example learned pattern") {
		t.Error("LocalMode prompt missing seeded pattern title when injection enabled")
	}
}
