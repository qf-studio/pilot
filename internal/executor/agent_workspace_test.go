package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TASK-651 (design 4.2): the agent workspace of a task run, its cleanup, the
// sweeps, the route rule of the git constructor and the workspace part of the
// preflight. No test needs root: sudo is a mock script that runs the command
// as the present user.

// mockSudoAgent puts a fake sudo first on PATH that appends its argv (one line
// per call) to the returned file and runs the command after "--" as the
// present user. The two preflight probes have no "--": the terminal probe
// prints "?", `true` exits 0. The daemon's repository is unreadable for the
// agent side: a command with deny in its arguments or working directory
// fails with "permission denied", as it would for the agent user. extra is
// shell code that runs before the command (its arguments are "$@").
func mockSudoAgent(t *testing.T, deny, extra string) (logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock sudo is a shell script")
	}
	dir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "sudo-calls")
	body := fmt.Sprintf(`#!/bin/sh
echo "$*" >> '%[1]s'
case "$*" in
*"ps -o tty= -p"*) echo "?"; exit 0 ;;
esac
dd=0
for a in "$@"; do [ "$a" = "--" ] && dd=1; done
[ $dd = 0 ] && exit 0
while [ $# -gt 0 ]; do
  a=$1
  shift
  [ "$a" = "--" ] && break
done
deny='%[2]s'
if [ -n "$deny" ]; then
  case "$(pwd)/ $*" in
  *"$deny"*) echo "mock sudo: $deny: permission denied" >&2; exit 1 ;;
  esac
fi
%[3]s
exec "$@"
`, logPath, deny, extra)
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(body), 0o755); err != nil { //nolint:gosec // mock must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func sudoCalls(t *testing.T, logPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// captureLog sends the default logger to a buffer for one test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// gitOut runs git as the test (not through the constructor) and returns the
// trimmed output.
func wsGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitTestIdentity is the commit identity of the test commits: a CI runner has
// no global one, and git fails there with "empty ident name" (GH-6658).
var gitTestIdentity = []string{"-c", "user.email=t@example.com", "-c", "user.name=T"}

// gitTestCommitIn runs git commit with args in dir under gitTestIdentity, for
// a fixture with no *testing.T such as a backend that commits (GH-6663).
func gitTestCommitIn(dir string, args ...string) error {
	cmd := exec.Command("git", append(append(append([]string{}, gitTestIdentity...), "commit"), args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit %v in %s: %v: %s", args, dir, err, out)
	}
	return nil
}

func gitTestCommit(t *testing.T, dir, msg string) string {
	t.Helper()
	if err := gitTestCommitIn(dir, "-q", "--allow-empty", "-m", msg); err != nil {
		t.Fatal(err)
	}
	return wsGit(t, dir, "rev-parse", "HEAD")
}

// agentWorkspaceEnv is a daemon repository with a bare origin, a workspace
// root, the option set for that root and the mock sudo, for which the daemon
// repository is unreadable.
type agentWorkspaceEnv struct {
	repo, remote, root string
	sudoLog            string
}

func newAgentWorkspaceEnv(t *testing.T) *agentWorkspaceEnv {
	t.Helper()
	stubRunAsGOOS(t)
	repo, remote := setupTestRepoWithRemote(t)
	t.Cleanup(func() { _ = os.RemoveAll(repo); _ = os.RemoveAll(remote) })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &agentWorkspaceEnv{repo: repo, remote: remote, root: root}
	e.sudoLog = mockSudoAgent(t, repo, "")
	setRunAsStateForTest(t, gitRouteSettings(t, root), "")
	return e
}

// rootEntries lists the names in the workspace root.
func (e *agentWorkspaceEnv) rootEntries(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(e.root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

// assertNoTempRef checks that the daemon repository holds no ref under
// refs/pilot/ws-base/.
func (e *agentWorkspaceEnv) assertNoTempRef(t *testing.T) {
	t.Helper()
	if refs := wsGit(t, e.repo, "for-each-ref", agentWorkspaceBaseRefPrefix); refs != "" {
		t.Errorf("temporary base ref left in the daemon repository: %s", refs)
	}
}

func TestAgentWorkspaceMockDeniesTheDaemonRepository(t *testing.T) {
	e := newAgentWorkspaceEnv(t)
	ws := workspaceDir(t, e.root)
	wsGit(t, ws, "init", "-q")
	cmd := gitCommandIn(context.Background(), ws, "fetch", e.repo, "main")
	if _, ok := agentCmdUser(cmd); !ok {
		t.Fatalf("not the agent route: %q", cmd.Args)
	}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "permission denied") {
		t.Fatalf("a fetch from the daemon repository on the agent side: err=%v out=%s, want permission denied", err, out)
	}
}

func TestCreateAgentWorkspaceBaseForms(t *testing.T) {
	tests := []struct {
		name string
		// setup prepares the daemon repository and returns the base ref to
		// pass, the commit the task branch must be at and the task branch.
		setup func(t *testing.T, e *agentWorkspaceEnv) (baseRef, wantSHA, branch string)
	}{
		{"empty base: fresh fetch of origin/main", func(t *testing.T, e *agentWorkspaceEnv) (string, string, string) {
			return "", wsGit(t, e.repo, "rev-parse", "HEAD"), "pilot/GH-1"
		}},
		{"origin/BRANCH", func(t *testing.T, e *agentWorkspaceEnv) (string, string, string) {
			return "origin/main", wsGit(t, e.repo, "rev-parse", "origin/main"), "pilot/GH-1"
		}},
		{"commit SHA only FETCH_HEAD holds", func(t *testing.T, e *agentWorkspaceEnv) (string, string, string) {
			sha := pushSideCommit(t, e)
			return sha, sha, "pilot/GH-1"
		}},
		{"the text FETCH_HEAD", func(t *testing.T, e *agentWorkspaceEnv) (string, string, string) {
			sha := pushSideCommit(t, e)
			return "FETCH_HEAD", sha, "pilot/GH-1"
		}},
		{"fix continuation: head of the task branch on origin", func(t *testing.T, e *agentWorkspaceEnv) (string, string, string) {
			branch := "pilot/GH-77"
			other := cloneRemote(t, e.remote)
			wsGit(t, other, "checkout", "-q", "-b", branch)
			sha := gitTestCommit(t, other, "fix work")
			wsGit(t, other, "push", "-q", "origin", branch)
			base := NewGitOperations(e.repo).ResolveFixContinuationBaseRef(context.Background(), branch, "")
			if base != "origin/"+branch {
				t.Fatalf("ResolveFixContinuationBaseRef = %q", base)
			}
			return base, sha, branch
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentWorkspaceEnv(t)
			baseRef, wantSHA, branch := tc.setup(t, e)
			m := NewWorktreeManager(e.repo)
			m.SetBaseBranch("main")
			ws, cleanup, err := m.createAgentWorkspace(context.Background(), "GH-1", branch, baseRef)
			if err != nil {
				t.Fatalf("createAgentWorkspace: %v", err)
			}
			project, err := workspaceProjectID(e.repo)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Dir(ws) != e.root || !regexp.MustCompile(`^pilot-ws-`+project+`-GH-1-[0-9]+$`).MatchString(filepath.Base(ws)) {
				t.Errorf("workspace %s is not root/pilot-ws-%s-GH-1-NANOSECONDS", ws, project)
			}
			if got := wsGit(t, ws, "rev-parse", "refs/heads/"+branch); got != wantSHA {
				t.Errorf("task branch at %s, want %s", got, wantSHA)
			}
			if got := wsGit(t, ws, "symbolic-ref", "HEAD"); got != "refs/heads/"+branch {
				t.Errorf("HEAD = %s, want the task branch", got)
			}
			if _, err := os.Lstat(filepath.Join(ws, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
				t.Errorf("alternates file present: %v", err)
			}
			if refs := wsGit(t, ws, "for-each-ref", "--format=%(refname) %(objectname)"); refs != "refs/heads/"+branch+" "+wantSHA+"\nrefs/remotes/origin/main "+wantSHA {
				t.Errorf("refs = %q, want only the task branch and origin/main, both at the base", refs)
			}
			if got := wsGit(t, ws, "remote", "get-url", "origin"); got != e.remote {
				t.Errorf("origin = %q, want %q", got, e.remote)
			}
			if names := e.rootEntries(t); len(names) != 1 || names[0] != filepath.Base(ws) {
				t.Errorf("workspace root holds %q, want only the workspace (no bundle file)", names)
			}
			e.assertNoTempRef(t)
			// Every step on the workspace side went through sudo.
			if calls := sudoCalls(t, e.sudoLog); len(calls) == 0 {
				t.Error("no command ran as the agent user")
			}
			if exists := gitBranchExists(t, e.repo, branch); exists && branch == "pilot/GH-1" {
				t.Errorf("the daemon repository got the task branch %s", branch)
			}

			cleanup()
			cleanup() // safe twice
			if _, err := os.Stat(ws); !os.IsNotExist(err) {
				t.Errorf("workspace still present after cleanup: %v", err)
			}
		})
	}
}

// pushSideCommit pushes a new commit to branch side of the remote and fetches
// it into the daemon repository by path, so that only FETCH_HEAD holds it.
func pushSideCommit(t *testing.T, e *agentWorkspaceEnv) string {
	t.Helper()
	other := cloneRemote(t, e.remote)
	wsGit(t, other, "checkout", "-q", "-b", "side")
	sha := gitTestCommit(t, other, "side commit")
	wsGit(t, other, "push", "-q", "origin", "side")
	wsGit(t, e.repo, "fetch", "-q", e.remote, "side")
	if refs := wsGit(t, e.repo, "for-each-ref", "--contains", sha); refs != "" {
		t.Fatalf("a ref holds the side commit: %s", refs)
	}
	return sha
}

func cloneRemote(t *testing.T, remote string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "other")
	if out, err := exec.Command("git", "clone", "-q", remote, dir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	return dir
}

func gitBranchExists(t *testing.T, repo, branch string) bool {
	t.Helper()
	return exec.Command("git", "-C", repo, "rev-parse", "--verify", "-q", "refs/heads/"+branch).Run() == nil
}

func TestCreateAgentWorkspaceOriginWithoutCredential(t *testing.T) {
	tests := []struct{ origin, want string }{
		{"https://x-access-token:fake-secret@example.invalid/org/repo.git", "https://example.invalid/org/repo.git"},
		{"https://fake-user@example.invalid/org/repo.git", "https://example.invalid/org/repo.git"},
		{"git@github.com:org/repo.git", "github.com:org/repo.git"},
	}
	for _, tc := range tests {
		t.Run(tc.origin, func(t *testing.T) {
			e := newAgentWorkspaceEnv(t)
			wsGit(t, e.repo, "remote", "set-url", "origin", tc.origin)
			ws, cleanup, err := NewWorktreeManager(e.repo).createAgentWorkspace(context.Background(), "GH-2", "pilot/GH-2", "HEAD")
			if err != nil {
				t.Fatalf("createAgentWorkspace: %v", err)
			}
			defer cleanup()
			if got := wsGit(t, ws, "remote", "get-url", "origin"); got != tc.want {
				t.Errorf("origin = %q, want %q", got, tc.want)
			}
			if raw, err := os.ReadFile(filepath.Join(ws, ".git", "config")); err != nil || strings.Contains(string(raw), "fake-") {
				t.Errorf("workspace config holds the credential (err %v)", err)
			}
		})
	}
}

func TestStripURLCredentials(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://u:p@host/r.git", "https://host/r.git"},
		{"ssh://git@host:22/r.git", "ssh://host:22/r.git"},
		{"git@host:org/r.git", "host:org/r.git"},
		{"host:org/r.git", "host:org/r.git"},
		{"/srv/git/r.git", "/srv/git/r.git"},
		{"./a@b:c", "./a@b:c"},
	}
	for _, tc := range tests {
		if got, err := stripURLCredentials(tc.in); err != nil || got != tc.want {
			t.Errorf("stripURLCredentials(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := stripURLCredentials("https://u:fake-secret@[bad"); err == nil || strings.Contains(err.Error(), "fake-secret") {
		t.Errorf("unparsable URL: err %v, want an error without the URL", err)
	}
}

func TestCreateAgentWorkspaceFailureLeavesNothing(t *testing.T) {
	tests := []struct {
		name    string
		branch  string
		hook    func(t *testing.T, e *agentWorkspaceEnv) func(string)
		wantErr string
	}{
		{"transport fails at the task branch", "bad..branch", nil, "task branch"},
		{"head differs from the resolved SHA", "pilot/GH-3", func(t *testing.T, e *agentWorkspaceEnv) func(string) {
			return func(ws string) { gitTestCommit(t, ws, "moved by the agent") }
		}, "head of pilot/GH-3"},
		{"objects directory is a symbolic link", "pilot/GH-3", func(t *testing.T, e *agentWorkspaceEnv) func(string) {
			return func(ws string) {
				objects := filepath.Join(ws, ".git", "objects")
				if err := os.Rename(objects, objects+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(objects+"-real", objects); err != nil {
					t.Skipf("symlink: %v", err)
				}
			}
		}, "objects directory"},
		{"alternates file", "pilot/GH-3", func(t *testing.T, e *agentWorkspaceEnv) func(string) {
			alt := t.TempDir()
			return func(ws string) {
				if err := os.WriteFile(filepath.Join(ws, ".git", "objects", "info", "alternates"), []byte(alt+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}, "objects/info/alternates"},
		{"object file hard-linked into the daemon repository", "pilot/GH-3", func(t *testing.T, e *agentWorkspaceEnv) func(string) {
			return func(ws string) { linkDaemonObject(t, e.repo, ws) }
		}, "hard link"},
		{"extra ref", "pilot/GH-3", func(t *testing.T, e *agentWorkspaceEnv) func(string) {
			return func(ws string) { wsGit(t, ws, "update-ref", "refs/tags/extra", "HEAD") }
		}, "refs are"},
		{"origin/BASE not at the base", "pilot/GH-3", func(t *testing.T, e *agentWorkspaceEnv) func(string) {
			return func(ws string) {
				wsGit(t, ws, "update-ref", "refs/remotes/origin/main", gitTestCommitDetached(t, ws))
			}
		}, "refs are"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentWorkspaceEnv(t)
			if tc.hook != nil {
				agentWorkspaceAfterTransport = tc.hook(t, e)
				t.Cleanup(func() { agentWorkspaceAfterTransport = nil })
			}
			ws, cleanup, err := NewWorktreeManager(e.repo).createAgentWorkspace(context.Background(), "GH-3", tc.branch, "HEAD")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
			if ws != "" || cleanup != nil {
				t.Errorf("failure returned a workspace %q", ws)
			}
			if names := e.rootEntries(t); len(names) != 0 {
				t.Errorf("workspace root holds %q after the failure, want nothing (no workspace, no bundle file)", names)
			}
			e.assertNoTempRef(t)
		})
	}
}

// linkDaemonObject hard-links the first object file of the daemon repository
// repo into the objects directory of the workspace ws.
func linkDaemonObject(t *testing.T, repo, ws string) {
	t.Helper()
	var src string
	_ = filepath.WalkDir(filepath.Join(repo, ".git", "objects"), func(p string, d os.DirEntry, err error) error {
		if err == nil && src == "" && d.Type().IsRegular() {
			src = p
		}
		return nil
	})
	if src == "" {
		t.Fatal("no object file in the daemon repository")
	}
	if err := os.Link(src, filepath.Join(ws, ".git", "objects", "pilot-test-link")); err != nil {
		t.Skipf("hard link: %v", err)
	}
}

// gitTestCommitDetached writes a commit object on top of HEAD of dir without
// moving any ref and returns its SHA.
func gitTestCommitDetached(t *testing.T, dir string) string {
	t.Helper()
	return wsGit(t, dir, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit-tree", "-p", "HEAD", "-m", "detached", "HEAD^{tree}")
}

func TestCreateAgentWorkspaceRefusesWithoutTheOption(t *testing.T) {
	repo := setupTestRepo(t)
	t.Cleanup(func() { _ = os.RemoveAll(repo) })
	setRunAsStateForTest(t, nil, "")
	if _, _, err := NewWorktreeManager(repo).createAgentWorkspace(context.Background(), "GH-4", "pilot/GH-4", "HEAD"); err == nil {
		t.Error("createAgentWorkspace with the option empty: no error")
	}
}

func TestAgentWorkspaceCleanupKeepsTheDaemonBranch(t *testing.T) {
	e := newAgentWorkspaceEnv(t)
	branch := "pilot/GH-5"
	wsGit(t, e.repo, "branch", branch)
	ws, cleanup, err := NewWorktreeManager(e.repo).createAgentWorkspace(context.Background(), "GH-5", branch, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Errorf("workspace not removed: %v", err)
	}
	if !gitBranchExists(t, e.repo, branch) {
		t.Error("cleanup deleted the branch of the daemon repository")
	}
}

func TestAgentWorkspaceCleanupRefusesPaths(t *testing.T) {
	e := newAgentWorkspaceEnv(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "pilot-ws-victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(e.root, "pilot-ws-00000000-GH-6-1")
	if err := os.Symlink(victim, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	wrongPrefix := filepath.Join(e.root, "other-ws")
	if err := os.MkdirAll(wrongPrefix, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(e.root, "pilot-ws-00000000-GH-6-2", "pilot-ws-inner")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
		keep []string
	}{
		{"path outside the root", victim, []string{victim}},
		{"wrong prefix", wrongPrefix, []string{wrongPrefix}},
		{"symbolic link", link, []string{link, filepath.Join(victim, "keep")}},
		{"not a direct child", nested, []string{nested}},
		{"not clean", e.root + "/pilot-ws-00000000-GH-6-2/../pilot-ws-00000000-GH-6-2", []string{nested}},
	}
	s := runAsSettingsNow()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLog(t)
			if err := removeAgentWorkspace(s, tc.path); err == nil {
				t.Error("removal of a refused path: no error")
			}
			for _, p := range tc.keep {
				if _, err := os.Lstat(p); err != nil {
					t.Errorf("%s was removed: %v", p, err)
				}
			}
			if n := strings.Count(buf.String(), "level=ERROR"); n != 1 {
				t.Errorf("want one Error line, got:\n%s", buf.String())
			}
		})
	}
	t.Run("a valid workspace is removed as the agent user", func(t *testing.T) {
		valid := filepath.Join(e.root, "pilot-ws-00000000-GH-6-3")
		if err := os.MkdirAll(filepath.Join(valid, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(e.sudoLog)
		if err := removeAgentWorkspace(s, valid); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(valid); !os.IsNotExist(err) {
			t.Errorf("not removed: %v", err)
		}
		calls := sudoCalls(t, e.sudoLog)
		if len(calls) != 1 || !strings.HasSuffix(calls[0], "-- rm -rf -- "+valid) {
			t.Errorf("sudo calls = %q, want one rm -rf as the agent user", calls)
		}
	})
}

// sweepFixture is two projects with workspaces of several tasks in one root.
type sweepFixture struct {
	repoA, repoB string
	a1, a1Bundle string // project A, task GH-1
	a2, a12      string // project A, tasks GH-2 and GH-1-2
	b1           string // project B, task GH-1
	other        string
}

func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	stubRunAsGOOS(t)
	_ = mockSudoAgent(t, "", "")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	setRunAsStateForTest(t, gitRouteSettings(t, root), "")
	f := &sweepFixture{repoA: setupTestRepo(t), repoB: setupTestRepo(t)}
	t.Cleanup(func() { _ = os.RemoveAll(f.repoA); _ = os.RemoveAll(f.repoB) })
	pa, err := workspaceProjectID(f.repoA)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := workspaceProjectID(f.repoB)
	if err != nil {
		t.Fatal(err)
	}
	if pa == pb {
		t.Fatal("two repositories with one project id")
	}
	// Each workspace is a clone of its repository whose tip is in
	// refs/remotes/origin/main there: safe to remove (agentWorkspaceTipSafe).
	for _, repo := range []string{f.repoA, f.repoB} {
		wsGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	}
	mk := func(name, repo string) string {
		p := filepath.Join(root, name)
		if repo != "" {
			wsGit(t, root, "clone", "-q", "--shared", repo, p)
		} else if err := os.WriteFile(p, []byte("bundle"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f.a1 = mk("pilot-ws-"+pa+"-GH-1-100", f.repoA)
	f.a1Bundle = mk("pilot-ws-"+pa+"-GH-1-100.bundle", "")
	f.a2 = mk("pilot-ws-"+pa+"-GH-2-200", f.repoA)
	f.a12 = mk("pilot-ws-"+pa+"-GH-1-2-300", f.repoA)
	f.b1 = mk("pilot-ws-"+pb+"-GH-1-400", f.repoB)
	f.other = mk("not-a-workspace", f.repoA)
	return f
}

func assertPresence(t *testing.T, gone, kept []string) {
	t.Helper()
	for _, p := range gone {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s still present", filepath.Base(p))
		}
	}
	for _, p := range kept {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(p), err)
		}
	}
}

func TestCleanupOrphanedWorktreesAgentWorkspaces(t *testing.T) {
	f := newSweepFixture(t)
	orphanWT := filepath.Join(os.TempDir(), fmt.Sprintf("pilot-worktree-gh6380-sweep-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(orphanWT, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(orphanWT) })
	buf := captureLog(t)

	removed, _, err := CleanupOrphanedWorktrees(context.Background(), f.repoA)
	if err != nil {
		t.Fatal(err)
	}
	assertPresence(t, []string{f.a1, f.a1Bundle, f.a2, f.a12, orphanWT}, []string{f.b1, f.other})
	if removed < 5 {
		t.Errorf("removed = %d, want at least 5 (4 workspace entries and the worktree orphan)", removed)
	}
	if n := strings.Count(buf.String(), `msg="Removed orphaned agent workspace"`); n != 4 {
		t.Errorf("%d Info lines for removed workspaces, want 4:\n%s", n, buf.String())
	}
	for _, task := range []string{"task_id=GH-1 ", "task_id=GH-2 ", "task_id=GH-1-2 "} {
		if !strings.Contains(buf.String(), task) {
			t.Errorf("no Info line with %s", task)
		}
	}
}

func TestPruneOrphanedWorktreeForTaskAgentWorkspaces(t *testing.T) {
	f := newSweepFixture(t)
	removed, err := PruneOrphanedWorktreeForTask(context.Background(), f.repoA, "GH-1")
	if err != nil {
		t.Fatal(err)
	}
	assertPresence(t, []string{f.a1, f.a1Bundle}, []string{f.a2, f.a12, f.b1, f.other})
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
}

func TestFindOrphanedWorktreeDirsReportsAgentWorkspaces(t *testing.T) {
	f := newSweepFixture(t)
	found, err := findOrphanedWorktreeDirs(f.repoA, "GH-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(found, ","); got != strings.Join([]string{f.a1, f.a1Bundle}, ",") {
		t.Errorf("found %q, want the GH-1 workspace of project A and its bundle file", found)
	}
	assertPresence(t, nil, []string{f.a1, f.a1Bundle, f.a2, f.a12, f.b1, f.other})
}

func TestOrphanedWorktreeSweepOptionEmptyKeepsWorkspaces(t *testing.T) {
	f := newSweepFixture(t)
	setRunAsStateForTest(t, nil, "")
	if _, _, err := CleanupOrphanedWorktrees(context.Background(), f.repoA); err != nil {
		t.Fatal(err)
	}
	if _, err := PruneOrphanedWorktreeForTask(context.Background(), f.repoA, "GH-1"); err != nil {
		t.Fatal(err)
	}
	assertPresence(t, nil, []string{f.a1, f.a1Bundle, f.a2, f.a12, f.b1, f.other})
}

func TestParseAgentWorkspaceName(t *testing.T) {
	tests := []struct {
		name, project, task string
		ok                  bool
	}{
		{"pilot-ws-0123abcd-GH-1-123", "0123abcd", "GH-1", true},
		{"pilot-ws-0123abcd-GH-1-2-123.bundle", "0123abcd", "GH-1-2", true},
		{"pilot-ws-0123abcd-GH", "", "", false},
		{"pilot-ws-0123abcd-GH-", "", "", false},
		{"pilot-ws-0123ABCD-GH-1-123", "", "", false},
		{"pilot-ws-probe-GH-1-123", "", "", false},
		{"pilot-worktree-GH-1-123", "", "", false},
	}
	for _, tc := range tests {
		p, task, ok := parseAgentWorkspaceName(tc.name)
		if ok != tc.ok || ok && (p != tc.project || task != tc.task) {
			t.Errorf("parse(%q) = %q, %q, %v; want %q, %q, %v", tc.name, p, task, ok, tc.project, tc.task, tc.ok)
		}
	}
}

// TestGitCommandRouteLinkInsideWorkspace is the route rule of 10a: a link
// inside a workspace that points at a second repository fails closed and no
// git process runs in that repository.
func TestGitCommandRouteLinkInsideWorkspace(t *testing.T) {
	stubRunAsGOOS(t)
	logPath := mockSudoExec(t)
	root := t.TempDir()
	ws := workspaceDir(t, root)
	second := setupTestRepo(t)
	t.Cleanup(func() { _ = os.RemoveAll(second) })
	link := filepath.Join(ws, "repo-link")
	if err := os.Symlink(second, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	setRunAsStateForTest(t, gitRouteSettings(t, root), "")
	ctx := context.Background()
	for name, cmd := range map[string]*exec.Cmd{
		"cmd.Dir form":  gitCommandIn(ctx, link, "tag", "gh6380-probe-dir"),
		"-C form":       gitCommand(ctx, link, "-C", link, "tag", "gh6380-probe-c"),
		"GitOperations": NewGitOperations(link).gitCmd(ctx, "tag", "gh6380-probe-ops"),
	} {
		t.Run(name, func(t *testing.T) {
			if cmd.Err == nil {
				t.Errorf("Err = nil for a link inside a workspace (argv %q)", cmd.Args)
			}
			if err := cmd.Run(); err == nil {
				t.Error("Run succeeded")
			}
		})
	}
	if tags := wsGit(t, second, "tag", "--list", "gh6380-*"); tags != "" {
		t.Errorf("git ran in the second repository: tags %q", tags)
	}
	if got := sudoArgv(t, logPath); got != nil {
		t.Errorf("a process started through sudo: %q", got)
	}
}

func TestGitCommandRouteDashCIntoWorkspace(t *testing.T) {
	stubRunAsGOOS(t)
	logPath := mockSudoExec(t)
	root := t.TempDir()
	ws := workspaceDir(t, root)
	outside := t.TempDir()
	setRunAsStateForTest(t, gitRouteSettings(t, root), "")
	ctx := context.Background()
	rel, err := filepath.Rel(outside, ws)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"-C into a workspace", []string{"-C", ws, "status"}, true},
		{"relative -C into a workspace", []string{"-C", rel, "status"}, true},
		{"-C twice ending in a workspace", []string{"-C", root, "-C", "ws-1", "status"}, true},
		{"--git-dir= into a workspace", []string{"--git-dir=" + filepath.Join(ws, ".git"), "status"}, true},
		{"--git-dir into a workspace", []string{"-c", "a.b=c", "--git-dir", filepath.Join(ws, ".git"), "log"}, true},
		{"--work-tree into a workspace", []string{"--work-tree=" + ws, "status"}, true},
		{"-C outside", []string{"-C", outside, "status"}, false},
		{"subcommand option -C is not a path", []string{"commit", "-C", ws, "--dry-run"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := gitCommand(ctx, outside, tc.args...)
			if (cmd.Err != nil) != tc.wantErr {
				t.Fatalf("Err = %v, want error %v", cmd.Err, tc.wantErr)
			}
			if !tc.wantErr {
				if _, agent := agentCmdUser(cmd); agent || joinNUL(cmd.Args) != joinNUL(append([]string{"git"}, tc.args...)) {
					t.Errorf("not the plain command: %q", cmd.Args)
				}
				return
			}
			if err := cmd.Run(); err == nil {
				t.Error("Run succeeded")
			}
		})
	}
	if got := sudoArgv(t, logPath); got != nil {
		t.Errorf("a process started through sudo: %q", got)
	}
}

func TestGitCommandRouteUnresolvableRoot(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the test user cannot search")
	}
	stubRunAsGOOS(t)
	locked := filepath.Join(t.TempDir(), "locked")
	root := filepath.Join(locked, "root")
	if err := os.MkdirAll(filepath.Join(root, "ws-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := filepath.EvalSymlinks(root); err == nil {
		t.Skip("the locked parent is still searchable")
	}
	outside := t.TempDir()
	setRunAsStateForTest(t, gitRouteSettings(t, root), "")
	ctx := context.Background()

	cmd := gitCommandIn(ctx, outside, "status")
	if cmd.Err != nil {
		t.Fatalf("command of another directory: Err = %v, want the plain route", cmd.Err)
	}
	if _, agent := agentCmdUser(cmd); agent {
		t.Errorf("command of another directory took the agent route: %q", cmd.Args)
	}
	if cmd := gitCommandIn(ctx, filepath.Join(root, "ws-1"), "status"); cmd.Err == nil {
		t.Error("command inside the unresolvable root: Err = nil")
	}
	if cmd := gitCommand(ctx, outside, "-C", filepath.Join(root, "ws-1"), "status"); cmd.Err == nil {
		t.Error("-C inside the unresolvable root from outside: Err = nil")
	}
}

// workspacePreflight runs the preflight for the option with root as the
// workspace root, the real workspace host check, and an agent user whose uid
// is ownerUID.
func workspacePreflight(t *testing.T, root, ownerUID string, cfg *ClaudeCodeConfig, useWorktree bool, pool int) (*bytes.Buffer, error) {
	t.Helper()
	setRunAsStateForTest(t, nil, "")
	home := t.TempDir()
	stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": home})
	runAsWorkspaceCheck = checkRunAsWorkspaceRoot
	// The agent user may have the uid of the test user (the owner of the
	// temporary root); the daemon then needs another one.
	currentUID = func() (string, error) { return "daemon-uid-stub", nil }
	lookupUser = func(name string) (*user.User, error) {
		if name != "pilot-agent" {
			return nil, fmt.Errorf("unknown user %s", name)
		}
		return &user.User{Uid: ownerUID, HomeDir: home}, nil
	}
	buf := captureLog(t)
	if cfg == nil {
		cfg = &ClaudeCodeConfig{}
	}
	cfg.RunAsUser, cfg.RunAsGroup, cfg.RunAsWorkspaceDir = "pilot-agent", "pilot-agents", root
	PreflightRunAsUser(cfg, "", useWorktree, pool)
	return buf, RunAsUserPreflightError()
}

func TestRunAsUserPreflightWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file owners")
	}
	me := strconv.Itoa(os.Getuid())
	unlistableProbe := `if [ "$1" = mkdir ]; then mkdir "$3" && chmod 0300 "$3"; exit $?; fi`
	tests := []struct {
		name        string
		root        func(t *testing.T) string
		owner       string
		extra       string
		useWorktree bool
		pool        int
		wantCheck   string // empty: passes
	}{
		{"passes", func(t *testing.T) string { return t.TempDir() }, me, "", true, 0, ""},
		{"root missing", func(t *testing.T) string { return filepath.Join(t.TempDir(), "gone") }, me, "", true, 0, "workspace root exists"},
		{"owner is not the agent user", func(t *testing.T) string { return t.TempDir() }, "5000", "", true, 0, "workspace root is owned by the agent user"},
		{"daemon cannot list the root", func(t *testing.T) string {
			if os.Geteuid() == 0 {
				t.Skip("root lists any directory")
			}
			d := t.TempDir()
			if err := os.Chmod(d, 0o300); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(d, 0o755) })
			return d
		}, me, "", true, 0, "daemon user can list the workspace root"},
		{"daemon cannot list a directory of the agent", func(t *testing.T) string {
			if os.Geteuid() == 0 {
				t.Skip("root lists any directory")
			}
			return t.TempDir()
		}, me, unlistableProbe, true, 0, "daemon user can list a directory the agent user creates"},
		{"agent cannot create the probe", func(t *testing.T) string { return t.TempDir() }, me, `[ "$1" = mkdir ] && exit 1`, true, 0, "daemon user can list a directory the agent user creates"},
		{"use_worktree false", func(t *testing.T) string { return t.TempDir() }, me, "", false, 0, "run_as_user requires use_worktree"},
		{"worktree pool", func(t *testing.T) string { return t.TempDir() }, me, "", true, 2, "run_as_user requires worktree_pool_size 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.root(t)
			logPath := mockSudoAgent(t, "", tc.extra)
			buf, err := workspacePreflight(t, root, tc.owner, nil, tc.useWorktree, tc.pool)
			if tc.wantCheck == "" {
				if err != nil || strings.Contains(buf.String(), "level=ERROR") {
					t.Fatalf("preflight failed: %v\n%s", err, buf.String())
				}
				entries, rerr := os.ReadDir(root)
				if rerr != nil || len(entries) != 0 {
					t.Errorf("workspace root after the preflight holds %v (%v), want the probe removed", entries, rerr)
				}
				var probe bool
				for _, c := range sudoCalls(t, logPath) {
					probe = probe || strings.Contains(c, "-- mkdir -- "+filepath.Join(root, runAsWorkspaceProbeName))
				}
				if !probe {
					t.Errorf("no probe directory created as the agent user: %q", sudoCalls(t, logPath))
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantCheck) {
				t.Errorf("preflight error = %v, want one naming %q", err, tc.wantCheck)
			}
			if n := strings.Count(buf.String(), "level=ERROR"); n != 1 || !strings.Contains(buf.String(), tc.wantCheck) {
				t.Errorf("want one Error line naming %q, got:\n%s", tc.wantCheck, buf.String())
			}
		})
	}
}

func TestRunAsUserPreflightRefusedWorkspaceRoots(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home", "daemon")
	pilotHome := filepath.Join(base, "data", "pilot")
	cfgDir := filepath.Join(base, "etc", "pilot")
	for _, d := range []string{home, pilotHome, cfgDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name    string
		root    string
		refused bool
	}{
		{"/", "/", true},
		{"daemon home", home, true},
		{"parent of the daemon home", filepath.Dir(home), true},
		{"default data directory", filepath.Join(home, ".pilot"), true},
		{"configured Pilot home", pilotHome, true},
		{"parent of the Pilot home", filepath.Dir(pilotHome), true},
		{"directory of the config file", cfgDir, true},
		{"trailing slash on the home", home + "/", true},
		{"a separate root", filepath.Join(base, "agent-workspaces"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_ = mockSudoAgent(t, "", "")
			prev := daemonHomeDir
			daemonHomeDir = func() (string, error) { return home, nil }
			t.Cleanup(func() { daemonHomeDir = prev })
			cfg := &ClaudeCodeConfig{PilotHome: pilotHome, ConfigPath: filepath.Join(cfgDir, "config.yaml")}
			setRunAsStateForTest(t, nil, "")
			stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
			buf := captureLog(t)
			cfg.RunAsUser, cfg.RunAsGroup, cfg.RunAsWorkspaceDir = "pilot-agent", "pilot-agents", tc.root
			PreflightRunAsUser(cfg, "", true, 0)
			err := RunAsUserPreflightError()
			const want = "run_as_workspace_dir is /, the daemon home"
			if !tc.refused {
				if err != nil {
					t.Errorf("a separate root was refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("preflight error = %v, want one containing %q", err, want)
			}
			if n := strings.Count(buf.String(), "level=ERROR"); n != 1 {
				t.Errorf("want one Error line, got:\n%s", buf.String())
			}
		})
	}
}

func TestRunAsUserPreflightReRunsWhenWorktreeSettingsChange(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	_ = mockSudo(t, 0)
	stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
	cfg := &ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents"}
	PreflightRunAsUser(cfg, "", true, 0)
	if err := RunAsUserPreflightError(); err != nil {
		t.Fatalf("first preflight: %v", err)
	}
	PreflightRunAsUser(cfg, "", true, 1)
	if err := RunAsUserPreflightError(); err == nil {
		t.Error("a reload to worktree_pool_size 1 kept the passing result")
	}
	PreflightRunAsUser(cfg, "", false, 0)
	if err := RunAsUserPreflightError(); err == nil {
		t.Error("a reload to use_worktree false kept a passing result")
	}
}

// TestAgentWorkspaceRunnerStopsWithoutWorkspace is item 6: with the option
// set, a run for which the runner creates no worktree stops before any
// process starts, with one Error line naming the task and the reason.
func TestAgentWorkspaceRunnerStopsWithoutWorkspace(t *testing.T) {
	tests := []struct {
		name          string
		allowWorktree bool
		branch        string
		direct        bool
		wantReason    string
	}{
		{"allowWorktree false", false, "pilot/GH-8", false, "worktree creation is off for this run"},
		{"empty branch", true, "", false, "the task has no branch"},
		{"direct commit", true, "pilot/GH-8", true, "direct commit is set"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentWorkspaceEnv(t)
			backend := &retryPathRecordingBackend{}
			runner := NewRunnerWithBackend(backend)
			runner.config = &BackendConfig{}
			runner.SetSkipPreflightChecks(true)
			runner.SetRecordingEnabled(false)
			var buf bytes.Buffer
			runner.log = slog.New(slog.NewTextHandler(&buf, nil))
			cmds := captureAgentCommands(t)
			task := &Task{ID: "GH-8", Title: "t", Description: "d", ProjectPath: e.repo, Branch: tc.branch, CreatePR: true, DirectCommit: tc.direct}

			res, err := runner.executeWithOptions(context.Background(), task, tc.allowWorktree)
			if err == nil || !strings.Contains(err.Error(), tc.wantReason) {
				t.Fatalf("err = %v, want one naming %q", err, tc.wantReason)
			}
			if res == nil || res.Success || !strings.Contains(res.Error, tc.wantReason) {
				t.Errorf("result = %+v", res)
			}
			if got := cmds(); len(got) != 0 {
				t.Errorf("%d processes were built before the stop, first %q", len(got), got[0].Args)
			}
			if paths := backend.paths(); len(paths) != 0 {
				t.Errorf("the backend ran in %q", paths)
			}
			if lines := strings.Count(buf.String(), "level=ERROR"); lines != 1 || !strings.Contains(buf.String(), "task_id=GH-8") || !strings.Contains(buf.String(), tc.wantReason) {
				t.Errorf("want one Error line with the task and the reason, got:\n%s", buf.String())
			}
		})
	}
}

// TestAgentWorkspaceRunnerRunsInWorkspace is item 6: with the option set the
// backend runs in the agent workspace, and the workspace is gone after the
// run.
func TestAgentWorkspaceRunnerRunsInWorkspace(t *testing.T) {
	e := newAgentWorkspaceEnv(t)
	backend := &retryPathRecordingBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	task := &Task{ID: "GH-9", Title: "t", Description: "d", ProjectPath: e.repo, Branch: "pilot/GH-9", CreatePR: true, BaseBranch: "main"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = runner.Execute(ctx, task)

	paths := backend.paths()
	if len(paths) == 0 {
		t.Fatal("the backend never ran")
	}
	if filepath.Dir(paths[0]) != e.root || !strings.HasPrefix(filepath.Base(paths[0]), agentWorkspacePrefix) {
		t.Errorf("the backend ran in %q, want an agent workspace under %s", paths[0], e.root)
	}
	if names := e.rootEntries(t); len(names) != 0 {
		t.Errorf("workspace root holds %q after the run, want the workspace removed", names)
	}
	if gitBranchExists(t, e.repo, "pilot/GH-9") {
		t.Error("the daemon repository got the task branch")
	}
}

// TestCreateAgentWorkspaceOriginBaseRef is item 2a: a new workspace holds
// refs/remotes/origin/BASE at the base commit, so the commit count and the
// merge-base of a run work in it (the agent user has no credential to fetch
// origin; the fetch of CountNewCommitsAgainstOrigin fails closed).
func TestCreateAgentWorkspaceOriginBaseRef(t *testing.T) {
	tests := []struct {
		name    string
		baseRef string
		commits int
	}{
		{"empty base, no commit yet", "", 0},
		{"empty base, one commit", "", 1},
		{"origin/main, two commits", "origin/main", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentWorkspaceEnv(t)
			m := NewWorktreeManager(e.repo)
			m.SetBaseBranch("main")
			ws, cleanup, err := m.createAgentWorkspace(context.Background(), "GH-10", "pilot/GH-10", tc.baseRef)
			if err != nil {
				t.Fatalf("createAgentWorkspace: %v", err)
			}
			defer cleanup()
			base := wsGit(t, e.repo, "rev-parse", "origin/main")
			if got := wsGit(t, ws, "rev-parse", "refs/remotes/origin/main"); got != base {
				t.Errorf("origin/main in the workspace = %s, want the base commit %s", got, base)
			}
			for i := 0; i < tc.commits; i++ {
				gitTestCommit(t, ws, fmt.Sprintf("work %d", i))
			}
			g := NewGitOperations(ws)
			ctx := context.Background()
			if n, err := g.CountNewCommitsAgainstOrigin(ctx, "main"); err != nil || n != tc.commits {
				t.Errorf("CountNewCommitsAgainstOrigin = %d, %v; want %d", n, err, tc.commits)
			}
			if mb, err := g.resolveMergeBaseSHA(ctx, "main"); err != nil || mb != base {
				t.Errorf("merge-base = %q, %v; want the base commit %s", mb, err, base)
			}
		})
	}
}

// TestCreateAgentWorkspaceRefusesEmptyTaskID is item 2d: a workspace name
// without TASK is never parsed, so no sweep would remove it.
func TestCreateAgentWorkspaceRefusesEmptyTaskID(t *testing.T) {
	e := newAgentWorkspaceEnv(t)
	ws, cleanup, err := NewWorktreeManager(e.repo).createAgentWorkspace(context.Background(), "", "pilot/GH-11", "HEAD")
	if err == nil || !strings.Contains(err.Error(), "no task ID") {
		t.Fatalf("err = %v, want one naming the missing task ID", err)
	}
	if ws != "" || cleanup != nil {
		t.Errorf("refusal returned a workspace %q", ws)
	}
	if names := e.rootEntries(t); len(names) != 0 {
		t.Errorf("workspace root holds %q, want nothing", names)
	}
	if calls := sudoCalls(t, e.sudoLog); len(calls) != 0 {
		t.Errorf("a command ran as the agent user: %q", calls)
	}
	e.assertNoTempRef(t)
}

// TestCleanupOrphanedWorktreesKeepAgentWorkspaces is item 2c: the sweep of a
// one-shot command removes the worktree orphans of the project and no agent
// workspace; only the daemon sweeps those.
func TestCleanupOrphanedWorktreesKeepAgentWorkspaces(t *testing.T) {
	tests := []struct {
		name  string
		sweep func(ctx context.Context, repo string) (int, int64, error)
		gone  func(f *sweepFixture) []string
	}{
		{"one-shot command", CleanupOrphanedWorktreesKeepAgentWorkspaces, func(*sweepFixture) []string { return nil }},
		{"daemon start", CleanupOrphanedWorktrees, func(f *sweepFixture) []string { return []string{f.a1, f.a1Bundle, f.a2, f.a12} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newSweepFixture(t)
			orphanWT := filepath.Join(os.TempDir(), fmt.Sprintf("pilot-worktree-gh6387-sweep-%d", time.Now().UnixNano()))
			if err := os.MkdirAll(orphanWT, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(orphanWT) })
			if _, _, err := tc.sweep(context.Background(), f.repoA); err != nil {
				t.Fatal(err)
			}
			gone := append(tc.gone(f), orphanWT)
			all := []string{f.a1, f.a1Bundle, f.a2, f.a12, f.b1, f.other}
			var kept []string
			for _, p := range all {
				removed := false
				for _, g := range gone {
					removed = removed || g == p
				}
				if !removed {
					kept = append(kept, p)
				}
			}
			assertPresence(t, gone, kept)
		})
	}
}

// TestRunAsUserPreflightPassesWhenAllChecksPass: with each check of the
// option met in the fixture the preflight passes; with one check not met at a
// time it refuses with one Error line naming that check, and no git command
// in the workspace root is built.
func TestRunAsUserPreflightPassesWhenAllChecksPass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file owners")
	}
	me := strconv.Itoa(os.Getuid())
	tests := []struct {
		name      string
		user      string
		group     string
		home      func(home string) string
		firewall  string // the result of the firewall probe
		wantCheck string // empty: passes
	}{
		{"every check met", "pilot-agent", "pilot-agents", func(h string) string { return h }, "", ""},
		{"no group", "pilot-agent", "", func(h string) string { return h }, "", "run_as_group is required with run_as_user"},
		{"a root user", "root", "pilot-agents", func(h string) string { return h }, "", "run_as_user name is not valid"},
		{"a missing home", "pilot-agent", "pilot-agents", func(h string) string { return filepath.Join(h, "gone") }, "", "home exists"},
		{"the firewall probe refused", "pilot-agent", "pilot-agents", func(h string) string { return h }, "firewall: stub refusal", "firewall: stub refusal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRunAsStateForTest(t, nil, "")
			root := t.TempDir()
			home := tc.home(t.TempDir())
			_ = mockSudoAgent(t, "", "")
			stubRunAsHost(t, "linux", "1000", nil)
			runAsWorkspaceCheck = checkRunAsWorkspaceRoot
			runAsFirewallCheck = func(*runAsSettings) string { return tc.firewall }
			// The agent user has the uid of the test user (the owner of the
			// temporary root); the daemon then needs another one.
			currentUID = func() (string, error) { return "daemon-uid-stub", nil }
			lookupUser = func(name string) (*user.User, error) {
				if name != tc.user {
					return nil, fmt.Errorf("unknown user %s", name)
				}
				return &user.User{Uid: me, HomeDir: home}, nil
			}
			buf := captureLog(t)
			PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: tc.user, RunAsGroup: tc.group, RunAsWorkspaceDir: root}, "", true, 0)
			err := RunAsUserPreflightError()
			if tc.wantCheck == "" {
				if err != nil || strings.Contains(buf.String(), "level=ERROR") {
					t.Fatalf("preflight failed: %v\n%s", err, buf.String())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantCheck) {
				t.Fatalf("preflight error = %v, want one naming %q", err, tc.wantCheck)
			}
			if n := strings.Count(buf.String(), "level=ERROR"); n != 1 || !strings.Contains(buf.String(), tc.wantCheck) {
				t.Errorf("want one Error line naming %q, got:\n%s", tc.wantCheck, buf.String())
			}
			if cmd := gitCommandIn(context.Background(), root, "status"); cmd.Err == nil {
				t.Errorf("a git command in the workspace root was built after the refusal: %q", cmd.Args)
			}
		})
	}
}

// TestAgentWorkspaceSweepWaitsForThePreflight: with the option set, the sweep
// at daemon start and the removal of a workspace start no sudo process after
// a refused preflight (one Warn line with the directory count), and after a
// passed one the read of agentWorkspaceTipSafe and one rm -rf as the agent
// user.
func TestAgentWorkspaceSweepWaitsForThePreflight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file owners")
	}
	me := strconv.Itoa(os.Getuid())
	tests := []struct {
		name      string
		refusal   bool
		wantCalls int
	}{
		{"refused preflight", true, 0},
		{"passed preflight", false, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubRunAsGOOS(t)
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			repo := setupTestRepo(t)
			t.Cleanup(func() { _ = os.RemoveAll(repo) })
			project, err := workspaceProjectID(repo)
			if err != nil {
				t.Fatal(err)
			}
			ws := filepath.Join(root, "pilot-ws-"+project+"-GH-1-100")
			wsGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
			wsGit(t, root, "clone", "-q", "--shared", repo, ws)
			logPath := mockSudoAgent(t, "", "")
			// A refused preflight: a daemon start without a gateway port.
			cfg := &ClaudeCodeConfig{RunAsGatewayRequired: tc.refusal}
			if _, err := workspacePreflight(t, root, me, cfg, true, 0); (err != nil) != tc.refusal {
				t.Fatalf("preflight error = %v, want refused = %v", err, tc.refusal)
			}
			_ = os.Remove(logPath) // the probes of the preflight
			buf := captureLog(t)

			if _, _, err := CleanupOrphanedWorktrees(context.Background(), repo); err != nil {
				t.Fatal(err)
			}

			calls := sudoCalls(t, logPath)
			if len(calls) != tc.wantCalls {
				t.Fatalf("sudo calls = %q, want %d", calls, tc.wantCalls)
			}
			if !tc.refusal {
				if !strings.Contains(calls[0], " rev-parse HEAD --all") || !strings.HasSuffix(calls[1], "-- rm -rf -- "+ws) {
					t.Errorf("sudo calls = %q, want the read of the tips, then rm -rf of the workspace", calls)
				}
				assertPresence(t, []string{ws}, nil)
				return
			}
			assertPresence(t, nil, []string{ws})
			out := buf.String()
			if n := strings.Count(out, "level=WARN msg=\"agent workspace sweep skipped"); n != 1 || !strings.Contains(out, "directories=1") {
				t.Errorf("want one Warn line naming the directory count, got:\n%s", out)
			}
			buf.Reset()
			if err := removeAgentWorkspace(runAsSettingsNow(), ws); err == nil {
				t.Error("removal after a refused preflight: no error")
			}
			if calls := sudoCalls(t, logPath); len(calls) != 0 {
				t.Errorf("removal after a refused preflight started sudo: %q", calls)
			}
			assertPresence(t, nil, []string{ws})
			if n := strings.Count(buf.String(), "level=WARN"); n != 1 || !strings.Contains(buf.String(), "directories=1") {
				t.Errorf("want one Warn line naming the directory count, got:\n%s", buf.String())
			}
		})
	}
}

