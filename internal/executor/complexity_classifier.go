package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// ComplexityClassifier uses Claude Code (Haiku model) to classify task complexity
// instead of word-count heuristics. Falls back to heuristic on subprocess failure.
// Caches results per task ID to avoid re-classification on retries.
//
// GH-868: Now uses `claude --print` subprocess instead of direct API calls,
// leveraging the user's existing Claude Code subscription (no ANTHROPIC_API_KEY needed).
type ComplexityClassifier struct {
	model   string
	timeout time.Duration
	log     *slog.Logger

	// cmdRunner is the function that executes the claude command.
	// Can be overridden for testing.
	cmdRunner func(ctx context.Context, args ...string) ([]byte, error)

	mu    sync.Mutex
	cache map[string]Complexity // task ID → cached classification
}

// classificationResponse is the JSON structure returned by the LLM.
type classificationResponse struct {
	Complexity string `json:"complexity"`
	Reason     string `json:"reason"`
}

const complexityClassifierSystemPrompt = `You are a task complexity classifier for a software development pipeline. Classify the given issue into exactly one complexity level.

Levels:
- TRIVIAL: Minimal changes like typos, log additions, renames, comment updates
- SIMPLE: Small focused changes: add a field, small fix, single function change
- MEDIUM: Standard feature work: new endpoint, component, integration. Also includes well-scoped issues with clear step-by-step instructions — these are NOT complex even if detailed
- COMPLEX: Requires multiple independent components or architectural changes: refactors, migrations, system redesigns
- EPIC: Too large for single execution: multi-phase projects with 5+ distinct phases

IMPORTANT: A detailed issue with clear step-by-step instructions is NOT complex — it's well-scoped MEDIUM work. COMPLEX means the task requires changes across multiple independent systems or architectural decisions.

Respond with ONLY a JSON object (no markdown, no explanation):
{"complexity": "TRIVIAL|SIMPLE|MEDIUM|COMPLEX|EPIC", "reason": "brief one-sentence explanation"}`

// NewComplexityClassifier creates a classifier that uses `claude --print` subprocess.
// Uses the user's existing Claude Code subscription - no separate API key needed.
func NewComplexityClassifier() *ComplexityClassifier {
	c := &ComplexityClassifier{
		model:   "claude-haiku-4-5-20251001",
		timeout: 30 * time.Second,
		log:     logging.WithComponent("complexity-classifier"),
		cache:   make(map[string]Complexity),
	}
	c.cmdRunner = c.defaultCmdRunner
	return c
}

// defaultCmdRunner executes the claude command.
func (c *ComplexityClassifier) defaultCmdRunner(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "claude", args...)
	return cmd.Output()
}

// newComplexityClassifierWithRunner creates a classifier with a custom command runner for testing.
func newComplexityClassifierWithRunner(runner func(ctx context.Context, args ...string) ([]byte, error)) *ComplexityClassifier {
	c := NewComplexityClassifier()
	c.cmdRunner = runner
	return c
}

// Classify determines task complexity using Claude Code subprocess.
// Returns cached result if available for the given task ID.
// Falls back to heuristic-based DetectComplexity on subprocess failure.
func (c *ComplexityClassifier) Classify(ctx context.Context, task *Task) Complexity {
	if task == nil {
		return ComplexityMedium
	}

	// Check cache first (prevents re-classification on retry)
	if task.ID != "" {
		c.mu.Lock()
		if cached, ok := c.cache[task.ID]; ok {
			c.mu.Unlock()
			c.log.Debug("using cached complexity", slog.String("task_id", task.ID), slog.String("complexity", string(cached)))
			return cached
		}
		c.mu.Unlock()
	}

	// Call Claude Code subprocess
	result, err := c.classify(ctx, task)
	if err != nil {
		c.log.Warn("LLM classification failed, falling back to heuristic",
			slog.String("task_id", task.ID),
			slog.Any("error", err),
		)
		return DetectComplexity(task)
	}

	// Cache result
	if task.ID != "" {
		c.mu.Lock()
		c.cache[task.ID] = result
		c.mu.Unlock()
	}

	c.log.Info("LLM classified task complexity",
		slog.String("task_id", task.ID),
		slog.String("complexity", string(result)),
	)

	return result
}

// classify calls Claude Code subprocess with Haiku model and parses the response.
func (c *ComplexityClassifier) classify(ctx context.Context, task *Task) (Complexity, error) {
	userContent := fmt.Sprintf("## Issue Title\n%s\n\n## Issue Description\n%s", task.Title, task.Description)

	// Truncate to avoid token overflow (description can be very long)
	const maxChars = 4000
	if len(userContent) > maxChars {
		userContent = userContent[:maxChars] + "\n...[truncated]"
	}

	// Build prompt with system instructions embedded
	prompt := fmt.Sprintf("%s\n\n---\n\n%s", complexityClassifierSystemPrompt, userContent)

	// Add timeout to context
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Call claude --print with Haiku model
	args := []string{
		"--print",
		"-p", prompt,
		"--model", c.model,
		"--output-format", "text",
	}

	output, err := c.cmdRunner(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("claude command failed: %w", err)
	}

	if len(output) == 0 {
		return "", fmt.Errorf("empty response from claude")
	}

	return parseClassificationResponse(string(output))
}

// parseClassificationResponse extracts complexity from the LLM's JSON response.
func parseClassificationResponse(text string) (Complexity, error) {
	// Strip any markdown code fence wrapper
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var resp classificationResponse
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		return "", fmt.Errorf("parse classification JSON: %w (raw: %s)", err, text)
	}

	switch strings.ToLower(resp.Complexity) {
	case "trivial":
		return ComplexityTrivial, nil
	case "simple":
		return ComplexitySimple, nil
	case "medium":
		return ComplexityMedium, nil
	case "complex":
		return ComplexityComplex, nil
	case "epic":
		return ComplexityEpic, nil
	default:
		return "", fmt.Errorf("unknown complexity level: %q", resp.Complexity)
	}
}

// HasLabel checks if the task has a specific label (case-insensitive).
func HasLabel(task *Task, label string) bool {
	if task == nil {
		return false
	}
	label = strings.ToLower(label)
	for _, l := range task.Labels {
		if strings.ToLower(l) == label {
			return true
		}
	}
	return false
}
