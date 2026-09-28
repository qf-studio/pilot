package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestSplitIssueSections_HeadingDetection is table-driven over H2 and H3
// headings, case-insensitivity, and content-before-first-heading handling
// (GH-5466 acceptance: "Table-driven test for section detection (H2 and H3
// headings, case-insensitive)").
func TestSplitIssueSections_HeadingDetection(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []issueSection
	}{
		{
			name: "H2 headings split into sections",
			body: "## Problem\n\nsomething broke\n\n## Change\n\nedit `foo.go`\n",
			want: []issueSection{
				{Heading: "Problem", Body: "\n\nsomething broke\n\n"},
				{Heading: "Change", Body: "\n\nedit `foo.go`\n"},
			},
		},
		{
			name: "H3 headings split into sections",
			body: "### Context\n\nbackground\n\n### Fix\n\nedit `bar.go`\n",
			want: []issueSection{
				{Heading: "Context", Body: "\n\nbackground\n\n"},
				{Heading: "Fix", Body: "\n\nedit `bar.go`\n"},
			},
		},
		{
			name: "mixed H2 and H3 headings",
			body: "## Problem\n\nx\n\n### Acceptance\n\ny\n",
			want: []issueSection{
				{Heading: "Problem", Body: "\n\nx\n\n"},
				{Heading: "Acceptance", Body: "\n\ny\n"},
			},
		},
		{
			name: "heading case is preserved verbatim (case-insensitivity is the caller's job)",
			body: "## cHaNgE\n\nedit `foo.go`\n",
			want: []issueSection{
				{Heading: "cHaNgE", Body: "\n\nedit `foo.go`\n"},
			},
		},
		{
			name: "content before the first heading is dropped",
			body: "no heading yet\n\n## Change\n\nedit `foo.go`\n",
			want: []issueSection{
				{Heading: "Change", Body: "\n\nedit `foo.go`\n"},
			},
		},
		{
			name: "no headings at all yields no sections",
			body: "just plain text, no markdown headings\n",
			want: nil,
		},
		{
			name: "H1 heading is not split on (only H2/H3 supported)",
			body: "# Title\n\n## Change\n\nedit `foo.go`\n",
			want: []issueSection{
				{Heading: "Change", Body: "\n\nedit `foo.go`\n"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitIssueSections(tt.body)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitIssueSections(%q) = %#v, want %#v", tt.body, got, tt.want)
			}
		})
	}
}

// TestIsDiffCoverageSection covers the case-insensitive allow-list directly.
func TestIsDiffCoverageSection(t *testing.T) {
	tests := []struct {
		heading string
		want    bool
	}{
		{"Change", true},
		{"changes", true},
		{"IMPLEMENTATION", true},
		{"Fix", true},
		{"Acceptance", true},
		{"  Change  ", true},
		{"Context", false},
		{"Problem", false},
		{"Refs", false},
		{"Out of scope", false},
		{"References", false},
	}
	for _, tt := range tests {
		if got := isDiffCoverageSection(tt.heading); got != tt.want {
			t.Errorf("isDiffCoverageSection(%q) = %v, want %v", tt.heading, got, tt.want)
		}
	}
}

// TestExtractDiffCoveragePaths_SectionScoping is the core section-scoping
// acceptance case: paths named under Change/Acceptance are collected; paths
// named only under Context/Problem/Refs/Out of scope are ignored.
func TestExtractDiffCoveragePaths_SectionScoping(t *testing.T) {
	body := "## Problem\n\nSee `internal/ignored_context.go` for background.\n\n" +
		"## Change\n\nEdit `internal/executor/foo.go` and `internal/executor/bar.go`.\n\n" +
		"## Out of scope\n\n`internal/executor/not_in_scope.go` is a follow-up, not this change.\n\n" +
		"## Acceptance\n\nUnit test covers `internal/executor/foo.go`.\n\n" +
		"## Refs\n\nSee `internal/executor/ref_only.go` for prior art.\n"

	got := ExtractDiffCoveragePaths(body)
	want := []string{"internal/executor/foo.go", "internal/executor/bar.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExtractDiffCoveragePaths() = %#v, want %#v", got, want)
	}
}

