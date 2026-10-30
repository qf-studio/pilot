package executor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// passAnswer is one pass's model answer; Read tool uses make FilesRead nonzero.
func passAnswer(t *testing.T, answer string, reads int) []byte {
	t.Helper()
	var sb strings.Builder
	for i := 0; i < reads; i++ {
		sb.WriteString(streamLine(t, streamToolUse("Read", fmt.Sprintf("/x/f%d.go", i))))
	}
	sb.WriteString(streamLine(t, map[string]any{"type": "result", "subtype": "success", "result": answer}))
	return []byte(sb.String())
}

// scriptedPasses is a runner that answers call n with answers[n] and records
// every invocation.
type scriptedPasses struct {
	mu       sync.Mutex
	t        *testing.T
	answers  []func(dir string) ([]byte, error)
	dirs     []string
	prompts  []string
	deadline []time.Time
}

func (s *scriptedPasses) run(ctx context.Context, dir, _ string, args ...string) ([]byte, error) {
	s.mu.Lock()
	n := len(s.dirs)
	s.dirs = append(s.dirs, dir)
	p, _ := flagValue(args, "-p")
	s.prompts = append(s.prompts, p)
	dl, _ := ctx.Deadline()
	s.deadline = append(s.deadline, dl)
	s.mu.Unlock()
	if n >= len(s.answers) {
		s.t.Errorf("unexpected invocation %d", n+1)
		return nil, errors.New("unexpected invocation")
	}
	return s.answers[n](dir)
}

func fixed(b []byte) func(string) ([]byte, error) {
	return func(string) ([]byte, error) { return b, nil }
}

// twoPassReviewer builds a two-pass reviewer over a real checkout whose runner
// follows answers; it also counts checkouts created.
func twoPassReviewer(t *testing.T, answers ...func(dir string) ([]byte, error)) (*PostMergeReviewer, *scriptedPasses, *int, PostMergeReviewInput) {
	t.Helper()
	repo, sha := initCheckoutRepo(t)
	s := &scriptedPasses{t: t, answers: answers}
	r := newPostMergeReviewerWithHomeRunner(s.run)
	r.SetPasses(2)
	checkouts := 0
	inner := r.checkout
	r.checkout = func(ctx context.Context, repoPath, commit string) (*postMergeCheckout, error) {
		checkouts++
		return inner(ctx, repoPath, commit)
	}
	return r, s, &checkouts, checkoutInput(repo, sha)
}

const (
	answerApproveMinorA = "VERDICT: APPROVE\nDEFECTS:\n- [minor] a.go:10 - claim from A - short\n- [minor] b.go:5 - only in A - ev b\nNOT-VERIFIED:\nnone\nNOTES:\nnote from A"
	answerBlockingB     = "VERDICT: REQUEST-CHANGES\nDEFECTS:\n- [blocking] c.go:7 - only in B - ev c\nNOT-VERIFIED:\nnone\nNOTES:\nnote from B"
)

func TestPostMergeReviewTwoPass_VerdictFromUnion(t *testing.T) {
	// Pass A says APPROVE and lists minors; pass B finds a blocking defect. The
	// union has a blocking defect, so the verdict is REQUEST-CHANGES.
	r, s, _, in := twoPassReviewer(t,
		fixed(passAnswer(t, answerApproveMinorA, 1)),
		fixed(passAnswer(t, answerBlockingB, 1)))
	got, err := r.Review(context.Background(), in)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got.Verdict != PostMergeRequestChanges {
		t.Errorf("verdict = %q, want REQUEST-CHANGES from the union", got.Verdict)
	}
	if len(got.Defects) != 3 || len(got.BlockingDefects()) != 1 {
		t.Errorf("defects = %+v, want 3 with 1 blocking", got.Defects)
	}
	if len(s.dirs) != 2 {
		t.Errorf("invocations = %d, want 2", len(s.dirs))
	}
	for _, want := range []string{"VERDICT: APPROVE", "VERDICT: REQUEST-CHANGES"} {
		if !strings.Contains(got.RawOutput, want) {
			t.Errorf("RawOutput lacks per-pass %q", want)
		}
	}
}

