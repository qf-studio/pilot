package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/testutil"
)

// stubCommsGitHubServer points the creator's GitHub client at an httptest
// server and returns a channel of the request paths it receives.
func stubCommsGitHubServer(t *testing.T) chan string {
	t.Helper()
	paths := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 1, "html_url": "https://example.test/issues/1"})
	}))
	t.Cleanup(srv.Close)

	orig := newCommsGitHubClient
	newCommsGitHubClient = func(cfg *config.Config) *github.Client {
		return github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	}
	t.Cleanup(func() { newCommsGitHubClient = orig })
	return paths
}

func commsGitHubConfig(repo, projectPath string) *config.Config {
	return &config.Config{Adapters: &config.AdaptersConfig{GitHub: &github.Config{
		Enabled:     true,
		Token:       testutil.FakeGitHubToken,
		Repo:        repo,
		ProjectPath: projectPath,
	}}}
}

func TestBuildCommsIssueCreator_PerProjectEntries(t *testing.T) {
	paths := stubCommsGitHubServer(t)
	cfg := commsGitHubConfig("", "")
	cfg.Projects = []*config.ProjectConfig{
		{Name: "a", Path: "/var/lib/pilot/repos/a", GitHub: &config.ProjectGitHubConfig{Owner: "org-a", Repo: "repo-a"}},
		{Name: "b", Path: "/var/lib/pilot/repos/b", GitHub: &config.ProjectGitHubConfig{Owner: "org-b", Repo: "repo-b"}},
	}

	ic := buildCommsIssueCreator(cfg)
	if ic == nil {
		t.Fatal("buildCommsIssueCreator returned nil")
	}
	_, err := ic.CreateIssue(context.Background(), "/var/lib/pilot/repos/b", comms.IssueDraft{
		Title: "feat(chat): file an issue", Body: "body", Labels: []string{"pilot"},
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if got, want := <-paths, "/repos/org-b/repo-b/issues"; got != want {
		t.Fatalf("issue posted to %q, want %q", got, want)
	}
}

func TestBuildCommsIssueCreator_NilWithoutGitHub(t *testing.T) {
	cfg := commsGitHubConfig("org/repo", "")
	cfg.Adapters.GitHub.Enabled = false
	if ic := buildCommsIssueCreator(cfg); ic != nil {
		t.Fatalf("expected nil creator with GitHub disabled, got %v", ic)
	}
	cfg.Adapters.GitHub = nil
	if ic := buildCommsIssueCreator(cfg); ic != nil {
		t.Fatalf("expected nil creator with no GitHub block, got %v", ic)
	}
}

func TestBuildCommsIssueCreator_AdapterRepoFallback(t *testing.T) {
	paths := stubCommsGitHubServer(t)
	cfg := commsGitHubConfig("adapter-org/adapter-repo", "/var/lib/pilot/repos/main")

	ic := buildCommsIssueCreator(cfg)
	if ic == nil {
		t.Fatal("buildCommsIssueCreator returned nil")
	}
	_, err := ic.CreateIssue(context.Background(), "/var/lib/pilot/repos/main", comms.IssueDraft{
		Title: "feat(chat): file an issue", Body: "body", Labels: []string{"pilot"},
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if got, want := <-paths, "/repos/adapter-org/adapter-repo/issues"; got != want {
		t.Fatalf("issue posted to %q, want %q", got, want)
	}
}
