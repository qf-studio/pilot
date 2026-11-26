package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
)

// forgejoCfg returns a default config with a valid enabled Forgejo adapter
// (clearly fake values) and the given projects.
func forgejoCfg(projects ...*ProjectConfig) *Config {
	c := DefaultConfig()
	c.Adapters.Forgejo = &ForgejoConfig{
		Enabled:       true,
		BaseURL:       "https://forgejo.example.test",
		Token:         "test-forgejo-token",
		WebhookSecret: "test-forgejo-webhook-secret",
		PilotLabel:    "pilot",
	}
	c.Projects = projects
	return c
}

func fjProject(name, path, repo string) *ProjectConfig {
	return &ProjectConfig{Name: name, Path: path, Forgejo: &ProjectForgejoConfig{Repo: repo}}
}

func TestForgejoDefaultDisabled(t *testing.T) {
	c := DefaultConfig()
	if c.Adapters.Forgejo == nil {
		t.Fatal("DefaultConfig().Adapters.Forgejo is nil, want a disabled default")
	}
	if c.Adapters.Forgejo.Enabled {
		t.Error("Forgejo must be disabled by default")
	}
	if got := c.Adapters.Forgejo.ResolvedPilotLabel(); got != "pilot" {
		t.Errorf("default label = %q, want pilot", got)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}
	var nilCfg *ForgejoConfig
	if got := nilCfg.ResolvedPilotLabel(); got != "pilot" {
		t.Errorf("nil receiver label = %q, want pilot", got)
	}
	if got := (&ForgejoConfig{PilotLabel: "  "}).ResolvedPilotLabel(); got != "pilot" {
		t.Errorf("blank label = %q, want pilot", got)
	}
	if got := (&ForgejoConfig{PilotLabel: "ship-it"}).ResolvedPilotLabel(); got != "ship-it" {
		t.Errorf("custom label = %q", got)
	}
}