// GH-6492: a workspace is removed only when agentWorkspaceTipSafe holds for
// it: at the end of the run after a push or a recovery ref, and at the sweeps.

// assertWorkspaceKept checks that ws is on disk and that logs hold one Error
// line that keeps it, with its path and reason.
func assertWorkspaceKept(t *testing.T, logs, ws, reason string) {
	t.Helper()
	if _, err := os.Lstat(ws); err != nil {
		t.Fatalf("workspace removed: %v", err)
	}
	kept := logLines(logs, "ERROR", "agent workspace kept")
	if len(kept) != 1 || !strings.Contains(kept[0], "path="+ws) || !strings.Contains(kept[0], reason) {
		t.Errorf("Error lines %q: want one that keeps %s with %q", kept, ws, reason)
	}
}

func TestAgentWorkspaceFinishChecksTheTip(t *testing.T) {
	tests := []struct {
		name string
		// run is what the run does in the workspace after its first commit.
		run      func(t *testing.T, e *handOverPushEnv)
		wantKept string // "" when the workspace is removed
	}{
		{"pushed, no later commit", func(t *testing.T, e *handOverPushEnv) {
			if err := e.git.Push(context.Background(), e.branch, handOverTestTask); err != nil {
				t.Fatalf("Push: %v", err)
			}
		}, ""},
		{"pushed, then one more commit", func(t *testing.T, e *handOverPushEnv) {
			if err := e.git.Push(context.Background(), e.branch, handOverTestTask); err != nil {
				t.Fatalf("Push: %v", err)
			}
			commitFile(t, e.ws, "b.txt", "two")
		}, agentWorkspaceTipUnsafe},
		{"failed push, no recovery ref", func(t *testing.T, e *handOverPushEnv) {
			wsGit(t, e.repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing"))
			if err := e.git.Push(context.Background(), e.branch, handOverTestTask); err == nil {
				t.Fatal("Push to a missing remote: no error")
			}
			// The hand-over ref holds the commit: only the record of the
			// push can remove the workspace.
			if daemonRefSHA(context.Background(), e.repo, handOverRef(handOverTestTask)) == "" {
				t.Fatal("no hand-over ref after the hand-over")
			}
		}, "were not pushed and no recovery ref holds them"},
		{"recovery ref, then one more commit", func(t *testing.T, e *handOverPushEnv) {
			if _, err := e.git.CreateRecoveryRef(context.Background(), handOverTestTask, "HEAD"); err != nil {
				t.Fatalf("CreateRecoveryRef: %v", err)
			}
			commitFile(t, e.ws, "b.txt", "two")
		}, agentWorkspaceTipUnsafe},
		{"pushed, HEAD broken", func(t *testing.T, e *handOverPushEnv) {
			if err := e.git.Push(context.Background(), e.branch, handOverTestTask); err != nil {
				t.Fatalf("Push: %v", err)
			}
			wsGit(t, e.ws, "symbolic-ref", "HEAD", "refs/heads/missing")
		}, agentWorkspaceTipUnsafe},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newHandOverPushEnv(t)
			logs := captureLog(t)
			commitFile(t, e.ws, "a.txt", "one")
			tc.run(t, e)
			e.cleanup()
			if tc.wantKept != "" {
				assertWorkspaceKept(t, logs.String(), e.ws, tc.wantKept)
				return
			}
			assertPresence(t, []string{e.ws}, nil)
			if kept := logLines(logs.String(), "ERROR", ""); len(kept) != 0 {
				t.Errorf("Error lines for a removed workspace: %q", kept)
			}
		})
	}
}

