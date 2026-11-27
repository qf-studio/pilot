package autopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

type fakePlaneDoneCall struct {
	ProjectID  string
	WorkItemID string
	PRURL      string
}

// fakePlaneDoneNotifier is a test double for PlaneDoneNotifier.
type fakePlaneDoneNotifier struct {
	calls []fakePlaneDoneCall
	err   error
}

func (f *fakePlaneDoneNotifier) NotifyMerged(_ context.Context, projectID, workItemID, prURL string) error {
	f.calls = append(f.calls, fakePlaneDoneCall{ProjectID: projectID, WorkItemID: workItemID, PRURL: prURL})
	return f.err
}

// TestNotifyPlaneDone_HandleMerging covers GH-6751's autopilot merge path: a
// merged PR with a Plane link fires the notifier exactly once with the
// project, the work item and the PR URL; a PR with no link never does; a
// notifier error is WARN-only and the merge still completes.
func TestNotifyPlaneDone_HandleMerging(t *testing.T) {
	const prURL = "https://github.com/owner/repo/pull/201"
	tests := []struct {
		name          string
		projectID     string
		workItemID    string
		notifierErr   error
		wantCalls     []fakePlaneDoneCall
		wantWarnInLog string
	}{
		{
			name:       "Plane link fires once",
			projectID:  "plane-proj",
			workItemID: "plane-wi",
			wantCalls:  []fakePlaneDoneCall{{ProjectID: "plane-proj", WorkItemID: "plane-wi", PRURL: prURL}},
		},
		{
			name: "no Plane link does not fire",
		},
		{
			name:          "notifier error is WARN-only",
			projectID:     "plane-proj",
			workItemID:    "plane-wi-2",
			notifierErr:   context.DeadlineExceeded,
			wantWarnInLog: "failed to move Plane work item to completed",
			wantCalls:     []fakePlaneDoneCall{{ProjectID: "plane-proj", WorkItemID: "plane-wi-2", PRURL: prURL}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/repo/pulls/201/merge" && r.Method == http.MethodPost {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("{}"))
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvDev
			cfg.AutoMerge = true

			var logBuf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuf, nil))

			c := NewController(cfg, ghClient, nil, "owner", "repo", WithLogger(logger))
			notifier := &fakePlaneDoneNotifier{err: tt.notifierErr}
			c.SetPlaneDoneNotifier(notifier)

			c.mu.Lock()
			c.activePRs[201] = &PRState{
				PRNumber:        201,
				PRURL:           prURL,
				BranchName:      "pilot/PLANE-7",
				HeadSHA:         "sha201",
				Stage:           StageMerging,
				TargetBranch:    "main",
				CreatedAt:       time.Now(),
				PlaneProjectID:  tt.projectID,
				PlaneWorkItemID: tt.workItemID,
			}
			c.mu.Unlock()

			if err := c.ProcessPR(context.Background(), 201, nil); err != nil {
				t.Fatalf("ProcessPR returned error: %v", err)
			}
			pr, _ := c.GetPRState(201)
			if pr.Stage != StageMerged {
				t.Errorf("Stage = %s, want %s — merge must succeed even if the Plane leg fails", pr.Stage, StageMerged)
			}
			if len(notifier.calls) != len(tt.wantCalls) {
				t.Fatalf("notifier calls = %+v, want %+v", notifier.calls, tt.wantCalls)
			}
			for i, want := range tt.wantCalls {
				if notifier.calls[i] != want {
					t.Errorf("call[%d] = %+v, want %+v", i, notifier.calls[i], want)
				}
			}
			if tt.wantWarnInLog != "" && !strings.Contains(logBuf.String(), tt.wantWarnInLog) {
				t.Errorf("log missing %q; got: %s", tt.wantWarnInLog, logBuf.String())
			}
		})
	}
}

// TestNotifyPlaneDone_ExternalMerge covers the checkExternalMergeOrClose
// merged branch: a human-merged PR with a Plane link fires the leg once.
func TestNotifyPlaneDone_ExternalMerge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls/42" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number: 42, State: "closed", Merged: true,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	notifier := &fakePlaneDoneNotifier{}
	c.SetPlaneDoneNotifier(notifier)

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 0, "abc123", "pilot/PLANE-7", "")
	if !c.SetPlaneLink(42, "plane-proj", "plane-wi") {
		t.Fatal("SetPlaneLink returned false for a tracked PR")
	}
	c.processAllPRs(context.Background())

	want := fakePlaneDoneCall{ProjectID: "plane-proj", WorkItemID: "plane-wi", PRURL: "https://github.com/owner/repo/pull/42"}
	if len(notifier.calls) != 1 || notifier.calls[0] != want {
		t.Fatalf("notifier calls = %+v, want [%+v]", notifier.calls, want)
	}
	if _, ok := c.GetPRState(42); ok {
		t.Error("PR should be removed after external merge detection")
	}
}

