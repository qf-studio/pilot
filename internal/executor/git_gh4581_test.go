package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRestoreDeletedIndexedMemoryDocs_RestoresStrikes1And2Shape reproduces
// GH-4581 (the 8th occurrence of the TASK-410 phantom-deletion class): a
// single commit deletes both a memory doc file AND its graph.json node (the
// "strikes 1-2" shape, previously seen in GH-4484/GH-4489). The restore leg,
// graphIndexedMemoryNodes, used to read graph.json via loadMemoryGraph() —
// the working tree's current/HEAD copy — so it saw no dangling reference and
// silently skipped restoring the doc, even though the doc WAS indexed on
// baseRef. EnforceMemoryDocDeletionGuard (the veto leg) already read
// baseRef's graph.json correctly and so hard-blocked the execution instead
// of the restore leg quietly fixing it up. This test pins the fix: the
// restore leg must also read baseRef's graph.json, restore the doc, and
// leave nothing for the veto leg to catch.
func TestRestoreDeletedIndexedMemoryDocs_RestoresStrikes1And2Shape(t *testing.T) {
	dir, _ := initTestRepo(t)
	ctx := context.Background()
	git := NewGitOperations(dir)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	docRel := ".agent/knowledge/memories/decisions/mem-020.md"
	write(docRel, "# a curated decision doc indexed on base, like mem-014..024")
	write(".agent/knowledge/graph.json", `{"nodes":{"memories":{"mem-020":{"file":"`+docRel+`"}}}}`)
	run("add", docRel, ".agent/knowledge/graph.json")
	run("commit", "-m", "chore: seed indexed memory doc on base")
	base, err := git.GetCurrentBranch(ctx)
	if err != nil {
		t.Fatalf("GetCurrentBranch failed: %v", err)
	}

	run("checkout", "-b", "pilot/GH-4581-repro")
	run("rm", docRel)
	// Strikes 1-2 shape: the node is removed from graph.json in the SAME
	// commit that deletes the file, so a HEAD-relative indexed check (the
	// pre-fix graphIndexedMemoryNodes) would find nothing dangling.
	write(".agent/knowledge/graph.json", `{"nodes":{"memories":{}}}`)
	run("add", ".agent/knowledge/graph.json")
	run("commit", "-m", "feat: unrelated pointer task change that also wiped a memory doc + its node")

	restored, err := git.RestoreDeletedIndexedMemoryDocs(ctx, base)
	if err != nil {
		t.Fatalf("RestoreDeletedIndexedMemoryDocs failed: %v", err)
	}
	if len(restored) != 1 || restored[0].Path != docRel || restored[0].NodeID != "mem-020" {
		t.Fatalf("restored = %+v, want [{%s mem-020}] — restore leg must check baseRef's graph.json, not HEAD's", restored, docRel)
	}
	if _, statErr := os.Stat(filepath.Join(dir, docRel)); statErr != nil {
		t.Fatalf("expected %s restored to disk, err = %v", docRel, statErr)
	}

	// Full-pipeline assertion: after the restore leg runs, the veto leg must
	// find nothing left to block. This is the acceptance criterion "the
	// 8th-occurrence scenario passes the lane guard cleanly".
	vetoed, err := git.EnforceMemoryDocDeletionGuard(ctx, base, false)
	if err != nil {
		t.Fatalf("EnforceMemoryDocDeletionGuard blocked dispatch after restore: %v (vetoed=%v)", err, vetoed)
	}
	if len(vetoed) != 0 {
		t.Fatalf("vetoed = %v, want none after successful restore", vetoed)
	}
}

// TestRestoreDeletedIndexedMemoryDocs_NeverIndexedDocStaysDeletedAndUnvetoed
// is the regression counterpart to the strikes-1-2 repro above: a memory doc
// that was NEVER indexed on baseRef must NOT be restored, and its deletion
// must NOT be vetoed either — this guard protects existing curated
// knowledge, it does not ban all memory-doc deletions outright. This pins
// that the GH-4581 fix (reading baseRef's graph.json in the restore leg
// instead of HEAD's) didn't overreach into restoring/blocking docs the
// branch had every right to delete.
func TestRestoreDeletedIndexedMemoryDocs_NeverIndexedDocStaysDeletedAndUnvetoed(t *testing.T) {
	dir, _ := initTestRepo(t)
	ctx := context.Background()
	git := NewGitOperations(dir)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	docRel := ".agent/knowledge/memories/learnings/mem_never_indexed.md"
	write(docRel, "# a stray doc, never referenced by any graph node")
	write(".agent/knowledge/graph.json", `{"nodes":{"memories":{}}}`)
	run("add", docRel, ".agent/knowledge/graph.json")
	run("commit", "-m", "chore: seed unindexed memory doc on base")
	base, err := git.GetCurrentBranch(ctx)
	if err != nil {
		t.Fatalf("GetCurrentBranch failed: %v", err)
	}

	run("checkout", "-b", "pilot/GH-4581-genuine-deletion")
	run("rm", docRel)
	run("commit", "-m", "chore(memory): remove a doc that was never indexed")

	restored, err := git.RestoreDeletedIndexedMemoryDocs(ctx, base)
	if err != nil {
		t.Fatalf("RestoreDeletedIndexedMemoryDocs failed: %v", err)
	}
	if len(restored) != 0 {
		t.Fatalf("restored = %v, want none (doc was never indexed on baseRef)", restored)
	}

	vetoed, err := git.EnforceMemoryDocDeletionGuard(ctx, base, false)
	if err != nil {
		t.Fatalf("expected no veto for a doc never indexed on baseRef, got: %v (vetoed=%v)", err, vetoed)
	}
	if len(vetoed) != 0 {
		t.Fatalf("vetoed = %v, want none (doc was never indexed on baseRef)", vetoed)
	}
}
