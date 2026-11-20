package workflow

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/qf-studio/pilot/internal/procgroup"
)

// DefaultHookTimeout is the per-script timeout applied when the caller passes 0.
const DefaultHookTimeout = 5 * time.Minute

// HookValue is an ordered list of shell scripts executed sequentially for one lifecycle event.
// YAML accepts: null/omit (no-op), a single string, or a list of strings — all normalised here.
type HookValue []string

// UnmarshalYAML allows a HookValue field to accept null, a single string, or a sequence.
func (h *HookValue) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Tag == "!!null" || value.Value == "" {
			*h = nil
			return nil
		}
		*h = HookValue{value.Value}
	case yaml.SequenceNode:
		var ss []string
		if err := value.Decode(&ss); err != nil {
			return fmt.Errorf("hook: decode sequence: %w", err)
		}
		*h = ss
	default:
		return fmt.Errorf("hook: unsupported YAML type %s", value.Tag)
	}
	return nil
}

// CommandBuilder builds the command of one hook script: bash -lc script in
// dir with the environment env. The command must stop on a cancel of ctx.
// RunHookWith sets its output.
type CommandBuilder func(ctx context.Context, dir string, env []string, script string) (*exec.Cmd, error)

// RunHook executes each script in scripts sequentially via bash -lc.
// It stops and returns the first error; subsequent scripts are skipped.
// timeout applies per-script; 0 uses DefaultHookTimeout.
// logFn, if non-nil, receives combined stdout+stderr from each script.
func RunHook(ctx context.Context, name string, scripts HookValue, dir string, env []string, timeout time.Duration, logFn func(string)) error {
	return RunHookWith(ctx, name, scripts, dir, env, timeout, logFn, bashCommand)
}

// RunHookWith is RunHook with the command of each script built by build.
func RunHookWith(ctx context.Context, name string, scripts HookValue, dir string, env []string, timeout time.Duration, logFn func(string), build CommandBuilder) error {
	if len(scripts) == 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultHookTimeout
	}
	for i, script := range scripts {
		hctx, cancel := context.WithTimeout(ctx, timeout)
		out, err := runScript(hctx, script, dir, env, build)
		cancel()
		if logFn != nil && len(out) > 0 {
			logFn(string(out))
		}
		if err != nil {
			return fmt.Errorf("hook %s[%d]: %w", name, i, err)
		}
	}
	return nil
}

// bashCommand is the builder of RunHook: bash -lc script in dir with env, in
// its own process group, and a cancel of ctx kills that group, so a child the
// script started in the background does not outlive the hook.
func bashCommand(ctx context.Context, dir string, env []string, script string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, "bash", "-lc", script)
	cmd.Dir = dir
	cmd.Env = env
	procgroup.Configure(cmd)
	cmd.Cancel = func() error { return procgroup.Kill(cmd, syscall.SIGKILL) }
	return cmd, nil
}

func runScript(ctx context.Context, script, dir string, env []string, build CommandBuilder) ([]byte, error) {
	cmd, err := build(ctx, dir, env, script)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.Bytes(), err
}