func TestPostMergeReviewTwoPass_VerdictRule(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want PostMergeVerdict
	}{
		{"both clean", "VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n", "VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n", PostMergeApprove},
		{"not-verified in one pass", "VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n", "VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\nnone\nNOT-VERIFIED:\nx.go not in the diff\nNOTES:\n", PostMergeApproveWithNotes},
		{"minor only", answerApproveMinorA, "VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n", PostMergeApproveWithNotes},
		{"blocking in A", "VERDICT: REQUEST-CHANGES\nDEFECTS:\n- [blocking] a.go:1 - c - e\nNOT-VERIFIED:\nnone\nNOTES:\n", "VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n", PostMergeRequestChanges},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _, _, in := twoPassReviewer(t, fixed(passAnswer(t, tt.a, 1)), fixed(passAnswer(t, tt.b, 1)))
			got, err := r.Review(context.Background(), in)
			if err != nil {
				t.Fatalf("Review: %v", err)
			}
			if got.Verdict != tt.want {
				t.Errorf("verdict = %q, want %q", got.Verdict, tt.want)
			}
		})
	}
}

func TestPostMergeReviewTwoPass_UnionDedupesByLocation(t *testing.T) {
	// Same location, different claim text: one defect. The second pass names
	// the file with the checkout prefix, which is stripped.
	r, _, _, in := twoPassReviewer(t,
		fixed(passAnswer(t, "VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\n- [minor] a.go:10 - claim from A - short\nNOT-VERIFIED:\nnone\nNOTES:\n", 1)),
		func(dir string) ([]byte, error) {
			return passAnswer(t, fmt.Sprintf("VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\n- [minor] %s - totally different wording - a much longer evidence string\nNOT-VERIFIED:\nnone\nNOTES:\n", filepath.Join(dir, "a.go")+":10"), 1), nil
		})
	got, err := r.Review(context.Background(), in)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(got.Defects) != 1 {
		t.Fatalf("defects = %+v, want 1 deduplicated by location", got.Defects)
	}
	if got.Overlap != 1 {
		t.Errorf("overlap = %d, want 1", got.Overlap)
	}
	if got.Defects[0].Evidence != "a much longer evidence string" {
		t.Errorf("evidence = %q, want the longer one", got.Defects[0].Evidence)
	}
}

func TestPostMergeReviewTwoPass_UnionKeepsHigherSeverity(t *testing.T) {
	tests := []struct {
		name     string
		a, b     string
		wantSev  PostMergeSeverity
		wantEv   string
		wantVerd PostMergeVerdict
	}{
		{"blocking in B beats minor in A",
			"VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\n- [minor] a.go:10 - c - ev a\nNOT-VERIFIED:\nnone\nNOTES:\n",
			"VERDICT: REQUEST-CHANGES\nDEFECTS:\n- [blocking] a.go:10 - c - ev b is longer\nNOT-VERIFIED:\nnone\nNOTES:\n",
			PostMergeSeverityBlocking, "ev b is longer", PostMergeRequestChanges},
		{"blocking in A stays over minor in B, longer evidence from B",
			"VERDICT: REQUEST-CHANGES\nDEFECTS:\n- [blocking] a.go:10 - c - ev a\nNOT-VERIFIED:\nnone\nNOTES:\n",
			"VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\n- [minor] a.go:10 - c - ev b is longer\nNOT-VERIFIED:\nnone\nNOTES:\n",
			PostMergeSeverityBlocking, "ev b is longer", PostMergeRequestChanges},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _, _, in := twoPassReviewer(t, fixed(passAnswer(t, tt.a, 1)), fixed(passAnswer(t, tt.b, 1)))
			got, err := r.Review(context.Background(), in)
			if err != nil {
				t.Fatalf("Review: %v", err)
			}
			if len(got.Defects) != 1 || got.Defects[0].Severity != tt.wantSev || got.Defects[0].Evidence != tt.wantEv || got.Verdict != tt.wantVerd {
				t.Errorf("defects=%+v verdict=%q, want one %s defect with evidence %q and %s", got.Defects, got.Verdict, tt.wantSev, tt.wantEv, tt.wantVerd)
			}
		})
	}
}

