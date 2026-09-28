package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// emitWriteToolUse simulates the model calling the Write tool, which bumps
// progressState.filesWrite — the pre-GH-5477 (and fallback) FilesChanged source.
func emitWriteToolUse(opts ExecuteOptions, path string) {
	if opts.EventHandler != nil {
		opts.EventHandler(BackendEvent{
			Type:      EventTypeToolUse,
			ToolName:  "Write",
			ToolInput: map[string]interface{}{"file_path": path},
		})
	}
}

// TestExecute_MetricsFill_PopulatesLinesFromDiff drives the real Runner.Execute
// path against a real git fixture: the backend commits three files, and the
// metrics-fill seam must record the diff's line and file counts rather than the
// Write-tool-call count (GH-5477).
func TestExecute_MetricsFill_PopulatesLinesFromDiff(t *testing.T) {
	const branch = "pilot/GH-5477-fill"
	dir, _ := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &mockGH4964Backend{
		perCall: func(_ int, opts ExecuteOptions) *BackendResult {
			files := map[string]string{
				"a.go": "package x\n\nfunc A() {}\n",
				"b.go": "package x\n\nfunc B() {}\n",
				"c.go": "package x\n",
			}
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Errorf("write %s: %v", name, err)
				}
			}
			runGit(t, dir, "add", "-A")
			runGit(t, dir, "commit", "-m", "feat: add files")
			// Only one Write call recorded, so FilesChanged==3 can only come from the diff.
			emitWriteToolUse(opts, filepath.Join(dir, "a.go"))
			return &BackendResult{Success: true}
		},
	}
	runner := newGH4964Runner(backend)
	task := newGH4964Task("GH-5477A", branch, dir)
	task.CreatePR = false

	result, err := runner.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if result.LinesAdded != 7 {
		t.Errorf("LinesAdded = %d, want 7", result.LinesAdded)
	}
	if result.LinesRemoved != 0 {
		t.Errorf("LinesRemoved = %d, want 0", result.LinesRemoved)
	}
	if result.FilesChanged != 3 {
		t.Errorf("FilesChanged = %d, want 3 (len of diff files, not Write calls)", result.FilesChanged)
	}
}

// TestExecute_MetricsFill_NoCommitFallsBackToWriteCount covers the fallback:
// with no commit there is no diff, so FilesChanged keeps the Write-tool-call
// count and the line counts stay zero.
func TestExecute_MetricsFill_NoCommitFallsBackToWriteCount(t *testing.T) {
	const branch = "pilot/GH-5477-nocommit"
	dir, _ := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &mockGH4964Backend{
		perCall: func(_ int, opts ExecuteOptions) *BackendResult {
			emitWriteToolUse(opts, filepath.Join(dir, "a.go"))
			emitWriteToolUse(opts, filepath.Join(dir, "b.go"))
			return &BackendResult{Success: true}
		},
	}
	runner := newGH4964Runner(backend)
	task := newGH4964Task("GH-5477B", branch, dir)
	task.CreatePR = false

	result, err := runner.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if result.LinesAdded != 0 || result.LinesRemoved != 0 {
		t.Errorf("lines = +%d/-%d, want 0/0 without a commit", result.LinesAdded, result.LinesRemoved)
	}
	if result.FilesChanged != 2 {
		t.Errorf("FilesChanged = %d, want 2 (state.filesWrite fallback)", result.FilesChanged)
	}
}
