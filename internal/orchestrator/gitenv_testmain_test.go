package orchestrator

import (
	"os"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// TestMain scrubs git-injected environment variables (GIT_DIR,
// GIT_WORK_TREE, GIT_INDEX_FILE, GIT_PREFIX, GIT_COMMON_DIR, ...) before any
// test in this package runs.
//
// GH-5223: setupTestGitRepo (quality_integration_test.go) spawns real `git`
// subprocesses against a temp directory. If GIT_DIR is leaked into the test
// binary's environment (git exports it into the pre-push hook, where it
// resolves to an ABSOLUTE path when the push runs from a linked worktree),
// those subprocesses silently operate on the REAL repo instead. See
// testutil.UnsetLeakedGitEnv.
func TestMain(m *testing.M) {
	testutil.UnsetLeakedGitEnv()
	os.Exit(m.Run())
}