func TestPostMergeReviewTwoPass_DefectKey(t *testing.T) {
	tests := []struct {
		name string
		a, b PostMergeDefect
		same bool
	}{
		{"same line", PostMergeDefect{Location: "a.go:10", Claim: "x"}, PostMergeDefect{Location: "a.go:10", Claim: "y"}, true},
		{"different line", PostMergeDefect{Location: "a.go:10", Claim: "x"}, PostMergeDefect{Location: "a.go:11", Claim: "x"}, false},
		{"checkout prefix", PostMergeDefect{Location: "a.go:10", Claim: "x"}, PostMergeDefect{Location: "/tmp/co/a.go:10", Claim: "y"}, true},
		{"no line, same claim head", PostMergeDefect{Location: "a.go", Claim: strings.Repeat("w", 40) + "tail one"}, PostMergeDefect{Location: "a.go", Claim: strings.Repeat("w", 40) + "tail two"}, true},
		{"no line, different claim", PostMergeDefect{Location: "a.go", Claim: "first"}, PostMergeDefect{Location: "a.go", Claim: "second"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defectKey(tt.a, "/tmp/co") == defectKey(tt.b, "/tmp/co"); got != tt.same {
				t.Errorf("keys equal = %v, want %v", got, tt.same)
			}
		})
	}
}

func TestPostMergeReviewTwoPass_OneCheckout(t *testing.T) {
	r, s, checkouts, in := twoPassReviewer(t,
		fixed(passAnswer(t, answerApproveMinorA, 2)),
		fixed(passAnswer(t, answerBlockingB, 3)))
	got, err := r.Review(context.Background(), in)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if *checkouts != 1 {
		t.Errorf("checkouts created = %d, want 1", *checkouts)
	}
	if len(s.dirs) != 2 || s.dirs[0] == "" || s.dirs[0] != s.dirs[1] {
		t.Errorf("working dirs = %v, want two invocations in one checkout", s.dirs)
	}
	if got.Passes != 2 || len(got.FilesReadByPass) != 2 || got.FilesReadByPass[0] != 2 || got.FilesReadByPass[1] != 3 || got.FilesRead != 5 {
		t.Errorf("passes=%d filesByPass=%v files=%d, want 2, [2 3], 5", got.Passes, got.FilesReadByPass, got.FilesRead)
	}
	if len(got.DefectsByPass) != 2 || got.DefectsByPass[0] != 2 || got.DefectsByPass[1] != 1 || len(got.DurationByPass) != 2 || len(got.TurnsByPass) != 2 {
		t.Errorf("per-pass stats = defects %v durations %v turns %v", got.DefectsByPass, got.DurationByPass, got.TurnsByPass)
	}
	if worktreeCount(t, in.ProjectPath) != 1 {
		t.Errorf("review worktree not cleaned up")
	}
}

func TestPostMergeReviewTwoPass_PromptVariants(t *testing.T) {
	for _, preMerge := range []bool{false, true} {
		t.Run(fmt.Sprintf("premerge=%v", preMerge), func(t *testing.T) {
			r, s, _, in := twoPassReviewer(t,
				fixed(passAnswer(t, answerApproveMinorA, 1)), fixed(passAnswer(t, answerApproveMinorA, 1)))
			in.PreMerge = preMerge
			if _, err := r.Review(context.Background(), in); err != nil {
				t.Fatalf("Review: %v", err)
			}
			a, b := s.prompts[0], s.prompts[1]
			if !strings.Contains(a, postMergeChecklistItem2Checkout) || strings.Contains(a, "2b)") || strings.Contains(a, postMergeAdjacentPassSentence) {
				t.Errorf("pass A must carry item 2 and neither 2b nor the pass B sentence")
			}
			if strings.Contains(b, "\n2) ") || !strings.Contains(b, "\n"+postMergeChecklistItemAdjacent+"\n3) ") || !strings.Contains(b, postMergeAdjacentPassSentence) {
				t.Errorf("pass B must carry 2b in place of item 2 and the pass B sentence")
			}
			if strings.Count(b, "The hunk itself has already been reviewed") != 1 {
				t.Errorf("pass B sentence must appear once")
			}
			for name, p := range map[string]string{"A": a, "B": b} {
				if preMerge {
					if !strings.Contains(p, preMergeAboutToPushSentence) || strings.Contains(p, postMergeAlreadyMergedSentence) {
						t.Errorf("pass %s: pre-merge sentence swap missing", name)
					}
				} else if !strings.Contains(p, postMergeAlreadyMergedSentence) {
					t.Errorf("pass %s: post-merge sentence missing", name)
				}
			}
			// Everything but item 2 and the one sentence is identical.
			norm := strings.NewReplacer(postMergeChecklistItem2Checkout, "ITEM2", postMergeChecklistItemAdjacent, "ITEM2", " "+postMergeAdjacentPassSentence, "")
			if norm.Replace(a) != norm.Replace(b) {
				t.Errorf("prompts differ beyond item 2 and the added sentence")
			}
		})
	}
}

