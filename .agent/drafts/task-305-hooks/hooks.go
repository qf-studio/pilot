// Package workflow — lifecycle hook execution. TASK-305.
package workflow

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// DefaultHookTimeout is the per-hook timeout when none is configured.
const DefaultHookTimeout = 300 * time.Second

// RunHook executes the scripts in scripts sequentially via bash -lc in dir.
// env entries (KEY=VALUE) are appended to the inherited process environment.
// logFn receives combined stdout/stderr per script execution; may be nil.
// Returns the first non-zero-exit error; remaining scripts are not executed.
// A nil/empty scripts slice is a no-op.
func RunHook(ctx context.Context, name string, scripts HookValue, dir string, env []string, timeout time.Duration, logFn func(string)) error {
	if len(scripts) == 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultHookTimeout
	}
	for i, script := range scripts {
		if script == "" {
			continue
		}
		hookCtx, cancel := context.WithTimeout(ctx, timeout)
		err := execScript(hookCtx, name, i, script, dir, env, logFn)
		cancel()
		if err != nil {
			return fmt.Errorf("hook %s[%d]: %w", name, i, err)
		}
	}
	return nil
}

// BuildHookEnv returns the standard env vars injected into every hook process.
func BuildHookEnv(taskID, branch, issueURL, worktreePath string) []string {
	return []string{
		"PILOT_TASK_ID=" + taskID,
		"PILOT_BRANCH=" + branch,
		"PILOT_ISSUE_URL=" + issueURL,
		"PILOT_WORKTREE=" + worktreePath,
	}
}

func execScript(ctx context.Context, name string, idx int, script, dir string, env []string, logFn func(string)) error {
	cmd := exec.CommandContext(ctx, "bash", "-lc", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	if output := buf.String(); output != "" && logFn != nil {
		logFn(fmt.Sprintf("[hook:%s] %s", name, output))
	}
	return err
}
