package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// mergedIssues returns a mergedIssueFunc that reports the listed issues as merged.
func mergedIssues(nums ...int) mergedIssueFunc {
	set := map[int]bool{}
	for _, n := range nums {
		set[n] = true
	}
	return func(_ context.Context, n int) (bool, error) { return set[n], nil }
}

// newStackedFixture builds the GH-5527 scenario: predecessor branch
// pilot/GH-1 creates redact.go; successor pilot/GH-2 is cut from it and only
// appends to redact.go. The predecessor is then squash-merged to main, so the
// successor still carries the predecessor's original commit.
func newStackedFixture(t *testing.T) string {
	t.Helper()
	local := newFixtureRepo(t)

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-1")
	writeFixtureFile(t, local, "redact.go", "package fixture\n\nfunc A() {}\n")
	runFixtureGit(t, local, "add", "redact.go")
	runFixtureGit(t, local, "commit", "-m", "feat(typesafe): add redact helpers")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-1")

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-2")
	writeFixtureFile(t, local, "redact.go", "package fixture\n\nfunc A() {}\n\nfunc B() {}\n")
	runFixtureGit(t, local, "add", "redact.go")
	runFixtureGit(t, local, "commit", "-m", "feat(typesafe): extend redact helpers\n\nCloses #2")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-2")

	runFixtureGit(t, local, "checkout", "main")
	runFixtureGit(t, local, "merge", "--squash", "pilot/GH-1")
	runFixtureGit(t, local, "commit", "-m", "feat(typesafe): add redact helpers (#11)")
	runFixtureGit(t, local, "push", "origin", "main")
	return local
}

func TestRebaseStackedBranch_DropsSquashMergedPredecessor(t *testing.T) {
	local := newStackedFixture(t)
	ctx := context.Background()

	// Sanity: a plain merge of main into the successor reproduces the false
	// conflict (add/add on redact.go).
	attempt, cleanup, err := attemptLocalMerge(ctx, local, "pilot/GH-2", "main")
	defer cleanup()
	if err != nil {
		t.Fatalf("attemptLocalMerge: %v", err)
	}
	if !attempt.Conflicted() {
		t.Fatal("expected plain merge to report a false conflict on redact.go")
	}

	res, err := rebaseStackedBranch(ctx, local, "pilot/GH-2", "main", 2, nil)
	if err != nil {
		t.Fatalf("rebaseStackedBranch: %v", err)
	}
	if res == nil {
		t.Fatal("expected stacked rebase to run, got nil result")
	}
	if res.Inherited != 1 || res.Replayed != 1 {
		t.Fatalf("got inherited=%d replayed=%d, want 1/1", res.Inherited, res.Replayed)
	}

	runFixtureGit(t, local, "fetch", "origin")
	count := strings.TrimSpace(runFixtureGit(t, local, "rev-list", "--count", "origin/main..origin/pilot/GH-2"))
	if count != "1" {
		t.Fatalf("expected single-commit branch, got %s commits", count)
	}
	subject := strings.TrimSpace(runFixtureGit(t, local, "log", "-1", "--format=%s", "origin/pilot/GH-2"))
	if subject != "feat(typesafe): extend redact helpers" {
		t.Fatalf("unexpected tip subject %q", subject)
	}
	content := runFixtureGit(t, local, "show", "origin/pilot/GH-2:redact.go")
	if !strings.Contains(content, "func A()") || !strings.Contains(content, "func B()") {
		t.Fatalf("rebased redact.go missing expected content:\n%s", content)
	}
	// The rebased branch must now merge cleanly with main.
	attempt2, cleanup2, err := attemptLocalMerge(ctx, local, "pilot/GH-2", "main")
	defer cleanup2()
	if err != nil {
		t.Fatalf("attemptLocalMerge after rebase: %v", err)
	}
	if attempt2.Conflicted() {
		t.Fatalf("expected clean merge after rebase, got %v", attempt2.ConflictedFiles)
	}
}

