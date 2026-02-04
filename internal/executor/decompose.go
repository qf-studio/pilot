package executor

import (
	"regexp"
	"strconv"
	"strings"
)

// DecomposeConfig configures auto-decomposition of complex tasks.
type DecomposeConfig struct {
	// Enabled controls whether auto-decomposition is active.
	Enabled bool `yaml:"enabled"`

	// MinComplexity is the minimum complexity level that triggers decomposition.
	// Valid values: "complex" (default). Only complex tasks are decomposed.
	MinComplexity string `yaml:"min_complexity"`

	// MaxSubtasks limits the number of subtasks created from decomposition.
	// Default: 5. Range: 2-10.
	MaxSubtasks int `yaml:"max_subtasks"`

	// MinDescriptionWords is the minimum word count in description to trigger decomposition.
	// Tasks with fewer words are not decomposed even if complex.
	// Default: 50.
	MinDescriptionWords int `yaml:"min_description_words"`
}

// DefaultDecomposeConfig returns default decomposition settings.
func DefaultDecomposeConfig() *DecomposeConfig {
	return &DecomposeConfig{
		Enabled:             false, // Disabled by default, opt-in
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 50,
	}
}

// DecomposeResult contains the outcome of task decomposition.
type DecomposeResult struct {
	// Decomposed indicates whether the task was split.
	Decomposed bool

	// Subtasks contains the generated subtasks (empty if not decomposed).
	Subtasks []*Task

	// Reason explains why decomposition did or did not occur.
	Reason string
}

// TaskDecomposer handles breaking complex tasks into smaller subtasks.
type TaskDecomposer struct {
	config *DecomposeConfig
}

// NewTaskDecomposer creates a decomposer with the given configuration.
func NewTaskDecomposer(config *DecomposeConfig) *TaskDecomposer {
	if config == nil {
		config = DefaultDecomposeConfig()
	}
	return &TaskDecomposer{config: config}
}

// Decompose analyzes a task and potentially splits it into subtasks.
// Returns the original task wrapped in DecomposeResult if decomposition
// is not triggered or not applicable.
func (d *TaskDecomposer) Decompose(task *Task) *DecomposeResult {
	if task == nil {
		return &DecomposeResult{
			Decomposed: false,
			Subtasks:   nil,
			Reason:     "nil task",
		}
	}

	// Check if decomposition is enabled
	if !d.config.Enabled {
		return &DecomposeResult{
			Decomposed: false,
			Subtasks:   []*Task{task},
			Reason:     "decomposition disabled",
		}
	}

	// Check complexity threshold
	complexity := DetectComplexity(task)
	if !d.shouldDecompose(complexity) {
		return &DecomposeResult{
			Decomposed: false,
			Subtasks:   []*Task{task},
			Reason:     "complexity below threshold: " + complexity.String(),
		}
	}

	// Check description length
	wordCount := len(strings.Fields(task.Description))
	if wordCount < d.config.MinDescriptionWords {
		return &DecomposeResult{
			Decomposed: false,
			Subtasks:   []*Task{task},
			Reason:     "description too short for decomposition",
		}
	}

	// Analyze and split
	subtasks := d.analyzeAndSplit(task)
	if len(subtasks) <= 1 {
		return &DecomposeResult{
			Decomposed: false,
			Subtasks:   []*Task{task},
			Reason:     "no decomposition points found",
		}
	}

	return &DecomposeResult{
		Decomposed: true,
		Subtasks:   subtasks,
		Reason:     "decomposed into subtasks",
	}
}

// shouldDecompose checks if the complexity meets the threshold.
// Epic tasks are always decomposable since they're too large for single execution.
func (d *TaskDecomposer) shouldDecompose(complexity Complexity) bool {
	// Epic tasks should always be decomposed
	if complexity == ComplexityEpic {
		return true
	}
	switch d.config.MinComplexity {
	case "complex":
		return complexity == ComplexityComplex
	case "medium":
		return complexity == ComplexityComplex || complexity == ComplexityMedium
	default:
		return complexity == ComplexityComplex
	}
}

// analyzeAndSplit breaks a task into subtasks based on structure analysis.
func (d *TaskDecomposer) analyzeAndSplit(task *Task) []*Task {
	desc := task.Description

	// Try different decomposition strategies in order of preference
	var parts []string

	// Strategy 1: Numbered steps (1. 2. 3. or 1) 2) 3))
	parts = extractNumberedSteps(desc)
	if len(parts) >= 2 {
		return d.createSubtasks(task, parts, "step")
	}

	// Strategy 2: Bullet points (- or *)
	parts = extractBulletPoints(desc)
	if len(parts) >= 2 {
		return d.createSubtasks(task, parts, "item")
	}

	// Strategy 3: Acceptance criteria sections
	parts = extractAcceptanceCriteria(desc)
	if len(parts) >= 2 {
		return d.createSubtasks(task, parts, "criteria")
	}

	// Strategy 4: File/module groups mentioned
	parts = extractFileGroups(desc)
	if len(parts) >= 2 {
		return d.createSubtasks(task, parts, "module")
	}

	// No decomposition points found
	return []*Task{task}
}

