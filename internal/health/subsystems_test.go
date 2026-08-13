package health

import (
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

func findSubsystem(checks []SubsystemCheck, name string) *SubsystemCheck {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}

func TestCheckDisabledSubsystems_EmptyConfig_AllDisabled(t *testing.T) {
	cfg := &config.Config{}
	checks := CheckDisabledSubsystems(cfg)

	wantNames := []string{
		"alert engine started",
		"alert processor wired on runner",
		"releaser resolved",
		"model routing enabled",
		"subprocess_limits enabled",
		"subprocess_limits cgroup v2 available",
		"intent classifier",
		"approval channel deliverable",
	}
	for _, name := range wantNames {
		c := findSubsystem(checks, name)
		if c == nil {
			t.Errorf("missing subsystem check %q", name)
			continue
		}
		if c.Wired {
			t.Errorf("subsystem %q: wired=true on an empty config, want false", c.Name)
		}
		if c.Reason == "" {
			t.Errorf("subsystem %q: empty reason", c.Name)
		}
	}
}

func TestCheckAlertEngineStarted(t *testing.T) {
	cfg := &config.Config{}
	if wired, _ := checkAlertEngineStarted(cfg); wired {
		t.Error("nil alerts config: wired=true, want false")
	}

	cfg.Alerts = &config.AlertsConfig{Enabled: true}
	if wired, reason := checkAlertEngineStarted(cfg); wired {
		t.Errorf("enabled but no channels: wired=true, want false (reason=%q)", reason)
	}

	cfg.Alerts.Channels = []config.AlertChannelConfig{{Name: "ops", Type: "slack", Enabled: true}}
	if wired, reason := checkAlertEngineStarted(cfg); !wired {
		t.Errorf("enabled with channels: wired=false, want true (reason=%q)", reason)
	}
}

func TestCheckAlertProcessorWiredOnRunner(t *testing.T) {
	// GH-4866: GH-4716 fixed the underlying gap (cmd/pilot/main.go:3058 now
	// calls runner.SetAlertProcessor right after alertsEngine.Start succeeds),
	// but this check kept hardcoding false — a permanently-red doctor line.
	// Engine.Start (engine.go) never errors on zero channels — it only
	// no-ops when config.Enabled is false — so main.go's SetAlertProcessor
	// call is gated on alerts.enabled alone, not on channels being
	// configured too (that's "alert engine started"'s concern, a distinct
	// question from whether the processor gets wired).
	cfg := &config.Config{}
	if wired, reason := checkAlertProcessorWiredOnRunner(cfg); wired {
		t.Errorf("nil alerts config: wired=true, want false (reason=%q)", reason)
	}

	cfg.Alerts = &config.AlertsConfig{Enabled: false}
	if wired, reason := checkAlertProcessorWiredOnRunner(cfg); wired {
		t.Errorf("alerts.enabled=false: wired=true, want false (reason=%q)", reason)
	}

	cfg.Alerts.Enabled = true
	if wired, reason := checkAlertProcessorWiredOnRunner(cfg); !wired {
		t.Errorf("alerts.enabled=true: wired=false, want true (reason=%q)", reason)
	}
}

func TestCheckReleaserResolved(t *testing.T) {
	cfg := &config.Config{}
	if wired, _ := checkReleaserResolved(cfg); wired {
		t.Error("no autopilot config: wired=true, want false")
	}

	cfg.Orchestrator = &config.OrchestratorConfig{Autopilot: autopilot.DefaultConfig()}
	if wired, reason := checkReleaserResolved(cfg); wired {
		t.Errorf("autopilot disabled: wired=true, want false (reason=%q)", reason)
	}

	cfg.Orchestrator.Autopilot.Enabled = true
	if wired, reason := checkReleaserResolved(cfg); wired {
		t.Errorf("no release policy: wired=true, want false (reason=%q)", reason)
	}

	cfg.Orchestrator.Autopilot.Release = autopilot.DefaultReleaseConfig() // Enabled: false
	if wired, reason := checkReleaserResolved(cfg); wired {
		t.Errorf("release policy present but disabled: wired=true, want false (reason=%q)", reason)
	}

	cfg.Orchestrator.Autopilot.Release.Enabled = true
	if wired, reason := checkReleaserResolved(cfg); !wired {
		t.Errorf("global release policy enabled: wired=false, want true (reason=%q)", reason)
	}

	// Env-scoped release config wins over global.
	cfg.Orchestrator.Autopilot.DefaultEnvironment = "stage-custom"
	envRelease := autopilot.DefaultReleaseConfig()
	envRelease.Enabled = false
	cfg.Orchestrator.Autopilot.Environments = map[string]*autopilot.EnvironmentConfig{
		"stage-custom": {Release: envRelease},
	}
	if err := cfg.Orchestrator.Autopilot.SetActiveEnvironment("stage-custom"); err != nil {
		t.Fatalf("SetActiveEnvironment: %v", err)
	}
	if wired, reason := checkReleaserResolved(cfg); wired {
		t.Errorf("env release policy disabled should win over enabled global: wired=true, want false (reason=%q)", reason)
	}
}

func TestCheckModelRoutingEnabled(t *testing.T) {
	cfg := &config.Config{}
	if wired, _ := checkModelRoutingEnabled(cfg); wired {
		t.Error("nil executor config: wired=true, want false")
	}

	cfg.Executor = &executor.BackendConfig{}
	if wired, _ := checkModelRoutingEnabled(cfg); wired {
		t.Error("nil model_routing: wired=true, want false")
	}

	cfg.Executor.ModelRouting = &executor.ModelRoutingConfig{}
	if wired, reason := checkModelRoutingEnabled(cfg); !wired {
		t.Errorf("model_routing set: wired=false, want true (reason=%q)", reason)
	}
}

func TestCheckSubprocessLimitsEnabled(t *testing.T) {
	cfg := &config.Config{Executor: &executor.BackendConfig{}}
	if wired, _ := checkSubprocessLimitsEnabled(cfg); wired {
		t.Error("nil subprocess_limits: wired=true, want false")
	}

	cfg.Executor.SubprocessLimits = executor.DefaultSubprocessLimitsConfig()
	if wired, reason := checkSubprocessLimitsEnabled(cfg); wired {
		t.Errorf("default subprocess_limits (telemetry-only, enabled=false): wired=true, want false (reason=%q)", reason)
	}

	cfg.Executor.SubprocessLimits.Enabled = true
	if wired, reason := checkSubprocessLimitsEnabled(cfg); !wired {
		t.Errorf("subprocess_limits.enabled=true: wired=false, want true (reason=%q)", reason)
	}
}

// TestCheckSubprocessLimitsCgroupAvailable covers the GH-4401 preflight
// guard: it must not report "wired" when the cap isn't enabled (nothing to
// warn about), and when enabled must directly reflect
// executor.CgroupV2MemoryAvailable() so operators are warned before the cap
// silently degrades to telemetry-only on hosts without cgroup v2 delegation.
func TestCheckSubprocessLimitsCgroupAvailable(t *testing.T) {
	cfg := &config.Config{Executor: &executor.BackendConfig{}}
	if wired, reason := checkSubprocessLimitsCgroupAvailable(cfg); wired {
		t.Errorf("nil subprocess_limits: wired=true, want false (reason=%q)", reason)
	}

	cfg.Executor.SubprocessLimits = executor.DefaultSubprocessLimitsConfig()
	if wired, reason := checkSubprocessLimitsCgroupAvailable(cfg); wired {
		t.Errorf("subprocess_limits.enabled=false: wired=true, want false (reason=%q)", reason)
	}

	cfg.Executor.SubprocessLimits.Enabled = true
	wantAvailable, wantReason := executor.CgroupV2MemoryAvailable()
	gotWired, gotReason := checkSubprocessLimitsCgroupAvailable(cfg)
	if gotWired != wantAvailable {
		t.Errorf("wired=%v, want %v (matching executor.CgroupV2MemoryAvailable)", gotWired, wantAvailable)
	}
	if gotWired && gotReason != wantReason {
		t.Errorf("wired reason = %q, want %q", gotReason, wantReason)
	}
	if !gotWired && gotReason == "" {
		t.Error("unwired: empty reason")
	}
}

func TestCheckIntentClassifier(t *testing.T) {
	cfg := &config.Config{Executor: &executor.BackendConfig{}}
	if wired, _ := checkIntentClassifier(cfg); wired {
		t.Error("nil pre_flight_judge: wired=true, want false")
	}

	cfg.Executor.PreFlightJudge = &executor.PreFlightJudgeConfig{Enabled: true}
	wired, reason := checkIntentClassifier(cfg)
	// Whether this reports true depends on whether "claude" is on the test
	// runner's PATH — assert the two are consistent rather than hardcoding one.
	if wired && reason != "executor.pre_flight_judge.enabled=true" {
		t.Errorf("wired=true but unexpected reason %q", reason)
	}
	if !wired && reason == "" {
		t.Error("wired=false but empty reason")
	}
}

// TestCheckAlertRuleCoverage covers the GH-4866 doctor gap: a persisted
// alerts.rules: list that predates the dead-man/intent-judge/dispatch-
// loop-breaker AlertTypes (the "legacy 5-rule" config.defaultAlertRules
// list) used to mean every one of those alerts was silently unreachable —
// the kill-drill's central finding. config.AlertsConfig.ToAlertConfig
// unions in alerts.defaultRules() for any missing Type, so this check
// should report wired=true through that path; this test also proves the
// check actually looks at rule coverage (not just cfg.Alerts.Enabled) by
// deliberately disabling the one rule GH-4866 cares about most and
// confirming it's named in the reason string.
func TestCheckAlertRuleCoverage(t *testing.T) {
	cfg := &config.Config{}
	if wired, reason := checkAlertRuleCoverage(cfg); wired {
		t.Errorf("nil alerts config: wired=true, want false (no rules exist at all — nothing can fire) (reason=%q)", reason)
	}

	// Legacy 5-rule list, same as config.defaultAlertRules(): predates every
	// dead-man/intent-judge/dispatch-loop-breaker rule.
	cfg.Alerts = &config.AlertsConfig{
		Enabled: true,
		Rules: []config.AlertRuleConfig{
			{Name: "task_stuck", Type: "task_stuck", Enabled: true},
			{Name: "task_failed", Type: "task_failed", Enabled: true},
			{Name: "consecutive_failures", Type: "consecutive_failures", Enabled: true},
			{Name: "daily_spend", Type: "daily_spend_exceeded", Enabled: false},
			{Name: "budget_depleted", Type: "budget_depleted", Enabled: false},
		},
	}
	wired, reason := checkAlertRuleCoverage(cfg)
	if !wired {
		t.Errorf("legacy 5-rule config: wired=false, want true (ToAlertConfig's union should still cover every handler-emitted type) (reason=%q)", reason)
	}

	// Now explicitly disable the label_lifecycle rule the union added —
	// coverage must report the gap.
	cfg.Alerts.Rules = append(cfg.Alerts.Rules, config.AlertRuleConfig{
		Name:    "label_lifecycle_failure_streak",
		Type:    "label_lifecycle_failure_streak",
		Enabled: false,
	})
	wired, reason = checkAlertRuleCoverage(cfg)
	if wired {
		t.Errorf("label_lifecycle_failure_streak explicitly disabled: wired=true, want false (reason=%q)", reason)
	}
	if !strings.Contains(reason, "label_lifecycle_failure_streak") {
		t.Errorf("reason %q does not name the missing rule label_lifecycle_failure_streak", reason)
	}
}

func TestCheckApprovalChannelDeliverable(t *testing.T) {
	cfg := &config.Config{}
	if wired, _ := checkApprovalChannelDeliverable(cfg); wired {
		t.Error("no approval config: wired=true, want false")
	}

	cfg.Approval = approval.DefaultConfig()
	cfg.Approval.Enabled = true
	if wired, reason := checkApprovalChannelDeliverable(cfg); !wired {
		t.Errorf("approval enabled, no telegram stranding: wired=false, want true (reason=%q)", reason)
	}

	// GH-3826: Telegram registered as an approval channel but polling is off.
	cfg.Adapters = &config.AdaptersConfig{
		Telegram: &telegram.Config{Enabled: true, BotToken: "test-telegram-token", Polling: false},
	}
	cfg.Approval.PreMerge = &approval.StageConfig{Enabled: true}
	if wired, reason := checkApprovalChannelDeliverable(cfg); wired {
		t.Errorf("telegram send-only stranding should force wired=false (reason=%q)", reason)
	}
}
