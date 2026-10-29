package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

func prePROutcome(verdict PostMergeVerdict) *PrePRReviewOutcome {
	return &PrePRReviewOutcome{
		Mode:    PrePRReviewModeShadow,
		Verdict: verdict,
		Review: &PostMergeReview{
			Verdict: verdict,
			Defects: []PostMergeDefect{
				{Severity: PostMergeSeverityBlocking, Location: "internal/foo/bar.go:42", Claim: "binds `field` the sender never sets", Evidence: "see sender.go:10"},
				{Severity: PostMergeSeverityMinor, Location: "internal/foo/baz.go:7", Claim: "stale comment"},
			},
			NotVerified: []string{"live smoke against the box — needs a key"},
		},
	}
}

func TestRenderPrePRReviewSection(t *testing.T) {
	tests := []struct {
		name    string
		out     *PrePRReviewOutcome
		want    []string
		wantNot []string
		empty   bool
	}{
		{
			name: "request changes lists defects with severity tags and the not-verified list",
			out:  prePROutcome(PostMergeRequestChanges),
			want: []string{
				"## Pre-PR review", "Mode: shadow", "Verdict: REQUEST-CHANGES", "Fix: none",
				"- [blocking] at internal/foo/bar.go:42 — binds field the sender never sets",
				"- [minor] at internal/foo/baz.go:7 — stale comment",
				"Not-verified:", "- live smoke against the box — needs a key",
			},
			wantNot: []string{"`", "Evidence", "## Not verified", "### "},
		},
		{
			name: "approve without defects has no defect or not-verified list",
			out: &PrePRReviewOutcome{Mode: PrePRReviewModeShadow, Verdict: PostMergeApprove,
				Review: &PostMergeReview{Verdict: PostMergeApprove}},
			want:    []string{"Verdict: APPROVE", "Fix: none"},
			wantNot: []string{"Defects:", "Not-verified:"},
		},
		{name: "nil outcome renders nothing", out: nil, empty: true},
		{name: "skipped review renders nothing", out: &PrePRReviewOutcome{Mode: PrePRReviewModeShadow, Skipped: "empty diff"}, empty: true},
		{name: "unknown verdict renders nothing", out: &PrePRReviewOutcome{Mode: PrePRReviewModeShadow, Review: &PostMergeReview{Verdict: PostMergeUnknown}}, empty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderPrePRReviewSection(tt.out)
			if tt.empty {
				if got != "" {
					t.Fatalf("want empty section, got %q", got)
				}
				return
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("section missing %q\n---\n%s", w, got)
				}
			}
			for _, w := range tt.wantNot {
				if strings.Contains(got, w) {
					t.Errorf("section must not contain %q\n---\n%s", w, got)
				}
			}
		})
	}
}

func TestParsePrePRReviewSection_RoundTrip(t *testing.T) {
	tests := []struct {
		name        string
		verdict     PostMergeVerdict
		bodySuffix  string
		bodyPrefix  string
		wantFix     string
		withDefects bool
	}{
		{name: "request changes", verdict: PostMergeRequestChanges, bodyPrefix: "## Summary\n\nbody\n\n## Not verified\n\n- thing\n", wantFix: "none", withDefects: true},
		{name: "approve with notes then another section", verdict: PostMergeApproveWithNotes, bodySuffix: "\n\n## Changes\n\nVerdict: APPROVE\nMode: off\n", wantFix: "none"},
		{name: "approve", verdict: PostMergeApprove, wantFix: "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := prePROutcome(tt.verdict)
			if !tt.withDefects {
				out.Review.Defects = nil
			}
			section := RenderPrePRReviewSection(out)
			// The heading is the contract autopilot and the sampler parse by: pin the
			// literal, not the constant, so a rename cannot slip through both sides.
			if !strings.Contains(section, "\n## Pre-PR review\n") {
				t.Fatalf("section must carry the literal H2 %q, got:\n%s", "## Pre-PR review", section)
			}
			body := tt.bodyPrefix + section + tt.bodySuffix
			mode, verdict, fix, ok := ParsePrePRReviewSection(body)
			if !ok {
				t.Fatalf("section not parsed from:\n%s", body)
			}
			if mode != PrePRReviewModeShadow || verdict != tt.verdict || fix != tt.wantFix {
				t.Errorf("got mode=%q verdict=%q fix=%q, want %q %q %q", mode, verdict, fix, PrePRReviewModeShadow, tt.verdict, tt.wantFix)
			}
		})
	}
}

