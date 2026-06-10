package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// conventionalCommitPrefixRegex matches conventional-commit type prefixes like
// "fix:", "feat(scope):", "chore(epic):" at the start of a title.
var conventionalCommitPrefixRegex = regexp.MustCompile(`^(?i)(fix|feat|chore|docs|test|refactor|style|perf|ci|build|revert)(\([^)]+\))?:`)

// subtaskActionVerbs is the allow-list of first words that identify a subtask
// title as an action item (vs LLM analysis/prose). Kept intentionally broad to
// cover normal engineering verbs without admitting analysis sentences.
var subtaskActionVerbs = map[string]bool{
	"add": true, "adjust": true, "allow": true, "apply": true, "audit": true,
	"block": true, "build": true, "bump": true, "cache": true, "check": true,
	"clean": true, "cleanup": true, "clear": true, "consolidate": true,
	"convert": true, "create": true, "decouple": true, "dedupe": true,
	"delete": true, "deploy": true, "deprecate": true, "detect": true,
	"disable": true, "document": true, "drop": true, "emit": true, "enable": true,
	"enforce": true, "ensure": true, "expose": true, "extract": true,
	"fallback": true, "filter": true, "fix": true, "gate": true, "generate": true,
	"guard": true, "handle": true, "harden": true, "hide": true, "implement": true,
	"improve": true, "init": true, "inject": true, "install": true,
	"instrument": true, "introduce": true, "invalidate": true, "limit": true,
	"load": true, "log": true, "make": true, "merge": true, "migrate": true,
	"move": true, "normalize": true, "parse": true, "patch": true, "persist": true,
	"plumb": true, "port": true, "prefix": true, "prevent": true, "propagate": true,
	"protect": true, "provide": true, "publish": true, "refactor": true,
	"register": true, "reject": true, "remove": true, "rename": true,
	"replace": true, "reset": true, "restore": true, "retry": true, "return": true,
	"revert": true, "rewrite": true, "route": true, "sanitize": true, "scope": true,
	"seed": true, "send": true, "serialize": true, "set": true, "setup": true,
	"simplify": true, "skip": true, "split": true, "standardize": true,
	"stop": true, "store": true, "strip": true, "support": true, "surface": true,
	"switch": true, "sync": true, "teach": true, "test": true, "throttle": true,
	"trim": true, "truncate": true, "unify": true, "unwire": true, "update": true,
	"upgrade": true, "use": true, "validate": true, "verify": true, "wait": true,
	"warn": true, "wire": true, "wrap": true, "write": true,
}

// subtaskProseIndicators are phrases that strongly signal a subtask "title" is
// actually LLM analysis/prose rather than an action item. See GH-2324 / GH-2315.
var subtaskProseIndicators = []string{
	", not ", " but ", " however", "however,",
	"appears correct", "appears to ", "looks good", "looks correct",
	" is fine", " is correct", " is actually ", " already ",
	"already marks", "already handles", "already does",
	" seems to ", " seems like", " should already",
	"the status appears", "the current code",
}

// propagatableLabelAllowlist contains exact label names that survive parent→child
// propagation during epic decomposition.
var propagatableLabelAllowlist = map[string]struct{}{
	"no-decompose": {},
	"no-plan":      {},
}

// propagatableLabelPrefixes are prefix patterns whose matching labels survive propagation.
var propagatableLabelPrefixes = []string{"area:", "priority:", "scope:"}

// alwaysBlockedLabels are pilot lifecycle markers that must never propagate to sub-issues.
var alwaysBlockedLabels = map[string]struct{}{
	"pilot":                     {},
	"pilot-done":                {},
	"pilot-failed":              {},
	"pilot-in-progress":         {},
	"pilot-superseded":          {},
	"pilot-needs-clarification": {},
}

// filterPropagatableLabels returns the subset of parent labels that sub-issues
// should inherit during epic decomposition. It lowercases and trims each label,
// drops empties, drops lifecycle labels, and keeps anything in the allow-list or
// matching a propagatable prefix.
func filterPropagatableLabels(parentLabels []string) []string {
	out := make([]string, 0, len(parentLabels))
	for _, raw := range parentLabels {
		l := strings.ToLower(strings.TrimSpace(raw))
		if l == "" {
			continue
		}
		if _, blocked := alwaysBlockedLabels[l]; blocked {
			continue
		}
		if _, ok := propagatableLabelAllowlist[l]; ok {
			out = append(out, l)
			continue
		}
		for _, p := range propagatableLabelPrefixes {
			if strings.HasPrefix(l, p) {
				out = append(out, l)
				break
			}
		}
	}
	return out
}

// validateSubtaskTitle reports an error when a subtask title extracted from LLM
// planning output is structurally unsuitable for use as a GitHub issue title.
//
// Incident (GH-2324): the decomposition of GH-2314 produced GH-2315 with this
// as its title, directly from the LLM's skeptical analysis of the parent issue:
//
//	"Dispatcher `recoverStaleTasks()` (line 188) already marks orphans as
//	 `\"failed\"`, not `\"completed\"`. The status appears correct in the
//	 current code."
//
// The string flowed verbatim into the sub-issue title, PR #2317 title, the
// squash-merge commit subject, and the public v2.95.3 changelog. This validator
// rejects such titles before they reach the tracker.
//
// Rejection criteria:
//  1. Contains prose/analysis indicators (", not ", " but ", "appears correct",
//     "already", ...).
//  2. Exceeds 15 words — real action titles are terse.
//  3. First significant word is neither a conventional-commit type prefix nor
//     an allow-listed action verb.
func validateSubtaskTitle(title string) error {
	t := strings.TrimSpace(title)
	if t == "" {
		return fmt.Errorf("empty title")
	}

	lower := strings.ToLower(t)
	for _, ind := range subtaskProseIndicators {
		if strings.Contains(lower, ind) {
			return fmt.Errorf("title contains analysis/prose indicator %q", strings.TrimSpace(ind))
		}
	}

	words := strings.Fields(t)
	if len(words) > 15 {
		return fmt.Errorf("title has %d words (>15); action titles should be terse", len(words))
	}

	if conventionalCommitPrefixRegex.MatchString(t) {
		return nil
	}

	firstWord := strings.ToLower(strings.Trim(words[0], "*_`\"'.,:;()[]"))
	if firstWord == "" {
		return fmt.Errorf("title has no leading word")
	}
	if !subtaskActionVerbs[firstWord] {
		return fmt.Errorf("title does not start with an action verb or conventional-commit prefix (got %q)", firstWord)
	}
	return nil
}

// syntheticSubtaskTitle builds a fallback title for subtasks whose LLM-produced
// title failed validateSubtaskTitle. Uses the parent ID so the sub-issue is
// still traceable back to the epic. GH-2324.
func syntheticSubtaskTitle(parent *Task, order int) string {
	parentID := "epic"
	if parent != nil && parent.ID != "" {
		parentID = parent.ID
	}
	return fmt.Sprintf("%s: Subtask %d", parentID, order)
}

// HasNoPlanKeyword checks whether the task title or description contains the [no-plan]
// keyword, allowing users to bypass epic planning (GH-1687).
func HasNoPlanKeyword(task *Task) bool {
	return strings.Contains(strings.ToLower(task.Title), strings.ToLower(NoPlanKeyword)) ||
		strings.Contains(strings.ToLower(task.Description), strings.ToLower(NoPlanKeyword))
}

// EpicPlan represents the result of planning an epic task.
// Contains the parent task and the subtasks derived from Claude Code's planning output.
type EpicPlan struct {
	// ParentTask is the original epic task that was planned
	ParentTask *Task

	// Subtasks are the sequential subtasks derived from the planning phase
	Subtasks []PlannedSubtask

	// TotalEffort is the estimated total effort (if provided by the planner)
	TotalEffort string

	// PlanOutput is the raw Claude Code output for reference
	PlanOutput string
}

