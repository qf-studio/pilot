package executor

import (
	"context"
	"log/slog"
	"sort"
	"time"
)

// AdmissionPauseOwnerUsageLimit is the Dispatcher.PauseAdmissionFor /
// ResumeAdmissionFor owner key for the usage-limit pause (GH-5862). Distinct
// from admissionPauseDefaultOwner (self-upgrade drain) and
// autopilot.PlatformBreakerAdmissionPauseOwner so the three never undo one
// another.
const AdmissionPauseOwnerUsageLimit = "usage-limit"

// pauseClock is the dispatcher's time seam for the usage-limit pause, so tests
// can drive the resume without sleeping.
type pauseClock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) pauseTimer
}

type pauseTimer interface {
	Stop() bool
}

type realPauseClock struct{}

func (realPauseClock) Now() time.Time { return time.Now() }
func (realPauseClock) AfterFunc(d time.Duration, f func()) pauseTimer {
	return time.AfterFunc(d, f)
}

// AdmissionStatus is a point-in-time view of the dispatcher's admission pause,
// surfaced on /api/v1/status.
type AdmissionStatus struct {
	// Paused is true while any owner holds admission paused.
	Paused bool
	// Owners lists the owners currently holding the pause, sorted.
	Owners []string
	// UsageLimitResumesAt is when the usage-limit owner releases; zero unless
	// that owner is set.
	UsageLimitResumesAt time.Time
}

// AdmissionStatus reports which owners hold admission paused and, for the
// usage-limit owner, when it resumes.
func (d *Dispatcher) AdmissionStatus() AdmissionStatus {
	d.admissionPauseMu.Lock()
	owners := make([]string, 0, len(d.admissionPauseOwners))
	for o := range d.admissionPauseOwners {
		owners = append(owners, o)
	}
	d.admissionPauseMu.Unlock()
	sort.Strings(owners)

	st := AdmissionStatus{Paused: len(owners) > 0, Owners: owners}
	for _, o := range owners {
		if o == AdmissionPauseOwnerUsageLimit {
			d.usageLimitMu.Lock()
			st.UsageLimitResumesAt = d.usageLimitResumeAt
			d.usageLimitMu.Unlock()
			break
		}
	}
	return st
}

func (d *Dispatcher) pauseClockOrReal() pauseClock {
	if d.clock != nil {
		return d.clock
	}
	return realPauseClock{}
}

func (d *Dispatcher) usageLimitPauseConfig() *UsageLimitPauseConfig {
	if d.runner != nil && d.runner.config != nil {
		return d.runner.config.UsageLimitPause
	}
	return nil
}

// armUsageLimitPause holds admission after taskID finalized rate_limited with
// errMsg (GH-5862). The resume is the parsed reset plus the scheduler's
// RetryBuffer (fallback_pause when no reset parses), capped at max_pause from
// now. A reset that already passed (GH-5879) resumes at now plus RetryBuffer. A later call while paused only moves the resume later; it never adds an
// owner and never alerts again. Running tasks are untouched and the single
// task's own re-queue at reset time stays with the poller's Scheduler.
//
// With usage_limit_pause.fallback api_key and the subscription in force
// (GH-6069), the episode switches the daemon to the API key until the same
// resumeAt through the runtime billing override and does not pause admission;
// the override's own expiry timer is the revert. When the key does not resolve
// the pause applies as without the fallback, and one
// billing_fallback_unavailable alert accompanies the usage_limit_pause alert.
//
// Decision: the episode is synchronous (GH-6073). The worker's onRateLimited
// callback must return with admission gated, because the worker loops to its
// admission check and the next queued pick right after it: either the api_key
// override is in force or the usage-limit pause is armed. That costs the
// worker up to DefaultBillingKeyCommandTimeout on api_key_command before the
// terminal persist, accepted because a usage-limit episode is rare and the
// next pick must wait for the decision. Nothing outlives Stop.
//
// A limit that arrives while the fallback override already serves (a peer
// installed it first, or the task froze its subscription credential before the
// switch) is not a new episode: no pause, no alert. A limit with the mode in
// force api_key from the cli, gateway or config is API-side and pauses.
func (d *Dispatcher) armUsageLimitPause(taskID, projectPath, errMsg string) {
	d.applyUsageLimit(taskID, projectPath, errMsg)
}

// usageLimitFallbackConfigured reports whether a usage limit may try the API
// key: usage_limit_pause.fallback api_key on a claude-code backend.
func (d *Dispatcher) usageLimitFallbackConfigured(cfg *UsageLimitPauseConfig) bool {
	return cfg.EffectiveFallback() == UsageLimitFallbackAPIKey && d.billingApplies()
}

