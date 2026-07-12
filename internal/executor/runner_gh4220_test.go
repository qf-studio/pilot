package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeGhPRCreate writes a fake "gh" binary to fakeBin that answers
// `gh pr list ...` with an empty array (no merged/open PR short-circuits) and
// `gh pr create --title <title> ...` by recording the title it was invoked
// with to capturedTitleFile and returning a fake PR URL. Mirrors
// writeFakeGhPRList (runner_gh4022_test.go) but also handles `pr create` so
// the epic finalize path can run all the way through CreatePR.
func writeFakeGhPRCreate(t *testing.T, fakeBin, capturedTitleFile string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *"pr list"*) echo "[]" ;;
  *"pr create"*)
    prev=""
    for arg in "$@"; do
      if [ "$prev" = "--title" ]; then
        printf '%%s' "$arg" > %q
      fi
      prev="$arg"
    done
    echo "https://github.com/o/r/pull/777"
    ;;
  *) echo "[]" ;;
esac
`, capturedTitleFile)
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
}

// setUpFakeGhPRCreatePATH prepends a fake "gh" binary (writeFakeGhPRCreate) to
// PATH for the duration of the test and returns the file the fake binary
// writes the captured `--title` argument to.
func setUpFakeGhPRCreatePATH(t *testing.T) string {
	t.Helper()
	fakeBin := t.TempDir()
	capturedTitleFile := filepath.Join(fakeBin, "captured-title.txt")
	writeFakeGhPRCreate(t, fakeBin, capturedTitleFile)
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return capturedTitleFile
}

// initRepoWithRemoteAndFeatureBranch creates a local repo with an "origin"
// remote (a bare repo), a base commit on main pushed to that remote, and a
// feature branch (matching task.Branch) carrying one additional commit —
// exactly the state finalizeEpicBranchPR expects for the push+CreatePR path
// (non-empty branch vs base, remote reachable).
func initRepoWithRemoteAndFeatureBranch(t *testing.T, branch string) string {
	t.Helper()
	localDir, remoteDir := setupTestRepoWithRemote(t)
	t.Cleanup(func() { _ = os.RemoveAll(remoteDir) })
	t.Cleanup(func() { _ = os.RemoveAll(localDir) })

	runGit(t, localDir, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(localDir, "epic-work.txt"), []byte("work\n"), 0644); err != nil {
		t.Fatalf("write epic-work.txt: %v", err)
	}
	runGit(t, localDir, "add", "epic-work.txt")
	runGit(t, localDir, "commit", "-m", "epic work")

	return localDir
}

// TestFinalizeEpicBranchPR_AutoPrefixesNonConventionalTitle is the GH-4220 (b)
// regression guard: finalizeEpicBranchPR must route the epic parent's raw
// issue title through the same normalizeTitle (autoPrefixTitle /
// inferConventionalPrefix) machinery as the direct path before calling
// git.CreatePR, instead of passing "<task.ID>: <task.Title>" straight through.
//
// Regression shape (TASK-401 / GH-4211 live repro): a raw issue title like
// "GH-4211: Throughput histograms record zero on the live path" is never a
// conventional commit, so validatePRTitle (git.go:177) rejected it
// deterministically, leaving the epic parent open + failed — and the
// subsequent re-poll re-implemented the child's already-shipped work from
// scratch (PR #4213 vs #4214, same fix twice).
func TestFinalizeEpicBranchPR_AutoPrefixesNonConventionalTitle(t *testing.T) {
	tests := []struct {
		name       string
		title      string
		labels     []string
		wantPrefix string // conventional-commit type expected in the final title
	}{
		{
			name:       "raw GH-4211-shaped title, no labels: diff-heuristic prefix",
			title:      "GH-4211: Throughput histograms record zero on the live path",
			labels:     nil,
			wantPrefix: "", // asserted generically below (any valid conventional type)
		},
		{
			name:       "raw title with bug label: label-derived fix prefix",
			title:      "Executor crashes on nil epic plan",
			labels:     []string{"bug"},
			wantPrefix: "fix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capturedTitleFile := setUpFakeGhPRCreatePATH(t)

			branch := "pilot/GH-4220-epic"
			dir := initRepoWithRemoteAndFeatureBranch(t, branch)

			r := newSilentRunnerTask359()
			result := &ExecutionResult{TaskID: "GH-4220", Success: true, IsEpic: true}
			task := &Task{
				ID:          "GH-4220",
				Title:       tt.title,
				Labels:      tt.labels,
				Description: "d",
				Branch:      branch,
				BaseBranch:  "main",
				CreatePR:    true,
			}

			r.finalizeEpicBranchPR(context.Background(), task, NewGitOperations(dir), result, nil)

			if !result.Success {
				t.Fatalf("expected Success=true, got false (error=%q)", result.Error)
			}
			if result.PRUrl != "https://github.com/o/r/pull/777" {
				t.Errorf("PRUrl = %q, want fake PR URL", result.PRUrl)
			}

			capturedTitleBytes, err := os.ReadFile(capturedTitleFile)
			if err != nil {
				t.Fatalf("gh pr create was not invoked with --title: %v", err)
			}
			capturedTitle := string(capturedTitleBytes)

			if capturedTitle == fmt.Sprintf("%s: %s", task.ID, tt.title) {
				t.Errorf("title was passed through unmodified (%q) — expected auto-prefixing", capturedTitle)
			}
			if err := validatePRTitle(capturedTitle); err != nil {
				t.Errorf("captured title %q failed validatePRTitle: %v", capturedTitle, err)
			}
			if !strings.HasPrefix(capturedTitle, task.ID+": ") {
				t.Errorf("captured title %q should still carry the %q auto-close prefix", capturedTitle, task.ID+": ")
			}
			if tt.wantPrefix != "" && !strings.Contains(capturedTitle, tt.wantPrefix+":") {
				t.Errorf("captured title %q, want it to contain conventional type %q", capturedTitle, tt.wantPrefix)
			}
		})
	}
}

// TestFinalizeEpicBranchPR_TitleNormalizeFailureIsRecoverable verifies that if
// normalizeTitle cannot produce a valid conventional title (empty title),
// finalizeEpicBranchPR fails loud with Success=false and never calls
// git.CreatePR (Shape A — mirrors the direct path's title-rejection contract,
// runner.go ~4001).
func TestFinalizeEpicBranchPR_TitleNormalizeFailureIsRecoverable(t *testing.T) {
	capturedTitleFile := setUpFakeGhPRCreatePATH(t)

	branch := "pilot/GH-4220-empty-title"
	dir := initRepoWithRemoteAndFeatureBranch(t, branch)

	r := newSilentRunnerTask359()
	result := &ExecutionResult{TaskID: "GH-4220", Success: true, IsEpic: true}
	task := &Task{
		ID:          "GH-4220",
		Title:       "   ",
		Description: "d",
		Branch:      branch,
		BaseBranch:  "main",
		CreatePR:    true,
	}

	r.finalizeEpicBranchPR(context.Background(), task, NewGitOperations(dir), result, nil)

	if result.Success {
		t.Error("expected Success=false when the epic title cannot be normalized")
	}
	if result.PRUrl != "" {
		t.Errorf("expected no PR URL, got %q", result.PRUrl)
	}
	if _, err := os.Stat(capturedTitleFile); err == nil {
		t.Error("gh pr create must not be invoked when title normalization fails")
	}
}

// TestFinalizeEpicBranchPR_TitleRejectionEscalatesOnSecondFailure is GH-4220
// (e): finalizeEpicBranchPR must feed titleErr into the same GH-2363
// record→escalate tracker the direct path uses (recordTitleRejection,
// title_rejection.go), not just fail loud once. Before this fix, an epic
// parent stuck on a title normalizeTitle can't fix would fail Success=false
// every poll forever with no escalation — never tripping the stop-retry
// guidance comment/labels the direct path relies on to stop the loop.
func TestFinalizeEpicBranchPR_TitleRejectionEscalatesOnSecondFailure(t *testing.T) {
	setUpFakeGhPRCreatePATH(t)

	branch := "pilot/GH-4220-escalate"
	dir := initRepoWithRemoteAndFeatureBranch(t, branch)

	r := newSilentRunnerTask359()
	r.titleRejections = newTitleRejectionTracker()
	task := &Task{
		ID:          "GH-4220",
		Title:       "   ", // never normalizes — see normalizeTitle empty-title guard
		Description: "d",
		Branch:      branch,
		BaseBranch:  "main",
		CreatePR:    true,
	}

	result1 := &ExecutionResult{TaskID: task.ID, Success: true, IsEpic: true}
	r.finalizeEpicBranchPR(context.Background(), task, NewGitOperations(dir), result1, nil)
	if result1.Success {
		t.Fatal("expected Success=false on first title-normalize failure")
	}
	if result1.TitleRejected {
		t.Error("first rejection must not escalate yet")
	}

	result2 := &ExecutionResult{TaskID: task.ID, Success: true, IsEpic: true}
	r.finalizeEpicBranchPR(context.Background(), task, NewGitOperations(dir), result2, nil)
	if result2.Success {
		t.Fatal("expected Success=false on second title-normalize failure")
	}
	if !result2.TitleRejected {
		t.Error("second consecutive title-normalize failure must escalate (TitleRejected=true)")
	}
}
