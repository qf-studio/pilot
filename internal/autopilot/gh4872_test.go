package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_HandleMerging_NonDefaultBase_Parks is the GH-4872 regression
// test for item 1 (auto-merge guard). Root incident, 2026-08-15: ui PR#76 was
// stacked on pilot/GH-70 (base != main) and autopilot merged it anyway,
// closing the linked issue as delivered even though the content never landed
// on main. A PR whose TargetBranch is not the repo's default branch must
// never be merged — it must be parked, alerted, and commented on exactly
// once, even across repeated ticks.
func TestController_HandleMerging_NonDefaultBase_Parks(t *testing.T) {
	var (
		mergeCalled   atomic.Int32
		commentPosted atomic.Int32
		labelApplied  atomic.Int32
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/76/merge":
			mergeCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))

		case r.URL.Path == "/repos/owner/repo/issues/76/comments" && r.Method == http.MethodGet:
			// postBaseMismatchComment's idempotency scan.
			w.WriteHeader(http.StatusOK)
			if commentPosted.Load() == 0 {
				_, _ = w.Write([]byte("[]"))
			} else {
				_, _ = w.Write([]byte(`[{"id":1,"body":"` + baseMismatchCommentMarker + `\nsome text"}]`))
			}

		case r.URL.Path == "/repos/owner/repo/issues/76/comments" && r.Method == http.MethodPost:
			commentPosted.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))

		case r.URL.Path == "/repos/owner/repo/issues/71/labels" && r.Method == http.MethodPost:
			labelApplied.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	c.mu.Lock()
	c.activePRs[76] = &PRState{
		PRNumber:     76,
		IssueNumber:  71,
		BranchName:   "ui/GH-71",
		TargetBranch: "pilot/GH-70", // stacked, not the default branch
		HeadSHA:      "sha76",
		Stage:        StageMerging,
		CreatedAt:    time.Now(),
	}
	c.mu.Unlock()

	// Drive two ticks: the merge must never happen, and the one-time park
	// side effects (label/alert/comment) must fire exactly once total.
	for i := 0; i < 2; i++ {
		if err := c.ProcessPR(context.Background(), 76, nil); err != nil {
			t.Fatalf("tick %d: ProcessPR returned error: %v", i, err)
		}
	}

	if mergeCalled.Load() != 0 {
		t.Errorf("merge was called %d times, want 0 — a stacked PR must never be auto-merged", mergeCalled.Load())
	}

	pr, ok := c.GetPRState(76)
	if !ok {
		t.Fatal("PR 76 should still be tracked (parked, not removed)")
	}
	if pr.Stage != StageMerging {
		t.Errorf("Stage = %s, want %s (parked PRs stay at StageMerging)", pr.Stage, StageMerging)
	}
	if !pr.Parked {
		t.Error("Parked should be true")
	}
	if pr.EscalationReason == "" {
		t.Error("EscalationReason should be set")
	}

	if labelApplied.Load() != 1 {
		t.Errorf("parked-awaiting-approval label applied %d times, want 1", labelApplied.Load())
	}
	if commentPosted.Load() != 1 {
		t.Errorf("PR comment posted %d times, want 1 (idempotent across ticks)", commentPosted.Load())
	}
	if len(sink.events) != 1 {
		t.Fatalf("alerts fired %d times, want 1 (deduped across ticks)", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Type != alerts.EventTypeEscalation {
		t.Errorf("alert Type = %q, want %q", ev.Type, alerts.EventTypeEscalation)
	}
	if !strings.Contains(ev.Error, "76") {
		t.Errorf("alert Error %q should mention the PR number", ev.Error)
	}
	if ev.Metadata["target_branch"] != "pilot/GH-70" || ev.Metadata["default_branch"] != "main" {
		t.Errorf("alert Metadata target/default branch = %q/%q, want pilot/GH-70/main",
			ev.Metadata["target_branch"], ev.Metadata["default_branch"])
	}
}

// TestController_HandleMerging_EmptyTargetBranch_ReReadsPR covers the
// GH-4872 fail-closed path: if prState.TargetBranch is empty when
// handleMerging runs (e.g. a row restored from before GH-2065 populated it),
// the base must be re-read from GitHub rather than assumed safe. A mismatch
// on the re-read still parks; a match proceeds to merge normally.
func TestController_HandleMerging_EmptyTargetBranch_ReReadsPR(t *testing.T) {
	t.Run("re-read reveals non-default base -> parks", func(t *testing.T) {
		var mergeCalled atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/repos/owner/repo/pulls/81" && r.Method == http.MethodGet:
				resp := github.PullRequest{
					Number: 81,
					Head:   github.PRRef{SHA: "sha81"},
					Base:   github.PRRef{Ref: "pilot/GH-70"},
				}
				w.WriteHeader(http.StatusOK)
				_ = writeJSON(w, resp)
			case r.URL.Path == "/repos/owner/repo/pulls/81/merge":
				mergeCalled.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"merged":true}`))
			default:
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("{}"))
			}
		}))
		defer server.Close()

		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		cfg := DefaultConfig()
		cfg.Environment = EnvDev
		c := NewController(cfg, ghClient, nil, "owner", "repo")
		c.SetAlertsEngine(&fakeAlertSink{})

		c.mu.Lock()
		c.activePRs[81] = &PRState{
			PRNumber:  81,
			HeadSHA:   "sha81",
			Stage:     StageMerging,
			CreatedAt: time.Now(),
			// TargetBranch intentionally left empty.
		}
		c.mu.Unlock()

		if err := c.ProcessPR(context.Background(), 81, nil); err != nil {
			t.Fatalf("ProcessPR returned error: %v", err)
		}
		if mergeCalled.Load() != 0 {
			t.Error("merge should not have been called after re-read found a non-default base")
		}
		pr, _ := c.GetPRState(81)
		if !pr.Parked {
			t.Error("Parked should be true after re-read reveals a base mismatch")
		}
		if pr.TargetBranch != "pilot/GH-70" {
			t.Errorf("TargetBranch = %q, want re-read value %q", pr.TargetBranch, "pilot/GH-70")
		}
	})

	t.Run("re-read reveals default base -> proceeds to merge", func(t *testing.T) {
		var mergeCalled atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/repos/owner/repo/pulls/82" && r.Method == http.MethodGet:
				resp := github.PullRequest{
					Number: 82,
					Head:   github.PRRef{SHA: "sha82"},
					Base:   github.PRRef{Ref: "main"},
				}
				w.WriteHeader(http.StatusOK)
				_ = writeJSON(w, resp)
			case r.URL.Path == "/repos/owner/repo/pulls/82/merge":
				mergeCalled.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"merged":true}`))
			default:
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("{}"))
			}
		}))
		defer server.Close()

		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		cfg := DefaultConfig()
		cfg.Environment = EnvDev
		c := NewController(cfg, ghClient, nil, "owner", "repo")
		c.SetAlertsEngine(&fakeAlertSink{})

		c.mu.Lock()
		c.activePRs[82] = &PRState{
			PRNumber:  82,
			HeadSHA:   "sha82",
			Stage:     StageMerging,
			CreatedAt: time.Now(),
		}
		c.mu.Unlock()

		if err := c.ProcessPR(context.Background(), 82, nil); err != nil {
			t.Fatalf("ProcessPR returned error: %v", err)
		}
		if mergeCalled.Load() != 1 {
			t.Errorf("merge called %d times, want 1 — a default-base PR must merge normally", mergeCalled.Load())
		}
		pr, _ := c.GetPRState(82)
		if pr.Parked {
			t.Error("Parked should be false — base matched the default branch")
		}
	})
}

