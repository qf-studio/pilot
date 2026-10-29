package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH-5947: the checkout review must trace code outside the diff and cite
// path:line; the daemon enforces both when the model does not.

func TestPostMergeReviewCitation_PromptCarriesTracingRules(t *testing.T) {
	repo, sha := initCheckoutRepo(t)
	var prompt string
	r := newPostMergeReviewerWithDirRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		prompt, _ = flagValue(args, "-p")
		return []byte(streamLine(t, streamToolUse("Read", "/a")) +
			streamLine(t, map[string]any{"type": "result", "result": "VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n"})), nil
	})
	if _, err := r.Review(context.Background(), checkoutInput(repo, sha)); err != nil {
		t.Fatalf("Review: %v", err)
	}
	for _, want := range []string{
		"Grep the checkout for its callers and open at least the first two",
		"open the producer or consumer file",
		"cite the checkout file:line that satisfies it",
		`marked "met" only with a citation of the form path:line from the checkout`,
		"the diff alone does not count",
		`the reason "not traced"`,
		"must quote the line it rests on",
		"at most 30 tool turns",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("checkout prompt lacks %q", want)
		}
	}

	// The diff-only prompt is unchanged: there a diff line is the evidence.
	var diffOnly string
	r = newPostMergeReviewerWithRunner(func(_ context.Context, args ...string) ([]byte, error) {
		diffOnly, _ = flagValue(args, "-p")
		return []byte("VERDICT: APPROVE\nDEFECTS:\nnone\n"), nil
	})
	if _, err := r.Review(context.Background(), PostMergeReviewInput{PRNumber: 1, Diff: checkoutDiff}); err != nil {
		t.Fatalf("diff-only Review: %v", err)
	}
	if strings.Contains(diffOnly, "Tracing rules") {
		t.Error("diff-only prompt carries the checkout tracing rules")
	}
}

func TestPostMergeCheckoutArgs_MaxTurns30(t *testing.T) {
	v, ok := flagValue(postMergeCheckoutArgs(), "--max-turns")
	if !ok || v != "30" {
		t.Errorf("--max-turns = %q (present %v), want 30", v, ok)
	}
	if defaultPostMergeReviewTimeout.Minutes() != 5 {
		t.Errorf("review timeout = %v, want it unchanged at 5m", defaultPostMergeReviewTimeout)
	}
}

