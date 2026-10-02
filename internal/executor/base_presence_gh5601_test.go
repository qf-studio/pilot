package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// GH-5601: non-GitHub (GitLab) projects must never be probed via the gh CLI.

func TestParseRemoteHost(t *testing.T) {
	tests := []struct{ url, want string }{
		{"https://gitlab.com/linear-invoices/linearinvoices-service.git", "gitlab.com"},
		{"https://oauth2:tok@gitlab.example.com:8443/a/b.git", "gitlab.example.com"},
		{"git@gitlab.com:linear-invoices/linearinvoices-client.git", "gitlab.com"},
		{"ssh://git@github.com/acme/widgets.git", "github.com"},
		{"https://github.com/acme/widgets", "github.com"},
		{"acme/widgets", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := parseRemoteHost(tt.url); got != tt.want {
			t.Errorf("parseRemoteHost(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestIsGitHubHost(t *testing.T) {
	for host, want := range map[string]bool{
		"github.com": true, "GitHub.com": true, "github.corp.example.com": true,
		"gitlab.com": false, "git.example.com": false,
	} {
		if got := isGitHubHost(host); got != want {
			t.Errorf("isGitHubHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newForgeProject builds a bare "origin" holding docker/entrypoint.sh on main
// and a clone whose configured origin URL is remoteURL (rewritten to the bare
// repo through url.<base>.insteadOf so fetch works offline).
func newForgeProject(t *testing.T, remoteURL string) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "init", "-q", "--bare", "-b", "main", bare)
	gitRun(t, seed, "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(seed, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "docker", "entrypoint.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "add", ".")
	gitRun(t, seed, "commit", "-q", "-m", "init")
	gitRun(t, seed, "push", "-q", bare, "main")
	gitRun(t, root, "clone", "-q", bare, proj)
	gitRun(t, proj, "remote", "set-url", "origin", remoteURL)
	gitRun(t, proj, "config", "url."+bare+".insteadOf", remoteURL)
	return proj
}

// failingGitHubProbe fails the test if the GitHub probe path is ever taken.
type failingGitHubProbe struct{ t *testing.T }

func (f failingGitHubProbe) IssueOrPRState(context.Context, string, string, int) (string, string, error) {
	f.t.Error("GitHub probe used for a non-GitHub project")
	return "", "", nil
}
func (f failingGitHubProbe) LinkedPRNumbers(context.Context, string, string, int) ([]int, error) {
	f.t.Error("GitHub probe used for a non-GitHub project")
	return nil, nil
}
func (f failingGitHubProbe) FileExistsOnDefaultBranch(context.Context, string, string, string) (bool, error) {
	f.t.Error("GitHub probe used for a non-GitHub project")
	return false, nil
}

func isLocalProbe(p BasePresenceProbe) bool {
	_, ok := p.(*localCheckoutBasePresenceProbe)
	return ok
}

func TestSelectBasePresenceProbe_ForgeResolution(t *testing.T) {
	ctx := context.Background()
	gl := newForgeProject(t, "https://gitlab.com/linear-invoices/linearinvoices-service.git")
	gh := newForgeProject(t, "https://github.com/acme/widgets.git")

	runner := &Runner{}
	registered := &fakeBasePresenceProbe{}
	runner.RegisterBasePresenceProbe("github:acme/widgets", registered)

	if p := selectBasePresenceProbe(ctx, runner, &Task{}, gl, "linear-invoices", "linearinvoices-service"); !isLocalProbe(p) {
		t.Errorf("GitLab project: got %T, want *localCheckoutBasePresenceProbe", p)
	}
	if p := selectBasePresenceProbe(ctx, runner, &Task{}, gh, "acme", "widgets"); p != BasePresenceProbe(registered) {
		t.Errorf("GitHub project: got %T, want the registered probe", p)
	}
	if p := selectBasePresenceProbe(ctx, runner, &Task{}, gh, "other", "repo"); p != BasePresenceProbe(ghCLIBasePresenceProbe{}) {
		t.Errorf("unregistered GitHub project: got %T, want ghCLIBasePresenceProbe", p)
	}
}

func TestCheckBasePresence_GitLabProject(t *testing.T) {
	proj := newForgeProject(t, "https://gitlab.com/linear-invoices/linearinvoices-service.git")
	runner := &Runner{}
	// A GitHub probe registered under the GitLab owner/repo must never be used.
	runner.RegisterBasePresenceProbe("github:linear-invoices/linearinvoices-service", failingGitHubProbe{t})

	task := &Task{ID: "LIN-QF-283", ProjectPath: proj, SourceAdapter: "linear"}

	hold, err := checkBasePresence(context.Background(), runner, task, proj, []int{7}, []string{"docker/entrypoint.sh"})
	if err != nil {
		t.Fatal(err)
	}
	if hold.Held {
		t.Errorf("existing path on GitLab main must not hold, got %+v", hold)
	}

	hold, err = checkBasePresence(context.Background(), runner, task, proj, nil, []string{"docker/entrypoint.sh", "src/test/mocks/server.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if !hold.Held {
		t.Error("genuinely missing path must still hold")
	}
}

func TestLocalCheckoutBasePresenceProbe_FetchFailureIsLookupError(t *testing.T) {
	proj := newForgeProject(t, "https://gitlab.com/acme/gone.git")
	gitRun(t, proj, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "does-not-exist.git"))
	probe := &localCheckoutBasePresenceProbe{dir: proj}
	exists, err := probe.FileExistsOnDefaultBranch(context.Background(), "", "", "docker/entrypoint.sh")
	if err == nil || exists {
		t.Errorf("unreachable origin must be a lookup error (fail-open upstream), got exists=%v err=%v", exists, err)
	}
}
