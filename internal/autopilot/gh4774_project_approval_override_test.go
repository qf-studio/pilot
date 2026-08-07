package autopilot

import (
	"context"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_ProjectApprovalOverride_ResolvesIndependently_GH4774 is the
// controller-level acceptance test for GH-4774, in the style of
// TestController_ProjectCIChecksOverride_GH4478. It builds two controllers
// sharing the SAME global *Config (mirroring cmd/pilot/main.go passing
// cfg.Orchestrator.Autopilot by pointer to every controller): a "personal"
// project overridden to require approval via Telegram, and a "work" project
// overridden to require approval via Slack, even though the resolved
// env/global config requires no approval at all. Before GH-4774,
// RequireApproval/ApprovalSource resolved once per-environment for every
// controller — there was no way for two project controllers sharing one
// Config to disagree on either.
func TestController_ProjectApprovalOverride_ResolvesIndependently_GH4774(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)

	// Single shared global Config: no approval required, telegram is the
	// resolved source — exactly as main.go threads cfg.Orchestrator.Autopilot
	// by pointer into every controller.
	cfg := DefaultConfig()
	cfg.Environment = EnvDev // RequireApproval = false
	cfg.ApprovalSource = ApprovalSourceTelegram

	requireTrue := true
	slackSource := ApprovalSourceSlack

	personalOverride := &ProjectApprovalOverride{RequireApproval: &requireTrue}
	workOverride := &ProjectApprovalOverride{RequireApproval: &requireTrue, ApprovalSource: &slackSource}

	t.Run("no override: inherits resolved env/global (no approval required, telegram)", func(t *testing.T) {
		c := NewController(cfg, ghClient, nil, "owner", "default-repo")
		if c.resolvedRequireApproval {
			t.Errorf("resolvedRequireApproval = true, want false (inherited from env)")
		}
		if c.resolvedApprovalSource != ApprovalSourceTelegram {
			t.Errorf("resolvedApprovalSource = %q, want %q (inherited from global)", c.resolvedApprovalSource, ApprovalSourceTelegram)
		}
	})

	t.Run("personal project override: require_approval=true, approval_source inherits telegram", func(t *testing.T) {
		c := NewController(cfg, ghClient, nil, "owner", "personal-repo", WithApprovalOverride(personalOverride))
		if !c.resolvedRequireApproval {
			t.Errorf("resolvedRequireApproval = false, want true (project override)")
		}
		if c.resolvedApprovalSource != ApprovalSourceTelegram {
			t.Errorf("resolvedApprovalSource = %q, want %q (unset override field inherits global)", c.resolvedApprovalSource, ApprovalSourceTelegram)
		}
	})

	t.Run("work project override: require_approval=true, approval_source=slack", func(t *testing.T) {
		c := NewController(cfg, ghClient, nil, "owner", "work-repo", WithApprovalOverride(workOverride))
		if !c.resolvedRequireApproval {
			t.Errorf("resolvedRequireApproval = false, want true (project override)")
		}
		if c.resolvedApprovalSource != ApprovalSourceSlack {
			t.Errorf("resolvedApprovalSource = %q, want %q (project override)", c.resolvedApprovalSource, ApprovalSourceSlack)
		}
	})
}

// TestController_ProjectApprovalOverride_NoOverrideRegression_GH4774 is the
// regression guard: a controller built with no WithApprovalOverride option
// (the overwhelming majority of existing configs) must resolve
// RequireApproval/ApprovalSource identically to the pre-GH-4774 behavior —
// reading straight through cfg.ResolvedEnvOrDefault().RequireApproval and
// cfg.EffectiveApprovalSource().
func TestController_ProjectApprovalOverride_NoOverrideRegression_GH4774(t *testing.T) {
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

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	wantRequireApproval := cfg.ResolvedEnvOrDefault().RequireApproval
	wantApprovalSource := cfg.EffectiveApprovalSource()

	if c.resolvedRequireApproval != wantRequireApproval {
		t.Errorf("resolvedRequireApproval = %v, want %v (byte-identical to pre-GH-4774 resolution)", c.resolvedRequireApproval, wantRequireApproval)
	}
	if c.resolvedApprovalSource != wantApprovalSource {
		t.Errorf("resolvedApprovalSource = %q, want %q (byte-identical to pre-GH-4774 resolution)", c.resolvedApprovalSource, wantApprovalSource)
	}
}

// TestSubmitAsyncApprovalRequest_ProjectApprovalOverride_PreferredChannel_GH4774
// verifies the escalation-gate interplay: a PR escalated to StageAwaitApproval
// by a defense-in-depth gate (size-floor here, mirroring
// TestHandleCIPassed_SizeFloorEscalation) on a project overridden to
// approval_source=slack must route its approval request's PreferredChannel to
// "slack" — even though the resolved env/global source is telegram and the
// escalation had nothing to do with require_approval. This proves
// submitAsyncApprovalRequest's PreferredChannel reads the resolved override,
// not just the RequireApproval-driven codepath.
func TestSubmitAsyncApprovalRequest_ProjectApprovalOverride_PreferredChannel_GH4774(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev // RequireApproval = false; ApprovalSource stays telegram (default)

	slackSource := ApprovalSourceSlack
	override := &ProjectApprovalOverride{ApprovalSource: &slackSource}

	mgr := asyncApprovalManager()
	telegram := &mockCapturingApprovalHandler{}
	slack := &mockCapturingApprovalHandler{}
	mgr.RegisterHandler(&namedApprovalHandler{mockCapturingApprovalHandler: telegram, name: "telegram"})
	mgr.RegisterHandler(&namedApprovalHandler{mockCapturingApprovalHandler: slack, name: "slack"})

	c := NewController(cfg, ghClient, mgr, "owner", "work-repo", WithApprovalOverride(override))

	// Escalated by a defense-in-depth gate (size-floor), not require_approval.
	prState := &PRState{
		PRNumber:         77,
		PRURL:            "https://github.com/owner/work-repo/pull/77",
		Stage:            StageAwaitApproval,
		EscalationReason: "size_floor: 600 net additions exceeds 500 threshold",
	}

	if err := c.submitAsyncApprovalRequest(context.Background(), prState); err != nil {
		t.Fatalf("submitAsyncApprovalRequest returned error: %v", err)
	}

	if len(slack.sent) != 1 {
		t.Fatalf("expected 1 request routed to the slack handler (per project approval override), got %d", len(slack.sent))
	}
	if len(telegram.sent) != 0 {
		t.Errorf("expected 0 requests routed to telegram, got %d", len(telegram.sent))
	}
	if got := slack.sent[0].PreferredChannel; got != "slack" {
		t.Errorf("PreferredChannel = %q, want %q", got, "slack")
	}
}
