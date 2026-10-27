package main

import (
	"time"

	"github.com/qf-studio/pilot/internal/config"
)

// Sequential-mode fallbacks used when orchestrator.execution leaves a value
// unset. They mirror the SDK poller's own defaults (wait true, 30s, 1h), so
// the banner, the epic merge waiter and the SDK poller agree on what "unset"
// means.
const (
	defaultPRPollInterval = 30 * time.Second
	defaultPRTimeout      = 1 * time.Hour
)

// executionWaitSettings is the orchestrator.execution wait_for_merge /
// poll_interval / pr_timeout triple as configured (GH-5903). The raw fields
// are what the SDK PollerDeps carries — nil / 0 / 0 when nothing is set so
// the SDK defaults apply — and the Wait/Interval/Timeout accessors give the
// effective values the banner and the epic merge waiter use. One resolver
// feeds all three sites, so they cannot drift apart again (#5897, #5898).
type executionWaitSettings struct {
	WaitForMerge *bool
	PollInterval time.Duration
	PRTimeout    time.Duration
}

// resolveExecutionWaitSettings reads the triple from cfg. Durations are
// carried only when greater than zero; wait_for_merge is carried as
// configured whenever an execution block exists.
func resolveExecutionWaitSettings(cfg *config.Config) executionWaitSettings {
	var s executionWaitSettings
	if cfg == nil || cfg.Orchestrator == nil || cfg.Orchestrator.Execution == nil {
		return s
	}
	execCfg := cfg.Orchestrator.Execution
	wait := execCfg.WaitForMerge
	s.WaitForMerge = &wait
	if execCfg.PollInterval > 0 {
		s.PollInterval = execCfg.PollInterval
	}
	if execCfg.PRTimeout > 0 {
		s.PRTimeout = execCfg.PRTimeout
	}
	return s
}

// Wait reports whether sequential mode waits for the PR to merge (default true).
func (s executionWaitSettings) Wait() bool {
	return s.WaitForMerge == nil || *s.WaitForMerge
}

// Interval is the effective PR poll interval.
func (s executionWaitSettings) Interval() time.Duration {
	if s.PollInterval > 0 {
		return s.PollInterval
	}
	return defaultPRPollInterval
}

// Timeout is the effective PR merge timeout.
func (s executionWaitSettings) Timeout() time.Duration {
	if s.PRTimeout > 0 {
		return s.PRTimeout
	}
	return defaultPRTimeout
}

// sequentialBannerLine is the startup line for sequential mode, printed from
// the same effective values the poller receives.
func (s executionWaitSettings) sequentialBannerLine() string {
	if !s.Wait() {
		return "   ◌ sequential mode · not waiting for PR merge\n"
	}
	return "   ◌ sequential mode · waiting for PR merge before next issue (poll: " +
		s.Interval().String() + ", timeout: " + s.Timeout().String() + ")\n"
}
