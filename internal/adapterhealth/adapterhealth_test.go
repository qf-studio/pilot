package adapterhealth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor polls cond until it's true or the timeout elapses, failing the test otherwise.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func TestRegistry_Go_CleanReturnDoesNotRestart(t *testing.T) {
	r := NewRegistry()
	var calls atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	r.Go(ctx, "clean-adapter", nil, func() {
		calls.Add(1)
		close(done)
	})

	<-done
	waitFor(t, time.Second, func() bool { return calls.Load() == 1 })
	time.Sleep(20 * time.Millisecond) // ensure no spurious restart follows

	if got := calls.Load(); got != 1 {
		t.Fatalf("expected fn called exactly once for a clean return, got %d", got)
	}

	snap := r.Snapshot()
	if len(snap) != 1 || !snap[0].Healthy || snap[0].RestartCount != 0 {
		t.Fatalf("unexpected status after clean return: %+v", snap)
	}
}

func TestRegistry_Go_RecoversPanicAndRestarts(t *testing.T) {
	orig := baseBackoff
	baseBackoff = time.Millisecond
	defer func() { baseBackoff = orig }()

	r := NewRegistry()
	var calls atomic.Int32
	var panicMsgs []string
	var mu sync.Mutex

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	onPanic := func(name, msg string) {
		mu.Lock()
		panicMsgs = append(panicMsgs, msg)
		mu.Unlock()
	}

	r.Go(ctx, "flaky-adapter", onPanic, func() {
		n := calls.Add(1)
		if n <= 2 {
			panic("boom")
		}
		// Third call succeeds cleanly — stop the goroutine.
	})

	waitFor(t, time.Second, func() bool { return calls.Load() == 3 })

	mu.Lock()
	gotMsgs := len(panicMsgs)
	mu.Unlock()
	if gotMsgs != 2 {
		t.Fatalf("expected onPanic called twice, got %d (%v)", gotMsgs, panicMsgs)
	}

	waitFor(t, time.Second, func() bool {
		snap := r.Snapshot()
		return len(snap) == 1 && snap[0].Healthy && snap[0].RestartCount == 2 && !snap[0].Disabled
	})
}

func TestRegistry_Go_DisablesAfterMaxRestarts(t *testing.T) {
	origBase, origMax := baseBackoff, maxBackoff
	baseBackoff = time.Millisecond
	maxBackoff = time.Millisecond
	defer func() { baseBackoff, maxBackoff = origBase, origMax }()

	r := NewRegistry()
	var calls atomic.Int32
	var panicCount atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.Go(ctx, "always-panics", func(name, msg string) {
		panicCount.Add(1)
	}, func() {
		calls.Add(1)
		panic("always boom")
	})

	// Registry.Go sets st.Disabled=true and then invokes onPanic (adapterhealth.go
	// runWithRestart) — the two aren't atomic w.r.t. a concurrent Snapshot(), so
	// waiting on Disabled alone can observe it before the final onPanic call lands.
	// Wait on all three so the exact-count assertions below are deterministic.
	waitFor(t, 2*time.Second, func() bool {
		snap := r.Snapshot()
		return len(snap) == 1 && snap[0].Disabled &&
			calls.Load() == MaxRestarts && panicCount.Load() == MaxRestarts
	})

	if got := calls.Load(); got != MaxRestarts {
		t.Fatalf("expected fn called exactly MaxRestarts=%d times, got %d", MaxRestarts, got)
	}
	if got := panicCount.Load(); got != MaxRestarts {
		t.Fatalf("expected onPanic called MaxRestarts=%d times, got %d", MaxRestarts, got)
	}

	snap := r.Snapshot()
	st := snap[0]
	if st.Healthy {
		t.Error("expected Healthy=false once disabled")
	}
	if !st.Disabled {
		t.Error("expected Disabled=true")
	}
	if st.LastError == "" {
		t.Error("expected LastError to be recorded")
	}
	if st.LastPanicAt.IsZero() {
		t.Error("expected LastPanicAt to be recorded")
	}

	// No further calls after disabling, even if we wait past another backoff window.
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != MaxRestarts {
		t.Fatalf("expected no further restarts after disabling, calls=%d", got)
	}
}

func TestRegistry_Go_StopsRestartingWhenContextCancelled(t *testing.T) {
	orig := baseBackoff
	baseBackoff = 200 * time.Millisecond
	defer func() { baseBackoff = orig }()

	r := NewRegistry()
	var calls atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())

	r.Go(ctx, "cancel-me", nil, func() {
		calls.Add(1)
		panic("boom")
	})

	waitFor(t, time.Second, func() bool { return calls.Load() == 1 })
	cancel() // cancel while the goroutine is sleeping in backoff

	time.Sleep(400 * time.Millisecond) // well past the backoff window
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected no restart after context cancellation, calls=%d", got)
	}
}

func TestRegistry_Snapshot_SortedAndIsolated(t *testing.T) {
	r := NewRegistry()
	r.register("zebra")
	r.register("alpha")
	r.register("mango")

	snap := r.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("expected 3 statuses, got %d", len(snap))
	}
	names := []string{snap[0].Name, snap[1].Name, snap[2].Name}
	want := []string{"alpha", "mango", "zebra"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("expected sorted names %v, got %v", want, names)
		}
	}

	// Mutating the returned snapshot must not affect the registry's internal state.
	snap[0].Healthy = false
	if got := r.Snapshot()[0].Healthy; !got {
		t.Fatal("Snapshot should return copies, not references into internal state")
	}
}

func TestRegistry_Go_NilOnPanicIsSafe(t *testing.T) {
	orig := baseBackoff
	baseBackoff = time.Millisecond
	defer func() { baseBackoff = orig }()

	r := NewRegistry()
	var calls atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.Go(ctx, "no-callback", nil, func() {
		n := calls.Add(1)
		if n == 1 {
			panic("boom")
		}
	})

	waitFor(t, time.Second, func() bool { return calls.Load() == 2 })
}
