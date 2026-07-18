package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// laneStarvationFakeIssue describes one open issue served by the fake GitHub
// server used across TestReconcileLaneStarvation subtests.
type laneStarvationFakeIssue struct {
	number int
	labels []string
}

// laneStarvationServer serves GET /repos/owner/repo/issues (what ListIssues
// calls) with the given issues on page 1 and an empty page thereafter, so
// ListIssues's pagination loop terminates after a single round-trip.
func laneStarvationServer(t *testing.T, issues []laneStarvationFakeIssue) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Issue{})
			return
		}

		out := make([]github.Issue, 0, len(issues))
		for _, iss := range issues {
			labels := make([]github.Label, 0, len(iss.labels))
			for _, name := range iss.labels {
				labels = append(labels, github.Label{Name: name})
			}
			out = append(out, github.Issue{Number: iss.number, State: github.StateOpen, Labels: labels})
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(server.Close)
	return server
}

// fakeLaneQueueStatus is a minimal LaneQueueStatus stub for tests, keyed by
// project path.
type fakeLaneQueueStatus struct {
	counts map[string]int
}

func (f *fakeLaneQueueStatus) QueuedOrRunningCount(projectPath string) int {
	return f.counts[projectPath]
}

func newLaneStarvationController(t *testing.T, issues []laneStarvationFakeIssue) (*Controller, *fakeAlertSink, *fakeLaneQueueStatus) {
	t.Helper()
	server := laneStarvationServer(t, issues)
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath("/proj"))

	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)
	lqs := &fakeLaneQueueStatus{counts: map[string]int{}}
	c.SetLaneQueueStatus(lqs)

	return c, sink, lqs
}

// TestReconcileLaneStarvation_NoEngineOrQueueStatus verifies the sweep is a
// silent no-op (GH-4454: SetAlertsEngine/SetLaneQueueStatus are both
// optional wiring) when either dependency was never configured.
func TestReconcileLaneStarvation_NoEngineOrQueueStatus(t *testing.T) {
	server := laneStarvationServer(t, []laneStarvationFakeIssue{{number: 1}})
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath("/proj"))
	c.reconcileLaneStarvation(context.Background())
	if c.laneStarvationStreak != 0 {
		t.Errorf("expected streak to stay 0 with no alertsEngine/laneQueueStatus wired, got %d", c.laneStarvationStreak)
	}

	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)
	c.reconcileLaneStarvation(context.Background())
	if len(sink.events) != 0 {
		t.Errorf("expected no events with laneQueueStatus still nil, got %d", len(sink.events))
	}
}

// TestReconcileLaneStarvation_NoOpenIssues verifies a lane with zero open
// pilot-labeled issues never trips the detector, and resets any prior streak.
func TestReconcileLaneStarvation_NoOpenIssues(t *testing.T) {
	c, sink, _ := newLaneStarvationController(t, nil)
	c.laneStarvationStreak = 5

	c.reconcileLaneStarvation(context.Background())

	if c.laneStarvationStreak != 0 {
		t.Errorf("expected streak reset to 0 with no open issues, got %d", c.laneStarvationStreak)
	}
	if len(sink.events) != 0 {
		t.Errorf("expected no event with no open issues, got %d", len(sink.events))
	}
}

// TestReconcileLaneStarvation_AllBlocked verifies issues carrying
// pilot-blocked (a deliberately-parked backlog, e.g. GH-4454's own
// repick-hard-cap stall label) are excluded from the actionable count and do
// not trip starvation.
func TestReconcileLaneStarvation_AllBlocked(t *testing.T) {
	c, sink, _ := newLaneStarvationController(t, []laneStarvationFakeIssue{
		{number: 1, labels: []string{github.LabelPilot, github.LabelBlocked}},
		{number: 2, labels: []string{github.LabelPilot, github.LabelBlocked}},
	})

	c.reconcileLaneStarvation(context.Background())

	if c.laneStarvationStreak != 0 {
		t.Errorf("expected streak to stay 0 when every open issue is pilot-blocked, got %d", c.laneStarvationStreak)
	}
	if len(sink.events) != 0 {
		t.Errorf("expected no event when every open issue is pilot-blocked, got %d", len(sink.events))
	}
}

