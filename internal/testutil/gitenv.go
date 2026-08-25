package testutil

import (
	"os"
	"strings"
)

// gitEnvAllowlist holds GIT_* environment variables that are intentional
// and safe to leave in a test subprocess's environment (commit identity
// overrides). Everything else prefixed GIT_ is git-injected plumbing state
// and must never leak into a test that spawns its own git subprocesses.
var gitEnvAllowlist = map[string]bool{
	"GIT_AUTHOR_NAME":     true,
	"GIT_AUTHOR_EMAIL":    true,
	"GIT_AUTHOR_DATE":     true,
	"GIT_COMMITTER_NAME":  true,
	"GIT_COMMITTER_EMAIL": true,
	"GIT_COMMITTER_DATE":  true,
}

// isDangerousGitEnvVar reports whether name is a git-injected environment
// variable (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, GIT_PREFIX,
// GIT_COMMON_DIR, and so on) that must be scrubbed before spawning a git
// subprocess against a temp/fixture repo.
func isDangerousGitEnvVar(name string) bool {
	return strings.HasPrefix(name, "GIT_") && !gitEnvAllowlist[name]
}

// ScrubbedGitEnv returns a copy of the current process environment with all
// git-injected variables removed, suitable for assigning directly to
// exec.Cmd.Env before spawning a git subprocess.
//
// Why this exists (GH-5223 — root cause of 5 core.bare-flip incidents,
// GH-5063): git exports GIT_DIR (and friends) into the pre-push hook's
// environment. From a linked worktree, GIT_DIR is an ABSOLUTE path into the
// shared .git dir, and an absolute GIT_DIR overrides `git -C <dir>`
// discovery entirely — verified repro: with GIT_DIR set to a worktree's
// gitdir, `git -C /tmp/anywhere rev-parse --absolute-git-dir` resolves to
// the real repo. Any test that spawns `git` against a temp directory while
// GIT_DIR is leaked silently operates on the REAL repo instead: `git init
// --bare` flips the real repo's core.bare, fixture commits land on real
// branches, and fixture pushes hit the real GitHub remote.
func ScrubbedGitEnv() []string {
	environ := os.Environ()
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if ok && isDangerousGitEnvVar(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// UnsetLeakedGitEnv removes git-injected environment variables from the
// current process's environment outright. Call it from a package's
// TestMain so that every exec.Command/exec.CommandContext git invocation in
// the package — including ones that leave cmd.Env nil and inherit the
// ambient environment — is immune to a leaked GIT_DIR for the lifetime of
// the test binary. See ScrubbedGitEnv for the full threat model (GH-5223).
func UnsetLeakedGitEnv() {
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if ok && isDangerousGitEnvVar(name) {
			_ = os.Unsetenv(name)
		}
	}
}
