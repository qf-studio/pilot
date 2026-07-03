// Package verify defines a stable, context-aware health-check contract and
// a small adapter that bridges it to the gateway's boolean ReadinessChecker
// interface, so follow-up checks can be written once against Verifiable and
// registered anywhere a gateway.ReadinessChecker is expected.
package verify

import (
	"context"
	"time"
)

// DefaultTimeout bounds a Verify call when NewReadinessAdapter is given a
// non-positive timeout.
const DefaultTimeout = 5 * time.Second

// Verifiable is implemented by components that can self-check their health
// via a context-bound call, allowing callers to bound the check's duration
// and cancel it.
type Verifiable interface {
	// Name returns a unique identifier for this check.
	Name() string
	// Verify returns nil if the component is healthy, or an error describing
	// why it isn't. Implementations should respect ctx cancellation/deadline.
	Verify(ctx context.Context) error
}

// ReadinessAdapter adapts a Verifiable into the gateway's ReadinessChecker
// shape (Name() string; Ready() bool) by calling Verify with a bounded
// timeout and mapping err == nil to Ready() == true.
type ReadinessAdapter struct {
	v       Verifiable
	timeout time.Duration
}

// NewReadinessAdapter wraps v so it satisfies gateway.ReadinessChecker.
// A non-positive timeout falls back to DefaultTimeout.
func NewReadinessAdapter(v Verifiable, timeout time.Duration) *ReadinessAdapter {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &ReadinessAdapter{v: v, timeout: timeout}
}

// Name returns the wrapped Verifiable's name.
func (a *ReadinessAdapter) Name() string {
	return a.v.Name()
}

// Ready calls Verify with a bounded timeout and reports true only if it
// returns nil before the timeout elapses.
func (a *ReadinessAdapter) Ready() bool {
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()
	return a.v.Verify(ctx) == nil
}
