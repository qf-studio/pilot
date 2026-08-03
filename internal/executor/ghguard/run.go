package ghguard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Execer runs the real gh binary with the given args, connecting stdin to
// the process's own stdin and streaming stdout/stderr to the given
// writers. Returns the process's exit code (0 on success). Injectable so
// tests can exercise Run without ever invoking a real gh process — the
// production DefaultExecer is the only implementation that spawns one.
type Execer func(realGH string, args []string, stdout, stderr io.Writer) int

// DefaultExecer shells out to the real gh binary via os/exec. Used by `pilot
// gh-guard` in production; see cmd/pilot/ghguard.go.
func DefaultExecer(realGH string, args []string, stdout, stderr io.Writer) int {
	cmd := exec.Command(realGH, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "pilot gh-guard: failed to run real gh: %v\n", err)
		return 127
	}
	return 0
}

// RunConfig is everything Run needs to classify and, if allowed, execute
// one gh invocation. Built by cmd/pilot/ghguard.go from process argv and
// the PILOT_GH_REAL / PILOT_TASK_* env vars set by the spawning backend
// (backend_claudecode.go's setupGHGuardShim).
type RunConfig struct {
	// Args is the gh invocation, with the leading "gh" already stripped.
	Args []string
	// RealGH is the absolute path to the real gh binary, resolved once at
	// daemon startup (never from the guarded subprocess's own PATH — see
	// GH-4671 acceptance criterion 2). Empty means the guard-infra itself
	// is unusable: nothing can run, allowed or not.
	RealGH string
	// TaskCtx identifies the dispatching task. Any field may be empty —
	// Classify degrades correctly (fail open for reads, fail closed for
	// mutations; see policy.go).
	TaskCtx TaskContext
	// JournalPath receives one line per denied invocation. Empty disables
	// journaling (the invocation is still denied, just not recorded).
	JournalPath string

	Stdout, Stderr io.Writer
	// Exec defaults to DefaultExecer when nil.
	Exec Execer
}

// Run classifies cfg.Args against cfg.TaskCtx and either executes the real
// gh binary (Allow) or blocks it and journals the denial (Deny). Returns a
// process exit code: the real gh's own exit code on Allow, 1 on Deny, 126
// if the real gh binary couldn't be resolved at all (a guard-infra failure
// distinct from a policy decision — see GH-4671 acceptance criterion 3).
func Run(cfg RunConfig) int {
	stderr := cfg.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	stdout := cfg.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}

	if cfg.RealGH == "" {
		fmt.Fprintln(stderr, "pilot gh-guard: PILOT_GH_REAL is not set; refusing to run gh (guard misconfigured)")
		return 126
	}

	verdict := Classify(cfg.Args, cfg.TaskCtx)
	if verdict.Decision == Deny {
		fmt.Fprintf(stderr, "pilot gh-guard: blocked `gh %s`: %s\n", strings.Join(cfg.Args, " "), verdict.Reason)
		if cfg.JournalPath != "" {
			if err := AppendDenyToJournal(cfg.JournalPath, cfg.Args, cfg.TaskCtx, verdict); err != nil {
				fmt.Fprintf(stderr, "pilot gh-guard: failed to record deny event: %v\n", err)
			}
		}
		return 1
	}

	execer := cfg.Exec
	if execer == nil {
		execer = DefaultExecer
	}
	return execer(cfg.RealGH, cfg.Args, stdout, stderr)
}
