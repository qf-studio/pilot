package executor

import (
	"context"
	"strings"

	"github.com/qf-studio/pilot/internal/memory"
)

// PatternContext provides learned patterns for task execution
type PatternContext struct {
	queryService *memory.PatternQueryService
}

// NewPatternContext creates a new pattern context provider
func NewPatternContext(store *memory.Store) *PatternContext {
	return &PatternContext{
		queryService: memory.NewPatternQueryService(store),
	}
}

// GetPatternsForTask retrieves relevant patterns for a task
func (c *PatternContext) GetPatternsForTask(ctx context.Context, projectPath, taskType, taskDescription string) (string, error) {
	return c.queryService.FormatForPrompt(ctx, projectPath, taskType, taskDescription)
}

// InjectPatterns adds learned patterns to a prompt
func (c *PatternContext) InjectPatterns(ctx context.Context, prompt, projectPath, taskType, taskDescription string) (string, error) {
	patterns, err := c.GetPatternsForTask(ctx, projectPath, taskType, taskDescription)
	if err != nil {
		// Don't fail task if pattern injection fails, just log
		return prompt, nil
	}

	if patterns == "" {
		return prompt, nil
	}

	// Insert patterns before the task description
	// Find "## Task:" marker and insert before it
	taskMarker := "## Task:"
	idx := strings.Index(prompt, taskMarker)
	if idx != -1 {
		var sb strings.Builder
		sb.WriteString(prompt[:idx])
		sb.WriteString(patterns)
		sb.WriteString("\n")
		sb.WriteString(prompt[idx:])
		return sb.String(), nil
	}

	// No marker found, prepend patterns
	return patterns + "\n" + prompt, nil
}

// PatternContextConfig configures pattern context injection
type PatternContextConfig struct {
	Enabled       bool    // Enable pattern injection
	MinConfidence float64 // Minimum confidence for patterns
	MaxPatterns   int     // Maximum patterns to inject
	IncludeAnti   bool    // Include anti-patterns
}

// inferTaskType derives a task type string from the task title or labels.
// Returns a short label like "feat", "fix", "refactor", "test", "docs", "chore".
func inferTaskType(task *Task) string {
	title := strings.ToLower(task.Title)
	for _, prefix := range []string{"feat", "fix", "refactor", "test", "docs", "chore"} {
		if strings.HasPrefix(title, prefix) {
			return prefix
		}
	}
	for _, label := range task.Labels {
		l := strings.ToLower(label)
		for _, t := range []string{"feat", "fix", "refactor", "test", "docs", "chore", "bug", "enhancement"} {
			if l == t {
				if l == "bug" {
					return "fix"
				}
				if l == "enhancement" {
					return "feat"
				}
				return l
			}
		}
	}
	return "feat" // default
}

// DefaultPatternContextConfig returns default configuration
func DefaultPatternContextConfig() *PatternContextConfig {
	return &PatternContextConfig{
		Enabled:       true,
		MinConfidence: 0.6,
		MaxPatterns:   5,
		IncludeAnti:   true,
	}
}
