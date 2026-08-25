package autopilot

import (
	"os"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// TestMain scrubs git-injected environment variables (GIT_DIR,
// GIT_WORK_TREE, GIT_INDEX_FILE, GIT_PREFIX, GIT_COMMON_DIR, ...) before any
// test in this package runs.
//
// GH-5223: this package's conflict/controller fixture tests (runFixtureGit
// and neighbors) spawn real `git` subprocesses — including pushes to
// fixture "origin" remotes — against temp directories. If GIT_DIR is leaked
// into the test binary's environment (git exports it into the pre-push
// hook, where it resolves to an ABSOLUTE path when the push runs from a
// linked worktree), every one of those subprocesses silently operates on
// the REAL repo instead of the temp fixture, up to and including pushing
// fixture branches to the real GitHub remote. See
// testutil.UnsetLeakedGitEnv.
func TestMain(m *testing.M) {
	testutil.UnsetLeakedGitEnv()
	os.Exit(m.Run())
}