func TestForgejoConfigValidation(t *testing.T) {
	dir := t.TempDir()
	real1 := filepath.Join(dir, "real1")
	if err := os.Mkdir(real1, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(real1, alias); err != nil {
		t.Fatal(err)
	}
	real2 := filepath.Join(dir, "real2")
	if err := os.Mkdir(real2, 0o755); err != nil {
		t.Fatal(err)
	}

	mut := func(f func(*ForgejoConfig), projects ...*ProjectConfig) *Config {
		c := forgejoCfg(projects...)
		f(c.Adapters.Forgejo)
		return c
	}
	noop := func(*ForgejoConfig) {}

	tests := []struct {
		name        string
		cfg         *Config
		errContains string // "" = valid
	}{
		{"valid enabled with project", forgejoCfg(fjProject("a", real1, "acme/app")), ""},
		{"valid enabled without projects", forgejoCfg(), ""},
		{"valid label default empty", mut(func(f *ForgejoConfig) { f.PilotLabel = "" }, fjProject("a", real1, "acme/app")), ""},
		{"valid loopback localhost http", mut(func(f *ForgejoConfig) { f.BaseURL = "http://localhost:3000" }), ""},
		{"valid loopback 127.0.0.1 http", mut(func(f *ForgejoConfig) { f.BaseURL = "http://127.0.0.1:31957" }), ""},
		{"valid loopback ipv6 http", mut(func(f *ForgejoConfig) { f.BaseURL = "http://[::1]:3000" }), ""},
		{"valid https subpath", mut(func(f *ForgejoConfig) { f.BaseURL = "https://forgejo.example.test/git/" }), ""},
		{"disabled adapter skips field checks", mut(func(f *ForgejoConfig) { *f = ForgejoConfig{BaseURL: "http://evil.example.test"} }), ""},

		{"missing base_url", mut(func(f *ForgejoConfig) { f.BaseURL = "" }), "adapters.forgejo.base_url is required"},
		{"http non-loopback", mut(func(f *ForgejoConfig) { f.BaseURL = "http://forgejo.example.test" }), "https"},
		{"http lookalike loopback host", mut(func(f *ForgejoConfig) { f.BaseURL = "http://localhost.example.test" }), "https"},
		{"http 127 prefix host", mut(func(f *ForgejoConfig) { f.BaseURL = "http://127.0.0.1.example.test" }), "https"},
		{"ftp scheme", mut(func(f *ForgejoConfig) { f.BaseURL = "ftp://forgejo.example.test" }), "https"},
		{"no host", mut(func(f *ForgejoConfig) { f.BaseURL = "https://" }), "host"},
		{"relative url", mut(func(f *ForgejoConfig) { f.BaseURL = "forgejo.example.test" }), "https"},
		{"userinfo", mut(func(f *ForgejoConfig) { f.BaseURL = "https://user:pw@forgejo.example.test" }), "credentials"},
		{"userinfo user only", mut(func(f *ForgejoConfig) { f.BaseURL = "https://user@forgejo.example.test" }), "credentials"},
		{"query", mut(func(f *ForgejoConfig) { f.BaseURL = "https://forgejo.example.test/?a=b" }), "query"},
		{"empty query marker", mut(func(f *ForgejoConfig) { f.BaseURL = "https://forgejo.example.test/?" }), "query"},
		{"fragment", mut(func(f *ForgejoConfig) { f.BaseURL = "https://forgejo.example.test/#x" }), "fragment"},
		{"whitespace", mut(func(f *ForgejoConfig) { f.BaseURL = "https://forgejo.example.test/ x" }), "whitespace"},
		{"missing token", mut(func(f *ForgejoConfig) { f.Token = "" }), "adapters.forgejo.token is required"},
		{"blank token", mut(func(f *ForgejoConfig) { f.Token = "   " }), "adapters.forgejo.token is required"},
		{"missing webhook_secret", mut(func(f *ForgejoConfig) { f.WebhookSecret = "" }), "adapters.forgejo.webhook_secret is required"},
		{"blank webhook_secret", mut(func(f *ForgejoConfig) { f.WebhookSecret = " " }), "adapters.forgejo.webhook_secret is required"},

		{"duplicate repo", forgejoCfg(fjProject("a", real1, "acme/app"), fjProject("b", real2, "acme/app")), "mapped more than once"},
		{"duplicate project path", forgejoCfg(fjProject("a", real1, "acme/app"), fjProject("b", real1, "acme/other")), "path"},
		{"duplicate project path via symlink alias", forgejoCfg(fjProject("a", real1, "acme/app"), fjProject("b", alias, "acme/other")), "path"},
		{"duplicate path via dotdot", forgejoCfg(fjProject("a", real1, "acme/app"), fjProject("b", filepath.Join(real2, "..", "real1"), "acme/other")), "path"},
		{"duplicate path via alias of missing leaf", forgejoCfg(fjProject("a", filepath.Join(real1, "sub"), "acme/app"), fjProject("b", filepath.Join(alias, "sub"), "acme/other")), "path"},
		{"repo case-sensitive distinct is allowed", forgejoCfg(fjProject("a", real1, "acme/app"), fjProject("b", real2, "acme/App")), ""},
		{"repo missing", forgejoCfg(fjProject("a", real1, "")), "forgejo.repo"},
		{"repo one segment", forgejoCfg(fjProject("a", real1, "app")), "owner/name"},
		{"repo three segments", forgejoCfg(fjProject("a", real1, "a/b/c")), "owner/name"},
		{"repo leading slash", forgejoCfg(fjProject("a", real1, "/acme/app")), "owner/name"},
		{"repo trailing slash", forgejoCfg(fjProject("a", real1, "acme/app/")), "owner/name"},
		{"repo whitespace", forgejoCfg(fjProject("a", real1, "acme/ app")), "owner/name"},
		{"repo padded", forgejoCfg(fjProject("a", real1, " acme/app")), "owner/name"},
		{"repo dot segment", forgejoCfg(fjProject("a", real1, "acme/..")), "owner/name"},
		{"repo .git suffix", forgejoCfg(fjProject("a", real1, "acme/app.git")), "owner/name"},
		{"repo url", forgejoCfg(fjProject("a", real1, "https://forgejo.example.test/acme/app")), "owner/name"},
		{"project path empty", forgejoCfg(fjProject("a", "", "acme/app")), "path is required"},
		{"project with github and forgejo", forgejoCfg(&ProjectConfig{Name: "a", Path: real1, GitHub: &ProjectGitHubConfig{Owner: "o", Repo: "r"}, Forgejo: &ProjectForgejoConfig{Repo: "acme/app"}}), "more than one forge"},
		{"project with gitlab and forgejo", forgejoCfg(&ProjectConfig{Name: "a", Path: real1, GitLab: &ProjectGitLabConfig{Project: "g/p"}, Forgejo: &ProjectForgejoConfig{Repo: "acme/app"}}), "more than one forge"},
		{"forgejo path shared with github project", forgejoCfg(
			&ProjectConfig{Name: "gh", Path: alias, GitHub: &ProjectGitHubConfig{Owner: "o", Repo: "r"}},
			fjProject("fj", real1, "acme/app")), "also used by"},
		{"forgejo path shared with gitlab project", forgejoCfg(
			&ProjectConfig{Name: "gl", Path: real1, GitLab: &ProjectGitLabConfig{Project: "g/p"}},
			fjProject("fj", real1, "acme/app")), "also used by"},
		{"project block without adapter token", mut(func(f *ForgejoConfig) { f.Token = "" }, fjProject("a", real1, "acme/app")), "adapters.forgejo.token"},
		{"project block without adapter at all", func() *Config {
			c := forgejoCfg(fjProject("a", real1, "acme/app"))
			c.Adapters.Forgejo = nil
			return c
		}(), "adapters.forgejo.token"},
		{"project block with disabled adapter and no token", mut(func(f *ForgejoConfig) { f.Enabled = false; f.Token = "" }, fjProject("a", real1, "acme/app")), "adapters.forgejo.token"},

		{"enabled with configured github app", func() *Config {
			c := forgejoCfg()
			c.Adapters.GitHub.App = &github.AppConfig{AppID: 1, InstallationID: 2, PrivateKeyPath: "/tmp/fake-key.pem"}
			return c
		}(), "github.app"},
		{"disabled with configured github app is fine", func() *Config {
			c := forgejoCfg()
			mut(noop)
			c.Adapters.Forgejo.Enabled = false
			c.Adapters.GitHub.App = &github.AppConfig{AppID: 1, InstallationID: 2, PrivateKeyPath: "/tmp/fake-key.pem"}
			return c
		}(), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.errContains == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.errContains) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.errContains)
			}
		})
	}
}

