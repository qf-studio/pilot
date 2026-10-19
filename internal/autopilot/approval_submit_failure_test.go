package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestSubmitAsyncApprovalRequest_PreferredChannelRoutesToConfiguredEnvSource
// is the GH-4380 regression test: a per-env approval_source (e.g.
// environments.stage.approval_source: slack) must actually determine which
// handler receives the request. Before the fix, submitAsyncApprovalRequest
// never set PreferredChannel at all, so the request always fell through to
// Manager's arbitrary-handler fallback regardless of this config.
func TestSubmitAsyncApprovalRequest_PreferredChannelRoutesToConfiguredEnvSource(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.Environments["stage"] = &EnvironmentConfig{
		Branch:          "main",
		RequireApproval: true,
		ApprovalSource:  ApprovalSourceSlack,
	}
	if err := cfg.SetActiveEnvironment("stage"); err != nil {
		t.Fatalf("SetActiveEnvironment: %v", err)
	}

	mgr := asyncApprovalManager()
	telegram := &mockCapturingApprovalHandler{}
	slack := &mockCapturingApprovalHandler{}
	// Distinguish the two handlers by name so Manager can route deterministically.
	mgr.RegisterHandler(&namedApprovalHandler{mockCapturingApprovalHandler: telegram, name: "telegram"})
	mgr.RegisterHandler(&namedApprovalHandler{mockCapturingApprovalHandler: slack, name: "slack"})

	c := NewController(cfg, ghClient, mgr, "owner", "repo")
	prState := &PRState{PRNumber: 42, PRURL: "https://github.com/owner/repo/pull/42", Stage: StageAwaitApproval}

	if err := c.submitAsyncApprovalRequest(context.Background(), prState); err != nil {
		t.Fatalf("submitAsyncApprovalRequest returned error: %v", err)
	}

	if len(slack.sent) != 1 {
		t.Errorf("expected 1 request routed to the slack handler (per environments.stage.approval_source), got %d", len(slack.sent))
	}
	if len(telegram.sent) != 0 {
		t.Errorf("expected 0 requests routed to telegram, got %d", len(telegram.sent))
	}
	if got := slack.sent[0].PreferredChannel; got != "slack" {
		t.Errorf("PreferredChannel = %q, want %q", got, "slack")
	}
}

// namedApprovalHandler wraps mockCapturingApprovalHandler with a configurable
// Name() so two instances can be registered under distinct channel keys.
type namedApprovalHandler struct {
	*mockCapturingApprovalHandler
	name string
}

func (n *namedApprovalHandler) Name() string { return n.name }

