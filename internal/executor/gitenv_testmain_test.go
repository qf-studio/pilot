package executor

import (
	"os"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// TestMain scrubs git-injected environment variables (GIT_DIR,
// GIT_WORK_TREE, GIT_INDEX_FILE, GIT_PREFIX, GIT_COMMON_DIR, ...) before any
// test in this package runs.
//
// GH-5223: this package's git fixture tests (git_test.go and neighbors)
// spawn real `git` subprocesses against temp directories. If GIT_DIR is
// leaked into the test binary's environment — as git does to the pre-push
// hook, where it resolves to an ABSOLUTE path when the push runs from a
// linked worktree — every one of those subprocesses silently operates on
// the REAL repo instead of the temp fixture: `git init --bare` flips the
// real repo's core.bare, fixture commits land on real branches, and
// fixture pushes hit the real GitHub remote. This was the root cause of 5
// core.bare-flip incidents (GH-5063). See testutil.UnsetLeakedGitEnv.
func TestMain(m *testing.M) {
	testutil.UnsetLeakedGitEnv()
	os.Exit(m.Run())
}