// TestCheckExternalMergeOrClose_NonDefaultBase_NotDelivered is the GH-4872
// regression for item 2's checkExternalMergeOrClose sub-bullet: a PR merged
// outside autopilot's own guarded path (human ran `gh pr merge`, or GitHub's
// UI) into a non-default base must not close the linked issue, must not
// label it done, and must alert + comment instead of silently declaring
// victory.
func TestCheckExternalMergeOrClose_NonDefaultBase_NotDelivered(t *testing.T) {
	var (
		issueClosed atomic.Bool
		doneAdded   atomic.Bool
		pivotPosted atomic.Int32
		listCalls   atomic.Int32
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/71" && r.Method == http.MethodPatch:
			issueClosed.Store(true)
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/repos/owner/repo/issues/71/labels" && r.Method == http.MethodPost:
			doneAdded.Store(true)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))

		case r.URL.Path == "/repos/owner/repo/issues/71/comments" && r.Method == http.MethodGet:
			listCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			if pivotPosted.Load() == 0 {
				_, _ = w.Write([]byte("[]"))
			} else {
				_, _ = w.Write([]byte(`[{"id":1,"body":"` + basePivotCommentMarker + `\nsome text"}]`))
			}

		case r.URL.Path == "/repos/owner/repo/issues/71/comments" && r.Method == http.MethodPost:
			pivotPosted.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{PRNumber: 76, IssueNumber: 71, BranchName: "ui/GH-71", Stage: StageAwaitApproval}
	c.mu.Lock()
	c.activePRs[76] = prState
	c.mu.Unlock()

	ghPR := &github.PullRequest{
		Number:  76,
		State:   "closed",
		Merged:  true,
		Base:    github.PRRef{Ref: "pilot/GH-70"}, // stacked, not the default branch
		HTMLURL: "https://github.com/owner/repo/pull/76",
	}

	prState.mu.Lock()
	resolved := c.checkExternalMergeOrClose(context.Background(), prState, ghPR)
	prState.mu.Unlock()

	if !resolved {
		t.Fatal("checkExternalMergeOrClose should return true (it handled the PR, even though it did not deliver it)")
	}
	if issueClosed.Load() {
		t.Error("issue must NOT be closed — content merged sideways, not onto the default branch")
	}
	if doneAdded.Load() {
		t.Error("pilot-done must NOT be added — content merged sideways, not onto the default branch")
	}
	if pivotPosted.Load() != 1 {
		t.Errorf("pivot comment posted %d times, want 1", pivotPosted.Load())
	}
	if _, ok := c.GetPRState(76); ok {
		t.Error("PR should be untracked after being discovered merged sideways")
	}
	if len(sink.events) != 1 {
		t.Fatalf("alerts fired %d times, want 1", len(sink.events))
	}
	if sink.events[0].Metadata["merged"] != "true" {
		t.Errorf("alert Metadata[merged] = %q, want %q", sink.events[0].Metadata["merged"], "true")
	}
}

