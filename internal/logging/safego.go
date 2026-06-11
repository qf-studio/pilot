package logging

import (
	"log/slog"
	"runtime/debug"
	"sync"
)

// PanicCounter is implemented by the metrics layer to count goroutine panics by component.
// The logging package holds no implementation to avoid import cycles — wire via SetPanicCounter.
type PanicCounter interface {
	Inc(component string)
}

var (
	panicCounterMu sync.RWMutex
	panicCounter   PanicCounter // nil until wired; recovery still happens regardless
)

// SetPanicCounter wires a counter into SafeGo. Called once at startup from gateway.
func SetPanicCounter(c PanicCounter) {
	panicCounterMu.Lock()
	panicCounter = c
	panicCounterMu.Unlock()
}

// SafeGo runs fn in a new goroutine. A deferred recover catches any panic, logs it
// at error level with a full stack trace, and increments the pilot_panics_total counter
// (when wired). Recovery is unconditional — the counter is optional.
func SafeGo(component string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				slog.Error("goroutine panic recovered",
					"component", component,
					"panic", r,
					"stack", string(stack))
				panicCounterMu.RLock()
				ctr := panicCounter
				panicCounterMu.RUnlock()
				if ctr != nil {
					ctr.Inc(component)
				}
			}
		}()
		fn()
	}()
}
