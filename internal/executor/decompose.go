package executor

import (
	"context"
	"fmt"
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

	// SkipReason is a machine-readable code for why decomposition did not
	// occur (GH-4271). Empty (SkipReasonNone) when Decomposed is true.
	SkipReason SkipReason

	// Complexity is the task's classified complexity at the point this
	// result was produced (GH-4271). Populated on every branch — including
	// early-exit gates (disabled/label/phrase) via the cheap word-count
	// heuristic, never the optional LLM classifier — so callers can judge
	// whether a skip is worth surfacing without re-classifying or triggering
	// an LLM call as a side effect of logging.
	Complexity Complexity

	// DescriptionWords is the word count evaluated against
	// MinDescriptionWords. Only populated when SkipReason is
	// SkipReasonDescriptionTooShort (GH-4271).
	DescriptionWords int
}

// SkipReason is a machine-readable code for why TaskDecomposer did not split
// a task, independent of the human-readable DecomposeResult.Reason string
// (GH-4271). Callers use it to build structured log fields/event details
// without parsing prose.
type SkipReason string

const (
	// SkipReasonNone means decomposition succeeded — there is nothing to report.
	SkipReasonNone SkipReason = ""
	// SkipReasonNilTask means Decompose was called with a nil task.
	SkipReasonNilTask SkipReason = "nil_task"
	// SkipReasonDisabled means decompose.enabled is false.
	SkipReasonDisabled SkipReason = "disabled"
	// SkipReasonNoDecomposeLabel means the task carries the no-decompose label (GH-664).
	SkipReasonNoDecomposeLabel SkipReason = "no_decompose_label"
	// SkipReasonNoDecomposePhrase means the task's title/description contains
	// prose that opts out of decomposition (GH-2783/GH-3597).
	SkipReasonNoDecomposePhrase SkipReason = "no_decompose_phrase"
	// SkipReasonBelowMinComplexity means the classified complexity does not
	// meet decompose.min_complexity.
	SkipReasonBelowMinComplexity SkipReason = "below_min_complexity"
	// SkipReasonDescriptionTooShort means the description word count is below
	// decompose.min_description_words (heuristic mode only, GH-1728).
	SkipReasonDescriptionTooShort SkipReason = "description_too_short"
	// SkipReasonNoSplitPoints means no structural decomposition points
	// (numbered steps/bullets/criteria/file groups) were found, or all
	// extracted parts collapsed to <=1 non-empty subtask (TASK-401 class).
	SkipReasonNoSplitPoints SkipReason = "no_split_points"
)

// NoDecomposeLabel is the GitHub label that bypasses decomposition entirely (GH-664).
const NoDecomposeLabel = "no-decompose"

// NoPlanKeyword is a keyword that users can include in the task title or description
// to bypass epic planning and decomposition (GH-1687).
const NoPlanKeyword = "[no-plan]"

// noDecomposePhrases are precompiled patterns that signal a task must not be split.
// Matched against lowercased title + description (GH-2783).
var noDecomposePhrases = []*regexp.Regexp{
	regexp.MustCompile(`single ac list`),
	regexp.MustCompile(`do not decompose`),
	regexp.MustCompile(`do not split`),
	regexp.MustCompile(`single pilot issue`),
	regexp.MustCompile(`keep as .+ single`),
	regexp.MustCompile(`splitting this would`),
	// GH-3597: "must NOT be decomposed" — the way humans actually write it —
	// reached only the planner LLM advisorily; #3582 split into 4 sub-issues
	// despite opening with exactly this phrasing.
	regexp.MustCompile(`(must|should) not be (decomposed|split)`),
	regexp.MustCompile(`is a standalone task`),
	regexp.MustCompile(`as a single pr\b`),
	regexp.MustCompile(`<!--\s*pilot:no-decompose\s*-->`),
}

// HasNoDecomposePhrase returns true when the task title or description contains
// prose that signals the task must not be split into subtasks (GH-2783).
func HasNoDecomposePhrase(task *Task) bool {
	haystack := strings.ToLower(task.Title + " " + task.Description)
	for _, re := range noDecomposePhrases {
		if re.MatchString(haystack) {
			return true
		}
	}
	return false
}

// TaskDecomposer handles breaking complex tasks into smaller subtasks.
type TaskDecomposer struct {
	config     *DecomposeConfig
	classifier *ComplexityClassifier // Optional LLM classifier (GH-727); nil = use heuristic
}

