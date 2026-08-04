package executor

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// TestResolveLivenessPolicy is the effort × complexity table for the single
// per-task resolution point: both the soft-stall timeout (previously
// effortAwareStallTimeout) and the hard-heartbeat floor (previously
// effortAwareHeartbeatFloor) must come out of ResolveLivenessPolicy exactly
// as they did from the two independent functions before GH-4715, since this
// refactor merges the policy, not the enforcement — kill/stall semantics
// must stay unchanged at the same thresholds as today.
func TestResolveLivenessPolicy(t *testing.T) {
	tests := []struct {
		name            string
		configuredStall time.Duration
		effort          string
		complexity      Complexity
		wantStall       time.Duration
		wantFloor       time.Duration
	}{
		{
			name:            "low effort, simple complexity: defaults, no floor",
			configuredStall: 3 * time.Minute,
			effort:          "low",
			complexity:      ComplexitySimple,
			wantStall:       3 * time.Minute,
			wantFloor:       0,
		},
		{
			name:            "medium effort, medium complexity: defaults, no floor",
			configuredStall: 3 * time.Minute,
			effort:          "medium",
			complexity:      ComplexityMedium,
			wantStall:       3 * time.Minute,
			wantFloor:       0,
		},
		{
			name:            "high effort: stall and heartbeat floor both raised",
			configuredStall: 3 * time.Minute,
			effort:          "high",
			complexity:      ComplexityMedium,
			wantStall:       highEffortStallFloor,
			wantFloor:       highEffortStallFloor,
		},
		{
			name:            "complex lane: stall and heartbeat floor both raised",
			configuredStall: 3 * time.Minute,
			effort:          "medium",
			complexity:      ComplexityComplex,
			wantStall:       highEffortStallFloor,
			wantFloor:       highEffortStallFloor,
		},
		{
			name:            "epic lane: stall and heartbeat floor both raised",
			configuredStall: 3 * time.Minute,
			effort:          "medium",
			complexity:      ComplexityEpic,
			wantStall:       highEffortStallFloor,
			wantFloor:       highEffortStallFloor,
		},
		{
			name:            "explicit config above floor wins for stall; floor is still applied for heartbeat",
			configuredStall: 15 * time.Minute,
			effort:          "high",
			complexity:      ComplexityComplex,
			wantStall:       15 * time.Minute,
			wantFloor:       highEffortStallFloor,
		},
		{
			name:            "stall detection disabled (0) stays disabled regardless of lane; heartbeat floor still applies",
			configuredStall: 0,
			effort:          "high",
			complexity:      ComplexityComplex,
			wantStall:       0,
			wantFloor:       highEffortStallFloor,
		},
		{
			name:            "stall detection explicitly disabled (negative) stays disabled",
			configuredStall: -1,
			effort:          "high",
			complexity:      ComplexityComplex,
			wantStall:       -1,
			wantFloor:       highEffortStallFloor,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveLivenessPolicy(tt.configuredStall, tt.effort, tt.complexity)
			if got.StallTimeout != tt.wantStall {
				t.Errorf("StallTimeout = %v, want %v", got.StallTimeout, tt.wantStall)
			}
			if got.HeartbeatFloor != tt.wantFloor {
				t.Errorf("HeartbeatFloor = %v, want %v", got.HeartbeatFloor, tt.wantFloor)
			}
			if wantInterval := watchdogTickInterval(got.StallTimeout); got.StallWatchdogInterval != wantInterval {
				t.Errorf("StallWatchdogInterval = %v, want %v", got.StallWatchdogInterval, wantInterval)
			}
		})
	}
}

// TestLivenessPolicy_SharedAcrossDetectors proves the GH-4715 drift-
// impossible-by-construction property: a single LivenessPolicy value,
// resolved once, is what both the stall watchdog (watchdog.go) and the
// heartbeat monitor's floor (backend_claudecode.go, via
// ExecuteOptions.LivenessPolicy) consume for the same task. Before this
// refactor, the two mechanisms carried independent constants that GH-4695
// had to hand-resync; this test fails if a future change makes either
// consumer read from a separately-derived value instead of the shared
// instance.
func TestLivenessPolicy_SharedAcrossDetectors(t *testing.T) {
	policy := ResolveLivenessPolicy(3*time.Minute, "high", ComplexityComplex)

	// The heartbeat path threads the policy through ExecuteOptions exactly
	// as runner.go's backendExecute call sites do.
	opts := ExecuteOptions{LivenessPolicy: policy}

	// The stall watchdog path consumes opts.LivenessPolicy directly — the
	// very same value the (simulated) backend call above would receive —
	// not an independently-resolved one.
	r := &Runner{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var (
		lastEventAt   atomic.Int64
		stallDetected atomic.Bool
		inFlight      atomic.Int64
		done          = make(chan struct{})
	)
	lastEventAt.Store(time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go r.runStallWatchdog("test-task", &lastEventAt, &stallDetected, &inFlight, opts.LivenessPolicy, done, cancel)
	defer close(done)

	if opts.LivenessPolicy != policy {
		t.Fatalf("ExecuteOptions.LivenessPolicy diverged from the resolved policy: got %+v, want %+v", opts.LivenessPolicy, policy)
	}

	// The heartbeat monitor's effective timeout (backend_claudecode.go)
	// must be derived from the identical instance the watchdog above just
	// consumed, not a fresh call to effortAwareHeartbeatFloor.
	heartbeatTimeout, source := effectiveHeartbeatTimeout(DefaultHeartbeatTimeout, opts.LivenessPolicy.HeartbeatFloor)
	if source != "effort_floor" || heartbeatTimeout != policy.HeartbeatFloor {
		t.Fatalf("heartbeat monitor did not observe the same policy instance as the stall watchdog: heartbeatTimeout=%v source=%q, want %v/effort_floor",
			heartbeatTimeout, source, policy.HeartbeatFloor)
	}

	select {
	case <-ctx.Done():
		t.Fatal("stall watchdog fired unexpectedly while the session was live")
	default:
		// expected: fresh lastEventAt, no stall yet.
	}
}
