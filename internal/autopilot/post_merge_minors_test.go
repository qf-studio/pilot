package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
)

func minorDefect(loc, claim, evidence string) executor.PostMergeDefect {
	return executor.PostMergeDefect{Severity: executor.PostMergeSeverityMinor, Location: loc, Claim: claim, Evidence: evidence}
}

func TestPostMergeMinorFilter(t *testing.T) {
	tests := []struct {
		name     string
		defects  []executor.PostMergeDefect
		wantKept int
		wantDrop int
	}{
		{"body-level location drops", []executor.PostMergeDefect{minorDefect("PR", "x", "y")}, 0, 1},
		{"path with line kept", []executor.PostMergeDefect{minorDefect("internal/quality/runner.go:356", "stale headline", "caller")}, 1, 0},
		{"path without line kept", []executor.PostMergeDefect{minorDefect("internal/executor/quality.go", "claim", "ev")}, 1, 0},
		{"bare word drops", []executor.PostMergeDefect{minorDefect("runner", "claim", "ev")}, 0, 1},
		{"no slash drops", []executor.PostMergeDefect{minorDefect("runner.go:12", "claim", "ev")}, 0, 1},
		{"no extension drops", []executor.PostMergeDefect{minorDefect("internal/quality/runner", "claim", "ev")}, 0, 1},
		{"known residual in evidence", []executor.PostMergeDefect{minorDefect("internal/a/b.go:1", "claim", "the issue marks this as a known residual risk")}, 0, 1},
		{"out of scope in claim", []executor.PostMergeDefect{minorDefect("internal/a/b.go:1", "out of scope per the issue", "ev")}, 0, 1},
		{"cannot run in evidence", []executor.PostMergeDefect{minorDefect("internal/a/b.go:1", "claim", "I cannot run tests in this checkout")}, 0, 1},
		{"PR body in claim, any case", []executor.PostMergeDefect{minorDefect("internal/a/b.go:1", "the pr BODY lacks a section", "ev")}, 0, 1},
		{"duplicates across passes collapse", []executor.PostMergeDefect{
			minorDefect("internal/a/b.go:1", "same", "pass one"),
			minorDefect("internal/a/b.go:1", "same", "pass two"),
		}, 1, 0},
		{"blocking defects are not minors", []executor.PostMergeDefect{
			{Severity: executor.PostMergeSeverityBlocking, Location: "internal/a/b.go:1", Claim: "c"},
		}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept, dropped := filedMinorDefects(&executor.PostMergeReview{Defects: tt.defects})
			if len(kept) != tt.wantKept || len(dropped) != tt.wantDrop {
				t.Fatalf("kept=%d dropped=%d, want kept=%d dropped=%d", len(kept), len(dropped), tt.wantKept, tt.wantDrop)
			}
		})
	}
}

func newMinorsFeedbackLoop(t *testing.T) (*pmrGitHub, *FeedbackLoop) {
	t.Helper()
	mock, gh := newPMRGitHub(t)
	return mock, NewFeedbackLoop(gh, "owner", "repo", DefaultConfig())
}