func TestPostMergeReviewCitation_Regex(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"met (DashboardView.vue:305-317; test at spec:465)", true},
		{"UpsertPRReview is at `internal/memory/pr_review.go:93`", true},
		{"mockAdapter.ts:1066 stats is only { shipped, needsYou }", true},
		{"truncatePreflightBody (intent_judge.go:302)", true},
		{"every controller in main.go (lines 1021, 2559, 2630)", false},
		{"Knob in `types.go`, `config.go` and `pilot.example.yaml`: met.", false},
		{"met in the view via dash()", false},
		{"version v2.298.0 shipped", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := checkoutCitationRe.MatchString(tt.in); got != tt.want {
			t.Errorf("checkoutCitationRe(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestPostMergeReviewCitation_ClaimsMet(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"- Posting: met. One comment is posted.", true},
		{"Review needed is a RouterLink: met (DashboardView.vue:308)", true},
		{"- **Met:** the knob exists", true},
		{"- **Met:**", false},
		{"Met:", false},
		{"the data binding is not met, because no producer supplies it", false},
		{"requirement never met", false},
		{"the acceptance bullet isn't met", false},
		{"an unmet requirement", false},
		{"the markup is met but the data binding is not met", true},
		{"nothing relevant here", false},
	}
	for _, tt := range tests {
		if got := claimsMet(tt.in); got != tt.want {
			t.Errorf("claimsMet(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestPostMergeReviewCitation_UncitedMetMovesToNotVerified(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantVerdict  PostMergeVerdict
		wantNV       []string // substrings, one per NOT-VERIFIED entry
		wantNotes    []string
		wantNotNotes []string
		wantDefects  int
		wantUntraced int
	}{
		{
			name:         "met without citation moves; met with path:line stays",
			raw:          "VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n- Knob in config: met. It has four keys.\n- Trigger: met (controller.go:6217).",
			wantVerdict:  PostMergeApproveWithNotes,
			wantNV:       []string{"Knob in config: met. It has four keys. — not traced"},
			wantNotes:    []string{"controller.go:6217"},
			wantNotNotes: []string{"Knob in config"},
			wantUntraced: 1,
		},
		{
			name:         "all cited stays APPROVE",
			raw:          "VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n- Trigger: met (controller.go:6217).",
			wantVerdict:  PostMergeApprove,
			wantNotes:    []string{"controller.go:6217"},
			wantUntraced: 0,
		},
		{
			name:         "not met is a finding, not a claim",
			raw:          "VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n- Header binding is not met.",
			wantVerdict:  PostMergeApproveWithNotes,
			wantNotes:    []string{"Header binding is not met."},
			wantUntraced: 0,
		},
		{
			name:         "uncited blocking met-defect is moved and REQUEST-CHANGES lowered",
			raw:          "VERDICT: REQUEST-CHANGES\nDEFECTS:\n- [blocking] the requirement is met in the diff, but tests mock it\nNOT-VERIFIED:\nnone\nNOTES:\n",
			wantVerdict:  PostMergeApproveWithNotes,
			wantNV:       []string{"not traced"},
			wantDefects:  0,
			wantUntraced: 1,
		},
		{
			name:         "cited blocking defect keeps REQUEST-CHANGES",
			raw:          "VERDICT: REQUEST-CHANGES\nDEFECTS:\n- [blocking] a/b.go:12, field has no producer, server/h.go:4 emits only x\nNOT-VERIFIED:\nnone\nNOTES:\n- Req 1: met (a/b.go:12).",
			wantVerdict:  PostMergeRequestChanges,
			wantNotes:    []string{"a/b.go:12"},
			wantDefects:  1,
			wantUntraced: 0,
		},
		{
			name:         "overflow collapses into one entry within the cap",
			raw:          "VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n- A: met.\n- B: met.\n- C: met.\n- D: met.\n- E: met.",
			wantVerdict:  PostMergeApproveWithNotes,
			wantNV:       []string{"A: met. — not traced", "B: met. — not traced", "3 more requirements marked met without a path:line citation: not traced"},
			wantNotNotes: []string{"A: met", "E: met"},
			wantUntraced: 5,
		},
		{
			name:         "full NOT-VERIFIED list keeps the cap",
			raw:          "VERDICT: APPROVE-WITH-NOTES\nDEFECTS:\nnone\nNOT-VERIFIED:\n- one\n- two\n- three\nNOTES:\n- A: met.",
			wantVerdict:  PostMergeApproveWithNotes,
			wantNV:       []string{"one", "two", "1 requirements marked met without a path:line citation: not traced"},
			wantUntraced: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			review := parsePostMergeReview(tt.raw)
			enforcePostMergeCitations(review)

			if review.Verdict != tt.wantVerdict {
				t.Errorf("verdict = %q, want %q", review.Verdict, tt.wantVerdict)
			}
			if review.Untraced != tt.wantUntraced {
				t.Errorf("Untraced = %d, want %d", review.Untraced, tt.wantUntraced)
			}
			if len(review.NotVerified) > MaxPostMergeNotVerified {
				t.Errorf("NotVerified has %d entries, cap is %d", len(review.NotVerified), MaxPostMergeNotVerified)
			}
			if len(review.NotVerified) != len(tt.wantNV) {
				t.Fatalf("NotVerified = %q, want %d entries %q", review.NotVerified, len(tt.wantNV), tt.wantNV)
			}
			for i, want := range tt.wantNV {
				if !strings.Contains(review.NotVerified[i], want) {
					t.Errorf("NotVerified[%d] = %q, want it to contain %q", i, review.NotVerified[i], want)
				}
			}
			for _, want := range tt.wantNotes {
				if !strings.Contains(review.Notes, want) {
					t.Errorf("Notes %q lacks %q", review.Notes, want)
				}
			}
			for _, bad := range tt.wantNotNotes {
				if strings.Contains(review.Notes, bad) {
					t.Errorf("Notes %q still carries %q", review.Notes, bad)
				}
			}
			if len(review.Defects) != tt.wantDefects {
				t.Errorf("defects = %d, want %d", len(review.Defects), tt.wantDefects)
			}
		})
	}
}

// The rule applies through Review for a checkout run only; a diff-only review
// keeps its "met" lines.
func TestPostMergeReviewCitation_AppliedOnlyWithCheckout(t *testing.T) {
	repo, sha := initCheckoutRepo(t)
	answer := "VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n- Req 1: met."
	checkout := newPostMergeReviewerWithDirRunner(func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(streamLine(t, streamToolUse("Read", "/a")) +
			streamLine(t, map[string]any{"type": "result", "result": answer})), nil
	})
	got, err := checkout.Review(context.Background(), checkoutInput(repo, sha))
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got.Verdict != PostMergeApproveWithNotes || len(got.NotVerified) != 1 || got.NoFilesRead {
		t.Errorf("checkout: verdict=%q notVerified=%q noFiles=%v, want AWN with one not-traced entry", got.Verdict, got.NotVerified, got.NoFilesRead)
	}

	diffOnly := newPostMergeReviewerWithRunner(func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(answer), nil
	})
	got, err = diffOnly.Review(context.Background(), PostMergeReviewInput{PRNumber: 1, Diff: checkoutDiff})
	if err != nil {
		t.Fatalf("diff-only Review: %v", err)
	}
	if got.Verdict != PostMergeApprove || len(got.NotVerified) != 0 || got.NoFilesRead {
		t.Errorf("diff-only: verdict=%q notVerified=%q noFiles=%v, want untouched APPROVE", got.Verdict, got.NotVerified, got.NoFilesRead)
	}
}

