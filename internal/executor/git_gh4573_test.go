package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRestoreDeletedIndexedMemoryDocs_UsesOriginWhenLocalBaseIsStale is the
// TASK-424 repro/regression for the restore leg: a memory doc + its
// graph.json node land on origin/main via a second clone (e.g. Navigator
// indexing it on another session) AFTER this clone's local `main` last
// synced. A worktree branch cut from the fresh origin/main tip (worktree.go
// semantics) then deletes the doc file (leaving the graph node intact — the
// exact restore-leg shape covered by the "restores an indexed memory doc"
// case above).
//
// Comparing against the stale local `main` ref makes the doc invisible to
// deletedMemoryDocs entirely (the file never existed on that ref, so
// --diff-filter=D reports nothing) — the restore leg silently no-ops and the
// deletion rides into the PR. Comparing against origin/main (this fix) sees
// the file was there and restores it.
func TestRestoreDeletedIndexedMemoryDocs_UsesOriginWhenLocalBaseIsStale(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")
	ctx := context.Background()

	docRel := ".agent/knowledge/memories/pitfalls/mem_task_origin_only.md"
	graph := fmt.Sprintf(`{"nodes":{"memories":{"mem-origin-1":{"file":%q}}}}`, docRel)
	pushMemoryDocFromSecondClone(t, origin, "main", docRel, "# indexed only on origin, not on the stale local base", graph)

	// Cut the branch straight from the freshly fetched origin/main tip, the
	// same way worktree.go does. Local `main` is deliberately left behind —
	// it still points at setupSyncTestRepos' initial commit and has never
	// seen docRel or graph.json.
	runGit(t, local, "fetch", "origin", "main")
	runGit(t, local, "checkout", "-B", "pilot/GH-4573-restore", "origin/main")
	runGit(t, local, "rm", docRel)
	runGit(t, local, "commit", "-m", "chore(memory): strip what looked like an unused doc")

	git := NewGitOperations(local)

	// Precondition: diffing against the stale local `main` ref hides the
	// deletion entirely (the file never existed there), demonstrating the
	// bug this fix closes.
	staleDeleted, err := git.deletedMemoryDocs(ctx, "main")
	if err != nil {
		t.Fatalf("deletedMemoryDocs(stale local main): %v", err)
	}
	if len(staleDeleted) != 0 {
		t.Fatalf("test setup invalid: expected stale local `main` diff to hide the deletion (repro precondition), got %v", staleDeleted)
	}

	restored, err := git.RestoreDeletedIndexedMemoryDocs(ctx, "main")
	if err != nil {
		t.Fatalf("RestoreDeletedIndexedMemoryDocs: %v", err)
	}
	if len(restored) != 1 || restored[0].Path != docRel || restored[0].NodeID != "mem-origin-1" {
		t.Fatalf("restored = %+v, want [{%s mem-origin-1}]", restored, docRel)
	}

	content, statErr := os.ReadFile(filepath.Join(local, docRel))
	if statErr != nil {
		t.Fatalf("expected %s restored to disk, err = %v", docRel, statErr)
	}
	if string(content) != "# indexed only on origin, not on the stale local base" {
		t.Errorf("restored content = %q, want the origin-only content", content)
	}

	hasChanges, err := git.HasUncommittedChanges(ctx)
	if err != nil {
		t.Fatalf("HasUncommittedChanges: %v", err)
	}
	if hasChanges {
		t.Error("expected restoration committed, working tree still dirty")
	}
}