func TestPostMergeMinorsIssue_Template(t *testing.T) {
	mock, f := newMinorsFeedbackLoop(t)

	var kept []executor.PostMergeDefect
	for i := 0; i < 40; i++ {
		kept = append(kept, minorDefect(
			fmt.Sprintf("internal/pkg%d/file%d.go:%d", i%6, i, 10+i),
			"claim `with ticks` "+strings.Repeat("c", 900),
			"evidence internal/x/y.go:42 `tick` "+strings.Repeat("e", 900)))
	}
	kept = append(kept, minorDefect("docs/guide.md:7", "doc claim", "doc evidence"))
	dropped := []executor.PostMergeDefect{
		minorDefect("PR", "body is thin", "ev"),
		minorDefect("internal/a/b.go:1", "claim", "a known residual"),
	}

	num, err := f.CreatePostMergeMinorsIssue(context.Background(), 6003, "https://github.com/owner/repo/pull/6003", 6002, kept, dropped)
	if err != nil || num == 0 {
		t.Fatalf("CreatePostMergeMinorsIssue = %d, %v", num, err)
	}
	if len(mock.issues) != 1 {
		t.Fatalf("issues created = %d, want 1", len(mock.issues))
	}
	in := mock.issues[0]

	if !strings.HasPrefix(in.Title, "fix(review): post-merge minors from PR #") || !strings.HasSuffix(in.Title, "#6002") {
		t.Errorf("title = %q", in.Title)
	}
	last := -1
	for _, h := range []string{"## Context", "## Implementation", "## Acceptance", "## Out of scope", "## Refs"} {
		idx := strings.Index(in.Body, "\n"+h+"\n")
		if strings.HasPrefix(in.Body, h+"\n") {
			idx = 0
		}
		if idx < 0 || idx <= last {
			t.Fatalf("header %q missing or out of order (idx %d, prev %d)", h, idx, last)
		}
		last = idx
	}
	if strings.Contains(in.Body, "## Defects") {
		t.Error("body must not carry a Defects section")
	}
	pathLine := regexp.MustCompile("`[^`]*[A-Za-z0-9_/.-]+:[0-9]+[^`]*`")
	if pathLine.MatchString(in.Body) {
		t.Errorf("a backticked span carries path:line: %q", pathLine.FindString(in.Body))
	}
	spans := regexp.MustCompile("`([^`]*)`").FindAllStringSubmatch(in.Body, -1)
	if len(spans) > 4 {
		t.Errorf("command bullets = %d, want at most 4", len(spans))
	}
	if len(spans) == 0 {
		t.Error("no go test command bullet")
	}
	for _, s := range spans {
		if !strings.HasPrefix(s[1], "go test -race ./") || !strings.HasSuffix(s[1], "/") {
			t.Errorf("backticked span %q is not a package go test command", s[1])
		}
	}
	if !strings.Contains(in.Body, "internal/pkg0/file0.go line 10") {
		t.Error("kept location must be plain text with a line N suffix")
	}
	if !strings.Contains(in.Body, "PR — body is thin (dropped: no file location)") ||
		!strings.Contains(in.Body, "(dropped: known residual)") ||
		!strings.Contains(in.Body, "No change to any file not named above.") {
		t.Errorf("Out of scope section wrong:\n%s", in.Body[strings.Index(in.Body, "## Out of scope"):])
	}
	if len(in.Body) > maxPostMergeIssueHardChars {
		t.Errorf("body = %d chars, want <= %d", len(in.Body), maxPostMergeIssueHardChars)
	}
	for _, l := range []string{"pilot", "autopilot-fix", "post-merge-review", "no-decompose", "review-minor"} {
		n := 0
		for _, have := range in.Labels {
			if have == l {
				n++
			}
		}
		if n != 1 {
			t.Errorf("label %q appears %d times in %v, want once", l, n, in.Labels)
		}
	}
}

