package executor

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// SimplifyConfig controls code simplification behavior.
// When enabled, Pilot auto-simplifies code after implementation for clarity.
type SimplifyConfig struct {
	// Enabled controls whether simplification runs after implementation
	Enabled bool `yaml:"enabled"`

	// Trigger specifies when simplification runs: "post-implementation" or "on-demand"
	Trigger string `yaml:"trigger"`

	// Scope determines which files to simplify: "modified" (git diff) or "all"
	Scope string `yaml:"scope"`

	// SkipPatterns are glob patterns for files to skip (e.g., "*.test.*", "*.md")
	SkipPatterns []string `yaml:"skip_patterns"`

	// MaxFileSize is the maximum file size in characters to process (default: 50000)
	MaxFileSize int `yaml:"max_file_size"`

	// PreserveComments keeps comments when simplifying (default: true)
	PreserveComments bool `yaml:"preserve_comments"`
}

// DefaultSimplifyConfig returns sensible defaults for simplification.
func DefaultSimplifyConfig() *SimplifyConfig {
	return &SimplifyConfig{
		Enabled:          false,
		Trigger:          "post-implementation",
		Scope:            "modified",
		SkipPatterns:     []string{"*.test.*", "*_test.go", "*.spec.*", "*.md", "*.json", "*.yaml", "*.yml"},
		MaxFileSize:      50000,
		PreserveComments: true,
	}
}

// SimplificationRule defines a simplification pattern.
// Each rule has a check function to detect applicability and an apply function to transform.
type SimplificationRule struct {
	// Name is a unique identifier for the rule
	Name string

	// Description explains what the rule does
	Description string

	// Check returns true if the rule can be applied to the code
	Check func(code string) bool

	// Apply transforms the code according to the rule
	Apply func(code string) string
}

// DefaultRules returns standard simplification rules.
// These rules improve code clarity without changing behavior.
func DefaultRules() []SimplificationRule {
	return []SimplificationRule{
		{
			Name:        "remove_redundant_else",
			Description: "Remove else after return/throw/break/continue",
			Check: func(code string) bool {
				// Look for patterns like: return X; } else {
				pattern := regexp.MustCompile(`(?m)(return|throw|break|continue)[^;]*;\s*\n\s*}\s*else\s*{`)
				return pattern.MatchString(code)
			},
			Apply: func(code string) string {
				// This is a structural change that requires AST parsing for safe application
				// For now, return unchanged - full implementation requires go/parser
				return code
			},
		},
		{
			Name:        "simplify_boolean_return",
			Description: "Convert if(cond) return true; return false; to return cond;",
			Check: func(code string) bool {
				pattern := regexp.MustCompile(`(?m)if\s*\([^)]+\)\s*{\s*return\s+true\s*;?\s*}\s*return\s+false`)
				return pattern.MatchString(code)
			},
			Apply: func(code string) string {
				// Structural change requiring AST - return unchanged for safety
				return code
			},
		},
		{
			Name:        "remove_unnecessary_braces",
			Description: "Remove braces from single-statement if/for/while",
			Check: func(code string) bool {
				// Detect single-statement blocks - this is style-dependent
				// Go prefers braces, so skip for .go files
				return false
			},
			Apply: func(code string) string {
				return code
			},
		},
		{
			Name:        "consolidate_imports",
			Description: "Remove duplicate and unused imports",
			Check: func(code string) bool {
				// Check for duplicate import lines
				lines := strings.Split(code, "\n")
				seen := make(map[string]bool)
				for _, line := range lines {
					trimmed := strings.TrimSpace(line)
					if strings.HasPrefix(trimmed, "import ") || (strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"")) {
						if seen[trimmed] {
							return true
						}
						seen[trimmed] = true
					}
				}
				return false
			},
			Apply: func(code string) string {
				// For Go, goimports handles this better
				return code
			},
		},
		{
			Name:        "remove_trailing_whitespace",
			Description: "Remove trailing whitespace from lines",
			Check: func(code string) bool {
				lines := strings.Split(code, "\n")
				for _, line := range lines {
					if strings.TrimRight(line, " \t") != line {
						return true
					}
				}
				return false
			},
			Apply: func(code string) string {
				lines := strings.Split(code, "\n")
				for i, line := range lines {
					lines[i] = strings.TrimRight(line, " \t")
				}
				return strings.Join(lines, "\n")
			},
		},
		{
			Name:        "normalize_empty_lines",
			Description: "Reduce multiple consecutive empty lines to one",
			Check: func(code string) bool {
				return strings.Contains(code, "\n\n\n")
			},
			Apply: func(code string) string {
				// Replace 3+ consecutive newlines with 2
				pattern := regexp.MustCompile(`\n{3,}`)
				return pattern.ReplaceAllString(code, "\n\n")
			},
		},
	}
}

// SimplifyFile applies rules to a single file.
// Returns true if the file was modified.
func SimplifyFile(path string, rules []SimplificationRule, config *SimplifyConfig) (bool, error) {
	// Check skip patterns
	for _, pattern := range config.SkipPatterns {
		matched, err := filepath.Match(pattern, filepath.Base(path))
		if err != nil {
			continue // Invalid pattern, skip
		}
		if matched {
			return false, nil
		}
	}

	// Read file
	content, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	// Check max file size
	if config.MaxFileSize > 0 && len(content) > config.MaxFileSize {
		return false, nil
	}

	code := string(content)
	modified := false

	// Apply each rule
	for _, rule := range rules {
		if rule.Check(code) {
			newCode := rule.Apply(code)
			if newCode != code {
				code = newCode
				modified = true
			}
		}
	}

	// Write if changed
	if modified {
		if err := os.WriteFile(path, []byte(code), 0644); err != nil {
			return false, err
		}
	}

	return modified, nil
}

// SimplifyModifiedFiles simplifies all files in git diff.
// Returns the list of modified files and any error.
func SimplifyModifiedFiles(projectPath string, config *SimplifyConfig) ([]string, error) {
	if config == nil {
		config = DefaultSimplifyConfig()
	}

	// Get modified files from git
	var files []string
	var err error

	if config.Scope == "all" {
		files, err = getAllTrackedFiles(projectPath)
	} else {
		files, err = getModifiedFiles(projectPath)
	}

	if err != nil {
		return nil, err
	}

	rules := DefaultRules()
	var simplified []string

	// Apply simplification to each file
	for _, file := range files {
		fullPath := filepath.Join(projectPath, file)

		// Skip non-existent files (deleted in git)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			continue
		}

		modified, err := SimplifyFile(fullPath, rules, config)
		if err != nil {
			// Log but continue with other files
			continue
		}
		if modified {
			simplified = append(simplified, file)
		}
	}

	return simplified, nil
}

// getModifiedFiles returns files modified in git working directory.
func getModifiedFiles(projectPath string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", "HEAD")
	cmd.Dir = projectPath
	output, err := cmd.Output()
	if err != nil {
		// Fallback to staged files
		cmd = exec.Command("git", "diff", "--name-only", "--cached")
		cmd.Dir = projectPath
		output, err = cmd.Output()
		if err != nil {
			return nil, err
		}
	}

	var files []string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// getAllTrackedFiles returns all files tracked by git.
func getAllTrackedFiles(projectPath string) ([]string, error) {
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = projectPath
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var files []string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}