// TestEnforceMemoryDocDeletionGuard_UsesOriginWhenLocalBaseIsStale is the
// TASK-424 repro/regression for the veto leg. It reproduces strikes 4/5
// (#4534/#4535, #4551): a memory doc lands on origin/main (indexed) after the
// local clone's `main` ref last synced, so the stale local `main` has no
// graph.json entry for it at all — loadMemoryGraphAtRef(ctx, "main") would
// return "no graph to protect" and the veto never fires. Reading
// origin/main's graph.json (this fix) sees the doc was indexed and vetoes
// the deletion.
func TestEnforceMemoryDocDeletionGuard_UsesOriginWhenLocalBaseIsStale(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")
	ctx := context.Background()

	docRel := ".agent/knowledge/memories/pitfalls/mem_task_origin_veto.md"
	graph := fmt.Sprintf(`{"nodes":{"memories":{"mem-origin-veto":{"file":%q}}}}`, docRel)
	pushMemoryDocFromSecondClone(t, origin, "main", docRel, "# indexed only on origin, not on the stale local base", graph)

	runGit(t, local, "fetch", "origin", "main")
	runGit(t, local, "checkout", "-B", "pilot/GH-4573-veto", "origin/main")
	// Strikes 1-2/4/5 shape: delete the file AND its graph node in the same
	// commit so a HEAD-relative indexed check would find nothing dangling.
	runGit(t, local, "rm", docRel)
	if err := os.WriteFile(filepath.Join(local, ".agent", "knowledge", "graph.json"), []byte(`{"nodes":{"memories":{}}}`), 0644); err != nil {
		t.Fatalf("write graph.json: %v", err)
	}
	runGit(t, local, "add", ".agent/knowledge/graph.json")
	runGit(t, local, "commit", "-m", "feat: unrelated change that also deleted a memory doc + its node")

	git := NewGitOperations(local)

	// Precondition: the stale local `main` ref has no graph.json at all (it
	// only carries setupSyncTestRepos' initial README commit), so checking
	// against it would find "no graph to protect" and silently miss the
	// veto — demonstrating the bug this fix closes.
	staleGraph, err := git.loadMemoryGraphAtRef(ctx, "main")
	if err != nil {
		t.Fatalf("loadMemoryGraphAtRef(stale local main): %v", err)
	}
	if staleGraph != nil {
		t.Fatalf("test setup invalid: expected stale local `main` to have no graph.json (repro precondition), got %+v", staleGraph)
	}

	vetoed, err := git.EnforceMemoryDocDeletionGuard(ctx, "main", false)
	if err == nil {
		t.Fatal("expected EnforceMemoryDocDeletionGuard to veto the deletion, got nil error")
	}
	if !errors.Is(err, ErrMemoryDocDeletionVetoed) {
		t.Errorf("expected ErrMemoryDocDeletionVetoed, got: %v", err)
	}
	if len(vetoed) != 1 || vetoed[0] != docRel {
		t.Fatalf("vetoed = %v, want [%s]", vetoed, docRel)
	}
}

// TestResolveGuardBaseRef_NoOriginRemoteFallsBackToLocalBranch covers repos
// with no "origin" remote configured at all (bare local repos — several
// existing memory-guard unit tests use initTestRepo, which has none) so the
// TASK-424 origin-relative resolution doesn't regress the guards' original
// behavior in that environment.
func TestResolveGuardBaseRef_NoOriginRemoteFallsBackToLocalBranch(t *testing.T) {
	dir, _ := initTestRepo(t)
	git := NewGitOperations(dir)

	got := git.resolveGuardBaseRef(context.Background(), "main")
	if got != "main" {
		t.Errorf("resolveGuardBaseRef = %q, want \"main\" (fallback, no origin remote configured)", got)
	}
}

// pushMemoryDocFromSecondClone clones originDir, writes a memory doc plus a
// graph.json indexing it, and pushes the commit to branch — simulating a
// memory doc that was authored and indexed on origin by a different session
// after the primary local clone last synced. Mirrors
// pushExtraCommitFromSecondClone's two-repo pattern (runner_git_test.go).
func pushMemoryDocFromSecondClone(t *testing.T, originDir, branch, docRel, docContent, graphJSON string) {
	t.Helper()
	tmp := t.TempDir()
	runGitCmd := func(args ...string) {
		t.Helper()
		runGit(t, tmp, args...)
	}
	if err := exec.Command("git", "clone", "-b", branch, originDir, tmp).Run(); err != nil {
		if err := exec.Command("git", "clone", originDir, tmp).Run(); err != nil {
			t.Fatalf("clone second: %v", err)
		}
	}
	runGitCmd("config", "user.email", "other@test.com")
	runGitCmd("config", "user.name", "Other")
	runGitCmd("checkout", "-B", branch)

	full := filepath.Join(tmp, docRel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", docRel, err)
	}
	if err := os.WriteFile(full, []byte(docContent), 0644); err != nil {
		t.Fatalf("write %s: %v", docRel, err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".agent", "knowledge", "graph.json"), []byte(graphJSON), 0644); err != nil {
		t.Fatalf("write graph.json: %v", err)
	}
	runGitCmd("add", docRel, ".agent/knowledge/graph.json")
	runGitCmd("commit", "-m", "docs(agent): index a memory doc on origin")
	runGitCmd("push", "origin", branch)
}