// forgetAgentWorkspaceRecord deletes the record of the workspace at ws, as a
// daemon start finds it: the sweep skips a workspace with a live record.
func forgetAgentWorkspaceRecord(t *testing.T, ws string) {
	t.Helper()
	if agentWorkspaceRecordOf(ws) == nil {
		t.Fatalf("no record of %s", ws)
	}
	agentWorkspaceBases.Delete(ws)
}

func TestAgentWorkspaceSweepChecksTheTip(t *testing.T) {
	e := newHandOverEnv(t)
	forgetAgentWorkspaceRecord(t, e.ws)
	logs := captureLog(t)
	head := commitFile(t, e.ws, "a.txt", "one")

	// The commit is in no ref of the daemon's repository: both sweeps keep it.
	if removed, err := PruneOrphanedWorktreeForTask(context.Background(), e.repo, handOverTestTask); err != nil || removed != 0 {
		t.Fatalf("PruneOrphanedWorktreeForTask = %d, %v; want 0, nil", removed, err)
	}
	assertWorkspaceKept(t, logs.String(), e.ws, agentWorkspaceTipUnsafe)
	if kept := logLines(logs.String(), "ERROR", "agent workspace kept")[0]; !strings.Contains(kept, "task_id="+handOverTestTask+" ") || !strings.Contains(kept, " age=") {
		t.Errorf("Error line %q: want the task and the age", kept)
	}
	logs.Reset()
	if removed, _ := sweepAgentWorkspaces(e.repo, ""); removed != 0 {
		t.Errorf("sweep removed %d, want 0", removed)
	}
	assertWorkspaceKept(t, logs.String(), e.ws, agentWorkspaceTipUnsafe)

	// The tip in refs/remotes/origin/ of the daemon's repository: removed.
	wsGit(t, e.repo, "fetch", "-q", e.ws, e.branch)
	wsGit(t, e.repo, "update-ref", "refs/remotes/origin/"+e.branch, head)
	logs.Reset()
	if removed, _ := sweepAgentWorkspaces(e.repo, ""); removed != 1 {
		t.Errorf("sweep removed %d, want 1", removed)
	}
	assertPresence(t, []string{e.ws}, nil)
	if n := len(logLines(logs.String(), "INFO", "Removed orphaned agent workspace")); n != 1 {
		t.Errorf("%d Info lines for the removed workspace, want 1:\n%s", n, logs.String())
	}
	if lines := logLines(logs.String(), "ERROR", ""); len(lines) != 0 {
		t.Errorf("Error lines for a safe workspace: %q", lines)
	}
}

