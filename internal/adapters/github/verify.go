package github

import (
	"context"

	"github.com/qf-studio/pilot/internal/health/verify"
)

// Verifier adapts *Client into verify.Verifiable by pinning the token
// source resolved at construction time. Client.Verify takes tokenSource as
// an argument (for diagnostic error messages, GH-3718) but the Verifiable
// interface takes none, so this wrapper closes over it.
type Verifier struct {
	client      *Client
	tokenSource string
}

// NewVerifier wraps client as a verify.Verifiable. tokenSource (e.g.
// "config", "env GITHUB_TOKEN", "gh CLI") is included in any Verify error
// so a dead token can be diagnosed without re-deriving the resolution
// chain. Pass "" when the source is unknown.
func NewVerifier(client *Client, tokenSource string) *Verifier {
	return &Verifier{client: client, tokenSource: tokenSource}
}

// Compile-time check: *Verifier implements verify.Verifiable.
var _ verify.Verifiable = (*Verifier)(nil)

// Name returns the adapter identifier used by doctor and /ready renderers.
func (v *Verifier) Name() string { return "github" }

// Verify delegates to the wrapped client, supplying the pinned token source.
func (v *Verifier) Verify(ctx context.Context) error {
	return v.client.Verify(ctx, v.tokenSource)
}