// TestCheckExternalMergeOrClose_StageMerged_NoDoubleFire is the GH-4872
// regression for item 3: checkExternalMergeOrClose runs BEFORE ProcessPR on
// every tick (processAllPRs), and previously had no guard for StageMerged.
// A PR that handleMerging just finalized (closed issue, posted comment,
// deleted branch, landed at StageMerged) must not have any of those side
// effects repeated by checkExternalMergeOrClose on the very next tick.
func TestCheckExternalMergeOrClose_StageMerged_NoDoubleFire(t *testing.T) {
	var (
		issueCloseCalls   atomic.Int32
		commentPostCalls  atomic.Int32
		branchDeleteCalls atomic.Int32
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/71" && r.Method == http.MethodPatch:
			issueCloseCalls.Add(1)
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/repos/owner/repo/issues/71/comments" && r.Method == http.MethodPost:
			commentPostCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))

		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/git/refs/heads/") && r.Method == http.MethodDelete:
			branchDeleteCalls.Add(1)
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetAlertsEngine(&fakeAlertSink{})

	// Simulate a PR that handleMerging already finalized on a prior tick:
	// Stage is StageMerged, and the one-time side effects already fired once.
	prState := &PRState{
		PRNumber:                76,
		IssueNumber:             71,
		BranchName:              "pilot/GH-71",
		Stage:                   StageMerged,
		MergeNotificationPosted: true,
	}
	c.mu.Lock()
	c.activePRs[76] = prState
	c.mu.Unlock()
	issueCloseCalls.Store(0)
	commentPostCalls.Store(0)
	branchDeleteCalls.Store(0)

	ghPR := &github.PullRequest{Number: 76, State: "closed", Merged: true, Base: github.PRRef{Ref: "main"}}

	// Two subsequent ticks of the external-merge check alone (mirrors
	// processAllPRs calling checkExternalMergeOrClose before ProcessPR every
	// tick) must both bounce off without repeating any finalize side effect.
	for i := 0; i < 2; i++ {
		prState.mu.Lock()
		resolved := c.checkExternalMergeOrClose(context.Background(), prState, ghPR)
		prState.mu.Unlock()
		if resolved {
			t.Errorf("tick %d: checkExternalMergeOrClose returned true, want false (StageMerged must bounce off)", i)
		}
	}

	if issueCloseCalls.Load() != 0 {
		t.Errorf("issue close called %d times, want 0 (already closed by handleMerging's own finalize)", issueCloseCalls.Load())
	}
	if commentPostCalls.Load() != 0 {
		t.Errorf("completion comment posted %d times, want 0", commentPostCalls.Load())
	}
	if branchDeleteCalls.Load() != 0 {
		t.Errorf("branch delete called %d times, want 0", branchDeleteCalls.Load())
	}
	if _, ok := c.GetPRState(76); !ok {
		t.Error("PR should still be tracked — StageMerged is owned by handleMerged's own tick, not removed here")
	}
}

