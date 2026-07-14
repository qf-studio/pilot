// Package adapterhealth tracks the health of long-lived adapter goroutines
// (poller listen loops, chat gateway listeners) and gives each one panic
// recovery with bounded restart-with-backoff (GH-4314). A panic in one
// adapter must never crash the daemon and take down every other adapter
// with it.
//
// Scope: this package is for adapter I/O goroutines only. Core
// executor/dispatcher panics indicate real corruption and must NOT be
// routed through Registry.Go — they should keep crashing loudly.
package adapterhealth

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

// MaxRestarts bounds how many times a panicking adapter goroutine is
// restarted before it's marked Disabled and left stopped, so an adapter
// that panics in a tight loop can't spin forever.
const MaxRestarts = 5

// baseBackoff/maxBackoff are vars (not consts) so tests can shrink them to
// keep the MaxRestarts-exhaustion path fast; production code never mutates them.
var (
	baseBackoff = time.Second
	maxBackoff  = time.Minute
)

// Status is a point-in-time health snapshot for one adapter goroutine.
type Status struct {
	Name         string
	Healthy      bool
	Disabled     bool
	LastError    string
	LastPanicAt  time.Time
	RestartCount int
}

// OnPanic is invoked with the adapter name and a human-readable message each
// time a panic is recovered, so the caller can wire it to alert dispatch.
type OnPanic func(name, message string)

// Registry tracks the health of every adapter goroutine started via Go.
// Safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	statuses map[string]*Status
}

// NewRegistry creates an empty adapter health registry.
func NewRegistry() *Registry {
	return &Registry{statuses: make(map[string]*Status)}
}

// Snapshot returns a stable-ordered copy of every tracked adapter's status.
func (r *Registry) Snapshot() []Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Status, 0, len(r.statuses))
	for _, st := range r.statuses {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) register(name string) *Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.statuses[name]
	if !ok {
		st = &Status{Name: name, Healthy: true}
		r.statuses[name] = st
	}
	return st
}

// Go runs fn in a goroutine, recovering any panic so it can never crash the
// process. A panicking adapter is restarted with exponential backoff (1s,
// capped at 1m) up to MaxRestarts times; once exhausted the adapter is
// marked Disabled and Go stops restarting it. onPanic (may be nil) is
// called on every recovered panic so the caller can raise an alert.
func (r *Registry) Go(ctx context.Context, name string, onPanic OnPanic, fn func()) {
	st := r.register(name)
	logging.SafeGo(name, func() {
		r.runWithRestart(ctx, name, st, onPanic, fn)
	})
}

func (r *Registry) runWithRestart(ctx context.Context, name string, st *Status, onPanic OnPanic, fn func()) {
	backoff := baseBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		if !r.runOnce(name, st, fn) {
			// Clean return (e.g. ctx cancelled during shutdown) — no restart needed.
			return
		}

		r.mu.Lock()
		st.RestartCount++
		count := st.RestartCount
		r.mu.Unlock()

		if count >= MaxRestarts {
			r.mu.Lock()
			st.Disabled = true
			st.Healthy = false
			r.mu.Unlock()
			msg := fmt.Sprintf("adapter %q disabled after %d panics — giving up restarts", name, count)
			logging.WithComponent(name).Error(msg, slog.Int("restart_count", count))
			if onPanic != nil {
				onPanic(name, msg)
			}
			return
		}

		msg := fmt.Sprintf("adapter %q panicked and will be restarted (attempt %d/%d)", name, count, MaxRestarts)
		logging.WithComponent(name).Warn(msg, slog.Int("restart_count", count), slog.Duration("backoff", backoff))
		if onPanic != nil {
			onPanic(name, msg)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// runOnce runs fn once, recovering a panic if it occurs. It reports whether
// fn panicked (true) so the caller can decide whether to restart, or
// returned cleanly (false).
func (r *Registry) runOnce(name string, st *Status, fn func()) (panicked bool) {
	defer func() {
		if rec := recover(); rec != nil {
			stack := debug.Stack()
			logging.WithComponent(name).Error("panic recovered in adapter goroutine",
				slog.Any("panic", rec),
				slog.String("stack", string(stack)),
			)
			r.mu.Lock()
			st.Healthy = false
			st.LastError = fmt.Sprintf("%v", rec)
			st.LastPanicAt = time.Now()
			r.mu.Unlock()
			panicked = true
		}
	}()
	fn()
	r.mu.Lock()
	st.Healthy = true
	r.mu.Unlock()
	return false
}
