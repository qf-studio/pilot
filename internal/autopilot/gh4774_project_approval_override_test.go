package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// namedCapturingApprovalHandler is a capturing approval.Handler with a
// configurable Name(), so a test can register both a "telegram" and a
// "slack" handler on the same approval.Manager and verify
// Request.PreferredChannel actually routed to the right one instead of
// relying on Go's map iteration order (GH-4380/GH-4774).
type namedCapturingApprovalHandler struct {
	name string
	sent []*approval.Request
}

func (m *namedCapturingApprovalHandler) Name() string { return m.name }

func (m *namedCapturingApprovalHandler) SendApprovalRequest(_ context.Context, req *approval.Request) (<-chan *approval.Response, error) {
	m.sent = append(m.sent, req)
	return make(chan *approval.Response, 1), nil
}

func (m *namedCapturingApprovalHandler) CancelRequest(context.Context, string) error { return nil }

// TestController_ProjectApprovalOverride_GatingIndependent_GH4774 is the
// GH-4774 acceptance test for gating independence: two controllers share the
// SAME global *Config (mirroring cmd/pilot/main.go threading
// cfg.Orchestrator.Autopilot by pointer into every repo's controller) — the
// active environment (prod) requires approval for everyone by default. A
// "personal" project overlays require_approval: false and must merge
// straight through; a "work" project has no require_approval override (only
// an approval_source override) and must still escalate, proving the two
// controllers' gating decisions are resolved independently rather than one
// overlay leaking onto the other's shared *Config (the exact class of bug
// GH-4478's shallow-copy discipline exists to avoid).
func TestController_ProjectApprovalOverride_GatingIndependent_GH4774(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Small, no linked issue — neither size-floor nor scope-drift gates fire,
		// isolating the assertion to the require_approval resolution itself.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	cfg := DefaultConfig()
	cfg.Environment = EnvProd // built-in prod default: RequireApproval=true

	requireApprovalFalse := false
	personal := NewController(cfg, ghClient, nil, "acme", "personal",
		WithApprovalOverride(&ProjectApprovalOverride{RequireApproval: &requireApprovalFalse}))
	work := NewController(cfg, ghClient, nil, "acme", "work",
		WithApprovalOverride(&ProjectApprovalOverride{ApprovalSource: ApprovalSourceSlack}))

	personalPR := &PRState{PRNumber: 1, PRTitle: "fix: small change", Stage: StageCIPassed}
	if err := personal.handleCIPassed(context.Background(), personalPR); err != nil {
		t.Fatalf("personal handleCIPassed error: %v", err)
	}
	if personalPR.Stage != StageMerging {
		t.Errorf("personal project stage = %s, want %s — its require_approval: false override must bypass the env's require_approval=true default", personalPR.Stage, StageMerging)
	}

	workPR := &PRState{PRNumber: 2, PRTitle: "fix: small change", Stage: StageCIPassed}
	if err := work.handleCIPassed(context.Background(), workPR); err != nil {
		t.Fatalf("work handleCIPassed error: %v", err)
	}
	if workPR.Stage != StageAwaitApproval {
		t.Errorf("work project stage = %s, want %s — it has no require_approval override, so it must still inherit the env's require_approval=true", workPR.Stage, StageAwaitApproval)
	}

	// Confirm the resolved fields themselves, not just the stage transition.
	if personal.ResolvedRequireApproval() {
		t.Error("personal.ResolvedRequireApproval() = true, want false")
	}
	if !work.ResolvedRequireApproval() {
		t.Error("work.ResolvedRequireApproval() = false, want true (inherited from env)")
	}
}

// TestController_ProjectApprovalOverride_ChannelRouting_GH4774 is the
// GH-4774 acceptance test for channel routing: a project's approval_source
// override must set Request.PreferredChannel to that project's channel, not
// the global/env default, and a daemon with both Telegram and Slack handlers
// registered must route to the right one.
func TestController_ProjectApprovalOverride_ChannelRouting_GH4774(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)

	cfg := DefaultConfig()
	cfg.Environment = EnvProd
	cfg.ApprovalSource = ApprovalSourceTelegram // global/env default channel

	mgr := asyncApprovalManager()
	tgHandler := &namedCapturingApprovalHandler{name: "telegram"}
	slackHandler := &namedCapturingApprovalHandler{name: "slack"}
	mgr.RegisterHandler(tgHandler)
	mgr.RegisterHandler(slackHandler)

	work := NewController(cfg, ghClient, mgr, "acme", "work",
		WithApprovalOverride(&ProjectApprovalOverride{ApprovalSource: ApprovalSourceSlack}))

	work.mu.Lock()
	work.activePRs[42] = &PRState{
		PRNumber:    42,
		PRURL:       "https://github.com/acme/work/pull/42",
		PRTitle:     "feat: something",
		IssueNumber: 10,
		Stage:       StageAwaitApproval,
	}
	work.mu.Unlock()

	if err := work.ProcessPR(context.Background(), 42, nil); err != nil {
		t.Fatalf("ProcessPR error: %v", err)
	}

	if len(slackHandler.sent) != 1 {
		t.Fatalf("slack handler received %d requests, want 1", len(slackHandler.sent))
	}
	if len(tgHandler.sent) != 0 {
		t.Fatalf("telegram handler received %d requests, want 0 — the project's approval_source: slack override must not fall back to the global telegram default", len(tgHandler.sent))
	}
	if got, want := slackHandler.sent[0].PreferredChannel, "slack"; got != want {
		t.Errorf("PreferredChannel = %q, want %q", got, want)
	}
}

// TestController_ProjectApprovalOverride_NilIsRegressionSafe_GH4774 is the
// regression guard from the acceptance criteria: a controller constructed
// with no WithApprovalOverride (nil projectApproval, i.e. every config that
// predates GH-4774) must resolve to exactly the same require_approval /
// approval_source values cfg.ResolvedEnvOrDefault().RequireApproval and
// cfg.EffectiveApprovalSource() would have produced directly — byte-identical
// behavior for every existing config.
func TestController_ProjectApprovalOverride_NilIsRegressionSafe_GH4774(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.Environment = EnvProd
	cfg.ApprovalSource = ApprovalSourceSlack

	c := NewController(cfg, ghClient, nil, "acme", "no-override")

	if got, want := c.ResolvedRequireApproval(), cfg.ResolvedEnvOrDefault().RequireApproval; got != want {
		t.Errorf("ResolvedRequireApproval() = %v, want %v (cfg.ResolvedEnvOrDefault().RequireApproval, unchanged pre-GH-4774 behavior)", got, want)
	}
	if got, want := c.ResolvedApprovalSource(), cfg.EffectiveApprovalSource(); got != want {
		t.Errorf("ResolvedApprovalSource() = %q, want %q (cfg.EffectiveApprovalSource(), unchanged pre-GH-4774 behavior)", got, want)
	}
}
