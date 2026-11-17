// GH-4671: spawn-side wiring for the gh-guard shim. Lives in the executor
// package (rather than ghguard itself) because it needs os/exec.Cmd and the
// ClaudeCodeBackend's own resolved paths — the ghguard package stays pure
// and dependency-free so its policy core is trivially unit-testable.
package executor

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
)

// ghGuardShimScript is the shim installed as `gh` on the subprocess's PATH.
// It re-execs the Pilot binary itself (with run_as_user set, the per-run
// copy of installGhGuardBinary) in gh-guard mode, passing the
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
	if err := applyGuardPerm(shimDir, 0o750); err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("ghguard: shim dir permissions: %w", err)
	}

	var pilotExe string
	if runAsSettingsNow() != nil {
		// TASK-651: the agent user cannot read the daemon's binary directory
		// on a prepared host, so the shim runs a copy in its own directory.
		pilotExe, err = installGhGuardBinary(shimDir)
		if err != nil {
			cleanup()
			return "", "", func() {}, err
		}
	} else {
		pilotExe, err = os.Executable()
		if err != nil {
			cleanup()
			return "", "", func() {}, fmt.Errorf("ghguard: resolve pilot executable path: %w", err)
		}
		// Resolve symlinks so the shim always execs the real binary even if
		// os.Executable() returned a symlink path (e.g. a Homebrew/asdf shim).
		if resolved, symErr := filepath.EvalSymlinks(pilotExe); symErr == nil {
			pilotExe = resolved
		}
	}

	shimPath := filepath.Join(shimDir, "gh")
	script := fmt.Sprintf(ghGuardShimScript, pilotExe)
	if err := os.WriteFile(shimPath, []byte(script), 0o755); err != nil { //nolint:gosec // shim must be executable
		cleanup()
		return "", "", func() {}, fmt.Errorf("ghguard: write shim script: %w", err)
	}
	if err := applyGuardPerm(shimPath, 0o750); err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("ghguard: shim script permissions: %w", err)
	}

	journalPath = filepath.Join(shimDir, "journal.jsonl")
	// TASK-651: the agent user cannot create a file in the 0750 directory of
	// the daemon user, so with run_as_user set the journal exists before the
	// spawn, empty, 0660 and in the agent group: the guard appends to it.
	if runAsSettingsNow() != nil {
		f, err := os.OpenFile(journalPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			cleanup()
			return "", "", func() {}, fmt.Errorf("ghguard: create journal: %w", err)
		}
		_ = f.Close()
		if err := applyGuardPerm(journalPath, 0o660); err != nil {
			cleanup()
			return "", "", func() {}, fmt.Errorf("ghguard: journal permissions: %w", err)
		}
	}
	return shimDir, journalPath, cleanup, nil
}

// ghGuardBinaryName is the name of the copy of the daemon binary in the shim
// directory (run_as_user set).
const ghGuardBinaryName = "pilot-gh-guard"

// ghGuardSelfExe is the running binary on Linux. It stays readable after a
// self-upgrade, when the os.Executable path ends in " (deleted)". Tests
// replace it.
var ghGuardSelfExe = "/proc/self/exe"

// ghGuardOpenBinary opens the source of the binary copy; tests replace it to
// give a source that fails in the middle of the copy.
var ghGuardOpenBinary = func(path string) (io.ReadCloser, error) {
	return os.Open(path) // #nosec G304 -- the running binary
}

// installGhGuardBinary copies the running binary into shimDir and returns the
// path of the copy, with the mode and group applyGuardPerm gives the gh
// script. The source is ghGuardSelfExe, the os.Executable path when that does
// not exist (darwin). The copy goes with shimDir in the clean-up of
// setupGhGuardShim.
//
// Decision: a copy for each run (founder call 11-16) rather than a binary in a
// directory the agent user can read: the daemon's binary directory stays
// closed to the agent user, and an upgrade during a run does not touch it.
func installGhGuardBinary(shimDir string) (string, error) {
	src := ghGuardSelfExe
	if _, err := os.Stat(src); err != nil {
		exe, exeErr := os.Executable()
		if exeErr != nil {
			return "", fmt.Errorf("ghguard: resolve pilot executable path: %w", exeErr)
		}
		src = exe
	}
	in, err := ghGuardOpenBinary(src)
	if err != nil {
		return "", fmt.Errorf("ghguard: open pilot binary %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	dst := filepath.Join(shimDir, ghGuardBinaryName)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700) // #nosec G302 -- executable, opened to the group by applyGuardPerm
	if err != nil {
		return "", fmt.Errorf("ghguard: create pilot binary copy: %w", err)
	}
	// A failed copy leaves no file at dst: a part of the binary is never run.
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return "", fmt.Errorf("ghguard: copy pilot binary %s: %w", src, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("ghguard: copy pilot binary %s: %w", src, err)
	}
	if err := applyGuardPerm(dst, 0o750); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("ghguard: pilot binary copy permissions: %w", err)
	}
	return dst, nil
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