func TestExtractDiffCoveragePaths_EmptyBody(t *testing.T) {
	if got := ExtractDiffCoveragePaths(""); got != nil {
		t.Errorf("ExtractDiffCoveragePaths(\"\") = %#v, want nil", got)
	}
}

// TestCheckDiffCoverage covers the GH-5466 acceptance scenario directly:
// issue body naming two existing files under Change, PR diff touching one
// -> the other is reported; a path under Out of scope is not reported; a
// path that does not exist on base is not reported (base-presence owns
// that class).
func TestCheckDiffCoverage(t *testing.T) {
	body := "## Change\n\nEdit `internal/executor/touched.go` and `internal/executor/untouched.go`.\n\n" +
		"## Out of scope\n\n`internal/executor/out_of_scope.go` is a follow-up.\n\n" +
		"## Change\n\nAlso edit `internal/executor/missing_on_base.go`.\n"

	changedFiles := []string{"internal/executor/touched.go"}

	// existsOnBase: everything exists on base except missing_on_base.go —
	// simulates a path base-presence would already flag as absent from the
	// base branch, which this gate must not double-report.
	existsOnBase := func(path string) bool {
		return path != "internal/executor/missing_on_base.go"
	}

	got := CheckDiffCoverage(body, changedFiles, existsOnBase)
	want := []string{"internal/executor/untouched.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CheckDiffCoverage() = %#v, want %#v", got, want)
	}
}

func TestCheckDiffCoverage_AllTouched_NoneReported(t *testing.T) {
	body := "## Change\n\nEdit `internal/executor/a.go` and `internal/executor/b.go`.\n"
	changedFiles := []string{"internal/executor/a.go", "internal/executor/b.go"}
	got := CheckDiffCoverage(body, changedFiles, func(string) bool { return true })
	if len(got) != 0 {
		t.Errorf("CheckDiffCoverage() = %#v, want empty", got)
	}
}

func TestCheckDiffCoverage_NilExistsOnBase_ReportsEverythingUncovered(t *testing.T) {
	body := "## Change\n\nEdit `internal/executor/a.go`.\n"
	got := CheckDiffCoverage(body, nil, nil)
	want := []string{"internal/executor/a.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CheckDiffCoverage() = %#v, want %#v", got, want)
	}
}

func TestDiffCoverageNotVerifiedReason(t *testing.T) {
	got := DiffCoverageNotVerifiedReason("internal/executor/foo.go")
	want := "internal/executor/foo.go is named in the issue but the PR does not modify it"
	if got != want {
		t.Errorf("DiffCoverageNotVerifiedReason() = %q, want %q", got, want)
	}
}

// --- Git-backed wiring (runDiffCoverageCheck / appendAcceptanceEvidence) ---

