package jira

import (
	"context"

	"github.com/qf-studio/pilot/internal/health/verify"
)

// Compile-time check: *Client implements verify.Verifiable.
var _ verify.Verifiable = (*Client)(nil)

// Name returns the adapter identifier used by doctor and /ready renderers.
func (c *Client) Name() string { return AdapterName }

// Verify has no live probe wired up yet — see verify.ErrProbeNotImplemented.
func (c *Client) Verify(ctx context.Context) error {
	return verify.ErrProbeNotImplemented
}
