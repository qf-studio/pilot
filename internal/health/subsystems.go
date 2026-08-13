package health

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

// SubsystemCheck reports whether a subsystem that can silently no-op is
// actually wired up, plus the reason for its Y/N state. Unlike the
// presence-only checks in checkConfig (e.g. "telegram.bot_token missing"),
// these checks target subsystems that look configured but never actually run
// because a second, less obvious gate (a missing wiring call, an unresolved
// env-scoped policy, a binary that's not on PATH) keeps them inert — the
// class of bug that GH-3718/GH-3826/GH-3839 kept finding one at a time.
type SubsystemCheck struct {
	Name   string
	Wired  bool
	Reason string
}

// CheckDisabledSubsystems evaluates each known configured-but-possibly-inert
// subsystem from static config (and, where the wiring gap is a build-time
// fact rather than a config toggle, from the current code path) so `pilot
// doctor` can surface silent no-ops instead of leaving them to be discovered
// in production (GH-3839).
func CheckDisabledSubsystems(cfg *config.Config) []SubsystemCheck {
	checks := []SubsystemCheck{}

	wired, reason := checkAlertEngineStarted(cfg)
	checks = append(checks, SubsystemCheck{Name: "alert engine started", Wired: wired, Reason: reason})

	wired, reason = checkAlertProcessorWiredOnRunner(cfg)
	checks = append(checks, SubsystemCheck{Name: "alert processor wired on runner", Wired: wired, Reason: reason})

	wired, reason = checkReleaserResolved(cfg)
	checks = append(checks, SubsystemCheck{Name: "releaser resolved", Wired: wired, Reason: reason})

	wired, reason = checkModelRoutingEnabled(cfg)
	checks = append(checks, SubsystemCheck{Name: "model routing enabled", Wired: wired, Reason: reason})

	wired, reason = checkSubprocessLimitsEnabled(cfg)
	checks = append(checks, SubsystemCheck{Name: "subprocess_limits enabled", Wired: wired, Reason: reason})

	wired, reason = checkSubprocessLimitsCgroupAvailable(cfg)
	checks = append(checks, SubsystemCheck{Name: "subprocess_limits cgroup v2 available", Wired: wired, Reason: reason})

	wired, reason = checkIntentClassifier(cfg)
	checks = append(checks, SubsystemCheck{Name: "intent classifier", Wired: wired, Reason: reason})

	wired, reason = checkApprovalChannelDeliverable(cfg)
	checks = append(checks, SubsystemCheck{Name: "approval channel deliverable", Wired: wired, Reason: reason})

	wired, reason = checkAlertRuleCoverage(cfg)
	checks = append(checks, SubsystemCheck{Name: "alert rule coverage", Wired: wired, Reason: reason})

	return checks
}

// checkAlertEngineStarted reports whether the alerts engine would actually
// start (cfg.alerts.enabled=true) and has somewhere to dispatch to.
func checkAlertEngineStarted(cfg *config.Config) (bool, string) {
	if cfg.Alerts == nil || !cfg.Alerts.Enabled {
		return false, "alerts.enabled=false — no rule can ever fire"
	}
	if len(cfg.Alerts.Channels) == 0 {
		return false, "alerts.enabled=true but no channels configured — engine starts with nowhere to dispatch"
	}
	return true, fmt.Sprintf("%d channel(s) configured", len(cfg.Alerts.Channels))
}

// checkAlertProcessorWiredOnRunner reports whether cmd/pilot's active start
// path calls executor.Runner.SetAlertProcessor once the alert engine starts.
// GH-394 originally left runPollingMode (cmd/pilot/main.go) — the default
// path whenever GitHub or Telegram polling is enabled — never calling
// SetAlertProcessor, so task-lifecycle events (stagnation/OOM/retry/
// escalation/dead-man, emitted via Runner.emitAlertEvent) were dropped
// silently even with a healthy, running alert engine. GH-4716 fixed that:
// main.go now calls runner.SetAlertProcessor immediately after
// alertsEngine.Start succeeds (main.go:3058), and internal/pilot's
// gateway/orchestrator path wires the equivalent
// orchestrator.SetAlertProcessor the same way (pilot.go's initAlerts). GH-4866
// found this check still hardcoded to `return false` long after that fix
// landed — a permanently-red doctor line trains operators to ignore doctor
// entirely, which is worse than no check at all. This is a build-time fact
// about the current start paths, not a config toggle — update this (not just
// flip the literal) if a start path is ever added that starts the alert
// engine without also wiring the processor.
func checkAlertProcessorWiredOnRunner(cfg *config.Config) (bool, string) {
	if cfg.Alerts == nil || !cfg.Alerts.Enabled {
		return false, "alert engine not started (see \"alert engine started\")"
	}
	return true, "runner.SetAlertProcessor is called once the alert engine starts (cmd/pilot/main.go runPollingMode; internal/pilot/pilot.go initAlerts for the gateway/orchestrator path)"
}

// checkReleaserResolved mirrors autopilot.resolveRelease's env-scoped-wins-
// over-global resolution so doctor can report whether a releaser would
// actually be constructed, without needing autopilot's unexported helper.
func checkReleaserResolved(cfg *config.Config) (bool, string) {
	if cfg.Orchestrator == nil || cfg.Orchestrator.Autopilot == nil {
		return false, "orchestrator.autopilot not configured"
	}
	ap := cfg.Orchestrator.Autopilot
	if !ap.Enabled {
		return false, "orchestrator.autopilot.enabled=false"
	}

	source := "global"
	relCfg := ap.Release
	if env := ap.ResolvedEnvOrDefault(); env != nil && env.Release != nil {
		relCfg = env.Release
		source = "env:" + ap.EnvironmentName()
	}

	if relCfg == nil {
		return false, "no release policy configured (autopilot.release or environments.<env>.release)"
	}
	if !relCfg.Enabled {
		return false, fmt.Sprintf("release policy present but disabled (source: %s)", source)
	}
	return true, fmt.Sprintf("resolved from %s", source)
}

