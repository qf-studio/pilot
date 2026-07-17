package executor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestFinalizeDecomposedParentPR_RestoresDeletedIndexedMemoryDoc covers
// GH-4397: the wiring leg of the GH-4387 protected-memory guard. Detection
// and restore (GitOperations.RestoreDeletedIndexedMemoryDocs, GH-4398) and
// event recording (Runner.recordMemoryGuardRestoreEvents) already existed,
// but neither was ever called from a push/PR-create path — a subtask session
// that deleted a graph-indexed memory doc during "cleanup" would still ship a
// PR that trips the Knowledge Graph Drift Gate. This proves the wiring: a
// deletion of a graph-indexed file is restored before push, the restored file
// survives in the pushed branch's diff against base, and the intervention is
// recorded as a StageMemoryGuardRestore execution event.
func TestFinalizeDecomposedParentPR_RestoresDeletedIndexedMemoryDoc(t *testing.T) {
	setUpFakeGhPATH(t, []byte(`[]`), []byte(`[]`)) // no pre-existing merged/open PR

	repoDir, _ := setupFreshnessRepo(t)

	// Seed graph.json + the indexed memory doc on main before branching.
	docRel := ".agent/knowledge/memories/pitfalls/pitfall_gh4397_repro.md"
	docPath := filepath.Join(repoDir, docRel)
	if err := os.MkdirAll(filepath.Dir(docPath), 0755); err != nil {
		t.Fatalf("mkdir memories dir: %v", err)
	}
	if err := os.WriteFile(docPath, []byte("# a pitfall the branch base already carries\n"), 0644); err != nil {
		t.Fatalf("write memory doc: %v", err)
	}
	graph := `{"nodes":{"memories":{"mem-gh4397":{"file":"` + docRel + `"}}}}`
	graphPath := filepath.Join(repoDir, ".agent", "knowledge", "graph.json")
	if err := os.WriteFile(graphPath, []byte(graph), 0644); err != nil {
		t.Fatalf("write graph.json: %v", err)
	}
	runGit(t, repoDir, "add", docRel, ".agent/knowledge/graph.json")
	runGit(t, repoDir, "commit", "-m", "chore: seed indexed memory doc + graph.json")
	runGit(t, repoDir, "push", "origin", "main")

	runGit(t, repoDir, "checkout", "-b", "pilot/GH-9397")

	// Real subtask work.
	if err := os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("subtask work\n"), 0644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	runGit(t, repoDir, "add", "f.txt")
	runGit(t, repoDir, "commit", "-m", "subtask work")

	// The session "cleans up" what it judges an unused doc — but it's still
	// graph-indexed, the exact shape that produced the drift-gate-red PR #4385.
	runGit(t, repoDir, "rm", docRel)
	runGit(t, repoDir, "commit", "-m", "chore(memory): strip unindexed memory doc(s) added during execution")

	store, cleanup := setupTestStore(t)
	defer cleanup()

	r := newSilentRunnerTask359()
	r.SetLogStore(store)
	creator := &fakePRCreatorGH4031{url: "https://gitlab.example.com/o/r/-/merge_requests/12"}
	r.prCreator = creator
	task := &Task{
		ID:            "GH-9397",
		Title:         "fix something",
		Description:   "d",
		Branch:        "pilot/GH-9397",
		BaseBranch:    "main",
		CreatePR:      true,
		SourceAdapter: "gitlab", // non-github so the registry PRCreator branch is used
	}
	if err := store.SaveExecution(&memory.Execution{ID: task.LogExecutionID(), TaskID: task.ID, Status: "running"}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}
	result := &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "placeholder"}

	r.finalizeDecomposedParentPR(context.Background(), task, NewGitOperations(repoDir), result)

	if !result.Success {
		t.Fatalf("expected Success=true (guard must restore, not block), got false (error=%q)", result.Error)
	}
	if result.PRUrl == "" {
		t.Error("expected a PR URL")
	}

	// The pushed branch must no longer show the indexed doc as deleted
	// relative to base — the guard restored it via a follow-up commit.
	diffCmd := exec.Command("git", "diff", "--name-only", "--diff-filter=D", "origin/main...pilot/GH-9397")
	diffCmd.Dir = repoDir
	out, err := diffCmd.Output()
	if err != nil {
		t.Fatalf("git diff failed: %v", err)
	}
	if strings.Contains(string(out), docRel) {
		t.Errorf("expected %s restored (not deleted) in pushed branch diff, got deletions: %s", docRel, out)
	}
	if _, statErr := os.Stat(docPath); statErr != nil {
		t.Errorf("expected %s restored on disk, stat err = %v", docRel, statErr)
	}

	// The intervention must be visible in the execution_events ledger.
	events, err := store.ListExecutionEvents(task.LogExecutionID())
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	var found bool
	for _, e := range events {
		if e.Stage != memory.StageMemoryGuardRestore {
			continue
		}
		var detail struct {
			Path   string `json:"path"`
			NodeID string `json:"node_id"`
		}
		if err := json.Unmarshal([]byte(e.Detail), &detail); err != nil {
			t.Fatalf("failed to unmarshal event detail: %v", err)
		}
		if detail.Path == docRel && detail.NodeID == "mem-gh4397" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a StageMemoryGuardRestore event for %s (node mem-gh4397), got events: %+v", docRel, events)
	}
}

