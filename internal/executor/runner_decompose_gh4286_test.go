package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFinalizeDecomposedParentPR_StripsUnindexedMemoryDoc is the acceptance
// regression for GH-4286: a subtask session that captures a memory doc under
// .agent/knowledge/memories (e.g. a task that says "document the pitfall")
// without indexing it in graph.json must not carry that doc into the pushed
// PR. Left in place it trips the Knowledge Graph Drift Gate
// (scripts/check-graph.py), which the autopilot CI-fix path treats as a real
// build failure — up to closing the PR via the size guard (PR #4279 was lost
// this way). finalizeDecomposedParentPR is the decomposed/epic-parent
// finalize path (distinct from the direct path and finalizeEpicBranchPR,
// both already covered by TestStripUnindexedMemoryDocs in git_test.go).
func TestFinalizeDecomposedParentPR_StripsUnindexedMemoryDoc(t *testing.T) {
	setUpFakeGhPATH(t, []byte(`[]`), []byte(`[]`)) // no pre-existing merged/open PR

	repoDir, _ := setupFreshnessRepo(t)

	// Seed graph.json on main before branching — the drift gate only has
	// something to check an added doc against once one exists.
	graphPath := filepath.Join(repoDir, ".agent", "knowledge", "graph.json")
	if err := os.MkdirAll(filepath.Dir(graphPath), 0755); err != nil {
		t.Fatalf("mkdir graph dir: %v", err)
	}
	if err := os.WriteFile(graphPath, []byte(`{"nodes":{"memories":{}}}`), 0644); err != nil {
		t.Fatalf("write graph.json: %v", err)
	}
	runGit(t, repoDir, "add", ".agent/knowledge/graph.json")
	runGit(t, repoDir, "commit", "-m", "chore: seed graph.json")
	runGit(t, repoDir, "push", "origin", "main")

	runGit(t, repoDir, "checkout", "-b", "pilot/GH-9286")

	// Real subtask work.
	if err := os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("subtask work\n"), 0644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	runGit(t, repoDir, "add", "f.txt")
	runGit(t, repoDir, "commit", "-m", "subtask work")

	// A "document the pitfall" memory doc, captured but never indexed — the
	// exact shape that produced the drift-gate-red PR #4279.
	docRel := ".agent/knowledge/memories/pitfalls/pitfall_gh4286_repro.md"
	docPath := filepath.Join(repoDir, docRel)
	if err := os.MkdirAll(filepath.Dir(docPath), 0755); err != nil {
		t.Fatalf("mkdir memories dir: %v", err)
	}
	if err := os.WriteFile(docPath, []byte("# document the pitfall\n"), 0644); err != nil {
		t.Fatalf("write memory doc: %v", err)
	}
	runGit(t, repoDir, "add", docRel)
	runGit(t, repoDir, "commit", "-m", "docs: document the pitfall")

	r := newSilentRunnerTask359()
	creator := &fakePRCreatorGH4031{url: "https://gitlab.example.com/o/r/-/merge_requests/11"}
	r.prCreator = creator
	task := &Task{
		ID:            "GH-9286",
		Title:         "fix something",
		Description:   "d",
		Branch:        "pilot/GH-9286",
		BaseBranch:    "main",
		CreatePR:      true,
		SourceAdapter: "gitlab", // non-github so the registry PRCreator branch is used
	}
	result := &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "placeholder"}

	r.finalizeDecomposedParentPR(context.Background(), task, NewGitOperations(repoDir), result)

	if !result.Success {
		t.Fatalf("expected Success=true (drift gate must not block the PR), got false (error=%q)", result.Error)
	}
	if result.PRUrl == "" {
		t.Error("expected a PR URL")
	}

	// The pushed branch must no longer carry the unindexed memory doc — the
	// drift gate would fail CI on it otherwise — while the real code commit
	// survives.
	diffCmd := exec.Command("git", "diff", "--name-only", "origin/main...pilot/GH-9286")
	diffCmd.Dir = repoDir
	out, err := diffCmd.Output()
	if err != nil {
		t.Fatalf("git diff failed: %v", err)
	}
	if strings.Contains(string(out), docRel) {
		t.Errorf("expected %s stripped from pushed branch diff, got: %s", docRel, out)
	}
	if !strings.Contains(string(out), "f.txt") {
		t.Errorf("expected f.txt (real work) to survive in pushed branch diff, got: %s", out)
	}
}
