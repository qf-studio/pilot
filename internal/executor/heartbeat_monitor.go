package executor

import "time"

// processLivenessSnapshot captures a point-in-time reading of a subprocess's
// process group: how many processes share it besides the tracked leader, and
// their combined utime+stime CPU ticks. GH-4668: comparing two snapshots
// across a heartbeat tick tells the monitor whether the group is doing real
// work (descendants exist and/or CPU ticks are advancing) even though the
// claude-code leader itself has gone silent on stdout — which is expected
// for the full duration of a long local tool call (e.g. `make test`), not
// evidence of a hang.
type processLivenessSnapshot struct {
	descendants int
	cpuTicks    uint64
}

// processLivenessProbe abstracts probing a process group for live descendant
// PIDs and total CPU ticks. Production code uses probeProcessLiveness
// (heartbeat_liveness_linux.go: /proc scan; heartbeat_liveness_other.go:
// always reports zero descendants/ticks with a nil error, degrading
// non-Linux platforms to the pre-GH-4668 kill-on-silence behavior). Tests
// inject a fake to drive deterministic scenarios without real subprocesses.
type processLivenessProbe func(pgid int) (processLivenessSnapshot, error)

// heartbeatDecision is the outcome of one heartbeat tick evaluation.
type heartbeatDecision int

const (
	// heartbeatNoAction means last_event_age is still within the timeout —
	// nothing to check or log this tick.
	heartbeatNoAction heartbeatDecision = iota
	// heartbeatGrace means the stream is silent but the process group shows
	// live descendants and/or advancing CPU time — do not kill.
	heartbeatGrace
	// heartbeatKill means the process should be killed: either genuinely
	// hung (silent + idle process group), the liveness probe itself failed
	// (fail toward the safe kill-on-silence path), or the task-level
	// watchdog deadline was reached (grace must never extend past it).
	heartbeatKill
)

// heartbeatKillReason explains why heartbeatKill was returned, so the kill
// log line is diagnosable from a single entry (acceptance criterion 3).
type heartbeatKillReason string

const (
	heartbeatKillReasonNone             heartbeatKillReason = ""
	heartbeatKillReasonNoActivity       heartbeatKillReason = "no_activity"
	heartbeatKillReasonProbeError       heartbeatKillReason = "probe_error"
	heartbeatKillReasonWatchdogDeadline heartbeatKillReason = "watchdog_deadline"
)

// heartbeatMonitor tracks process-liveness probe state across ticks and
// decides, on each tick, whether a silent stdout stream represents a hang or
// an in-flight local tool execution (GH-4668). It is a stateful struct
// rather than a free function because the CPU-tick delta check needs the
// previous tick's snapshot, and the grace log needs its own rate limit.
type heartbeatMonitor struct {
	heartbeatTimeout time.Duration
	// watchdogTimeout is the task-level hard deadline (opts.WatchdogTimeout).
	// 0 means no ceiling from this source — grace can be granted for as long
	// as the process group stays live. When > 0, grace must never push a
	// kill past this deadline, matching the separate watchdog goroutine's
	// own hard kill so the two mechanisms agree.
	watchdogTimeout time.Duration
	probe           processLivenessProbe

	haveSnapshot bool
	prevSnapshot processLivenessSnapshot
	lastGraceLog time.Time
}

// newHeartbeatMonitor constructs a heartbeatMonitor. probe must not be nil.
func newHeartbeatMonitor(heartbeatTimeout, watchdogTimeout time.Duration, probe processLivenessProbe) *heartbeatMonitor {
	return &heartbeatMonitor{
		heartbeatTimeout: heartbeatTimeout,
		watchdogTimeout:  watchdogTimeout,
		probe:            probe,
	}
}

// evaluate is called once per heartbeat tick. now/startedAt/lastEventAt
// drive all timing so tests can use a fake clock instead of real sleeps.
// pid is the tracked process group leader's PID, which (thanks to
// configureProcessGroup's Setpgid) is also the process group id (pgid).
func (m *heartbeatMonitor) evaluate(now, startedAt, lastEventAt time.Time, pid int) (decision heartbeatDecision, descendants int, cpuDelta uint64, logGrace bool, reason heartbeatKillReason, probeErr error) {
	age := now.Sub(lastEventAt)
	if age <= m.heartbeatTimeout {
		return heartbeatNoAction, 0, 0, false, heartbeatKillReasonNone, nil
	}

	// GH-4668: the hard backstop must remain absolute — grace can never push
	// a kill past the task-level watchdog deadline, regardless of how live
	// the descendant tree looks.
	if m.watchdogTimeout > 0 && now.Sub(startedAt) >= m.watchdogTimeout {
		return heartbeatKill, 0, 0, false, heartbeatKillReasonWatchdogDeadline, nil
	}

	snap, err := m.probe(pid)
	if err != nil {
		// Fail toward the pre-GH-4668 behavior: an unreadable process tree
		// is not evidence of liveness.
		return heartbeatKill, 0, 0, false, heartbeatKillReasonProbeError, err
	}

	if m.haveSnapshot && snap.cpuTicks > m.prevSnapshot.cpuTicks {
		cpuDelta = snap.cpuTicks - m.prevSnapshot.cpuTicks
	}
	descendants = snap.descendants
	active := descendants > 0 || cpuDelta > 0
	m.prevSnapshot = snap
	m.haveSnapshot = true

	if active {
		logGrace = m.lastGraceLog.IsZero() || now.Sub(m.lastGraceLog) >= heartbeatGraceLogInterval
		if logGrace {
			m.lastGraceLog = now
		}
		return heartbeatGrace, descendants, cpuDelta, logGrace, heartbeatKillReasonNone, nil
	}

	return heartbeatKill, descendants, cpuDelta, false, heartbeatKillReasonNoActivity, nil
}
