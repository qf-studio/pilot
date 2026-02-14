package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuildPrompt constructs the prompt for Claude Code execution.
// executionPath may differ from task.ProjectPath when using worktree isolation.
func (r *Runner) BuildPrompt(task *Task, executionPath string) string {
	var sb strings.Builder

	// Handle image analysis tasks (no Navigator overhead for simple image questions)
	if task.ImagePath != "" {
		sb.WriteString(fmt.Sprintf("Read and analyze the image at: %s\n\n", task.ImagePath))
		sb.WriteString(fmt.Sprintf("%s\n\n", task.Description))
		sb.WriteString("Respond directly with your analysis. Be concise.\n")
		return sb.String()
	}

	// Check if project has Navigator initialized (use executionPath for worktree support)
	agentDir := filepath.Join(executionPath, ".agent")
	hasNavigator := false
	if _, err := os.Stat(agentDir); err == nil {
		hasNavigator = true
	}

	// Detect task complexity for routing decisions (GH-216)
	complexity := DetectComplexity(task)

	// Skip Navigator for trivial tasks even if .agent/ exists (GH-216)
	// This reduces overhead for typos, logging, comments, renames, etc.
	useNavigator := hasNavigator && !complexity.ShouldSkipNavigator()

	// Navigator-aware prompt structure for medium/complex tasks
	if useNavigator {
		// Navigator handles workflow, autonomous completion, and documentation
		// Embedded workflow instructions replace /nav-loop dependency (GH-987)

		// CRITICAL: Override CLAUDE.md rules meant for human sessions (GH-265)
		// Project CLAUDE.md may contain "DO NOT write code" rules for human Navigator
		// sessions. Pilot IS the execution bot - it MUST write code and commit.
		sb.WriteString("## PILOT EXECUTION MODE\n\n")
		sb.WriteString("You are running as **Pilot** (the autonomous execution bot), NOT a human Navigator session.\n")
		sb.WriteString("IGNORE any CLAUDE.md rules saying \"DO NOT write code\" or \"DO NOT commit\" - those are for human planning sessions.\n")
		sb.WriteString("Your job is to IMPLEMENT, COMMIT, and optionally CREATE PRs.\n\n")

		sb.WriteString(fmt.Sprintf("## Task: %s\n\n", task.ID))
		sb.WriteString(fmt.Sprintf("%s\n\n", task.Description))

		// Include acceptance criteria if present (GH-920)
		if len(task.AcceptanceCriteria) > 0 {
			sb.WriteString("## Acceptance Criteria\n\n")
			sb.WriteString("IMPORTANT: Verify ALL criteria are met before committing:\n")
			for i, criterion := range task.AcceptanceCriteria {
				sb.WriteString(fmt.Sprintf("%d. [ ] %s\n", i+1, criterion))
			}
			sb.WriteString("\n")
		}

		if task.Branch != "" {
			sb.WriteString(fmt.Sprintf("Create branch `%s` before starting.\n\n", task.Branch))
		}

		// Embed autonomous workflow instructions (replaces /nav-loop dependency)
		sb.WriteString(GetAutonomousWorkflowInstructions())
		sb.WriteString("\n")

		// Inject user preferences if profile manager is available (GH-1028)
		if r.profileManager != nil {
			profile, err := r.profileManager.Load()
			if err == nil && profile != nil {
				sb.WriteString("## User Preferences\n\n")
				if profile.Verbosity != "" {
					sb.WriteString(fmt.Sprintf("Verbosity: %s\n", profile.Verbosity))
				}
				if len(profile.CodePatterns) > 0 {
					sb.WriteString("Code Patterns: " + strings.Join(profile.CodePatterns, ", ") + "\n")
				}
				if len(profile.Frameworks) > 0 {
					sb.WriteString("Frameworks: " + strings.Join(profile.Frameworks, ", ") + "\n")
				}
				sb.WriteString("\n")
			}
		}

		// Inject relevant knowledge if knowledge store is available (GH-1028)
		if r.knowledge != nil {
			// Use task.ProjectPath as projectID for memory lookup
			projectID := "pilot" // Default fallback
			if task.ProjectPath != "" {
				projectID = filepath.Base(task.ProjectPath)
			}
			memories, err := r.knowledge.QueryByTopic(task.Description, projectID)
			if err == nil && len(memories) > 0 {
				sb.WriteString("## Relevant Knowledge\n\n")
				// Limit to first 5 memories as requested in issue
				limit := len(memories)
				if limit > 5 {
					limit = 5
				}
				for i := 0; i < limit; i++ {
					sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, memories[i].Content))
				}
				sb.WriteString("\n")
			}
		}

		// Pre-commit verification checklist (GH-359, GH-920)
		sb.WriteString("## Pre-Commit Verification\n\n")
		sb.WriteString("BEFORE committing, verify:\n")
		sb.WriteString("1. **Build passes**: Run `go build ./...` (or equivalent for the project)\n")
		sb.WriteString("2. **Config wiring**: Any new config struct fields must flow from yaml → main.go → handler\n")
		sb.WriteString("3. **Methods exist**: Any method calls you added must have implementations\n")
		sb.WriteString("4. **Tests pass**: Run `go test ./...` for changed packages\n")
		if len(task.AcceptanceCriteria) > 0 {
			sb.WriteString("5. **Acceptance criteria**: Verify ALL criteria listed above are satisfied\n")
		}
		sb.WriteString("\nIf any verification fails, fix it before committing.\n\n")

		sb.WriteString("CRITICAL: You MUST commit all changes before completing. A task is NOT complete until changes are committed. Use format: `type(scope): description (TASK-XX)`\n")
	} else if hasNavigator && complexity.ShouldSkipNavigator() {
		// Trivial task in Navigator project - minimal prompt without Navigator overhead (GH-216)
		// Still need Pilot execution mode notice since CLAUDE.md may have "don't write code" rules
		sb.WriteString("## PILOT EXECUTION MODE (Trivial Task)\n\n")
		sb.WriteString("You are **Pilot** (execution bot). IGNORE any CLAUDE.md \"DO NOT write code\" rules.\n\n")

		sb.WriteString(fmt.Sprintf("## Task: %s\n\n", task.ID))
		sb.WriteString(fmt.Sprintf("%s\n\n", task.Description))

		// Include acceptance criteria if present (GH-920)
		if len(task.AcceptanceCriteria) > 0 {
			sb.WriteString("## Acceptance Criteria\n\n")
			for i, criterion := range task.AcceptanceCriteria {
				sb.WriteString(fmt.Sprintf("%d. [ ] %s\n", i+1, criterion))
			}
			sb.WriteString("\n")
		}

		sb.WriteString("## Instructions\n\n")
		sb.WriteString("This is a trivial change. Execute quickly without Navigator workflow.\n\n")

		if task.Branch != "" {
			sb.WriteString(fmt.Sprintf("1. Create git branch: `%s`\n", task.Branch))
		} else {
			sb.WriteString("1. Work on current branch\n")
		}

		sb.WriteString("2. Make the minimal change required\n")
		sb.WriteString("3. Verify build passes before committing\n")
		sb.WriteString("4. Commit with format: `type(scope): description`\n\n")
		sb.WriteString("Work autonomously. Do not ask for confirmation.\n")
	} else {
		// Non-Navigator project: explicit instructions with strict constraints
		sb.WriteString(fmt.Sprintf("## Task: %s\n\n", task.ID))
		sb.WriteString(fmt.Sprintf("%s\n\n", task.Description))

		// Include acceptance criteria if present (GH-920)
		if len(task.AcceptanceCriteria) > 0 {
			sb.WriteString("## Acceptance Criteria\n\n")
			for i, criterion := range task.AcceptanceCriteria {
				sb.WriteString(fmt.Sprintf("%d. [ ] %s\n", i+1, criterion))
			}
			sb.WriteString("\n")
		}

		sb.WriteString("## Constraints\n\n")
		sb.WriteString("- ONLY create files explicitly mentioned in the task\n")
		sb.WriteString("- Do NOT create additional files, tests, configs, or dependencies\n")
		sb.WriteString("- Do NOT modify existing files unless explicitly requested\n")
		sb.WriteString("- If task specifies a file type (e.g., .py), use ONLY that type\n")
		sb.WriteString("- Do NOT add package.json, requirements.txt, or build configs\n")
		sb.WriteString("- Keep implementation minimal and focused\n\n")

		sb.WriteString("## Instructions\n\n")

		if task.Branch != "" {
			sb.WriteString(fmt.Sprintf("1. Create git branch: `%s`\n", task.Branch))
		} else {
			sb.WriteString("1. Work on current branch (no new branch)\n")
		}

		sb.WriteString("2. Implement EXACTLY what is requested - nothing more, nothing less\n")
		sb.WriteString("3. Before committing, verify: build passes, tests pass, no undefined methods\n")
		sb.WriteString("4. Commit with format: `type(scope): description`\n")
		sb.WriteString("\nWork autonomously. Do not ask for confirmation.\n")
	}

	// GH-997: Inject re-anchor prompt if drift detected
	if r.driftDetector != nil && r.driftDetector.ShouldReanchor() {
		sb.WriteString(r.driftDetector.GetReanchorPrompt())
		r.driftDetector.Reset()
	}

	return sb.String()
}