// TestFinalizeDecomposedParentPR_AllowsDeletingUnindexedMemoryDoc covers the
// inverse of GH-4397's guard: deleting a genuinely unindexed memory doc (no
// surviving graph.json node) must remain allowed — the guard must not
// resurrect files nobody references.
func TestFinalizeDecomposedParentPR_AllowsDeletingUnindexedMemoryDoc(t *testing.T) {
	setUpFakeGhPATH(t, []byte(`[]`), []byte(`[]`))

	repoDir, _ := setupFreshnessRepo(t)

	docRel := ".agent/knowledge/memories/pitfalls/pitfall_gh4397_unindexed.md"
	docPath := filepath.Join(repoDir, docRel)
	if err := os.MkdirAll(filepath.Dir(docPath), 0755); err != nil {
		t.Fatalf("mkdir memories dir: %v", err)
	}
	if err := os.WriteFile(docPath, []byte("# never indexed\n"), 0644); err != nil {
		t.Fatalf("write memory doc: %v", err)
	}
	graphPath := filepath.Join(repoDir, ".agent", "knowledge", "graph.json")
	if err := os.MkdirAll(filepath.Dir(graphPath), 0755); err != nil {
		t.Fatalf("mkdir graph dir: %v", err)
	}
	if err := os.WriteFile(graphPath, []byte(`{"nodes":{"memories":{}}}`), 0644); err != nil {
		t.Fatalf("write graph.json: %v", err)
	}
	runGit(t, repoDir, "add", docRel, ".agent/knowledge/graph.json")
	runGit(t, repoDir, "commit", "-m", "chore: seed unindexed memory doc + graph.json")
	runGit(t, repoDir, "push", "origin", "main")

	runGit(t, repoDir, "checkout", "-b", "pilot/GH-9398")
	if err := os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("subtask work\n"), 0644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	runGit(t, repoDir, "add", "f.txt")
	runGit(t, repoDir, "commit", "-m", "subtask work")
	runGit(t, repoDir, "rm", docRel)
	runGit(t, repoDir, "commit", "-m", "chore(memory): remove genuinely unused doc")

	store, cleanup := setupTestStore(t)
	defer cleanup()

	r := newSilentRunnerTask359()
	r.SetLogStore(store)
	creator := &fakePRCreatorGH4031{url: "https://gitlab.example.com/o/r/-/merge_requests/13"}
	r.prCreator = creator
	task := &Task{
		ID:            "GH-9398",
		Title:         "fix something",
		Description:   "d",
		Branch:        "pilot/GH-9398",
		BaseBranch:    "main",
		CreatePR:      true,
		SourceAdapter: "gitlab",
	}
	if err := store.SaveExecution(&memory.Execution{ID: task.LogExecutionID(), TaskID: task.ID, Status: "running"}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}
	result := &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "placeholder"}

	r.finalizeDecomposedParentPR(context.Background(), task, NewGitOperations(repoDir), result)

	if !result.Success {
		t.Fatalf("expected Success=true, got false (error=%q)", result.Error)
	}

	diffCmd := exec.Command("git", "diff", "--name-only", "--diff-filter=D", "origin/main...pilot/GH-9398")
	diffCmd.Dir = repoDir
	out, err := diffCmd.Output()
	if err != nil {
		t.Fatalf("git diff failed: %v", err)
	}
	if !strings.Contains(string(out), docRel) {
		t.Errorf("expected %s to remain deleted (genuinely unindexed) in pushed branch diff, got: %s", docRel, out)
	}
	if _, statErr := os.Stat(docPath); !os.IsNotExist(statErr) {
		t.Errorf("expected %s to remain absent on disk, stat err = %v", docRel, statErr)
	}

	events, err := store.ListExecutionEvents(task.LogExecutionID())
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	for _, e := range events {
		if e.Stage == memory.StageMemoryGuardRestore {
			t.Errorf("expected no StageMemoryGuardRestore event for a genuinely unindexed doc, got: %+v", e)
		}
	}
}
