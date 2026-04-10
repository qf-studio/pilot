package autopilot

import (
	"testing"
	"time"
)

func TestResolvedEnv_LegacyDev(t *testing.T) {
	cfg := &Config{Environment: EnvDev}
	env := cfg.ResolvedEnv()

	if env.RequireApproval {
		t.Error("dev: RequireApproval should be false")
	}
	if env.CITimeout != 5*time.Minute {
		t.Errorf("dev: CITimeout = %v, want 5m", env.CITimeout)
	}
	if !env.SkipPostMergeCI {
		t.Error("dev: SkipPostMergeCI should be true")
	}
}

func TestResolvedEnv_LegacyStage(t *testing.T) {
	cfg := &Config{Environment: EnvStage}
	env := cfg.ResolvedEnv()

	if env.RequireApproval {
		t.Error("stage: RequireApproval should be false")
	}
	if env.CITimeout != 30*time.Minute {
		t.Errorf("stage: CITimeout = %v, want 30m", env.CITimeout)
	}
	if env.SkipPostMergeCI {
		t.Error("stage: SkipPostMergeCI should be false")
	}
}

func TestResolvedEnv_LegacyProd(t *testing.T) {
	cfg := &Config{Environment: EnvProd}
	env := cfg.ResolvedEnv()

	if !env.RequireApproval {
		t.Error("prod: RequireApproval should be true")
	}
	if env.CITimeout != 30*time.Minute {
		t.Errorf("prod: CITimeout = %v, want 30m", env.CITimeout)
	}
	if env.SkipPostMergeCI {
		t.Error("prod: SkipPostMergeCI should be false")
	}
}

func TestResolvedEnv_NewStyleMap(t *testing.T) {
	cfg := &Config{
		Environments: map[string]*EnvironmentConfig{
			"staging": {
				Branch:          "develop",
				RequireApproval: false,
				CITimeout:       15 * time.Minute,
				SkipPostMergeCI: false,
			},
		},
	}
	if err := cfg.SetActiveEnvironment("staging"); err != nil {
		t.Fatalf("SetActiveEnvironment: %v", err)
	}

	env := cfg.ResolvedEnv()
	if env.Branch != "develop" {
		t.Errorf("Branch = %q, want %q", env.Branch, "develop")
	}
	if env.CITimeout != 15*time.Minute {
		t.Errorf("CITimeout = %v, want 15m", env.CITimeout)
	}
	if env.RequireApproval {
		t.Error("RequireApproval should be false")
	}
}

func TestResolvedEnv_CustomEnv(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Environments["qa"] = &EnvironmentConfig{
		Branch:          "qa",
		RequireApproval: true,
		CITimeout:       10 * time.Minute,
		SkipPostMergeCI: false,
		PostMerge:       &PostMergeConfig{Action: "none"},
	}

	if err := cfg.SetActiveEnvironment("qa"); err != nil {
		t.Fatalf("SetActiveEnvironment: %v", err)
	}

	env := cfg.ResolvedEnv()
	if !env.RequireApproval {
		t.Error("qa: RequireApproval should be true")
	}
	if env.CITimeout != 10*time.Minute {
		t.Errorf("qa: CITimeout = %v, want 10m", env.CITimeout)
	}
	if env.Branch != "qa" {
		t.Errorf("qa: Branch = %q, want %q", env.Branch, "qa")
	}
}

func TestResolvedEnv_NewOverridesLegacy(t *testing.T) {
	// Legacy field says prod (RequireApproval=true), but new-style active env is dev (RequireApproval=false).
	cfg := &Config{
		Environment: EnvProd,
		Environments: map[string]*EnvironmentConfig{
			"dev": {RequireApproval: false, CITimeout: 5 * time.Minute, SkipPostMergeCI: true},
		},
	}
	if err := cfg.SetActiveEnvironment("dev"); err != nil {
		t.Fatalf("SetActiveEnvironment: %v", err)
	}

	env := cfg.ResolvedEnv()
	if env.RequireApproval {
		t.Error("new-style dev should override legacy prod: RequireApproval should be false")
	}
}

func TestPRState_RepoOwnerAndName(t *testing.T) {
	tests := []struct {
		name      string
		prURL     string
		wantOwner string
		wantRepo  string
	}{
		{
			name:      "cross-repo PR URL",
			prURL:     "https://github.com/qf-studio/auth-service/pull/422",
			wantOwner: "qf-studio",
			wantRepo:  "auth-service",
		},
		{
			name:      "same-repo PR URL",
			prURL:     "https://github.com/alekspetrov/pilot/pull/100",
			wantOwner: "alekspetrov",
			wantRepo:  "pilot",
		},
		{
			name:      "empty URL falls back",
			prURL:     "",
			wantOwner: "fallback-owner",
			wantRepo:  "fallback-repo",
		},
		{
			name:      "non-github URL falls back",
			prURL:     "https://gitlab.com/org/repo/merge_requests/1",
			wantOwner: "fallback-owner",
			wantRepo:  "fallback-repo",
		},
		{
			name:      "malformed github URL falls back",
			prURL:     "https://github.com/",
			wantOwner: "fallback-owner",
			wantRepo:  "fallback-repo",
		},
		{
			name:      "github URL with only owner",
			prURL:     "https://github.com/owner",
			wantOwner: "fallback-owner",
			wantRepo:  "fallback-repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := &PRState{PRURL: tt.prURL}
			owner, repo := ps.RepoOwnerAndName("fallback-owner", "fallback-repo")
			if owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}

func TestEnvironmentName_Legacy(t *testing.T) {
	tests := []struct {
		env  Environment
		want string
	}{
		{EnvDev, "dev"},
		{EnvStage, "stage"},
		{EnvProd, "prod"},
	}
	for _, tt := range tests {
		cfg := &Config{Environment: tt.env}
		got := cfg.EnvironmentName()
		if got != tt.want {
			t.Errorf("EnvironmentName() for env %q = %q, want %q", tt.env, got, tt.want)
		}
	}
}

func TestEnvironmentName_NewStyle(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.SetActiveEnvironment("prod"); err != nil {
		t.Fatalf("SetActiveEnvironment: %v", err)
	}

	got := cfg.EnvironmentName()
	if got != "prod" {
		t.Errorf("EnvironmentName() = %q, want %q", got, "prod")
	}

	// Custom env name
	cfg2 := DefaultConfig()
	cfg2.Environments["canary"] = &EnvironmentConfig{
		RequireApproval: false,
		CITimeout:       20 * time.Minute,
	}
	if err := cfg2.SetActiveEnvironment("canary"); err != nil {
		t.Fatalf("SetActiveEnvironment canary: %v", err)
	}
	got2 := cfg2.EnvironmentName()
	if got2 != "canary" {
		t.Errorf("EnvironmentName() = %q, want %q", got2, "canary")
	}
}
