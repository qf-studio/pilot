package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultForgejoPilotLabel is the issue label Forgejo intake looks for when
// adapters.forgejo.pilot_label is empty.
const DefaultForgejoPilotLabel = "pilot"

// ForgejoConfig configures the single Forgejo instance Pilot talks to
// (TASK-657). The MVP supports exactly one explicit instance, identified by
// its normalized base_url; there is no default instance and no polling block.
//
// The config package owns this type on purpose: it must not import the
// forgejo adapter package, so the two evolve in parallel.
//
// token and webhook_secret are registered as secrets by the existing
// key-name matcher in cmd/pilot/secret_values.go (secretKeyPattern matches
// "token" and "secret"), so no per-adapter registration site is needed.
type ForgejoConfig struct {
	Enabled       bool   `yaml:"enabled"`
	BaseURL       string `yaml:"base_url"`
	Token         string `yaml:"token"`
	WebhookSecret string `yaml:"webhook_secret"`
	// PilotLabel is the issue label that marks work for Pilot. Empty means
	// DefaultForgejoPilotLabel; use ResolvedPilotLabel to read it.
	PilotLabel string `yaml:"pilot_label"`
}

// ProjectForgejoConfig marks a project's repository as Forgejo-hosted. It
// carries only the repo (owner/name): the instance is the global
// adapters.forgejo.base_url and there is no per-project override. The set of
// these blocks is the trusted repo-to-project map.
type ProjectForgejoConfig struct {
	Repo string `yaml:"repo"`
}

// DefaultForgejoConfig returns the disabled default adapter config.
func DefaultForgejoConfig() *ForgejoConfig {
	return &ForgejoConfig{PilotLabel: DefaultForgejoPilotLabel}
}

// UnmarshalYAML rejects unknown keys (for example a polling block, which does
// not exist by design) instead of silently dropping them.
func (f *ForgejoConfig) UnmarshalYAML(n *yaml.Node) error {
	if err := rejectUnknownYAMLKeys(n, "adapters.forgejo", "enabled", "base_url", "token", "webhook_secret", "pilot_label"); err != nil {
		return err
	}
	type plain ForgejoConfig
	return n.Decode((*plain)(f))
}

// UnmarshalYAML rejects unknown keys, notably a per-project base_url.
func (p *ProjectForgejoConfig) UnmarshalYAML(n *yaml.Node) error {
	if err := rejectUnknownYAMLKeys(n, "projects[].forgejo", "repo"); err != nil {
		return err
	}
	type plain ProjectForgejoConfig
	return n.Decode((*plain)(p))
}

func rejectUnknownYAMLKeys(n *yaml.Node, where string, allowed ...string) error {
	if n.Kind != yaml.MappingNode {
		return nil // let the regular decoder report the type error
	}
	ok := make(map[string]bool, len(allowed))
	for _, k := range allowed {
		ok[k] = true
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if key := n.Content[i].Value; !ok[key] {
			return fmt.Errorf("%s: unknown key %q (line %d); allowed keys: %s", where, key, n.Content[i].Line, strings.Join(allowed, ", "))
		}
	}
	return nil
}

// ResolvedPilotLabel returns the configured label, or the default when empty.
// Safe on a nil receiver.
func (f *ForgejoConfig) ResolvedPilotLabel() string {
	if f == nil || strings.TrimSpace(f.PilotLabel) == "" {
		return DefaultForgejoPilotLabel
	}
	return strings.TrimSpace(f.PilotLabel)
}

// isLoopbackHost reports whether host is a literal loopback name: localhost,
// 127.0.0.1 or ::1. Lookalike names (localhost.example.test) do not count.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback))
}

// NormalizedBaseURL validates BaseURL and returns its canonical form: lower
// case scheme and host, no trailing slash. https is required; plain http is
// accepted only for a literal loopback host (local installation and tests).
// Userinfo, query and fragment are rejected.
func (f *ForgejoConfig) NormalizedBaseURL() (string, error) {
	raw := f.BaseURL
	if raw == "" {
		return "", fmt.Errorf("adapters.forgejo.base_url is required")
	}
	if strings.IndexFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("adapters.forgejo.base_url must not contain whitespace or control characters")
	}
	if strings.Contains(raw, "#") {
		return "", fmt.Errorf("adapters.forgejo.base_url must not contain a fragment")
	}
	if strings.Contains(raw, "?") {
		return "", fmt.Errorf("adapters.forgejo.base_url must not contain a query")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("adapters.forgejo.base_url is not a valid URL")
	}
	if u.User != nil {
		return "", fmt.Errorf("adapters.forgejo.base_url must not contain credentials")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return "", fmt.Errorf("adapters.forgejo.base_url must use https (http is allowed only for localhost, 127.0.0.1 or ::1)")
		}
	default:
		return "", fmt.Errorf("adapters.forgejo.base_url must be an https URL")
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("adapters.forgejo.base_url must include a host")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