// checkModelRoutingEnabled reports whether complexity-based model routing is
// configured; without it every task uses executor.default_model regardless
// of complexity.
func checkModelRoutingEnabled(cfg *config.Config) (bool, string) {
	if cfg.Executor == nil || cfg.Executor.ModelRouting == nil {
		return false, "executor.model_routing not set — every task uses default_model"
	}
	return true, "executor.model_routing configured"
}

// checkSubprocessLimitsEnabled reports whether the executor subprocess memory
// cap/telemetry is enabled.
func checkSubprocessLimitsEnabled(cfg *config.Config) (bool, string) {
	if cfg.Executor == nil || cfg.Executor.SubprocessLimits == nil || !cfg.Executor.SubprocessLimits.Enabled {
		return false, "executor.subprocess_limits.enabled=false — no RSS telemetry or memory cap on task subprocesses"
	}
	return true, "executor.subprocess_limits.enabled=true"
}

// checkSubprocessLimitsCgroupAvailable reports whether the host can actually
// enforce the memory cap the operator asked for. GH-4401: the RLIMIT_AS
// implementation that shipped with GH-3028 always "succeeded" (prlimit64
// rarely fails outright) while silently breaking every Node/V8 HTTPS fetch
// inside the subprocess — the failure mode was invisible until 100% of
// executions started dying in production. The replacement (cgroup v2
// memory.max) fails safe instead: it degrades to telemetry-only mode rather
// than corrupting subprocess networking, but that degrade is still silent
// unless surfaced here. Skipped entirely when the cap isn't enabled, since
// there's nothing to warn about.
func checkSubprocessLimitsCgroupAvailable(cfg *config.Config) (bool, string) {
	if cfg.Executor == nil || cfg.Executor.SubprocessLimits == nil || !cfg.Executor.SubprocessLimits.Enabled {
		return false, "subprocess_limits not enabled — cgroup v2 availability not checked"
	}
	available, reason := executor.CgroupV2MemoryAvailable()
	if !available {
		return false, "memory cap will silently degrade to telemetry-only (cooperative NODE_OPTIONS heap bound still applies): " + reason
	}
	return true, reason
}

// checkIntentClassifier reports whether the pre-flight issue-quality judge
// (GH-2802) is enabled and its backend binary is actually on PATH — matching
// the same guard cmd/pilot/main.go uses before wiring it into the poller.
func checkIntentClassifier(cfg *config.Config) (bool, string) {
	if cfg.Executor == nil || cfg.Executor.PreFlightJudge == nil || !cfg.Executor.PreFlightJudge.Enabled {
		return false, "executor.pre_flight_judge.enabled=false — issues dispatch without quality pre-screening"
	}
	claudeCmd := "claude"
	if cfg.Executor.ClaudeCode != nil && cfg.Executor.ClaudeCode.Command != "" {
		claudeCmd = cfg.Executor.ClaudeCode.Command
	}
	if _, err := exec.LookPath(claudeCmd); err != nil {
		return false, fmt.Sprintf("enabled in config but disabled at runtime: %q binary not found on PATH", claudeCmd)
	}
	return true, "executor.pre_flight_judge.enabled=true"
}

// checkApprovalChannelDeliverable reports whether the configured approval
// channel can actually receive the approver's decision. Reuses
// TelegramApprovalStranding (GH-3826) so the send-only warning surfaces here
// too, alongside the other silently-inert subsystems.
func checkApprovalChannelDeliverable(cfg *config.Config) (bool, string) {
	if msg := TelegramApprovalStranding(cfg); msg != "" {
		return false, msg + " — inbound polling is off, taps can never be received"
	}
	if cfg.Approval == nil || !cfg.Approval.Enabled {
		return false, "approval.enabled=false"
	}
	return true, "inbound processing active"
}

// checkAlertRuleCoverage reports whether every AlertType an engine handler
// can emit without its own Condition-based counting (alerts.
// HandlerEmittedAlertTypes — dispatch-loop-breaker, intent-judge streak, and
// every generic dead-man tracker: label-lifecycle, self-review, finish-
// tripwire, push-retry-exhausted) actually has an enabled rule once this
// config runs through the same union-with-defaults conversion the daemon
// start path uses (config.AlertsConfig.ToAlertConfig, which wraps
// alerts.FromConfigAlerts). GH-4866: a persisted config carrying only the
// legacy 5-rule list (task_stuck/task_failed/consecutive_failures/
// daily_spend/budget_depleted, predating the dead-man trackers) used to
// mean every one of those alerts was silently unreachable — the kill-drill's
// central finding. ToAlertConfig's union logic already closes that gap for
// any config going through the real conversion, so this check should read
// as wired=true for any config using that path; a false here means either a
// gap in FromConfigAlerts' union list or a rule explicitly disabled by the
// operator.
func checkAlertRuleCoverage(cfg *config.Config) (bool, string) {
	acfg := cfg.Alerts.ToAlertConfig()
	gaps := alerts.CoverageGaps(acfg)
	if len(gaps) == 0 {
		return true, fmt.Sprintf("all %d handler-emitted alert types have an enabled rule", len(alerts.HandlerEmittedAlertTypes))
	}

	names := make([]string, len(gaps))
	for i, t := range gaps {
		names[i] = string(t)
	}
	return false, fmt.Sprintf("no enabled rule for: %s — these alerts can never fire", strings.Join(names, ", "))
}
