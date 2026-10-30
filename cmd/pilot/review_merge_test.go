package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

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
	// notVerified is returned as PostMergeReview.NotVerified (GH-5927).
	notVerified []string
	// rawOutput, filesRead and checkout are returned as the review's raw text and
	// checkout counters (GH-5946).
	rawOutput string
	filesRead int
	checkout  bool
	// two-pass stats returned on the review, and the value --passes set (GH-5977)
	passes          int
	filesReadByPass []int
	defectsByPass   []int
	durationByPass  []time.Duration
	passFailed      []bool
	overlap         int
	setPasses       []int
}

// SetPasses records the --passes override the command applied.
func (f *rmFakeReviewer) SetPasses(n int) { f.setPasses = append(f.setPasses, n) }

func (f *rmFakeReviewer) Review(_ context.Context, in executor.PostMergeReviewInput) (*executor.PostMergeReview, error) {
	f.got = append(f.got, in)
	return &executor.PostMergeReview{
		Verdict:     f.verdict,
		Defects:     f.defects,
		NotVerified: f.notVerified,
		Notes:       f.notes + " [saw " + in.IssueTitle + "]",
		DiffBytes:   len(in.Diff),
		DiffFiles:   strings.Count(in.Diff, "diff --git"),
		DiffTrimmed: false,
		RawOutput:   f.rawOutput,
		FilesRead:   f.filesRead,
		Checkout:    f.checkout,

		Passes:          f.passes,
		FilesReadByPass: f.filesReadByPass,
		DefectsByPass:   f.defectsByPass,
		DurationByPass:  f.durationByPass,
		PassFailed:      f.passFailed,
		Overlap:         f.overlap,
	}, nil
}

// runRM executes the command with fakes installed and returns its stdout.
func runRM(t *testing.T, gh *rmFakeGitHub, rev *rmFakeReviewer, args ...string) (string, error) {
	t.Helper()
	out, _, err := runRMStreams(t, gh, rev, args...)
	return out, err
}