// calibrationOutput is the part of one stored calibration
// output (the JSON written by `pilot review-merge`) the citation check reads.
type calibrationOutput struct {
	Verdict string `json:"verdict"`
	Notes   string `json:"notes"`
}

// The three calibration runs (root, run2, run3) are real reviewer outputs. The
// citation check must see the cited requirement lines as cited and the bare
// ones as uncited, without panicking on any of them.
func TestPostMergeReviewCitation_CalibrationOutputSets(t *testing.T) {
	root := filepath.Join("..", "..", ".agent", "system", "bench", "post-merge-reviewer-calibration-2026-10")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("calibration outputs not present: %v", err)
	}
	var cited, uncited, outputs int
	for _, dir := range []string{"", "run2", "run3"} {
		files, err := filepath.Glob(filepath.Join(root, dir, "pilot*.json"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no calibration outputs in %q (err %v)", dir, err)
		}
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var out calibrationOutput
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			outputs++
			for _, line := range strings.Split(out.Notes, "\n") {
				if !claimsMet(line) {
					continue
				}
				if checkoutCitationRe.MatchString(line) {
					cited++
				} else {
					uncited++
				}
			}
			review := parsePostMergeReview("VERDICT: " + out.Verdict + "\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n" + out.Notes)
			enforcePostMergeCitations(review)
			if len(review.NotVerified) > MaxPostMergeNotVerified {
				t.Errorf("%s: NotVerified over the cap: %d", f, len(review.NotVerified))
			}
			for _, line := range strings.Split(review.Notes, "\n") {
				if claimsMet(line) && !checkoutCitationRe.MatchString(line) {
					t.Errorf("%s: uncited met line survived: %q", f, line)
				}
			}
		}
	}
	// Run 3 (Opus) cites file:line on its met lines; runs 1 and 2 mostly do not.
	if outputs < 30 || cited == 0 || uncited == 0 {
		t.Errorf("outputs=%d cited=%d uncited=%d, want 3 sets of 14 with both kinds of met line", outputs, cited, uncited)
	}
	t.Logf("calibration: %d outputs, %d cited met lines, %d uncited met lines", outputs, cited, uncited)
}

