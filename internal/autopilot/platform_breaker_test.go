package autopilot

import (
	"log/slog"
	"testing"
	"time"
)

// fixedClock returns a now func that advances by delta on every call after
// the first, letting tests control PlatformBreaker's internal now() without
// a real sleep.
type fixedClock struct {
	t time.Time
}

func (c *fixedClock) now() time.Time {
	return c.t
}

func (c *fixedClock) advance(d time.Duration) {
	c.t = c.t.Add(d)
}

func newTestPlatformBreaker(minDistinctPRs int, correlationWindow, quietPeriod time.Duration) (*PlatformBreaker, *fixedClock) {
	b := NewPlatformBreaker(minDistinctPRs, correlationWindow, quietPeriod, slog.Default())
	clock := &fixedClock{t: time.Now()}
	b.now = clock.now
	return b, clock
}

// TestPlatformBreaker_OpensOnCorrelatedDistinctPRs verifies the core
// acceptance criterion: N distinct PRs observing infra-or-unknown-class
// failures inside the correlation window opens the breaker.
func TestPlatformBreaker_OpensOnCorrelatedDistinctPRs(t *testing.T) {
	b, _ := newTestPlatformBreaker(3, 15*time.Minute, 20*time.Minute)

	r1 := b.Observe(1, "owner/repo", FailureClassInfra)
	if r1.Open || r1.JustOpened {
		t.Fatalf("after 1 distinct PR: Open=%v JustOpened=%v, want both false", r1.Open, r1.JustOpened)
	}

	r2 := b.Observe(2, "owner/repo", FailureClassUnknown)
	if r2.Open || r2.JustOpened {
		t.Fatalf("after 2 distinct PRs: Open=%v JustOpened=%v, want both false", r2.Open, r2.JustOpened)
	}

	r3 := b.Observe(3, "owner/repo", FailureClassInfra)
	if !r3.Open || !r3.JustOpened {
		t.Fatalf("after 3 distinct PRs: Open=%v JustOpened=%v, want both true", r3.Open, r3.JustOpened)
	}
	wantPRs := []string{"owner/repo#1", "owner/repo#2", "owner/repo#3"}
	if !equalStringSlices(r3.CorrelatedPRs, wantPRs) {
		t.Errorf("CorrelatedPRs = %v, want %v", r3.CorrelatedPRs, wantPRs)
	}

	if !b.IsOpen() {
		t.Error("IsOpen() = false after breaker opened, want true")
	}
}

// TestPlatformBreaker_DoesNotOpenOnSinglePRRepeatedFailures verifies the
// breaker does NOT open on repeated failures of the SAME PR — that is the
// per-PR circuit breaker's job, not this one's.
func TestPlatformBreaker_DoesNotOpenOnSinglePRRepeatedFailures(t *testing.T) {
	b, _ := newTestPlatformBreaker(3, 15*time.Minute, 20*time.Minute)

	for i := 0; i < 5; i++ {
		r := b.Observe(42, "owner/repo", FailureClassInfra)
		if r.Open || r.JustOpened {
			t.Fatalf("observation %d of same PR: Open=%v JustOpened=%v, want both false", i, r.Open, r.JustOpened)
		}
	}
	if b.IsOpen() {
		t.Error("IsOpen() = true after repeated single-PR failures, want false")
	}
}

// TestPlatformBreaker_CodeClassifiedFailuresDoNotCorrelate verifies that
// FailureClassCode observations never feed the correlation signal, even
// across many distinct PRs.
func TestPlatformBreaker_CodeClassifiedFailuresDoNotCorrelate(t *testing.T) {
	b, _ := newTestPlatformBreaker(3, 15*time.Minute, 20*time.Minute)

	for pr := 1; pr <= 5; pr++ {
		r := b.Observe(pr, "owner/repo", FailureClassCode)
		if r.Open || r.JustOpened {
			t.Fatalf("pr %d code-classified: Open=%v JustOpened=%v, want both false", pr, r.Open, r.JustOpened)
		}
	}
	if b.IsOpen() {
		t.Error("IsOpen() = true after only code-classified failures, want false")
	}
}

// TestPlatformBreaker_ObservationsOutsideWindowDoNotCorrelate verifies that
// observations older than correlationWindow are pruned and do not count
// toward the distinct-PR threshold.
func TestPlatformBreaker_ObservationsOutsideWindowDoNotCorrelate(t *testing.T) {
	b, clock := newTestPlatformBreaker(3, 15*time.Minute, 20*time.Minute)

	b.Observe(1, "owner/repo", FailureClassInfra)
	clock.advance(20 * time.Minute) // outside the 15m correlation window
	b.Observe(2, "owner/repo", FailureClassInfra)
	r := b.Observe(3, "owner/repo", FailureClassInfra)

	if r.Open || r.JustOpened {
		t.Fatalf("after window-expired first observation: Open=%v JustOpened=%v, want both false", r.Open, r.JustOpened)
	}
}