func TestForgejoNormalizedBaseURL(t *testing.T) {
	tests := map[string]string{
		"https://Forgejo.Example.test/":     "https://forgejo.example.test",
		"https://forgejo.example.test/git/": "https://forgejo.example.test/git",
		"http://127.0.0.1:3000":             "http://127.0.0.1:3000",
	}
	for in, want := range tests {
		got, err := (&ForgejoConfig{BaseURL: in}).NormalizedBaseURL()
		if err != nil || got != want {
			t.Errorf("NormalizedBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := (&ForgejoConfig{BaseURL: "http://forgejo.example.test"}).NormalizedBaseURL(); err == nil {
		t.Error("non-loopback http must error")
	}
}

func TestForgejoFindProjectByRepo(t *testing.T) {
	c := forgejoCfg(fjProject("a", "/srv/a", "acme/app"), fjProject("b", "/srv/b", "acme/App"))
	if p := c.FindProjectByForgejoRepo("acme/app"); p == nil || p.Name != "a" {
		t.Errorf("exact match failed: %v", p)
	}
	if p := c.FindProjectByForgejoRepo("acme/App"); p == nil || p.Name != "b" {
		t.Errorf("case-sensitive match failed: %v", p)
	}
	if p := c.FindProjectByForgejoRepo("ACME/APP"); p != nil {
		t.Errorf("unmapped repo must not match, got %v", p)
	}
	if p := c.FindProjectByForgejoRepo(""); p != nil {
		t.Errorf("empty repo must not match")
	}
}

func TestForgejoYAMLStrictKeys(t *testing.T) {
	load := func(t *testing.T, body string) (*Config, error) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return Load(path)
	}

	cfg, err := load(t, `
adapters:
  forgejo:
    enabled: true
    base_url: "https://forgejo.example.test"
    token: "test-forgejo-token"
    webhook_secret: "test-forgejo-webhook-secret"
projects:
  - name: app
    path: /srv/forgejo-app-does-not-exist
    forgejo:
      repo: "acme/app"
`)
	if err != nil {
		t.Fatalf("valid yaml: %v", err)
	}
	if !cfg.Adapters.Forgejo.Enabled || cfg.Adapters.Forgejo.ResolvedPilotLabel() != "pilot" {
		t.Errorf("unexpected adapter: %+v", cfg.Adapters.Forgejo)
	}
	if cfg.Projects[0].Forgejo == nil || cfg.Projects[0].Forgejo.Repo != "acme/app" {
		t.Errorf("project forgejo not bound: %+v", cfg.Projects[0].Forgejo)
	}

	_, err = load(t, `
adapters:
  forgejo:
    enabled: true
    base_url: "https://forgejo.example.test"
    token: "test-forgejo-token"
    webhook_secret: "test-forgejo-webhook-secret"
projects:
  - name: app
    path: /srv/forgejo-app-does-not-exist
    forgejo:
      repo: "acme/app"
      base_url: "https://other.example.test"
`)
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("per-project base_url must be rejected, got %v", err)
	}

	_, err = load(t, `
adapters:
  forgejo:
    enabled: true
    base_url: "https://forgejo.example.test"
    token: "test-forgejo-token"
    webhook_secret: "test-forgejo-webhook-secret"
    polling:
      enabled: true
`)
	if err == nil || !strings.Contains(err.Error(), "polling") {
		t.Fatalf("unknown adapter key must be rejected, got %v", err)
	}
}