// NewTaskDecomposer creates a decomposer with the given configuration.
func NewTaskDecomposer(config *DecomposeConfig) *TaskDecomposer {
	if config == nil {
		config = DefaultDecomposeConfig()
	}
	return &TaskDecomposer{config: config}
}

// SetClassifier attaches an LLM complexity classifier to the decomposer (GH-727).
// When set, the classifier is used instead of the word-count heuristic.
func (d *TaskDecomposer) SetClassifier(c *ComplexityClassifier) {
	d.classifier = c
}

// Decompose analyzes a task and potentially splits it into subtasks.
// Returns the original task wrapped in DecomposeResult if decomposition
// is not triggered or not applicable.
func (d *TaskDecomposer) Decompose(task *Task) *DecomposeResult {
	return d.DecomposeWithContext(context.Background(), task)
}

// DecomposeWithContext is like Decompose but accepts a context for the LLM call.
func (d *TaskDecomposer) DecomposeWithContext(ctx context.Context, task *Task) *DecomposeResult {
	if task == nil {
		return &DecomposeResult{
			Decomposed: false,
			Subtasks:   nil,
			Reason:     "nil task",
			SkipReason: SkipReasonNilTask,
		}
	}

	// GH-4271: cheap word-count heuristic classification up front, purely so
	// early-exit gates below (disabled/label/phrase) can still report whether
	// the task looked epic/complex-tier. Never the optional LLM classifier —
	// that must not fire as a side effect of a branch that never reaches the
	// "real" classification a few lines down.
	precheckComplexity := DetectComplexity(task)

	// Check if decomposition is enabled
	if !d.config.Enabled {
		return &DecomposeResult{
			Decomposed: false,
			Subtasks:   []*Task{task},
			Reason:     "decomposition disabled",
			SkipReason: SkipReasonDisabled,
			Complexity: precheckComplexity,
		}
	}

	// GH-664: Skip decomposition entirely if task has no-decompose label
	if HasLabel(task, NoDecomposeLabel) {
		return &DecomposeResult{
			Decomposed: false,
			Subtasks:   []*Task{task},
			Reason:     "skipped: no-decompose label",
			SkipReason: SkipReasonNoDecomposeLabel,
			Complexity: precheckComplexity,
		}
	}

	// GH-3597: standalone prose gates exactly like the label
	if HasNoDecomposePhrase(task) {
		return &DecomposeResult{
			Decomposed: false,
			Subtasks:   []*Task{task},
			Reason:     "skipped: no-decompose phrase in title/description",
			SkipReason: SkipReasonNoDecomposePhrase,
			Complexity: precheckComplexity,
		}
	}

	// GH-727: Use LLM classifier if available, otherwise fall back to heuristic
	var complexity Complexity
	if d.classifier != nil {
		complexity = d.classifier.Classify(ctx, task)
	} else {
		complexity = precheckComplexity
	}

	if !d.shouldDecompose(complexity) {
		return &DecomposeResult{
			Decomposed: false,
			Subtasks:   []*Task{task},
			Reason:     "complexity below threshold: " + complexity.String(),
			SkipReason: SkipReasonBelowMinComplexity,
			Complexity: complexity,
		}
	}

	// Check description length — only enforce in heuristic mode (GH-1728).
	// When the LLM classifier is attached and confirmed COMPLEX, trust it over word count.
	wordCount := len(strings.Fields(task.Description))
	usedLLMClassifier := d.classifier != nil
	if !usedLLMClassifier && wordCount < d.config.MinDescriptionWords {
		return &DecomposeResult{
			Decomposed:       false,
			Subtasks:         []*Task{task},
			Reason:           "description too short for decomposition (heuristic mode)",
			SkipReason:       SkipReasonDescriptionTooShort,
			Complexity:       complexity,
			DescriptionWords: wordCount,
		}
	}

	// Analyze and split
	subtasks := d.analyzeAndSplit(task)
	if len(subtasks) <= 1 {
		return &DecomposeResult{
			Decomposed: false,
			Subtasks:   []*Task{task},
			Reason:     "no decomposition points found",
			SkipReason: SkipReasonNoSplitPoints,
			Complexity: complexity,
		}
	}

	return &DecomposeResult{
		Decomposed: true,
		Subtasks:   subtasks,
		Reason:     "decomposed into subtasks",
		Complexity: complexity,
	}
}

