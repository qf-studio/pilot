package executor

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// ghGuardJournalPath derives the GH-4671 gh-guard journal file path
// deterministically from a task ID, so the writer (the gh-guard shim
// subprocess, spawned fresh per gh call) and the reader
// (ingestGHGuardJournal, run once per task after the run completes) always
// agree on the same path without any cross-process coordination — both
// live in this same daemon process and call this same function.
func ghGuardJournalPath(taskID string) string {
	return filepath.Join(os.TempDir(), "pilot-ghguard", sanitizeBranchName(taskID)+".jsonl")
}

// ghGuardShimScript is the tiny shell shim installed on the guarded
// subprocess's PATH ahead of the real gh. It re-execs into `pilot gh-guard`
// (cmd/pilot/ghguard.go), which classifies the call and either runs the
// real gh (via PILOT_GH_REAL) or blocks it. Kept as a shell script rather
// than a Go binary copy so setup is a single file write, no compilation or
// binary copy per execution.
const ghGuardShimScript = "#!/bin/sh\nexec \"%s\" gh-guard \"$@\"\n"

// setupGHGuardShim creates a per-execution shim directory containing a `gh`
// script that routes through `pilot gh-guard`, when gh-guard is enabled and
// a real gh binary was resolved at daemon start (SetGHGuard). Returns the
// shim directory (empty string if guarding is inactive for this execution)
// and a cleanup func that removes it — always non-nil, safe to defer
// unconditionally. Setup failures (temp dir/file write errors) are logged
// and treated as "guarding inactive" rather than failing the execution —
// the guard is a containment measure, not a correctness dependency for the
// task itself.
func (b *ClaudeCodeBackend) setupGHGuardShim(opts ExecuteOptions) (dir string, cleanup func(), err error) {
	noop := func() {}
	if !b.ghGuardEnabled || b.ghRealPath == "" {
		return "", noop, nil
	}

	pilotBin, err := os.Executable()
	if err != nil {
		return "", noop, fmt.Errorf("resolve pilot binary path: %w", err)
	}

	shimDir, err := os.MkdirTemp("", "pilot-ghguard-shim-*")
	if err != nil {
		return "", noop, fmt.Errorf("create shim dir: %w", err)
	}
	cleanupFn := func() {
		if rmErr := os.RemoveAll(shimDir); rmErr != nil {
			b.log.Warn("gh_guard_shim_cleanup_failed", slog.String("dir", shimDir), slog.String("error", rmErr.Error()))
		}
	}

	shimPath := filepath.Join(shimDir, "gh")
	script := fmt.Sprintf(ghGuardShimScript, pilotBin)
	if err := os.WriteFile(shimPath, []byte(script), 0o755); err != nil {
		cleanupFn()
		return "", noop, fmt.Errorf("write gh shim: %w", err)
	}

	b.log.Debug("gh_guard_shim_installed",
		slog.String("shim_dir", shimDir),
		slog.String("task_id", opts.TaskID),
	)
	return shimDir, cleanupFn, nil
}

// ghGuardEnv builds the PILOT_GH_REAL / PILOT_TASK_* / PILOT_GH_GUARD_JOURNAL
// env vars for a guarded execution. Called only when setupGHGuardShim
// produced a non-empty shim dir.
func (b *ClaudeCodeBackend) ghGuardEnv(opts ExecuteOptions) []string {
	return []string{
		"PILOT_GH_REAL=" + b.ghRealPath,
		"PILOT_TASK_ISSUE=" + opts.GHGuardIssue,
		"PILOT_TASK_REPO=" + opts.GHGuardRepo,
		"PILOT_TASK_BRANCH=" + opts.GHGuardBranch,
		"PILOT_GH_GUARD_JOURNAL=" + ghGuardJournalPath(opts.TaskID),
	}
}