// TestParsePrePRReviewSection_LiteralBody parses a hand-written body, so the
// parser is held to the documented format and not just to the renderer.
func TestParsePrePRReviewSection_LiteralBody(t *testing.T) {
	body := "## Summary\n\nx\n\n## Pre-PR review\n\nMode: shadow\nVerdict: REQUEST-CHANGES\nFix: none\n\nDefects:\n- [blocking] at a.go:1 — b\n"
	mode, verdict, fix, ok := ParsePrePRReviewSection(body)
	if !ok || mode != "shadow" || verdict != PostMergeRequestChanges || fix != "none" {
		t.Errorf("got %q %q %q ok=%v", mode, verdict, fix, ok)
	}
}

func TestParsePrePRReviewSection_NotOK(t *testing.T) {
	tests := []struct{ name, body string }{
		{"empty", ""},
		{"no section", "## Summary\n\nVerdict: APPROVE\nMode: shadow\n"},
		{"heading only", "## Pre-PR review\n\n"},
		{"unknown verdict", "## Pre-PR review\n\nMode: shadow\nVerdict: unknown\nFix: none\n"},
		{"fields past the next heading", "## Pre-PR review\n\n## Changes\n\nMode: shadow\nVerdict: APPROVE\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, _, ok := ParsePrePRReviewSection(tt.body); ok {
				t.Errorf("body must not parse:\n%s", tt.body)
			}
		})
	}
}

// TestRenderPrePRReviewSection_NotHeldAsLiveSmoke pins that the section's
// Not-verified list is invisible to autopilot's "## Not verified" hold.
func TestRenderPrePRReviewSection_NotHeldAsLiveSmoke(t *testing.T) {
	body := "## Summary\n\nx" + RenderPrePRReviewSection(prePROutcome(PostMergeRequestChanges))
	if got := LiveSmokeNotVerifiedBullets(body); len(got) != 0 {
		t.Errorf("pre-PR Not-verified list must not trip the live-smoke hold, got %v", got)
	}
}

// ghShim puts a gh on PATH that logs each call's args to the returned file.
func ghShim(t *testing.T) (logFile string) {
	t.Helper()
	bin := t.TempDir()
	logFile = filepath.Join(t.TempDir(), "gh.log")
	script := "#!/bin/sh\necho \"$@\" >> \"" + logFile + "\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write gh shim: %v", err)
	}
	t.Setenv("PATH", bin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return logFile
}

func prePRPublishTask() *Task {
	return &Task{ID: "GH-5956", ExecutionID: "exec-5956", ProjectPath: "/tmp/proj"}
}

func TestPrePRReviewLabel_OnlyOnRC(t *testing.T) {
	tests := []struct {
		name      string
		verdict   PostMergeVerdict
		prURL     string
		wantLabel bool
	}{
		{"request changes labels the PR", PostMergeRequestChanges, "https://github.com/o/r/pull/77", true},
		{"approve with notes does not", PostMergeApproveWithNotes, "https://github.com/o/r/pull/77", false},
		{"approve does not", PostMergeApprove, "https://github.com/o/r/pull/77", false},
		{"no verdict does not", "", "https://github.com/o/r/pull/77", false},
		{"merge request url has no PR number", PostMergeRequestChanges, "https://gitlab.com/o/r/-/merge_requests/9", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logFile := ghShim(t)
			r := newSilentRunnerTask359()
			out := prePROutcome(tt.verdict)
			r.publishPrePRReview(context.Background(), prePRPublishTask(), "", tt.prURL, out)

			raw, _ := os.ReadFile(logFile)
			calls := string(raw)
			got := strings.Contains(calls, "issue edit 77 --add-label "+PrePRReviewRCLabel)
			if got != tt.wantLabel {
				t.Errorf("label added = %v, want %v; gh calls:\n%s", got, tt.wantLabel, calls)
			}
			if !tt.wantLabel && strings.Contains(calls, PrePRReviewRCLabel) {
				t.Errorf("label %q must not be touched; gh calls:\n%s", PrePRReviewRCLabel, calls)
			}
		})
	}
}

func prePRLedgerRunner(t *testing.T) (*Runner, *memory.Store) {
	t.Helper()
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	r := newSilentRunnerTask359()
	r.logStore = store
	return r, store
}

