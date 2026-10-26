package executor

import (
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
// now. A later call while paused only moves the resume later; it never adds an
// owner and never alerts again. Running tasks are untouched and the single
// task's own re-queue at reset time stays with the poller's Scheduler.
func (d *Dispatcher) armUsageLimitPause(taskID, projectPath, errMsg string) {
	cfg := d.usageLimitPauseConfig()
	if !cfg.IsEnabled() {
		return
	}

	clock := d.pauseClockOrReal()
	now := clock.Now()

	var resetAt time.Time
	resumeAt := now.Add(cfg.EffectiveFallbackPause())
	if info, ok := parseRateLimitErrorAt(errMsg, now); ok {
		if candidate := info.ResetTime.Add(DefaultSchedulerConfig().RetryBuffer); candidate.After(now) {
			resetAt, resumeAt = info.ResetTime, candidate
		}
	}
	// Decision: max_pause bounds the hold from now, so a bad or far-future
	// parsed reset can never wedge admission for more than the operator allows.
	if limit := now.Add(cfg.EffectiveMaxPause()); resumeAt.After(limit) {
		resumeAt = limit
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
	d.log.Info(msg, attrs...)
	if !extended {
		d.emitUsageLimitPauseAlert(taskID, projectPath, resetAt, resumeAt)
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