func TestRebaseStackedBranch_NotStacked(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-3")
	writeFixtureFile(t, local, "c.txt", "c\n")
	runFixtureGit(t, local, "add", "c.txt")
	runFixtureGit(t, local, "commit", "-m", "feat: add c")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-3")

	runFixtureGit(t, local, "checkout", "main")
	writeFixtureFile(t, local, "d.txt", "d\n")
	runFixtureGit(t, local, "add", "d.txt")
	runFixtureGit(t, local, "commit", "-m", "feat: add d (#5)")
	runFixtureGit(t, local, "push", "origin", "main")

	res, err := rebaseStackedBranch(ctx, local, "pilot/GH-3", "main", 3, nil)
	if err != nil {
		t.Fatalf("rebaseStackedBranch: %v", err)
	}
	if res != nil {
		t.Fatalf("expected nil result for non-stacked branch, got %+v", res)
	}
}

func TestStackedCandidates(t *testing.T) {
	mainSubjects := map[string]bool{"feat: add a": true}
	mainCloses := map[int]bool{10: true, 20: true}
	merged := map[int]bool{18: true}

	branch := []stackedCommit{
		{SHA: "1", Parents: 1, Subject: "feat: add a (#7)"},                   // subject match
		{SHA: "2", Parents: 2, Subject: "Merge branch 'pilot/GH-10'"},         // merge: skipped
		{SHA: "3", Parents: 1, Subject: "feat: other", Body: "Closes #10"},    // trailer match
		{SHA: "4", Parents: 1, Subject: "feat: mine", Body: "Closes #20"},     // own issue: not a candidate
		{SHA: "5", Parents: 1, Subject: "feat: unrelated", Body: "Fixes #99"}, // no match
		{SHA: "6", Parents: 1, Subject: "feat: different words (GH-18)"},      // exact: merged PR for #18
		{SHA: "7", Parents: 1, Subject: "feat: pending (GH-19)"},              // #19 not merged
		{SHA: "8", Parents: 1, Subject: "feat: mine again (GH-20)"},           // own issue
	}
	cands := stackedCandidates(branch, mainSubjects, mainCloses, merged, 20)
	want := map[string]bool{"1": true, "3": true, "6": true}
	if len(cands) != len(want) {
		t.Fatalf("candidates = %v, want %v", cands, want)
	}
	for sha := range want {
		if !cands[sha] {
			t.Errorf("commit %s should be a candidate", sha)
		}
	}

	refs := stackedIssueRefs(branch, 20)
	if len(refs) != 2 || refs[0] != 18 || refs[1] != 19 {
		t.Fatalf("stackedIssueRefs = %v, want [18 19]", refs)
	}
}

func TestSubjectIssueRef(t *testing.T) {
	cases := map[string]int{
		"feat(x): y (GH-123)":   123,
		"feat(x): y (GH-7) ":    7,
		"feat(x): y":            0,
		"feat(x): (GH-5) and y": 0,
		"feat(x): y (GH-abc)":   0,
		"feat(x): y (#123)":     0,
		"fix: z (GH-9) (GH-10)": 10,
	}
	for in, want := range cases {
		if got := subjectIssueRef(in); got != want {
			t.Errorf("subjectIssueRef(%q) = %d, want %d", in, got, want)
		}
	}
}

