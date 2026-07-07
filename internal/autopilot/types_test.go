package autopilot

import (
	"testing"
	"time"
)

func TestProjectReleaseConfig_Apply(t *testing.T) {
	t.Run("nil overlay returns base unchanged", func(t *testing.T) {
		base := &ReleaseConfig{Enabled: true, Publish: "workflow", TagPrefix: "v"}
		var p *ProjectReleaseConfig
		got := p.Apply(base)
		if got != base {
			t.Errorf("Apply(base) with nil overlay = %+v, want the same base pointer %+v", got, base)
		}
	})

	t.Run("nil base with overlay not enabling release returns nil", func(t *testing.T) {
		p := &ProjectReleaseConfig{Publish: "api"}
		if got := p.Apply(nil); got != nil {
			t.Errorf("Apply(nil) = %+v, want nil (overlay does not enable release)", got)
		}
	})

	t.Run("nil base with enabled overlay starts from DefaultReleaseConfig", func(t *testing.T) {
		p := &ProjectReleaseConfig{Enabled: boolPtr(true), Publish: "api"}
		got := p.Apply(nil)
		if got == nil {
			t.Fatal("Apply(nil) = nil, want a config derived from DefaultReleaseConfig")
		}
		want := DefaultReleaseConfig()
		if !got.Enabled {
			t.Error("Enabled = false, want true")
		}
		if got.Publish != "api" {
			t.Errorf("Publish = %q, want %q", got.Publish, "api")
		}
		if got.TagPrefix != want.TagPrefix {
			t.Errorf("TagPrefix = %q, want default %q (uninvolved field must inherit)", got.TagPrefix, want.TagPrefix)
		}
	})

	t.Run("publish-only overlay inherits everything else", func(t *testing.T) {
		base := &ReleaseConfig{
			Enabled:           true,
			Trigger:           "on_merge",
			VersionStrategy:   "conventional_commits",
			TagPrefix:         "v",
			GenerateChangelog: true,
			NotifyOnRelease:   true,
			RequireCI:         true,
			Publish:           "workflow",
		}
		p := &ProjectReleaseConfig{Publish: "api"}
		got := p.Apply(base)
		if got.Publish != "api" {
			t.Errorf("Publish = %q, want %q", got.Publish, "api")
		}
		if !got.Enabled {
			t.Error("Enabled must be inherited as true from base")
		}
		if got.TagPrefix != "v" {
			t.Errorf("TagPrefix = %q, want inherited %q", got.TagPrefix, "v")
		}
		if !got.RequireCI {
			t.Error("RequireCI must be inherited from base (not overlayable)")
		}
		// Base must not be mutated.
		if base.Publish != "workflow" {
			t.Errorf("base.Publish mutated to %q, want unchanged %q", base.Publish, "workflow")
		}
	})

	t.Run("enabled override false disables an otherwise-enabled base", func(t *testing.T) {
		base := &ReleaseConfig{Enabled: true, Publish: "workflow"}
		p := &ProjectReleaseConfig{Enabled: boolPtr(false)}
		got := p.Apply(base)
		if got.Enabled {
			t.Error("Enabled = true, want false (overlay explicitly disables)")
		}
	})

	t.Run("tag_prefix override", func(t *testing.T) {
		base := &ReleaseConfig{Enabled: true, TagPrefix: "v"}
		p := &ProjectReleaseConfig{TagPrefix: "release-"}
		got := p.Apply(base)
		if got.TagPrefix != "release-" {
			t.Errorf("TagPrefix = %q, want %q", got.TagPrefix, "release-")
		}
	})

	t.Run("verify_release and verify_timeout override", func(t *testing.T) {
		base := &ReleaseConfig{Enabled: true, VerifyRelease: boolPtr(true), VerifyTimeout: 10 * time.Minute}
		p := &ProjectReleaseConfig{VerifyRelease: boolPtr(false), VerifyTimeout: 5 * time.Minute}
		got := p.Apply(base)
		if got.VerifyRelease == nil || *got.VerifyRelease {
			t.Errorf("VerifyRelease = %v, want false", got.VerifyRelease)
		}
		if got.VerifyTimeout != 5*time.Minute {
			t.Errorf("VerifyTimeout = %v, want %v", got.VerifyTimeout, 5*time.Minute)
		}
	})

	t.Run("verify_release and verify_timeout unset inherit from base", func(t *testing.T) {
		base := &ReleaseConfig{Enabled: true, VerifyRelease: boolPtr(true), VerifyTimeout: 10 * time.Minute}
		p := &ProjectReleaseConfig{Publish: "api"}
		got := p.Apply(base)
		if got.VerifyRelease == nil || !*got.VerifyRelease {
			t.Errorf("VerifyRelease = %v, want inherited true", got.VerifyRelease)
		}
		if got.VerifyTimeout != 10*time.Minute {
			t.Errorf("VerifyTimeout = %v, want inherited %v", got.VerifyTimeout, 10*time.Minute)
		}
	})
}

func TestReleaseConfig_VerifyReleaseEnabled(t *testing.T) {
	tests := []struct {
		name string
		rel  *ReleaseConfig
		want bool
	}{
		{name: "nil receiver", rel: nil, want: false},
		{name: "unset defaults to true in workflow mode", rel: &ReleaseConfig{Publish: "workflow"}, want: true},
		{name: "unset defaults to true when publish empty (workflow)", rel: &ReleaseConfig{}, want: true},
		{name: "unset defaults to false in api mode", rel: &ReleaseConfig{Publish: "api"}, want: false},
		{name: "unset defaults to false in tag_only mode", rel: &ReleaseConfig{Publish: "tag_only"}, want: false},
		{name: "explicit true in api mode wins", rel: &ReleaseConfig{Publish: "api", VerifyRelease: boolPtr(true)}, want: true},
		{name: "explicit false in workflow mode wins", rel: &ReleaseConfig{Publish: "workflow", VerifyRelease: boolPtr(false)}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rel.VerifyReleaseEnabled(); got != tt.want {
				t.Errorf("VerifyReleaseEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDefaultReleaseConfig_VerifyTimeout(t *testing.T) {
	got := DefaultReleaseConfig()
	if got.VerifyTimeout != 10*time.Minute {
		t.Errorf("DefaultReleaseConfig().VerifyTimeout = %v, want %v", got.VerifyTimeout, 10*time.Minute)
	}
}

func TestReleaseConfig_PublishMode(t *testing.T) {
	tests := []struct {
		name string
		rel  *ReleaseConfig
		want string
	}{
		{name: "nil receiver defaults to workflow", rel: nil, want: ReleasePublishWorkflow},
		{name: "empty publish defaults to workflow", rel: &ReleaseConfig{Publish: ""}, want: ReleasePublishWorkflow},
		{name: "explicit workflow", rel: &ReleaseConfig{Publish: "workflow"}, want: "workflow"},
		{name: "api", rel: &ReleaseConfig{Publish: "api"}, want: "api"},
		{name: "tag_only", rel: &ReleaseConfig{Publish: "tag_only"}, want: "tag_only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rel.PublishMode(); got != tt.want {
				t.Errorf("PublishMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

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
			prURL:     "https://github.com/qf-studio/pilot/pull/100",
			wantOwner: "qf-studio",
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