// TestForgejoExampleConfig uncomments the example block in
// configs/pilot.example.yaml and checks it loads and validates.
func TestForgejoExampleConfig(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "pilot.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`).FindAllStringSubmatch(string(data), -1) {
		t.Setenv(m[1], "test-value")
	}
	lines := strings.Split(string(data), "\n")

	uncomment := func(start string) {
		t.Helper()
		idx := -1
		for i, l := range lines {
			if strings.TrimSpace(l) == start {
				idx = i
				break
			}
		}
		if idx < 0 {
			t.Fatalf("example config missing %q", start)
		}
		indent := lines[idx][:strings.Index(lines[idx], "#")]
		lines[idx] = indent + strings.TrimPrefix(strings.TrimSpace(lines[idx]), "# ")
		for j := idx + 1; j < len(lines); j++ {
			trimmed := strings.TrimSpace(lines[j])
			if !strings.HasPrefix(lines[j], indent+"#   ") || trimmed == "" {
				break
			}
			lines[j] = indent + strings.TrimPrefix(lines[j], indent+"# ")
		}
	}
	uncomment("# forgejo:") // adapters.forgejo (first occurrence)
	// The adapter block is uncommented now; the remaining "# forgejo:" is the
	// project-level block.
	second := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "# forgejo:" {
			second = i
			break
		}
	}
	if second < 0 {
		t.Fatal("example config missing project-level forgejo block")
	}
	indent := lines[second][:strings.Index(lines[second], "#")]
	lines[second] = indent + "forgejo:"
	for j := second + 1; j < len(lines) && strings.HasPrefix(lines[j], indent+"#   "); j++ {
		lines[j] = indent + strings.TrimPrefix(lines[j], indent+"# ")
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("example config with forgejo uncommented: %v", err)
	}
	fj := cfg.Adapters.Forgejo
	if fj == nil || fj.BaseURL == "" || fj.Token == "" || fj.WebhookSecret == "" {
		t.Fatalf("example forgejo block not bound: %+v", fj)
	}
}