// newEpicFixture mirrors the PR #5524 incident: pilot/GH-19 is cut from
// pilot/GH-18 and pilot/GH-20 from pilot/GH-19. Executor commits carry Claude's
// own subject plus " (GH-N)" and never a "Closes #N" trailer. Main squash-merges
// GH-18 then GH-19 under PR titles that differ from the branch subjects.
func newEpicFixture(t *testing.T) string {
	t.Helper()
	local := newFixtureRepo(t)

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-18")
	writeFixtureFile(t, local, "a.go", "package fixture\n\nfunc A() {}\n")
	runFixtureGit(t, local, "add", "a.go")
	runFixtureGit(t, local, "commit", "-m", "add redaction primitives (GH-18)")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-18")

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-19")
	writeFixtureFile(t, local, "a.go", "package fixture\n\nfunc A() {}\n\nfunc B() {}\n")
	runFixtureGit(t, local, "add", "a.go")
	runFixtureGit(t, local, "commit", "-m", "wire redaction into logger (GH-19)")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-19")

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-20")
	writeFixtureFile(t, local, "c.go", "package fixture\n\nfunc C() {}\n")
	runFixtureGit(t, local, "add", "c.go")
	runFixtureGit(t, local, "commit", "-m", "add typesafe gate (GH-20)")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-20")

	runFixtureGit(t, local, "checkout", "main")
	runFixtureGit(t, local, "merge", "--squash", "pilot/GH-18")
	runFixtureGit(t, local, "commit", "-m", "feat(typesafe): redaction primitives (#31)")
	// GH-19 was retargeted onto main after GH-18 merged, so its squash carries
	// only its own change.
	writeFixtureFile(t, local, "a.go", "package fixture\n\nfunc A() {}\n\nfunc B() {}\n")
	runFixtureGit(t, local, "add", "a.go")
	runFixtureGit(t, local, "commit", "-m", "feat(typesafe): logger redaction (#32)")
	runFixtureGit(t, local, "push", "origin", "main")
	return local
}

func TestRebaseStackedBranch_ExactGHSuffixDetection(t *testing.T) {
	local := newEpicFixture(t)
	ctx := context.Background()

	// Without the merged-PR lookup no signal fires (subjects differ, no
	// Closes trailer) — the pre-GH-5538 behaviour that left the conflict hold.
	res, err := rebaseStackedBranch(ctx, local, "pilot/GH-20", "main", 20, nil)
	if err != nil {
		t.Fatalf("rebaseStackedBranch (no lookup): %v", err)
	}
	if res != nil {
		t.Fatalf("expected nil result without merged lookup, got %+v", res)
	}

	res, err = rebaseStackedBranch(ctx, local, "pilot/GH-20", "main", 20, mergedIssues(18, 19))
	if err != nil {
		t.Fatalf("rebaseStackedBranch: %v", err)
	}
	if res == nil || res.Inherited != 2 || res.Replayed != 1 {
		t.Fatalf("got %+v, want inherited=2 replayed=1", res)
	}

	runFixtureGit(t, local, "fetch", "origin")
	if count := strings.TrimSpace(runFixtureGit(t, local, "rev-list", "--count", "origin/main..origin/pilot/GH-20")); count != "1" {
		t.Fatalf("expected single-commit branch, got %s commits", count)
	}
	if subj := strings.TrimSpace(runFixtureGit(t, local, "log", "-1", "--format=%s", "origin/pilot/GH-20")); subj != "add typesafe gate (GH-20)" {
		t.Fatalf("unexpected tip subject %q", subj)
	}
}

// A merged PR for issue N is only a nomination: if the branch commit's change
// is not actually on main it must be kept.
func TestRebaseStackedBranch_MergedIssueButContentNotOnBase(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-30")
	writeFixtureFile(t, local, "x.go", "package fixture\n\nfunc X() {}\n")
	runFixtureGit(t, local, "add", "x.go")
	runFixtureGit(t, local, "commit", "-m", "add X (GH-29)")
	writeFixtureFile(t, local, "y.go", "package fixture\n\nfunc Y() {}\n")
	runFixtureGit(t, local, "add", "y.go")
	runFixtureGit(t, local, "commit", "-m", "add Y (GH-30)")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-30")

	runFixtureGit(t, local, "checkout", "main")
	writeFixtureFile(t, local, "z.go", "package fixture\n")
	runFixtureGit(t, local, "add", "z.go")
	runFixtureGit(t, local, "commit", "-m", "unrelated (#40)")
	runFixtureGit(t, local, "push", "origin", "main")

	res, err := rebaseStackedBranch(ctx, local, "pilot/GH-30", "main", 30, mergedIssues(29))
	if err != nil {
		t.Fatalf("rebaseStackedBranch: %v", err)
	}
	if res != nil {
		t.Fatalf("commit whose content is not on main must not be dropped, got %+v", res)
	}
}

