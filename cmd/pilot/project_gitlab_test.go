package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/testutil"
)

type fakeProjectGitLabCreator struct {
	mu    sync.Mutex
	calls int
	// last call
	source, target, title, body string
	url                         string
}

func (f *fakeProjectGitLabCreator) CreatePR(_ context.Context, source, target, title, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.source, f.target, f.title, f.body = source, target, title, body
	return f.url, nil
}

// TestRegisterProjectGitLabPRCreators: one gitlab: project + one github:
// project registers exactly one project: creator and leaves the GitHub
// registrations untouched (GH-5583). adapters.gitlab.enabled stays false —
// registration must not depend on issue-intake flags.
func TestRegisterProjectGitLabPRCreators(t *testing.T) {
	glDir := t.TempDir()
	ghDir := t.TempDir()

	cfg := config.DefaultConfig()
	cfg.Adapters.GitLab.Enabled = false
	cfg.Adapters.GitLab.Token = testutil.FakeGitLabToken
	cfg.Projects = []*config.ProjectConfig{
		{Name: "service", Path: glDir, GitLab: &config.ProjectGitLabConfig{Project: "example-group/service"}},
		{Name: "webapp", Path: ghDir, GitHub: &config.ProjectGitHubConfig{Owner: "o", Repo: "webapp"}},
		{Name: "plain", Path: t.TempDir()},
	}

	runner := executor.NewRunner()
	preexisting := &fakeProjectGitLabCreator{}
	runner.RegisterPRCreator("github:o/webapp", preexisting)

	keys := registerProjectGitLabPRCreators(cfg, runner)

	wantKey := executor.ProjectPRCreatorKey(glDir)
	if len(keys) != 1 || keys[0] != wantKey {
		t.Fatalf("registered keys = %v, want exactly [%s]", keys, wantKey)
	}
	if !strings.HasPrefix(wantKey, "project:") {
		t.Fatalf("key %q lacks project: prefix", wantKey)
	}
	if !runner.HasPRCreator(wantKey) {
		t.Errorf("project creator %q not registered on runner", wantKey)
	}
	if runner.HasPRCreator(executor.ProjectPRCreatorKey(ghDir)) {
		t.Error("github-configured project must not get a project: creator")
	}
	if !runner.HasPRCreator("github:o/webapp") {
		t.Error("pre-existing github: registration was disturbed")
	}
}

func TestRegisterProjectGitLabPRCreators_NilAndMissingToken(t *testing.T) {
	if got := registerProjectGitLabPRCreators(nil, executor.NewRunner()); got != nil {
		t.Errorf("nil cfg: %v", got)
	}
	cfg := config.DefaultConfig()
	cfg.Projects = []*config.ProjectConfig{{Name: "s", Path: t.TempDir(), GitLab: &config.ProjectGitLabConfig{Project: "g/s"}}}
	// Validate would reject this at load; the registration is defence in depth.
	if got := registerProjectGitLabPRCreators(cfg, executor.NewRunner()); len(got) != 0 {
		t.Errorf("empty token must not register: %v", got)
	}
}

// TestStartAdapterPollers_RegistersProjectGitLabCreators proves the startup
// entry point performs the registration (not just the helper in isolation).
func TestStartAdapterPollers_RegistersProjectGitLabCreators(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Adapters.GitLab.Token = testutil.FakeGitLabToken
	cfg.Projects = []*config.ProjectConfig{{Name: "service", Path: dir, GitLab: &config.ProjectGitLabConfig{Project: "example-group/service"}}}
	runner := executor.NewRunner()

	StartAdapterPollers(context.Background(), &PollerDeps{Cfg: cfg, Runner: runner}, nil)

	if !runner.HasPRCreator(executor.ProjectPRCreatorKey(dir)) {
		t.Fatal("StartAdapterPollers did not register the project-keyed MR creator")
	}
}

func TestIsGitLabMRURL(t *testing.T) {
	for url, want := range map[string]bool{
		"https://gitlab.com/example-group/service/-/merge_requests/42":    true,
		"https://gitlab.example.com/g/sub/proj/-/merge_requests/7#note_1": true,
		"https://github.com/o/r/pull/42":                                  false,
		"https://dev.azure.com/o/p/_git/r/pullrequest/9":                  false,
		"": false,
	} {
		if got := isGitLabMRURL(url); got != want {
			t.Errorf("isGitLabMRURL(%q) = %v, want %v", url, got, want)
		}
	}
}

// mockCommitBackend is a no-op backend: the branch already carries a commit.
type mockCommitBackend struct{}

func (mockCommitBackend) Name() string      { return "mock-gh5583" }
func (mockCommitBackend) IsAvailable() bool { return true }
func (mockCommitBackend) Execute(_ context.Context, _ executor.ExecuteOptions) (*executor.BackendResult, error) {
	return &executor.BackendResult{Success: true, Output: "implementation complete"}, nil
}

