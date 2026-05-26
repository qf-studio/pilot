// Package workflow provides support for per-repo .pilot/workflow.yaml lifecycle hooks.
//
// Hooks allow target repos to inject project-specific shell scripts at four lifecycle
// points: after_create, before_run, after_run, and before_remove.
package workflow

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultHookTimeout is the per-hook execution timeout when none is configured.
const DefaultHookTimeout = 300 * time.Second

// WorkflowHooks configures the four lifecycle hook points from .pilot/workflow.yaml.
// Each field is a list of shell snippets executed sequentially via bash -lc.
type WorkflowHooks struct {
	AfterCreate  StringOrSlice `yaml:"after_create"`
	BeforeRun    StringOrSlice `yaml:"before_run"`
	AfterRun     StringOrSlice `yaml:"after_run"`
	BeforeRemove StringOrSlice `yaml:"before_remove"`
	// TimeoutSec is the per-hook timeout in seconds. Default: 300.
	TimeoutSec int `yaml:"timeout_sec"`
}

// StringOrSlice unmarshals a YAML value that can be either a string or a list of strings.
type StringOrSlice []string

// UnmarshalYAML handles both scalar ("echo hello") and sequence forms.
func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Value != "" {
			*s = []string{value.Value}
		}
		return nil
	case yaml.SequenceNode:
		var ss []string
		if err := value.Decode(&ss); err != nil {
			return err
		}
		*s = ss
		return nil
	default:
		return fmt.Errorf("workflow hooks: expected string or sequence, got %v", value.Kind)
	}
}

// Timeout returns the effective per-hook timeout.
func (h *WorkflowHooks) Timeout() time.Duration {
	if h == nil || h.TimeoutSec <= 0 {
		return DefaultHookTimeout
	}
	return time.Duration(h.TimeoutSec) * time.Second
}

// Scripts returns the shell snippets configured for hookName.
// Valid names: "after_create", "before_run", "after_run", "before_remove".
func (h *WorkflowHooks) Scripts(hookName string) []string {
	if h == nil {
		return nil
	}
	switch hookName {
	case "after_create":
		return []string(h.AfterCreate)
	case "before_run":
		return []string(h.BeforeRun)
	case "after_run":
		return []string(h.AfterRun)
	case "before_remove":
		return []string(h.BeforeRemove)
	}
	return nil
}

// workflowFile is the minimal YAML structure of .pilot/workflow.yaml needed for hooks.
// TASK-304 will add the full schema (agent overrides, policy, prompt body).
type workflowFile struct {
	Hooks *WorkflowHooks `yaml:"hooks"`
}

// Load reads .pilot/workflow.yaml from repoPath and returns its hooks block.
// Returns nil (not an error) when the file does not exist or has no hooks block.
func Load(repoPath string) (*WorkflowHooks, error) {
	path := filepath.Join(repoPath, ".pilot", "workflow.yaml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var wf workflowFile
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	return wf.Hooks, nil
}

// HookEnv carries the pilot-specific environment variables injected into every hook.
type HookEnv struct {
	TaskID   string // $PILOT_TASK_ID
	Branch   string // $PILOT_BRANCH
	IssueURL string // $PILOT_ISSUE_URL
	Worktree string // $PILOT_WORKTREE
}

// RunHook executes all scripts configured for hookName, in declaration order.
// Each script runs as: bash -lc "<script>" in dir.
// Returns an error on the first failing script; remaining scripts are skipped.
func RunHook(ctx context.Context, hookName string, hooks *WorkflowHooks, env HookEnv, dir string, log *slog.Logger) error {
	scripts := hooks.Scripts(hookName)
	if len(scripts) == 0 {
		return nil
	}

	timeout := hooks.Timeout()
	for i, script := range scripts {
		if err := runScript(ctx, hookName, i, script, timeout, env, dir, log); err != nil {
			return err
		}
	}
	return nil
}

// runScript executes a single hook script and streams its output to log.
func runScript(ctx context.Context, hookName string, idx int, script string, timeout time.Duration, env HookEnv, dir string, log *slog.Logger) error {
	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(hookCtx, "bash", "-lc", script)
	cmd.Dir = dir
	// Inherit process environment so PATH, HOME, and shell configs are available.
	cmd.Env = append(os.Environ(),
		"PILOT_TASK_ID="+env.TaskID,
		"PILOT_BRANCH="+env.Branch,
		"PILOT_ISSUE_URL="+env.IssueURL,
		"PILOT_WORKTREE="+env.Worktree,
	)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	log.Info("Running lifecycle hook",
		slog.String("hook", hookName),
		slog.Int("script_index", idx),
		slog.String("dir", dir),
	)

	runErr := cmd.Run()
	output := out.String()

	if output != "" {
		log.Info("Hook output",
			slog.String("hook", hookName),
			slog.Int("script_index", idx),
			slog.String("output", output),
		)
	}

	if runErr != nil {
		if hookCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("hook %s[%d] timed out after %v", hookName, idx, timeout)
		}
		return fmt.Errorf("hook %s[%d] failed: %w\noutput: %s", hookName, idx, runErr, output)
	}

	log.Info("Hook completed",
		slog.String("hook", hookName),
		slog.Int("script_index", idx),
	)
	return nil
}