// runRMStreams is runRM that also returns stderr.
func runRMStreams(t *testing.T, gh *rmFakeGitHub, rev *rmFakeReviewer, args ...string) (stdout, stderr string, err error) {
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
	err = cmd.ExecuteContext(context.Background())
	if err == nil && gotRepo == "" {
		t.Error("environment factory never called with the --repo value")
	}
	return out.String(), errOut.String(), err
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
		"input_diff_bytes", "input_issue_bytes", "input_pr_body_bytes", "diff_source", "issue", "pr", "files_read", "checkout"} {
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

// TestReviewMergeJSONSeverityFields pins the --json fields severity and
// not_verified (GH-5927). The second case is the shape of the PR#5921
// verdict: a blocking defect plus a valid second, minor defect, with the
// claims the reviewer could not check listed apart from the defects.
func TestReviewMergeJSONSeverityFields(t *testing.T) {
	tests := []struct {
		name            string
		defects         []executor.PostMergeDefect
		notVerified     []string
		wantSeverities  []string // per defect; "" means the key must be absent
		wantNotVerified []string
	}{
		{
			name:            "no defects and nothing unverified emit an empty not_verified array",
			wantSeverities:  nil,
			wantNotVerified: []string{},
		},
		{
			name: "blocking then minor defect keep their own severity",
			defects: []executor.PostMergeDefect{
				{Severity: executor.PostMergeSeverityBlocking, Location: "internal/autopilot/post_merge_review.go:171", Claim: "reviews the wrong issue", Evidence: "reads PRState.IssueNumber"},
				{Severity: executor.PostMergeSeverityMinor, Location: "internal/autopilot/feedback_loop.go:450", Claim: "title omits the issue", Evidence: "title names only the PR"},
			},
			notVerified:     []string{"CI result of the follow-up", "behaviour on a squash merge"},
			wantSeverities:  []string{"blocking", "minor"},
			wantNotVerified: []string{"CI result of the follow-up", "behaviour on a squash merge"},
		},
		{
			name: "untagged severity is omitted from the defect",
			defects: []executor.PostMergeDefect{
				{Location: "a.go:1", Claim: "c", Evidence: "e"},
			},
			wantSeverities:  []string{""},
			wantNotVerified: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh, rev := newRMFakes()
			rev.defects, rev.notVerified = tt.defects, tt.notVerified

			out, err := runRM(t, gh, rev, "--repo", "qf-studio/pilot", "--pr", "5921", "--issue", "5917", "--json")
			if err != nil {
				t.Fatalf("command: %v", err)
			}
			var got struct {
				Defects     []map[string]any `json:"defects"`
				NotVerified []string         `json:"not_verified"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("output is not JSON: %v\n%s", err, out)
			}
			if !strings.Contains(out, `"not_verified"`) {
				t.Fatalf("JSON lacks the not_verified key:\n%s", out)
			}
			if got.NotVerified == nil || len(got.NotVerified) != len(tt.wantNotVerified) {
				t.Fatalf("not_verified = %#v, want %#v", got.NotVerified, tt.wantNotVerified)
			}
			for i, want := range tt.wantNotVerified {
				if got.NotVerified[i] != want {
					t.Errorf("not_verified[%d] = %q, want %q", i, got.NotVerified[i], want)
				}
			}
			if len(got.Defects) != len(tt.wantSeverities) {
				t.Fatalf("defects = %d, want %d:\n%s", len(got.Defects), len(tt.wantSeverities), out)
			}
			for i, want := range tt.wantSeverities {
				sev, present := got.Defects[i]["severity"]
				switch {
				case want == "" && present:
					t.Errorf("defect %d has severity %v, want the key absent", i, sev)
				case want != "" && sev != want:
					t.Errorf("defect %d severity = %v, want %q", i, sev, want)
				}
			}
		})
	}
}

func TestReviewMergeUnknownKeepsRaw(t *testing.T) {
	const raw = "I reviewed the diff and it looks fine, no verdict line though."
	tests := []struct {
		name string
		args []string
	}{
		{"text", nil},
		{"json", []string{"--json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh, rev := newRMFakes()
			rev.verdict = executor.PostMergeUnknown
			rev.rawOutput = raw
			rev.filesRead, rev.checkout = 7, true
			args := append([]string{"--repo", "o/r", "--pr", "7"}, tt.args...)
			stdout, stderr, err := runRMStreams(t, gh, rev, args...)
			if err == nil || !strings.Contains(err.Error(), "verdict unknown") {
				t.Fatalf("err = %v, want the contract-broken error (non-zero exit)", err)
			}
			if !strings.Contains(stderr, "verdict unknown") || !strings.Contains(stderr, raw) {
				t.Errorf("stderr should carry %q and the raw output:\n%s", "verdict unknown", stderr)
			}
			if len(gh.comments) != 0 {
				t.Errorf("raw output reached GitHub: %v", gh.comments)
			}
			if tt.name == "json" {
				var got map[string]any
				// cobra appends its usage text to the same writer on an error, so
				// decode the first JSON value only.
				if err := json.NewDecoder(strings.NewReader(stdout)).Decode(&got); err != nil {
					t.Fatalf("output is not JSON: %v\n%s", err, stdout)
				}
				if got["raw_output"] != raw || got["files_read"] != float64(7) || got["checkout"] != true {
					t.Errorf("raw_output/files_read/checkout = %v/%v/%v, want %q/7/true", got["raw_output"], got["files_read"], got["checkout"], raw)
				}
				return
			}
			if strings.Contains(stdout, raw) {
				t.Errorf("raw output belongs on stderr, not stdout:\n%s", stdout)
			}
			if !strings.Contains(stdout, "files read: 7") {
				t.Errorf("Diff line should report files read:\n%s", stdout)
			}
		})
	}
}

func TestReviewMergeCommand_DiffLineChecksOutNo(t *testing.T) {
	gh, rev := newRMFakes()
	out, err := runRM(t, gh, rev, "--repo", "o/r", "--pr", "7")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if !strings.Contains(out, "checkout: no, files read: 0") {
		t.Errorf("Diff line should state no checkout:\n%s", out)
	}
}

func TestReviewMergePasses_JSONCarriesPerPassFields(t *testing.T) {
	gh, rev := newRMFakes()
	rev.checkout, rev.passes, rev.overlap = true, 2, 1
	rev.filesReadByPass, rev.defectsByPass = []int{4, 6}, []int{2, 3}
	rev.durationByPass = []time.Duration{70 * time.Second, 90 * time.Second}
	rev.passFailed = []bool{false, true}

	out, err := runRM(t, gh, rev, "--repo", "qf-studio/pilot", "--pr", "5921", "--issue", "5917", "--json")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	var got struct {
		Passes          int     `json:"passes"`
		FilesReadByPass []int   `json:"files_read_by_pass"`
		DurationMSBy    []int64 `json:"duration_ms_by_pass"`
		DefectsByPass   []int   `json:"defects_by_pass"`
		PassFailed      []bool  `json:"pass_failed"`
		Overlap         int     `json:"overlap"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if got.Passes != 2 || got.Overlap != 1 {
		t.Errorf("passes=%d overlap=%d, want 2 and 1:\n%s", got.Passes, got.Overlap, out)
	}
	if fmt.Sprint(got.FilesReadByPass) != "[4 6]" || fmt.Sprint(got.DefectsByPass) != "[2 3]" || fmt.Sprint(got.DurationMSBy) != "[70000 90000]" || fmt.Sprint(got.PassFailed) != "[false true]" {
		t.Errorf("per-pass fields = files %v defects %v duration_ms %v failed %v", got.FilesReadByPass, got.DefectsByPass, got.DurationMSBy, got.PassFailed)
	}
}

func TestReviewMergePasses_SinglePassJSON(t *testing.T) {
	gh, rev := newRMFakes()
	out, err := runRM(t, gh, rev, "--repo", "qf-studio/pilot", "--pr", "5921", "--issue", "5917", "--json")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if !strings.Contains(out, `"passes": 1`) || strings.Contains(out, "files_read_by_pass") {
		t.Errorf("single-pass JSON should report passes 1 and no per-pass slices:\n%s", out)
	}
	if len(rev.setPasses) != 0 {
		t.Errorf("SetPasses called %v without --passes; the configured value must stand", rev.setPasses)
	}
}

func TestReviewMergePasses_FlagOverridesConfig(t *testing.T) {
	gh, rev := newRMFakes()
	if _, err := runRM(t, gh, rev, "--repo", "qf-studio/pilot", "--pr", "5921", "--issue", "5917", "--passes", "2"); err != nil {
		t.Fatalf("command: %v", err)
	}
	if len(rev.setPasses) != 1 || rev.setPasses[0] != 2 {
		t.Errorf("SetPasses calls = %v, want [2]", rev.setPasses)
	}
}

func TestReviewMergePasses_FlagValidation(t *testing.T) {
	for _, bad := range []string{"3", "-1"} {
		gh, rev := newRMFakes()
		_, err := runRM(t, gh, rev, "--repo", "qf-studio/pilot", "--pr", "5921", "--issue", "5917", "--passes", bad)
		if err == nil || !strings.Contains(err.Error(), "--passes") {
			t.Errorf("--passes %s: err = %v, want a --passes error", bad, err)
		}
	}
}

func TestReviewMergePasses_TextShowsPasses(t *testing.T) {
	gh, rev := newRMFakes()
	rev.checkout, rev.passes, rev.overlap = true, 2, 1
	rev.filesReadByPass, rev.defectsByPass = []int{4, 6}, []int{2, 3}
	out, err := runRM(t, gh, rev, "--repo", "qf-studio/pilot", "--pr", "5921", "--issue", "5917")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if !strings.Contains(out, "Passes: 2") {
		t.Errorf("text output lacks the Passes line:\n%s", out)
	}
}