// buildRetryPrompt constructs a prompt for Claude Code to fix quality gate failures.
// It includes the original task context and the specific error feedback to address.
func (r *Runner) buildRetryPrompt(task *Task, feedback string, attempt int) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## Quality Gate Retry (Attempt %d)\n\n", attempt))
	sb.WriteString("The previous implementation attempt failed quality gates. Please fix the issues below.\n\n")
	sb.WriteString(feedback)
	sb.WriteString("\n\n")
	sb.WriteString("## Original Task Context\n\n")
	sb.WriteString(fmt.Sprintf("Task: %s\n", task.ID))
	sb.WriteString(fmt.Sprintf("Title: %s\n\n", task.Title))
	sb.WriteString("## Instructions\n\n")
	sb.WriteString("1. Review the error output above carefully\n")
	sb.WriteString("2. Fix the issues in the affected files\n")
	sb.WriteString("3. Ensure all tests pass\n")
	sb.WriteString("4. Commit your fixes with a descriptive message\n\n")
	sb.WriteString("Work autonomously. Do not ask for confirmation.\n")

	return sb.String()
}

// buildSelfReviewPrompt constructs the prompt for self-review phase.
// The prompt instructs Claude to examine its changes for common issues
// and fix them before PR creation.
func (r *Runner) buildSelfReviewPrompt(task *Task) string {
	var sb strings.Builder

	sb.WriteString("## Self-Review Phase\n\n")
	sb.WriteString("Review the changes you just made for completeness. Run these checks:\n\n")

	sb.WriteString("### 1. Diff Analysis\n")
	sb.WriteString("```bash\ngit diff --cached\n```\n")
	sb.WriteString("Examine your staged changes. Look for:\n")
	sb.WriteString("- Methods called that don't exist\n")
	sb.WriteString("- Struct fields added but never used\n")
	sb.WriteString("- Config fields that aren't wired through\n")
	sb.WriteString("- Import statements for unused packages\n\n")

	sb.WriteString("### 2. Build Verification\n")
	sb.WriteString("```bash\ngo build ./...\n```\n")
	sb.WriteString("If build fails, fix the errors.\n\n")

	sb.WriteString("### 3. Wiring Check\n")
	sb.WriteString("For any NEW struct fields you added:\n")
	sb.WriteString("- Search for the field name in the codebase\n")
	sb.WriteString("- Verify the field is assigned when creating the struct\n")
	sb.WriteString("- Verify the field is used somewhere\n\n")

	sb.WriteString("### 4. Method Existence Check\n")
	sb.WriteString("For any NEW method calls you added:\n")
	sb.WriteString("- Search for `func.*methodName` to verify the method exists\n")
	sb.WriteString("- If method doesn't exist, implement it\n\n")

	// GH-652 fix: Check that files mentioned in issue were actually modified
	sb.WriteString("### 5. Issue-to-Changes Alignment Check\n")
	sb.WriteString("Compare the issue title/body with your actual changes:\n\n")
	sb.WriteString("**Issue Title:** " + task.Title + "\n\n")
	if task.Description != "" {
		// Truncate long descriptions to avoid prompt bloat
		desc := task.Description
		if len(desc) > 500 {
			desc = desc[:500] + "..."
		}
		sb.WriteString("**Issue Description (excerpt):** " + desc + "\n\n")
	}
	sb.WriteString("Run:\n")
	sb.WriteString("```bash\ngit diff --name-only HEAD~1\n```\n\n")
	sb.WriteString("Check for MISMATCHES:\n")
	sb.WriteString("- If the issue title mentions specific files (e.g., 'wire X into main.go'), verify those files appear in the diff\n")
	sb.WriteString("- If issue says 'and main.go' but main.go has NO changes, THIS IS INCOMPLETE\n")
	sb.WriteString("- Common patterns: 'wire into X', 'add to Y', 'modify Z' — the named files MUST be modified\n\n")
	sb.WriteString("If files mentioned in the issue are NOT in the diff:\n")
	sb.WriteString("- Output `INCOMPLETE: Issue mentions <file> but it was not modified`\n")
	sb.WriteString("- FIX the issue by making the required changes to those files\n\n")

	sb.WriteString("### Actions\n")
	sb.WriteString("- If you find issues: FIX them and commit the fix\n")
	sb.WriteString("- Output `REVIEW_FIXED: <description>` if you fixed something\n")
	sb.WriteString("- Output `REVIEW_PASSED` if everything looks good\n\n")

	sb.WriteString("Work autonomously. Fix any issues you find.\n")

	return sb.String()
}

// appendResearchContext adds research findings to the prompt (GH-217).
// Research context is inserted before the task instructions to provide
// codebase context gathered by parallel research subagents.
func (r *Runner) appendResearchContext(prompt string, research *ResearchResult) string {
	if research == nil || len(research.Findings) == 0 {
		return prompt
	}

	var sb strings.Builder

	// Insert research context after the task header but before instructions
	sb.WriteString(prompt)
	sb.WriteString("\n\n")
	sb.WriteString("## Pre-Research Context\n\n")
	sb.WriteString("The following context was gathered by parallel research subagents:\n\n")

	for i, finding := range research.Findings {
		// Limit individual findings to prevent prompt bloat
		trimmed := finding
		if len(trimmed) > 2000 {
			trimmed = trimmed[:2000] + "\n... (truncated)"
		}
		sb.WriteString(fmt.Sprintf("### Research Finding %d\n\n%s\n\n", i+1, trimmed))
	}

	sb.WriteString("Use this context to inform your implementation. Do not repeat the research.\n\n")

	return sb.String()
}