// GH-5538 defect 2: an own commit whose subject matches a main commit but whose
// content differs must be kept. Removing the content guard makes it inherited,
// so this test fails without it.
func TestRebaseStackedBranch_SharedSubjectDifferentContentKept(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	// Predecessor (genuinely inherited) and an own commit that shares its
	// subject with an unrelated main commit, plus a second own commit.
	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-50")
	writeFixtureFile(t, local, "pred.go", "package fixture\n\nfunc Pred() {}\n")
	runFixtureGit(t, local, "add", "pred.go")
	runFixtureGit(t, local, "commit", "-m", "add predecessor (GH-49)")
	writeFixtureFile(t, local, "own1.go", "package fixture\n\nfunc Own1() {}\n")
	runFixtureGit(t, local, "add", "own1.go")
	runFixtureGit(t, local, "commit", "-m", "chore: update docs")
	writeFixtureFile(t, local, "own2.go", "package fixture\n\nfunc Own2() {}\n")
	runFixtureGit(t, local, "add", "own2.go")
	runFixtureGit(t, local, "commit", "-m", "add own2 (GH-50)")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-50")

	runFixtureGit(t, local, "checkout", "main")
	writeFixtureFile(t, local, "pred.go", "package fixture\n\nfunc Pred() {}\n")
	runFixtureGit(t, local, "add", "pred.go")
	runFixtureGit(t, local, "commit", "-m", "predecessor landed (#41)")
	writeFixtureFile(t, local, "docs.md", "different content\n")
	runFixtureGit(t, local, "add", "docs.md")
	runFixtureGit(t, local, "commit", "-m", "chore: update docs (#42)")
	runFixtureGit(t, local, "push", "origin", "main")

	res, err := rebaseStackedBranch(ctx, local, "pilot/GH-50", "main", 50, mergedIssues(49))
	if err != nil {
		t.Fatalf("rebaseStackedBranch: %v", err)
	}
	if res == nil {
		t.Fatal("expected stacked rebase to run, got nil result")
	}
	if res.Inherited != 1 || res.Replayed != 2 {
		t.Fatalf("got inherited=%d replayed=%d, want 1/2 (shared-subject own commit must be kept)", res.Inherited, res.Replayed)
	}

	runFixtureGit(t, local, "fetch", "origin")
	if _, err := gitOutput(ctx, local, "cat-file", "-e", "origin/pilot/GH-50:own1.go"); err != nil {
		t.Fatalf("own1.go (shared-subject commit) was dropped from the branch: %v", err)
	}
	if count := strings.TrimSpace(runFixtureGit(t, local, "rev-list", "--count", "origin/main..origin/pilot/GH-50")); count != "2" {
		t.Fatalf("expected two own commits on branch, got %s", count)
	}
}

// The patch-id and empty-cherry-pick guards accept a commit whose identical
// change already landed on main even when paths later diverged.
func TestStackedContentGuard_PatchIDMatchesLandedCommit(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-60")
	writeFixtureFile(t, local, "f.txt", "one\n")
	runFixtureGit(t, local, "add", "f.txt")
	runFixtureGit(t, local, "commit", "-m", "add f (GH-59)")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-60")

	// Main lands the identical change, then moves the file on.
	runFixtureGit(t, local, "checkout", "main")
	writeFixtureFile(t, local, "f.txt", "one\n")
	runFixtureGit(t, local, "add", "f.txt")
	runFixtureGit(t, local, "commit", "-m", "landed f (#61)")
	writeFixtureFile(t, local, "f.txt", "one\ntwo\n")
	runFixtureGit(t, local, "add", "f.txt")
	runFixtureGit(t, local, "commit", "-m", "extend f (#62)")
	runFixtureGit(t, local, "push", "origin", "main")
	runFixtureGit(t, local, "fetch", "origin")

	sha := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "origin/pilot/GH-60"))
	mb := strings.TrimSpace(runFixtureGit(t, local, "merge-base", "origin/main", "origin/pilot/GH-60"))
	wt := t.TempDir() + "/wt"
	runFixtureGit(t, local, "worktree", "add", "--detach", wt, "origin/main")
	g := &stackedContentGuard{ctx: ctx, repoPath: local, worktreePath: wt, baseRef: "origin/main", mergeBase: mb}

	if same, err := g.treeMatchesBase(stackedCommit{SHA: sha, Parents: 1}); err != nil || same {
		t.Fatalf("tree guard should not match after main moved on: same=%v err=%v", same, err)
	}
	ok, err := g.alreadyOnBase(stackedCommit{SHA: sha, Parents: 1})
	if err != nil {
		t.Fatalf("alreadyOnBase: %v", err)
	}
	if !ok {
		t.Fatal("expected patch-id match against landed commit")
	}
}