func TestPostMergeReviewNoFilesReadDowngrade(t *testing.T) {
	repo, sha := initCheckoutRepo(t)
	const rc = "VERDICT: REQUEST-CHANGES\nDEFECTS:\n- [blocking] client/api.go:2, binds org_id nobody sends, no producer\nNOT-VERIFIED:\nnone\nNOTES:\n- Req: not met."
	approve := "VERDICT: APPROVE\nDEFECTS:\nnone\nNOT-VERIFIED:\nnone\nNOTES:\n"
	result := func(text string) map[string]any { return map[string]any{"type": "result", "result": text} }

	tests := []struct {
		name        string
		stream      string
		wantVerdict PostMergeVerdict
		wantFiles   int
		wantNoFiles bool
		wantFirst   bool
		wantDefects int
	}{
		{"zero reads downgrades REQUEST-CHANGES",
			streamLine(t, streamToolUse("Grep", "")) + streamLine(t, result(rc)),
			PostMergeApproveWithNotes, 0, true, true, 1},
		{"zero reads downgrades APPROVE too",
			streamLine(t, result(approve)),
			PostMergeApproveWithNotes, 0, true, true, 0},
		{"one read keeps REQUEST-CHANGES",
			streamLine(t, streamToolUse("Read", "/a.go")) + streamLine(t, result(rc)),
			PostMergeRequestChanges, 1, false, false, 1},
		{"N distinct reads are counted",
			streamLine(t, streamToolUse("Read", "/a.go")) + streamLine(t, streamToolUse("Read", "/b.go")) +
				streamLine(t, streamToolUse("Read", "/c.go")) + streamLine(t, streamToolUse("Read", "/a.go")) + streamLine(t, result(approve)),
			PostMergeApprove, 3, false, false, 0},
		{"plain-text output with a checkout counts as zero reads",
			rc,
			PostMergeApproveWithNotes, 0, true, true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newPostMergeReviewerWithDirRunner(func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				return []byte(tt.stream), nil
			})
			got, err := r.Review(context.Background(), checkoutInput(repo, sha))
			if err != nil {
				t.Fatalf("Review: %v", err)
			}
			if got.Verdict != tt.wantVerdict || got.FilesRead != tt.wantFiles || got.NoFilesRead != tt.wantNoFiles {
				t.Errorf("verdict=%q files=%d noFiles=%v, want %q/%d/%v", got.Verdict, got.FilesRead, got.NoFilesRead, tt.wantVerdict, tt.wantFiles, tt.wantNoFiles)
			}
			if !got.Checkout {
				t.Error("Checkout = false, want true")
			}
			if first := strings.HasPrefix(got.Notes, postMergeNoFilesReadNote); first != tt.wantFirst {
				t.Errorf("notes %q: first note is the no-files note = %v, want %v", got.Notes, first, tt.wantFirst)
			}
			if len(got.Defects) != tt.wantDefects {
				t.Errorf("defects = %d, want %d (a downgrade keeps the findings)", len(got.Defects), tt.wantDefects)
			}
		})
	}

	t.Run("exhausted review stays exhausted", func(t *testing.T) {
		r := newPostMergeReviewerWithDirRunner(func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(streamLine(t, map[string]any{"type": "result", "subtype": "error_max_turns", "num_turns": 31})), nil
		})
		got, err := r.Review(context.Background(), checkoutInput(repo, sha))
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		if got.Verdict != PostMergeExhausted || got.NoFilesRead {
			t.Errorf("verdict=%q noFiles=%v, want exhausted and not downgraded", got.Verdict, got.NoFilesRead)
		}
	})

	t.Run("diff-only review is not gated", func(t *testing.T) {
		r := newPostMergeReviewerWithRunner(func(_ context.Context, _ ...string) ([]byte, error) {
			return []byte(rc), nil
		})
		got, err := r.Review(context.Background(), PostMergeReviewInput{PRNumber: 1, Diff: checkoutDiff})
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		if got.Verdict != PostMergeRequestChanges || got.NoFilesRead {
			t.Errorf("verdict=%q noFiles=%v, want REQUEST-CHANGES untouched", got.Verdict, got.NoFilesRead)
		}
	})
}
