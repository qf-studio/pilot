package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadAgentsFile reads AGENTS.md from the project directory if it exists.
// Returns the file content as a string, or empty string if the file doesn't exist.
// This supports the Anthropic AGENTS.md convention for project-level agent instructions.
func LoadAgentsFile(projectPath string) (string, error) {
	if projectPath == "" {
		return "", nil
	}

	agentsPath := filepath.Join(projectPath, "AGENTS.md")

	if _, err := os.Stat(agentsPath); os.IsNotExist(err) {
		return "", nil
	}

	content, err := os.ReadFile(agentsPath)
	if err != nil {
		return "", fmt.Errorf("failed to read AGENTS.md: %w", err)
	}

	return strings.TrimSpace(string(content)), nil
}

// LoadAgentsFileWithCache loads AGENTS.md with caching. Returns cached content
// if the project path hasn't changed, otherwise invalidates and reloads.
func (r *Runner) LoadAgentsFileWithCache(projectPath string) (string, error) {
	r.agentsMu.Lock()
	defer r.agentsMu.Unlock()

	// Invalidate cache if project path changed (multi-repo scenario)
	if projectPath != r.agentsProjectPath {
		r.agentsContent = ""
		r.agentsProjectPath = projectPath
	}

	// Return cached content if available
	if r.agentsContent != "" {
		return r.agentsContent, nil
	}

	// Load fresh if not cached
	content, err := LoadAgentsFile(projectPath)
	if err != nil {
		return "", err
	}

	// Cache for subsequent calls
	r.agentsContent = content
	return content, nil
}

// FormatAgentsContext wraps AGENTS.md content in a prompt section.
// Returns empty string if content is empty.
func FormatAgentsContext(content string) string {
	if content == "" {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Project Agent Instructions (AGENTS.md)\n\n")
	sb.WriteString(content)
	sb.WriteString("\n\n")
	return sb.String()
}
