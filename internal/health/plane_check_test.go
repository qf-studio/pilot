package health

import (
	"errors"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/plane"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/health/verify"
	"github.com/qf-studio/pilot/internal/testutil"
)

// planeCfg builds a config with the given Plane block and project entries.
func planeCfg(pc *plane.Config, projects ...*config.ProjectConfig) *config.Config {
	return &config.Config{
		Adapters: &config.AdaptersConfig{Plane: pc},
		Projects: projects,
	}
}

// validPlaneBlock returns an enabled Plane block with no problem.
func validPlaneBlock() *plane.Config {
	return &plane.Config{
		Enabled:       true,
		BaseURL:       "https://plane.example.test",
		APIKey:        testutil.FakePlaneAPIKey,
		WorkspaceSlug: "acme",
		ProjectIDs:    []string{"proj-a", "proj-b"},
	}
}

func TestCheckPlaneLive(t *testing.T) {
	okVerifier := func(pc *plane.Config) verify.Verifiable {
		return &fakeVerifiable{name: "plane"}
	}

	t.Run("plane off gives nil", func(t *testing.T) {
		pc := validPlaneBlock()
		pc.Enabled = false
		if got := checkPlaneLive(planeCfg(pc), okVerifier); got != nil {
			t.Fatalf("expected nil, got %+v", got)
		}
		if got := checkPlaneLive(&config.Config{}, okVerifier); got != nil {
			t.Fatalf("expected nil with no adapters, got %+v", got)
		}
	})

	t.Run("empty key gives nil", func(t *testing.T) {
		pc := validPlaneBlock()
		pc.APIKey = ""
		if got := checkPlaneLive(planeCfg(pc), okVerifier); got != nil {
			t.Fatalf("expected nil, got %+v", got)
		}
	})

	t.Run("verifier ok gives OK", func(t *testing.T) {
		var seen *plane.Config
		pc := validPlaneBlock()
		got := checkPlaneLive(planeCfg(pc), func(c *plane.Config) verify.Verifiable {
			seen = c
			return &fakeVerifiable{name: "plane"}
		})
		if got == nil || got.Status != StatusOK || got.Name != "plane.key.live" {
			t.Fatalf("expected plane.key.live StatusOK, got %+v", got)
		}
		if seen != pc {
			t.Errorf("verifier got %p, want the configured Plane block %p", seen, pc)
		}
	})

	t.Run("verifier error gives Error with the error text", func(t *testing.T) {
		got := checkPlaneLive(planeCfg(validPlaneBlock()), func(c *plane.Config) verify.Verifiable {
			return &fakeVerifiable{name: "plane", err: errors.New("plane check failed: 401 unauthorized")}
		})
		if got == nil || got.Status != StatusError || got.Name != "plane.key.live" {
			t.Fatalf("expected plane.key.live StatusError, got %+v", got)
		}
		if !strings.Contains(got.Message, "401 unauthorized") {
			t.Errorf("message %q does not hold the verifier error", got.Message)
		}
	})

	t.Run("RunChecks adds the plane rows", func(t *testing.T) {
		orig := newPlaneVerifier
		t.Cleanup(func() { newPlaneVerifier = orig })
		newPlaneVerifier = func(pc *plane.Config) verify.Verifiable {
			return &fakeVerifiable{name: "plane", err: errFakeInvalidToken}
		}

		report := RunChecks(planeCfg(validPlaneBlock()), "")

		live := findConfigCheck(report.Config, "plane.key.live")
		if live == nil || live.Status != StatusError {
			t.Fatalf("expected plane.key.live StatusError from RunChecks, got %+v", live)
		}
		cfgRow := findConfigCheck(report.Config, "plane.config")
		if cfgRow == nil || cfgRow.Status != StatusOK {
			t.Fatalf("expected plane.config StatusOK from RunChecks, got %+v", cfgRow)
		}
	})
}

