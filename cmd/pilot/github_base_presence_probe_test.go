package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

// githubBasePresenceProbe must satisfy executor.BasePresenceProbe at
// compile time — a build-time guard that catches a signature drift between
// this shim and the interface it's registered against (GH-5053).
var _ executor.BasePresenceProbe = githubBasePresenceProbe{}

// TestGithubBasePresenceProbe_LinkedPRNumbers confirms the one method this
// shim adds beyond *github.Client's own FileExistsOnDefaultBranch/
// IssueOrPRState (repo_state.go, GH-5046): LinkedPRNumbers maps
// SearchPRsForIssue's []*github.PullRequest results down to bare numbers
// against a real HTTP round trip, and IssueOrPRState (inherited directly
// via the embedded *github.Client) keeps working through the same wrapper.
func TestGithubBasePresenceProbe_LinkedPRNumbers(t *testing.T) {
	var gotPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/issues"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"items":[{"id":1,"number":42,"title":"fix","state":"open"},{"id":2,"number":43,"title":"fix2","state":"closed","pull_request":{"merged_at":"2026-01-01T00:00:00Z"}}]}`))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/7"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"number":7,"state":"open"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	defer server.Close()

	probe := githubBasePresenceProbe{Client: github.NewClientWithBaseURL("test-token", server.URL)}

	numbers, err := probe.LinkedPRNumbers(context.Background(), "owner", "repo", 7)
	if err != nil {
		t.Fatalf("LinkedPRNumbers: unexpected error: %v", err)
	}
	if len(numbers) != 2 || numbers[0] != 42 || numbers[1] != 43 {
		t.Errorf("LinkedPRNumbers = %v, want [42 43]", numbers)
	}

	kind, state, err := probe.IssueOrPRState(context.Background(), "owner", "repo", 7)
	if err != nil {
		t.Fatalf("IssueOrPRState: unexpected error: %v", err)
	}
	if kind != "issue" || state != "open" {
		t.Errorf("IssueOrPRState = (%q, %q), want (\"issue\", \"open\")", kind, state)
	}

	if len(gotPaths) != 2 {
		t.Fatalf("expected 2 HTTP requests against the test server, got %d: %v", len(gotPaths), gotPaths)
	}
}

// TestNewGitHubBasePresenceProbe_ReturnsGithubBackedProbe confirms
// newGitHubBasePresenceProbe(cfg) — the exact call
// startGithubSDKPollerForRepo wires into RegisterBasePresenceProbe — returns
// a githubBasePresenceProbe backed by a real, non-nil *github.Client (built
// via newGitHubClient, whose token is re-resolved per request rather than
// frozen at construction — GH-4747), not a stub or nil probe.
func TestNewGitHubBasePresenceProbe_ReturnsGithubBackedProbe(t *testing.T) {
	cfg := &config.Config{Adapters: &config.AdaptersConfig{
		GitHub: &github.Config{Enabled: true, Token: "test-token"},
	}}

	probe := newGitHubBasePresenceProbe(cfg)

	shim, ok := probe.(githubBasePresenceProbe)
	if !ok {
		t.Fatalf("newGitHubBasePresenceProbe returned %T, want githubBasePresenceProbe", probe)
	}
	if shim.Client == nil {
		t.Fatal("newGitHubBasePresenceProbe returned a probe with a nil *github.Client")
	}
}

// TestStartGithubSDKPollerForRepo_RegistersBasePresenceProbe is a
// source-level guard (mirrors
// TestStartGithubSDKPollerForRepo_RegistersPerRepoLabelLifecycleTracker's
// established pattern for this otherwise-unexercisable startup function,
// label_lifecycle_deadman_test.go): startGithubSDKPollerForRepo must
// register the in-tree github adapter as this repo's base-presence probe
// via RegisterBasePresenceProbe(..., newGitHubBasePresenceProbe(deps.Cfg))
// (GH-5053) — replacing the gh-CLI shellout fallback in production. This
// test fails if that registration line is ever removed or reverted to a
// different (e.g. gh-CLI-backed) probe.
func TestStartGithubSDKPollerForRepo_RegistersBasePresenceProbe(t *testing.T) {
	body := githubFuncBody(t, "poller_github.go", "func startGithubSDKPollerForRepo(")

	if !strings.Contains(body, `deps.Runner.RegisterBasePresenceProbe("github:"+target.repoFullName, newGitHubBasePresenceProbe(deps.Cfg))`) {
		t.Error("startGithubSDKPollerForRepo must register the in-tree github adapter as the base-presence probe via " +
			`deps.Runner.RegisterBasePresenceProbe("github:"+target.repoFullName, newGitHubBasePresenceProbe(deps.Cfg)) (GH-5053), ` +
			"replacing the gh-CLI shellout in the production wiring path")
	}

	// Registration must live alongside the sibling PR-creator/issue-state
	// registrations under the same deps.Runner != nil guard, not gated
	// separately — mirrors the label-lifecycle guard's sibling-ordering check.
	prCreatorIdx := strings.Index(body, "RegisterPRCreator(")
	basePresenceIdx := strings.Index(body, "RegisterBasePresenceProbe(")
	if prCreatorIdx < 0 || basePresenceIdx < 0 {
		t.Fatal("expected both the PR-creator and base-presence-probe registrations in startGithubSDKPollerForRepo")
	}
}
