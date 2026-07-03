// Package verify_test is an external (black-box) test package so it can
// import gateway to assert ReadinessChecker satisfaction without creating an
// import cycle: gateway -> adapters/linear -> verify.
package verify_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/gateway"
	"github.com/qf-studio/pilot/internal/health/verify"
)

// Compile-time check: *ReadinessAdapter must satisfy gateway.ReadinessChecker.
var _ gateway.ReadinessChecker = (*verify.ReadinessAdapter)(nil)

// fakeVerifiable is a test double for Verifiable. If delay > 0, Verify
// blocks until delay elapses or ctx is done, whichever comes first —
// returning ctx.Err() in the latter case, exercising timeout mapping.
type fakeVerifiable struct {
	name  string
	err   error
	delay time.Duration
}

func (f *fakeVerifiable) Name() string { return f.name }

func (f *fakeVerifiable) Verify(ctx context.Context) error {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.err
}

func TestReadinessAdapter_Name(t *testing.T) {
	v := &fakeVerifiable{name: "db"}
	a := verify.NewReadinessAdapter(v, time.Second)
	if got := a.Name(); got != "db" {
		t.Errorf("Name() = %q, want %q", got, "db")
	}
}

func TestReadinessAdapter_Ready_OK(t *testing.T) {
	v := &fakeVerifiable{name: "ok-check", err: nil}
	a := verify.NewReadinessAdapter(v, 50*time.Millisecond)
	if !a.Ready() {
		t.Error("Ready() = false, want true when Verify returns nil")
	}
}

func TestReadinessAdapter_Ready_Error(t *testing.T) {
	v := &fakeVerifiable{name: "err-check", err: errors.New("dependency down")}
	a := verify.NewReadinessAdapter(v, 50*time.Millisecond)
	if a.Ready() {
		t.Error("Ready() = true, want false when Verify returns an error")
	}
}

func TestReadinessAdapter_Ready_Timeout(t *testing.T) {
	v := &fakeVerifiable{name: "slow-check", delay: 200 * time.Millisecond}
	a := verify.NewReadinessAdapter(v, 20*time.Millisecond)

	start := time.Now()
	ready := a.Ready()
	elapsed := time.Since(start)

	if ready {
		t.Error("Ready() = true, want false when Verify exceeds the bounded timeout")
	}
	// Ready() must return once the bounded timeout fires, not wait for the
	// full delay — proves the timeout is actually enforced via context.
	if elapsed >= v.delay {
		t.Errorf("Ready() took %v, want well under the %v Verify delay (timeout not enforced)", elapsed, v.delay)
	}
}

func TestNewReadinessAdapter_NonPositiveTimeoutUsesDefault(t *testing.T) {
	v := &fakeVerifiable{name: "default-timeout-check"}
	for _, timeout := range []time.Duration{0, -time.Second} {
		a := verify.NewReadinessAdapter(v, timeout)
		if a.Timeout() != verify.DefaultTimeout {
			t.Errorf("NewReadinessAdapter(%v) timeout = %v, want DefaultTimeout (%v)", timeout, a.Timeout(), verify.DefaultTimeout)
		}
	}
}
