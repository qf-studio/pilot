package executor

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
	"github.com/qf-studio/pilot/internal/testutil"
)

// tokenMarkerProgram writes a shell program that appends its name to marker
// when PILOT_GIT_TOKEN is in its environment; it reads its stdin (a hook gets
// the refs there) and exits 0, so git goes on as if it had run.
func tokenMarkerProgram(t *testing.T, path, marker string) string {
	t.Helper()
	body := fmt.Sprintf("#!/bin/sh\ncat >/dev/null 2>&1 || true\nif [ -n \"$PILOT_GIT_TOKEN\" ]; then echo \"$0\" >> '%s'; fi\nexit 0\n", marker)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil { //nolint:gosec // program must be executable
		t.Fatal(err)
	}
	return path
}

// assertNoTokenMarker fails when marker exists: the program of what ran.
func assertNoTokenMarker(t *testing.T, what, marker string) {
	t.Helper()
	if raw, err := os.ReadFile(marker); err == nil {
		t.Errorf("the %s of the repository ran with the token: %q", what, raw)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

// optionEmptyRepoWithRemote is a repository with a bare remote and the option
// empty, with the secret gate state reset; a fake token provider is installed.
func optionEmptyRepoWithRemote(t *testing.T) (repo, remote string) {
	t.Helper()
	secretpatterns.ResetSecretValuesForTest()
	ResetSecretGateConfigForTest()
	t.Cleanup(func() {
		secretpatterns.ResetSecretValuesForTest()
		ResetSecretGateConfigForTest()
	})
	setRunAsStateForTest(t, nil, "")
	SetGitCredentialProvider(func(context.Context) (string, error) { return testutil.FakeGitHubToken, nil })
	t.Cleanup(func() { SetGitCredentialProvider(nil) })
	repo, remote = setupTestRepoWithRemote(t)
	t.Cleanup(func() { _ = os.RemoveAll(repo); _ = os.RemoveAll(remote) })
	return repo, remote
}

// TestEachTokenCommandRunsNoRepositoryProgram (TASK-651): with the option
// empty, a reference-transaction hook, a core.fsmonitor program and a
// core.alternateRefsCommand program (with an alternates file) of the
// repository run for none of the fetch and pull commands that carry the
// token. The remote is one commit ahead before each call, so each fetch
// updates a ref and asks the alternate for its refs.
// mutation pin: withGitCredentials without the hooks-off arguments -> this test fails
func TestEachTokenCommandRunsNoRepositoryProgram(t *testing.T) {
	repo, remote := optionEmptyRepoWithRemote(t)
	g := NewGitOperations(repo)

	ahead := filepath.Join(t.TempDir(), "ahead")
	wsGit(t, filepath.Dir(ahead), "clone", "-q", remote, ahead)
	wsGit(t, ahead, "config", "user.email", "test@example.com")
	wsGit(t, ahead, "config", "user.name", "Test User")
	n := 0
	advance := func() {
		n++
		commitFile(t, ahead, "ahead.txt", fmt.Sprintf("commit %d", n))
		wsGit(t, ahead, "push", "-q", "origin", "HEAD:main")
	}

	alternate := filepath.Join(t.TempDir(), "alternate.git")
	wsGit(t, filepath.Dir(alternate), "init", "-q", "--bare", alternate)
	if err := os.WriteFile(filepath.Join(repo, ".git", "objects", "info", "alternates"), []byte(filepath.Join(alternate, "objects")+"\n"), 0o644); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
	dir := t.TempDir()
	markers := map[string]string{}
	for _, what := range []string{"reference-transaction hook", "core.fsmonitor program", "core.alternateRefsCommand program"} {
		markers[what] = filepath.Join(dir, strings.ReplaceAll(what, " ", "-")+".marker")
	}
	hooks := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil { //nolint:gosec // test dir
		t.Fatal(err)
	}
	tokenMarkerProgram(t, filepath.Join(hooks, "reference-transaction"), markers["reference-transaction hook"])
	wsGit(t, repo, "config", "core.fsmonitor", tokenMarkerProgram(t, filepath.Join(dir, "fsmonitor"), markers["core.fsmonitor program"]))
	wsGit(t, repo, "config", "core.alternateRefsCommand", tokenMarkerProgram(t, filepath.Join(dir, "alternate-refs"), markers["core.alternateRefsCommand program"]))

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	advance()
	if _, _, err := g.GetDiffAgainstOrigin(ctx, "main"); err != nil {
		t.Errorf("GetDiffAgainstOrigin: %v", err)
	}
	advance()
	if _, err := g.CommitsBehindMain(ctx, "main"); err != nil {
		t.Errorf("CommitsBehindMain: %v", err)
	}
	advance()
	if err := g.Pull(ctx, "main"); err != nil {
		t.Errorf("Pull: %v", err)
	}
	advance()
	if _, err := g.CountNewCommitsAgainstOrigin(ctx, "main"); err != nil {
		t.Errorf("CountNewCommitsAgainstOrigin: %v", err)
	}
	if got, want := wsGit(t, repo, "rev-parse", "origin/main"), wsGit(t, ahead, "rev-parse", "HEAD"); got != want {
		t.Errorf("origin/main = %s after the fetches, want the head of the remote %s", got, want)
	}
	for what, marker := range markers {
		assertNoTokenMarker(t, what, marker)
	}

	// The programs do run for a pull with the token and no -c argument: the
	// test sees each of them.
	advance()
	pull := exec.Command("git", "-C", repo, "pull", "-q", "origin", "main")
	pull.Env = append(os.Environ(), "PILOT_GIT_TOKEN="+testutil.FakeGitHubToken)
	if out, err := pull.CombinedOutput(); err != nil {
		t.Fatalf("control pull: %v: %s", err, out)
	}
	for what, marker := range markers {
		if _, err := os.Stat(marker); err != nil {
			t.Errorf("control: the %s did not run for a pull with the token: %v", what, err)
		}
	}
}

// TestTokenCommandPushRunsNoSigningProgram (TASK-651): with push.gpgSign=true
// and a gpg.program in the repository config and a remote that takes a signed
// push (receive.certNonceSeed), the push runs no program: pushArgs has
// --no-signed.
// mutation pin: pushArgs without --no-signed -> this test fails
func TestTokenCommandPushRunsNoSigningProgram(t *testing.T) {
	repo, remote := optionEmptyRepoWithRemote(t)
	g := NewGitOperations(repo)
	wsGit(t, remote, "config", "receive.certNonceSeed", "pilot-test-seed")
	marker := filepath.Join(t.TempDir(), "gpg.marker")
	wsGit(t, repo, "config", "push.gpgSign", "true")
	wsGit(t, repo, "config", "gpg.program", tokenMarkerProgram(t, filepath.Join(t.TempDir(), "gpg"), marker))

	branch := "pilot/GH-1"
	wsGit(t, repo, "checkout", "-q", "-b", branch)
	head := commitFile(t, repo, "a.txt", "one")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := g.Push(ctx, branch, handOverTestTask); err != nil {
		t.Errorf("Push: %v", err)
	}
	assertNoTokenMarker(t, "gpg.program", marker)
	if got := wsGit(t, remote, "rev-parse", "refs/heads/"+branch); got != head {
		t.Errorf("remote head = %s, want %s", got, head)
	}

	// The program does run for a push with the token and no --no-signed.
	commitFile(t, repo, "b.txt", "two")
	push := exec.Command("git", "-C", repo, "push", "-q", "origin", "HEAD:refs/heads/control")
	push.Env = append(os.Environ(), "PILOT_GIT_TOKEN="+testutil.FakeGitHubToken)
	_ = push.Run() // the program signs nothing, so the push fails
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("control: the gpg.program did not run for a signed push: %v", err)
	}
}

// tokenCommandCallSites is the number of calls of withGitCredentials per
// function of git.go and worktree.go. Each one gets the hooks-off arguments
// from withGitCredentials; TestEachTokenCommandRunsNoRepositoryProgram runs
// GetDiffAgainstOrigin, CommitsBehindMain, Pull and the fetch of the count
// (remoteGitCmd), TestTokenCommandPushRunsNoSigningProgram the push.
var tokenCommandCallSites = map[string]int{
	"git.go remoteGitCmd":                      1,
	"git.go resolveGuardBaseRef":               1,
	"git.go Push":                              1,
	"git.go PushForceWithLease":                1,
	"git.go pushHandOver":                      1,
	"git.go PushToMain":                        1,
	"git.go GetDiffAgainstOrigin":              1,
	"git.go Pull":                              1,
	"git.go CommitsBehindMain":                 1,
	"git.go ChangedFilesAgainstOrigin":         1,
	"git.go ResolveFixContinuationBaseRef":     2,
	"worktree.go preparePooledWorktree":        1,
	"worktree.go fetchOriginBranchForWorktree": 1,
	"worktree.go VerifyRemoteAccess":           1,
}

// tokenCommandCallSiteCount is the sum of tokenCommandCallSites.
const tokenCommandCallSiteCount = 15

// TestTokenCommandCallSitesAreKnown reads git.go and worktree.go: each call of
// withGitCredentials is in tokenCommandCallSites and the count is
// tokenCommandCallSiteCount, so a new call site fails this test until its
// author adds a case.
func TestTokenCommandCallSitesAreKnown(t *testing.T) {
	got := map[string]int{}
	total := 0
	for _, file := range []string{"git.go", "worktree.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "withGitCredentials" {
						got[file+" "+fn.Name.Name]++
						total++
					}
				}
				return true
			})
		}
	}
	want := 0
	for _, n := range tokenCommandCallSites {
		want += n
	}
	if want != tokenCommandCallSiteCount {
		t.Fatalf("tokenCommandCallSites sums to %d, want %d", want, tokenCommandCallSiteCount)
	}
	if total != tokenCommandCallSiteCount {
		t.Errorf("%d calls of withGitCredentials in git.go and worktree.go, want %d: add a case for the new call site to TestEachTokenCommandRunsNoRepositoryProgram", total, tokenCommandCallSiteCount)
	}
	for site, n := range got {
		if tokenCommandCallSites[site] != n {
			t.Errorf("%s: %d calls of withGitCredentials, want %d", site, n, tokenCommandCallSites[site])
		}
	}
	for site, n := range tokenCommandCallSites {
		if got[site] != n {
			t.Errorf("%s: %d calls of withGitCredentials found, want %d", site, got[site], n)
		}
	}
}
