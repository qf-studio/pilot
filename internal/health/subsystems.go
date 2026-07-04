package health

import (
	"fmt"
	"os/exec"

	"github.com/qf-studio/pilot/internal/config"
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

	wired, reason = checkIntentClassifier(cfg)
	checks = append(checks, SubsystemCheck{Name: "intent classifier", Wired: wired, Reason: reason})

	wired, reason = checkApprovalChannelDeliverable(cfg)
	checks = append(checks, SubsystemCheck{Name: "approval channel deliverable", Wired: wired, Reason: reason})

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
// path calls executor.Runner.SetAlertProcessor. GH-394 makes runPollingMode
// (cmd/pilot/main.go) the default whenever GitHub or Telegram polling is
// enabled, and that path never calls SetAlertProcessor — alertsEngine is only
// passed into individual issue-handler calls for one-off ProcessEvent
// invocations, never wired onto the runner itself. That means task-lifecycle
// events (stagnation/OOM/retry/escalation, emitted via Runner.emitAlertEvent)
// are dropped silently even when the alert engine is healthy and running.
// This is a build-time fact about the current start path, not a config
// toggle — update this check if cmd/pilot/main.go is changed to wire
// SetAlertProcessor there (internal/pilot's gateway-only path already does,
// via orchestrator.SetAlertProcessor, but that path is not the default once
// any polling adapter is enabled).
func checkAlertProcessorWiredOnRunner(cfg *config.Config) (bool, string) {
	if cfg.Alerts == nil || !cfg.Alerts.Enabled {
		return false, "alert engine not started (see \"alert engine started\")"
	}
	return false, "runner.SetAlertProcessor is not called on the polling-mode start path (cmd/pilot/main.go runPollingMode) — task lifecycle alerts (stagnation/OOM/retry) are dropped even though the alert engine is running"
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
	if env := ap.ResolvedEnv(); env != nil && env.Release != nil {
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