// DecomposeForRetry attempts decomposition after an execution failure (OOM/killed).
// Bypasses word count gate — execution failure already proved task is too large.
// Still respects no-decompose label and requires structural split points.
// GH-1716: Safety net for tasks that slip through initial classification.
func (d *TaskDecomposer) DecomposeForRetry(ctx context.Context, task *Task) *DecomposeResult {
	if task == nil {
		return &DecomposeResult{Decomposed: false, Reason: "nil task"}
	}

	if HasLabel(task, NoDecomposeLabel) {
		return &DecomposeResult{
			Decomposed: false,
			Subtasks:   []*Task{task},
			Reason:     "skipped: no-decompose label (even on retry)",
		}
	}

	// GH-3597: standalone prose gates exactly like the label, even on retry
	if HasNoDecomposePhrase(task) {
		return &DecomposeResult{
			Decomposed: false,
			Subtasks:   []*Task{task},
			Reason:     "skipped: no-decompose phrase (even on retry)",
		}
	}

	subtasks := d.analyzeAndSplit(task)
	if len(subtasks) <= 1 {
		return &DecomposeResult{
			Decomposed: false,
			Subtasks:   []*Task{task},
			Reason:     "no decomposition points found (retry fallback)",
		}
	}

	return &DecomposeResult{
		Decomposed: true,
		Subtasks:   subtasks,
		Reason:     "decomposed after execution failure (retry fallback)",
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

// ReportableSkip reports whether result represents a non-decomposition
// outcome worth surfacing as a skip log line + execution event (GH-4271).
//
// The canary defect this closes: a task classified epic (or at/above
// decompose.min_complexity) bypassed decomposition — e.g. gated by
// min_description_words — with zero trace, indistinguishable from the
// TASK-401 defect class. shouldDecompose(result.Complexity) is exactly
// "would this task's complexity tier alone have triggered decomposition",
// so reusing it here means every OTHER gate (disabled/label/phrase/
// description-too-short/no-split-points) is reported whenever it fires on a
// task that met the complexity bar. SkipReasonBelowMinComplexity is
// intentionally excluded: shouldDecompose(result.Complexity) is false by
// construction on that branch, since the whole point of that gate is that
// complexity was too low to warrant decomposition in the first place — not
// a silent-epic scenario.
func (d *TaskDecomposer) ReportableSkip(result *DecomposeResult) bool {
	if result == nil || result.Decomposed || result.SkipReason == SkipReasonNone {
		return false
	}
	return d.shouldDecompose(result.Complexity)
}

// SkipLogDetail renders a machine-readable, single-line message for
// result's skip reason, e.g. "decomposition skipped: description_words=275 <
// min_description_words=300" (GH-4271). Concrete threshold/observed values
// are included where the gate has them; other gates carry just the reason
// code since there's nothing numeric to report.
func (d *TaskDecomposer) SkipLogDetail(result *DecomposeResult) string {
	prefix := "decomposition skipped: reason=" + string(result.SkipReason) +
		" complexity=" + result.Complexity.String()
	switch result.SkipReason {
	case SkipReasonDescriptionTooShort:
		return prefix + fmt.Sprintf(" description_words=%d < min_description_words=%d",
			result.DescriptionWords, d.config.MinDescriptionWords)
	case SkipReasonBelowMinComplexity:
		return prefix + " min_complexity=" + d.config.MinComplexity
	default:
		return prefix
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

		// GH-3540: BaseBranch is inherited from the parent. When the parent's
		// BaseBranch is empty (e.g. project config omits default_branch), the
		// runner resolves it from the main-repo git context before creating
		// the worktree, so CreatePR always receives the repo default branch.
		subtask := &Task{
			ID:          generateSubtaskID(parent.ID, i+1),
			Title:       truncateTitle(part, 80),
			Description: buildSubtaskDescription(parent, part, i+1, len(parts)),
			ProjectPath: parent.ProjectPath,
			Branch:      parent.Branch,
			BaseBranch:  parent.BaseBranch,
			CreatePR:    false, // Only final subtask creates PR
			Verbose:     parent.Verbose,
			// GH-4032: subtasks never get their own dispatcher-assigned executions
			// row (they run inline inside the parent's single Execute() call), so
			// borrow the parent's resolved execution ID for ledger writes. Without
			// this, LogExecutionID() fell back to the subtask's own ID (e.g.
			// "GH-4021-2"), which has no matching executions row and trips the
			// execution_events FOREIGN KEY constraint on every stage write.
			ParentExecutionID: parent.LogExecutionID(),
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
// NOTE: Uses heuristic-only detection (DetectComplexity). The full DecomposeWithContext()
// method skips the word count gate when the LLM classifier confirms COMPLEX (GH-1728).
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
