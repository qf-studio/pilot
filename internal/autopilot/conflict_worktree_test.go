package autopilot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runFixtureGit runs git in dir and fails the test on error, returning combined output.
func runFixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeFixtureFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// newFixtureRepo creates a bare "origin" repo plus a local clone with an
// initial commit on main, returning the local clone path (which acts as the
// stand-in for the daemon's local project checkout).
func newFixtureRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	local := filepath.Join(root, "local")

	runFixtureGit(t, root, "init", "--bare", "-b", "main", bare)
	runFixtureGit(t, root, "clone", bare, local)
	runFixtureGit(t, local, "config", "user.email", "test@example.com")
	runFixtureGit(t, local, "config", "user.name", "Test")

	writeFixtureFile(t, local, "go.mod", "module fixture\n\ngo 1.25\n")
	writeFixtureFile(t, local, "go.sum", "")
	writeFixtureFile(t, local, "README.md", "fixture\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "initial")
	runFixtureGit(t, local, "push", "origin", "main")

	return local
}

// TestAttemptLocalMerge_CleanMerge covers two branches touching disjoint
// source files — the local merge should succeed with no conflicted files.
func TestAttemptLocalMerge_CleanMerge(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "feature/a")
	writeFixtureFile(t, local, "a.txt", "from a\n")
	runFixtureGit(t, local, "add", "a.txt")
	runFixtureGit(t, local, "commit", "-m", "add a.txt")
	runFixtureGit(t, local, "push", "origin", "feature/a")

	runFixtureGit(t, local, "checkout", "main")
	writeFixtureFile(t, local, "b.txt", "from b\n")
	runFixtureGit(t, local, "add", "b.txt")
	runFixtureGit(t, local, "commit", "-m", "add b.txt")
	runFixtureGit(t, local, "push", "origin", "main")

	result, cleanup, err := attemptLocalMerge(ctx, local, "feature/a", "main")
	defer cleanup()
	if err != nil {
		t.Fatalf("attemptLocalMerge: %v", err)
	}
	if result.Conflicted() {
		t.Fatalf("expected clean merge, got conflicted files: %v", result.ConflictedFiles)
	}
	if _, statErr := os.Stat(result.WorktreePath); statErr != nil {
		t.Fatalf("expected worktree to exist at %s: %v", result.WorktreePath, statErr)
	}
}

// TestAttemptLocalMerge_GoModSumConflict reproduces the exact GH-4328 scenario:
// two sibling branches each add a different dependency, so go.mod and go.sum
// both end up textually conflicting even though the branches otherwise touch
// disjoint source files.
func TestAttemptLocalMerge_GoModSumConflict(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "feature/dep-a")
	writeFixtureFile(t, local, "go.mod", "module fixture\n\ngo 1.25\n\nrequire example.com/dep-a v1.0.0\n")
	writeFixtureFile(t, local, "go.sum", "example.com/dep-a v1.0.0 h1:aaaa=\n")
	writeFixtureFile(t, local, "internal_a.go", "package fixture\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "add dep-a")
	runFixtureGit(t, local, "push", "origin", "feature/dep-a")

	runFixtureGit(t, local, "checkout", "main")
	writeFixtureFile(t, local, "go.mod", "module fixture\n\ngo 1.25\n\nrequire example.com/dep-b v1.0.0\n")
	writeFixtureFile(t, local, "go.sum", "example.com/dep-b v1.0.0 h1:bbbb=\n")
	writeFixtureFile(t, local, "internal_b.go", "package fixture\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "add dep-b")
	runFixtureGit(t, local, "push", "origin", "main")

	result, cleanup, err := attemptLocalMerge(ctx, local, "feature/dep-a", "main")
	defer cleanup()
	if err != nil {
		t.Fatalf("attemptLocalMerge: %v", err)
	}
	if !result.Conflicted() {
		t.Fatalf("expected go.mod/go.sum conflict, got clean merge")
	}

	got := map[string]bool{}
	for _, f := range result.ConflictedFiles {
		got[f] = true
	}
	if !got["go.mod"] || !got["go.sum"] {
		t.Fatalf("expected go.mod and go.sum conflicted, got: %v", result.ConflictedFiles)
	}
	if len(result.ConflictedFiles) != 2 {
		t.Fatalf("expected exactly go.mod+go.sum conflicted (disjoint source files should merge clean), got: %v", result.ConflictedFiles)
	}
}

// TestAttemptLocalMerge_CleansUpWorktree verifies the returned cleanup func
// removes the scratch worktree.
func TestAttemptLocalMerge_CleansUpWorktree(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "feature/a")
	writeFixtureFile(t, local, "a.txt", "from a\n")
	runFixtureGit(t, local, "add", "a.txt")
	runFixtureGit(t, local, "commit", "-m", "add a.txt")
	runFixtureGit(t, local, "push", "origin", "feature/a")
	runFixtureGit(t, local, "checkout", "main")

	result, cleanup, err := attemptLocalMerge(ctx, local, "feature/a", "main")
	if err != nil {
		t.Fatalf("attemptLocalMerge: %v", err)
	}
	worktreePath := result.WorktreePath
	cleanup()

	if _, statErr := os.Stat(worktreePath); !os.IsNotExist(statErr) {
		t.Fatalf("expected worktree %s to be removed, stat err: %v", worktreePath, statErr)
	}
}