// createSubtasks generates Task objects from extracted parts.
func (d *TaskDecomposer) createSubtasks(parent *Task, parts []string, partType string) []*Task {
	maxParts := d.config.MaxSubtasks
	if maxParts < 2 {
		maxParts = 2
	}
	if maxParts > 10 {
		maxParts = 10
	}

	if len(parts) > maxParts {
		parts = parts[:maxParts]
	}

	subtasks := make([]*Task, 0, len(parts))
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		subtask := &Task{
			ID:          generateSubtaskID(parent.ID, i+1),
			Title:       truncateTitle(part, 80),
			Description: buildSubtaskDescription(parent, part, i+1, len(parts)),
			ProjectPath: parent.ProjectPath,
			Branch:      parent.Branch,
			BaseBranch:  parent.BaseBranch,
			CreatePR:    false, // Only final subtask creates PR
			Verbose:     parent.Verbose,
		}

		// Last subtask creates the PR
		if i == len(parts)-1 {
			subtask.CreatePR = parent.CreatePR
		}

		subtasks = append(subtasks, subtask)
	}

	return subtasks
}

// extractNumberedSteps finds numbered list items in text.
// Matches: "1. item", "1) item", "Step 1: item"
func extractNumberedSteps(text string) []string {
	// Pattern for numbered lists
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\s*\d+[\.\)]\s+(.+)$`),
		regexp.MustCompile(`(?mi)^\s*step\s+\d+[:\s]+(.+)$`),
	}

	for _, pattern := range patterns {
		matches := pattern.FindAllStringSubmatch(text, -1)
		if len(matches) >= 2 {
			parts := make([]string, 0, len(matches))
			for _, m := range matches {
				if len(m) > 1 {
					parts = append(parts, m[1])
				}
			}
			return parts
		}
	}

	return nil
}

// extractBulletPoints finds bullet list items.
// Matches: "- item", "* item", "• item"
func extractBulletPoints(text string) []string {
	pattern := regexp.MustCompile(`(?m)^\s*[-*•]\s+(.+)$`)
	matches := pattern.FindAllStringSubmatch(text, -1)

	if len(matches) < 2 {
		return nil
	}

	parts := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 {
			// Skip checkbox items that are already marked done
			item := m[1]
			if strings.HasPrefix(item, "[x]") || strings.HasPrefix(item, "[X]") {
				continue
			}
			// Clean checkbox prefix if present
			item = strings.TrimPrefix(item, "[ ] ")
			parts = append(parts, item)
		}
	}

	return parts
}

// extractAcceptanceCriteria finds acceptance criteria sections.
// Matches: "[ ] criteria", "- [ ] criteria"
func extractAcceptanceCriteria(text string) []string {
	pattern := regexp.MustCompile(`(?m)^\s*[-*]?\s*\[\s*\]\s+(.+)$`)
	matches := pattern.FindAllStringSubmatch(text, -1)

	if len(matches) < 2 {
		return nil
	}

	parts := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 {
			parts = append(parts, m[1])
		}
	}

	return parts
}

// extractFileGroups finds file or module groupings.
// Looks for patterns like "file.go", "package/module", "src/component"
func extractFileGroups(text string) []string {
	// Pattern for file paths
	filePattern := regexp.MustCompile(`\b([\w\-]+(?:/[\w\-]+)*\.(?:go|py|ts|tsx|js|jsx|rs|java|rb))\b`)
	matches := filePattern.FindAllString(text, -1)

	if len(matches) < 2 {
		return nil
	}

	// Deduplicate and group by directory
	seen := make(map[string]bool)
	groups := make([]string, 0)
	for _, m := range matches {
		if !seen[m] {
			seen[m] = true
			groups = append(groups, "Implement changes in "+m)
		}
	}

	return groups
}

// generateSubtaskID creates a subtask ID from parent ID.
// Example: "GH-150" -> "GH-150-1", "GH-150-2"
func generateSubtaskID(parentID string, index int) string {
	return parentID + "-" + strconv.Itoa(index)
}

// truncateTitle truncates a string to maxLen, adding ellipsis if needed.
func truncateTitle(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	// Remove newlines
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")

	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// buildSubtaskDescription creates the description for a subtask.
func buildSubtaskDescription(parent *Task, part string, index, total int) string {
	var sb strings.Builder

	sb.WriteString("## Subtask ")
	sb.WriteString(strconv.Itoa(index))
	sb.WriteString(" of ")
	sb.WriteString(strconv.Itoa(total))
	sb.WriteString("\n\n")

	sb.WriteString("**Parent Task:** ")
	sb.WriteString(parent.ID)
	sb.WriteString(" - ")
	sb.WriteString(parent.Title)
	sb.WriteString("\n\n")

	sb.WriteString("## Objective\n\n")
	sb.WriteString(part)
	sb.WriteString("\n\n")

	sb.WriteString("## Context\n\n")
	sb.WriteString("This is part of a larger task that has been decomposed for better execution.\n")
	sb.WriteString("Focus on this specific objective. Other subtasks will handle the remaining work.\n\n")

	if index == total {
		sb.WriteString("**Note:** This is the final subtask. Ensure all previous subtasks are complete before finishing.\n")
	}

	return sb.String()
}

// ShouldDecompose is a convenience function that checks if a task needs decomposition.
// Returns true if the task is complex enough and has sufficient structure for splitting.
func ShouldDecompose(task *Task, config *DecomposeConfig) bool {
	if config == nil || !config.Enabled {
		return false
	}

	complexity := DetectComplexity(task)
	// Epic tasks always need decomposition
	if complexity == ComplexityEpic {
		return true
	}
	if complexity != ComplexityComplex {
		return false
	}

	wordCount := len(strings.Fields(task.Description))
	return wordCount >= config.MinDescriptionWords
}
