package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const copyBackMemoryRel = ".agent/knowledge/memories/patterns/copy-back.md"

func writeCopyBackFile(t *testing.T, root, rel, content string, mtime time.Time) string {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(full, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return full
}

func readCopyBackFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// newCopyBackRepos returns a git clone (the "source repo") and a separate
// directory standing in for the task worktree.
func newCopyBackRepos(t *testing.T) (clone, worktree string) {
	t.Helper()
	clone = t.TempDir()
	runGit(t, clone, "init", "-b", "main")
	runGit(t, clone, "config", "user.email", "test@test.com")
	runGit(t, clone, "config", "user.name", "Test User")
	runGit(t, clone, "commit", "--allow-empty", "-m", "initial")
	return clone, t.TempDir()
}

// TestRunner_CopiesNavigatorLearningsBackOnFinalisation drives a full Execute
// in an isolated worktree: the backend writes a memory file in the worktree
// and the file must be on the project clone once the task has finished and the
// worktree is gone. The task fails to produce a commit, so this also covers
// the not-successful outcome.
func TestRunner_CopiesNavigatorLearningsBackOnFinalisation(t *testing.T) {
	const branch = "pilot/GH-5680-copy-back"
	dir, _ := setupFreshnessRepo(t)

	var worktreePath string
	backend := &ctxRespectingBackend{
		run: func(_ context.Context, _ int, opts ExecuteOptions) (*BackendResult, error) {
			worktreePath = opts.ProjectPath
			writeCopyBackFile(t, opts.ProjectPath, copyBackMemoryRel, "learned in the worktree\n", time.Now())
			return &BackendResult{Success: true, Output: "done"}, nil
		},
	}
	runner := newGH4964Runner(backend)
	runner.config.UseWorktree = boolPtr(true)
	task := newGH4964Task("GH-5680", branch, dir)
	task.CreatePR = false

	if _, err := runner.Execute(context.Background(), task); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if worktreePath == "" || worktreePath == dir {
		t.Fatalf("backend did not run in an isolated worktree (path %q)", worktreePath)
	}

	if got := readCopyBackFile(t, dir, copyBackMemoryRel); got != "learned in the worktree\n" {
		t.Errorf("clone memory = %q, want the worktree's content", got)
	}
}

func TestRunner_NavigatorCopyBackDisabledByConfig(t *testing.T) {
	const branch = "pilot/GH-5680-copy-back-off"
	dir, _ := setupFreshnessRepo(t)

	backend := &ctxRespectingBackend{
		run: func(_ context.Context, _ int, opts ExecuteOptions) (*BackendResult, error) {
			writeCopyBackFile(t, opts.ProjectPath, copyBackMemoryRel, "should stay in the worktree\n", time.Now())
			return &BackendResult{Success: true, Output: "done"}, nil
		},
	}
	runner := newGH4964Runner(backend)
	runner.config.UseWorktree = boolPtr(true)
	off := false
	runner.config.Navigator = &NavigatorConfig{CopyBack: &off}
	task := newGH4964Task("GH-5680-off", branch, dir)
	task.CreatePR = false

	if _, err := runner.Execute(context.Background(), task); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(copyBackMemoryRel))); err == nil {
		t.Error("navigator.copy_back=false must not copy the memory file to the clone")
	}
}