// runGit runs a git command in dir, failing the test on error.
func runGitDiffCoverage(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFileDiffCoverage(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// TestRunDiffCoverageCheck_GitBacked exercises the real git.go wiring
// (ChangedFilesAgainstOrigin / FileExistsAtRef) end to end: an issue body
// naming two files that exist on the base branch under "## Change", a
// worktree branch that only touches one of them.
func TestRunDiffCoverageCheck_GitBacked(t *testing.T) {
	dir, _ := initTestRepo(t)
	ctx := context.Background()

	base := "main"
	runGitDiffCoverage(t, dir, "branch", "-m", base)

	writeFileDiffCoverage(t, dir, "internal/executor/touched.go", "package executor\n")
	writeFileDiffCoverage(t, dir, "internal/executor/untouched.go", "package executor\n")
	runGitDiffCoverage(t, dir, "add", ".")
	runGitDiffCoverage(t, dir, "commit", "-m", "seed both files on base")

	runGitDiffCoverage(t, dir, "checkout", "-b", "pilot/GH-5466-test")
	writeFileDiffCoverage(t, dir, "internal/executor/touched.go", "package executor\n\nfunc touched() {}\n")
	runGitDiffCoverage(t, dir, "add", ".")
	runGitDiffCoverage(t, dir, "commit", "-m", "only touch one of the two named files")

	task := &Task{
		ID:          "GH-5466",
		BaseBranch:  base,
		Description: "## Change\n\nEdit `internal/executor/touched.go` and `internal/executor/untouched.go`.\n",
	}

	results := runDiffCoverageCheck(ctx, nil, task, dir)
	if len(results) != 1 {
		t.Fatalf("runDiffCoverageCheck() = %#v, want exactly 1 result", results)
	}
	if results[0].Item.Text != "internal/executor/untouched.go" {
		t.Errorf("reported path = %q, want %q", results[0].Item.Text, "internal/executor/untouched.go")
	}
	wantReason := "internal/executor/untouched.go is named in the issue but the PR does not modify it"
	if results[0].NotVerifiedReason != wantReason {
		t.Errorf("reason = %q, want %q", results[0].NotVerifiedReason, wantReason)
	}
}

// TestRunDiffCoverageCheck_PathNotOnBase_NotReported confirms base-presence
// scoping end to end: a path named in the issue that never existed on the
// base branch at all is not reported by this gate.
func TestRunDiffCoverageCheck_PathNotOnBase_NotReported(t *testing.T) {
	dir, _ := initTestRepo(t)
	ctx := context.Background()

	base := "main"
	runGitDiffCoverage(t, dir, "branch", "-m", base)

	runGitDiffCoverage(t, dir, "checkout", "-b", "pilot/GH-5466-test2")
	writeFileDiffCoverage(t, dir, "internal/executor/added.go", "package executor\n")
	runGitDiffCoverage(t, dir, "add", ".")
	runGitDiffCoverage(t, dir, "commit", "-m", "unrelated commit")

	task := &Task{
		ID:          "GH-5466",
		BaseBranch:  base,
		Description: "## Change\n\nEdit `internal/executor/never_existed.go`.\n",
	}

	results := runDiffCoverageCheck(ctx, nil, task, dir)
	if len(results) != 0 {
		t.Errorf("runDiffCoverageCheck() = %#v, want empty (path absent from base is base-presence's class)", results)
	}
}

// TestRunDiffCoverageCheck_NoCandidatePaths_NoGitShellout confirms the
// git-free short-circuit: an issue body with no backtick-quoted path in a
// scoped section never touches git at all, so a non-git workDir is safe.
func TestRunDiffCoverageCheck_NoCandidatePaths_NoGitShellout(t *testing.T) {
	task := &Task{ID: "GH-1", Description: "## Problem\n\nno paths named anywhere useful\n"}
	results := runDiffCoverageCheck(context.Background(), nil, task, t.TempDir())
	if results != nil {
		t.Errorf("runDiffCoverageCheck() = %#v, want nil", results)
	}
}

// TestRunDiffCoverageCheck_LiveBodyNarrowsScope is the GH-5469 acceptance
// scenario: the queue-time snapshot names a.go and b.go under "## Change",
// but the issue's live body (as fetchIssueState would return it) has since
// been edited to name only a.go. The PR only touches a.go, so nothing
// should be reported — checking the frozen snapshot instead of the live
// body would wrongly report b.go as named-but-untouched, reintroducing the
// class of bug GH-5193 already fixed for base-presence.
func TestRunDiffCoverageCheck_LiveBodyNarrowsScope(t *testing.T) {
	dir, _ := initTestRepo(t)
	ctx := context.Background()

	base := "main"
	runGitDiffCoverage(t, dir, "branch", "-m", base)

	writeFileDiffCoverage(t, dir, "internal/executor/a.go", "package executor\n")
	writeFileDiffCoverage(t, dir, "internal/executor/b.go", "package executor\n")
	runGitDiffCoverage(t, dir, "add", ".")
	runGitDiffCoverage(t, dir, "commit", "-m", "seed both files on base")

	runGitDiffCoverage(t, dir, "checkout", "-b", "pilot/GH-5469-test")
	writeFileDiffCoverage(t, dir, "internal/executor/a.go", "package executor\n\nfunc a() {}\n")
	runGitDiffCoverage(t, dir, "add", ".")
	runGitDiffCoverage(t, dir, "commit", "-m", "only touch a.go, matching the corrected live body")

	liveBody := "## Change\n\nEdit `internal/executor/a.go`.\n"
	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Body: liveBody}, nil
	})

	task := &Task{
		ID:          "GH-5469",
		BaseBranch:  base,
		Description: "## Change\n\nEdit `internal/executor/a.go` and `internal/executor/b.go`.\n",
	}

	results := runDiffCoverageCheck(ctx, nil, task, dir)
	if len(results) != 0 {
		t.Errorf("runDiffCoverageCheck() = %#v, want empty (live body only names a.go, which the PR touches)", results)
	}
}