// TestPlatformBreaker_ClosesAfterQuietPeriod verifies simple time-based
// recovery: once open, the breaker closes after quietPeriod elapses with no
// new infra/unknown-class observation, reported via JustClosed on the next
// Observe call that crosses the deadline.
func TestPlatformBreaker_ClosesAfterQuietPeriod(t *testing.T) {
	b, clock := newTestPlatformBreaker(3, 15*time.Minute, 20*time.Minute)

	b.Observe(1, "owner/repo", FailureClassInfra)
	b.Observe(2, "owner/repo", FailureClassInfra)
	opened := b.Observe(3, "owner/repo", FailureClassInfra)
	if !opened.Open {
		t.Fatal("breaker did not open as precondition for close test")
	}

	clock.advance(20 * time.Minute)

	// A code-classified observation is not itself correlation evidence, but
	// it still triggers the lazy time-based close check.
	closeResult := b.Observe(4, "owner/repo", FailureClassCode)
	if !closeResult.JustClosed {
		t.Fatalf("JustClosed = false after quiet period elapsed, want true (Open=%v)", closeResult.Open)
	}
	if closeResult.Open {
		t.Error("Open = true on the same call that reports JustClosed, want false")
	}
	wantPRs := []string{"owner/repo#1", "owner/repo#2", "owner/repo#3"}
	if !equalStringSlices(closeResult.CorrelatedPRs, wantPRs) {
		t.Errorf("CorrelatedPRs on close = %v, want %v", closeResult.CorrelatedPRs, wantPRs)
	}

	if b.IsOpen() {
		t.Error("IsOpen() = true after quiet-period close, want false")
	}
}

// TestPlatformBreaker_QuietPeriodResetsOnNewInfraFailure verifies that a new
// infra/unknown observation while open resets the quiet-period clock — the
// breaker must not close early just because quietPeriod has elapsed since
// the FIRST observation if a more recent one landed since.
func TestPlatformBreaker_QuietPeriodResetsOnNewInfraFailure(t *testing.T) {
	b, clock := newTestPlatformBreaker(3, 15*time.Minute, 20*time.Minute)

	b.Observe(1, "owner/repo", FailureClassInfra)
	b.Observe(2, "owner/repo", FailureClassInfra)
	b.Observe(3, "owner/repo", FailureClassInfra)

	clock.advance(15 * time.Minute) // < quietPeriod
	stillOpen := b.Observe(4, "owner/repo", FailureClassInfra)
	if !stillOpen.Open || stillOpen.JustClosed {
		t.Fatalf("mid-episode new infra failure: Open=%v JustClosed=%v, want Open=true JustClosed=false", stillOpen.Open, stillOpen.JustClosed)
	}

	clock.advance(15 * time.Minute) // < quietPeriod since the reset at t+15m
	stillOpen2 := b.Observe(5, "owner/repo", FailureClassInfra)
	if !stillOpen2.Open || stillOpen2.JustClosed {
		t.Fatalf("breaker closed early despite quiet-period reset: Open=%v JustClosed=%v", stillOpen2.Open, stillOpen2.JustClosed)
	}
}

// TestPlatformBreaker_DistinctRepos verifies correlation spans repos, not
// just PRs within one repo — a single PlatformBreaker is shared across every
// repo's controller.
func TestPlatformBreaker_DistinctRepos(t *testing.T) {
	b, _ := newTestPlatformBreaker(3, 15*time.Minute, 20*time.Minute)

	b.Observe(1, "owner/repo-a", FailureClassInfra)
	b.Observe(1, "owner/repo-b", FailureClassInfra) // same PR number, different repo
	r := b.Observe(1, "owner/repo-c", FailureClassInfra)

	if !r.Open || !r.JustOpened {
		t.Fatalf("cross-repo correlation: Open=%v JustOpened=%v, want both true", r.Open, r.JustOpened)
	}
}

// TestPlatformBreaker_NilReceiverIsNoOp verifies the disabled-by-config path
// is a byte-identical no-op: a nil *PlatformBreaker always reports closed
// with no transition, regardless of how many observations are fed in.
func TestPlatformBreaker_NilReceiverIsNoOp(t *testing.T) {
	var b *PlatformBreaker

	for pr := 1; pr <= 10; pr++ {
		r := b.Observe(pr, "owner/repo", FailureClassInfra)
		if r.Open || r.JustOpened || r.JustClosed || r.CorrelatedPRs != nil {
			t.Fatalf("nil breaker Observe(%d) = %+v, want zero value", pr, r)
		}
	}
	if b.IsOpen() {
		t.Error("nil breaker IsOpen() = true, want false")
	}
}

// TestPlatformBreaker_DefaultsApplied verifies zero-value constructor args
// fall back to the documented defaults rather than leaving the breaker
// permanently open or permanently closed.
func TestPlatformBreaker_DefaultsApplied(t *testing.T) {
	b := NewPlatformBreaker(0, 0, 0, nil)
	if b.minDistinctPRs != DefaultPlatformBreakerMinCorrelatedPRs {
		t.Errorf("minDistinctPRs = %d, want default %d", b.minDistinctPRs, DefaultPlatformBreakerMinCorrelatedPRs)
	}
	if b.correlationWindow != DefaultPlatformBreakerCorrelationWindow {
		t.Errorf("correlationWindow = %v, want default %v", b.correlationWindow, DefaultPlatformBreakerCorrelationWindow)
	}
	if b.quietPeriod != DefaultPlatformBreakerQuietPeriod {
		t.Errorf("quietPeriod = %v, want default %v", b.quietPeriod, DefaultPlatformBreakerQuietPeriod)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
