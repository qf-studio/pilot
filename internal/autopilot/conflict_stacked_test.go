package autopilot

import (
	"context"
	"strings"
	"testing"
)

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

	res, err := rebaseStackedBranch(ctx, local, "pilot/GH-2", "main", 2)
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

	res, err := rebaseStackedBranch(ctx, local, "pilot/GH-3", "main", 3)
	if err != nil {
		t.Fatalf("rebaseStackedBranch: %v", err)
	}
	if res != nil {
		t.Fatalf("expected nil result for non-stacked branch, got %+v", res)
	}
}

func TestClassifyStackedCommits(t *testing.T) {
	mainSubjects := map[string]bool{"feat: add a": true}
	mainCloses := map[int]bool{10: true, 20: true}

	branch := []stackedCommit{
		{SHA: "1", Parents: 1, Subject: "feat: add a (#7)"},                   // subject match
		{SHA: "2", Parents: 2, Subject: "Merge branch 'pilot/GH-10'"},         // merge: skipped
		{SHA: "3", Parents: 1, Subject: "feat: other", Body: "Closes #10"},    // trailer match
		{SHA: "4", Parents: 1, Subject: "feat: mine", Body: "Closes #20"},     // own issue: not inherited
		{SHA: "5", Parents: 1, Subject: "feat: unrelated", Body: "Fixes #99"}, // no match
	}
	inherited, own := classifyStackedCommits(branch, mainSubjects, mainCloses, 20)
	if inherited != 2 {
		t.Fatalf("inherited = %d, want 2", inherited)
	}
	if len(own) != 2 || own[0].SHA != "4" || own[1].SHA != "5" {
		t.Fatalf("unexpected own commits: %+v", own)
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
