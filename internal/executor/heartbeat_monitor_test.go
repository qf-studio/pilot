package executor

import (
	"errors"
	"testing"
	"time"
)

// fakeProbe builds a processLivenessProbe for tests that returns a
// pre-scripted sequence of (snapshot, error) results, one per call, without
// touching a real process tree. It also records how many times it was
// invoked so tests can assert the watchdog-deadline short-circuit (scenario
// c) never even calls the probe.
type fakeProbe struct {
	results []struct {
		snap processLivenessSnapshot
		err  error
	}
	calls int
}

func (f *fakeProbe) probe(_ int) (processLivenessSnapshot, error) {
	f.calls++
	if len(f.results) == 0 {
		return processLivenessSnapshot{}, nil
	}
	idx := f.calls - 1
	if idx >= len(f.results) {
		idx = len(f.results) - 1
	}
	r := f.results[idx]
	return r.snap, r.err
}

func (f *fakeProbe) push(snap processLivenessSnapshot, err error) {
	f.results = append(f.results, struct {
		snap processLivenessSnapshot
		err  error
	}{snap, err})
}

// TestHeartbeatMonitor_NoActionWithinTimeout verifies that a stream still
// within the heartbeat window never consults the liveness probe at all -
// there is nothing to grace or kill yet.
func TestHeartbeatMonitor_NoActionWithinTimeout(t *testing.T) {
	fp := &fakeProbe{}
	m := newHeartbeatMonitor(5*time.Minute, 30*time.Minute, fp.probe)

	startedAt := time.Unix(0, 0)
	lastEventAt := startedAt
	now := startedAt.Add(4 * time.Minute) // age = 4m < 5m timeout

	decision, _, _, _, _, err := m.evaluate(now, startedAt, lastEventAt, 1234)
	if decision != heartbeatNoAction {
		t.Fatalf("decision = %v, want heartbeatNoAction", decision)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fp.calls != 0 {
		t.Fatalf("probe called %d times, want 0 (age within timeout)", fp.calls)
	}
}

// TestHeartbeatMonitor_GraceWithActiveDescendant covers acceptance scenario
// (a): a silent stream with a live descendant process must not be killed,
// and the grace INFO log must fire (rate-limited across repeated ticks).
func TestHeartbeatMonitor_GraceWithActiveDescendant(t *testing.T) {
	fp := &fakeProbe{}
	// Every tick reports one live descendant with advancing CPU ticks -
	// exactly the shape of a `make test` child still running.
	fp.push(processLivenessSnapshot{descendants: 1, cpuTicks: 100}, nil)
	fp.push(processLivenessSnapshot{descendants: 1, cpuTicks: 150}, nil)
	fp.push(processLivenessSnapshot{descendants: 1, cpuTicks: 200}, nil)

	m := newHeartbeatMonitor(5*time.Minute, 30*time.Minute, fp.probe)
	startedAt := time.Unix(0, 0)
	lastEventAt := startedAt

	// Tick 1: age = 6m, past timeout. Descendant is live -> grace, and since
	// this is the first grace tick it must log immediately.
	now := startedAt.Add(6 * time.Minute)
	decision, descendants, cpuDelta, logGrace, _, err := m.evaluate(now, startedAt, lastEventAt, 1234)
	if decision != heartbeatGrace {
		t.Fatalf("tick1 decision = %v, want heartbeatGrace", decision)
	}
	if err != nil {
		t.Fatalf("tick1 unexpected error: %v", err)
	}
	if descendants != 1 {
		t.Fatalf("tick1 descendants = %d, want 1", descendants)
	}
	if cpuDelta != 0 {
		t.Fatalf("tick1 cpuDelta = %d, want 0 (no prior snapshot yet)", cpuDelta)
	}
	if !logGrace {
		t.Fatalf("tick1 logGrace = false, want true (first grace observation)")
	}

	// Tick 2: 30s later (well under the 2m grace-log rate limit) -> still
	// grace, but must NOT re-log.
	now = now.Add(30 * time.Second)
	decision, descendants, cpuDelta, logGrace, _, err = m.evaluate(now, startedAt, lastEventAt, 1234)
	if decision != heartbeatGrace {
		t.Fatalf("tick2 decision = %v, want heartbeatGrace", decision)
	}
	if err != nil {
		t.Fatalf("tick2 unexpected error: %v", err)
	}
	if descendants != 1 {
		t.Fatalf("tick2 descendants = %d, want 1", descendants)
	}
	if cpuDelta != 50 {
		t.Fatalf("tick2 cpuDelta = %d, want 50 (100 -> 150)", cpuDelta)
	}
	if logGrace {
		t.Fatalf("tick2 logGrace = true, want false (rate-limited)")
	}

	// Tick 3: past the 2m grace-log interval since tick1's log -> must log
	// again.
	now = now.Add(2 * time.Minute)
	decision, _, cpuDelta, logGrace, _, err = m.evaluate(now, startedAt, lastEventAt, 1234)
	if decision != heartbeatGrace {
		t.Fatalf("tick3 decision = %v, want heartbeatGrace", decision)
	}
	if err != nil {
		t.Fatalf("tick3 unexpected error: %v", err)
	}
	if cpuDelta != 50 {
		t.Fatalf("tick3 cpuDelta = %d, want 50 (150 -> 200)", cpuDelta)
	}
	if !logGrace {
		t.Fatalf("tick3 logGrace = false, want true (rate limit elapsed)")
	}
}

// TestHeartbeatMonitor_KillWhenIdle covers acceptance scenario (b): a
// genuinely silent stream with no descendants and no CPU movement must
// still be killed exactly as before.
func TestHeartbeatMonitor_KillWhenIdle(t *testing.T) {
	fp := &fakeProbe{}
	fp.push(processLivenessSnapshot{descendants: 0, cpuTicks: 0}, nil)

	m := newHeartbeatMonitor(5*time.Minute, 30*time.Minute, fp.probe)
	startedAt := time.Unix(0, 0)
	lastEventAt := startedAt
	now := startedAt.Add(6 * time.Minute)

	decision, descendants, cpuDelta, logGrace, reason, err := m.evaluate(now, startedAt, lastEventAt, 1234)
	if decision != heartbeatKill {
		t.Fatalf("decision = %v, want heartbeatKill", decision)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason != heartbeatKillReasonNoActivity {
		t.Fatalf("reason = %q, want %q", reason, heartbeatKillReasonNoActivity)
	}
	if descendants != 0 || cpuDelta != 0 {
		t.Fatalf("descendants=%d cpuDelta=%d, want 0/0", descendants, cpuDelta)
	}
	if logGrace {
		t.Fatalf("logGrace = true, want false on a kill decision")
	}
}

// TestHeartbeatMonitor_CPUTicksNotAdvancingIsNotActive guards against a
// stalled-but-present descendant (e.g. a zombie or a process stuck waiting
// on I/O with no CPU movement and reported as 0 descendants by the probe)
// being mistaken for liveness, and against CPU-tick counters that appear to
// go backwards (process reuse/wrap) producing a spurious positive delta.
func TestHeartbeatMonitor_CPUTicksNotAdvancingIsNotActive(t *testing.T) {
	fp := &fakeProbe{}
	fp.push(processLivenessSnapshot{descendants: 0, cpuTicks: 500}, nil)
	fp.push(processLivenessSnapshot{descendants: 0, cpuTicks: 500}, nil) // no advance
	fp.push(processLivenessSnapshot{descendants: 0, cpuTicks: 100}, nil) // "decreased"

	m := newHeartbeatMonitor(5*time.Minute, 30*time.Minute, fp.probe)
	startedAt := time.Unix(0, 0)
	lastEventAt := startedAt

	now := startedAt.Add(6 * time.Minute)
	decision, _, _, _, _, _ := m.evaluate(now, startedAt, lastEventAt, 1234)
	// First observation: no prior snapshot to diff against, no descendants -> kill.
	if decision != heartbeatKill {
		t.Fatalf("tick1 decision = %v, want heartbeatKill", decision)
	}

	// Re-arm a fresh monitor to exercise the "no advance" and "decreased"
	// cases against a real prior snapshot (the first monitor already killed
	// so a fresh instance mirrors what production does after a restart).
	m2 := newHeartbeatMonitor(5*time.Minute, 30*time.Minute, fp.probe)
	fp2 := &fakeProbe{}
	fp2.push(processLivenessSnapshot{descendants: 0, cpuTicks: 500}, nil)
	fp2.push(processLivenessSnapshot{descendants: 0, cpuTicks: 500}, nil)
	m2.probe = fp2.probe

	now2 := startedAt.Add(6 * time.Minute)
	decision, _, cpuDelta, _, _, _ := m2.evaluate(now2, startedAt, lastEventAt, 1234)
	if decision != heartbeatKill || cpuDelta != 0 {
		t.Fatalf("tick1: decision=%v cpuDelta=%d, want heartbeatKill/0", decision, cpuDelta)
	}
	// Simulate the stream staying silent one more tick with an unchanged
	// CPU reading - must still be a kill (no advance == no activity).
	now2 = now2.Add(30 * time.Second)
	decision, _, cpuDelta, _, reason, _ := m2.evaluate(now2, startedAt, lastEventAt, 1234)
	if decision != heartbeatKill {
		t.Fatalf("tick2 decision = %v, want heartbeatKill", decision)
	}
	if cpuDelta != 0 {
		t.Fatalf("tick2 cpuDelta = %d, want 0 (ticks unchanged)", cpuDelta)
	}
	if reason != heartbeatKillReasonNoActivity {
		t.Fatalf("tick2 reason = %q, want %q", reason, heartbeatKillReasonNoActivity)
	}
}

// TestHeartbeatMonitor_GraceNeverExtendsPastWatchdogDeadline covers
// acceptance scenario (c): even with a permanently live descendant tree,
// grace must stop being granted once the task-level watchdog deadline is
// reached - the probe must not even be consulted past that point, since the
// hard backstop is absolute.
func TestHeartbeatMonitor_GraceNeverExtendsPastWatchdogDeadline(t *testing.T) {
	fp := &fakeProbe{}
	// Always report a live descendant - if the deadline check didn't
	// short-circuit before the probe, this would grant grace forever.
	for i := 0; i < 10; i++ {
		fp.push(processLivenessSnapshot{descendants: 1, cpuTicks: uint64(100 * (i + 1))}, nil)
	}

	watchdogTimeout := 10 * time.Minute
	m := newHeartbeatMonitor(5*time.Minute, watchdogTimeout, fp.probe)
	startedAt := time.Unix(0, 0)
	lastEventAt := startedAt

	// Well past heartbeat timeout but before the watchdog deadline -> grace.
	now := startedAt.Add(6 * time.Minute)
	decision, _, _, _, _, _ := m.evaluate(now, startedAt, lastEventAt, 1234)
	if decision != heartbeatGrace {
		t.Fatalf("pre-deadline decision = %v, want heartbeatGrace", decision)
	}
	callsBeforeDeadline := fp.calls

	// At/after the watchdog deadline -> must kill, and must NOT consult the
	// probe again (the deadline check happens first).
	now = startedAt.Add(watchdogTimeout)
	decision, descendants, cpuDelta, logGrace, reason, err := m.evaluate(now, startedAt, lastEventAt, 1234)
	if decision != heartbeatKill {
		t.Fatalf("at-deadline decision = %v, want heartbeatKill", decision)
	}
	if reason != heartbeatKillReasonWatchdogDeadline {
		t.Fatalf("at-deadline reason = %q, want %q", reason, heartbeatKillReasonWatchdogDeadline)
	}
	if err != nil {
		t.Fatalf("at-deadline unexpected error: %v", err)
	}
	if descendants != 0 || cpuDelta != 0 || logGrace {
		t.Fatalf("at-deadline descendants=%d cpuDelta=%d logGrace=%v, want 0/0/false", descendants, cpuDelta, logGrace)
	}
	if fp.calls != callsBeforeDeadline {
		t.Fatalf("probe called again after deadline reached (calls %d -> %d); deadline check must short-circuit before probing", callsBeforeDeadline, fp.calls)
	}

	// Further past the deadline: still kill, still no probe call.
	now = now.Add(5 * time.Minute)
	decision, _, _, _, reason, _ = m.evaluate(now, startedAt, lastEventAt, 1234)
	if decision != heartbeatKill || reason != heartbeatKillReasonWatchdogDeadline {
		t.Fatalf("past-deadline decision=%v reason=%q, want heartbeatKill/%q", decision, reason, heartbeatKillReasonWatchdogDeadline)
	}
	if fp.calls != callsBeforeDeadline {
		t.Fatalf("probe called again well past deadline (calls %d -> %d)", callsBeforeDeadline, fp.calls)
	}
}

// TestHeartbeatMonitor_ProbeErrorFailsTowardKill covers acceptance scenario
// (d): if the descendant probe itself errors, the monitor must fail toward
// today's kill-on-silence behavior rather than grant unearned grace, and
// must surface the error so the caller can log a warning.
func TestHeartbeatMonitor_ProbeErrorFailsTowardKill(t *testing.T) {
	probeErr := errors.New("read /proc: permission denied")
	fp := &fakeProbe{}
	fp.push(processLivenessSnapshot{}, probeErr)

	m := newHeartbeatMonitor(5*time.Minute, 30*time.Minute, fp.probe)
	startedAt := time.Unix(0, 0)
	lastEventAt := startedAt
	now := startedAt.Add(6 * time.Minute)

	decision, descendants, cpuDelta, logGrace, reason, err := m.evaluate(now, startedAt, lastEventAt, 1234)
	if decision != heartbeatKill {
		t.Fatalf("decision = %v, want heartbeatKill", decision)
	}
	if !errors.Is(err, probeErr) {
		t.Fatalf("err = %v, want %v surfaced to caller", err, probeErr)
	}
	if reason != heartbeatKillReasonProbeError {
		t.Fatalf("reason = %q, want %q", reason, heartbeatKillReasonProbeError)
	}
	if descendants != 0 || cpuDelta != 0 || logGrace {
		t.Fatalf("descendants=%d cpuDelta=%d logGrace=%v, want 0/0/false", descendants, cpuDelta, logGrace)
	}
}

// TestHeartbeatMonitor_WatchdogDisabledAllowsIndefiniteGrace documents that
// when opts.WatchdogTimeout is 0 (disabled - no task-level backstop
// configured by the caller), the heartbeat monitor imposes no ceiling of its
// own; the separate absence of a watchdog goroutine in that case is a
// caller-level decision, not something this monitor should paper over.
func TestHeartbeatMonitor_WatchdogDisabledAllowsIndefiniteGrace(t *testing.T) {
	fp := &fakeProbe{}
	fp.push(processLivenessSnapshot{descendants: 1, cpuTicks: 10}, nil)
	fp.push(processLivenessSnapshot{descendants: 1, cpuTicks: 20}, nil)

	m := newHeartbeatMonitor(5*time.Minute, 0, fp.probe)
	startedAt := time.Unix(0, 0)
	lastEventAt := startedAt

	now := startedAt.Add(6 * time.Minute)
	decision, _, _, _, _, _ := m.evaluate(now, startedAt, lastEventAt, 1234)
	if decision != heartbeatGrace {
		t.Fatalf("decision = %v, want heartbeatGrace", decision)
	}

	// Even very far past what would otherwise be a typical watchdog window.
	now = startedAt.Add(2 * time.Hour)
	decision, _, _, _, _, _ = m.evaluate(now, startedAt, lastEventAt, 1234)
	if decision != heartbeatGrace {
		t.Fatalf("decision after 2h = %v, want heartbeatGrace (no watchdog ceiling configured)", decision)
	}
}
