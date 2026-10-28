package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/executor"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// rmFakeGitHub serves one merged PR and records every comment call.
type rmFakeGitHub struct {
	branch   string
	closing  []int
	diff     string
	comments []string // bodies passed to AddComment
	gotIssue int
}

func (f *rmFakeGitHub) GetPullRequest(_ context.Context, owner, repo string, number int) (*githubSDK.PullRequest, error) {
	return &githubSDK.PullRequest{
		Number: number, State: "closed", Merged: true, MergeCommitSHA: "abcdef0123456789",
		Head: githubSDK.PRRef{Ref: f.branch}, Body: "pr body for " + owner + "/" + repo,
	}, nil
}

func (f *rmFakeGitHub) GetIssue(_ context.Context, owner, repo string, number int) (*githubSDK.Issue, error) {
	f.gotIssue = number
	return &githubSDK.Issue{Number: number, Title: fmt.Sprintf("issue %d title", number), Body: "issue body"}, nil
}

func (f *rmFakeGitHub) ClosingIssueNumbers(_ context.Context, owner, repo string, prNumber int) ([]int, error) {
	return f.closing, nil
}

func (f *rmFakeGitHub) MergeCommitDiff(_ context.Context, owner, repo, mergeSHA string) (string, error) {
	return f.diff, nil
}

func (f *rmFakeGitHub) AddComment(_ context.Context, owner, repo string, number int, body string) (*githubSDK.Comment, error) {
	f.comments = append(f.comments, fmt.Sprintf("%s/%s#%d: %s", owner, repo, number, body))
	return &githubSDK.Comment{ID: 4242}, nil
}

// rmFakeReviewer derives its review from the input it is given, so a test
// fails when the command hands it the wrong inputs.
type rmFakeReviewer struct {
	got     []executor.PostMergeReviewInput
	verdict executor.PostMergeVerdict
	defects []executor.PostMergeDefect
	notes   string
}

func (f *rmFakeReviewer) Review(_ context.Context, in executor.PostMergeReviewInput) (*executor.PostMergeReview, error) {
	f.got = append(f.got, in)
	return &executor.PostMergeReview{
		Verdict:     f.verdict,
		Defects:     f.defects,
		Notes:       f.notes + " [saw " + in.IssueTitle + "]",
		DiffBytes:   len(in.Diff),
		DiffFiles:   strings.Count(in.Diff, "diff --git"),
		DiffTrimmed: false,
	}, nil
}

// runRM executes the command with fakes installed and returns its stdout.
func runRM(t *testing.T, gh *rmFakeGitHub, rev *rmFakeReviewer, args ...string) (string, error) {
	t.Helper()
	old := reviewMergeEnvFactory
	t.Cleanup(func() { reviewMergeEnvFactory = old })
	var gotRepo string
	reviewMergeEnvFactory = func(repo string) (*reviewMergeEnv, error) {
		gotRepo = repo
		return &reviewMergeEnv{GH: gh, Reviewer: rev, Model: "test-model"}, nil
	}
	cmd := newReviewMergeCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	if err == nil && gotRepo == "" {
		t.Error("environment factory never called with the --repo value")
	}
	return out.String(), err
}

func newRMFakes() (*rmFakeGitHub, *rmFakeReviewer) {
	return &rmFakeGitHub{
			branch: "pilot/GH-5895",
			diff:   "diff --git a/a.go b/a.go\n+x\ndiff --git a/b.go b/b.go\n+y\n",
		}, &rmFakeReviewer{
			verdict: executor.PostMergeApproveWithNotes,
			defects: []executor.PostMergeDefect{{Location: "internal/foo/bar.go:42", Claim: "binds to org_id", Evidence: "server sends organization_id"}},
			notes:   "looks fine",
		}
}

