package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// GH-5608: a done context is a lookup error, never "path absent".
func TestLocalCheckoutBasePresenceProbe_CancelledContextIsLookupError(t *testing.T) {
	proj := newForgeProject(t, "https://gitlab.com/acme/widgets.git")

	// Cancel exactly between prepare (fetch + ref resolve) and cat-file by
	// cancelling from inside the fetch seam's follow-up: resolve once with a
	// live context, then probe with a context that is already done.
	probe := &localCheckoutBasePresenceProbe{dir: proj}
	if _, err := probe.prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exists, err := probe.FileExistsOnDefaultBranch(ctx, "", "", "does/not/exist.sh")
	if err == nil || exists {
		t.Errorf("cancelled context must be a lookup error, got exists=%v err=%v", exists, err)
	}

	// Through the checker: a lookup error fails open (no hold).
	hold := basePresenceChecker{probe: probe}.Check(ctx, "", "", nil, []string{"does/not/exist.sh"})
	if hold.Held {
		t.Errorf("cancelled context must not produce a hold, got %+v", hold)
	}

	// Cancelled before prepare: error, and nothing is cached for later calls.
	fresh := &localCheckoutBasePresenceProbe{dir: proj}
	if exists, err := fresh.FileExistsOnDefaultBranch(ctx, "", "", "docker/entrypoint.sh"); err == nil || exists {
		t.Errorf("cancelled prepare must be a lookup error, got exists=%v err=%v", exists, err)
	}
	if exists, err := fresh.FileExistsOnDefaultBranch(context.Background(), "", "", "docker/entrypoint.sh"); err != nil || !exists {
		t.Errorf("cancellation must not poison the cache, got exists=%v err=%v", exists, err)
	}
}

// GH-5608: two checks on the same project within the TTL fetch once.
func TestCheckBasePresence_FetchCachedPerProject(t *testing.T) {
	proj := newForgeProject(t, "https://gitlab.com/acme/widgets.git")
	var fetches int
	orig := localProbeFetchOrigin
	localProbeFetchOrigin = func(ctx context.Context, dir string) error {
		fetches++
		return orig(ctx, dir)
	}
	t.Cleanup(func() { localProbeFetchOrigin = orig })

	runner := &Runner{}
	task := &Task{ID: "LIN-1", SourceAdapter: "linear"}
	for i := 0; i < 3; i++ {
		hold, err := checkBasePresence(context.Background(), runner, task, proj, nil, []string{"docker/entrypoint.sh"})
		if err != nil || hold.Held {
			t.Fatalf("tick %d: hold=%+v err=%v", i, hold, err)
		}
	}
	if fetches != 1 {
		t.Errorf("fetches = %d, want 1 across 3 checks within the TTL", fetches)
	}

	// Expired entry refetches.
	e := runner.baseRefCache.entry(proj)
	e.at = e.at.Add(-2 * baseRefCacheTTL)
	if _, err := checkBasePresence(context.Background(), runner, task, proj, nil, []string{"docker/entrypoint.sh"}); err != nil {
		t.Fatal(err)
	}
	if fetches != 2 {
		t.Errorf("fetches = %d after TTL expiry, want 2", fetches)
	}
}

// GH-5608: an unresolvable default branch is a lookup error (fail open).
func TestCheckBasePresence_UnresolvableDefaultBranchFailsOpen(t *testing.T) {
	proj := newForgeProject(t, "https://gitlab.com/acme/widgets.git")
	// Rename the only branch so neither origin/HEAD, origin/main nor origin/master resolve.
	bare := filepath.Join(filepath.Dir(proj), "origin.git")
	gitRun(t, bare, "branch", "-m", "main", "trunk")
	gitRun(t, bare, "symbolic-ref", "HEAD", "refs/heads/unborn") // fetch cannot re-create origin/HEAD
	gitRun(t, proj, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	gitRun(t, proj, "update-ref", "-d", "refs/remotes/origin/main")

	hold, err := checkBasePresence(context.Background(), &Runner{}, &Task{SourceAdapter: "linear"}, proj, nil, []string{"docker/entrypoint.sh", "nope/missing.sh"})
	if err != nil {
		t.Fatal(err)
	}
	if hold.Held {
		t.Errorf("unresolvable default branch must fail open, got %+v", hold)
	}
	probe := &localCheckoutBasePresenceProbe{dir: proj}
	if _, err := probe.FileExistsOnDefaultBranch(context.Background(), "", "", "docker/entrypoint.sh"); err == nil {
		t.Error("want lookup error for unresolvable default branch")
	}
}

// GH-5608: an unparseable origin host (file://, bare path) uses the local
// probe, never the gh fallback; an unregistered non-GitHub project never
// reaches the gh probe.
func TestSelectBasePresenceProbe_UnparseableOriginUsesLocalProbe(t *testing.T) {
	ctx := context.Background()
	for _, url := range []string{"file:///srv/mirrors/widgets.git", "/srv/mirrors/widgets.git"} {
		proj := newForgeProject(t, url)
		if p := selectBasePresenceProbe(ctx, &Runner{}, &Task{}, proj, "x", "y"); !isLocalProbe(p) {
			t.Errorf("origin %q: got %T, want *localCheckoutBasePresenceProbe", url, p)
		}
	}
	// No origin at all keeps the gh fallback.
	noOrigin := t.TempDir()
	gitRun(t, noOrigin, "init", "-q", "-b", "main")
	if p := selectBasePresenceProbe(ctx, &Runner{}, &Task{}, noOrigin, "x", "y"); p != BasePresenceProbe(ghCLIBasePresenceProbe{}) {
		t.Errorf("no origin: got %T, want ghCLIBasePresenceProbe", p)
	}
}

func TestCheckBasePresence_UnregisteredNonGitHubNeverCallsGhProbe(t *testing.T) {
	proj := newForgeProject(t, "https://gitlab.com/acme/widgets.git")
	runner := &Runner{}
	// Nothing registered for this repo; a probe registered under the same key
	// shape must not be reached, and the gh fallback must not be selected.
	if p := selectBasePresenceProbe(context.Background(), runner, &Task{SourceAdapter: "linear"}, proj, "acme", "widgets"); p == BasePresenceProbe(ghCLIBasePresenceProbe{}) {
		t.Fatal("unregistered non-GitHub project selected the gh probe")
	}
	hold, err := checkBasePresence(context.Background(), runner, &Task{SourceAdapter: "linear"}, proj, nil, []string{"nope/missing.sh"})
	if err != nil {
		t.Fatal(err)
	}
	if !hold.Held || !strings.Contains(hold.Reason, "does not release this hold for a non-GitHub task") {
		t.Errorf("want a hold carrying the release-path note, got %+v", hold)
	}

	// A GitHub-adapter task's reason is unchanged.
	hold, _ = checkBasePresence(context.Background(), runner, &Task{SourceAdapter: "github"}, proj, nil, []string{"nope/missing.sh"})
	if !hold.Held || strings.Contains(hold.Reason, "non-GitHub") {
		t.Errorf("github-adapter reason must not carry the note, got %+v", hold)
	}
}