func TestPostMergeReviewTwoPass_SinglePassUnchanged(t *testing.T) {
	repo, sha := initCheckoutRepo(t)
	var prompts []string
	r := newPostMergeReviewerWithDirRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		p, _ := flagValue(args, "-p")
		prompts = append(prompts, p)
		return passAnswer(t, answerApproveMinorA, 1), nil
	})
	in := checkoutInput(repo, sha)
	for _, setter := range []func(){func() {}, func() { r.SetPasses(1) }, func() { r.SetPasses(0) }, func() { r.SetPasses(7) }} {
		setter()
		prompts = nil
		got, err := r.Review(context.Background(), in)
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		if len(prompts) != 1 {
			t.Fatalf("invocations = %d, want 1", len(prompts))
		}
		// The prompt the single pass built before passes existed.
		want := postMergeReviewerSystemPrompt
		want = strings.Replace(want, postMergeChecklistItem2, postMergeChecklistItem2Checkout+"\n"+postMergeChecklistItemAdjacent, 1)
		want = strings.Replace(want, "\n\nIMPORTANT — truncation handling", "\n\n"+postMergeCheckoutPrompt+"\n\nIMPORTANT — truncation handling", 1)
		if !strings.HasPrefix(prompts[0], want+"\n\n## Issue Title\n") {
			t.Errorf("single-pass prompt changed")
		}
		if strings.Contains(prompts[0], postMergeAdjacentPassSentence) {
			t.Errorf("single-pass prompt carries the pass B sentence")
		}
		if got.Passes != 0 || got.PassFailed != nil || got.FilesReadByPass != nil || got.DefectsByPass != nil || got.Overlap != 0 || got.EffectivePasses() != 1 {
			t.Errorf("single-pass result carries two-pass stats: %+v", got)
		}
		if got.Verdict != PostMergeApprove || len(got.Defects) != 2 {
			t.Errorf("single-pass result = verdict %q defects %d, want the model's own APPROVE and 2 defects", got.Verdict, len(got.Defects))
		}
	}
}

func TestPostMergeReviewTwoPass_PassBTimeout(t *testing.T) {
	r, s, _, in := twoPassReviewer(t,
		fixed(passAnswer(t, answerApproveMinorA, 1)),
		func(string) ([]byte, error) { return nil, context.DeadlineExceeded })
	got, err := r.Review(context.Background(), in)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(s.dirs) != 2 {
		t.Fatalf("invocations = %d, want 2", len(s.dirs))
	}
	if got.Passes != 2 || len(got.PassFailed) != 2 || got.PassFailed[0] || !got.PassFailed[1] {
		t.Errorf("passes=%d failed=%v, want 2 with only pass B failed", got.Passes, got.PassFailed)
	}
	if len(got.Defects) != 2 || got.Verdict != PostMergeApproveWithNotes {
		t.Errorf("defects=%d verdict=%q, want pass A's defects alone under the derived APPROVE-WITH-NOTES", len(got.Defects), got.Verdict)
	}
	if got.Overlap != 0 {
		t.Errorf("overlap = %d, want 0", got.Overlap)
	}
}

