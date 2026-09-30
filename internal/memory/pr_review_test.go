package memory

import (
	"testing"
	"time"
)

func TestParseReviewVerdict_Review(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		state string
		want  string
	}{
		{"emphasised approve-w-notes", "**APPROVE-w-notes** (post-merge review of the diff)", "COMMENTED", VerdictApproveWNotes},
		{"approve-w-defects wins over approve", "APPROVE-w-defects: two issues", "COMMENTED", VerdictApproveWDefects},
		{"plain approve", "APPROVE", "COMMENTED", VerdictApprove},
		{"request-changes merged", "**REQUEST-CHANGES** (merged; the gate never runs)", "COMMENTED", VerdictRequestChanges},
		{"body beats state: request-changes with COMMENTED", "REQUEST-CHANGES\n\nfix it", "COMMENTED", VerdictRequestChanges},
		{"body beats state: approve body with CHANGES_REQUESTED", "APPROVE", "CHANGES_REQUESTED", VerdictApprove},
		{"case-insensitive", "approve-W-Notes fine", "COMMENTED", VerdictApproveWNotes},
		{"leading blank lines skipped", "\n\n  __Approve__\nrest", "COMMENTED", VerdictApprove},
		{"LGTM with APPROVED state", "LGTM", "APPROVED", VerdictApprove},
		{"LGTM with COMMENTED state", "LGTM", "COMMENTED", ""},
		{"empty body APPROVED", "", "APPROVED", VerdictApprove},
		{"empty body CHANGES_REQUESTED", "", "CHANGES_REQUESTED", VerdictRequestChanges},
		{"empty body COMMENTED", "", "COMMENTED", ""},
		{"verdict on second line is not matched", "Review notes\nAPPROVE", "COMMENTED", ""},
		{"APPROVED word is not the APPROVE verdict", "APPROVED by me", "COMMENTED", ""},
		{"unknown APPROVE- suffix is not guessed", "APPROVE-with-caveats", "COMMENTED", ""},
		{"DISMISSED state gives nothing", "", "DISMISSED", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseReviewVerdict(tt.body, tt.state); got != tt.want {
				t.Errorf("ParseReviewVerdict(%q, %q) = %q, want %q", tt.body, tt.state, got, tt.want)
			}
		})
	}
}

func TestPRReview_UpsertIdempotentAndJoinsExecutions_Review(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	exec := &Execution{
		ID: "exec-1", TaskID: "GH-1", ProjectPath: "/p", Status: "completed",
		PRUrl: "https://github.com/o/r/pull/7", CreatedAt: now, ModelName: "claude-sonnet-5-5",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	rev := &PRReview{
		ReviewID: 900, ProjectPath: "/p", PRNumber: 7, ExecutionID: "exec-1",
		Reviewer: "reviewer", State: "COMMENTED", Verdict: VerdictApproveWNotes, SubmittedAt: now,
	}
	if err := store.UpsertPRReview(rev); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Same review_id again, refreshed and with a blank execution id: still one
	// row, and the resolved execution id survives.
	rev2 := *rev
	rev2.ExecutionID = ""
	rev2.Verdict = VerdictRequestChanges
	if err := store.UpsertPRReview(&rev2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if err := store.UpsertPRReview(&PRReview{
		ReviewID: 901, ProjectPath: "/p", PRNumber: 7, ExecutionID: "exec-1",
		State: "APPROVED", Verdict: VerdictApprove, SubmittedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("third upsert: %v", err)
	}

	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pr_reviews WHERE review_id = 900`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows for review_id 900 = %d, want 1", n)
	}

	got, err := store.ListPRReviewsByExecution("exec-1")
	if err != nil {
		t.Fatalf("ListPRReviewsByExecution: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("reviews joined to exec-1 = %d, want 2", len(got))
	}
	if got[0].ReviewID != 900 || got[0].Verdict != VerdictRequestChanges {
		t.Errorf("first review = %+v, want id 900 refreshed to REQUEST-CHANGES", got[0])
	}

	ids, err := store.ListPRReviewIDs("/p", 7)
	if err != nil {
		t.Fatalf("ListPRReviewIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("stored ids = %v, want 2", ids)
	}
	if other, _ := store.ListPRReviewIDs("/other", 7); len(other) != 0 {
		t.Errorf("ids leaked across project paths: %v", other)
	}

	counts, err := store.GetReviewVerdictCountsByModel(now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GetReviewVerdictCountsByModel: %v", err)
	}
	want := map[string]int{VerdictApprove: 1, VerdictRequestChanges: 1}
	if len(counts) != len(want) {
		t.Fatalf("counts = %+v, want %v", counts, want)
	}
	for _, c := range counts {
		if c.Model != "claude-sonnet-5-5" || want[c.Verdict] != c.Count {
			t.Errorf("unexpected count row %+v", c)
		}
	}

	recent, err := store.ListRecentPRExecutions("/p", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("ListRecentPRExecutions: %v", err)
	}
	if len(recent) != 1 || recent[0].ID != "exec-1" || recent[0].PRUrl != exec.PRUrl {
		t.Errorf("recent = %+v, want exec-1", recent)
	}
}
