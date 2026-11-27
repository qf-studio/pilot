package plane

import (
	"context"
	"fmt"
	"net/http"

	planeSDK "github.com/qf-studio/studio-sdk/sdk/integrations/plane"

	"github.com/qf-studio/pilot/internal/health/verify"
)

// Verifier checks the Plane key, the workspace slug and the project ID of a
// Config with one live request (GH-6735).
type Verifier struct {
	cfg        *Config
	httpClient *http.Client
}

// VerifierOption configures a Verifier.
type VerifierOption func(*Verifier)

// WithVerifierHTTPClient sets the HTTP client the SDK client uses, so a test
// can point the check at a test server or a failing transport.
func WithVerifierHTTPClient(hc *http.Client) VerifierOption {
	return func(v *Verifier) {
		v.httpClient = hc
	}
}

// NewVerifier returns a Verifier for cfg.
func NewVerifier(cfg *Config, opts ...VerifierOption) *Verifier {
	v := &Verifier{cfg: cfg}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// Compile-time check: *Verifier implements verify.Verifiable.
var _ verify.Verifiable = (*Verifier)(nil)

// Name returns the adapter identifier used by doctor and /ready renderers.
func (v *Verifier) Name() string { return "plane" }

// Verify lists the labels of the first configured project. The SDK client has
// no ping method; one ListLabels call tests the key, the slug and the project
// ID together, and the poller reads the labels in any case.
//
// Decision: only the first project is checked — the health loop runs this
// every interval, and one call per interval is the limit.
//
// The returned error never holds the key: the SDK masks the key in its own
// errors (since v0.39.3), and the text added here names only the slug and the
// project ID.
func (v *Verifier) Verify(ctx context.Context) error {
	if v.cfg == nil || len(v.cfg.ProjectIDs) == 0 {
		slug := ""
		if v.cfg != nil {
			slug = v.cfg.WorkspaceSlug
		}
		return fmt.Errorf("plane check failed: no project is set (workspace %q)", slug)
	}

	var opts []planeSDK.ClientOption
	if v.httpClient != nil {
		opts = append(opts, planeSDK.WithHTTPClient(v.httpClient))
	}
	client := planeSDK.NewClient(v.cfg.BaseURL, v.cfg.APIKey, opts...)

	projectID := v.cfg.ProjectIDs[0]
	if _, err := client.ListLabels(ctx, v.cfg.WorkspaceSlug, projectID); err != nil {
		return fmt.Errorf("plane check failed: workspace %q, project %q: %w", v.cfg.WorkspaceSlug, projectID, err)
	}
	return nil
}