// applyUsageLimit is the body of armUsageLimitPause; it may block on the key
// command when the fallback is configured.
func (d *Dispatcher) applyUsageLimit(taskID, projectPath, errMsg string) {
	cfg := d.usageLimitPauseConfig()
	if !cfg.IsEnabled() {
		return
	}

	clock := d.pauseClockOrReal()
	now := clock.Now()

	var resetAt time.Time
	var alreadyReset bool
	retryBuffer := DefaultSchedulerConfig().RetryBuffer
	resumeAt := now.Add(cfg.EffectiveFallbackPause())
	if info, ok := parseRateLimitErrorAt(errMsg, now); ok {
		if info.AlreadyReset {
			resetAt, resumeAt, alreadyReset = info.ResetTime, now.Add(retryBuffer), true
		} else if candidate := info.ResetTime.Add(retryBuffer); candidate.After(now) {
			resetAt, resumeAt = info.ResetTime, candidate
		}
	}
	// Decision: max_pause bounds the hold from now, so a bad or far-future
	// parsed reset can never wedge admission for more than the operator allows.
	if limit := now.Add(cfg.EffectiveMaxPause()); resumeAt.After(limit) {
		resumeAt = limit
	}

	var fallbackErr error
	if d.usageLimitFallbackConfigured(cfg) {
		// Decision: the key resolves outside the mutex (it can block for the
		// resolver timeout and concurrent episodes may overlap), but the mode
		// is re-read under it, and so is the pause a failed fallback arms:
		// holding the mutex through the pause below keeps a peer's install
		// from landing between the check and the arm.
		resolver := d.billingResolverOrConfig()
		var keyErr error
		tried := resolver.Mode() == BillingModeSubscription
		if tried {
			keyErr = d.resolveFallbackKey(resolver)
		}

		d.usageLimitFallbackMu.Lock()
		defer d.usageLimitFallbackMu.Unlock()
		if mode := d.BillingOverride(); mode.Mode == string(BillingModeAPIKey) && mode.Source == BillingModeSourceFallback {
			d.log.Debug("usage limit hit, fallback already serving", slog.String("task_id", taskID))
			if keyErr != nil {
				d.emitBillingFallbackUnavailableAlert(taskID, projectPath, resetAt, resumeAt, keyErr)
			}
			return
		}
		if tried && resolver.Mode() == BillingModeSubscription {
			if keyErr == nil {
				keyErr = d.installAPIKeyFallback(resumeAt)
			}
			if keyErr == nil {
				attrs := []any{slog.String("task_id", taskID), slog.Time("until", resumeAt)}
				if !resetAt.IsZero() {
					attrs = append(attrs, slog.Time("reset_at", resetAt))
				}
				d.log.Info("usage limit hit, billing switched to api_key until the reset", attrs...)
				return
			}
			fallbackErr = keyErr
		}
	}

	d.usageLimitMu.Lock()
	defer d.usageLimitMu.Unlock()

	extended := !d.usageLimitResumeAt.IsZero()
	if extended && !resumeAt.After(d.usageLimitResumeAt) {
		d.log.Debug("usage-limit admission pause already holds a later resume",
			slog.String("task_id", taskID),
			slog.Time("resumes_at", d.usageLimitResumeAt),
		)
		return
	}

	if d.usageLimitTimer != nil {
		d.usageLimitTimer.Stop()
	}
	d.usageLimitGen++
	gen := d.usageLimitGen
	d.usageLimitResumeAt = resumeAt
	d.usageLimitTimer = clock.AfterFunc(resumeAt.Sub(now), func() { d.releaseUsageLimitPause(gen) })
	d.PauseAdmissionFor(AdmissionPauseOwnerUsageLimit)

	msg := "usage limit hit, task admission paused"
	if extended {
		msg = "usage limit hit again, task admission pause extended"
	}
	attrs := []any{
		slog.String("owner", AdmissionPauseOwnerUsageLimit),
		slog.Time("resumes_at", resumeAt),
		slog.String("task_id", taskID),
	}
	if !resetAt.IsZero() {
		attrs = append(attrs, slog.Time("reset_at", resetAt))
	}
	if alreadyReset {
		attrs = append(attrs, slog.String("reset_note", "reset had already passed; resuming after retry buffer"))
	}
	d.log.Info(msg, attrs...)
	if !extended {
		d.emitUsageLimitPauseAlert(taskID, projectPath, resetAt, resumeAt)
		if fallbackErr != nil {
			d.emitBillingFallbackUnavailableAlert(taskID, projectPath, resetAt, resumeAt, fallbackErr)
		}
	}
}