// PlannedSubtask represents a single subtask derived from epic planning.
type PlannedSubtask struct {
	// Title is the short title of the subtask
	Title string

	// Description is the detailed description of what needs to be done
	Description string

	// Order is the execution order (1-indexed)
	Order int

	// DependsOn contains the orders of subtasks this depends on
	DependsOn []int
}

// CreatedIssue represents an issue created from a planned subtask.
// Supports both GitHub (numeric Number) and other trackers (string Identifier).
type CreatedIssue struct {
	// Number is the GitHub issue number (0 for non-GitHub adapters)
	Number int

	// Identifier is the issue identifier string (GH-1471).
	// For GitHub: same as Number as string (e.g., "123")
	// For Linear: full identifier (e.g., "APP-123")
	// For Jira: issue key (e.g., "PROJ-456")
	// This field is always populated; Number is for backwards compatibility.
	Identifier string

	// URL is the full issue URL
	URL string

	// State is the issue state ("open" or "closed") populated by recoverExistingSubIssues.
	State string

	// Subtask is the planned subtask this issue was created from
	Subtask PlannedSubtask
}

// numberedListRegex matches numbered patterns: "1. ", "1) ", "Step 1:", "Phase 1:", "**1.", etc.
// Allows optional markdown bold markers (**) before the number (GH-490 fix).
// Also handles markdown heading prefixes (### 1.), dash/asterisk bullets (- 1., * 1.),
// and combinations like "- **1. Title**" or "### Step 1: Title" (GH-542 fix).
// Used by parseSubtasks as the regex fallback in the parsing pipeline:
//
//	PlanEpic → parseSubtasksWithFallback → SubtaskParser (Haiku API) → parseSubtasks (regex)
var numberedListRegex = regexp.MustCompile(`(?mi)^(?:\s*)(?:#{1,6}\s+)?(?:[-*]\s+)?(?:\*{0,2})(?:step|phase|task)?\s*(\d+)[.):]\s*(.+)`)

// PlanEpic runs Claude Code in planning mode to break an epic into subtasks.
// Returns an EpicPlan with 3-5 sequential subtasks.
// executionPath may differ from task.ProjectPath when using worktree isolation (GH-968).
func (r *Runner) PlanEpic(ctx context.Context, task *Task, executionPath string) (*EpicPlan, error) {
	// Build planning prompt
	prompt := buildPlanningPrompt(task)

	// Get claude command from config or use default
	claudeCmd := "claude"
	if r.config != nil && r.config.ClaudeCode != nil && r.config.ClaudeCode.Command != "" {
		claudeCmd = r.config.ClaudeCode.Command
	}

	// GH-2432: Planning gets Opus for stronger reasoning; execution stays on
	// Sonnet (set via the regular runner path). The model is also exported via
	// ANTHROPIC_MODEL because Pilot's global env may otherwise win on Node's
	// last-write lookup inside Claude Code (see backend_claudecode.go).
	planningModel := "claude-opus-4-7"
	if r.config != nil && r.config.Planning != nil && r.config.Planning.Model != "" {
		planningModel = r.config.Planning.Model
	}

	// Run Claude Code with --print flag for planning. Restrict tools to
	// read-only — planning must not write code.
	args := []string{
		"--print", "-p", prompt,
		"--model", planningModel,
		"--allowedTools", strings.Join(DefaultAllowedToolsPlanning(), ","),
	}

	cmd := exec.CommandContext(ctx, claudeCmd, args...)
	cmd.Env = append(os.Environ(), "ANTHROPIC_MODEL="+planningModel)

	// Set working directory - use executionPath which respects worktree isolation
	if executionPath != "" {
		cmd.Dir = executionPath
	}

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	r.log.Debug("Running Claude Code planning",
		"task_id", task.ID,
		"command", claudeCmd,
		"args", args,
	)

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude planning failed: %w (stderr: %s)", err, stderr.String())
	}

	output := stdout.String()
	if output == "" {
		return nil, fmt.Errorf("claude planning returned empty output")
	}

	// Parse subtasks: tries Haiku structured extraction first, falls back to regex.
	// See parseSubtasksWithFallback in subtask_parser.go for the fallback chain.
	subtasks := parseSubtasksWithFallback(r.subtaskParser, output)
	if len(subtasks) == 0 {
		return nil, fmt.Errorf("no subtasks found in planning output")
	}

	// Validate and fix subtask titles: enforce conventional-commits format,
	// reject placeholders, re-prompt via LLM or fall back to parent type/scope.
	subtasks = validateAndFixSubtaskTitles(ctx, subtasks, task, r.subtaskParser, r.log)

	return &EpicPlan{
		ParentTask: task,
		Subtasks:   subtasks,
		PlanOutput: output,
	}, nil
}

// buildPlanningPrompt creates the prompt for epic planning.
func buildPlanningPrompt(task *Task) string {
	var sb strings.Builder

	sb.WriteString("You are a software architect planning an implementation.\n\n")
	sb.WriteString("Break down this epic task into 3-5 sequential subtasks that can each be completed independently.\n")
	sb.WriteString("Each subtask should be a concrete, implementable unit of work.\n\n")

	sb.WriteString("## CRITICAL: Subtask Title Format\n\n")
	sb.WriteString("Every subtask title MUST follow the conventional-commits format:\n\n")
	sb.WriteString("  type(scope): description\n\n")
	sb.WriteString("Accepted types: feat, fix, chore, refactor, test, docs, perf, build, ci, style\n")
	sb.WriteString("Format templates (these are placeholders, NOT real subtasks — do NOT copy them verbatim):\n")
	sb.WriteString("  feat(SCOPE): IMPERATIVE_SUMMARY\n")
	sb.WriteString("  fix(SCOPE): WHAT_IS_BEING_FIXED\n")
	sb.WriteString("  chore(SCOPE): MAINTENANCE_ACTION\n\n")
	sb.WriteString("Replace SCOPE and the ALL_CAPS slots with terms drawn from the actual task being planned.\n")
	sb.WriteString("Do NOT emit titles like \"GH-123: Subtask 1\" or plain action phrases without a type prefix.\n\n")

	sb.WriteString("## CRITICAL: Avoid Single-Package Splits\n\n")
	sb.WriteString("If all the work lives in one package or directory (e.g., all files in `cmd/pilot/`),\n")
	sb.WriteString("DO NOT split into separate subtasks. Instead, return a SINGLE subtask with the full scope.\n")
	sb.WriteString("Splitting work within the same package causes merge conflicts when subtasks execute in parallel.\n")
	sb.WriteString("Only split when subtasks genuinely touch DIFFERENT packages or directories.\n\n")

	sb.WriteString("## Task to Plan\n\n")
	sb.WriteString(fmt.Sprintf("**Title:** %s\n\n", task.Title))
	if task.Description != "" {
		sb.WriteString(fmt.Sprintf("**Description:**\n%s\n\n", task.Description))
	}

	sb.WriteString("## Output Format\n\n")
	sb.WriteString("List each subtask with a number, title, and description:\n\n")
	sb.WriteString("1. **Subtask title** - Description of what needs to be done\n")
	sb.WriteString("2. **Next subtask** - Its description\n")
	sb.WriteString("...\n\n")

	sb.WriteString("Focus on:\n")
	sb.WriteString("- Clear boundaries between subtasks\n")
	sb.WriteString("- Logical ordering (dependencies flow naturally)\n")
	sb.WriteString("- Each subtask should be testable/verifiable\n")
	sb.WriteString("- Include any setup/infrastructure subtasks first\n")
	sb.WriteString("- NEVER split work that belongs to the same Go package or directory into separate subtasks\n")

	return sb.String()
}