// TestRunDiffCoverageCheck_FetchFailure_FallsBackToSnapshot confirms the
// fail-open contract: when fetchIssueState errors, the check falls back to
// the queue-time snapshot exactly as before GH-5469 — the snapshot names
// a.go and b.go, the PR only touches a.go, so b.go is still reported.
func TestRunDiffCoverageCheck_FetchFailure_FallsBackToSnapshot(t *testing.T) {
	dir, _ := initTestRepo(t)
	ctx := context.Background()

	base := "main"
	runGitDiffCoverage(t, dir, "branch", "-m", base)

	writeFileDiffCoverage(t, dir, "internal/executor/a.go", "package executor\n")
	writeFileDiffCoverage(t, dir, "internal/executor/b.go", "package executor\n")
	runGitDiffCoverage(t, dir, "add", ".")
	runGitDiffCoverage(t, dir, "commit", "-m", "seed both files on base")

	runGitDiffCoverage(t, dir, "checkout", "-b", "pilot/GH-5469-test2")
	writeFileDiffCoverage(t, dir, "internal/executor/a.go", "package executor\n\nfunc a() {}\n")
	runGitDiffCoverage(t, dir, "add", ".")
	runGitDiffCoverage(t, dir, "commit", "-m", "only touch a.go")

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{}, errors.New("GitHub API: 503 Service Unavailable")
	})

	task := &Task{
		ID:          "GH-5469",
		BaseBranch:  base,
		Description: "## Change\n\nEdit `internal/executor/a.go` and `internal/executor/b.go`.\n",
	}

	results := runDiffCoverageCheck(ctx, nil, task, dir)
	if len(results) != 1 {
		t.Fatalf("runDiffCoverageCheck() = %#v, want exactly 1 result", results)
	}
	if results[0].Item.Text != "internal/executor/b.go" {
		t.Errorf("reported path = %q, want %q", results[0].Item.Text, "internal/executor/b.go")
	}
}

// TestAppendAcceptanceEvidence_DiffCoverage_MergedIntoNotVerified confirms
// the documented rendering choice (GH-5466 acceptance: "merged into Not
// verified"): a diff-coverage finding appears under the PR body's existing
// "## Not verified" heading, without a distinct new section.
func TestAppendAcceptanceEvidence_DiffCoverage_MergedIntoNotVerified(t *testing.T) {
	dir, _ := initTestRepo(t)
	ctx := context.Background()
	base := "main"
	runGitDiffCoverage(t, dir, "branch", "-m", base)

	writeFileDiffCoverage(t, dir, "internal/executor/touched.go", "package executor\n")
	writeFileDiffCoverage(t, dir, "internal/executor/untouched.go", "package executor\n")
	runGitDiffCoverage(t, dir, "add", ".")
	runGitDiffCoverage(t, dir, "commit", "-m", "seed both files on base")

	runGitDiffCoverage(t, dir, "checkout", "-b", "pilot/GH-5466-render")
	writeFileDiffCoverage(t, dir, "internal/executor/touched.go", "package executor\n\nfunc touched() {}\n")
	runGitDiffCoverage(t, dir, "add", ".")
	runGitDiffCoverage(t, dir, "commit", "-m", "only touch one of the two named files")

	task := &Task{
		ID:          "GH-5466",
		BaseBranch:  base,
		Description: "## Change\n\nEdit `internal/executor/touched.go` and `internal/executor/untouched.go`.\n",
	}
	r := &Runner{config: DefaultBackendConfig()}
	prBody := "## Summary\n\nsome PR body"

	got := r.appendAcceptanceEvidence(ctx, task, dir, prBody)

	if !strings.Contains(got, "## Not verified") {
		t.Fatalf("expected a merged \"## Not verified\" section, got %q", got)
	}
	if strings.Count(got, "## Not verified") != 1 {
		t.Errorf("expected exactly one \"## Not verified\" heading (merged, not duplicated), got %q", got)
	}
	if !strings.Contains(got, "internal/executor/untouched.go is named in the issue but the PR does not modify it") {
		t.Errorf("expected the diff-coverage reason in the rendered body, got %q", got)
	}
}