func TestAgentWorkspaceSweepKeepsABrokenHead(t *testing.T) {
	e := newHandOverEnv(t)
	forgetAgentWorkspaceRecord(t, e.ws)
	logs := captureLog(t)
	wsGit(t, e.ws, "symbolic-ref", "HEAD", "refs/heads/missing")
	if removed, _ := sweepAgentWorkspaces(e.repo, ""); removed != 0 {
		t.Errorf("sweep removed %d, want 0", removed)
	}
	assertWorkspaceKept(t, logs.String(), e.ws, agentWorkspaceTipUnsafe)
}

func TestAgentWorkspaceSweepRemovesALostWorkspaceAfterSevenDays(t *testing.T) {
	e := newHandOverEnv(t)
	forgetAgentWorkspaceRecord(t, e.ws)
	logs := captureLog(t)
	commitFile(t, e.ws, "a.txt", "one")
	prev := agentWorkspaceAge
	agentWorkspaceAge = func(string) (time.Duration, bool) { return agentWorkspaceLostAge + time.Hour, true }
	t.Cleanup(func() { agentWorkspaceAge = prev })

	if removed, _ := sweepAgentWorkspaces(e.repo, ""); removed != 1 {
		t.Errorf("sweep removed %d, want 1", removed)
	}
	assertPresence(t, []string{e.ws}, nil)
	lost := logLines(logs.String(), "ERROR", "its commits are lost")
	if len(lost) != 1 || !strings.Contains(lost[0], "path="+e.ws) || !strings.Contains(lost[0], "task_id="+handOverTestTask+" ") {
		t.Errorf("Error lines %q: want one with the task and the path %s", lost, e.ws)
	}
	if n := len(logLines(logs.String(), "", "")); n != 1 {
		t.Errorf("want one log line, got:\n%s", logs.String())
	}
}