// consolidateEpicPlan merges the original task description with the planned subtasks
// into a single description for non-decomposed execution. The executor gets the full
// implementation plan but executes it as one unit on one branch.
func consolidateEpicPlan(originalDesc string, subtasks []PlannedSubtask) string {
	var sb strings.Builder
	sb.WriteString(originalDesc)
	sb.WriteString("\n\n## Planned Steps (execute all in sequence)\n\n")
	for _, st := range subtasks {
		sb.WriteString(fmt.Sprintf("%d. **%s**", st.Order, st.Title))
		if st.Description != "" {
			sb.WriteString(" — ")
			sb.WriteString(st.Description)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// isSinglePackageScope checks whether all planned subtasks reference files within
// the same Go package or directory. When true, creating separate GitHub issues
// would cause merge conflicts because each sub-issue branches from main independently.
//
// Detection strategy:
// 1. Extract all file paths mentioned across subtask titles and descriptions
// 2. Compute unique parent directories
// 3. If only 1 directory (or 0 files found), consider it single-package scope
//
// GH-1265: This prevents the "serial conflict cascade" bug where N sub-issues
// all touching cmd/pilot/ create N branches from main, each redeclaring shared types.
func isSinglePackageScope(subtasks []PlannedSubtask, taskDescription string) bool {
	// Collect all text to scan for file references
	var allText strings.Builder
	allText.WriteString(taskDescription)
	allText.WriteString("\n")
	for _, st := range subtasks {
		allText.WriteString(st.Title)
		allText.WriteString("\n")
		allText.WriteString(st.Description)
		allText.WriteString("\n")
	}

	dirs := extractUniqueDirectories(allText.String())

	// If we found file references and they all point to 1 directory → single package
	if len(dirs) == 1 {
		return true
	}

	// If no file references found, use heuristic: check if subtask titles suggest
	// the same component (e.g., all mention "onboard", "dashboard", "config")
	if len(dirs) == 0 {
		return detectSameComponentFromTitles(subtasks)
	}

	return false
}

// extractUniqueDirectories finds file paths in text and returns their unique parent directories.
// Delegates to the shared ExtractDirectoriesFromText (scope.go) for reuse across packages.
func extractUniqueDirectories(text string) map[string]bool {
	return ExtractDirectoriesFromText(text)
}

// detectSameComponentFromTitles checks if subtask titles all reference the same component.
// Uses a simple heuristic: extract the most common significant word from titles.
// If one word appears in >80% of subtask titles, it's likely single-scope.
func detectSameComponentFromTitles(subtasks []PlannedSubtask) bool {
	if len(subtasks) < 2 {
		return false
	}

	// Count word frequency across titles
	wordCounts := make(map[string]int)
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true, "for": true,
		"to": true, "in": true, "of": true, "with": true, "from": true, "by": true,
		"add": true, "create": true, "implement": true, "update": true, "fix": true,
		"setup": true, "set": true, "up": true, "new": true, "test": true, "tests": true,
		// conventional-commit type prefixes are not component names
		"feat": true, "chore": true, "refactor": true, "docs": true, "perf": true,
		"build": true, "ci": true, "style": true, "revert": true,
	}

	for _, st := range subtasks {
		words := strings.Fields(strings.ToLower(st.Title))
		seen := make(map[string]bool) // dedupe within a single title
		for _, w := range words {
			w = strings.Trim(w, ".,:-()[]\"'`*")
			if len(w) < 3 || stopWords[w] {
				continue
			}
			if !seen[w] {
				wordCounts[w]++
				seen[w] = true
			}
		}
	}

	// Check if any significant word appears in >80% of titles
	threshold := int(float64(len(subtasks)) * 0.8)
	for _, count := range wordCounts {
		if count >= threshold {
			return true
		}
	}

	return false
}

// parseSubtasks extracts subtasks from Claude's planning output using regex.
// This is the fallback parser when Haiku API is unavailable (see subtask_parser.go).
// Looks for numbered patterns: "1. Title - Description", "Step 1: Title", "**1. Title**"
func parseSubtasks(output string) []PlannedSubtask {
	var subtasks []PlannedSubtask
	seenOrders := make(map[int]bool)

	scanner := bufio.NewScanner(strings.NewReader(output))
	var currentSubtask *PlannedSubtask
	var descriptionLines []string

	for scanner.Scan() {
		line := scanner.Text()

		// Try to match numbered list patterns
		matches := numberedListRegex.FindStringSubmatch(line)
		if len(matches) >= 3 {
			// Save previous subtask if exists
			if currentSubtask != nil {
				finalizeSubtask(currentSubtask, descriptionLines)
				if currentSubtask.Title != "" && !seenOrders[currentSubtask.Order] {
					subtasks = append(subtasks, *currentSubtask)
					seenOrders[currentSubtask.Order] = true
				}
			}

			order := 0
			_, _ = fmt.Sscanf(matches[1], "%d", &order)

			// Extract title and possibly inline description
			titleAndDesc := strings.TrimSpace(matches[2])
			title, desc := splitTitleDescription(titleAndDesc)

			currentSubtask = &PlannedSubtask{
				Title:       title,
				Description: desc,
				Order:       order,
			}
			descriptionLines = nil
			continue
		}

		// Accumulate description lines for current subtask
		if currentSubtask != nil && strings.TrimSpace(line) != "" {
			// Skip markdown headers that might be formatting
			if !strings.HasPrefix(strings.TrimSpace(line), "#") {
				descriptionLines = append(descriptionLines, strings.TrimSpace(line))
			}
		}
	}

	// Save last subtask
	if currentSubtask != nil {
		finalizeSubtask(currentSubtask, descriptionLines)
		if currentSubtask.Title != "" && !seenOrders[currentSubtask.Order] {
			subtasks = append(subtasks, *currentSubtask)
		}
	}

	return subtasks
}

// splitTitleDescription splits "**Title** - Description" or "Title: Description" patterns.
func splitTitleDescription(s string) (title, description string) {
	// Remove markdown bold markers
	s = strings.ReplaceAll(s, "**", "")

	// Try common separators (em-dash first since Claude often uses it)
	separators := []string{" — ", " - ", ": ", " – "}
	for _, sep := range separators {
		if idx := strings.Index(s, sep); idx > 0 {
			return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+len(sep):])
		}
	}

	// No separator found, entire string is title
	return strings.TrimSpace(s), ""
}

// finalizeSubtask combines inline description with accumulated description lines.
func finalizeSubtask(subtask *PlannedSubtask, lines []string) {
	if len(lines) == 0 {
		return
	}

	accumulated := strings.TrimSpace(strings.Join(lines, "\n"))
	if subtask.Description == "" {
		subtask.Description = accumulated
	} else {
		// Prepend inline description to accumulated lines
		subtask.Description = subtask.Description + "\n" + accumulated
	}
}

// conventionalSubtaskTitleRE mirrors the conventional-commit regex from the github
// package but is defined here to avoid an import cycle (adapters/github → executor).
var conventionalSubtaskTitleRE = regexp.MustCompile(
	`^(feat|fix|chore|refactor|test|docs|perf|build|ci|style)(\([^)]+\))?: .+$`,
)

// placeholderSubtaskTitleRE matches synthetic fallback titles like "GH-123: Subtask 1"
// produced by syntheticSubtaskTitle. Their presence in a batch signals a re-prompt is needed.
var placeholderSubtaskTitleRE = regexp.MustCompile(`^[A-Z][A-Z0-9]*-\d+:\s+Subtask\s+\d+$`)

// parentTypeScopeRE extracts the conventional-commit prefix from a parent task title.
// Used by Approach B fallback: "feat(auth):" → "feat(auth): " prepended to the subtask description.
var parentTypeScopeRE = regexp.MustCompile(
	`^(feat|fix|chore|refactor|test|docs|perf|build|ci|style)(\([^)]+\))?:`,
)

// ErrSubIssuesAlreadyExist is returned by CreateSubIssues when open sub-issues
// referencing the parent already exist, preventing duplicate batch creation.
var ErrSubIssuesAlreadyExist = errors.New("open sub-issues already exist for this parent")

// ErrParentDone is returned by CreateSubIssues when the parent task is already
// closed or skipped, so spawning sub-issues would be wasteful (GH-2867).
var ErrParentDone = errors.New("parent task is already done; refusing to create sub-issues")

