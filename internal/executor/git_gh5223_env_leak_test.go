package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// runScrubbed runs `git <args...>` with GIT_DIR (and friends) removed from
// its environment via testutil.ScrubbedGitEnv, regardless of what's leaked
// into the ambient process environment. dir, if non-empty, sets cmd.Dir.
func runScrubbed(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Env = testutil.ScrubbedGitEnv()
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustScrubbed(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runScrubbed(t, dir, args...)
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return out
}

// TestGitFixtures_ImmuneToLeakedGITDIR is the GH-5223 regression pin. It
// reproduces the exact leak that caused 5 core.bare-flip incidents
// (GH-5063): GIT_DIR exported into the process environment, resolving to
// an ABSOLUTE path, exactly as git passes it to a pre-push hook run from a
// linked worktree (verified repro: with GIT_DIR set to a worktree's
// gitdir, `git -C /tmp/anywhere rev-parse --absolute-git-dir` resolves to
// the real repo, overriding `-C` discovery entirely).
//
// It proves a git fixture flow built on testutil.ScrubbedGitEnv (the same
// scrub TestMain applies process-wide at suite start, see
// gitenv_testmain_test.go) leaves a decoy repo untouched even while a
// hostile GIT_DIR is set. Without the scrub, this exact sequence — bare
// init on a temp "remote", local init/commit, push — is what corrupted the
// real repo: `git init --bare` against a leaked absolute GIT_DIR
// re-initializes the real repo as bare, and the push lands fixture refs on
// the real remote.
func TestGitFixtures_ImmuneToLeakedGITDIR(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Decoy stands in for "the real repo" an absolute leaked GIT_DIR would
	// point at. Built before GIT_DIR is set, using the scrubbed helper.
	decoyDir, err := os.MkdirTemp("", "pilot-git-decoy-*")
	if err != nil {
		t.Fatalf("failed to create decoy dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(decoyDir) }()
	mustScrubbed(t, "", "init", "--bare", decoyDir)

	snapshotDecoy := func() (bareCfg, refs string) {
		t.Helper()
		bareCfg = strings.TrimSpace(mustScrubbed(t, decoyDir, "config", "--get", "core.bare"))
		refs, _ = runScrubbed(t, decoyDir, "show-ref")
		return
	}
	wantBare, wantRefs := snapshotDecoy()
	if wantBare != "true" {
		t.Fatalf("sanity check failed: decoy core.bare = %q, want true", wantBare)
	}

	// Simulate the leak: GIT_DIR exported to the decoy's absolute path, as
	// it would be inherited from a pre-push hook run in a linked worktree.
	t.Setenv("GIT_DIR", decoyDir)

	// Run the same fixture shape that historically corrupted the real repo
	// (see TestRemoteBranchExists_WithRemote): bare "remote" init, local
	// init+commit, add remote, push — all via the scrubbed helper despite
	// the hostile ambient GIT_DIR set above.
	remoteDir, err := os.MkdirTemp("", "pilot-git-remote-*")
	if err != nil {
		t.Fatalf("failed to create remote dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(remoteDir) }()

	localDir, err := os.MkdirTemp("", "pilot-git-local-*")
	if err != nil {
		t.Fatalf("failed to create local dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(localDir) }()

	mustScrubbed(t, "", "init", "--bare", remoteDir)
	mustScrubbed(t, localDir, "init")
	mustScrubbed(t, localDir, "config", "user.email", "test@test.com")
	mustScrubbed(t, localDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(localDir, "test.txt"), []byte("initial"), 0644); err != nil {
		t.Fatalf("failed to write fixture file: %v", err)
	}
	mustScrubbed(t, localDir, "add", ".")
	mustScrubbed(t, localDir, "commit", "-m", "initial")
	mustScrubbed(t, localDir, "remote", "add", "origin", remoteDir)
	mustScrubbed(t, localDir, "push", "-u", "origin", "HEAD:main")

	// The fixture flow above must have landed on remoteDir, not the decoy.
	fixtureRefs := mustScrubbed(t, remoteDir, "show-ref", "--heads", "main")
	if strings.TrimSpace(fixtureRefs) == "" {
		t.Error("expected fixture push to land on the fixture remote (main branch), got no refs")
	}

	gotBare, gotRefs := snapshotDecoy()
	if gotBare != wantBare {
		t.Errorf("decoy core.bare changed under leaked GIT_DIR: got %q, want %q", gotBare, wantBare)
	}
	if gotRefs != wantRefs {
		t.Errorf("decoy refs changed under leaked GIT_DIR: got %q, want %q", gotRefs, wantRefs)
	}
}
