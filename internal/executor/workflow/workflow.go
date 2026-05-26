// Package workflow loads and parses .pilot/workflow.yaml from a target repo.
// The file is YAML front-matter (delimited by ---) followed by a Markdown body.
// Front-matter overrides executor agent settings and exposes policy fields to the
// prompt; the Markdown body is appended to the system prompt as project instructions.
package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// FilePath is the location of the workflow file relative to the repo root.
const FilePath = ".pilot/workflow.yaml"

// SupportedVersion is the only schema version this loader understands.
const SupportedVersion = 1

// AgentOverrides contains executor agent settings the repo can override.
type AgentOverrides struct {
	// MaxTurns overrides the --max-turns flag passed to the Claude Code subprocess.
	// Zero means "use Pilot's default" (no --max-turns flag passed).
	MaxTurns int `yaml:"max_turns,omitempty"`

	// ReasoningEffort overrides the effort level for execution.
	// Valid values: "low", "medium", "high", "max". Empty means no override.
	ReasoningEffort string `yaml:"reasoning_effort,omitempty"`
}

// PolicyOverrides contains policy fields exposed to the executor prompt.
type PolicyOverrides struct {
	// CommitFormat is the desired commit message format (e.g. "conventional").
	CommitFormat string `yaml:"commit_format,omitempty"`

	// BranchPrefix is the prefix for branches created by Pilot (e.g. "pilot/GH-").
	BranchPrefix string `yaml:"branch_prefix,omitempty"`

	// PRTemplate is a path (relative to repo root) to a PR template file.
	PRTemplate string `yaml:"pr_template,omitempty"`
}

// HookConfig holds hook definitions. Parsed for forward-compatibility;
// hook execution is implemented in TASK-305.
type HookConfig struct {
	AfterCreate  interface{} `yaml:"after_create,omitempty"`
	BeforeRun    interface{} `yaml:"before_run,omitempty"`
	AfterRun     interface{} `yaml:"after_run,omitempty"`
	BeforeRemove interface{} `yaml:"before_remove,omitempty"`
}

// Workflow is the parsed representation of .pilot/workflow.yaml.
type Workflow struct {
	// Version is the schema version. Only version 1 is supported.
	Version int `yaml:"version"`

	// Agent contains executor agent overrides.
	Agent AgentOverrides `yaml:"agent,omitempty"`

	// Policy contains policy fields exposed to the prompt.
	Policy PolicyOverrides `yaml:"policy,omitempty"`

	// Hooks is parsed but not executed in this version (TASK-305).
	Hooks HookConfig `yaml:"hooks,omitempty"`

	// PromptAppendix is the Markdown body after the closing --- delimiter.
	// It is appended to the executor system prompt under a "## Project Workflow" header.
	PromptAppendix string
}

// Load reads .pilot/workflow.yaml from repoPath and returns the parsed Workflow.
// Returns (nil, nil) when the file is absent — callers must treat nil as "use defaults".
// Returns an error only for malformed YAML or unsupported schema version.
func Load(repoPath string) (*Workflow, error) {
	filePath := filepath.Join(repoPath, FilePath)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("workflow: read %s: %w", filePath, err)
	}

	frontMatter, body, err := splitFrontMatter(data)
	if err != nil {
		return nil, fmt.Errorf("workflow: parse %s: %w", filePath, err)
	}

	var w Workflow
	if err := parseYAML(frontMatter, &w); err != nil {
		return nil, fmt.Errorf("workflow: yaml decode %s: %w", filePath, err)
	}

	if w.Version != SupportedVersion {
		return nil, fmt.Errorf("workflow: unsupported version %d in %s (expected %d)", w.Version, filePath, SupportedVersion)
	}

	w.PromptAppendix = strings.TrimSpace(body)
	return &w, nil
}

// splitFrontMatter splits a file with YAML front-matter delimited by "---" lines.
// The first line must be "---"; the closing delimiter is the next "---" line.
// Returns the front-matter bytes and the body string.
func splitFrontMatter(data []byte) (frontMatter []byte, body string, err error) {
	// Normalise line endings
	content := strings.ReplaceAll(string(data), "\r\n", "\n")

	const delim = "---"

	if !strings.HasPrefix(content, delim) {
		return nil, "", errors.New("missing opening --- delimiter")
	}

	// Skip the opening delimiter line
	rest := content[len(delim):]
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	}

	// Find the closing delimiter
	closingIdx := strings.Index(rest, "\n"+delim)
	if closingIdx == -1 {
		// Try a bare --- at the very start of rest (edge case: empty front-matter)
		if strings.HasPrefix(rest, delim) {
			return []byte{}, rest[len(delim):], nil
		}
		return nil, "", errors.New("missing closing --- delimiter")
	}

	fm := rest[:closingIdx]
	// Advance past the closing --- line
	afterClosing := rest[closingIdx+1+len(delim):]
	if len(afterClosing) > 0 && afterClosing[0] == '\n' {
		afterClosing = afterClosing[1:]
	}

	return []byte(fm), afterClosing, nil
}

// parseYAML decodes YAML into dst, warning on unknown top-level keys instead of failing.
func parseYAML(data []byte, dst *Workflow) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(false) // lax: unknown keys produce a warning, not an error
	if err := dec.Decode(dst); err != nil {
		return err
	}

	// Separately decode into a raw map to detect and warn about unknown top-level keys.
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	known := map[string]bool{"version": true, "agent": true, "policy": true, "hooks": true}
	for k := range raw {
		if !known[k] {
			slog.Warn("workflow: unknown top-level key ignored", slog.String("key", k))
		}
	}
	return nil
}