func TestPostMergeMinorsIssue_Template_NoDroppedAndSmall(t *testing.T) {
	mock, f := newMinorsFeedbackLoop(t)
	kept := []executor.PostMergeDefect{minorDefect("internal/quality/runner.go:356", "stale headline", "caller hop")}
	if _, err := f.CreatePostMergeMinorsIssue(context.Background(), 1, "https://x/pull/1", 2, kept, nil); err != nil {
		t.Fatal(err)
	}
	body := mock.issues[0].Body
	for _, want := range []string{
		"1. internal/quality/runner.go line 356 — stale headline. Evidence: caller hop.",
		"- `go test -race ./internal/quality/` passes; paste the output into the PR body.",
		"mutation pin (revert the item -> the test fails).",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func approveWithNotes(defects ...executor.PostMergeDefect) *executor.PostMergeReview {
	return &executor.PostMergeReview{Verdict: executor.PostMergeApproveWithNotes, Defects: defects}
}

func twoKeptOneDropped() *executor.PostMergeReview {
	return approveWithNotes(
		minorDefect("internal/quality/runner.go:356", "stale headline", "one caller hop outside the diff"),
		minorDefect("internal/executor/quality.go:10", "flag value parsed as target", "make -f Makefile test"),
		minorDefect("PR", "body omits the mutation pins", "PR description"),
	)
}

func runMinorReviews(t *testing.T, c *Controller, prs ...int) {
	t.Helper()
	for _, pr := range prs {
		if err := c.handleMerged(context.Background(), mergedPRState(pr, 10)); err != nil {
			t.Fatalf("handleMerged PR %d: %v", pr, err)
		}
		c.waitPostMergeReviews()
	}
}

func TestPostMergeMinor_FilesOneIssueOnApproveWithNotes(t *testing.T) {
	cases := []struct {
		name       string
		review     *executor.PostMergeReview
		fileMinors bool
		wantIssues int
		wantMinor  bool
		wantTitle  string
	}{
		{"approve-with-notes, file_minors on", twoKeptOneDropped(), true, 1, true, "fix(review): post-merge minors from PR #42 for issue #10"},
		{"approve-with-notes, file_minors off", twoKeptOneDropped(), false, 0, false, ""},
		{"request-changes keeps today's issue", requestChangesReview(), true, 1, false, postMergeReviewIssueTitle(42, 10)},
		{"approve without defects", &executor.PostMergeReview{Verdict: executor.PostMergeApprove}, true, 0, false, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			mock, gh := newPMRGitHub(t)
			c := newPMRController(t, gh, &pmrFakeReviewer{review: tt.review}, true)
			c.config.PostMergeReview.FileMinors = tt.fileMinors
			var logs lockedBuffer
			c.log = slog.New(slog.NewTextHandler(&logs, nil))

			runMinorReviews(t, c, 42)

			if got := mock.issueCount(); got != tt.wantIssues {
				t.Fatalf("issues = %d, want %d", got, tt.wantIssues)
			}
			filed := strings.Contains(logs.String(), "post-merge review: minor follow-up filed")
			if filed != tt.wantMinor {
				t.Errorf("minor-filed log = %v, want %v\n%s", filed, tt.wantMinor, logs.String())
			}
			if tt.wantIssues == 0 {
				return
			}
			in := mock.issues[0]
			if in.Title != tt.wantTitle {
				t.Errorf("title = %q, want %q", in.Title, tt.wantTitle)
			}
			if slices.Contains(in.Labels, postMergeReviewMinorLabel) != tt.wantMinor {
				t.Errorf("labels = %v, review-minor want %v", in.Labels, tt.wantMinor)
			}
			if tt.wantMinor {
				for _, l := range []string{"pilot", "autopilot-fix", "post-merge-review", "no-decompose"} {
					if !slices.Contains(in.Labels, l) {
						t.Errorf("labels %v lack %q", in.Labels, l)
					}
				}
				if !strings.Contains(logs.String(), "kept=2") || !strings.Contains(logs.String(), "dropped=1") {
					t.Errorf("filed log lacks kept=2 dropped=1: %s", logs.String())
				}
				if got := c.metrics.Snapshot().PostMergeMinorIssuesFiled; got != 1 {
					t.Errorf("PostMergeMinorIssuesFiled = %d, want 1", got)
				}
			}
		})
	}
}

func TestPostMergeMinor_DailyCapOnePerDay(t *testing.T) {
	mock, gh := newPMRGitHub(t)
	c := newPMRController(t, gh, &pmrFakeReviewer{review: twoKeptOneDropped()}, true)
	c.config.PostMergeReview.FileMinors = true
	var logs lockedBuffer
	c.log = slog.New(slog.NewTextHandler(&logs, nil))
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	c.postMergeMinorBudget().now = func() time.Time { return now }

	runMinorReviews(t, c, 51, 52)
	if got := mock.issueCount(); got != 1 {
		t.Fatalf("issues after two reviews in one day = %d, want 1", got)
	}
	if got := strings.Count(logs.String(), "minor follow-up skipped, daily cap reached"); got != 1 {
		t.Errorf("cap line logged %d times, want 1", got)
	}

	now = now.Add(24 * time.Hour)
	runMinorReviews(t, c, 53)
	if got := mock.issueCount(); got != 2 {
		t.Fatalf("issues after the next UTC day = %d, want 2", got)
	}
}

func TestPostMergeMinor_DedupsByTitle(t *testing.T) {
	mock, gh := newPMRGitHub(t)
	c := newPMRController(t, gh, &pmrFakeReviewer{review: twoKeptOneDropped()}, true)
	c.config.PostMergeReview.FileMinors = true
	c.config.PostMergeReview.MinorIssueDailyCap = 2

	runMinorReviews(t, c, 61)
	// A second review of the same PR number (PostMergeReviewPosted is a fresh
	// state here) must hit the exact-title dedup and file nothing.
	runMinorReviews(t, c, 61)
	if got := mock.issueCount(); got != 1 {
		t.Fatalf("issues = %d, want 1 (second filing deduped by title)", got)
	}
}