// GH-6536: the sweep reads every ref of a workspace, skips a workspace with a
// live record, counts a local branch of the daemon as safe with no record,
// falls back to the directory time for the age, and counts a removal once.

// newSafeSweepWorkspace is a repository and, in a workspace root of the
// option, one workspace of its project (task GH-1) named with stamp: a clone
// whose tip is in refs/remotes/origin/main of the repository, so safe.
func newSafeSweepWorkspace(t *testing.T, stamp string) (repo, ws string) {
	t.Helper()
	stubRunAsGOOS(t)
	_ = mockSudoAgent(t, "", "")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	setRunAsStateForTest(t, gitRouteSettings(t, root), "")
	repo = setupTestRepo(t)
	t.Cleanup(func() { _ = os.RemoveAll(repo) })
	project, err := workspaceProjectID(repo)
	if err != nil {
		t.Fatal(err)
	}
	wsGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	ws = filepath.Join(root, "pilot-ws-"+project+"-GH-1-"+stamp)
	wsGit(t, root, "clone", "-q", "--shared", repo, ws)
	return repo, ws
}

func TestAgentWorkspaceSweepReadsEveryRef(t *testing.T) {
	tests := []struct {
		name string
		// extra leaves a commit of the workspace in one ref outside
		// refs/heads/ and returns that ref.
		extra func(t *testing.T, ws string) string
	}{
		{"refs/stash", func(t *testing.T, ws string) string {
			if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("changed"), 0o644); err != nil { //nolint:gosec // test file
				t.Fatal(err)
			}
			wsGit(t, ws, "stash", "-q")
			return "refs/stash"
		}},
		{"tag", func(t *testing.T, ws string) string {
			c := wsGit(t, ws, "commit-tree", "-p", "HEAD", "-m", "only in a tag", "HEAD^{tree}")
			wsGit(t, ws, "tag", "only-tag", c)
			return "refs/tags/only-tag"
		}},
		{"refs/agent/x", func(t *testing.T, ws string) string {
			c := wsGit(t, ws, "commit-tree", "-p", "HEAD", "-m", "only in refs/agent/x", "HEAD^{tree}")
			wsGit(t, ws, "update-ref", "refs/agent/x", c)
			return "refs/agent/x"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newHandOverPushEnv(t)
			logs := captureLog(t)
			commitFile(t, e.ws, "a.txt", "one")
			if err := e.git.Push(context.Background(), e.branch, handOverTestTask); err != nil {
				t.Fatalf("Push: %v", err)
			}
			ref := tc.extra(t, e.ws)
			sha := wsGit(t, e.ws, "rev-parse", ref)
			if out, err := exec.Command("git", "-C", e.repo, "cat-file", "-e", sha).CombinedOutput(); err == nil {
				t.Fatalf("the daemon's repository has the commit of %s: %s", ref, out)
			}

			// The end of the run: the push succeeded, the commit of ref is
			// only in the workspace.
			e.cleanup()
			assertWorkspaceKept(t, logs.String(), e.ws, agentWorkspaceTipUnsafe)

			// The sweep after a daemon start: no record.
			logs.Reset()
			if removed, _ := sweepAgentWorkspaces(e.repo, ""); removed != 0 {
				t.Errorf("sweep removed %d, want 0", removed)
			}
			assertWorkspaceKept(t, logs.String(), e.ws, agentWorkspaceTipUnsafe)
		})
	}
}

