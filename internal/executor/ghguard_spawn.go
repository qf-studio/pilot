// GH-4671: spawn-side wiring for the gh-guard shim. Lives in the executor
// package (rather than ghguard itself) because it needs os/exec.Cmd and the
// ClaudeCodeBackend's own resolved paths — the ghguard package stays pure
// and dependency-free so its policy core is trivially unit-testable.
package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
)

// ghGuardShimScript is the shim installed as `gh` on the subprocess's PATH.
// It re-execs the Pilot binary itself in gh-guard mode, passing the
// original argv through untouched. A shell script (rather than a symlink or
// argv[0]-dispatch binary) because /bin/sh is present on every platform
// Pilot supports (darwin + Linux) and it keeps the shim trivially
// inspectable/testable as plain text.
const ghGuardShimScript = "#!/bin/sh\nexec %q gh-guard -- \"$@\"\n"

// setupGhGuardShim creates a per-execution directory containing a `gh` shim
// script and returns that directory plus the path of the JSONL journal file
// denials will be appended to. The directory lives outside the task's git
// worktree (under os.TempDir(), not opts.ProjectPath) specifically so it is
// never swept up by the executor session's own `git add -A`/commit — a
// literal "worktree-scoped" reading would risk the guard's own evidence
// journal landing in the task's PR diff. It IS scoped to the single
// execution: the returned cleanup func removes it, and callers defer that
// cleanup immediately after a successful call.
//
// realGh may be empty (daemon startup couldn't resolve a `gh` binary on its
// own PATH) — the shim is still installed so mutations are still blocked;
// `pilot gh-guard` falls back to a PATH search (ghguard.ResolveFallbackGh)
// to find something to exec on ALLOW when PILOT_GH_REAL is unset (GH-4671
// AC3: fail closed for mutations, fail open for reads).
func setupGhGuardShim(realGh string) (shimDir, journalPath string, cleanup func(), err error) {
	shimDir, err = os.MkdirTemp("", "pilot-ghguard-*")
	if err != nil {
		return "", "", func() {}, fmt.Errorf("ghguard: create shim dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(shimDir) }

	pilotExe, err := os.Executable()
	if err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("ghguard: resolve pilot executable path: %w", err)
	}
	// Resolve symlinks so the shim always execs the real binary even if
	// os.Executable() returned a symlink path (e.g. a Homebrew/asdf shim).
	if resolved, symErr := filepath.EvalSymlinks(pilotExe); symErr == nil {
		pilotExe = resolved
	}

	shimPath := filepath.Join(shimDir, "gh")
	script := fmt.Sprintf(ghGuardShimScript, pilotExe)
	if err := os.WriteFile(shimPath, []byte(script), 0o755); err != nil { //nolint:gosec // shim must be executable
		cleanup()
		return "", "", func() {}, fmt.Errorf("ghguard: write shim script: %w", err)
	}

	journalPath = filepath.Join(shimDir, "journal.jsonl")
	return shimDir, journalPath, cleanup, nil
}

// prependPathEnv returns env with dir prepended to the PATH entry. It
// rewrites the existing "PATH=" entry in place rather than appending a
// second one — a duplicate PATH= entry in an exec.Cmd.Env slice has
// inconsistent, implementation-dependent precedence, so find-and-rewrite is
// the only safe way to do this. If no PATH entry exists (unusual, but
// possible in a stripped-down environment), one is appended.
func prependPathEnv(env []string, dir string) []string {
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			existing := strings.TrimPrefix(kv, "PATH=")
			env[i] = "PATH=" + dir + string(os.PathListSeparator) + existing
			return env
		}
	}
	return append(env, "PATH="+dir)
}

// ghGuardTaskEnv returns the PILOT_TASK_*/PILOT_GH_REAL/PILOT_GH_GUARD_*
// env vars that carry task identity and wiring down to the `pilot gh-guard`
// invocation running inside the shim.
func ghGuardTaskEnv(opts ExecuteOptions, realGh, shimDir, journalPath string) []string {
	return []string{
		ghguard.EnvTaskIssue + "=" + opts.SourceIssueID,
		ghguard.EnvTaskRepo + "=" + opts.SourceRepo,
		ghguard.EnvTaskBranch + "=" + opts.Branch,
		ghguard.EnvRealGh + "=" + realGh,
		ghguard.EnvShimDir + "=" + shimDir,
		ghguard.EnvJournalPath + "=" + journalPath,
	}
}

// readGhGuardJournal reads back denial entries from journalPath. Fails
// open: any read error is swallowed to nil (the run's outcome must never
// depend on this evidence trail being readable), matching the GH-4670
// audit's own fail-open discipline.
func readGhGuardJournal(journalPath string) []ghguard.JournalEntry {
	if journalPath == "" {
		return nil
	}
	entries, err := ghguard.ReadJournal(journalPath)
	if err != nil {
		return nil
	}
	return entries
}
