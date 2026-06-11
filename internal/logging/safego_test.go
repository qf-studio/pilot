package logging

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestSafeGo_RecoversFromPanic(t *testing.T) {
	done := make(chan struct{})
	SafeGo("test-component", func() {
		defer close(done)
		panic("intentional test panic")
	})
	<-done // blocks until the goroutine finishes (after recovery)
}

func TestSafeGo_NormalCompletion(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	SafeGo("test-normal", func() {
		defer wg.Done()
	})
	wg.Wait()
}

type stubCounter struct {
	mu        sync.Mutex
	component string
	count     atomic.Int64
	done      chan struct{}
}

func (s *stubCounter) Inc(component string) {
	s.mu.Lock()
	s.component = component
	s.mu.Unlock()
	s.count.Add(1)
	close(s.done)
}

func TestSafeGo_IncrementsCounter(t *testing.T) {
	prev := panicCounter
	ctr := &stubCounter{done: make(chan struct{})}
	SetPanicCounter(ctr)
	t.Cleanup(func() { SetPanicCounter(prev) })

	SafeGo("counter-test", func() {
		panic("trigger counter")
	})
	<-ctr.done // blocks until Inc() fires after recovery

	if ctr.count.Load() != 1 {
		t.Fatalf("expected counter increment 1, got %d", ctr.count.Load())
	}
	ctr.mu.Lock()
	comp := ctr.component
	ctr.mu.Unlock()
	if comp != "counter-test" {
		t.Fatalf("expected component %q, got %q", "counter-test", comp)
	}
}