func TestPostMergeReviewTwoPass_BothPassesFail(t *testing.T) {
	boom := errors.New("boom")
	r, _, _, in := twoPassReviewer(t,
		func(string) ([]byte, error) { return nil, boom },
		func(string) ([]byte, error) { return nil, boom })
	if _, err := r.Review(context.Background(), in); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the subprocess error", err)
	}
}

func TestPostMergeReviewTwoPass_UnusablePassContributesNothing(t *testing.T) {
	r, _, _, in := twoPassReviewer(t,
		fixed(passAnswer(t, "I could not review this.", 1)),
		fixed(passAnswer(t, answerBlockingB, 1)))
	got, err := r.Review(context.Background(), in)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got.Verdict != PostMergeRequestChanges || !got.PassFailed[0] || got.PassFailed[1] {
		t.Errorf("verdict=%q failed=%v, want pass B's REQUEST-CHANGES with pass A marked failed", got.Verdict, got.PassFailed)
	}
}

func TestPostMergeReviewTwoPass_FullTimeoutPerPass(t *testing.T) {
	r, s, _, in := twoPassReviewer(t,
		fixed(passAnswer(t, answerApproveMinorA, 1)), fixed(passAnswer(t, answerApproveMinorA, 1)))
	r.timeout = 10 * time.Minute
	start := time.Now()
	if _, err := r.Review(context.Background(), in); err != nil {
		t.Fatalf("Review: %v", err)
	}
	for i, dl := range s.deadline {
		if left := dl.Sub(start); left < 9*time.Minute+30*time.Second || left > 10*time.Minute+30*time.Second {
			t.Errorf("pass %d deadline = %s after start, want the full 10m timeout", i+1, left)
		}
	}
}

func TestPostMergeReviewTwoPass_PassContextBoundedByParent(t *testing.T) {
	r, s, _, in := twoPassReviewer(t,
		fixed(passAnswer(t, answerApproveMinorA, 1)), fixed(passAnswer(t, answerApproveMinorA, 1)))
	r.timeout = 10 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	start := time.Now()
	if _, err := r.Review(ctx, in); err != nil {
		t.Fatalf("Review: %v", err)
	}
	for i, dl := range s.deadline {
		if left := dl.Sub(start); left > 3*time.Minute+30*time.Second {
			t.Errorf("pass %d deadline = %s after start, want it bounded by the caller's 3m", i+1, left)
		}
	}
}

func TestPostMergeReviewTwoPass_KeyNormalisesLocation(t *testing.T) {
	forms := []string{"a.go:589 - the claim", "a.go:589-590", "a.go:L589", "a.go:589 (func X)", "a.go:589,590", "./a.go:589", "`a.go:589`", "/co/a.go:589 (func X)"}
	want := defectKey(PostMergeDefect{Location: "a.go:589", Claim: "c1"}, "/co")
	if want != "a.go:589" {
		t.Fatalf("key of a.go:589 = %q", want)
	}
	for _, loc := range forms {
		if got := defectKey(PostMergeDefect{Location: loc, Claim: "other claim"}, "/co"); got != want {
			t.Errorf("defectKey(%q) = %q, want %q", loc, got, want)
		}
	}
	if got := defectKey(PostMergeDefect{Location: "a.go:590"}, "/co"); got == want {
		t.Errorf("defectKey(a.go:590) = %q, must differ from %q", got, want)
	}
	t.Run("no line falls back to path and claim", func(t *testing.T) {
		a := defectKey(PostMergeDefect{Location: "a.go (whole file)", Claim: "Missing  guard"}, "")
		b := defectKey(PostMergeDefect{Location: "a.go", Claim: "missing guard"}, "")
		c := defectKey(PostMergeDefect{Location: "a.go", Claim: "something else"}, "")
		if a != b || a == c || a != "a.go|missing guard" {
			t.Errorf("keys = %q, %q, %q", a, b, c)
		}
	})
}

