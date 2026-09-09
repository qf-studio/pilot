package executor

import (
	"context"
	"testing"
)

// TestExtractPRNumberFromURL covers the local mirror of
// adapters/github.ExtractPRNumber used by isBorrowedOriginPR (GH-5400).
func TestExtractPRNumberFromURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantNum int
		wantOK  bool
	}{
		{"pull url", "https://github.com/o/r/pull/5384", 5384, true},
		{"pulls url", "https://github.com/o/r/pulls/42", 42, true},
		{"no number", "https://github.com/o/r/pull/", 0, false},
		{"empty", "", 0, false},
		{"garbage", "not a url", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			num, ok := extractPRNumberFromURL(tt.url)
			if ok != tt.wantOK || num != tt.wantNum {
				t.Errorf("extractPRNumberFromURL(%q) = (%d, %v), want (%d, %v)", tt.url, num, ok, tt.wantNum, tt.wantOK)
			}
		})
	}
}

// TestIsBorrowedOriginPR covers the GH-5400 discriminator: a fix-issue task
// (task.FromPR > 0) reuses its origin PR's branch, so a merged/open PR found
// on that branch is only evidence of the origin's already-known work when its
// number matches FromPR — any other PR number is a genuinely new/retried PR
// for this task and must still be trusted, exactly like before this fix.
func TestIsBorrowedOriginPR(t *testing.T) {
	tests := []struct {
		name  string
		task  *Task
		prURL string
		want  bool
	}{
		{
			name:  "fix-issue task, PR matches FromPR (origin PR) — borrowed",
			task:  &Task{ID: "GH-5385", Branch: "pilot/GH-5379", FromPR: 5384},
			prURL: "https://github.com/o/r/pull/5384",
			want:  true,
		},
		{
			name:  "fix-issue task, PR differs from FromPR — this task's own PR",
			task:  &Task{ID: "GH-5385", Branch: "pilot/GH-5379", FromPR: 5384},
			prURL: "https://github.com/o/r/pull/5401",
			want:  false,
		},
		{
			name:  "ordinary task, no FromPR — never borrowed",
			task:  &Task{ID: "GH-100", Branch: "pilot/GH-100"},
			prURL: "https://github.com/o/r/pull/100",
			want:  false,
		},
		{
			name:  "nil task",
			task:  nil,
			prURL: "https://github.com/o/r/pull/1",
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBorrowedOriginPR(tt.task, tt.prURL); got != tt.want {
				t.Errorf("isBorrowedOriginPR() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCheckAlreadyMergedBranch_SkipsBorrowedOriginPR is the GH-5400
// regression test for the incident behind fix issue #5385: a fix issue's
// task reuses its origin PR's branch (resolveAutopilotFixBranch), and that
// origin PR (task.FromPR) already merged before this task ever pushed a
// commit of its own. Pre-fix, checkAlreadyMergedBranch treated the origin
// PR's merge as this task's own completed deliverable and short-circuited on
// the very first dispatch tick — reporting PR #5384 as #5385's own PR and
// letting the fix issue close "done" citing someone else's merge, with no PR
// of its own. It must now fall through so the caller proceeds to push+create
// a genuine PR for this task instead.
func TestCheckAlreadyMergedBranch_SkipsBorrowedOriginPR(t *testing.T) {
	setUpFakeGhPATH(t,
		[]byte(`[{"url":"https://github.com/o/r/pull/5384"}]`), // merged: the origin PR
		[]byte(`[]`),
	)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	r := newSilentRunnerTask359()
	r.SetLogStore(store)
	git := NewGitOperations(t.TempDir())
	task := &Task{
		ID:       "GH-5385",
		Title:    "fix(ci): resolve post-merge CI failure from PR #5384",
		Branch:   "pilot/GH-5379", // borrowed from the origin PR's branch
		CreatePR: true,
		FromPR:   5384, // the origin PR this fix issue was spawned to continue
	}
	result := &ExecutionResult{TaskID: task.ID, Success: true}

	if handled := r.checkAlreadyMergedBranch(context.Background(), git, task, result, nil); handled {
		t.Fatalf("checkAlreadyMergedBranch short-circuited on the origin PR (FromPR=%d) — expected it to fall through so the fix issue's own work still gets pushed and its own PR created", task.FromPR)
	}
	if result.PRUrl != "" {
		t.Errorf("result.PRUrl = %q, want empty — the origin PR must not be reported as this task's deliverable", result.PRUrl)
	}
}

// TestCheckAlreadyMergedBranch_TrustsGenuinelyDifferentMergedPR verifies the
// FromPR guard does not overreach: when the merged PR found on the branch is
// NOT the origin PR (e.g. a retried dispatch of this exact fix-issue task
// that already pushed and merged its own PR), the pre-GH-5400 short-circuit
// behavior is unchanged.
func TestCheckAlreadyMergedBranch_TrustsGenuinelyDifferentMergedPR(t *testing.T) {
	setUpFakeGhPATH(t,
		[]byte(`[{"url":"https://github.com/o/r/pull/5401"}]`), // merged: this task's own (retried) PR
		[]byte(`[]`),
	)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	r := newSilentRunnerTask359()
	r.SetLogStore(store)
	git := NewGitOperations(t.TempDir())
	task := &Task{
		ID:       "GH-5385",
		Title:    "fix(ci): resolve post-merge CI failure from PR #5384",
		Branch:   "pilot/GH-5379",
		CreatePR: true,
		FromPR:   5384,
	}
	result := &ExecutionResult{TaskID: task.ID, Success: true}

	if handled := r.checkAlreadyMergedBranch(context.Background(), git, task, result, nil); !handled {
		t.Fatal("expected checkAlreadyMergedBranch to short-circuit on a genuinely different merged PR")
	}
	if result.PRUrl != "https://github.com/o/r/pull/5401" {
		t.Errorf("result.PRUrl = %q, want the task's own merged PR (5401)", result.PRUrl)
	}
}

// TestAdoptOpenBranchPR_SkipsBorrowedOriginPR mirrors
// TestCheckAlreadyMergedBranch_SkipsBorrowedOriginPR for the open-PR adoption
// checkpoint (GH-5400): an open PR that is still task.FromPR must not be
// adopted as this task's own PR.
func TestAdoptOpenBranchPR_SkipsBorrowedOriginPR(t *testing.T) {
	setUpFakeGhPATH(t,
		[]byte(`[]`),
		[]byte(`[{"url":"https://github.com/o/r/pull/5384"}]`), // open: the origin PR
	)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	r := newSilentRunnerTask359()
	r.SetLogStore(store)
	git := NewGitOperations(t.TempDir())
	task := &Task{
		ID:       "GH-5385",
		Branch:   "pilot/GH-5379",
		CreatePR: true,
		FromPR:   5384,
	}
	result := &ExecutionResult{TaskID: task.ID, Success: true}

	if handled := r.adoptOpenBranchPR(context.Background(), git, task, result, nil); handled {
		t.Fatal("adoptOpenBranchPR adopted the origin PR — expected it to skip and let the fix issue create its own PR")
	}
	if result.PRUrl != "" {
		t.Errorf("result.PRUrl = %q, want empty", result.PRUrl)
	}
}