func TestReviewMergeCommand_PrintsVerdict(t *testing.T) {
	gh, rev := newRMFakes()
	out, err := runRM(t, gh, rev, "--repo", "qf-studio/pilot", "--pr", "5902")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	for _, want := range []string{
		"VERDICT: APPROVE-WITH-NOTES",
		"Defects (1):",
		"internal/foo/bar.go:42 — binds to org_id",
		"Evidence: server sends organization_id",
		"looks fine [saw issue 5895 title]",
		fmt.Sprintf("Diff: %d bytes, 2 files, trimmed: no", len(gh.diff)),
		"Diff source: github compare",
		"Model: test-model",
		"Duration:",
		"Posted: no (--post not given)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if len(rev.got) != 1 {
		t.Fatalf("reviewer calls = %d, want 1", len(rev.got))
	}
	in := rev.got[0]
	if in.PRNumber != 5902 || in.Diff != gh.diff || in.PRBody != "pr body for qf-studio/pilot" || in.IssueBody != "issue body" {
		t.Errorf("reviewer input = %+v, want the PR 5902 inputs gathered from GitHub", in)
	}
	if gh.gotIssue != 5895 {
		t.Errorf("issue fetched = %d, want 5895 from the branch pilot/GH-5895", gh.gotIssue)
	}
}

func TestReviewMergeCommand_NoDefects(t *testing.T) {
	gh, rev := newRMFakes()
	rev.defects = nil
	out, err := runRM(t, gh, rev, "--repo", "o/r", "--pr", "7")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if !strings.Contains(out, "Defects: none") {
		t.Errorf("output missing the empty defect list:\n%s", out)
	}
}

func TestReviewMergeCommand_JSONShape(t *testing.T) {
	gh, rev := newRMFakes()
	out, err := runRM(t, gh, rev, "--repo", "qf-studio/pilot", "--pr", "5902", "--issue", "12", "--json")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	for _, key := range []string{"verdict", "defects", "notes", "diff_bytes", "diff_files", "diff_trimmed", "model", "duration_ms",
		"input_diff_bytes", "input_issue_bytes", "input_pr_body_bytes", "diff_source", "issue", "pr"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON missing key %q:\n%s", key, out)
		}
	}
	if got["verdict"] != "APPROVE-WITH-NOTES" || got["model"] != "test-model" || got["issue"] != float64(12) {
		t.Errorf("verdict/model/issue = %v/%v/%v", got["verdict"], got["model"], got["issue"])
	}
	if got["diff_bytes"] != float64(len(gh.diff)) || got["input_diff_bytes"] != float64(len(gh.diff)) || got["diff_files"] != float64(2) || got["diff_trimmed"] != false {
		t.Errorf("diff sizes = %v/%v/%v/%v, want %d/%d/2/false", got["diff_bytes"], got["input_diff_bytes"], got["diff_files"], got["diff_trimmed"], len(gh.diff), len(gh.diff))
	}
	defects, _ := got["defects"].([]any)
	if len(defects) != 1 {
		t.Fatalf("defects = %v, want 1", got["defects"])
	}
	d, _ := defects[0].(map[string]any)
	if d["file"] != "internal/foo/bar.go" || d["line"] != "42" || d["claim"] != "binds to org_id" {
		t.Errorf("defect = %v, want file, line and claim split out", d)
	}
	if got["posted"] != false {
		t.Errorf("posted = %v, want false", got["posted"])
	}
	if gh.gotIssue != 12 {
		t.Errorf("--issue 12 not used: fetched %d", gh.gotIssue)
	}
}

func TestReviewMergeCommand_NoPostByDefault(t *testing.T) {
	gh, rev := newRMFakes()
	if _, err := runRM(t, gh, rev, "--repo", "qf-studio/pilot", "--pr", "5902"); err != nil {
		t.Fatalf("command: %v", err)
	}
	if len(gh.comments) != 0 {
		t.Fatalf("comments posted without --post: %v", gh.comments)
	}
	if _, err := runRM(t, gh, rev, "--repo", "qf-studio/pilot", "--pr", "5902", "--json"); err != nil {
		t.Fatalf("command: %v", err)
	}
	if len(gh.comments) != 0 {
		t.Fatalf("comments posted without --post (--json): %v", gh.comments)
	}
}