func TestAgentWorkspacePruneSkipsALiveRun(t *testing.T) {
	e := newHandOverEnv(t)
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// No commit above the base: safe, so only the live record keeps it.
	if removed, err := PruneOrphanedWorktreeForTask(context.Background(), e.repo, handOverTestTask); err != nil || removed != 0 {
		t.Fatalf("PruneOrphanedWorktreeForTask = %d, %v; want 0, nil", removed, err)
	}
	assertPresence(t, nil, []string{e.ws})
	if skipped := logLines(logs.String(), "DEBUG", "skipped the workspace of a live run"); len(skipped) != 1 || !strings.Contains(skipped[0], "path="+e.ws) {
		t.Errorf("Debug lines %q: want one that names %s", skipped, e.ws)
	}

	forgetAgentWorkspaceRecord(t, e.ws)
	if removed, err := PruneOrphanedWorktreeForTask(context.Background(), e.repo, handOverTestTask); err != nil || removed != 1 {
		t.Fatalf("PruneOrphanedWorktreeForTask with no record = %d, %v; want 1, nil", removed, err)
	}
	assertPresence(t, []string{e.ws}, nil)
}

func TestAgentWorkspaceSweepNoRecordLocalBranchBase(t *testing.T) {
	e := newAgentWorkspaceEnv(t)
	logs := captureLog(t)
	base := wsGit(t, e.repo, "commit-tree", "-p", "HEAD", "-m", "only on a local branch", "HEAD^{tree}")
	wsGit(t, e.repo, "update-ref", "refs/heads/feature", base)
	if out := wsGit(t, e.repo, "rev-list", base, "--not", "--glob=refs/remotes/"); out == "" {
		t.Fatal("the base is in refs/remotes/ of the daemon's repository")
	}
	m := NewWorktreeManager(e.repo)
	m.SetBaseBranch("main")
	ws, cleanup, err := m.createAgentWorkspace(context.Background(), "GH-1", "pilot/GH-1", "feature")
	if err != nil {
		t.Fatalf("createAgentWorkspace: %v", err)
	}
	t.Cleanup(cleanup)
	forgetAgentWorkspaceRecord(t, ws)

	if removed, _ := sweepAgentWorkspaces(e.repo, ""); removed != 1 {
		t.Errorf("sweep removed %d, want 1", removed)
	}
	assertPresence(t, []string{ws}, nil)
	if lines := logLines(logs.String(), "ERROR", ""); len(lines) != 0 {
		t.Errorf("Error lines for a safe workspace: %q", lines)
	}
}

