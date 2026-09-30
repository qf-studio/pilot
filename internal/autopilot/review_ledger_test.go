package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

const reviewTestProject = "/tmp/review-test-project"

type reviewFake struct {
	server      *httptest.Server
	reviewCalls atomic.Int32
	prCalls     atomic.Int32
}

// newReviewFake serves one PR (number 42): its merge state and a fixed review
// list, counting calls to each endpoint.
func newReviewFake(t *testing.T, merged bool, mergedAt time.Time, reviews []map[string]any) *reviewFake {
	t.Helper()
	f := &reviewFake{}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		f.prCalls.Add(1)
		state := "open"
		if merged {
			state = "closed"
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"number": 42, "state": state, "merged": merged,
			"merged_at": mergedAt.UTC().Format(time.RFC3339),
		}); err != nil {
			t.Errorf("encode PR: %v", err)
		}
	})
	mux.HandleFunc("/repos/owner/repo/pulls/42/reviews", func(w http.ResponseWriter, r *http.Request) {
		f.reviewCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(reviews); err != nil {
			t.Errorf("encode reviews: %v", err)
		}
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func newReviewController(t *testing.T, f *reviewFake) (*Controller, *memory.Store) {
	t.Helper()
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	c := NewController(DefaultConfig(), github.NewClientWithBaseURL(testutil.FakeGitHubToken, f.server.URL), nil, "owner", "repo")
	c.memoryStore = store
	c.projectPath = reviewTestProject
	c.reviews.pollInterval = 0 // every tick polls; dedup must come from stored ids
	return c, store
}

func saveReviewExecution(t *testing.T, store *memory.Store, id, taskID string) {
	t.Helper()
	if err := store.SaveExecution(&memory.Execution{
		ID: id, TaskID: taskID, ProjectPath: reviewTestProject, Status: "completed",
		PRUrl: "https://github.com/owner/repo/pull/42", CreatedAt: time.Now().Add(-time.Hour),
		ModelName: "claude-sonnet-5-5",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
}

func countReviewRows(t *testing.T, store *memory.Store, execID string) []*memory.PRReview {
	t.Helper()
	rows, err := store.ListPRReviewsByExecution(execID)
	if err != nil {
		t.Fatalf("ListPRReviewsByExecution: %v", err)
	}
	return rows
}

var twoReviews = []map[string]any{
	{"id": 1001, "user": map[string]any{"login": "reviewer"}, "state": "COMMENTED",
		"body": "**APPROVE-w-notes** (post-merge review)", "submitted_at": "2026-09-29T10:00:00Z"},
	{"id": 1002, "user": map[string]any{"login": "reviewer"}, "state": "COMMENTED",
		"body": "**REQUEST-CHANGES** (merged; gate never runs)", "submitted_at": "2026-09-29T11:00:00Z"},
}

func TestController_MergedPRReviewsRecordedOnce_Review(t *testing.T) {
	f := newReviewFake(t, true, time.Now().Add(-2*time.Hour), twoReviews)
	c, store := newReviewController(t, f)
	saveReviewExecution(t, store, "exec-42", "GH-10")
	ctx := context.Background()

	c.processAllPRs(ctx) // PR 42 is not tracked: it already merged
	rows := countReviewRows(t, store, "exec-42")
	if len(rows) != 2 {
		t.Fatalf("rows after first tick = %d, want 2", len(rows))
	}
	if rows[0].Verdict != memory.VerdictApproveWNotes || rows[1].Verdict != memory.VerdictRequestChanges {
		t.Errorf("verdicts = %q, %q; want APPROVE-w-notes, REQUEST-CHANGES", rows[0].Verdict, rows[1].Verdict)
	}
	if rows[0].Reviewer != "reviewer" || rows[0].State != "COMMENTED" || rows[0].PRNumber != 42 {
		t.Errorf("row fields wrong: %+v", rows[0])
	}
	if rows[0].SubmittedAt.IsZero() {
		t.Error("submitted_at not recorded")
	}

	c.processAllPRs(ctx)
	if got := len(countReviewRows(t, store, "exec-42")); got != 2 {
		t.Fatalf("rows after second tick = %d, want still 2 (zero new)", got)
	}
	if f.reviewCalls.Load() != 2 {
		t.Errorf("review list calls = %d, want 2 (polled each tick, deduped by id)", f.reviewCalls.Load())
	}
	if f.prCalls.Load() != 1 {
		t.Errorf("PR fetches = %d, want 1 (merged_at cached after first tick)", f.prCalls.Load())
	}
}

func TestController_MergedPRReviewsStopAfterWindow_Review(t *testing.T) {
	f := newReviewFake(t, true, time.Now().Add(-postMergeReviewWindow-time.Hour), twoReviews)
	c, store := newReviewController(t, f)
	saveReviewExecution(t, store, "exec-42", "GH-10")
	ctx := context.Background()

	c.processAllPRs(ctx)
	c.processAllPRs(ctx)
	if got := len(countReviewRows(t, store, "exec-42")); got != 0 {
		t.Fatalf("rows = %d, want 0 for a PR merged more than 7 days ago", got)
	}
	if f.reviewCalls.Load() != 0 {
		t.Errorf("review list calls = %d, want 0 past the window", f.reviewCalls.Load())
	}
	if f.prCalls.Load() != 1 {
		t.Errorf("PR fetches = %d, want 1 (expired PR is not re-fetched)", f.prCalls.Load())
	}
}

func TestController_ActivePRReviewsJoinTaskExecution_Review(t *testing.T) {
	f := newReviewFake(t, false, time.Time{}, []map[string]any{
		{"id": 2001, "user": map[string]any{"login": "bot"}, "state": "APPROVED",
			"body": "LGTM", "submitted_at": "2026-09-29T10:00:00Z"},
		{"id": 2002, "user": map[string]any{"login": "bot"}, "state": "PENDING", "body": "draft"},
	})
	c, store := newReviewController(t, f)
	saveReviewExecution(t, store, "exec-42", "GH-10")

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")
	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR 42 not tracked")
	}
	c.collectActivePRReviews(context.Background(), pr)

	rows := countReviewRows(t, store, "exec-42")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (PENDING review skipped)", len(rows))
	}
	if rows[0].Verdict != memory.VerdictApprove {
		t.Errorf("verdict = %q, want APPROVE from state APPROVED with body LGTM", rows[0].Verdict)
	}
}

func TestPRNumberFromURL_Review(t *testing.T) {
	tests := []struct {
		url    string
		want   int
		wantOK bool
	}{
		{"https://github.com/owner/repo/pull/42", 42, true},
		{"https://github.com/Owner/Repo/pull/7", 7, true},
		{"https://github.com/other/repo/pull/42", 0, false},
		{"https://github.com/owner/repo/issues/42", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		got, ok := prNumberFromURL(tt.url, "owner", "repo")
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("prNumberFromURL(%q) = %d, %v; want %d, %v", tt.url, got, ok, tt.want, tt.wantOK)
		}
	}
}