// TestReconcileLaneStarvation_LaneBusy verifies open actionable issues do not
// trip starvation while the lane still has something queued or running.
func TestReconcileLaneStarvation_LaneBusy(t *testing.T) {
	c, sink, lqs := newLaneStarvationController(t, []laneStarvationFakeIssue{
		{number: 1, labels: []string{github.LabelPilot}},
	})
	lqs.counts["/proj"] = 1

	c.reconcileLaneStarvation(context.Background())

	if c.laneStarvationStreak != 0 {
		t.Errorf("expected streak to stay 0 while the lane is busy, got %d", c.laneStarvationStreak)
	}
	if len(sink.events) != 0 {
		t.Errorf("expected no event while the lane is busy, got %d", len(sink.events))
	}
}

// TestReconcileLaneStarvation_Starved verifies open actionable issues with
// nothing queued/running increments the streak and emits an event carrying
// the running streak plus open issue count as metadata (GH-4454) — every
// tick, not just once the Engine's threshold is crossed, since the Engine
// (not this method) owns the fire/cooldown decision.
func TestReconcileLaneStarvation_Starved(t *testing.T) {
	c, sink, _ := newLaneStarvationController(t, []laneStarvationFakeIssue{
		{number: 1, labels: []string{github.LabelPilot}},
		{number: 2, labels: []string{github.LabelPilot}},
	})

	c.reconcileLaneStarvation(context.Background())

	if c.laneStarvationStreak != 1 {
		t.Errorf("expected streak 1 after first starved tick, got %d", c.laneStarvationStreak)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event after first starved tick, got %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Type != alerts.EventTypeLaneStarvation {
		t.Errorf("expected event type %s, got %s", alerts.EventTypeLaneStarvation, ev.Type)
	}
	if ev.Metadata["poll_cycles_starved"] != "1" {
		t.Errorf("expected poll_cycles_starved=1, got %s", ev.Metadata["poll_cycles_starved"])
	}
	if ev.Metadata["open_issue_count"] != "2" {
		t.Errorf("expected open_issue_count=2, got %s", ev.Metadata["open_issue_count"])
	}
	if ev.Metadata["project_path"] != "/proj" {
		t.Errorf("expected project_path=/proj, got %s", ev.Metadata["project_path"])
	}
	if ev.Metadata["repo"] != "owner/repo" {
		t.Errorf("expected repo=owner/repo, got %s", ev.Metadata["repo"])
	}

	// A second consecutive starved tick increments the streak again.
	c.reconcileLaneStarvation(context.Background())
	if c.laneStarvationStreak != 2 {
		t.Errorf("expected streak 2 after second starved tick, got %d", c.laneStarvationStreak)
	}
	if len(sink.events) != 2 {
		t.Fatalf("expected 2 events after second starved tick, got %d", len(sink.events))
	}
	if sink.events[1].Metadata["poll_cycles_starved"] != "2" {
		t.Errorf("expected poll_cycles_starved=2 on second tick, got %s", sink.events[1].Metadata["poll_cycles_starved"])
	}
}

// TestReconcileLaneStarvation_RecoveryResetsStreak verifies the streak resets
// to 0 the moment the lane stops looking starved (either the queue gains
// work or the open-issue backlog clears), matching the documented
// increment-while-starved/reset-otherwise contract.
func TestReconcileLaneStarvation_RecoveryResetsStreak(t *testing.T) {
	c, sink, lqs := newLaneStarvationController(t, []laneStarvationFakeIssue{
		{number: 1, labels: []string{github.LabelPilot}},
	})

	c.reconcileLaneStarvation(context.Background())
	if c.laneStarvationStreak != 1 {
		t.Fatalf("expected streak 1 after starved tick, got %d", c.laneStarvationStreak)
	}

	lqs.counts["/proj"] = 1
	c.reconcileLaneStarvation(context.Background())
	if c.laneStarvationStreak != 0 {
		t.Errorf("expected streak reset to 0 once the lane picks up work, got %d", c.laneStarvationStreak)
	}
	if len(sink.events) != 1 {
		t.Errorf("expected no additional event once recovered, got %d total", len(sink.events))
	}
}
