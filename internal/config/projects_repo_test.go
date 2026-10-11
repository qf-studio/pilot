package config

import "testing"

func TestProjectSource_RepoFromGitHubBlock(t *testing.T) {
	cfg := &Config{Projects: []*ProjectConfig{
		{Name: "alpha", Path: "/work/alpha", GitHub: &ProjectGitHubConfig{Owner: "acme", Repo: "alpha-repo"}},
		{Name: "beta", Path: "/work/beta"},
		{Name: "gamma", Path: "/work/gamma", GitHub: &ProjectGitHubConfig{Owner: "acme"}},
	}}
	src := NewProjectSource(cfg)

	tests := []struct{ name, want string }{
		{"alpha", "acme/alpha-repo"},
		{"beta", ""},
		{"gamma", ""},
	}
	for _, tt := range tests {
		p := src.GetProjectByName(tt.name)
		if p == nil {
			t.Fatalf("project %q not found", tt.name)
		}
		if p.Repo != tt.want {
			t.Errorf("%s: Repo = %q, want %q", tt.name, p.Repo, tt.want)
		}
	}
}
