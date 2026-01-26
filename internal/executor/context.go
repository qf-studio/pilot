package executor

import (
	"context"
	"strings"

	"github.com/alekspetrov/pilot/internal/memory"
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
func (c *PatternContext) GetPatternsForTask(ctx context.Context, projectPath, taskDescription string) (string, error) {
	return c.queryService.FormatForPrompt(ctx, projectPath, taskDescription)
}

// InjectPatterns adds learned patterns to a prompt
func (c *PatternContext) InjectPatterns(ctx context.Context, prompt, projectPath, taskDescription string) (string, error) {
	patterns, err := c.GetPatternsForTask(ctx, projectPath, taskDescription)
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

// DefaultPatternContextConfig returns default configuration
func DefaultPatternContextConfig() *PatternContextConfig {
	return &PatternContextConfig{
		Enabled:       true,
		MinConfidence: 0.6,
		MaxPatterns:   5,
		IncludeAnti:   true,
	}
}
