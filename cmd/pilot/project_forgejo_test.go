package main

import (
	"testing"

	forgejoSDK "github.com/qf-studio/studio-sdk/sdk/integrations/forgejo"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

var _ executor.PRCreator = (*forgejoSDK.Client)(nil)

func TestRegisterProjectForgejoPRCreators(t *testing.T) {
	fgDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Adapters.Forgejo.Enabled = false
	cfg.Adapters.Forgejo.BaseURL = "http://rome.tail3ce82d.ts.net:30030"
	cfg.Adapters.Forgejo.Token = "test-forgejo-token"
	cfg.Projects = []*config.ProjectConfig{
		{Name: "aisto", Path: fgDir, Forgejo: &config.ProjectForgejoConfig{Repo: "lurra/aisto"}},
		{Name: "webapp", Path: t.TempDir(), GitHub: &config.ProjectGitHubConfig{Owner: "o", Repo: "webapp"}},
	}

	runner := executor.NewRunner()
	runner.RegisterPRCreator("github:o/webapp", &fakeProjectGitLabCreator{})

	keys := registerProjectForgejoPRCreators(cfg, runner)
	if len(keys) != 1 {
		t.Fatalf("registered %d keys, want 1: %v", len(keys), keys)
	}
	wantKey := executor.ProjectPRCreatorKey(fgDir)
	if keys[0] != wantKey {
		t.Errorf("key = %q, want %q", keys[0], wantKey)
	}
	if !runner.HasPRCreator(wantKey) {
		t.Errorf("runner missing PRCreator for %q", wantKey)
	}
	if !runner.HasPRCreator("github:o/webapp") {
		t.Error("existing github PRCreator was clobbered")
	}
}

func TestRegisterProjectForgejoPRCreators_NilAndMissingToken(t *testing.T) {
	if got := registerProjectForgejoPRCreators(nil, executor.NewRunner()); got != nil {
		t.Errorf("nil cfg returned %v, want nil", got)
	}
	cfg := config.DefaultConfig()
	cfg.Projects = []*config.ProjectConfig{
		{Name: "s", Path: t.TempDir(), Forgejo: &config.ProjectForgejoConfig{Repo: "lurra/aisto"}},
	}
	if got := registerProjectForgejoPRCreators(cfg, executor.NewRunner()); len(got) != 0 {
		t.Errorf("missing token returned %v, want empty", got)
	}
}

func TestStartAdapterPollers_RegistersProjectForgejoCreators(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Adapters.Forgejo.BaseURL = "http://rome.tail3ce82d.ts.net:30030"
	cfg.Adapters.Forgejo.Token = "test-forgejo-token"
	cfg.Projects = []*config.ProjectConfig{
		{Name: "aisto", Path: dir, Forgejo: &config.ProjectForgejoConfig{Repo: "lurra/aisto"}},
	}

	runner := executor.NewRunner()
	StartAdapterPollers(t.Context(), &PollerDeps{Cfg: cfg, Runner: runner}, nil)

	wantKey := executor.ProjectPRCreatorKey(dir)
	if !runner.HasPRCreator(wantKey) {
		t.Errorf("StartAdapterPollers did not register project Forgejo PRCreator %q", wantKey)
	}
}