// isParentDone reports whether a task should be treated as done based on its
// labels (pilot-done, pilot-skip) or its state (closed, merged).
//
// Defensive fallback: whenever `State` is empty and the task ID looks like a
// GitHub issue ("GH-N"), this function shells out to `gh issue view` to fetch
// the authoritative state. This catches every code path that produces a Task
// without `State`:
//
//   - The dispatcher worker (`dispatcher.go:639`) reconstructs Task from a
//     persisted execution row; the `executions` schema has no `task_state`
//     column.
//   - The `task_labels` column may be populated but stale (frozen at queue
//     time) — a parent queued with `["pilot"]` and later closed with
//     `pilot-done` produces a Task whose labels still say `["pilot"]`.
//
// Both cases bypassed the gate during the 2026-05-08 GH-201 incident
// (70+ spurious OAuth sub-issues). Non-fatal on lookup error: returns false
// so this never blocks legitimate dispatches.
//
// Tests can override this var to assert the fallback path or to keep the
// production default no-op when constructing tasks with deterministic GH-* IDs.
var isParentDoneLiveFallback = func(taskID, dir string) bool {
	// Never shell out during `go test` — tests override this var explicitly
	// when they want to exercise the fallback path.
	if testing.Testing() {
		return false
	}
	return queryParentDoneViaGitHub(taskID, dir)
}

func isParentDone(t *Task) bool {
	if t == nil {
		return false
	}
	for _, label := range t.Labels {
		if label == "pilot-done" || label == "pilot-skip" {
			return true
		}
	}
	if t.State == "closed" || t.State == "merged" {
		return true
	}
	// Live fallback when State is missing — Labels alone are not authoritative
	// because dispatcher-restored Tasks carry stale labels from queue time.
	// We only reach here when no terminal label was found above; the remaining
	// label values are non-terminal and therefore inconclusive.
	if t.State == "" && strings.HasPrefix(t.ID, "GH-") {
		if isParentDoneLiveFallback(t.ID, t.ProjectPath) {
			return true
		}
	}
	return false
}