func gh5583Git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestHandleIssueGeneric_LinearTaskOnGitLabProject_OpensMR is the GH-5583
// handler-level acceptance test: a Linear-sourced task whose ProjectPath has a
// gitlab: block ends with the project-keyed creator called with
// (pilot/<ID>, main, title, body); the body carries the Linear URL and no
// "Closes #"; hr.PRNumber is the MR iid parsed from the returned URL — even
// though a stale shared creator is also set and info.Adapter is "linear".
func TestHandleIssueGeneric_LinearTaskOnGitLabProject_OpensMR(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Fake gh: every lookup answers "no PR"; a stray `gh pr create` would be visible.
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte("#!/bin/sh\necho '[]'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))

	bare := t.TempDir()
	gh5583Git(t, bare, "init", "--bare", "-b", "main")
	gh5583Git(t, bare, "config", "gc.auto", "0")
	gh5583Git(t, bare, "config", "receive.autogc", "false")
	repo := t.TempDir()
	gh5583Git(t, repo, "init", "-b", "main")
	gh5583Git(t, repo, "config", "user.email", "test@pilot.local")
	gh5583Git(t, repo, "config", "user.name", "Pilot Test")
	gh5583Git(t, repo, "config", "gc.auto", "0")
	gh5583Git(t, repo, "commit", "--allow-empty", "-m", "initial")
	gh5583Git(t, repo, "remote", "add", "origin", bare)
	gh5583Git(t, repo, "push", "-u", "origin", "main")
	const taskID = "APP-77"
	gh5583Git(t, repo, "checkout", "-b", "pilot/"+taskID)
	if err := os.WriteFile(filepath.Join(repo, "change.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gh5583Git(t, repo, "add", ".")
	gh5583Git(t, repo, "commit", "-m", "fix(app): implement change")

	runner := executor.NewRunnerWithBackend(mockCommitBackend{})
	runner.SetRecordingEnabled(false)
	runner.SetSkipPreflightChecks(true)

	stale := &fakeProjectGitLabCreator{url: "https://gitlab.example.com/wrong/repo/-/merge_requests/1"}
	runner.SetPRCreator(stale)
	creator := &fakeProjectGitLabCreator{url: "https://gitlab.example.com/example-group/service/-/merge_requests/42"}
	runner.RegisterPRCreator(executor.ProjectPRCreatorKey(repo), creator)

	const title = "fix(app): implement change"
	task := &executor.Task{
		ID:            taskID,
		Title:         title,
		Description:   "Linear Issue APP-77: implement change",
		ProjectPath:   repo,
		Branch:        "pilot/" + taskID,
		BaseBranch:    "main",
		CreatePR:      true,
		SourceAdapter: "linear",
		SourceIssueID: "linear-uuid-77",
	}
	deps := HandlerDeps{Cfg: &config.Config{}, Runner: runner, ProjectPath: repo}
	info := IssueInfo{TaskID: taskID, Title: title, URL: "https://linear.app/issue/" + taskID, Adapter: "linear", LogMark: "▸"}

	hr, err := handleIssueGeneric(context.Background(), deps, info, task)
	if err != nil {
		t.Fatalf("handleIssueGeneric: %v", err)
	}
	if hr.Error != nil || !hr.Success {
		t.Fatalf("expected success, got Success=%v Error=%v (result err %q)", hr.Success, hr.Error, resultError(hr))
	}
	if stale.calls != 0 {
		t.Errorf("stale shared creator called %d times, want 0", stale.calls)
	}
	if creator.calls != 1 {
		t.Fatalf("project creator called %d times, want 1", creator.calls)
	}
	if creator.source != "pilot/"+taskID || creator.target != "main" {
		t.Errorf("CreatePR(source=%q, target=%q), want (pilot/%s, main)", creator.source, creator.target, taskID)
	}
	if want := taskID + ": " + strings.TrimPrefix(title, ""); !strings.HasPrefix(creator.title, taskID+": ") {
		t.Errorf("title = %q, want prefix %q", creator.title, want)
	}
	if !strings.Contains(creator.body, "https://linear.app/issue/"+taskID) {
		t.Errorf("body lacks Linear URL: %q", creator.body)
	}
	if strings.Contains(creator.body, "Closes #") {
		t.Errorf("body must not carry a closing keyword for a non-gitlab source: %q", creator.body)
	}
	if hr.PRURL != creator.url {
		t.Errorf("hr.PRURL = %q, want %q", hr.PRURL, creator.url)
	}
	if hr.PRNumber != 42 {
		t.Errorf("hr.PRNumber = %d, want 42 (MR iid)", hr.PRNumber)
	}
	// Control: the GitHub extractor cannot read an MR URL, which is the bug.
	if n, err := github.ExtractPRNumber(creator.url); err == nil && n == 42 {
		t.Error("control failed: GitHub extractor unexpectedly parsed the MR iid")
	}
}

func resultError(hr *HandlerResult) string {
	if hr == nil || hr.Result == nil {
		return ""
	}
	return hr.Result.Error
}
