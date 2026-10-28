package autopilot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// gatherFakeGitHub is a PostMergeGitHub (with both optional capabilities)
// that serves canned data and records the arguments it was called with.
type gatherFakeGitHub struct {
	pr         *github.PullRequest
	prErr      error
	issues     map[int]*github.Issue
	closing    []int
	closingErr error
	diff       string
	diffErr    error

	gotPR        int
	gotIssue     int
	closingCalls int
	diffCalls    []string // merge shas
}

func (f *gatherFakeGitHub) GetPullRequest(_ context.Context, owner, repo string, number int) (*github.PullRequest, error) {
	f.gotPR = number
	if f.prErr != nil {
		return nil, f.prErr
	}
	return f.pr, nil
}

func (f *gatherFakeGitHub) GetIssue(_ context.Context, owner, repo string, number int) (*github.Issue, error) {
	f.gotIssue = number
	iss, ok := f.issues[number]
	if !ok {
		return nil, fmt.Errorf("issue %d not found in %s/%s", number, owner, repo)
	}
	return iss, nil
}

func (f *gatherFakeGitHub) ClosingIssueNumbers(_ context.Context, owner, repo string, prNumber int) ([]int, error) {
	f.closingCalls++
	return f.closing, f.closingErr
}

func (f *gatherFakeGitHub) MergeCommitDiff(_ context.Context, owner, repo, mergeSHA string) (string, error) {
	f.diffCalls = append(f.diffCalls, mergeSHA)
	return f.diff, f.diffErr
}

// gatherCoreGitHub satisfies only the two required methods, so the optional
// capabilities are absent.
type gatherCoreGitHub struct{ inner *gatherFakeGitHub }

func (g gatherCoreGitHub) GetPullRequest(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error) {
	return g.inner.GetPullRequest(ctx, owner, repo, number)
}

func (g gatherCoreGitHub) GetIssue(ctx context.Context, owner, repo string, number int) (*github.Issue, error) {
	return g.inner.GetIssue(ctx, owner, repo, number)
}

func newGatherFake(branch string) *gatherFakeGitHub {
	return &gatherFakeGitHub{
		pr: &github.PullRequest{
			Number: 5902, State: "closed", Merged: true, MergeCommitSHA: "mergesha9",
			Head: github.PRRef{Ref: branch},
			Body: "## Summary\nbody of the PR",
		},
		issues: map[int]*github.Issue{
			5895: {Number: 5895, Title: "closing issue", Body: "closing issue body"},
			77:   {Number: 77, Title: "branch issue", Body: "branch issue body"},
			12:   {Number: 12, Title: "explicit issue", Body: "explicit issue body"},
		},
		diff: "diff --git a/x.go b/x.go\n+compare\n",
	}
}

func TestGatherPostMergeReviewInput_IssueResolution(t *testing.T) {
	tests := []struct {
		name        string
		branch      string
		closing     []int
		closingErr  error
		issueArg    int
		wantIssue   int
		wantTitle   string
		wantClosing int
		wantErr     string
	}{
		{name: "explicit issue skips resolution", branch: "pilot/GH-77", closing: []int{5895}, issueArg: 12, wantIssue: 12, wantTitle: "explicit issue", wantClosing: 0},
		{name: "first closing issue wins over the branch", branch: "pilot/GH-77", closing: []int{5895, 12}, wantIssue: 5895, wantTitle: "closing issue", wantClosing: 1},
		{name: "lookup error falls through to the branch", branch: "pilot/GH-77", closingErr: errors.New("graphql down"), wantIssue: 77, wantTitle: "branch issue", wantClosing: 1},
		{name: "no closing issue and a non-GH branch asks for --issue", branch: "feature/cleanup", wantClosing: 1, wantErr: "--issue"},
		{name: "lookup error is reported with the --issue hint", branch: "feature/cleanup", closingErr: errors.New("graphql down"), wantClosing: 1, wantErr: "graphql down"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newGatherFake(tt.branch)
			fake.closing, fake.closingErr = tt.closing, tt.closingErr

			got, err := GatherPostMergeReviewInput(context.Background(), fake, "o", "r", "", 5902, tt.issueArg)
			if fake.closingCalls != tt.wantClosing {
				t.Errorf("closing-issue lookups = %d, want %d", fake.closingCalls, tt.wantClosing)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "--issue") {
					t.Fatalf("err = %v, want one naming %q and --issue", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("gather: %v", err)
			}
			if got.IssueNumber != tt.wantIssue || fake.gotIssue != tt.wantIssue {
				t.Errorf("issue = %d (fetched %d), want %d", got.IssueNumber, fake.gotIssue, tt.wantIssue)
			}
			if got.Input.IssueTitle != tt.wantTitle {
				t.Errorf("IssueTitle = %q, want %q", got.Input.IssueTitle, tt.wantTitle)
			}
			if got.Input.PRNumber != 5902 || fake.gotPR != 5902 {
				t.Errorf("PRNumber = %d (fetched %d), want 5902", got.Input.PRNumber, fake.gotPR)
			}
			if got.Input.PRBody != fake.pr.Body {
				t.Errorf("PRBody = %q, want %q", got.Input.PRBody, fake.pr.Body)
			}
		})
	}
}

