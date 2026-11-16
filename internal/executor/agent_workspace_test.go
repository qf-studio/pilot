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

func gitTestCommit(t *testing.T, dir, msg string) string {
	t.Helper()
	wsGit(t, dir, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-q", "--allow-empty", "-m", msg)
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
	mk := func(name string, dir bool) string {
		p := filepath.Join(root, name)
		if dir {
			if err := os.MkdirAll(filepath.Join(p, "src"), 0o755); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(p, []byte("bundle"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f.a1 = mk("pilot-ws-"+pa+"-GH-1-100", true)
	f.a1Bundle = mk("pilot-ws-"+pa+"-GH-1-100.bundle", false)
	f.a2 = mk("pilot-ws-"+pa+"-GH-2-200", true)
	f.a12 = mk("pilot-ws-"+pa+"-GH-1-2-300", true)
	f.b1 = mk("pilot-ws-"+pb+"-GH-1-400", true)
	f.other = mk("not-a-workspace", true)
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

// TestRunAsUserPreflightRefusal is item 1: until the hand-over leg and the
// gates leg merge, the preflight fails for every host, a host that passes
// every check included, with one Error line naming the legs, and no git
// command in the workspace root runs.
func TestRunAsUserPreflightRefusal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file owners")
	}
	me := strconv.Itoa(os.Getuid())
	tests := []struct {
		name    string
		refusal bool
		root    func(t *testing.T) string
		wantErr string // empty: passes
	}{
		{"refusal on, a host that passes every check", true, func(t *testing.T) string { return t.TempDir() }, runAsIncompleteCheck},
		{"refusal on, a host without the root", true, func(t *testing.T) string { return filepath.Join(t.TempDir(), "gone") }, runAsIncompleteCheck},
		{"refusal off, a host that passes every check", false, func(t *testing.T) string { return t.TempDir() }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prev := runAsIncompleteRefusal
			runAsIncompleteRefusal = tc.refusal
			t.Cleanup(func() { runAsIncompleteRefusal = prev })
			root := tc.root(t)
			_ = mockSudoAgent(t, "", "")
			buf, err := workspacePreflight(t, root, me, nil, true, 0)
			if tc.wantErr == "" {
				if err != nil || strings.Contains(buf.String(), "level=ERROR") {
					t.Fatalf("preflight failed: %v\n%s", err, buf.String())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("preflight error = %v, want one naming %q", err, tc.wantErr)
			}
			if n := strings.Count(buf.String(), "level=ERROR"); n != 1 || !strings.Contains(buf.String(), "hand-over leg and the gates leg") {
				t.Errorf("want one Error line naming the legs, got:\n%s", buf.String())
			}
			if cmd := gitCommandIn(context.Background(), root, "status"); cmd.Err == nil {
				t.Errorf("a git command in the workspace root was built after the refusal: %q", cmd.Args)
			}
		})
	}
}