// TestScanRecentlyMergedPRsWithWindow_NonDefaultBase_NotDelivered is the
// GH-4872 regression for item 2's scanner sub-bullet: the recently-merged-PR
// scanner must apply the same base-branch predicate to Pilot's own PRs that
// it already applied to human PRs (existing pr.Base.Ref != main check for
// !isPilotPR). A pilot/GH-* PR discovered merged into a non-default base
// must alert instead of running the delivered bookkeeping.
func TestScanRecentlyMergedPRsWithWindow_NonDefaultBase_NotDelivered(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_ = writeJSON(w, []*github.PullRequest{
				{
					Number:   76,
					State:    "closed",
					Merged:   true,
					Head:     github.PRRef{Ref: "pilot/GH-71"},
					Base:     github.PRRef{Ref: "pilot/GH-70"}, // stacked, not default
					MergedAt: time.Now().Format(time.RFC3339),
					HTMLURL:  "https://github.com/owner/repo/pull/76",
				},
			})

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	if err := c.ScanRecentlyMergedPRsWithWindow(context.Background(), time.Hour); err != nil {
		t.Fatalf("ScanRecentlyMergedPRsWithWindow returned error: %v", err)
	}

	if len(sink.events) != 1 {
		t.Fatalf("alerts fired %d times, want 1", len(sink.events))
	}
	if sink.events[0].Metadata["pr"] != "76" {
		t.Errorf("alert Metadata[pr] = %q, want %q", sink.events[0].Metadata["pr"], "76")
	}
	if sink.events[0].Metadata["target_branch"] != "pilot/GH-70" {
		t.Errorf("alert Metadata[target_branch] = %q, want %q", sink.events[0].Metadata["target_branch"], "pilot/GH-70")
	}
}