// The branch-name fallback is its own test: dropping it must fail here.
func TestGatherPostMergeReviewInput_BranchNameFallback(t *testing.T) {
	fake := newGatherFake("pilot/GH-77")
	// No closing issue references: only the head branch names the issue.
	got, err := GatherPostMergeReviewInput(context.Background(), fake, "o", "r", "", 5902, 0)
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if got.IssueNumber != 77 {
		t.Fatalf("IssueNumber = %d, want 77 from the branch pilot/GH-77", got.IssueNumber)
	}
	if fake.gotIssue != 77 {
		t.Fatalf("fetched issue %d, want 77", fake.gotIssue)
	}
	if got.Input.IssueBody != "branch issue body" {
		t.Fatalf("IssueBody = %q, want the branch issue's body", got.Input.IssueBody)
	}
}

func TestGatherPostMergeReviewInput_NoOptionalCapabilities(t *testing.T) {
	inner := newGatherFake("pilot/GH-77")
	inner.closing = []int{5895} // must not be consulted: the client cannot list closing issues
	inner.pr.MergeCommitSHA = "HEAD"
	got, err := GatherPostMergeReviewInput(context.Background(), gatherCoreGitHub{inner}, "o", "r", gatherTempRepo(t), 5902, 0)
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if got.IssueNumber != 77 || inner.closingCalls != 0 {
		t.Errorf("IssueNumber = %d, closing lookups = %d, want 77 from the branch and no lookups", got.IssueNumber, inner.closingCalls)
	}

	_, err = GatherPostMergeReviewInput(context.Background(), gatherCoreGitHub{newGatherFake("pilot/GH-77")}, "o", "r", "", 5902, 0)
	if err == nil || !strings.Contains(err.Error(), "no project path") {
		t.Errorf("err = %v, want the no-diff-source error when neither a checkout nor compare is available", err)
	}
}

func TestGatherPostMergeReviewInput_PRState(t *testing.T) {
	tests := []struct {
		name    string
		pr      *github.PullRequest
		prErr   error
		wantErr string
	}{
		{name: "open PR", pr: &github.PullRequest{State: "open", MergeCommitSHA: "testmerge"}, wantErr: "still open"},
		{name: "merged PR without a merge sha", pr: &github.PullRequest{State: "closed", Merged: true}, wantErr: "no merge commit sha"},
		{name: "fetch error", prErr: errors.New("404 not found"), wantErr: "fetch PR #5902: 404 not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newGatherFake("pilot/GH-77")
			fake.pr, fake.prErr = tt.pr, tt.prErr
			_, err := GatherPostMergeReviewInput(context.Background(), fake, "o", "r", "", 5902, 12)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
			if len(fake.diffCalls) != 0 {
				t.Errorf("diff fetched for an unreviewable PR: %v", fake.diffCalls)
			}
		})
	}
}

