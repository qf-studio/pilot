package executor

import (
	"strings"
	"testing"
)

// TestAutonomousWorkflowInstructions_NoConcreteLeakStrings guards against the
// 2026-05-04 OAuth cascade recurrence. The original 2026-05-03 incident was
// traced to a literal example in the planner prompt (epic.go) and "fixed" in
// PR #2562 — but the SAME concrete OAuth example string survived in the
// executor workflow prompt (this file), and on 2026-05-04 the executor LLM
// pattern-matched on it, regenerating the same OAuth code on unrelated tasks
// (#2566/#2568/#2570/#2571/#2572/#2575/#2577).
//
// Keep this list aligned with epic_test.go's TestBuildPlanningPrompt forbidden
// slice. Both prompts feed into the same model and either one can leak.
func TestAutonomousWorkflowInstructions_NoConcreteLeakStrings(t *testing.T) {
	prompt := GetAutonomousWorkflowInstructions()

	forbidden := []string{
		"feat(auth): add OAuth provider integration",
		"feat(auth): add OAuth session logout endpoint",
		"feat(auth): add GitLab OAuth provider integration",
		"feat(auth): add Microsoft and Discord OAuth provider integration",
		"fix(api): handle nil response in webhook handler",
		"chore(deps): upgrade go modules to latest",
	}
	for _, f := range forbidden {
		if strings.Contains(prompt, f) {
			t.Errorf("workflow prompt contains forbidden concrete example %q — must be an ALL_CAPS placeholder template (#2559 recurrence on 2026-05-04)", f)
		}
	}

	// Sanity: the placeholder template should be present.
	if !strings.Contains(prompt, "feat(SCOPE)") {
		t.Errorf("workflow prompt missing ALL_CAPS placeholder template %q", "feat(SCOPE)")
	}
}