func TestReviewMergeCommand_PostUsesControllerComment(t *testing.T) {
	gh, rev := newRMFakes()
	out, err := runRM(t, gh, rev, "--repo", "qf-studio/pilot", "--pr", "5902", "--post")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if len(gh.comments) != 1 {
		t.Fatalf("comments = %v, want exactly one", gh.comments)
	}
	want := "qf-studio/pilot#5902: " + autopilot.BuildPostMergeReviewComment(&executor.PostMergeReview{
		Verdict:   rev.verdict,
		Defects:   rev.defects,
		Notes:     "looks fine [saw issue 5895 title]",
		DiffBytes: len(gh.diff),
		DiffFiles: 2,
	})
	if gh.comments[0] != want {
		t.Errorf("comment =\n%s\nwant the controller's comment\n%s", gh.comments[0], want)
	}
	if !strings.Contains(out, "Posted: yes (comment 4242)") {
		t.Errorf("output does not report the post:\n%s", out)
	}
}

func TestReviewMergeCommand_PostSkipsUnknownVerdict(t *testing.T) {
	gh, rev := newRMFakes()
	rev.verdict = executor.PostMergeUnknown
	out, err := runRM(t, gh, rev, "--repo", "o/r", "--pr", "7", "--post")
	if err == nil || !strings.Contains(err.Error(), "verdict unknown") {
		t.Fatalf("err = %v, want the contract-broken error", err)
	}
	if len(gh.comments) != 0 {
		t.Errorf("unknown verdict posted: %v", gh.comments)
	}
	if !strings.Contains(out, "VERDICT: unknown") || !strings.Contains(out, "Posted: no (verdict unusable)") {
		t.Errorf("output should still show the run:\n%s", out)
	}
}

func TestReviewMergeCommand_MissingIssueNamesFlag(t *testing.T) {
	gh, rev := newRMFakes()
	gh.branch = "feature/cleanup" // no GH-N, and the fake lists no closing issue
	_, err := runRM(t, gh, rev, "--repo", "o/r", "--pr", "7")
	if err == nil || !strings.Contains(err.Error(), "--issue") {
		t.Fatalf("err = %v, want one naming --issue", err)
	}
	if len(rev.got) != 0 {
		t.Errorf("reviewer ran without an issue: %v", rev.got)
	}
}

func TestReviewMergeCommand_ClosingIssueBeatsBranch(t *testing.T) {
	gh, rev := newRMFakes()
	gh.closing = []int{99, 100}
	if _, err := runRM(t, gh, rev, "--repo", "o/r", "--pr", "7"); err != nil {
		t.Fatalf("command: %v", err)
	}
	if gh.gotIssue != 99 {
		t.Errorf("issue = %d, want the first closing reference 99", gh.gotIssue)
	}
}

func TestReviewMergeCommand_FlagValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"missing repo", []string{"--pr", "5"}, "--repo must be owner/name"},
		{"bad repo", []string{"--repo", "noslash", "--pr", "5"}, "--repo must be owner/name"},
		{"missing pr", []string{"--repo", "o/r"}, "--pr is required"},
		{"negative issue", []string{"--repo", "o/r", "--pr", "5", "--issue", "-1"}, "--issue must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh, rev := newRMFakes()
			_, err := runRM(t, gh, rev, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
			if len(rev.got) != 0 {
				t.Errorf("reviewer ran despite bad flags")
			}
		})
	}
}

func TestReviewMergeCommand_TerminalSafeOutput(t *testing.T) {
	gh, rev := newRMFakes()
	rev.notes = "ok\x1b[31mRED\x1b[0m"
	rev.defects = []executor.PostMergeDefect{{Location: "a.go:1", Claim: "bad\x1b]0;title\x07claim"}}
	out, err := runRM(t, gh, rev, "--repo", "o/r", "--pr", "7")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("output carries control characters: %q", out)
	}
}

func TestSplitDefect(t *testing.T) {
	tests := []struct {
		loc, file, line string
	}{
		{"internal/a.go:42", "internal/a.go", "42"},
		{"internal/a.go:42-50", "internal/a.go", "42-50"},
		{"internal/a.go", "internal/a.go", ""},
		{"C:\\x\\a.go", "C:\\x\\a.go", ""},
		{"a.go:", "a.go:", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		got := splitDefect(executor.PostMergeDefect{Location: tt.loc, Claim: "c"})
		if got.File != tt.file || got.Line != tt.line || got.Claim != "c" {
			t.Errorf("splitDefect(%q) = %+v, want file %q line %q", tt.loc, got, tt.file, tt.line)
		}
	}
}