func TestAgentWorkspaceSweepUnknownAgeUsesTheDirectoryTime(t *testing.T) {
	tests := []struct {
		name    string
		mtime   time.Duration // before now
		removed int
		line    string
	}{
		{"older than seven days", agentWorkspaceLostAge + time.Hour, 1, "its commits are lost"},
		{"newer", 0, 0, "agent workspace kept"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// NANOSECONDS that do not fit an int64: a workspace name, no age.
			repo, ws := newSafeSweepWorkspace(t, "99999999999999999999999")
			if _, known := agentWorkspaceAge(ws); known {
				t.Fatal("the age of the name is known")
			}
			gitTestCommit(t, ws, "only in the workspace")
			when := time.Now().Add(-tc.mtime)
			if err := os.Chtimes(ws, when, when); err != nil {
				t.Fatal(err)
			}
			logs := captureLog(t)

			if removed, _ := sweepAgentWorkspaces(repo, ""); removed != tc.removed {
				t.Errorf("sweep removed %d, want %d", removed, tc.removed)
			}
			lines := logLines(logs.String(), "ERROR", tc.line)
			if len(lines) != 1 || !strings.Contains(lines[0], "path="+ws) || !strings.Contains(lines[0], " age=") || strings.Contains(lines[0], " age=0s") {
				t.Errorf("Error lines %q: want one with the path and the age of the directory", lines)
			}
			if tc.removed == 1 {
				assertPresence(t, []string{ws}, nil)
			} else {
				assertPresence(t, nil, []string{ws})
			}
		})
	}
}