func TestPostMergeReviewTwoPass_UnionUnitesLocationForms(t *testing.T) {
	r, _, _, in := twoPassReviewer(t,
		fixed(passAnswer(t, "VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\n- [minor] a.go:589 (func X) - claim - ev\nNOT-VERIFIED:\nnone\nNOTES:\n", 1)),
		fixed(passAnswer(t, "VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\n- [minor] a.go:L589 - claim again - longer evidence\nNOT-VERIFIED:\nnone\nNOTES:\n", 1)))
	got, err := r.Review(context.Background(), in)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(got.Defects) != 1 || got.Overlap != 1 {
		t.Fatalf("defects=%+v overlap=%d, want one united defect and overlap 1", got.Defects, got.Overlap)
	}
	if got.Defects[0].Location != "a.go:589" || got.Defects[0].Evidence != "longer evidence" {
		t.Errorf("kept defect = %+v, want location a.go:589 with the longer evidence", got.Defects[0])
	}
}

func TestPostMergeReviewTwoPass_NotVerifiedCapped(t *testing.T) {
	r, _, _, in := twoPassReviewer(t,
		fixed(passAnswer(t, "VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\nnone\nNOT-VERIFIED:\na1 not in the diff\na2 truncated\na3 cut\nNOTES:\n", 1)),
		fixed(passAnswer(t, "VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\nnone\nNOT-VERIFIED:\nb1 not in the diff\nb2 truncated\nb3 cut\nNOTES:\n", 1)))
	got, err := r.Review(context.Background(), in)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	want := []string{"a1 not in the diff", "a2 truncated", "4 more items the passes could not verify (capped at 3)"}
	if len(got.NotVerified) != MaxPostMergeNotVerified || strings.Join(got.NotVerified, "|") != strings.Join(want, "|") {
		t.Errorf("not-verified = %q, want %q", got.NotVerified, want)
	}
	if got.Verdict != PostMergeApproveWithNotes {
		t.Errorf("verdict = %q", got.Verdict)
	}
}

func TestPostMergeReviewTwoPass_SinglePassDerivedVerdict(t *testing.T) {
	// Pass B fails; pass A says APPROVE but lists two minor defects. The verdict
	// is derived from the defects, not the VERDICT line.
	r, _, _, in := twoPassReviewer(t,
		fixed(passAnswer(t, answerApproveMinorA, 1)),
		func(string) ([]byte, error) { return nil, context.DeadlineExceeded })
	got, err := r.Review(context.Background(), in)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(got.Defects) != 2 || got.Verdict != PostMergeApproveWithNotes {
		t.Errorf("defects=%d verdict=%q, want 2 and APPROVE-WITH-NOTES", len(got.Defects), got.Verdict)
	}
	if got.PassFailReason[1] != "error" || got.PassFailReason[0] != "" {
		t.Errorf("fail reasons = %q", got.PassFailReason)
	}
}

