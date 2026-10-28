package executor

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

const postMergeFixtureRequestChanges = `VERDICT: REQUEST-CHANGES
DEFECTS:
- [blocking] internal/adapters/foo/client.go:42, binds to field "org_id" that the server never sends, internal/server/handler.go emits only "organization_id"
- [minor] internal/adapters/foo/client_test.go:10, test mocks the adapter instead of exercising it, the httptest handler returns a hand-written payload
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
			Severity: PostMergeSeverityBlocking,
			Location: "internal/adapters/foo/client.go:42",
			Claim:    `binds to field "org_id" that the server never sends`,
			Evidence: `internal/server/handler.go emits only "organization_id"`,
		},
		{
			Severity: PostMergeSeverityMinor,
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
		{"pipe separator", "VERDICT: REQUEST-CHANGES\nDEFECTS:\n[blocking] a.go:1 | c | e\nNOTES:", PostMergeRequestChanges, 1},
		{"empty output", "", PostMergeUnknown, 0},
		{"prose before verdict", "Sure, here is my review.\nVERDICT: APPROVE", PostMergeUnknown, 0},
		{"unknown verdict word", "VERDICT: LGTM\nDEFECTS:\nnone", PostMergeUnknown, 0},
		{"verdict with trailing text", "VERDICT: APPROVE because fine\nDEFECTS:\nnone", PostMergeUnknown, 0},
		{"request changes without defects downgrades", "VERDICT: REQUEST-CHANGES\nDEFECTS:\nnone\nNOTES:", PostMergeApproveWithNotes, 0},
		{"approve with notes keeps a minor defect", "VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\n[minor] a.go:1 - c - e\nNOT-VERIFIED:\nnone\nNOTES:", PostMergeApproveWithNotes, 1},
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
	runner := func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if dir != "" {
			t.Errorf("diff-only review cwd = %q, want none", dir)
		}
		gotArgs = args
		deadline, _ = ctx.Deadline()
		return []byte("VERDICT: APPROVE\nDEFECTS:\nnone\nNOTES:\n"), nil
	}
	r := newPostMergeReviewerWithDirRunner(runner)
	r.model, r.timeout = "claude-test-model", 90*time.Second

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
		"## CI Result\nsuccess", "## PR Body\n## Summary", "## Evidence", "go test ./...", "ignored", "DIFF-LINE", "PR #9",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "## PR Evidence") {
		t.Error("evidence is inside the supplied PR body and must not be repeated")
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

func TestPostMergeReviewer_DiffBudget(t *testing.T) {
	diff, big, _ := sevenFileDiff()
	tests := []struct {
		name        string
		budget      int
		wantTrimmed bool
	}{
		{"default budget sends whole", 0, false},
		{"small budget trims", 10000, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var prompt string
			r := newPostMergeReviewerWithRunner(func(ctx context.Context, args ...string) ([]byte, error) {
				prompt = args[2]
				return []byte("VERDICT: APPROVE\nDEFECTS:\nnone\nNOTES:\n"), nil
			})
			var logs strings.Builder
			r.log = slog.New(slog.NewTextHandler(&logs, nil))
			if tt.budget > 0 {
				r.SetMaxDiffChars(tt.budget)
			}
			review, err := r.Review(context.Background(), PostMergeReviewInput{PRNumber: 7, Diff: diff})
			if err != nil {
				t.Fatalf("Review: %v", err)
			}
			if review.DiffBytes != len(diff) || review.DiffFiles != 7 || review.DiffTrimmed != tt.wantTrimmed {
				t.Fatalf("review diff info = %d/%d/%v", review.DiffBytes, review.DiffFiles, review.DiffTrimmed)
			}
			hasLine := strings.Contains(prompt, "Diff trimmed: sent ")
			if hasLine != tt.wantTrimmed {
				t.Errorf("prompt has trim line = %v, want %v", hasLine, tt.wantTrimmed)
			}
			if !tt.wantTrimmed {
				if !strings.Contains(prompt, diff) {
					t.Error("untrimmed review must send the diff whole")
				}
				if len(review.TrimmedFiles) != 0 {
					t.Errorf("TrimmedFiles = %v", review.TrimmedFiles)
				}
				return
			}
			if !strings.Contains(prompt, "trimmed files: "+big[0]+" (") || !strings.Contains(prompt, " bytes cut)") {
				t.Errorf("prompt trim line does not name %s", big[0])
			}
			if strings.Index(prompt, "Diff trimmed:") > strings.Index(prompt, "## Merged Diff") {
				t.Error("trim line must sit above the Merged Diff section")
			}
			for _, p := range big {
				found := false
				for _, f := range review.TrimmedFiles {
					found = found || f == p
				}
				if !found {
					t.Errorf("TrimmedFiles %v missing %s", review.TrimmedFiles, p)
				}
			}
			for _, want := range []string{"pr=7", "budget=10000", "original_bytes=", "sent_bytes=", "trimmed_files=", big[0]} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("log missing %q:\n%s", want, logs.String())
				}
			}
		})
	}
}

func TestPostMergeReviewer_SystemPromptNotReviewed(t *testing.T) {
	if !strings.Contains(postMergeReviewerSystemPrompt, `"Not reviewed" bullet`) {
		t.Error("system prompt must tell the model to list cut files under Not reviewed")
	}
}

func TestPostMergeReviewer_SetMaxDiffChars(t *testing.T) {
	r := NewPostMergeReviewer("claude", "", 0)
	if r.MaxDiffChars() != 200000 {
		t.Fatalf("default = %d, want 200000", r.MaxDiffChars())
	}
	r.SetMaxDiffChars(5000)
	if r.MaxDiffChars() != 5000 {
		t.Fatalf("got %d", r.MaxDiffChars())
	}
	r.SetMaxDiffChars(0)
	if r.MaxDiffChars() != 200000 {
		t.Fatalf("zero must restore the default, got %d", r.MaxDiffChars())
	}
}

func TestParsePostMergeDefectSeparators(t *testing.T) {
	tests := []struct {
		name string
		line string
		want PostMergeDefect
		ok   bool
	}{
		{"comma", "a.go:1, the claim, the evidence",
			PostMergeDefect{Severity: PostMergeSeverityMinor, Location: "a.go:1", Claim: "the claim", Evidence: "the evidence"}, true},
		{"pipe", "a.go:1 | the claim | the evidence",
			PostMergeDefect{Severity: PostMergeSeverityMinor, Location: "a.go:1", Claim: "the claim", Evidence: "the evidence"}, true},
		{"dash", "internal/x.go:12 - claim text - evidence text",
			PostMergeDefect{Severity: PostMergeSeverityMinor, Location: "internal/x.go:12", Claim: "claim text", Evidence: "evidence text"}, true},
		{"dash with commas in the claim", "a.go:1 - claim, with commas - evidence",
			PostMergeDefect{Severity: PostMergeSeverityMinor, Location: "a.go:1", Claim: "claim, with commas", Evidence: "evidence"}, true},
		{"dash inside the evidence stays in the evidence", "a.go:1 - claim - evidence - more",
			PostMergeDefect{Severity: PostMergeSeverityMinor, Location: "a.go:1", Claim: "claim", Evidence: "evidence - more"}, true},
		{"comma line with a dash in the evidence", "a.go:1, claim, evidence - more",
			PostMergeDefect{Severity: PostMergeSeverityMinor, Location: "a.go:1", Claim: "claim", Evidence: "evidence - more"}, true},
		{"hyphenated path is not a separator", "my-pkg/a-b.go:1 - claim - evidence",
			PostMergeDefect{Severity: PostMergeSeverityMinor, Location: "my-pkg/a-b.go:1", Claim: "claim", Evidence: "evidence"}, true},
		{"blocking tag with bullet", "- [blocking] a.go:1 - claim - evidence",
			PostMergeDefect{Severity: PostMergeSeverityBlocking, Location: "a.go:1", Claim: "claim", Evidence: "evidence"}, true},
		{"minor tag", "[minor] a.go:1, claim, evidence",
			PostMergeDefect{Severity: PostMergeSeverityMinor, Location: "a.go:1", Claim: "claim", Evidence: "evidence"}, true},
		{"tag is case-insensitive", "[BLOCKING] a.go:1 | claim | evidence",
			PostMergeDefect{Severity: PostMergeSeverityBlocking, Location: "a.go:1", Claim: "claim", Evidence: "evidence"}, true},
		{"no tag defaults to minor", "a.go:1 - claim",
			PostMergeDefect{Severity: PostMergeSeverityMinor, Location: "a.go:1", Claim: "claim"}, true},
		{"none", "none", PostMergeDefect{}, false},
		{"bulleted none", "- None.", PostMergeDefect{}, false},
		{"tagged none", "[minor] none", PostMergeDefect{}, false},
		{"empty", "  ", PostMergeDefect{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parsePostMergeDefect(tt.line)
			if ok != tt.ok || got != tt.want {
				t.Errorf("parsePostMergeDefect(%q) = %+v, %v; want %+v, %v", tt.line, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// TestParsePostMergeDefectSeparators_Dash pins the " - " separator on its own.
func TestParsePostMergeDefectSeparators_Dash(t *testing.T) {
	got, ok := parsePostMergeDefect("internal/x.go:12 - claim text - evidence text")
	if !ok || got.Location != "internal/x.go:12" || got.Claim != "claim text" || got.Evidence != "evidence text" {
		t.Fatalf("got %+v, %v; want location, claim and evidence split on \" - \"", got, ok)
	}
}

func TestParsePostMergeReviewSeverity(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		wantVerdict  PostMergeVerdict
		wantDowngrad bool
		wantBlocking int
		wantDefects  int
	}{
		{"blocking keeps request-changes",
			"VERDICT: REQUEST-CHANGES\nDEFECTS:\n[blocking] a.go:1 - c - e\n[minor] b.go:2 - c - e\nNOTES:",
			PostMergeRequestChanges, false, 1, 2},
		{"approve with notes carries a minor defect",
			"VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\n[minor] a.go:1 - c - e\nNOTES:\nreq 1 met",
			PostMergeApproveWithNotes, false, 0, 1},
		{"untagged request-changes defects are minor",
			"VERDICT: REQUEST-CHANGES\nDEFECTS:\na.go:1, c, e\nNOTES:",
			PostMergeApproveWithNotes, true, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePostMergeReview(tt.text)
			if got.Verdict != tt.wantVerdict || got.Downgraded != tt.wantDowngrad ||
				len(got.BlockingDefects()) != tt.wantBlocking || len(got.Defects) != tt.wantDefects {
				t.Errorf("verdict=%q downgraded=%v blocking=%d defects=%d; want %q %v %d %d",
					got.Verdict, got.Downgraded, len(got.BlockingDefects()), len(got.Defects),
					tt.wantVerdict, tt.wantDowngrad, tt.wantBlocking, tt.wantDefects)
			}
		})
	}
}

func TestParsePostMergeReviewSeverity_MinorOnlyDowngrades(t *testing.T) {
	got := parsePostMergeReview("VERDICT: REQUEST-CHANGES\nDEFECTS:\n[minor] a.go:1 - c - e\n[minor] b.go:2 - c - e\nNOTES:\nnit")
	if got.Verdict != PostMergeApproveWithNotes {
		t.Fatalf("verdict = %q, want %q for a minor-only REQUEST-CHANGES", got.Verdict, PostMergeApproveWithNotes)
	}
	if !got.Downgraded || len(got.Defects) != 2 {
		t.Errorf("downgraded=%v defects=%d, want true and the two minor defects kept", got.Downgraded, len(got.Defects))
	}
}

func TestParsePostMergeReviewSeverity_NotVerifiedCap(t *testing.T) {
	text := "VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\nnone\nNOT-VERIFIED:\n- one\n- two\n* three\n- four\n- five\nNOTES:\nok"
	got := parsePostMergeReview(text)
	want := []string{"one", "two", "three"}
	if len(got.NotVerified) != len(want) {
		t.Fatalf("NotVerified = %q, want %q", got.NotVerified, want)
	}
	for i := range want {
		if got.NotVerified[i] != want[i] {
			t.Errorf("NotVerified[%d] = %q, want %q", i, got.NotVerified[i], want[i])
		}
	}
	if got.Notes != "ok" {
		t.Errorf("notes = %q, want the NOTES section after NOT-VERIFIED", got.Notes)
	}
	for _, none := range []string{"NOT-VERIFIED: none", "NOT VERIFIED:\nNone."} {
		if g := parsePostMergeReview("VERDICT: APPROVE\nDEFECTS:\nnone\n" + none + "\nNOTES:"); len(g.NotVerified) != 0 {
			t.Errorf("%q parsed items %q, want none", none, g.NotVerified)
		}
	}
}

func TestPostMergeReviewer_DowngradeIsLogged(t *testing.T) {
	r := newPostMergeReviewerWithRunner(mockJudgeRunner("VERDICT: REQUEST-CHANGES\nDEFECTS:\n[minor] a.go:1 - c - e\nNOTES:\n"))
	var logs strings.Builder
	r.log = slog.New(slog.NewTextHandler(&logs, nil))
	got, err := r.Review(context.Background(), PostMergeReviewInput{PRNumber: 77, Diff: "diff --git a/x b/x\n+x"})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got.Verdict != PostMergeApproveWithNotes {
		t.Fatalf("verdict = %q, want %q", got.Verdict, PostMergeApproveWithNotes)
	}
	for _, want := range []string{"downgraded", "REQUEST-CHANGES", "APPROVE-WITH-NOTES", "pr=77"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log missing %q:\n%s", want, logs.String())
		}
	}
}

func TestPostMergeReviewer_PromptContract(t *testing.T) {
	var prompt string
	r := newPostMergeReviewerWithRunner(func(_ context.Context, args ...string) ([]byte, error) {
		prompt = args[2]
		return []byte("VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n"), nil
	})
	// An Evidence block in the middle of a body too long to send whole is
	// re-supplied under its own heading.
	body := strings.Repeat("a", maxPreflightBodyChars) + "\n## Evidence\n\nEVIDENCE-LINE\n\n## Tail\n" + strings.Repeat("b", maxPreflightBodyChars)
	if _, err := r.Review(context.Background(), PostMergeReviewInput{PRNumber: 3, Diff: "diff --git a/x b/x\n+x", PRBody: body}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	for _, want := range []string{
		"[blocking]", "[minor]", "NOT-VERIFIED:", "at most three", "supplied in full under \"## PR Body\"",
		"REQUEST-CHANGES only when at least one defect is [blocking]", "\"could not\"", "\"please verify\"", "\"I assume\"",
		"a requirement of the issue is not met", "a wire field has no producer", "\"Not in the diff\" is never a reason",
		"## PR Body\n", "## PR Evidence (from the PR body)\n## Evidence\n\nEVIDENCE-LINE",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
