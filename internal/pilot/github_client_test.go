package pilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/config"
)

// rotatingGitHubToken is a minimal TokenFunc source for exercising
// per-request token resolution against a webhook-mode Pilot instance,
// mirroring internal/adapters/github/token_func_test.go's
// rotatingTokenSource.
type rotatingGitHubToken struct {
	mu      sync.Mutex
	current string
}

func (r *rotatingGitHubToken) tokenFunc() github.TokenFunc {
	return func(ctx context.Context) (string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.current, nil
	}
}

func (r *rotatingGitHubToken) set(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current = token
}

// newTestPilotConfig returns a minimal, GitHub-enabled config pointed at a
// temp memory dir, suitable for New() in these tests.
func newTestPilotConfig(t *testing.T, token string) *config.Config {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "pilot-github-client-test")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	cfg := config.DefaultConfig()
	cfg.Memory.Path = tempDir
	cfg.Adapters.GitHub.Enabled = true
	cfg.Adapters.GitHub.Token = token
	return cfg
}

// TestNew_WithGitHubClient_ResolvesTokenPerRequest is the GH-4755 regression
// test: webhook-mode Pilot (built via New, exactly as cmd/pilot's
// gateway/webhook mode does) must keep resolving its GitHub token
// per-request through whatever client the caller injects via
// WithGitHubClient, instead of freezing whatever cfg.Adapters.GitHub.Token
// held at construction time. Before this fix, New unconditionally built
// github.NewClient(cfg.Adapters.GitHub.Token) regardless of options,
// discarding any token-func-based client cmd/pilot's newGitHubClient would
// have supplied and locking the daemon to a single static credential for
// its lifetime — so an App-auth token rotation (or a 401) after that point
// would never be picked up.
func TestNew_WithGitHubClient_ResolvesTokenPerRequest(t *testing.T) {
	var gotAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"number":1}`))
	}))
	defer server.Close()

	source := &rotatingGitHubToken{current: "token-v1"}
	client := github.NewClientWithTokenFuncAndBaseURL(source.tokenFunc(), server.URL)

	// The static config token must NOT win over the injected client — if it
	// does, New rebuilt its own client from this value instead of keeping
	// the one passed via WithGitHubClient.
	cfg := newTestPilotConfig(t, "boot-time-static-token")

	p, err := New(cfg, WithGitHubClient(client))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	if p.githubClient != client {
		t.Fatal("New must keep the client supplied via WithGitHubClient instead of building its own from cfg.Adapters.GitHub.Token")
	}

	if _, err := p.githubClient.GetIssue(context.Background(), "owner", "repo", 1); err != nil {
		t.Fatalf("GetIssue (pre-rotation) failed: %v", err)
	}

	// Simulate a mid-daemon-lifetime rotation (GitHub App token refresh, gh-CLI
	// re-auth, ...) — the Pilot instance is never rebuilt, exactly like the
	// live daemon.
	source.set("token-v2")

	if _, err := p.githubClient.GetIssue(context.Background(), "owner", "repo", 1); err != nil {
		t.Fatalf("GetIssue (post-rotation) failed: %v", err)
	}

	if len(gotAuth) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(gotAuth))
	}
	if gotAuth[0] != "Bearer token-v1" {
		t.Errorf("first request Authorization = %q, want %q", gotAuth[0], "Bearer token-v1")
	}
	if gotAuth[1] != "Bearer token-v2" {
		t.Errorf("second request Authorization = %q (stale boot-time token), want %q — webhook-mode Pilot's client did not pick up the rotated token", gotAuth[1], "Bearer token-v2")
	}
}

// TestNew_WithoutGitHubClient_FallsBackToStaticToken guards the fallback
// path for direct New() callers that don't supply WithGitHubClient (e.g.
// other existing tests in this package/cmd/pilot) — New must still build a
// usable client from cfg.Adapters.GitHub.Token so their behavior is
// unchanged by this fix.
func TestNew_WithoutGitHubClient_FallsBackToStaticToken(t *testing.T) {
	cfg := newTestPilotConfig(t, "static-token")

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	if p.githubClient == nil {
		t.Fatal("New must build a fallback GitHub client from cfg.Adapters.GitHub.Token when no WithGitHubClient option is supplied")
	}
}