// releaseUsageLimitPause is the timer callback; a stale generation (the pause
// was extended after this timer was set) is ignored.
func (d *Dispatcher) releaseUsageLimitPause(gen uint64) {
	d.usageLimitMu.Lock()
	defer d.usageLimitMu.Unlock()
	if gen != d.usageLimitGen || d.usageLimitResumeAt.IsZero() {
		return
	}
	d.usageLimitResumeAt = time.Time{}
	d.usageLimitTimer = nil
	d.ResumeAdmissionFor(AdmissionPauseOwnerUsageLimit)
	d.log.Info("usage-limit admission pause elapsed, task admission resumed",
		slog.String("owner", AdmissionPauseOwnerUsageLimit))
}

// stopUsageLimitPauseTimer cancels the pending resume on Stop. The pause is
// in-memory, so the process exiting drops it anyway.
func (d *Dispatcher) stopUsageLimitPauseTimer() {
	d.usageLimitMu.Lock()
	defer d.usageLimitMu.Unlock()
	if d.usageLimitTimer != nil {
		d.usageLimitTimer.Stop()
		d.usageLimitTimer = nil
	}
}

func (d *Dispatcher) emitUsageLimitPauseAlert(taskID, projectPath string, resetAt, resumeAt time.Time) {
	if d.runner == nil || d.runner.AlertProcessor() == nil {
		return
	}
	meta := map[string]string{
		"owner":      AdmissionPauseOwnerUsageLimit,
		"resumes_at": resumeAt.Format(time.RFC3339),
	}
	if !resetAt.IsZero() {
		meta["reset_at"] = resetAt.Format(time.RFC3339)
	}
	d.runner.AlertProcessor().ProcessEvent(AlertEvent{
		Type:      AlertEventTypeUsageLimitPause,
		TaskID:    taskID,
		Project:   projectPath,
		Metadata:  meta,
		Timestamp: d.pauseClockOrReal().Now(),
	})
}

// resolveFallbackKey checks that the billing API key resolves without changing
// the mode (GH-6069). The resolved key is zeroed at once: admission resolves
// its own while the override is in force. The error is the resolver's and
// never carries the key.
func (d *Dispatcher) resolveFallbackKey(resolver *BillingResolver) error {
	if resolver.cfg == nil {
		return &BillingKeyError{Reason: "no billing key source configured", ExitCode: -1}
	}
	key, err := resolver.resolveKey(context.Background())
	if err != nil {
		return err
	}
	for i := range key {
		key[i] = 0
	}
	return nil
}

// installAPIKeyFallback installs the api_key override until until with source
// fallback and releases an older usage-limit pause still armed, as the override
// now carries the episode. The caller holds usageLimitFallbackMu with the
// subscription in force.
func (d *Dispatcher) installAPIKeyFallback(until time.Time) error {
	if err := d.SetBillingOverride(string(BillingModeAPIKey), until, BillingModeSourceFallback); err != nil {
		return err
	}
	d.releaseUsageLimitPauseForFallback()
	return nil
}

// releaseUsageLimitPauseForFallback drops a usage-limit pause an earlier
// episode left armed (its key did not resolve then): the override now serves
// admission, so holding it until that older resume would be wrong.
func (d *Dispatcher) releaseUsageLimitPauseForFallback() {
	d.usageLimitMu.Lock()
	defer d.usageLimitMu.Unlock()
	if d.usageLimitResumeAt.IsZero() {
		return
	}
	if d.usageLimitTimer != nil {
		d.usageLimitTimer.Stop()
		d.usageLimitTimer = nil
	}
	d.usageLimitGen++
	d.usageLimitResumeAt = time.Time{}
	d.ResumeAdmissionFor(AdmissionPauseOwnerUsageLimit)
	d.log.Info("usage-limit admission pause released, billing fallback serves admission",
		slog.String("owner", AdmissionPauseOwnerUsageLimit))
}

func (d *Dispatcher) emitBillingFallbackUnavailableAlert(taskID, projectPath string, resetAt, until time.Time, cause error) {
	if d.runner == nil || d.runner.AlertProcessor() == nil {
		return
	}
	// reset_at is always present, empty when no reset parsed.
	resetStr := ""
	if !resetAt.IsZero() {
		resetStr = resetAt.Format(time.RFC3339)
	}
	meta := map[string]string{
		"reset_at": resetStr,
		"until":    until.Format(time.RFC3339),
		"error":    cause.Error(),
	}
	d.runner.AlertProcessor().ProcessEvent(AlertEvent{
		Type:      AlertEventTypeBillingFallbackUnavailable,
		TaskID:    taskID,
		Project:   projectPath,
		Error:     cause.Error(),
		Metadata:  meta,
		Timestamp: d.pauseClockOrReal().Now(),
	})
}