func TestDerivePostMergeVerdict(t *testing.T) {
	blocking := PostMergeDefect{Severity: PostMergeSeverityBlocking}
	minor := PostMergeDefect{Severity: PostMergeSeverityMinor}
	tests := []struct {
		name   string
		review PostMergeReview
		want   PostMergeVerdict
	}{
		{"clean", PostMergeReview{}, PostMergeApprove},
		{"minor", PostMergeReview{Defects: []PostMergeDefect{minor}}, PostMergeApproveWithNotes},
		{"not verified", PostMergeReview{NotVerified: []string{"x"}}, PostMergeApproveWithNotes},
		{"blocking", PostMergeReview{Defects: []PostMergeDefect{minor, blocking}}, PostMergeRequestChanges},
		{"blocking but no files", PostMergeReview{Defects: []PostMergeDefect{blocking}, NoFilesRead: true}, PostMergeApproveWithNotes},
		{"no files, no defects", PostMergeReview{NoFilesRead: true}, PostMergeApproveWithNotes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := derivePostMergeVerdict(&tt.review); got != tt.want {
				t.Errorf("verdict = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPostMergeReviewTwoPass_NoFilesBlockingDemoted(t *testing.T) {
	t.Run("arrives as minor", func(t *testing.T) {
		r, _, _, in := twoPassReviewer(t, fixed(passAnswer(t, answerApproveMinorA, 1)), fixed(passAnswer(t, answerBlockingB, 0)))
		got, err := r.Review(context.Background(), in)
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		if n := len(got.BlockingDefects()); n != 0 {
			t.Errorf("blocking defects = %d, want 0 (the no-files pass's blocking is demoted)", n)
		}
		var c *PostMergeDefect
		for i := range got.Defects {
			if strings.HasPrefix(got.Defects[i].Location, "c.go") {
				c = &got.Defects[i]
			}
		}
		if c == nil || c.Severity != PostMergeSeverityMinor || !strings.HasSuffix(c.Evidence, "(pass read no files)") {
			t.Errorf("c.go defect = %+v, want [minor] with the no-files evidence suffix", c)
		}
		if got.Verdict != PostMergeApproveWithNotes {
			t.Errorf("verdict = %q, want APPROVE-WITH-NOTES", got.Verdict)
		}
	})
	t.Run("does not raise a kept defect", func(t *testing.T) {
		minorA := "VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\n- [minor] c.go:7 - claim - ev\nNOT-VERIFIED:\nnone\nNOTES:\n"
		r, _, _, in := twoPassReviewer(t, fixed(passAnswer(t, minorA, 1)), fixed(passAnswer(t, answerBlockingB, 0)))
		got, err := r.Review(context.Background(), in)
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		if len(got.Defects) != 1 || got.Defects[0].Severity != PostMergeSeverityMinor || got.Defects[0].Evidence != "ev" {
			t.Errorf("defects = %+v, want pass A's [minor] kept untouched", got.Defects)
		}
		if got.Verdict != PostMergeApproveWithNotes {
			t.Errorf("verdict = %q", got.Verdict)
		}
	})
	t.Run("single usable no-files pass", func(t *testing.T) {
		r, _, _, in := twoPassReviewer(t, fixed(passAnswer(t, answerBlockingB, 0)), func(string) ([]byte, error) { return nil, errors.New("boom") })
		got, err := r.Review(context.Background(), in)
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		if len(got.BlockingDefects()) != 0 || got.Verdict != PostMergeApproveWithNotes {
			t.Errorf("defects=%+v verdict=%q, want the blocking demoted under APPROVE-WITH-NOTES", got.Defects, got.Verdict)
		}
	})
}

func TestPostMergeReviewTwoPass_PreMergeSkipsPassBUnderBudget(t *testing.T) {
	slowA := func(string) ([]byte, error) {
		time.Sleep(150 * time.Millisecond)
		return passAnswer(t, answerApproveMinorA, 1), nil
	}
	tests := []struct {
		name      string
		preMerge  bool
		wantCalls int
	}{
		{"pre-merge skips pass B", true, 1},
		{"post-merge still runs pass B", false, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, s, _, in := twoPassReviewer(t, slowA, fixed(passAnswer(t, answerApproveMinorA, 1)))
			r.timeout = 200 * time.Millisecond
			in.PreMerge = tt.preMerge
			// The caller leaves 300ms: after pass A's 150ms, less than one timeout remains.
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			got, err := r.Review(ctx, in)
			if err != nil {
				t.Fatalf("Review: %v", err)
			}
			if len(s.dirs) != tt.wantCalls {
				t.Fatalf("invocations = %d, want %d", len(s.dirs), tt.wantCalls)
			}
			if tt.preMerge {
				if !got.PassFailed[1] || got.PassFailReason[1] != "budget" || got.PassFailed[0] {
					t.Errorf("failed=%v reasons=%q, want only pass B failed with reason budget", got.PassFailed, got.PassFailReason)
				}
				if got.Verdict != PostMergeApproveWithNotes || len(got.Defects) != 2 {
					t.Errorf("verdict=%q defects=%d, want pass A's result under the derived verdict", got.Verdict, len(got.Defects))
				}
			}
		})
	}
}

func TestPostMergeReviewTwoPass_NotesAndNotVerified(t *testing.T) {
	r, _, _, in := twoPassReviewer(t,
		fixed(passAnswer(t, "VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\nnone\nNOT-VERIFIED:\nx.go not in the diff\ny.go truncated\nNOTES:\nnote from A", 1)),
		fixed(passAnswer(t, "VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\nnone\nNOT-VERIFIED:\nX.go not in the diff\nz.go cut\nNOTES:\nnote from B", 1)))
	got, err := r.Review(context.Background(), in)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(got.NotVerified) != 3 {
		t.Errorf("not-verified = %v, want the 3-element set", got.NotVerified)
	}
	for _, want := range []string{"[pass A (hunk)]\nnote from A", "[pass B (adjacent)]\nnote from B"} {
		if !strings.Contains(got.Notes, want) {
			t.Errorf("notes %q lack %q", got.Notes, want)
		}
	}
}

func TestPostMergeReviewTwoPass_Downgrades(t *testing.T) {
	downgraded := "VERDICT: REQUEST-CHANGES\nDEFECTS:\n- [minor] a.go:1 - c - e\nNOT-VERIFIED:\nnone\nNOTES:\n"
	t.Run("both downgraded", func(t *testing.T) {
		r, _, _, in := twoPassReviewer(t, fixed(passAnswer(t, downgraded, 1)), fixed(passAnswer(t, downgraded, 1)))
		got, err := r.Review(context.Background(), in)
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		if !got.Downgraded || got.Verdict != PostMergeApproveWithNotes {
			t.Errorf("downgraded=%v verdict=%q, want true and APPROVE-WITH-NOTES", got.Downgraded, got.Verdict)
		}
	})
	t.Run("one downgraded", func(t *testing.T) {
		r, _, _, in := twoPassReviewer(t, fixed(passAnswer(t, downgraded, 1)), fixed(passAnswer(t, answerBlockingB, 1)))
		got, err := r.Review(context.Background(), in)
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		if got.Downgraded || got.Verdict != PostMergeRequestChanges {
			t.Errorf("downgraded=%v verdict=%q, want false and REQUEST-CHANGES", got.Downgraded, got.Verdict)
		}
	})
	t.Run("a pass that read nothing cannot escalate", func(t *testing.T) {
		r, _, _, in := twoPassReviewer(t, fixed(passAnswer(t, answerApproveMinorA, 1)), fixed(passAnswer(t, answerBlockingB, 0)))
		got, err := r.Review(context.Background(), in)
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		if got.Verdict != PostMergeApproveWithNotes || got.NoFilesRead {
			t.Errorf("verdict=%q noFiles=%v, want APPROVE-WITH-NOTES with NoFilesRead false (pass A read files)", got.Verdict, got.NoFilesRead)
		}
	})
	t.Run("both read nothing", func(t *testing.T) {
		r, _, _, in := twoPassReviewer(t, fixed(passAnswer(t, "VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n", 0)), fixed(passAnswer(t, "VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n", 0)))
		got, err := r.Review(context.Background(), in)
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		if !got.NoFilesRead || got.Verdict != PostMergeApproveWithNotes {
			t.Errorf("noFiles=%v verdict=%q, want true and APPROVE-WITH-NOTES", got.NoFilesRead, got.Verdict)
		}
	})
}

func TestPostMergeReviewTwoPass_DiffOnlyRunsOnePass(t *testing.T) {
	calls := 0
	r := newPostMergeReviewerWithRunner(func(context.Context, ...string) ([]byte, error) {
		calls++
		return []byte("VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n"), nil
	})
	r.SetPasses(2)
	got, err := r.Review(context.Background(), PostMergeReviewInput{PRNumber: 1, Diff: checkoutDiff})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if calls != 1 || got.Passes != 0 {
		t.Errorf("calls=%d passes=%d, want one diff-only pass without two-pass stats", calls, got.Passes)
	}
}

func TestPostMergeReviewerSetPasses(t *testing.T) {
	r := NewPostMergeReviewer("claude", "", 0)
	if r.Passes() != 1 {
		t.Errorf("default passes = %d, want 1", r.Passes())
	}
	for in, want := range map[int]int{2: 2, 1: 1, 0: 1, -1: 1, 3: 1} {
		r.SetPasses(in)
		if r.Passes() != want {
			t.Errorf("SetPasses(%d) -> %d, want %d", in, r.Passes(), want)
		}
	}
}
