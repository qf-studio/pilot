package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestApprovalMisconfigKey verifies approvalMisconfigKey names the specific
// approval.* YAML key that's unset, distinguishing a nil manager / disabled
// top-level gate (approval.enabled) from an enabled manager whose pre_merge
// stage specifically is off (approval.pre_merge.enabled). GH-4597.
func TestApprovalMisconfigKey(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	t.Run("nil approvalMgr names approval.enabled", func(t *testing.T) {
		c := NewController(cfg, ghClient, nil, "owner", "repo")
		if got := c.approvalMisconfigKey(); got != "approval.enabled" {
			t.Errorf("approvalMisconfigKey() = %q, want %q", got, "approval.enabled")
		}
	})

	t.Run("manager present but top-level disabled names approval.enabled", func(t *testing.T) {
		mgr := approval.NewManager(&approval.Config{Enabled: false})
		c := NewController(cfg, ghClient, mgr, "owner", "repo")
		if got := c.approvalMisconfigKey(); got != "approval.enabled" {
			t.Errorf("approvalMisconfigKey() = %q, want %q", got, "approval.enabled")
		}
	})

	t.Run("top-level enabled but pre_merge disabled names approval.pre_merge.enabled", func(t *testing.T) {
		mgr := approval.NewManager(&approval.Config{
			Enabled:  true,
			PreMerge: &approval.StageConfig{Enabled: false},
		})
		c := NewController(cfg, ghClient, mgr, "owner", "repo")
		if got := c.approvalMisconfigKey(); got != "approval.pre_merge.enabled" {
			t.Errorf("approvalMisconfigKey() = %q, want %q", got, "approval.pre_merge.enabled")
		}
	})
}

// TestSubmitAsyncApprovalRequest_MisconfigAlertsOncePerReason is the GH-4597
// regression test: the approval-misconfig config_error alert must fire
// exactly once per {PR, reason} pair. Steady-state ticks are cut off by the
// Parked guard (GH-4596); the {PR, reason} map dedupes across fresh
// escalation cycles (Parked reset via re-registration), while a DIFFERENT
// escalation reason on a fresh cycle still gets its own alert.
func TestSubmitAsyncApprovalRequest_MisconfigAlertsOncePerReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	// approvalMgr nil -> IsStageEnabled false -> misconfig branch.
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{
		PRNumber:         96,
		Stage:            StageAwaitApproval,
		EscalationReason: "PR adds 656 net lines (> 500 threshold)",
	}

	// Ticks 1-3 with the same reason: exactly one alert.
	for i := 0; i < 3; i++ {
		if err := c.submitAsyncApprovalRequest(context.Background(), prState); err != nil {
			t.Fatalf("tick %d: submitAsyncApprovalRequest returned error: %v", i, err)
		}
	}
	if len(sink.events) != 1 {
		t.Fatalf("after 3 ticks with the same reason: got %d alerts, want 1", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Type != alerts.EventTypeConfigError {
		t.Errorf("alert Type = %q, want %q", ev.Type, alerts.EventTypeConfigError)
	}
	if !strings.Contains(ev.Error, "96") {
		t.Errorf("alert Error %q should mention the PR number", ev.Error)
	}
	if !strings.Contains(ev.Error, "PR adds 656 net lines") {
		t.Errorf("alert Error %q should name the escalation reason", ev.Error)
	}
	if ev.Metadata["missing_config_key"] != "approval.enabled" {
		t.Errorf("alert Metadata[missing_config_key] = %q, want %q", ev.Metadata["missing_config_key"], "approval.enabled")
	}

	// While parked, later ticks never reach the alert — even with a new
	// reason, GH-4596's Parked guard short-circuits first.
	prState.EscalationReason = "environments.stage.require_approval=true"
	if err := c.submitAsyncApprovalRequest(context.Background(), prState); err != nil {
		t.Fatalf("submitAsyncApprovalRequest returned error: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("after a new reason while parked: got %d alerts, want 1", len(sink.events))
	}

	// A fresh escalation cycle (Parked reset, e.g. PR re-registered) with the
	// original reason must NOT re-alert — the {PR, reason} map dedupes across
	// cycles.
	prState.Parked = false
	prState.EscalationReason = "PR adds 656 net lines (> 500 threshold)"
	if err := c.submitAsyncApprovalRequest(context.Background(), prState); err != nil {
		t.Fatalf("submitAsyncApprovalRequest returned error: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("after a repeat reason on a fresh cycle: got %d alerts, want 1", len(sink.events))
	}

	// A fresh cycle with a different reason must alert again (new {PR, reason} key).
	prState.Parked = false
	prState.EscalationReason = "environments.stage.require_approval=true"
	if err := c.submitAsyncApprovalRequest(context.Background(), prState); err != nil {
		t.Fatalf("submitAsyncApprovalRequest returned error: %v", err)
	}
	if len(sink.events) != 2 {
		t.Fatalf("after a new reason on a fresh cycle: got %d alerts, want 2", len(sink.events))
	}
}