func TestGatherPostMergeReviewInput_DiffSource(t *testing.T) {
	repoDir := gatherTempRepo(t)

	t.Run("local checkout when the commit is present", func(t *testing.T) {
		fake := newGatherFake("pilot/GH-77")
		fake.pr.MergeCommitSHA = "HEAD"
		got, err := GatherPostMergeReviewInput(context.Background(), fake, "o", "r", repoDir, 5902, 12)
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		if got.DiffSource != PostMergeDiffSourceLocal {
			t.Errorf("DiffSource = %q, want %q", got.DiffSource, PostMergeDiffSourceLocal)
		}
		if !strings.Contains(got.Input.Diff, "+second line") {
			t.Errorf("Diff = %q, want the local first-parent diff", got.Input.Diff)
		}
		if len(fake.diffCalls) != 0 {
			t.Errorf("GitHub compare called although the checkout had the commit: %v", fake.diffCalls)
		}
		if got.Input.ProjectPath != repoDir || got.Input.MergeCommitSHA != "HEAD" {
			t.Errorf("reviewer checkout input = (%q, %q), want (%q, HEAD)", got.Input.ProjectPath, got.Input.MergeCommitSHA, repoDir)
		}
	})

	t.Run("github compare without a project path", func(t *testing.T) {
		fake := newGatherFake("pilot/GH-77")
		got, err := GatherPostMergeReviewInput(context.Background(), fake, "o", "r", "", 5902, 12)
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		if !strings.HasPrefix(got.DiffSource, PostMergeDiffSourceGitHubCompare) {
			t.Errorf("DiffSource = %q, want it to start with %q", got.DiffSource, PostMergeDiffSourceGitHubCompare)
		}
		if got.Input.Diff != fake.diff {
			t.Errorf("Diff = %q, want the compare diff %q", got.Input.Diff, fake.diff)
		}
		if len(fake.diffCalls) != 1 || fake.diffCalls[0] != "mergesha9" {
			t.Errorf("compare called with %v, want [mergesha9]", fake.diffCalls)
		}
		if got.Input.ProjectPath != "" || got.Input.MergeCommitSHA != "" {
			t.Errorf("a compare diff must keep the diff-only review, got checkout input (%q, %q)", got.Input.ProjectPath, got.Input.MergeCommitSHA)
		}
	})

	t.Run("github compare when the checkout lacks the commit", func(t *testing.T) {
		fake := newGatherFake("pilot/GH-77")
		fake.pr.MergeCommitSHA = "0123456789abcdef0123456789abcdef01234567"
		got, err := GatherPostMergeReviewInput(context.Background(), fake, "o", "r", repoDir, 5902, 12)
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		if !strings.HasPrefix(got.DiffSource, PostMergeDiffSourceGitHubCompare) || !strings.Contains(got.DiffSource, "local checkout unavailable") {
			t.Errorf("DiffSource = %q, want a compare source naming why the checkout was skipped", got.DiffSource)
		}
	})

	t.Run("both sources failing reports both", func(t *testing.T) {
		fake := newGatherFake("pilot/GH-77")
		fake.diffErr = errors.New("compare 404")
		_, err := GatherPostMergeReviewInput(context.Background(), fake, "o", "r", "", 5902, 12)
		if err == nil || !strings.Contains(err.Error(), "compare 404") || !strings.Contains(err.Error(), "no project path") {
			t.Fatalf("err = %v, want both failures", err)
		}
	})
}

// gatherTempRepo builds a two-commit repo whose HEAD adds "second line".
func gatherTempRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("first line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("-c", "commit.gpgsign=false", "commit", "-q", "-m", "first")
	if err := os.WriteFile(file, []byte("first line\nsecond line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-c", "commit.gpgsign=false", "commit", "-q", "-am", "second")
	return dir
}

func TestPostMergeGitHubClient_ExtraReads(t *testing.T) {
	var paths, accepts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		accepts = append(accepts, r.Header.Get("Accept"))
		switch r.URL.Path {
		case "/graphql":
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"closingIssuesReferences":{"nodes":[` +
				`{"number":3,"repository":{"nameWithOwner":"other/repo"}},` +
				`{"number":5895,"repository":{"nameWithOwner":"Owner/Repo"}}]}}}}}`))
		case "/repos/owner/repo/commits/mergesha9":
			_, _ = w.Write([]byte(`{"parents":[{"sha":"parentsha1"},{"sha":"parentsha2"}]}`))
		case "/repos/owner/repo/compare/parentsha1...mergesha9":
			_, _ = w.Write([]byte("diff --git a/x b/x\n+raw\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewPostMergeGitHubClient(nil, ghadapter.NewClientWithBaseURL("test-github-token", srv.URL))

	nums, err := c.ClosingIssueNumbers(context.Background(), "owner", "repo", 5902)
	if err != nil || len(nums) != 1 || nums[0] != 5895 {
		t.Fatalf("ClosingIssueNumbers = %v, %v; want [5895] (other-repo issue dropped)", nums, err)
	}
	diff, err := c.MergeCommitDiff(context.Background(), "owner", "repo", "mergesha9")
	if err != nil || diff != "diff --git a/x b/x\n+raw\n" {
		t.Fatalf("MergeCommitDiff = %q, %v", diff, err)
	}
	if last := accepts[len(accepts)-1]; last != "application/vnd.github.diff" {
		t.Errorf("compare Accept = %q, want the diff media type", last)
	}
	if want := "/repos/owner/repo/compare/parentsha1...mergesha9"; paths[len(paths)-1] != want {
		t.Errorf("last request = %q, want %q (first parent, not the second)", paths[len(paths)-1], want)
	}
}