func TestNavigatorConfig_CopyBackDefaultsToTrue(t *testing.T) {
	off, on := false, true
	tests := []struct {
		name string
		cfg  *NavigatorConfig
		want bool
	}{
		{"nil config", nil, true},
		{"unset", &NavigatorConfig{}, true},
		{"default config", DefaultNavigatorConfig(), true},
		{"explicit true", &NavigatorConfig{CopyBack: &on}, true},
		{"explicit false", &NavigatorConfig{CopyBack: &off}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.CopyBackEnabled(); got != tt.want {
				t.Errorf("CopyBackEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCopyNavigatorBackFromWorktree_NewFiles(t *testing.T) {
	clone, wt := newCopyBackRepos(t)
	now := time.Now()
	writeCopyBackFile(t, wt, copyBackMemoryRel, "memory", now)
	writeCopyBackFile(t, wt, ".agent/sops/deploy/ship.md", "sop", now)
	writeCopyBackFile(t, wt, ".agent/system/arch.md", "system", now)
	// Outside the copy-back scope: never copied.
	writeCopyBackFile(t, wt, ".agent/tasks/TASK-1.md", "task", now)
	writeCopyBackFile(t, wt, ".agent/.context-markers/m.md", "marker", now)

	n, err := CopyNavigatorBackFromWorktree(wt, clone)
	if err != nil {
		t.Fatalf("CopyNavigatorBackFromWorktree: %v", err)
	}
	if n != 3 {
		t.Errorf("merged = %d, want 3", n)
	}
	for rel, want := range map[string]string{
		copyBackMemoryRel:            "memory",
		".agent/sops/deploy/ship.md": "sop",
		".agent/system/arch.md":      "system",
	} {
		if got := readCopyBackFile(t, clone, rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	for _, rel := range []string{".agent/tasks/TASK-1.md", ".agent/.context-markers/m.md"} {
		if _, err := os.Stat(filepath.Join(clone, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s is outside the copy-back scope but was copied", rel)
		}
	}
}

func TestCopyNavigatorBackFromWorktree_NewerModTimeWins(t *testing.T) {
	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)
	const rel = ".agent/sops/shared.md"

	t.Run("worktree newer overwrites the clone", func(t *testing.T) {
		clone, wt := newCopyBackRepos(t)
		writeCopyBackFile(t, clone, rel, "clone old", older)
		writeCopyBackFile(t, wt, rel, "worktree new", newer)

		n, err := CopyNavigatorBackFromWorktree(wt, clone)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("merged = %d, want 1", n)
		}
		if got := readCopyBackFile(t, clone, rel); got != "worktree new" {
			t.Errorf("clone = %q, want the newer worktree copy", got)
		}
	})

	t.Run("clone newer is kept", func(t *testing.T) {
		clone, wt := newCopyBackRepos(t)
		writeCopyBackFile(t, clone, rel, "clone new", newer)
		writeCopyBackFile(t, wt, rel, "worktree old", older)

		n, err := CopyNavigatorBackFromWorktree(wt, clone)
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("merged = %d, want 0", n)
		}
		if got := readCopyBackFile(t, clone, rel); got != "clone new" {
			t.Errorf("clone = %q, want the clone's newer copy kept", got)
		}
	})
}

func TestCopyNavigatorBackFromWorktree_RefusesGitTrackedPaths(t *testing.T) {
	clone, wt := newCopyBackRepos(t)
	const rel = ".agent/sops/tracked.md"
	older := time.Now().Add(-2 * time.Hour)
	writeCopyBackFile(t, clone, rel, "tracked content", older)
	runGit(t, clone, "add", rel)
	runGit(t, clone, "commit", "-m", "track sop")
	// The worktree copy is newer and different: only the tracked check stops it.
	writeCopyBackFile(t, wt, rel, "worktree rewrite", time.Now())
	writeCopyBackFile(t, wt, ".agent/sops/untracked.md", "fresh", time.Now())

	n, err := CopyNavigatorBackFromWorktree(wt, clone)
	if err != nil {
		t.Fatal(err)
	}
	if got := readCopyBackFile(t, clone, rel); got != "tracked content" {
		t.Errorf("tracked file = %q, copy-back must never write a git-tracked path", got)
	}
	if n != 1 || readCopyBackFile(t, clone, ".agent/sops/untracked.md") != "fresh" {
		t.Errorf("untracked sibling should still be copied (merged=%d)", n)
	}
}

func TestCopyNavigatorBackFromWorktree_NonGitSourceWritesNothing(t *testing.T) {
	clone, wt := t.TempDir(), t.TempDir()
	writeCopyBackFile(t, wt, copyBackMemoryRel, "memory", time.Now())

	if _, err := CopyNavigatorBackFromWorktree(wt, clone); err == nil {
		t.Error("expected an error when git cannot say what is tracked")
	}
	if _, err := os.Stat(filepath.Join(clone, ".agent")); err == nil {
		t.Error("nothing may be written when tracked paths are unknown")
	}
}

func TestCopyNavigatorBackFromWorktree_NeverDeletes(t *testing.T) {
	clone, wt := newCopyBackRepos(t)
	writeCopyBackFile(t, clone, ".agent/sops/only-on-clone.md", "keep me", time.Now())
	writeCopyBackFile(t, wt, ".agent/sops/other.md", "other", time.Now())

	if _, err := CopyNavigatorBackFromWorktree(wt, clone); err != nil {
		t.Fatal(err)
	}
	if got := readCopyBackFile(t, clone, ".agent/sops/only-on-clone.md"); got != "keep me" {
		t.Errorf("clone-only file changed: %q", got)
	}
}

func TestCopyNavigatorBackFromWorktree_SkipsUnmodifiedTrackedWorktreeFiles(t *testing.T) {
	clone, _ := newCopyBackRepos(t)
	// A worktree on a branch whose base carries a SOP the clone has not pulled.
	wt := t.TempDir()
	runGit(t, wt, "init", "-b", "main")
	runGit(t, wt, "config", "user.email", "test@test.com")
	runGit(t, wt, "config", "user.name", "Test User")
	writeCopyBackFile(t, wt, ".agent/sops/from-base.md", "base", time.Now())
	writeCopyBackFile(t, wt, ".agent/sops/edited.md", "v1", time.Now())
	runGit(t, wt, "add", ".")
	runGit(t, wt, "commit", "-m", "base")
	writeCopyBackFile(t, wt, ".agent/sops/edited.md", "v2", time.Now().Add(time.Minute))
	writeCopyBackFile(t, wt, ".agent/sops/new.md", "new", time.Now())

	if _, err := CopyNavigatorBackFromWorktree(wt, clone); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(clone, ".agent/sops/from-base.md")); err == nil {
		t.Error("unmodified tracked worktree file travels with the PR and must not be copied")
	}
	if got := readCopyBackFile(t, clone, ".agent/sops/edited.md"); got != "v2" {
		t.Errorf("modified tracked worktree file = %q, want v2", got)
	}
	if got := readCopyBackFile(t, clone, ".agent/sops/new.md"); got != "new" {
		t.Errorf("new file = %q", got)
	}
}

func TestCopyNavigatorBackFromWorktree_NoAgentDirIsNoOp(t *testing.T) {
	clone, wt := newCopyBackRepos(t)
	n, err := CopyNavigatorBackFromWorktree(wt, clone)
	if err != nil || n != 0 {
		t.Errorf("got (%d, %v), want (0, nil)", n, err)
	}
}

func TestCopyNavigatorBackFromWorktree_MergesGraphByID(t *testing.T) {
	clone, wt := newCopyBackRepos(t)
	const rel = ".agent/knowledge/graph.json"
	older := time.Now().Add(-time.Hour)

	// Two tasks forked from the same graph; the first already copied back.
	writeCopyBackFile(t, clone, rel, `{
  "version": "1.0.0",
  "nodes": {"memories": {"mem-base": {"label": "base"}, "mem-a": {"label": "from task A"}}},
  "edges": [{"from": "mem-base", "to": "mem-a", "type": "related"}],
  "concept_index": {"copy": ["mem-base", "mem-a"]}
}`, older)
	writeCopyBackFile(t, wt, rel, `{
  "version": "1.0.0",
  "nodes": {"memories": {"mem-base": {"label": "base"}, "mem-b": {"label": "from task B"}}, "pitfalls": {"pit-b": {"label": "p"}}},
  "edges": [{"from": "mem-base", "to": "mem-a", "type": "related"}, {"from": "mem-base", "to": "mem-b", "type": "related"}],
  "concept_index": {"copy": ["mem-base", "mem-b"]}
}`, time.Now())

	n, err := CopyNavigatorBackFromWorktree(wt, clone)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("merged = %d, want 1 (the graph)", n)
	}

	var graph struct {
		Nodes        map[string]map[string]json.RawMessage `json:"nodes"`
		Edges        []map[string]string                   `json:"edges"`
		ConceptIndex map[string][]string                   `json:"concept_index"`
	}
	if err := json.Unmarshal([]byte(readCopyBackFile(t, clone, rel)), &graph); err != nil {
		t.Fatalf("merged graph is not valid JSON: %v", err)
	}
	for _, id := range []string{"mem-base", "mem-a", "mem-b"} {
		if _, ok := graph.Nodes["memories"][id]; !ok {
			t.Errorf("node %s lost in merge", id)
		}
	}
	if _, ok := graph.Nodes["pitfalls"]["pit-b"]; !ok {
		t.Error("worktree-only category lost in merge")
	}
	if len(graph.Edges) != 2 {
		t.Errorf("edges = %v, want 2 distinct edges", graph.Edges)
	}
	if got := graph.ConceptIndex["copy"]; len(got) != 3 {
		t.Errorf("concept_index[copy] = %v, want 3 ids", got)
	}

	// A second copy-back of the same worktree changes nothing.
	n, err = CopyNavigatorBackFromWorktree(wt, clone)
	if err != nil || n != 0 {
		t.Errorf("repeat copy-back = (%d, %v), want (0, nil)", n, err)
	}
}

func TestCopyNavigatorBackFromWorktree_GraphListNodesAndNewerWins(t *testing.T) {
	clone, wt := newCopyBackRepos(t)
	const rel = ".agent/knowledge/graph.json"
	writeCopyBackFile(t, clone, rel, `{"nodes":[{"id":"n1","label":"clone"},{"id":"n2"}],"edges":[]}`, time.Now().Add(-time.Hour))
	writeCopyBackFile(t, wt, rel, `{"nodes":[{"id":"n1","label":"worktree"},{"id":"n3"}],"edges":[]}`, time.Now())

	if _, err := CopyNavigatorBackFromWorktree(wt, clone); err != nil {
		t.Fatal(err)
	}
	var graph struct {
		Nodes []map[string]string `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(readCopyBackFile(t, clone, rel)), &graph); err != nil {
		t.Fatal(err)
	}
	if len(graph.Nodes) != 3 {
		t.Fatalf("nodes = %v, want n1 n2 n3", graph.Nodes)
	}
	if graph.Nodes[0]["id"] != "n1" || graph.Nodes[0]["label"] != "worktree" {
		t.Errorf("n1 = %v, want the newer worktree version in place", graph.Nodes[0])
	}
}

func TestCopyNavigatorBackFromWorktree_GraphNewFileAndInvalidJSON(t *testing.T) {
	const rel = ".agent/knowledge/graph.json"

	t.Run("no clone graph copies the worktree graph", func(t *testing.T) {
		clone, wt := newCopyBackRepos(t)
		writeCopyBackFile(t, wt, rel, `{"nodes":{}}`, time.Now())
		if _, err := CopyNavigatorBackFromWorktree(wt, clone); err != nil {
			t.Fatal(err)
		}
		if got := readCopyBackFile(t, clone, rel); got != `{"nodes":{}}` {
			t.Errorf("graph = %q", got)
		}
	})

	t.Run("invalid worktree graph leaves the clone untouched", func(t *testing.T) {
		clone, wt := newCopyBackRepos(t)
		writeCopyBackFile(t, clone, rel, `{"nodes":{}}`, time.Now().Add(-time.Hour))
		writeCopyBackFile(t, wt, rel, `not json`, time.Now())
		if _, err := CopyNavigatorBackFromWorktree(wt, clone); err == nil {
			t.Error("expected an error for an unparseable graph")
		}
		if got := readCopyBackFile(t, clone, rel); got != `{"nodes":{}}` {
			t.Errorf("clone graph = %q, want untouched", got)
		}
	})

	t.Run("tracked graph is never written", func(t *testing.T) {
		clone, wt := newCopyBackRepos(t)
		writeCopyBackFile(t, clone, rel, `{"nodes":{"c":{"a":{}}}}`, time.Now().Add(-time.Hour))
		runGit(t, clone, "add", rel)
		runGit(t, clone, "commit", "-m", "track graph")
		writeCopyBackFile(t, wt, rel, `{"nodes":{"c":{"b":{}}}}`, time.Now())
		if _, err := CopyNavigatorBackFromWorktree(wt, clone); err != nil {
			t.Fatal(err)
		}
		if got := readCopyBackFile(t, clone, rel); got != `{"nodes":{"c":{"a":{}}}}` {
			t.Errorf("tracked graph = %q, want untouched", got)
		}
	})
}

// TestCopyFile_PreservesModTime pins the forward copy's mtime handling that
// the newer-wins rule depends on.
func TestCopyFile_PreservesModTime(t *testing.T) {
	src := writeCopyBackFile(t, t.TempDir(), "a.md", "x", time.Now().Add(-48*time.Hour))
	dst := filepath.Join(t.TempDir(), "a.md")
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	si, _ := os.Stat(src)
	di, _ := os.Stat(dst)
	if !si.ModTime().Equal(di.ModTime()) {
		t.Errorf("dst mtime %v != src mtime %v", di.ModTime(), si.ModTime())
	}
}

// GH-5710: a project with `navigator: false` gets no copy-back and no INFO line.
func TestRunner_CopyNavigatorBack_SkippedWhenProjectNavigatorDisabled(t *testing.T) {
	tests := []struct {
		name        string
		lookup      NavigatorEnabledLookup
		wantWritten bool
	}{
		{"navigator false", func(string) bool { return false }, false},
		{"navigator true", func(string) bool { return true }, true},
		{"no lookup wired", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clone, wt := newCopyBackRepos(t)
			writeCopyBackFile(t, wt, copyBackMemoryRel, "learned", time.Now())

			var logs bytes.Buffer
			runner := NewRunnerWithBackend(&ctxRespectingBackend{})
			runner.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			runner.SetNavigatorEnabledLookup(tt.lookup)

			runner.copyNavigatorBack("GH-5710", wt, clone)

			_, err := os.Stat(filepath.Join(clone, filepath.FromSlash(copyBackMemoryRel)))
			if written := err == nil; written != tt.wantWritten {
				t.Errorf("memory file written = %v, want %v", written, tt.wantWritten)
			}
			hasInfo := strings.Contains(logs.String(), "level=INFO")
			if hasInfo == !tt.wantWritten {
				t.Errorf("INFO line present = %v, want %v; logs:\n%s", hasInfo, tt.wantWritten, logs.String())
			}
		})
	}
}
