package executor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

const postMergeFixtureRequestChanges = `VERDICT: REQUEST-CHANGES
DEFECTS:
- internal/adapters/foo/client.go:42, binds to field "org_id" that the server never sends, internal/server/handler.go emits only "organization_id"
- internal/adapters/foo/client_test.go:10, test mocks the adapter instead of exercising it, the httptest handler returns a hand-written payload
NOTES:
Migration 0042 has a down path.
Consider a contract test.`

func TestPostMergeReviewer_ParsesFixture(t *testing.T) {
	r := newPostMergeReviewerWithRunner(mockJudgeRunner(postMergeFixtureRequestChanges))

	got, err := r.Review(context.Background(), PostMergeReviewInput{
		PRNumber: 7, IssueTitle: "feat: foo", IssueBody: "do foo", Diff: "diff --git a/x b/x\n+x",
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got.Verdict != PostMergeRequestChanges {
		t.Fatalf("verdict = %q, want %q", got.Verdict, PostMergeRequestChanges)
	}
	want := []PostMergeDefect{
		{
			Location: "internal/adapters/foo/client.go:42",
			Claim:    `binds to field "org_id" that the server never sends`,
			Evidence: `internal/server/handler.go emits only "organization_id"`,
		},
		{
			Location: "internal/adapters/foo/client_test.go:10",
			Claim:    "test mocks the adapter instead of exercising it",
			Evidence: "the httptest handler returns a hand-written payload",
		},
	}
	if len(got.Defects) != len(want) {
		t.Fatalf("defects = %d, want %d: %+v", len(got.Defects), len(want), got.Defects)
	}
	for i := range want {
		if got.Defects[i] != want[i] {
			t.Errorf("defect[%d] = %+v, want %+v", i, got.Defects[i], want[i])
		}
	}
	if !strings.Contains(got.Notes, "Migration 0042") || !strings.Contains(got.Notes, "contract test") {
		t.Errorf("notes = %q, want both note lines", got.Notes)
	}
}

func TestParsePostMergeReview(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		wantVerdict PostMergeVerdict
		wantDefects int
	}{
		{"approve", "VERDICT: APPROVE\nDEFECTS:\nnone\nNOTES:\n", PostMergeApprove, 0},
		{"approve with notes", "VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\nnone\nNOTES:\nnit", PostMergeApproveWithNotes, 0},
		{"leading blank lines and lowercase", "\n\nverdict: approve\nDEFECTS: none\nNOTES:", PostMergeApprove, 0},
		{"pipe separator", "VERDICT: REQUEST-CHANGES\nDEFECTS:\na.go:1 | c | e\nNOTES:", PostMergeRequestChanges, 1},
		{"empty output", "", PostMergeUnknown, 0},
		{"prose before verdict", "Sure, here is my review.\nVERDICT: APPROVE", PostMergeUnknown, 0},
		{"unknown verdict word", "VERDICT: LGTM\nDEFECTS:\nnone", PostMergeUnknown, 0},
		{"verdict with trailing text", "VERDICT: APPROVE because fine\nDEFECTS:\nnone", PostMergeUnknown, 0},
		{"request changes without defects", "VERDICT: REQUEST-CHANGES\nDEFECTS:\nnone\nNOTES:", PostMergeUnknown, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePostMergeReview(tt.text)
			if got.Verdict != tt.wantVerdict {
				t.Errorf("verdict = %q, want %q", got.Verdict, tt.wantVerdict)
			}
			if len(got.Defects) != tt.wantDefects {
				t.Errorf("defects = %d, want %d", len(got.Defects), tt.wantDefects)
			}
		})
	}
}

func TestPostMergeReviewer_MalformedOutputIsUnknown(t *testing.T) {
	r := newPostMergeReviewerWithRunner(mockJudgeRunner("I could not review this diff."))
	got, err := r.Review(context.Background(), PostMergeReviewInput{Diff: "diff --git a/x b/x\n+x"})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got.Verdict != PostMergeUnknown || len(got.Defects) != 0 {
		t.Fatalf("got %+v, want unknown verdict with no defects", got)
	}
}

func TestPostMergeReviewer_Errors(t *testing.T) {
	t.Run("empty diff", func(t *testing.T) {
		r := newPostMergeReviewerWithRunner(mockJudgeRunner("VERDICT: APPROVE"))
		if _, err := r.Review(context.Background(), PostMergeReviewInput{Diff: "  \n"}); err == nil {
			t.Fatal("want error for empty diff")
		}
	})
	t.Run("subprocess failure", func(t *testing.T) {
		boom := errors.New("exit status 1")
		r := newPostMergeReviewerWithRunner(mockJudgeRunnerError(boom))
		_, err := r.Review(context.Background(), PostMergeReviewInput{Diff: "diff --git a/x b/x\n+x"})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want wrapping %v", err, boom)
		}
	})
}

func TestPostMergeReviewer_PromptAndArgs(t *testing.T) {
	var (
		mu       sync.Mutex
		gotArgs  []string
		deadline time.Time
	)
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		gotArgs = args
		deadline, _ = ctx.Deadline()
		return []byte("VERDICT: APPROVE\nDEFECTS:\nnone\nNOTES:\n"), nil
	}
	r := NewPostMergeReviewer("claude", "claude-test-model", 90*time.Second)
	r.cmdRunner = runner

	body := "## Summary\nx\n\n## Evidence\n\n$ go test ./...\nok\n\n## Other\nignored"
	_, err := r.Review(context.Background(), PostMergeReviewInput{
		PRNumber: 9, IssueTitle: "ISSUE-TITLE", IssueBody: "ISSUE-BODY",
		Diff: "diff --git a/x b/x\n+DIFF-LINE", CIResult: "success", PRBody: body,
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotArgs) < 3 || gotArgs[0] != "--print" || gotArgs[1] != "-p" {
		t.Fatalf("args = %q, want --print -p <prompt> ...", gotArgs)
	}
	prompt := gotArgs[2]
	for _, want := range []string{
		"post-merge code reviewer", "wire field", "ISSUE-TITLE", "ISSUE-BODY",
		"## CI Result\nsuccess", "## PR Evidence", "go test ./...", "DIFF-LINE", "PR #9",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "ignored") {
		t.Error("prompt carries content past the evidence section")
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "--model claude-test-model") {
		t.Errorf("args = %q, want --model claude-test-model", gotArgs)
	}
	if time.Until(deadline) > 90*time.Second || time.Until(deadline) < 60*time.Second {
		t.Errorf("runner deadline %v, want ~90s from now", time.Until(deadline))
	}
}

func TestExtractPRBodyEvidence(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"none", "## Summary\nx", ""},
		{"empty", "", ""},
		{"section to end", "## Summary\nx\n\n## Evidence\n\n### A\n\nout", "## Evidence\n\n### A\n\nout"},
		{"stops at next h2", "## Evidence (manual pins)\nfail\n## Not verified\n- y", "## Evidence (manual pins)\nfail"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractPRBodyEvidence(tt.body); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