// TestNotifyPlaneDone_OncePerPR: a second merge tick (re-entry) must not call
// the notifier again, and the flag survives a store round-trip.
func TestNotifyPlaneDone_OncePerPR(t *testing.T) {
	store := newTestStateStore(t)
	c := NewController(DefaultConfig(), nil, nil, "owner", "repo")
	c.SetStateStore(store)
	notifier := &fakePlaneDoneNotifier{}
	c.SetPlaneDoneNotifier(notifier)

	prState := &PRState{
		PRNumber:        42,
		PRURL:           "https://github.com/owner/repo/pull/42",
		BranchName:      "pilot/PLANE-7",
		Stage:           StageWaitingCI,
		CreatedAt:       time.Now().Truncate(time.Second),
		PlaneProjectID:  "plane-proj",
		PlaneWorkItemID: "plane-wi",
	}

	c.notifyPlaneDone(context.Background(), prState)
	if len(notifier.calls) != 1 || !prState.PlaneDoneNotified {
		t.Fatalf("after first call: calls=%d notified=%v", len(notifier.calls), prState.PlaneDoneNotified)
	}

	loaded, err := store.GetPRState("owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPRState: %v", err)
	}
	if loaded == nil || !loaded.PlaneDoneNotified {
		t.Fatalf("PlaneDoneNotified did not persist: %+v", loaded)
	}
	if loaded.PlaneProjectID != "plane-proj" || loaded.PlaneWorkItemID != "plane-wi" {
		t.Fatalf("Plane link did not persist: project=%q work item=%q", loaded.PlaneProjectID, loaded.PlaneWorkItemID)
	}
	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil {
		t.Fatalf("LoadAllPRStates: %v", err)
	}
	if len(all) != 1 || !all[0].PlaneDoneNotified {
		t.Fatalf("LoadAllPRStates did not preserve PlaneDoneNotified: %+v", all)
	}

	c.notifyPlaneDone(context.Background(), loaded)
	c.notifyPlaneDone(context.Background(), all[0])
	c.notifyPlaneDone(context.Background(), prState)
	if len(notifier.calls) != 1 {
		t.Errorf("notifier calls = %d after re-entry, want 1", len(notifier.calls))
	}
}

// TestNotifyPlaneDone_NilNotifier: an unwired controller is a no-op and does
// not set the attempt flag.
func TestNotifyPlaneDone_NilNotifier(t *testing.T) {
	c := NewController(DefaultConfig(), nil, nil, "owner", "repo")
	prState := &PRState{PRNumber: 1, PlaneProjectID: "plane-proj", PlaneWorkItemID: "plane-wi"}
	c.notifyPlaneDone(context.Background(), prState)
	if prState.PlaneDoneNotified {
		t.Error("PlaneDoneNotified set without a notifier")
	}
}

// TestNotifyPlaneDone_NoLink: a PR with an empty project or work item ID
// (another adapter, or a link that was never stored) makes no call and does
// not set the attempt flag.
func TestNotifyPlaneDone_NoLink(t *testing.T) {
	tests := []struct {
		name       string
		projectID  string
		workItemID string
	}{
		{name: "both empty"},
		{name: "no project", workItemID: "plane-wi"},
		{name: "no work item", projectID: "plane-proj"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewController(DefaultConfig(), nil, nil, "owner", "repo")
			notifier := &fakePlaneDoneNotifier{}
			c.SetPlaneDoneNotifier(notifier)
			prState := &PRState{PRNumber: 1, PlaneProjectID: tt.projectID, PlaneWorkItemID: tt.workItemID}
			c.notifyPlaneDone(context.Background(), prState)
			if len(notifier.calls) != 0 {
				t.Errorf("notifier calls = %+v, want none", notifier.calls)
			}
			if prState.PlaneDoneNotified {
				t.Error("PlaneDoneNotified set for a PR with no Plane link")
			}
		})
	}
}

// TestNotifyPlaneDone_ErrorStillSetsFlag: a failed attempt still sets and
// persists the flag, so the leg is not retried on every later tick.
func TestNotifyPlaneDone_ErrorStillSetsFlag(t *testing.T) {
	store := newTestStateStore(t)
	c := NewController(DefaultConfig(), nil, nil, "owner", "repo")
	c.SetStateStore(store)
	notifier := &fakePlaneDoneNotifier{err: context.DeadlineExceeded}
	c.SetPlaneDoneNotifier(notifier)

	prState := &PRState{
		PRNumber:        43,
		PRURL:           "https://github.com/owner/repo/pull/43",
		BranchName:      "pilot/PLANE-8",
		Stage:           StageWaitingCI,
		CreatedAt:       time.Now().Truncate(time.Second),
		PlaneProjectID:  "plane-proj",
		PlaneWorkItemID: "plane-wi",
	}

	c.notifyPlaneDone(context.Background(), prState)
	if len(notifier.calls) != 1 {
		t.Fatalf("notifier calls = %d, want 1", len(notifier.calls))
	}
	if !prState.PlaneDoneNotified {
		t.Fatal("PlaneDoneNotified not set after a failed attempt")
	}
	loaded, err := store.GetPRState("owner/repo", 43)
	if err != nil {
		t.Fatalf("GetPRState: %v", err)
	}
	if loaded == nil || !loaded.PlaneDoneNotified {
		t.Fatalf("PlaneDoneNotified did not persist after a failed attempt: %+v", loaded)
	}

	c.notifyPlaneDone(context.Background(), loaded)
	if len(notifier.calls) != 1 {
		t.Errorf("notifier calls = %d after a retry tick, want 1", len(notifier.calls))
	}
}
