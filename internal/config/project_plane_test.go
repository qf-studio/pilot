package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func planeProjectCfg(projects ...*ProjectConfig) *Config {
	c := DefaultConfig()
	c.Projects = projects
	return c
}

// TestConfig_GetProjectByPlaneID covers the GH-6707 lookup.
func TestConfig_GetProjectByPlaneID(t *testing.T) {
	cfg := planeProjectCfg(
		&ProjectConfig{Name: "plain", Path: "/srv/plain"},
		&ProjectConfig{Name: "web", Path: "/srv/web", Plane: &ProjectPlaneConfig{ProjectID: "plane-proj-web"}},
		&ProjectConfig{Name: "api", Path: "/srv/api", Plane: &ProjectPlaneConfig{ProjectID: "plane-proj-api"}},
	)

	tests := []struct {
		name     string
		id       string
		wantName string // "" = nil
	}{
		{name: "first entry", id: "plane-proj-web", wantName: "web"},
		{name: "second entry", id: "plane-proj-api", wantName: "api"},
		{name: "unknown id", id: "plane-proj-other"},
		{name: "empty id does not match a project without a plane block", id: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.GetProjectByPlaneID(tt.id)
			if tt.wantName == "" {
				if got != nil {
					t.Fatalf("GetProjectByPlaneID(%q) = %q, want nil", tt.id, got.Name)
				}
				return
			}
			if got == nil || got.Name != tt.wantName {
				t.Fatalf("GetProjectByPlaneID(%q) = %v, want %q", tt.id, got, tt.wantName)
			}
		})
	}
}

// TestConfig_Validate_ProjectPlane covers the GH-6707 validation rules.
func TestConfig_Validate_ProjectPlane(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *Config
		errContains []string // nil = valid
	}{
		{
			name: "no plane block",
			cfg:  planeProjectCfg(&ProjectConfig{Name: "plain", Path: "/srv/plain"}),
		},
		{
			name: "two distinct plane project ids",
			cfg: planeProjectCfg(
				&ProjectConfig{Name: "web", Path: "/srv/web", Plane: &ProjectPlaneConfig{ProjectID: "plane-proj-web"}},
				&ProjectConfig{Name: "api", Path: "/srv/api", Plane: &ProjectPlaneConfig{ProjectID: "plane-proj-api"}},
			),
		},
		{
			name: "duplicate plane project id names both entries",
			cfg: planeProjectCfg(
				&ProjectConfig{Name: "web", Path: "/srv/web", Plane: &ProjectPlaneConfig{ProjectID: "plane-proj-shared"}},
				&ProjectConfig{Name: "plain", Path: "/srv/plain"},
				&ProjectConfig{Name: "api", Path: "/srv/api", Plane: &ProjectPlaneConfig{ProjectID: "plane-proj-shared"}},
			),
			errContains: []string{"projects[0] (web)", "projects[2] (api)", `plane.project_id "plane-proj-shared"`},
		},
		{
			name: "empty project id",
			cfg: planeProjectCfg(
				&ProjectConfig{Name: "web", Path: "/srv/web", Plane: &ProjectPlaneConfig{}},
			),
			errContains: []string{"projects[0] (web): plane.project_id must not be empty"},
		},
		{
			name: "whitespace project id is empty",
			cfg: planeProjectCfg(
				&ProjectConfig{Name: "web", Path: "/srv/web", Plane: &ProjectPlaneConfig{ProjectID: "  "}},
			),
			errContains: []string{"plane.project_id must not be empty"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.errContains == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.errContains)
			}
			for _, want := range tt.errContains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Validate() = %v, want error containing %q", err, want)
				}
			}
		})
	}
}

func TestProjectConfig_PlaneYAML(t *testing.T) {
	const doc = `
name: web
path: /srv/repos/web
plane:
  project_id: plane-proj-web
`
	var p ProjectConfig
	if err := yaml.Unmarshal([]byte(doc), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if p.Plane == nil || p.Plane.ProjectID != "plane-proj-web" {
		t.Fatalf("Plane = %+v, want project_id plane-proj-web", p.Plane)
	}
}
