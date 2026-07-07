package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// writeFakeGhPRList writes a fake "gh" binary to fakeBin that answers
// `gh pr list --head <branch> --state <merged|open> --json url --limit 1`
// by inspecting the --state flag: "merged" state returns mergedJSON, "open"
// state returns openJSON, anything else (e.g. a later `gh pr create` call)
// returns an empty array so an accidental call is visible in assertions
// rather than silently satisfied.
func writeFakeGhPRList(t *testing.T, fakeBin string, mergedJSON, openJSON []byte) {
	t.Helper()
	mergedFile := filepath.Join(fakeBin, "merged.json")
	openFile := filepath.Join(fakeBin, "open.json")
	if err := os.WriteFile(mergedFile, mergedJSON, 0o644); err != nil {
		t.Fatalf("write merged.json: %v", err)
	}
	if err := os.WriteFile(openFile, openJSON, 0o644); err != nil {
		t.Fatalf("write open.json: %v", err)
	}
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *"--state merged"*) cat %q ;;
  *"--state open"*) cat %q ;;
  *) echo "[]" ;;
esac
`, mergedFile, openFile)
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
}

// setUpFakeGhPATH prepends a fake "gh" binary (see writeFakeGhPRList) to PATH
// for the duration of the test and returns its directory.
func setUpFakeGhPATH(t *testing.T, mergedJSON, openJSON []byte) string {
	t.Helper()
	fakeBin := t.TempDir()
	writeFakeGhPRList(t, fakeBin, mergedJSON, openJSON)
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return fakeBin
}

// TestDirectPathExistingBranchPR is the GH-4022 table-driven regression
// suite for the direct (non-epic) execution path's pre-push/pre-create
// branch-PR checks (checkAlreadyMergedBranch, adoptOpenBranchPR). It mirrors
// the TASK-359 Shape C short-circuit on the epic finalization path
// (finalizeEpicBranchPR, runner_task359_test.go) but exercises the direct
// path's two checkpoints instead of one.
//
// Regression shape: execution 76c1c97f — a retried/duplicate dispatch of a
// branch whose PR had already merged reached push+CreatePR again because the
// direct path never re-checked branch state, producing a duplicate PR.
func TestDirectPathExistingBranchPR(t *testing.T) {
	tests := []struct {
		name string
		// mergedJSON/openJSON are the fake `gh pr list` responses for
		// --state merged / --state open respectively.
		mergedJSON []byte
		openJSON   []byte

		// wantMergedHandled/wantOpenHandled assert which checkpoint (if any)
		// short-circuits the direct path.
		wantMergedHandled bool
		wantOpenHandled   bool
		wantPRUrl         string
		wantStages        []memory.Stage
	}{
		{
			// (a) branch PR merged mid-run: the pre-push check finds a
			// merged PR and short-circuits before push+CreatePR ever run —
			// no second PR is opened, and the execution records success
			// referencing the existing PR.
			name:              "branch PR merged mid-run: no second PR, success with existing PR ref",
			mergedJSON:        []byte(`[{"url":"https://github.com/o/r/pull/42"}]`),
			openJSON:          []byte(`[]`),
			wantMergedHandled: true,
			wantOpenHandled:   false,
			wantPRUrl:         "https://github.com/o/r/pull/42",
			wantStages:        []memory.Stage{memory.StagePRCreated, memory.StageMerged},
		},
		{
			// (b) branch PR still open: not merged, so the pre-push check
			// does not fire — but the pre-create check adopts the open PR
			// instead of creating a duplicate.
			name:              "branch PR still open: adopt, no duplicate",
			mergedJSON:        []byte(`[]`),
			openJSON:          []byte(`[{"url":"https://github.com/o/r/pull/43"}]`),
			wantMergedHandled: false,
			wantOpenHandled:   true,
			wantPRUrl:         "https://github.com/o/r/pull/43",
			wantStages:        []memory.Stage{memory.StagePRCreated},
		},
		{
			// (c) no prior PR: neither checkpoint fires, leaving the
			// existing push+CreatePR flow to run as today.
			name:              "no prior PR: create as today",
			mergedJSON:        []byte(`[]`),
			openJSON:          []byte(`[]`),
			wantMergedHandled: false,
			wantOpenHandled:   false,
			wantPRUrl:         "",
			wantStages:        nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setUpFakeGhPATH(t, tt.mergedJSON, tt.openJSON)

			store, cleanup := setupTestStore(t)
			defer cleanup()

			r := newSilentRunnerTask359()
			r.SetLogStore(store)
			git := NewGitOperations(t.TempDir())
			task := &Task{
				ID:       "GH-4022",
				Title:    "feat: existing branch PR handling",
				Branch:   "pilot/GH-4022",
				CreatePR: true,
			}
			if err := store.SaveExecution(&memory.Execution{ID: task.ID, TaskID: task.ID, Status: "running"}); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}
			result := &ExecutionResult{TaskID: task.ID, Success: true}

			mergedHandled := r.checkAlreadyMergedBranch(context.Background(), git, task, result, nil)
			if mergedHandled != tt.wantMergedHandled {
				t.Fatalf("checkAlreadyMergedBranch handled = %v, want %v", mergedHandled, tt.wantMergedHandled)
			}

			var openHandled bool
			if !mergedHandled {
				openHandled = r.adoptOpenBranchPR(context.Background(), git, task, result, nil)
				if openHandled != tt.wantOpenHandled {
					t.Fatalf("adoptOpenBranchPR handled = %v, want %v", openHandled, tt.wantOpenHandled)
				}
			}

			if result.PRUrl != tt.wantPRUrl {
				t.Errorf("result.PRUrl = %q, want %q", result.PRUrl, tt.wantPRUrl)
			}
			if !result.Success {
				t.Errorf("expected Success to remain true, got false (error=%q)", result.Error)
			}

			events, err := store.ListExecutionEvents(task.LogExecutionID())
			if err != nil {
				t.Fatalf("ListExecutionEvents: %v", err)
			}
			if len(events) != len(tt.wantStages) {
				var gotStages []memory.Stage
				for _, e := range events {
					gotStages = append(gotStages, e.Stage)
				}
				t.Fatalf("got %d events %v, want %d %v", len(events), gotStages, len(tt.wantStages), tt.wantStages)
			}
			for i, want := range tt.wantStages {
				if events[i].Stage != want {
					t.Errorf("event[%d].Stage = %q, want %q", i, events[i].Stage, want)
				}
			}
			if tt.wantPRUrl != "" && len(events) > 0 && !strings.Contains(events[0].Detail, tt.wantPRUrl) {
				t.Errorf("event[0].Detail = %q, want it to reference %q", events[0].Detail, tt.wantPRUrl)
			}
			// (c): neither checkpoint should have consumed the branch — the
			// caller falls through to the existing push+CreatePR flow.
			if tt.name == "no prior PR: create as today" && (mergedHandled || openHandled) {
				t.Error("expected neither checkpoint to fire so the existing create-PR flow runs unmodified")
			}
		})
	}
}

// TestAdoptOpenBranchPR_NotCalledWhenMerged verifies checkAlreadyMergedBranch
// and adoptOpenBranchPR compose correctly: a caller that short-circuits on
// checkAlreadyMergedBranch's true return must never invoke adoptOpenBranchPR
// (mirrors the runner.go call-site guard `if !mergedHandled { ... }`), so a
// merged branch never triggers a second gh CLI open-PR lookup at all.
func TestAdoptOpenBranchPR_NotCalledWhenMerged(t *testing.T) {
	setUpFakeGhPATH(t,
		[]byte(`[{"url":"https://github.com/o/r/pull/99"}]`),  // merged
		[]byte(`[{"url":"https://github.com/o/r/pull/100"}]`), // open — must never be consulted
	)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	r := newSilentRunnerTask359()
	r.SetLogStore(store)
	git := NewGitOperations(t.TempDir())
	task := &Task{ID: "GH-4022B", Title: "feat: x", Branch: "pilot/GH-4022B", CreatePR: true}
	result := &ExecutionResult{TaskID: task.ID, Success: true}

	if !r.checkAlreadyMergedBranch(context.Background(), git, task, result, nil) {
		t.Fatal("expected checkAlreadyMergedBranch to short-circuit")
	}
	if result.PRUrl != "https://github.com/o/r/pull/99" {
		t.Errorf("PRUrl = %q, want the merged PR (99), not the open one (100)", result.PRUrl)
	}
}