// TestGitIsStrictAncestor_StrictDescendant covers the core GH-5027 shape:
// feature/child was branched from feature/parent and carries an extra
// commit on top, so feature/parent's head is a strict ancestor of
// feature/child's head.
func TestGitIsStrictAncestor_StrictDescendant(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "feature/parent")
	writeFixtureFile(t, local, "parent.txt", "from parent\n")
	runFixtureGit(t, local, "add", "parent.txt")
	runFixtureGit(t, local, "commit", "-m", "parent commit")
	runFixtureGit(t, local, "push", "origin", "feature/parent")
	parentSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	runFixtureGit(t, local, "checkout", "-b", "feature/child")
	writeFixtureFile(t, local, "child.txt", "from child\n")
	runFixtureGit(t, local, "add", "child.txt")
	runFixtureGit(t, local, "commit", "-m", "child commit stacked on parent")
	runFixtureGit(t, local, "push", "origin", "feature/child")
	childSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	isAncestor, err := gitIsStrictAncestor(ctx, local, "feature/parent", parentSHA, "feature/child", childSHA)
	if err != nil {
		t.Fatalf("gitIsStrictAncestor: %v", err)
	}
	if !isAncestor {
		t.Fatal("expected feature/parent's head to be a strict ancestor of feature/child's head")
	}

	// Symmetric direction must be false: the parent's head is not a
	// descendant of the child's head.
	reversed, err := gitIsStrictAncestor(ctx, local, "feature/child", childSHA, "feature/parent", parentSHA)
	if err != nil {
		t.Fatalf("gitIsStrictAncestor (reversed): %v", err)
	}
	if reversed {
		t.Fatal("expected feature/child's head NOT to be an ancestor of feature/parent's head")
	}
}

// TestGitIsStrictAncestor_Unrelated covers two branches with disjoint
// histories off main — neither head is an ancestor of the other.
func TestGitIsStrictAncestor_Unrelated(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "feature/a")
	writeFixtureFile(t, local, "a.txt", "from a\n")
	runFixtureGit(t, local, "add", "a.txt")
	runFixtureGit(t, local, "commit", "-m", "add a.txt")
	runFixtureGit(t, local, "push", "origin", "feature/a")
	aSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	runFixtureGit(t, local, "checkout", "main")
	runFixtureGit(t, local, "checkout", "-b", "feature/b")
	writeFixtureFile(t, local, "b.txt", "from b\n")
	runFixtureGit(t, local, "add", "b.txt")
	runFixtureGit(t, local, "commit", "-m", "add b.txt")
	runFixtureGit(t, local, "push", "origin", "feature/b")
	bSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	isAncestor, err := gitIsStrictAncestor(ctx, local, "feature/a", aSHA, "feature/b", bSHA)
	if err != nil {
		t.Fatalf("gitIsStrictAncestor: %v", err)
	}
	if isAncestor {
		t.Fatal("expected unrelated branches to report no ancestry relationship")
	}
}

// TestGitIsStrictAncestor_SameCommit covers the identical-SHA case, which
// must report false (not a STRICT ancestor of itself) without even
// shelling out to git.
func TestGitIsStrictAncestor_SameCommit(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()
	headSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	isAncestor, err := gitIsStrictAncestor(ctx, local, "main", headSHA, "main", headSHA)
	if err != nil {
		t.Fatalf("gitIsStrictAncestor: %v", err)
	}
	if isAncestor {
		t.Fatal("expected identical SHAs to report no strict-ancestry relationship")
	}
}
