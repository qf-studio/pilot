package graphrecall

import (
	"os"
	"path/filepath"
	"testing"
)

func writeGraph(t *testing.T, projectPath, content string) {
	t.Helper()
	dir := filepath.Join(projectPath, ".agent", "knowledge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "graph.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write graph.json: %v", err)
	}
}

func TestRecallRelevant(t *testing.T) {
	// Mirrors this repo's real .agent/knowledge/graph.json shape: nodes
	// grouped by category (concepts/tasks/memories), a top-level
	// concept_index that must never be consulted directly, and memory
	// path fields expressed via the file/path/memory_file variants.
	const fixture = `{
  "version": 1,
  "nodes": {
    "concepts": {
      "executor": {
        "label": "Executor",
        "description": "Claude Code process spawner",
        "files": ["internal/executor/runner.go"]
      },
      "git-operations": {
        "label": "Git Operations",
        "description": "sync/reset/merge handling"
      },
      "dashboard": {
        "label": "Dashboard",
        "description": "TUI"
      }
    },
    "tasks": {
      "TASK-01": {"title": "Gateway Foundation", "status": "completed", "concepts": ["executor"]}
    },
    "memories": {
      "mem-low": {
        "type": "pattern",
        "summary": "single concept match, low confidence",
        "concepts": ["executor"],
        "confidence": 0.5,
        "file": "internal/memory/memories/mem-low.md"
      },
      "mem-high-conf": {
        "type": "pattern",
        "summary": "single concept match, high confidence",
        "concepts": ["executor"],
        "confidence": 0.9,
        "path": "memories/mem-high-conf.md"
      },
      "mem-two-concepts": {
        "type": "pitfall",
        "summary": "two concept matches beats single match",
        "concepts": ["executor", "git-operations"],
        "confidence": 0.1,
        "memory_file": ".agent/knowledge/memories/pitfalls/mem-two-concepts.md"
      },
      "mem-tie-b": {
        "type": "decision",
        "summary": "tie on overlap and confidence, id asc wins",
        "concepts": ["git-operations"],
        "confidence": 0.7
      },
      "mem-tie-a": {
        "type": "decision",
        "summary": "tie on overlap and confidence, id asc wins",
        "concepts": ["git-operations"],
        "confidence": 0.7
      },
      "mem-resolved-bool": {
        "type": "pitfall",
        "summary": "excluded: resolved true",
        "concepts": ["executor"],
        "confidence": 1.0,
        "resolved": true
      },
      "mem-resolved-date": {
        "type": "pitfall",
        "summary": "excluded: resolved as date string",
        "concepts": ["executor"],
        "confidence": 1.0,
        "resolved": "2026-05-20 (v2.146.7)"
      },
      "mem-superseded": {
        "type": "pitfall",
        "summary": "excluded: superseded_by set",
        "concepts": ["executor"],
        "confidence": 1.0,
        "superseded_by": "mem-two-concepts"
      },
      "mem-unrelated": {
        "type": "pattern",
        "summary": "no concept overlap with task",
        "concepts": ["dashboard"],
        "confidence": 1.0
      }
    }
  },
  "edges": [],
  "concept_index": {
    "executor": ["TASK-01"],
    "memory": ["should-never-be-read"]
  }
}`

	t.Run("ranking by overlap then confidence then id", func(t *testing.T) {
		dir := t.TempDir()
		writeGraph(t, dir, fixture)

		got := RecallRelevant(dir, "fix executor and git-operations bug", 10)

		wantOrder := []string{
			"mem-two-concepts", // overlap=2
			"mem-high-conf",    // overlap=1, confidence=0.9
			"mem-tie-a",        // overlap=1, confidence=0.7, id asc
			"mem-tie-b",        // overlap=1, confidence=0.7
			"mem-low",          // overlap=1, confidence=0.5
		}
		if len(got) != len(wantOrder) {
			t.Fatalf("got %d results, want %d: %+v", len(got), len(wantOrder), got)
		}
		for i, id := range wantOrder {
			if got[i].ID != id {
				t.Errorf("position %d: got %q, want %q (full: %+v)", i, got[i].ID, id, got)
			}
		}
	})

	t.Run("resolved and superseded memories excluded", func(t *testing.T) {
		dir := t.TempDir()
		writeGraph(t, dir, fixture)

		got := RecallRelevant(dir, "executor", 100)
		for _, m := range got {
			if m.ID == "mem-resolved-bool" || m.ID == "mem-resolved-date" || m.ID == "mem-superseded" {
				t.Errorf("expected %q to be excluded, but it was returned", m.ID)
			}
		}
	})

	t.Run("limit caps results", func(t *testing.T) {
		dir := t.TempDir()
		writeGraph(t, dir, fixture)

		got := RecallRelevant(dir, "fix executor and git-operations bug", 1)
		if len(got) != 1 {
			t.Fatalf("got %d results, want 1", len(got))
		}
		if got[0].ID != "mem-two-concepts" {
			t.Errorf("got %q, want mem-two-concepts", got[0].ID)
		}
	})

	t.Run("path key schema variants all resolve", func(t *testing.T) {
		dir := t.TempDir()
		writeGraph(t, dir, fixture)

		got := RecallRelevant(dir, "fix executor and git-operations bug", 10)
		paths := map[string]string{}
		for _, m := range got {
			paths[m.ID] = m.Path
		}
		if paths["mem-low"] != "internal/memory/memories/mem-low.md" {
			t.Errorf("file variant: got %q", paths["mem-low"])
		}
		if paths["mem-high-conf"] != "memories/mem-high-conf.md" {
			t.Errorf("path variant: got %q", paths["mem-high-conf"])
		}
		if paths["mem-two-concepts"] != ".agent/knowledge/memories/pitfalls/mem-two-concepts.md" {
			t.Errorf("memory_file variant: got %q", paths["mem-two-concepts"])
		}
		if paths["mem-tie-a"] != "" {
			t.Errorf("no path fields set: got %q, want empty", paths["mem-tie-a"])
		}
	})

	t.Run("unrelated concepts never surface", func(t *testing.T) {
		dir := t.TempDir()
		writeGraph(t, dir, fixture)

		got := RecallRelevant(dir, "fix executor bug", 100)
		for _, m := range got {
			if m.ID == "mem-unrelated" {
				t.Errorf("mem-unrelated has no concept overlap with task text and should not be returned")
			}
		}
	})

	t.Run("missing graph file fails open", func(t *testing.T) {
		dir := t.TempDir() // no .agent/knowledge/graph.json written

		got := RecallRelevant(dir, "fix executor bug", 10)
		if len(got) != 0 {
			t.Fatalf("got %d results for missing graph, want 0: %+v", len(got), got)
		}
	})

	t.Run("malformed graph file fails open", func(t *testing.T) {
		dir := t.TempDir()
		writeGraph(t, dir, `{not valid json`)

		got := RecallRelevant(dir, "fix executor bug", 10)
		if len(got) != 0 {
			t.Fatalf("got %d results for malformed graph, want 0: %+v", len(got), got)
		}
	})

	t.Run("empty task text yields no matches", func(t *testing.T) {
		dir := t.TempDir()
		writeGraph(t, dir, fixture)

		got := RecallRelevant(dir, "", 10)
		if len(got) != 0 {
			t.Fatalf("got %d results for empty task text, want 0: %+v", len(got), got)
		}
	})

	t.Run("zero or negative limit yields no results", func(t *testing.T) {
		dir := t.TempDir()
		writeGraph(t, dir, fixture)

		if got := RecallRelevant(dir, "executor", 0); len(got) != 0 {
			t.Errorf("limit=0: got %d results, want 0", len(got))
		}
		if got := RecallRelevant(dir, "executor", -1); len(got) != 0 {
			t.Errorf("limit=-1: got %d results, want 0", len(got))
		}
	})

	t.Run("label alias matching", func(t *testing.T) {
		dir := t.TempDir()
		writeGraph(t, dir, fixture)

		// Task text uses the human-readable label "Git Operations" rather
		// than the concept id "git-operations".
		got := RecallRelevant(dir, "investigate Git Operations regression", 10)
		found := false
		for _, m := range got {
			if m.ID == "mem-tie-a" || m.ID == "mem-tie-b" || m.ID == "mem-two-concepts" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected label-based match to surface git-operations memories, got %+v", got)
		}
	})
}