// TestSubmitAsyncApprovalRequest_SubmitFailure_AlertsCommentsAndCountsOnce is
// the GH-4380 regression test for defect 3 (the wedge is invisible): when
// SubmitApprovalRequest fails (e.g. approval_source names an unregistered
// handler), the controller must increment the ApprovalSubmitFailures metric,
// fire an alerts.Event, and post a PR comment — exactly once, even across
// repeated ticks (handleAwaitApproval calls this on every tick a PR sits in
// StageAwaitApproval, so without dedup the PR would either spam an alert
// every tick, or — once the circuit breaker opens and ProcessPR stops
// reaching this code at all — never alert again).
func TestSubmitAsyncApprovalRequest_SubmitFailure_AlertsCommentsAndCountsOnce(t *testing.T) {
	var postCount int
	var postedBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/comments") {
			postCount++
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			postedBodies = append(postedBodies, string(buf))
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig() // ApprovalSource defaults to telegram; no handler registered below.

	mgr := asyncApprovalManager() // PreMerge stage enabled, zero handlers registered.
	c := NewController(cfg, ghClient, mgr, "owner", "repo")

	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{PRNumber: 42, PRURL: "https://github.com/owner/repo/pull/42", Stage: StageAwaitApproval}

	// Tick 1: submit fails (no "telegram" handler registered).
	err := c.submitAsyncApprovalRequest(context.Background(), prState)
	if err == nil {
		t.Fatal("expected submitAsyncApprovalRequest to return an error")
	}

	if got := c.metrics.Snapshot().ApprovalSubmitFailures; got != 1 {
		t.Errorf("ApprovalSubmitFailures = %d, want 1", got)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 alert event, got %d", len(sink.events))
	}
	if sink.events[0].Type != alerts.EventTypeTaskFailed {
		t.Errorf("alert Type = %q, want %q", sink.events[0].Type, alerts.EventTypeTaskFailed)
	}
	if !strings.Contains(sink.events[0].Error, "42") {
		t.Errorf("alert Error %q should mention the PR number", sink.events[0].Error)
	}
	if postCount != 1 {
		t.Fatalf("postCount after tick 1 = %d, want 1", postCount)
	}
	if !strings.Contains(postedBodies[0], "@owner") {
		t.Errorf("PR comment %q should @mention the repo owner", postedBodies[0])
	}

	// Tick 2 (e.g. a retried controller loop before the circuit breaker
	// opens): must NOT alert or comment again.
	err = c.submitAsyncApprovalRequest(context.Background(), prState)
	if err == nil {
		t.Fatal("expected submitAsyncApprovalRequest to return an error on tick 2 too")
	}
	if got := c.metrics.Snapshot().ApprovalSubmitFailures; got != 2 {
		t.Errorf("ApprovalSubmitFailures after tick 2 = %d, want 2 (metric increments every failure)", got)
	}
	if len(sink.events) != 1 {
		t.Errorf("expected alert to fire exactly once across 2 ticks, got %d events", len(sink.events))
	}
	if postCount != 1 {
		t.Errorf("expected PR comment to post exactly once across 2 ticks, got %d", postCount)
	}
}

// TestSubmitAsyncApprovalRequest_NoHandlerFailsClosed_PRStaysUnmerged is the
// GH-5756 controller leg: a pre-merge ask with no registered handler (and no
// preferred channel, so Manager takes its handler==nil branch) must surface as
// a submit error. The PR must not be merged, no approval request may be
// recorded for the wall-clock guard to later default-approve, and the
// submit-failure alert fires exactly once across repeated ticks — even with
// default_action: approved.
func TestSubmitAsyncApprovalRequest_NoHandlerFailsClosed_PRStaysUnmerged(t *testing.T) {
	var mergeCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/merge") {
			mergeCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"merged":true,"sha":"abc"}`))
			return
		}
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/comments") {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	mgr := approval.NewManager(&approval.Config{
		Enabled:        true,
		DefaultTimeout: time.Hour,
		DefaultAction:  approval.DecisionApproved,
		PreMerge: &approval.StageConfig{
			Enabled:       true,
			Timeout:       time.Hour,
			DefaultAction: approval.DecisionApproved,
		},
	})

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, mgr, "owner", "repo")
	c.resolvedApprovalSource = "" // no preferred channel -> Manager's handler==nil branch
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{PRNumber: 42, PRURL: "https://github.com/owner/repo/pull/42", Stage: StageAwaitApproval}

	for tick := 1; tick <= 2; tick++ {
		if err := c.handleAwaitApproval(context.Background(), prState); err == nil {
			t.Fatalf("tick %d: expected a submit error with no handler registered, got nil", tick)
		}
	}

	if mergeCalls != 0 {
		t.Errorf("merge API called %d times, want 0 (PR must stay unmerged)", mergeCalls)
	}
	if prState.ApprovalRequestID != "" || !prState.ApprovalRequestedAt.IsZero() {
		t.Errorf("no approval request may be recorded on submit failure, got ID %q requestedAt %v",
			prState.ApprovalRequestID, prState.ApprovalRequestedAt)
	}
	if prState.Stage != StageAwaitApproval {
		t.Errorf("Stage = %v, want StageAwaitApproval", prState.Stage)
	}
	if len(sink.events) != 1 {
		t.Errorf("expected the submit-failure alert exactly once across 2 ticks, got %d", len(sink.events))
	}
}