// queryParentDoneViaGitHub returns true when the GitHub issue identified by
// taskID ("GH-N") is closed/merged or carries pilot-done / pilot-skip labels.
// Non-fatal: any subprocess or parse error returns false so the caller falls
// back to the default decision.
func queryParentDoneViaGitHub(taskID, dir string) bool {
	issueNum := strings.TrimPrefix(taskID, "GH-")
	if issueNum == "" || issueNum == taskID {
		return false
	}
	args := []string{"issue", "view", issueNum, "--json", "state,labels"}
	cmd := exec.Command("gh", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return false
	}
	var resp struct {
		State  string `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return false
	}
	state := strings.ToLower(strings.TrimSpace(resp.State))
	if state == "closed" || state == "merged" {
		return true
	}
	for _, l := range resp.Labels {
		switch strings.ToLower(strings.TrimSpace(l.Name)) {
		case "pilot-done", "pilot-skip":
			return true
		}
	}
	return false
}

// isConventionalSubtaskTitle reports whether title is in conventional-commits format.
func isConventionalSubtaskTitle(title string) bool {
	return conventionalSubtaskTitleRE.MatchString(strings.TrimSpace(title))
}

// isPlaceholderSubtaskTitle reports whether title is a synthetic placeholder like "GH-123: Subtask 1".
func isPlaceholderSubtaskTitle(title string) bool {
	return placeholderSubtaskTitleRE.MatchString(strings.TrimSpace(title))
}

// scopeKeywords returns body keywords that confirm a conventional-commit scope is legit.
// Only covers scopes that have caused cascade contamination; all others return nil (always trusted).
func scopeKeywords(scope string) []string {
	switch scope {
	case "auth":
		return []string{"auth", "oauth", "login", "token", "session", "identity", "credential", "password", "jwt"}
	default:
		return nil
	}
}

// extractScope pulls the scope name out of a conventional-commit prefix like "feat(auth):".
// Returns "" when the prefix has no scope (e.g. "fix:").
func extractScope(prefix string) string {
	start := strings.Index(prefix, "(")
	end := strings.Index(prefix, ")")
	if start < 0 || end < 0 || end <= start+1 {
		return ""
	}
	return prefix[start+1 : end]
}

// isCascadeArtefact returns true when the parent title carries a scoped prefix but
// the parent body contains no keywords matching that scope — a signal that the title
// was inherited from an unrelated earlier ticket (cascade-artefact pattern, GH-2587).
func isCascadeArtefact(prefix, body string) bool {
	scope := extractScope(prefix)
	if scope == "" {
		return false // no scope to verify
	}
	keywords := scopeKeywords(scope)
	if keywords == nil {
		return false // scope not in watchlist — trust it
	}
	bodyLower := strings.ToLower(body)
	for _, kw := range keywords {
		if strings.Contains(bodyLower, kw) {
			return false // body confirms scope is legit
		}
	}
	return true // title claims scope X but body never mentions X
}

// extractParentTypeScope returns the conventional-commit prefix (e.g. "feat(auth):") from
// parentTitle. Falls back to "chore:" when the parent title is not in conventional-commit format
// or when the scoped prefix looks like a cascade artefact (GH-2587: title scope not reflected in body).
func extractParentTypeScope(parentTitle, parentBody string) string {
	m := parentTypeScopeRE.FindString(strings.TrimSpace(parentTitle))
	if m == "" {
		return "chore:"
	}
	if isCascadeArtefact(m, parentBody) {
		return "chore:"
	}
	return m
}

// applyParentTypeScopeFallback rewrites the subtasks at invalidIdx using the parent's
// conventional-commit prefix (Approach B). The result is guaranteed to satisfy
// conventionalSubtaskTitleRE.
func applyParentTypeScopeFallback(subtasks []PlannedSubtask, invalidIdx []int, parentTitle, parentBody string) []PlannedSubtask {
	prefix := extractParentTypeScope(parentTitle, parentBody) // e.g. "feat(auth):"
	result := make([]PlannedSubtask, len(subtasks))
	copy(result, subtasks)
	for _, idx := range invalidIdx {
		if idx < 0 || idx >= len(result) {
			continue
		}
		st := &result[idx]
		raw := strings.TrimSpace(st.Title)
		// Strip any existing issue-id prefix like "GH-N: "
		raw = issuePrefixRegex.ReplaceAllString(raw, "")
		// Replace bare "Subtask N" placeholders with a meaningful verb phrase
		if raw == "" || strings.HasPrefix(strings.ToLower(raw), "subtask") {
			raw = fmt.Sprintf("implement subtask %d", st.Order)
		}
		st.Title = prefix + " " + lowercaseFirstRune(raw)
	}
	return result
}

// findInvalidSubtaskTitleIdx returns the indices of subtasks whose titles are either
// not in conventional-commits format or are synthetic placeholder strings.
func findInvalidSubtaskTitleIdx(subtasks []PlannedSubtask) []int {
	var invalid []int
	for i, st := range subtasks {
		if isPlaceholderSubtaskTitle(st.Title) || !isConventionalSubtaskTitle(st.Title) {
			invalid = append(invalid, i)
		}
	}
	return invalid
}

// validateAndFixSubtaskTitles ensures every subtask title follows conventional-commits
// format (type(scope): description). Invalid or placeholder titles trigger a re-prompt
// via SubtaskParser; if the re-prompt fails or no parser is available, Approach B
// inherits the parent's type/scope prefix.
func validateAndFixSubtaskTitles(ctx context.Context, subtasks []PlannedSubtask, parent *Task, parser *SubtaskParser, log *slog.Logger) []PlannedSubtask {
	invalid := findInvalidSubtaskTitleIdx(subtasks)
	if len(invalid) == 0 {
		return subtasks
	}

	parentTitle := ""
	parentBody := ""
	if parent != nil {
		parentTitle = parent.Title
		parentBody = parent.Description
	}

	// Attempt re-prompt via SubtaskParser API.
	if parser != nil {
		invalidSubtasks := make([]PlannedSubtask, 0, len(invalid))
		for _, idx := range invalid {
			invalidSubtasks = append(invalidSubtasks, subtasks[idx])
		}

		reformatted, err := parser.ReformatTitles(ctx, parentTitle, invalidSubtasks)
		if err == nil && len(reformatted) > 0 {
			// Merge reformatted titles back by order.
			byOrder := make(map[int]string, len(reformatted))
			for _, st := range reformatted {
				byOrder[st.Order] = st.Title
			}
			updated := make([]PlannedSubtask, len(subtasks))
			copy(updated, subtasks)
			for _, idx := range invalid {
				if t, ok := byOrder[updated[idx].Order]; ok && t != "" {
					updated[idx].Title = t
				}
			}
			// Re-check: if all valid after re-prompt, done.
			if len(findInvalidSubtaskTitleIdx(updated)) == 0 {
				return updated
			}
			// Partial fix — use the updated slice as base for Approach B.
			subtasks = updated
			invalid = findInvalidSubtaskTitleIdx(subtasks)
		} else if log != nil {
			log.Warn("subtask title re-prompt failed; applying Approach B fallback",
				"invalid_count", len(invalid),
				"error", err,
			)
		}
	}

	// Approach B: inherit parent's type/scope for remaining invalid titles.
	return applyParentTypeScopeFallback(subtasks, invalid, parentTitle, parentBody)
}

// queryRecentSubIssues returns true when there are open or recently-closed GitHub
// issues (created within the last 24 hours) that include "Parent: <parentID>" in
// their body. Used by CreateSubIssues as a dedup guard (GH-2867).
// Non-fatal: if the gh CLI call fails the check returns (false, nil) to allow creation.
func queryRecentSubIssues(ctx context.Context, dir, parentID string) (bool, error) {
	since := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02T15:04:05Z")
	args := []string{
		"issue", "list",
		"--state", "all",
		"--search", fmt.Sprintf("\"Parent: %s\" in:body created:>=%s", parentID, since),
		"--json", "number",
		"--limit", "3",
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return false, nil // non-fatal
	}
	var issues []struct{ Number int }
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		return false, nil
	}
	return len(issues) > 0, nil
}

// recoverExistingSubIssues lists all issues (any state) whose body contains
// "Parent: <parentID>" and reconstructs them as []CreatedIssue so the epic
// orchestrator can decide whether to no-op or continue executing open children.
// Non-fatal: returns an empty slice on gh CLI failure.
func recoverExistingSubIssues(ctx context.Context, dir, parentID string) ([]CreatedIssue, error) {
	args := []string{
		"issue", "list",
		"--state", "all",
		"--search", fmt.Sprintf("\"Parent: %s\" in:body", parentID),
		"--json", "number,url,state",
		"--limit", "50",
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, nil // non-fatal
	}
	var raw []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return nil, nil
	}
	out := make([]CreatedIssue, 0, len(raw))
	for _, r := range raw {
		out = append(out, CreatedIssue{
			Number:     r.Number,
			Identifier: strconv.Itoa(r.Number),
			URL:        r.URL,
			State:      r.State,
		})
	}
	return out, nil
}

// allChildrenDone reports whether every issue in the slice is in a non-open state.
func allChildrenDone(issues []CreatedIssue) bool {
	// GH-3053: empty slice must not vacuously satisfy "all done". A
	// recoverExistingSubIssues call that returns zero issues (gh search
	// hiccup, no sub-issues created yet, network blip) would otherwise
	// trip the false epic-complete path in runner.go: empty → true →
	// "All sub-issues already completed (100%)" → exit without work.
	if len(issues) == 0 {
		return false
	}
	for _, iss := range issues {
		if strings.ToLower(iss.State) == "open" {
			return false
		}
	}
	return true
}

// issueNumberRegex extracts the issue number from a GitHub issue URL.
// Matches patterns like: https://github.com/owner/repo/issues/123
var issueNumberRegex = regexp.MustCompile(`/issues/(\d+)`)

// parseIssueNumber extracts the issue number from a GitHub issue URL.
// Returns 0 if no issue number is found.
func parseIssueNumber(url string) int {
	matches := issueNumberRegex.FindStringSubmatch(url)
	if len(matches) < 2 {
		return 0
	}
	var num int
	_, _ = fmt.Sscanf(matches[1], "%d", &num)
	return num
}

// parsePRNumberFromURL extracts a PR number from a GitHub PR URL.
// Returns 0 if the URL doesn't contain a valid PR number.
func parsePRNumberFromURL(url string) int {
	// Match /pull/123 at the end of the URL
	idx := strings.LastIndex(url, "/pull/")
	if idx < 0 {
		return 0
	}
	numStr := strings.TrimSpace(url[idx+len("/pull/"):])
	// Strip any trailing path segments
	if slashIdx := strings.Index(numStr, "/"); slashIdx >= 0 {
		numStr = numStr[:slashIdx]
	}
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0
	}
	return n
}

// CreateSubIssues creates issues from the planned subtasks.
// For GitHub-sourced tasks (or when no SubIssueCreator is set), uses gh CLI.
// For non-GitHub adapters with a SubIssueCreator, dispatches via that interface (GH-1471).
// Returns a slice of CreatedIssue with issue identifiers and URLs.
// executionPath may differ from task.ProjectPath when using worktree isolation (GH-968).
func (r *Runner) CreateSubIssues(ctx context.Context, plan *EpicPlan, executionPath string) ([]CreatedIssue, error) {
	if plan == nil || len(plan.Subtasks) == 0 {
		return nil, fmt.Errorf("plan has no subtasks to create issues from")
	}

	if plan.ParentTask != nil && isParentDone(plan.ParentTask) {
		// GH-2867: refuse to spawn sub-issues for a parent that is already done.
		r.log.Info("Skipping sub-issue creation: parent is already done",
			"parent_id", plan.ParentTask.ID,
			"state", plan.ParentTask.State,
			"labels", plan.ParentTask.Labels,
		)
		return nil, ErrParentDone
	}

	// GH-1471: pick the creation backend up front. The adapter path is
	// non-GitHub (Linear, Jira, …) and uses its own per-adapter auth, so
	// the repo allowlist guardrail below is skipped for that branch.
	useAdapterCreator := r.subIssueCreator != nil &&
		plan.ParentTask != nil &&
		plan.ParentTask.SourceAdapter != "" &&
		plan.ParentTask.SourceAdapter != "github"

	// TASK-286 / GH-3027: guardrail must run BEFORE queryRecentSubIssues
	// because that helper also shells out to `gh` against the worktree's
	// inferred origin remote. Without this ordering, a misconfigured Pilot
	// would still leak `gh issue list` calls to an unmanaged repo even if
	// no sub-issue was created. Only enforced when a RepoAllowlist has been
	// wired onto the Runner; production callers do this in cmd/pilot via
	// SetRepoAllowlist(newConfigRepoAllowlist(cfg)).
	if !useAdapterCreator && r.repoAllowlist != nil && executionPath != "" {
		owner, repo, remoteErr := resolveGitRemote(ctx, executionPath)
		if remoteErr != nil {
			r.log.Error("sub-issue guardrail: could not resolve origin remote",
				"execution_path", executionPath, "error", remoteErr)
			if err := ValidateTargetRepo(r.repoAllowlist, "", "", executionPath); err != nil {
				return nil, fmt.Errorf("sub-issue guardrail (no origin remote at %s): %w", executionPath, err)
			}
		} else if err := ValidateTargetRepo(r.repoAllowlist, owner, repo, executionPath); err != nil {
			r.log.Error("sub-issue guardrail rejected target repo",
				"owner", owner, "repo", repo,
				"execution_path", executionPath, "error", err)
			return nil, fmt.Errorf("sub-issue guardrail: %w", err)
		} else {
			r.log.Debug("sub-issue guardrail passed",
				"owner", owner, "repo", repo, "execution_path", executionPath)
		}
	} else if !useAdapterCreator && r.repoAllowlist == nil {
		r.log.Warn("sub-issue guardrail skipped: no RepoAllowlist configured on Runner; production callers must invoke Runner.SetRepoAllowlist",
			"execution_path", executionPath)
	}

	if plan.ParentTask != nil {
		// Dedup guard: skip creation if recent sub-issues referencing this parent already exist.
		// Uses an injectable checker so tests can control the result without spawning gh CLI.
		checker := r.openSubIssueCheck
		if checker == nil {
			checker = queryRecentSubIssues
		}
		if exists, _ := checker(ctx, executionPath, plan.ParentTask.ID); exists {
			r.log.Info("Skipping sub-issue creation: open children already exist",
				"parent_id", plan.ParentTask.ID,
			)
			return nil, ErrSubIssuesAlreadyExist
		}
	}

	if useAdapterCreator {
		return r.createSubIssuesViaAdapter(ctx, plan)
	}

	return r.createSubIssuesViaGitHub(ctx, plan, executionPath)
}

// createSubIssuesViaAdapter creates sub-issues using the SubIssueCreator interface.
// Used for non-GitHub adapters like Linear, Jira, GitLab, Azure DevOps.
func (r *Runner) createSubIssuesViaAdapter(ctx context.Context, plan *EpicPlan) ([]CreatedIssue, error) {
	var created []CreatedIssue
	parentID := plan.ParentTask.SourceIssueID

	// Map subtask order → created issue identifier for dependency annotation (GH-1794)
	orderToIdentifier := make(map[int]string)

	r.log.Info("Creating sub-issues via adapter",
		"adapter", plan.ParentTask.SourceAdapter,
		"parent_id", parentID,
		"subtask_count", len(plan.Subtasks),
	)

	for _, subtask := range plan.Subtasks {
		// Build the issue body
		body := subtask.Description
		if plan.ParentTask.ID != "" {
			body = subIssueBody(plan.ParentTask.ID, body)
		}

		// Wire DependsOn annotations into the body (GH-1794)
		for _, depOrder := range subtask.DependsOn {
			if depID, ok := orderToIdentifier[depOrder]; ok {
				body += fmt.Sprintf("\n\nDepends on: %s", depID)
			}
		}

		// Truncate title (adapter may have different limits, but 80 is reasonable)
		title := truncateTitle(subtask.Title, 80)

		// GH-2324: Reject LLM analysis-style titles before they reach the tracker.
		// Falls back to a parent-scope conventional title (not a placeholder).
		if err := validateSubtaskTitle(title); err != nil {
			fallback := applyParentTypeScopeFallback(
				[]PlannedSubtask{{Title: title, Order: subtask.Order}},
				[]int{0},
				plan.ParentTask.Title,
				plan.ParentTask.Description,
			)[0].Title
			r.log.Warn("Rejected invalid LLM subtask title; using conventional fallback",
				"original_title", subtask.Title,
				"fallback_title", fallback,
				"reason", err.Error(),
				"subtask_order", subtask.Order,
				"parent_id", parentID,
			)
			r.emitAlertEvent(AlertEvent{
				Type:      AlertEventTypeConfigError,
				TaskID:    parentID,
				TaskTitle: plan.ParentTask.Title,
				Project:   plan.ParentTask.ProjectPath,
				Error:     fmt.Sprintf("invalid subtask title rejected: %v", err),
				Metadata: map[string]string{
					"event":          "invalid_subtask_title",
					"original_title": subtask.Title,
					"fallback_title": fallback,
					"subtask_order":  strconv.Itoa(subtask.Order),
				},
				Timestamp: time.Now(),
			})
			title = fallback
		}

		// Final CC-format guard for adapter path.
		if !isConventionalSubtaskTitle(title) {
			title = applyParentTypeScopeFallback(
				[]PlannedSubtask{{Title: title, Order: subtask.Order}},
				[]int{0},
				plan.ParentTask.Title,
				plan.ParentTask.Description,
			)[0].Title
		}

		r.log.Debug("Creating sub-issue via adapter",
			"subtask_order", subtask.Order,
			"title", title,
			"parent_id", parentID,
		)

		subLabels := append([]string{"pilot"}, filterPropagatableLabels(plan.ParentTask.Labels)...)
		identifier, url, err := r.subIssueCreator.CreateIssue(ctx, parentID, title, body, subLabels)
		if err != nil {
			return created, fmt.Errorf("failed to create sub-issue for subtask %d via %s adapter: %w",
				subtask.Order, plan.ParentTask.SourceAdapter, err)
		}

		created = append(created, CreatedIssue{
			Number:     0, // Non-GitHub adapters don't use numeric IDs
			Identifier: identifier,
			URL:        url,
			Subtask:    subtask,
		})

		// Track order → identifier for dependency resolution (GH-1794)
		orderToIdentifier[subtask.Order] = identifier

		r.log.Info("Created sub-issue via adapter",
			"subtask_order", subtask.Order,
			"identifier", identifier,
			"url", url,
		)
	}

	return created, nil
}

// subIssueBody assembles a decomposer-generated sub-issue body: the
// autopilot-meta marker (GH-2695, lets spec_validator's inherited-spec bailout
// recognise the issue), the parent reference, the subtask's planned slice, and
// a scope fence.
//
// The fence exists because of the GH-3513 incident: executors that see
// "Parent: GH-N" fetch the parent issue and treat its full spec as their task,
// so every sibling re-implements the entire feature and the redundant PRs
// collide. The fence pins the executor to this subtask's slice.
func subIssueBody(parentID, description string) string {
	return fmt.Sprintf(`<!--autopilot-meta
parent: %[1]s
inherited-spec: true
-->

Parent: %[1]s

%[2]s

## Scope fence

Implement ONLY the slice described above. The parent issue %[1]s is decomposed
into sibling sub-issues that cover the rest of its spec — consult the parent
for context, but do NOT implement parts of it that fall outside this subtask.`,
		parentID, description)
}

// createSubIssuesViaGitHub creates sub-issues using the gh CLI.
// This is the original implementation and fallback path.
//
// The TASK-286 / GH-3027 repo guardrail runs one level up in CreateSubIssues
// (it must fire before queryRecentSubIssues, which also shells out to gh).
func (r *Runner) createSubIssuesViaGitHub(ctx context.Context, plan *EpicPlan, executionPath string) ([]CreatedIssue, error) {
	var created []CreatedIssue

	// Map subtask order → created GitHub issue number for dependency annotation (GH-1794)
	orderToIssueNumber := make(map[int]int)

	for _, subtask := range plan.Subtasks {
		// Build the issue body
		body := subtask.Description
		if plan.ParentTask != nil && plan.ParentTask.ID != "" {
			body = subIssueBody(plan.ParentTask.ID, body)
		}

		// Wire DependsOn annotations into the body (GH-1794)
		for _, depOrder := range subtask.DependsOn {
			if depNum, ok := orderToIssueNumber[depOrder]; ok {
				body += fmt.Sprintf("\n\nDepends on: #%d", depNum)
			}
		}

		// Truncate title to max 80 chars for GitHub issue limits (GH-1133)
		title := truncateTitle(subtask.Title, 80)

		// GH-2324: Reject LLM analysis-style titles before they reach GitHub.
		// Falls back to a parent-scope conventional title (not a placeholder).
		if err := validateSubtaskTitle(title); err != nil {
			parentID := ""
			parentProject := ""
			parentTitle := ""
			parentBody := ""
			if plan.ParentTask != nil {
				parentID = plan.ParentTask.ID
				parentProject = plan.ParentTask.ProjectPath
				parentTitle = plan.ParentTask.Title
				parentBody = plan.ParentTask.Description
			}
			fallback := applyParentTypeScopeFallback(
				[]PlannedSubtask{{Title: title, Order: subtask.Order}},
				[]int{0},
				parentTitle,
				parentBody,
			)[0].Title
			r.log.Warn("Rejected invalid LLM subtask title; using conventional fallback",
				"original_title", subtask.Title,
				"fallback_title", fallback,
				"reason", err.Error(),
				"subtask_order", subtask.Order,
				"parent_id", parentID,
			)
			r.emitAlertEvent(AlertEvent{
				Type:      AlertEventTypeConfigError,
				TaskID:    parentID,
				TaskTitle: parentTitle,
				Project:   parentProject,
				Error:     fmt.Sprintf("invalid subtask title rejected: %v", err),
				Metadata: map[string]string{
					"event":          "invalid_subtask_title",
					"original_title": subtask.Title,
					"fallback_title": fallback,
					"subtask_order":  strconv.Itoa(subtask.Order),
				},
				Timestamp: time.Now(),
			})
			title = fallback
		}

		// Final CC-format guard: ensures the title passed to gh is always conventional-commit.
		// Catches titles that satisfy validateSubtaskTitle (action verb) but lack type prefix.
		if !isConventionalSubtaskTitle(title) {
			parentTitle := ""
			parentBody := ""
			if plan.ParentTask != nil {
				parentTitle = plan.ParentTask.Title
				parentBody = plan.ParentTask.Description
			}
			title = applyParentTypeScopeFallback(
				[]PlannedSubtask{{Title: title, Order: subtask.Order}},
				[]int{0},
				parentTitle,
				parentBody,
			)[0].Title
		}

		// Create issue using gh CLI
		subLabels := append([]string{"pilot"}, filterPropagatableLabels(plan.ParentTask.Labels)...)
		args := []string{"issue", "create", "--title", title, "--body", body}
		for _, l := range subLabels {
			args = append(args, "--label", l)
		}

		cmd := exec.CommandContext(ctx, "gh", args...)

		// Set working directory - use executionPath which respects worktree isolation
		if executionPath != "" {
			cmd.Dir = executionPath
		}

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		r.log.Debug("Creating GitHub issue",
			"subtask_order", subtask.Order,
			"title", subtask.Title,
		)

		if err := cmd.Run(); err != nil {
			return created, fmt.Errorf("failed to create issue for subtask %d: %w (stderr: %s)",
				subtask.Order, err, stderr.String())
		}

		// gh issue create outputs the issue URL on success
		issueURL := strings.TrimSpace(stdout.String())
		issueNumber := parseIssueNumber(issueURL)

		created = append(created, CreatedIssue{
			Number:     issueNumber,
			Identifier: strconv.Itoa(issueNumber), // For consistency, populate Identifier too
			URL:        issueURL,
			Subtask:    subtask,
		})

		// GH-3240: pre-mark in the poller so the sub-issue is not re-dispatched
		// on the next poll cycle (the epic will execute it directly via ExecuteSubIssues).
		if r.subIssuePollerSkip != nil && issueNumber > 0 {
			r.subIssuePollerSkip(issueNumber)
		}

		// Track order → issue number for dependency resolution (GH-1794)
		orderToIssueNumber[subtask.Order] = issueNumber

		// GH-2211: Wire native GitHub sub-issue link (non-fatal — text marker is fallback)
		if r.subIssueLinker != nil &&
			plan.ParentTask != nil &&
			plan.ParentTask.SourceRepo != "" &&
			plan.ParentTask.SourceIssueID != "" {
			if parts := strings.SplitN(plan.ParentTask.SourceRepo, "/", 2); len(parts) == 2 {
				if parentNum, parseErr := strconv.Atoi(plan.ParentTask.SourceIssueID); parseErr == nil {
					if linkErr := r.subIssueLinker.LinkSubIssue(ctx, parts[0], parts[1], parentNum, issueNumber); linkErr != nil {
						r.log.Warn("Failed to link native sub-issue",
							"parent", parentNum,
							"child", issueNumber,
							"error", linkErr,
						)
					}
				}
			}
		}

		r.log.Info("Created GitHub issue",
			"subtask_order", subtask.Order,
			"issue_number", issueNumber,
			"url", issueURL,
		)
	}

	return created, nil
}

// UpdateIssueProgress adds a progress comment to an issue.
func (r *Runner) UpdateIssueProgress(ctx context.Context, projectPath string, issueID string, message string) error {
	if r.dryRun {
		r.log.Info("dry-run: skipping UpdateIssueProgress", "issue", issueID)
		return nil
	}

	args := []string{"issue", "comment", issueID, "--body", message}
	cmd := exec.CommandContext(ctx, "gh", args...)
	if projectPath != "" {
		cmd.Dir = projectPath
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to comment on issue %s: %w (stderr: %s)", issueID, err, stderr.String())
	}
	return nil
}

// CloseIssueWithComment closes an issue with a completion comment.
// Includes an idempotency check: if the issue is already CLOSED, the close is skipped.
func (r *Runner) CloseIssueWithComment(ctx context.Context, projectPath string, issueID string, comment string) error {
	if r.dryRun {
		r.log.Info("dry-run: skipping CloseIssueWithComment", "issue", issueID)
		return nil
	}

	// Idempotency: check if issue is already closed before attempting close.
	stateCmd := exec.CommandContext(ctx, "gh", "issue", "view", issueID, "--json", "state", "--jq", ".state")
	if projectPath != "" {
		stateCmd.Dir = projectPath
	}
	if stateOut, err := stateCmd.Output(); err == nil {
		if strings.TrimSpace(string(stateOut)) == "CLOSED" {
			r.log.Info("issue already closed, skipping", "issue", issueID)
			return nil
		}
	}

	args := []string{"issue", "close", issueID, "--comment", comment}
	cmd := exec.CommandContext(ctx, "gh", args...)
	if projectPath != "" {
		cmd.Dir = projectPath
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to close issue %s: %w (stderr: %s)", issueID, err, stderr.String())
	}
	return nil
}

// ExecuteSubIssues executes created sub-issues sequentially and tracks progress on the parent.
// Each sub-issue is executed as a separate task, and the parent issue is updated with progress.
// Returns an error if any sub-issue fails; completed sub-issues remain done.
// executionPath may differ from task.ProjectPath when using worktree isolation (GH-968).
// GH-2177: repoPath is the real repository path (not a worktree). Sub-issues need this
// as their ProjectPath so they can create their own branches from the real repo.
// executionPath is still used for gh CLI commands (issue comments) that need worktree context.
func (r *Runner) ExecuteSubIssues(ctx context.Context, parent *Task, issues []CreatedIssue, executionPath string, repoPath string) error {
	if len(issues) == 0 {
		return fmt.Errorf("no sub-issues to execute")
	}

	total := len(issues)
	// Use executionPath for gh CLI commands (respects worktree isolation)
	projectPath := executionPath
	if projectPath == "" && parent != nil {
		projectPath = parent.ProjectPath
	}

	// GH-2177: Use repoPath for sub-task ProjectPath so each sub-issue branches
	// from the real repo, not the parent's worktree. Fall back to projectPath
	// for backwards compatibility (non-worktree mode).
	subTaskRepoPath := repoPath
	if subTaskRepoPath == "" {
		subTaskRepoPath = projectPath
	}

	r.log.Info("Starting sequential sub-issue execution",
		"parent_id", parent.ID,
		"total_issues", total,
	)

	// Update parent with start message
	startMsg := fmt.Sprintf("🚀 Starting sequential execution of %d sub-issues", total)
	if err := r.UpdateIssueProgress(ctx, projectPath, parent.ID, startMsg); err != nil {
		r.log.Warn("Failed to update parent progress", "error", err)
		// Non-fatal, continue execution
	}

	for i, issue := range issues {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return fmt.Errorf("execution cancelled: %w", ctx.Err())
		default:
		}

		// GH-1471: Determine issue reference and task ID format
		// For GitHub issues (Number > 0): use "GH-N" format for backwards compatibility
		// For non-GitHub adapters (Number == 0): use Identifier directly (e.g., "APP-123")
		var issueRef string
		var taskID string
		if issue.Number > 0 {
			// GitHub issue: use "GH-N" format
			taskID = fmt.Sprintf("GH-%d", issue.Number)
			issueRef = strconv.Itoa(issue.Number)
		} else if issue.Identifier != "" {
			// Non-GitHub adapter: use Identifier directly
			taskID = issue.Identifier
			issueRef = issue.Identifier
		} else {
			// Fallback (shouldn't happen)
			taskID = "unknown"
			issueRef = "unknown"
		}

		// Update parent with current progress
		progressMsg := fmt.Sprintf("⏳ Progress: %d/%d - Starting: **%s** (%s)",
			i, total, issue.Subtask.Title, issueRef)
		if err := r.UpdateIssueProgress(ctx, projectPath, parent.ID, progressMsg); err != nil {
			r.log.Warn("Failed to update parent progress", "error", err)
		}

		// Create task from sub-issue
		// GH-2177: Use real repo path so sub-issues can create branches from main,
		// not from inside the parent's worktree (which locks the branch).
		subTask := &Task{
			ID:          taskID,
			Title:       issue.Subtask.Title,
			Description: issue.Subtask.Description,
			ProjectPath: subTaskRepoPath,
			Branch:      fmt.Sprintf("pilot/%s", taskID),
			CreatePR:    true,
		}

		r.log.Info("Executing sub-issue",
			"parent_id", parent.ID,
			"sub_issue", issueRef,
			"order", i+1,
			"total", total,
		)

		// Execute the sub-task (use override if set, for testing)
		var result *ExecutionResult
		var err error
		if r.executeFunc != nil {
			// Use test override function if set
			result, err = r.executeFunc(ctx, subTask)
		} else {
			// GH-2178: Enable worktree isolation for sub-issues. Each sub-issue creates
			// its own worktree from the real repo (safe after GH-2177 set ProjectPath = repoPath).
			// Previously false (GH-948) to prevent nested worktrees, but GH-2177 ensured
			// subTask.ProjectPath points to the real repo, not the parent's worktree.
			result, err = r.executeWithOptions(ctx, subTask, true)
		}
		if err != nil {
			failMsg := fmt.Sprintf("❌ Failed on %d/%d: %s - Error: %v",
				i+1, total, issue.Subtask.Title, err)
			_ = r.UpdateIssueProgress(ctx, projectPath, parent.ID, failMsg)
			return fmt.Errorf("sub-issue %s failed: %w", issueRef, err)
		}

		if !result.Success {
			failMsg := fmt.Sprintf("❌ Failed on %d/%d: %s - %s",
				i+1, total, issue.Subtask.Title, result.Error)
			_ = r.UpdateIssueProgress(ctx, projectPath, parent.ID, failMsg)
			return fmt.Errorf("sub-issue %s failed: %s", issueRef, result.Error)
		}

		// TASK-356 #1: work-loss guard. A sub-issue runs with CreatePR=true, so a
		// successful child MUST yield a PR. When it instead reports success with real
		// commits (CommitSHA set) but no PR (empty PRUrl), its work is stranded in a
		// worktree that cleanup will discard — the failure mode that silently lost 26
		// minutes of a real port (studio-sdk #17). Refuse to close the issue or report
		// epic success: fail loud so the child issue stays OPEN for recovery/retry
		// instead of the work vanishing behind a false "completed" record.
		if result.PRUrl == "" && result.CommitSHA != "" {
			shortSHA := result.CommitSHA[:min(7, len(result.CommitSHA))]
			warnMsg := fmt.Sprintf("⚠️ %d/%d produced commits (%s) but no PR — work not delivered; leaving issue open for retry: %s",
				i+1, total, shortSHA, issue.Subtask.Title)
			_ = r.UpdateIssueProgress(ctx, projectPath, parent.ID, warnMsg)
			r.log.Error("sub-issue committed work but produced no PR — refusing to discard",
				"parent_id", parent.ID,
				"sub_issue", issueRef,
				"commit_sha", shortSHA,
				"branch", subTask.Branch,
			)
			return fmt.Errorf("sub-issue %s committed work (sha %s) but produced no PR — work would be lost, halting epic", issueRef, shortSHA)
		}

		// Register sub-issue PR with autopilot controller (GH-596)
		// Note: PR callback uses int issueNumber for GitHub compatibility
		if result.PRUrl != "" && r.onSubIssuePRCreated != nil {
			if prNum := parsePRNumberFromURL(result.PRUrl); prNum > 0 {
				r.onSubIssuePRCreated(prNum, result.PRUrl, issue.Number, result.CommitSHA, subTask.Branch, "")
			} else {
				r.log.Warn("Failed to extract PR number from sub-issue PR URL",
					"pr_url", result.PRUrl)
			}
		}

		// GH-2178: Wait for the sub-issue PR to merge before starting the next one.
		// Skip for the last sub-issue (no next issue to sequence).
		// Nil check degrades gracefully — if not wired, execution proceeds without waiting.
		if r.subIssueMergeWait != nil && result.PRUrl != "" && i < total-1 {
			prNum := parsePRNumberFromURL(result.PRUrl)
			if prNum > 0 {
				waitMsg := fmt.Sprintf("⏳ Waiting for PR #%d to merge before starting next sub-issue (%d/%d)...",
					prNum, i+1, total)
				_ = r.UpdateIssueProgress(ctx, projectPath, parent.ID, waitMsg)

				r.log.Info("Waiting for sub-issue PR to merge",
					"parent_id", parent.ID,
					"sub_issue", issueRef,
					"pr_number", prNum,
					"order", i+1,
					"total", total,
				)

				if err := r.subIssueMergeWait(ctx, prNum); err != nil {
					failMsg := fmt.Sprintf("❌ Merge wait failed for %s (PR #%d): %v", issueRef, prNum, err)
					_ = r.UpdateIssueProgress(ctx, projectPath, parent.ID, failMsg)
					return fmt.Errorf("merge wait failed for sub-issue %s (PR #%d): %w", issueRef, prNum, err)
				}

				// Sync local main branch so the next sub-issue branches from the merged state.
				if syncErr := r.syncMainBranch(ctx, subTaskRepoPath); syncErr != nil {
					r.log.Warn("Failed to sync main branch after sub-issue merge",
						"sub_issue", issueRef,
						"error", syncErr,
					)
					// Non-fatal: next sub-issue will fetch from origin anyway.
				}
			}
		}

		// Close completed sub-issue
		// GH-1471: Use Identifier for issue reference in close command
		closeComment := fmt.Sprintf("✅ Completed as part of %s", parent.ID)
		if result.PRUrl != "" {
			closeComment = fmt.Sprintf("✅ Completed as part of %s\nPR: %s", parent.ID, result.PRUrl)
		}
		if err := r.CloseIssueWithComment(ctx, projectPath, issueRef, closeComment); err != nil {
			r.log.Warn("Failed to close sub-issue", "issue", issueRef, "error", err)
			// Non-fatal, continue
		}

		r.log.Info("Sub-issue completed",
			"parent_id", parent.ID,
			"sub_issue", issueRef,
			"pr_url", result.PRUrl,
		)
	}

	// All done - update and close parent
	completeMsg := fmt.Sprintf("✅ Completed: %d/%d sub-issues done\n\nAll sub-tasks executed successfully.", total, total)
	_ = r.UpdateIssueProgress(ctx, projectPath, parent.ID, completeMsg)

	if err := r.CloseIssueWithComment(ctx, projectPath, parent.ID, "All sub-issues completed successfully."); err != nil {
		r.log.Warn("Failed to close parent issue", "error", err)
		// Non-fatal
	}

	r.log.Info("Epic execution completed",
		"parent_id", parent.ID,
		"total_completed", total,
	)

	return nil
}