func TestCheckPlaneConfig(t *testing.T) {
	tests := []struct {
		name       string
		cfg        func() *config.Config
		wantNil    bool
		wantStatus Status
		wantSubstr []string
	}{
		{
			name: "plane off",
			cfg: func() *config.Config {
				pc := validPlaneBlock()
				pc.Enabled = false
				return planeCfg(pc)
			},
			wantNil: true,
		},
		{
			name: "no adapters block",
			cfg: func() *config.Config {
				return &config.Config{}
			},
			wantNil: true,
		},
		{
			name: "full valid block",
			cfg: func() *config.Config {
				return planeCfg(validPlaneBlock(),
					&config.ProjectConfig{Name: "web", Plane: &config.ProjectPlaneConfig{ProjectID: "proj-a"}},
					&config.ProjectConfig{Name: "api"},
				)
			},
			wantStatus: StatusOK,
			wantSubstr: []string{"acme", "2 polled"},
		},
		{
			name: "empty key",
			cfg: func() *config.Config {
				pc := validPlaneBlock()
				pc.APIKey = ""
				return planeCfg(pc)
			},
			wantStatus: StatusError,
			wantSubstr: []string{"api_key is empty"},
		},
		{
			name: "empty slug",
			cfg: func() *config.Config {
				pc := validPlaneBlock()
				pc.WorkspaceSlug = ""
				return planeCfg(pc)
			},
			wantStatus: StatusError,
			wantSubstr: []string{"workspace_slug is empty"},
		},
		{
			name: "empty project list",
			cfg: func() *config.Config {
				pc := validPlaneBlock()
				pc.ProjectIDs = nil
				return planeCfg(pc)
			},
			wantStatus: StatusError,
			wantSubstr: []string{"project_ids is empty"},
		},
		{
			name: "project entry pairs a plane project that is not polled",
			cfg: func() *config.Config {
				return planeCfg(validPlaneBlock(),
					&config.ProjectConfig{Name: "web", Plane: &config.ProjectPlaneConfig{ProjectID: "proj-a"}},
					&config.ProjectConfig{Name: "mobile", Plane: &config.ProjectPlaneConfig{ProjectID: "proj-z"}},
				)
			},
			wantStatus: StatusError,
			wantSubstr: []string{"projects[1] (mobile)", `"proj-z"`},
		},
		{
			name: "two problems at once",
			cfg: func() *config.Config {
				pc := validPlaneBlock()
				pc.APIKey = ""
				pc.WorkspaceSlug = ""
				return planeCfg(pc)
			},
			wantStatus: StatusError,
			wantSubstr: []string{"api_key is empty", "workspace_slug is empty"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkPlaneConfig(tt.cfg())
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected a plane.config row, got nil")
			}
			if got.Name != "plane.config" {
				t.Errorf("name = %q, want plane.config", got.Name)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("status = %v, want %v (message %q)", got.Status, tt.wantStatus, got.Message)
			}
			for _, sub := range tt.wantSubstr {
				if !strings.Contains(got.Message, sub) {
					t.Errorf("message %q does not contain %q", got.Message, sub)
				}
			}
			if tt.wantStatus == StatusError && !strings.Contains(got.Fix, "adapters.plane.project_ids") {
				t.Errorf("fix %q does not name the config keys", got.Fix)
			}
		})
	}
}

func TestCheckPlaneConfig_NoKeyInMessage(t *testing.T) {
	pc := validPlaneBlock()
	pc.WorkspaceSlug = ""
	pc.ProjectIDs = nil
	cfg := planeCfg(pc,
		&config.ProjectConfig{Name: "web", Plane: &config.ProjectPlaneConfig{ProjectID: "proj-z"}},
	)

	got := checkPlaneConfig(cfg)
	if got == nil || got.Status != StatusError {
		t.Fatalf("expected plane.config StatusError, got %+v", got)
	}
	marker := testutil.FakePlaneAPIKey
	if strings.Contains(got.Message, marker) {
		t.Errorf("message holds the key: %q", got.Message)
	}
	if strings.Contains(got.Fix, marker) {
		t.Errorf("fix holds the key: %q", got.Fix)
	}
}

// mutation pin: a project_ids list of only blank entries passes plane.config as OK -> this test fails
func TestCheckPlaneConfig_BlankProjectIDsAreEmpty(t *testing.T) {
	pc := validPlaneBlock()
	pc.ProjectIDs = []string{"", "  "}

	got := checkPlaneConfig(planeCfg(pc))
	if got == nil || got.Status != StatusError {
		t.Fatalf("expected plane.config StatusError, got %+v", got)
	}
	if !strings.Contains(got.Message, "project_ids is empty") {
		t.Errorf("message %q does not contain %q", got.Message, "project_ids is empty")
	}
}

// mutation pin: checkPlaneLive probes a whitespace-only key that plane.config already reports as empty -> this test fails
func TestCheckPlaneLive_WhitespaceKeyGivesNil(t *testing.T) {
	pc := validPlaneBlock()
	pc.APIKey = "   "

	called := false
	got := checkPlaneLive(planeCfg(pc), func(c *plane.Config) verify.Verifiable {
		called = true
		return &fakeVerifiable{name: "plane", err: errFakeInvalidToken}
	})
	if got != nil {
		t.Fatalf("expected nil for a whitespace-only key, got %+v", got)
	}
	if called {
		t.Error("verifier was built for a whitespace-only key")
	}
	if cfgRow := checkPlaneConfig(planeCfg(pc)); cfgRow == nil || !strings.Contains(cfgRow.Message, "api_key is empty") {
		t.Errorf("expected plane.config to report the empty key, got %+v", cfgRow)
	}
}