// TestSafeDeleteBranch_HeldWhenBaseOfOpenPR is the GH-4872 regression for
// item 4: a branch that is currently the base of another open PR must
// survive a delete attempt — this is exactly how the 2026-08-15 incident's
// merged content was orphaned (pilot/GH-70, already holding ui#76's squashed
// commit, was deleted during unrelated PR#74 cleanup while ui#76 was the
// only pointer to that content). Table-driven over held vs. not-held.
func TestSafeDeleteBranch_HeldWhenBaseOfOpenPR(t *testing.T) {
	tests := []struct {
		name       string
		openPRs    []*github.PullRequest
		branch     string
		wantDelete bool
	}{
		{
			name: "branch is base of an open PR -> held",
			openPRs: []*github.PullRequest{
				{Number: 77, State: "open", Head: github.PRRef{Ref: "ui/GH-72"}, Base: github.PRRef{Ref: "pilot/GH-70"}},
			},
			branch:     "pilot/GH-70",
			wantDelete: false,
		},
		{
			name: "branch is not the base of any open PR -> deleted",
			openPRs: []*github.PullRequest{
				{Number: 77, State: "open", Head: github.PRRef{Ref: "ui/GH-72"}, Base: github.PRRef{Ref: "main"}},
			},
			branch:     "pilot/GH-70",
			wantDelete: true,
		},
		{
			name:       "no open PRs at all -> deleted",
			openPRs:    []*github.PullRequest{},
			branch:     "pilot/GH-70",
			wantDelete: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var deleteCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/pulls" && r.Method == http.MethodGet:
					w.WriteHeader(http.StatusOK)
					_ = writeJSON(w, tt.openPRs)
				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/git/refs/heads/") && r.Method == http.MethodDelete:
					deleteCalls.Add(1)
					w.WriteHeader(http.StatusOK)
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")
			c.SetAlertsEngine(&fakeAlertSink{})

			deleted, err := c.safeDeleteBranch(context.Background(), tt.branch, 71)
			if err != nil {
				t.Fatalf("safeDeleteBranch returned error: %v", err)
			}
			if deleted != tt.wantDelete {
				t.Errorf("deleted = %v, want %v", deleted, tt.wantDelete)
			}
			gotCalls := deleteCalls.Load() != 0
			if gotCalls != tt.wantDelete {
				t.Errorf("DeleteBranch called = %v, want %v", gotCalls, tt.wantDelete)
			}
		})
	}
}

// TestSafeDeleteBranch_HeldAlertsOncePerBranch confirms the hold-escalation
// alert (GH-4872) is deduplicated per branch name across repeated calls —
// safeDeleteBranch is invoked from four independent cleanup sites, any of
// which could re-offer the same still-stacked branch for deletion before a
// human resolves the stack.
func TestSafeDeleteBranch_HeldAlertsOncePerBranch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_ = writeJSON(w, []*github.PullRequest{
				{Number: 77, State: "open", Head: github.PRRef{Ref: "ui/GH-72"}, Base: github.PRRef{Ref: "pilot/GH-70"}},
			})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	// Simulate two independent cleanup sites both trying to delete the same
	// stacked base branch across two ticks.
	for i := 0; i < 2; i++ {
		if _, err := c.safeDeleteBranch(context.Background(), "pilot/GH-70", 76); err != nil {
			t.Fatalf("tick %d: safeDeleteBranch returned error: %v", i, err)
		}
		if _, err := c.safeDeleteBranch(context.Background(), "pilot/GH-70", 78); err != nil {
			t.Fatalf("tick %d: safeDeleteBranch returned error: %v", i, err)
		}
	}

	if len(sink.events) != 1 {
		t.Fatalf("alerts fired %d times, want 1 (deduped per branch name)", len(sink.events))
	}
}

// writeJSON is a small test helper to encode a JSON response body while
// satisfying errcheck on the Write/Encode call.
func writeJSON(w http.ResponseWriter, v interface{}) error {
	return json.NewEncoder(w).Encode(v)
}