// TestController_HandleMergeConflict_StackedRungRebasesEpicBranch proves the
// controller reaches the stacked rung: update-branch fails, the rung detects
// the inherited GH-18/GH-19 commits exactly (via merged PRs on pilot/GH-N
// branches), force-pushes a single-commit branch and advances to StageWaitingCI
// without closing the PR or escalating.
func TestController_HandleMergeConflict_StackedRungRebasesEpicBranch(t *testing.T) {
	local := newEpicFixture(t)
	ctx := context.Background()

	var prClosed, commentPosted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/77/update-branch" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"merge conflict between base and head"}`))
		case r.URL.Path == "/repos/owner/repo/pulls/77" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/77/comments" && r.Method == http.MethodPost:
			commentPosted = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.PRComment{ID: 1})
		case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
			var body struct {
				Variables map[string]interface{} `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			branch, _ := body.Variables["branch"].(string)
			nodes := []map[string]interface{}{}
			if branch == "pilot/GH-18" || branch == "pilot/GH-19" {
				nodes = append(nodes, map[string]interface{}{"number": 31, "state": "MERGED", "merged": true})
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"repository": map[string]interface{}{
						"pullRequests": map[string]interface{}{"nodes": nodes},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.MaxRebaseAttempts = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	c.mu.Lock()
	c.activePRs[77] = &PRState{
		PRNumber:    77,
		PRURL:       "https://github.com/owner/repo/pull/77",
		IssueNumber: 20,
		BranchName:  "pilot/GH-20",
		HeadSHA:     "deadbeef",
		Stage:       StageMerging,
		CreatedAt:   time.Now(),
	}
	c.mu.Unlock()

	if err := c.handleMergeConflict(ctx, c.activePRs[77]); err != nil {
		t.Fatalf("handleMergeConflict: %v", err)
	}

	pr, ok := c.GetPRState(77)
	if !ok {
		t.Fatal("PR 77 not found in activePRs")
	}
	if pr.Stage != StageWaitingCI {
		t.Fatalf("Stage = %s, want %s (stacked rung should rebase)", pr.Stage, StageWaitingCI)
	}
	if pr.RebaseAttempts != 1 {
		t.Fatalf("RebaseAttempts = %d, want 1", pr.RebaseAttempts)
	}
	if prClosed || commentPosted {
		t.Fatalf("PR must not be closed or escalated: closed=%v comment=%v", prClosed, commentPosted)
	}
	runFixtureGit(t, local, "fetch", "origin")
	if count := strings.TrimSpace(runFixtureGit(t, local, "rev-list", "--count", "origin/main..origin/pilot/GH-20")); count != "1" {
		t.Fatalf("expected single-commit branch after rung, got %s commits", count)
	}
}

func TestNormalizeSubject(t *testing.T) {
	cases := map[string]string{
		"feat(x): y (#123)":  "feat(x): y",
		"feat(x): y":         "feat(x): y",
		"  feat(x): y (#1) ": "feat(x): y",
	}
	for in, want := range cases {
		if got := normalizeSubject(in); got != want {
			t.Errorf("normalizeSubject(%q) = %q, want %q", in, got, want)
		}
	}
}