func TestPrePRReviewLedgerRow_ShadowWritesPrePRReviewerID(t *testing.T) {
	ghShim(t)
	tests := []struct {
		verdict     PostMergeVerdict
		wantVerdict string
	}{
		{PostMergeRequestChanges, memory.VerdictRequestChanges},
		{PostMergeApproveWithNotes, memory.VerdictApproveWNotes},
		{PostMergeApprove, memory.VerdictApprove},
	}
	for _, tt := range tests {
		t.Run(string(tt.verdict), func(t *testing.T) {
			r, store := prePRLedgerRunner(t)
			task := prePRPublishTask()
			r.publishPrePRReview(context.Background(), task, "", "https://github.com/o/r/pull/77", prePROutcome(tt.verdict))

			rows, err := store.ListPRReviewsByExecution(task.ExecutionID)
			if err != nil {
				t.Fatalf("ListPRReviewsByExecution: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("want 1 row, got %d", len(rows))
			}
			row := rows[0]
			if row.Reviewer != "pre-pr-reviewer" {
				t.Errorf("reviewer = %q, want pre-pr-reviewer", row.Reviewer)
			}
			if row.ReviewID >= 0 {
				t.Errorf("review_id = %d, want negative", row.ReviewID)
			}
			if row.ReviewID != prePRReviewID(task.ExecutionID) {
				t.Errorf("review_id = %d, want %d", row.ReviewID, prePRReviewID(task.ExecutionID))
			}
			if row.PRNumber != 77 || row.ProjectPath != task.ProjectPath {
				t.Errorf("pr_number/project = %d %q, want 77 %q", row.PRNumber, row.ProjectPath, task.ProjectPath)
			}
			if row.State != "COMMENTED" || row.Verdict != tt.wantVerdict {
				t.Errorf("state/verdict = %q %q, want COMMENTED %q", row.State, row.Verdict, tt.wantVerdict)
			}

			// A second publish for the same execution refreshes the row.
			r.publishPrePRReview(context.Background(), task, "", "https://github.com/o/r/pull/77", prePROutcome(tt.verdict))
			if rows, _ = store.ListPRReviewsByExecution(task.ExecutionID); len(rows) != 1 {
				t.Errorf("republish must upsert, got %d rows", len(rows))
			}
		})
	}
}

func TestPrePRReviewLedgerRow_NoRowWithoutVerdictOrPR(t *testing.T) {
	ghShim(t)
	tests := []struct {
		name  string
		out   *PrePRReviewOutcome
		prURL string
	}{
		{"no verdict", &PrePRReviewOutcome{Mode: PrePRReviewModeShadow, Skipped: "empty diff"}, "https://github.com/o/r/pull/77"},
		{"nil outcome", nil, "https://github.com/o/r/pull/77"},
		{"no pr number", prePROutcome(PostMergeApprove), "https://gitlab.com/o/r/-/merge_requests/9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, store := prePRLedgerRunner(t)
			task := prePRPublishTask()
			r.publishPrePRReview(context.Background(), task, "", tt.prURL, tt.out)
			if rows, err := store.ListPRReviewsByExecution(task.ExecutionID); err != nil || len(rows) != 0 {
				t.Errorf("want no rows, got %d (err %v)", len(rows), err)
			}
		})
	}
}

func TestPrePRReviewID(t *testing.T) {
	a, b := prePRReviewID("exec-a"), prePRReviewID("exec-b")
	if a >= 0 || b >= 0 {
		t.Errorf("ids must be negative, got %d %d", a, b)
	}
	if a == b {
		t.Error("distinct executions must get distinct ids")
	}
	if a != prePRReviewID("exec-a") {
		t.Error("id must be stable per execution")
	}
	if -a >= 1<<62 {
		t.Errorf("id %d exceeds 62 bits", a)
	}
}

func TestPrePRReviewCounters(t *testing.T) {
	before := PrePRReviewsTotal()["REQUEST-CHANGES"]
	RecordPrePRReview(PostMergeRequestChanges)
	if got := PrePRReviewsTotal()["REQUEST-CHANGES"]; got != before+1 {
		t.Errorf("reviews counter = %d, want %d", got, before+1)
	}
	beforeFix := PrePRFixesTotal()["reserved-test"]
	RecordPrePRFix("reserved-test")
	if got := PrePRFixesTotal()["reserved-test"]; got != beforeFix+1 {
		t.Errorf("fixes counter = %d, want %d", got, beforeFix+1)
	}
}

func TestLedgerVerdict(t *testing.T) {
	tests := []struct {
		in   PostMergeVerdict
		want string
	}{
		{PostMergeApprove, memory.VerdictApprove},
		{PostMergeApproveWithNotes, memory.VerdictApproveWNotes},
		{PostMergeRequestChanges, memory.VerdictRequestChanges},
		{PostMergeUnknown, ""},
		{PostMergeExhausted, ""},
	}
	for _, tt := range tests {
		if got := LedgerVerdict(tt.in); got != tt.want {
			t.Errorf("LedgerVerdict(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