func TestAgentWorkspaceSweepTwoAtOnceCountOneRemoval(t *testing.T) {
	repo, ws := newSafeSweepWorkspace(t, "100")
	logs := captureLog(t)
	var wg sync.WaitGroup
	counts := make([]int, 2)
	start := make(chan struct{})
	for i := range counts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			counts[i], _ = sweepAgentWorkspaces(repo, "")
		}()
	}
	close(start)
	wg.Wait()
	if sum := counts[0] + counts[1]; sum != 1 {
		t.Errorf("sweeps removed %v, want a sum of 1", counts)
	}
	assertPresence(t, []string{ws}, nil)
	if n := len(logLines(logs.String(), "INFO", "Removed orphaned agent workspace")); n != 1 {
		t.Errorf("%d Info lines for the removal, want 1:\n%s", n, logs.String())
	}
}

func TestAgentWorkspaceTipSafeRefusesOddWorkspaces(t *testing.T) {
	repo, ws := newSafeSweepWorkspace(t, "100")
	ctx := context.Background()
	if !agentWorkspaceTipSafe(ctx, ws, repo) {
		t.Fatal("the clone is not safe")
	}
	root := filepath.Dir(ws)

	t.Run(".git is a link", func(t *testing.T) {
		p := filepath.Join(root, "pilot-ws-link")
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(ws, ".git"), filepath.Join(p, ".git")); err != nil {
			t.Fatal(err)
		}
		if agentWorkspaceTipSafe(ctx, p, repo) {
			t.Error("safe with .git as a symbolic link")
		}
	})
	t.Run(".git is a file", func(t *testing.T) {
		p := filepath.Join(root, "pilot-ws-file")
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, ".git"), []byte("gitdir: "+filepath.Join(ws, ".git")+"\n"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatal(err)
		}
		if wsGit(t, p, "rev-parse", "HEAD") == "" {
			t.Fatal("git does not read the .git file")
		}
		if agentWorkspaceTipSafe(ctx, p, repo) {
			t.Error("safe with .git as a file")
		}
	})
	// The tips are the agent's text: an option would reach the walk in the
	// daemon's repository (--max-count=0 lists nothing).
	for _, tip := range []string{strings.Repeat("g", 40), "--max-count=0"} {
		t.Run("tip not hex "+tip, func(t *testing.T) {
			_ = mockSudoAgent(t, "", `case "$*" in *"rev-parse HEAD --all"*) printf '%s\n' '`+tip+`'; exit 0 ;; esac`)
			if agentWorkspaceTipSafe(ctx, ws, repo) {
				t.Errorf("safe with the tip %q", tip)
			}
		})
	}
}