// forgejoRepoSegment matches one owner or repository name segment.
var forgejoRepoSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validForgejoRepo reports whether s is a canonical "owner/name" repo path:
// exactly two segments of [A-Za-z0-9._-], neither "." nor ".." and the name
// not ending in ".git". Matching elsewhere is exact and case-sensitive.
func validForgejoRepo(s string) bool {
	owner, name, ok := strings.Cut(s, "/")
	if !ok {
		return false
	}
	for _, seg := range []string{owner, name} {
		if !forgejoRepoSegment.MatchString(seg) || seg == "." || seg == ".." {
			return false
		}
	}
	return !strings.HasSuffix(name, ".git")
}

// FindProjectByForgejoRepo returns the project mapped to the exact
// (case-sensitive) owner/name repo, or nil. An unmapped repo is never
// admitted.
func (c *Config) FindProjectByForgejoRepo(repo string) *ProjectConfig {
	if repo == "" {
		return nil
	}
	for _, p := range c.Projects {
		if p != nil && p.Forgejo != nil && p.Forgejo.Repo == repo {
			return p
		}
	}
	return nil
}

// canonicalProjectPath returns an absolute, symlink-resolved form of p so
// aliases of one directory compare equal. When the path (or its tail) does
// not exist yet, the deepest existing ancestor is resolved and the remainder
// is appended.
func canonicalProjectPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	rest := ""
	cur := abs
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// validateForgejo checks the adapters.forgejo block and every
// projects[].forgejo block (TASK-657):
//   - an enabled adapter needs a valid explicit base_url, token and
//     webhook_secret and may not coexist with GitHub App auth, which the
//     daemon installs process-wide;
//   - a project block needs a canonical owner/name repo, the adapter token,
//     a path, and no other forge block on the same project;
//   - each repo maps to exactly one project and Forgejo-mapped projects have
//     unique canonical paths (symlink aliases included) that no github- or
//     gitlab-mapped project shares.
func (c *Config) validateForgejo() error {
	var fj *ForgejoConfig
	if c.Adapters != nil {
		fj = c.Adapters.Forgejo
	}

	if fj != nil && fj.Enabled {
		if _, err := fj.NormalizedBaseURL(); err != nil {
			return err
		}
		if strings.TrimSpace(fj.Token) == "" {
			return fmt.Errorf("adapters.forgejo.token is required when adapters.forgejo.enabled is true")
		}
		if strings.TrimSpace(fj.WebhookSecret) == "" {
			return fmt.Errorf("adapters.forgejo.webhook_secret is required when adapters.forgejo.enabled is true")
		}
		if c.Adapters.GitHub != nil && c.Adapters.GitHub.App != nil {
			return fmt.Errorf("adapters.forgejo cannot be enabled together with adapters.github.app: the daemon installs GitHub App credentials process-wide")
		}
	}

	repos := map[string]string{} // repo -> project label
	paths := map[string]string{} // canonical path -> project label
	for i, p := range c.Projects {
		if p == nil || p.Forgejo == nil {
			continue
		}
		label := p.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i)
		}
		if p.Forgejo.Repo == "" {
			return fmt.Errorf("projects[%d] (%s): forgejo.repo is required", i, label)
		}
		if !validForgejoRepo(p.Forgejo.Repo) {
			return fmt.Errorf("projects[%d] (%s): forgejo.repo must be owner/name, got %q", i, label, p.Forgejo.Repo)
		}
		if p.GitHub != nil || p.GitLab != nil {
			return fmt.Errorf("projects[%d] (%s): a project may not declare more than one forge block (github, gitlab, forgejo)", i, label)
		}
		if fj == nil || strings.TrimSpace(fj.Token) == "" {
			return fmt.Errorf("projects[%d] (%s): forgejo: block requires adapters.forgejo.token to be set", i, label)
		}
		if strings.TrimSpace(p.Path) == "" {
			return fmt.Errorf("projects[%d] (%s): path is required for a forgejo project", i, label)
		}
		if prev, dup := repos[p.Forgejo.Repo]; dup {
			return fmt.Errorf("projects[%d] (%s): forgejo repo %q is mapped more than once (also %s)", i, label, p.Forgejo.Repo, prev)
		}
		repos[p.Forgejo.Repo] = label
		cp := canonicalProjectPath(p.Path)
		if prev, dup := paths[cp]; dup {
			return fmt.Errorf("projects[%d] (%s): path %q resolves to the same directory as forgejo project %s", i, label, p.Path, prev)
		}
		paths[cp] = label
	}

	// A directory mapped to Forgejo must not also be another forge's project.
	for i, p := range c.Projects {
		if p == nil || p.Forgejo != nil || (p.GitHub == nil && p.GitLab == nil) || strings.TrimSpace(p.Path) == "" {
			continue
		}
		if prev, clash := paths[canonicalProjectPath(p.Path)]; clash {
			name := p.Name
			if name == "" {
				name = fmt.Sprintf("#%d", i)
			}
			return fmt.Errorf("projects[%d] (%s): path %q is also used by forgejo project %s", i, name, p.Path, prev)
		}
	}
	return nil
}
