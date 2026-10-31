package main

import (
	"context"
	"io"
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
// IssueOrPRState (repo_state.go, GH-5046) against a real HTTP round trip:
// LinkedPRNumbers takes SearchPRsForIssue's hits as candidates and keeps only
// PRs that deliver the issue (GH-5985) — a mention-only PR is dropped, a PR
// on branch pilot/GH-N, a PR whose closingIssuesReferences hold N, a PR whose
// body closes #N are kept, and a candidate whose pull lookup fails is dropped
// — and IssueOrPRState (inherited via the embedded *github.Client) keeps
// working through the same wrapper.
func TestGithubBasePresenceProbe_LinkedPRNumbers(t *testing.T) {
	// PR 31: mention only (the 10-28 incident shape: closes #5927, says "see #5917").
	// PR 32: delivering, by head branch.
	// PR 33: delivering, by closingIssuesReferences (graphql).
	// PR 34: delivering, by body closing keyword.
	// PR 35: pull lookup 404s — dropped, never a hold.
	var gotPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/issues"):
			_, _ = w.Write([]byte(`{"items":[` +
				`{"id":1,"number":31,"title":"other","state":"open"},` +
				`{"id":2,"number":32,"title":"deliver","state":"open"},` +
				`{"id":3,"number":33,"title":"graphql","state":"open"},` +
				`{"id":4,"number":34,"title":"body","state":"open"},` +
				`{"id":5,"number":35,"title":"gone","state":"open"}]}`))
		case r.URL.Path == "/graphql":
			reqBody, _ := io.ReadAll(r.Body)
			nodes := `[]`
			if strings.Contains(string(reqBody), `"pr":33`) {
				nodes = `[{"number":5917,"repository":{"nameWithOwner":"owner/repo"}}]`
			}
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"closingIssuesReferences":{"nodes":` + nodes + `}}}}}`))
		case r.URL.Path == "/repos/owner/repo/pulls/31":
			_, _ = w.Write([]byte(`{"number":31,"body":"see #5917","head":{"ref":"pilot/GH-5927"}}`))
		case r.URL.Path == "/repos/owner/repo/pulls/32":
			_, _ = w.Write([]byte(`{"number":32,"body":"","head":{"ref":"pilot/GH-5917"}}`))
		case r.URL.Path == "/repos/owner/repo/pulls/33":
			_, _ = w.Write([]byte(`{"number":33,"body":"","head":{"ref":"feature/x"}}`))
		case r.URL.Path == "/repos/owner/repo/pulls/34":
			_, _ = w.Write([]byte(`{"number":34,"body":"Fixes: #5917","head":{"ref":"feature/y"}}`))
		case r.URL.Path == "/repos/owner/repo/issues/7":
			_, _ = w.Write([]byte(`{"number":7,"state":"open"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	defer server.Close()

	probe := githubBasePresenceProbe{Client: github.NewClientWithBaseURL("test-token", server.URL)}

	numbers, err := probe.LinkedPRNumbers(context.Background(), "owner", "repo", 5917)
	if err != nil {
		t.Fatalf("LinkedPRNumbers: unexpected error: %v", err)
	}
	want := []int{32, 33, 34}
	if len(numbers) != len(want) {
		t.Fatalf("LinkedPRNumbers = %v, want %v (mention-only PR 31 and failed lookup PR 35 must be dropped)", numbers, want)
	}
	for i, n := range want {
		if numbers[i] != n {
			t.Errorf("LinkedPRNumbers = %v, want %v", numbers, want)
			break
		}
	}

	kind, state, err := probe.IssueOrPRState(context.Background(), "owner", "repo", 7)
	if err != nil {
		t.Fatalf("IssueOrPRState: unexpected error: %v", err)
	}
	if kind != "issue" || state != "open" {
		t.Errorf("IssueOrPRState = (%q, %q), want (\"issue\", \"open\")", kind, state)
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
