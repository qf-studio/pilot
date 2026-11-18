package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/procgroup"
)

// TASK-651: the hand-over of the commits of the branch out of the agent
// workspace into a ref of the daemon's repository. No test needs root: sudo is
// the mock of the workspace tests, for which the daemon's repository is
// unreadable.

// handOverEnv is an agent workspace with the task branch at the base commit.
type handOverEnv struct {
	*agentWorkspaceEnv
	ws, branch, base string
	// cleanup is the cleanup of the workspace that createAgentWorkspace
	// returned; it also runs at the end of the test.
	cleanup func()
}

const handOverTestTask = "GH-1"

func newHandOverEnv(t *testing.T) *handOverEnv {
	t.Helper()
	e := newAgentWorkspaceEnv(t)
	m := NewWorktreeManager(e.repo)
	m.SetBaseBranch("main")
	branch := "pilot/GH-1"
	ws, cleanup, err := m.createAgentWorkspace(context.Background(), handOverTestTask, branch, "")
	if err != nil {
		t.Fatalf("createAgentWorkspace: %v", err)
	}
	t.Cleanup(cleanup)
	return &handOverEnv{agentWorkspaceEnv: e, ws: ws, branch: branch, base: wsGit(t, e.repo, "rev-parse", "HEAD"), cleanup: cleanup}
}

// commitFile commits name with content on the checked-out branch of dir.
func commitFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
	wsGit(t, dir, "add", name)
	return gitTestCommit(t, dir, "add "+name)
}

// daemonRefs lists the refs of the daemon's repository with their objects.
func (e *handOverEnv) daemonRefs(t *testing.T) []string {
	t.Helper()
	return strings.Split(wsGit(t, e.repo, "for-each-ref", "--format=%(refname) %(objectname)"), "\n")
}

// assertNoHandOverRef checks that the daemon's repository holds no ref under
// refs/pilot/handover/.
func (e *handOverEnv) assertNoHandOverRef(t *testing.T) {
	t.Helper()
	if refs := wsGit(t, e.repo, "for-each-ref", handOverRefPrefix); refs != "" {
		t.Errorf("hand-over ref left in the daemon repository: %s", refs)
	}
}

// handOverTmpDir points the temporary directory of the daemon at an empty
// directory and returns it.
func handOverTmpDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, en := range entries {
			names = append(names, en.Name())
		}
		t.Errorf("temporary files left in %s: %q", dir, names)
	}
}

func writeGitHook(t *testing.T, gitDir, name, marker string) {
	t.Helper()
	hooks := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("#!/bin/sh\necho %s >> '%s'\n", name, marker)
	if err := os.WriteFile(filepath.Join(hooks, name), []byte(body), 0o755); err != nil { //nolint:gosec // hook must be executable
		t.Fatal(err)
	}
}

func TestHandOverBranch(t *testing.T) {
	tests := []struct {
		name string
		// setup changes the workspace (as the agent would) and returns the
		// branch to hand over, the head the daemon must get ("" when the
		// hand-over must fail) and the shell code of the mock sudo.
		setup func(t *testing.T, e *handOverEnv) (branch, wantSHA, sudoExtra string)
		// check runs after a successful hand-over.
		check func(t *testing.T, e *handOverEnv, sha string, before []string)
	}{
		{
			name: "head and commits arrive",
			setup: func(t *testing.T, e *handOverEnv) (string, string, string) {
				commitFile(t, e.ws, "a.txt", "one")
				return e.branch, commitFile(t, e.ws, "b.txt", "two"), ""
			},
			check: func(t *testing.T, e *handOverEnv, sha string, _ []string) {
				if got := wsGit(t, e.repo, "rev-list", "--count", e.base+".."+sha); got != "2" {
					t.Errorf("daemon repository has %s commits after the base, want 2", got)
				}
				if got := wsGit(t, e.repo, "show", sha+":b.txt"); got != "two" {
					t.Errorf("b.txt in the daemon repository = %q", got)
				}
				var bundle, fetch int
				for _, c := range sudoCalls(t, e.sudoLog) {
					if strings.Contains(c, " bundle create - ") {
						bundle++
					}
					if strings.Contains(c, " fetch ") || strings.Contains(c, " rev-parse ") {
						fetch++
					}
				}
				if bundle != 1 || fetch != 0 {
					t.Errorf("sudo calls %q: want the bundle command as the agent user once and no fetch or rev-parse", sudoCalls(t, e.sudoLog))
				}
			},
		},
		{
			name: "hooks of the workspace and of the daemon repository do not run",
			setup: func(t *testing.T, e *handOverEnv) (string, string, string) {
				marker := filepath.Join(t.TempDir(), "marker")
				writeGitHook(t, filepath.Join(e.ws, ".git"), "pre-push", marker)
				writeGitHook(t, filepath.Join(e.ws, ".git"), "post-checkout", marker)
				writeGitHook(t, filepath.Join(e.repo, ".git"), "reference-transaction", marker)
				writeGitHook(t, filepath.Join(e.repo, ".git"), "post-checkout", marker)
				t.Cleanup(func() {
					if raw, err := os.ReadFile(marker); err == nil {
						t.Errorf("a hook ran during the hand-over: %q", raw)
					}
				})
				return e.branch, commitFile(t, e.ws, "a.txt", "one"), ""
			},
		},
		{
			name: "a replace ref in the workspace does not change the objects",
			setup: func(t *testing.T, e *handOverEnv) (string, string, string) {
				real := commitFile(t, e.ws, "a.txt", "real")
				wsGit(t, e.ws, "checkout", "-q", "-b", "fake", e.base)
				fake := commitFile(t, e.ws, "a.txt", "fake")
				wsGit(t, e.ws, "checkout", "-q", e.branch)
				wsGit(t, e.ws, "replace", real, fake)
				if got := wsGit(t, e.ws, "show", "HEAD:a.txt"); got != "fake" {
					t.Fatalf("replace ref not in effect in the workspace: %q", got)
				}
				return e.branch, real, ""
			},
			check: func(t *testing.T, e *handOverEnv, sha string, _ []string) {
				if got := wsGit(t, e.repo, "--no-replace-objects", "show", sha+":a.txt"); got != "real" {
					t.Errorf("a.txt of the handed-over head = %q, want the real content", got)
				}
				if refs := wsGit(t, e.repo, "for-each-ref", "refs/replace/"); refs != "" {
					t.Errorf("replace ref reached the daemon repository: %s", refs)
				}
			},
		},
		{
			name: "a second ref in the bundle reaches no ref of the daemon",
			setup: func(t *testing.T, e *handOverEnv) (string, string, string) {
				head := commitFile(t, e.ws, "a.txt", "one")
				// The agent answers the bundle command with a bundle that
				// also holds a tag and a forged origin/main.
				extra := fmt.Sprintf(`case "$*" in
*"bundle create - "*)
  git tag -f forged-tag HEAD >/dev/null 2>&1
  git update-ref refs/remotes/origin/main HEAD
  exec git bundle create - refs/heads/%s refs/tags/forged-tag refs/remotes/origin/main ^%s ;;
esac`, e.branch, e.base)
				return e.branch, head, extra
			},
			check: func(t *testing.T, e *handOverEnv, sha string, before []string) {
				want := append(append([]string{}, before...), handOverRef(handOverTestTask)+" "+sha)
				got := e.daemonRefs(t)
				slices.Sort(got)
				slices.Sort(want)
				if !slices.Equal(got, want) {
					t.Errorf("daemon refs after the hand-over:\n%s\nwant the refs before plus the hand-over ref:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
				}
			},
		},
		{
			name: "a branch the workspace does not have",
			setup: func(t *testing.T, e *handOverEnv) (string, string, string) {
				commitFile(t, e.ws, "a.txt", "one")
				return "pilot/GH-404", "", ""
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newHandOverEnv(t)
			branch, wantSHA, extra := tc.setup(t, e)
			// A fresh log: only the calls of the hand-over.
			e.sudoLog = mockSudoAgent(t, e.repo, extra)
			tmp := handOverTmpDir(t)
			before := e.daemonRefs(t)

			sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, branch, e.base)
			assertEmptyDir(t, tmp)
			// Whatever the outcome: no ref of the daemon's repository changes
			// besides the hand-over ref.
			known := map[string]bool{}
			for _, r := range before {
				known[r] = true
			}
			for _, r := range e.daemonRefs(t) {
				if !known[r] && !strings.HasPrefix(r, handOverRef(handOverTestTask)+" ") {
					t.Errorf("the hand-over wrote %q into the daemon repository", r)
				}
			}
			if wantSHA == "" {
				if err == nil {
					t.Fatalf("handOverBranch = %s, want an error", sha)
				}
				e.assertNoHandOverRef(t)
				return
			}
			if err != nil {
				t.Fatalf("handOverBranch: %v", err)
			}
			if sha != wantSHA {
				t.Errorf("head in the daemon repository = %s, want the workspace head %s", sha, wantSHA)
			}
			if got := wsGit(t, e.repo, "rev-parse", handOverRef(handOverTestTask)); got != wantSHA {
				t.Errorf("%s = %s, want %s", handOverRef(handOverTestTask), got, wantSHA)
			}
			if tc.check != nil {
				tc.check(t, e, sha, before)
			}
		})
	}
}

// mutation pin: remove the size limit of the bundle stream -> this test fails
func TestHandOverBranchSizeLimit(t *testing.T) {
	e := newHandOverEnv(t)
	commitFile(t, e.ws, "a.txt", strings.Repeat("x", 4096))
	// The kill of the agent command: `sudo ... kill -s KILL -- -PGID` reaches
	// the mock as "-PGID" after the "--".
	e.sudoLog = mockSudoAgent(t, e.repo, `case "$1" in -[0-9]*) exec kill -s KILL -- "$1" ;; esac`)
	prev := handOverMaxBundleBytes
	handOverMaxBundleBytes = 64
	t.Cleanup(func() { handOverMaxBundleBytes = prev })
	tmp := handOverTmpDir(t)

	sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
	if err == nil {
		t.Fatalf("handOverBranch = %s over the limit, want an error", sha)
	}
	if !errors.Is(err, errHandOverTooLarge) || !strings.Contains(err.Error(), "64 bytes") {
		t.Errorf("error = %v, want the size limit of 64 bytes", err)
	}
	assertEmptyDir(t, tmp)
	e.assertNoHandOverRef(t)
	var killed bool
	for _, c := range sudoCalls(t, e.sudoLog) {
		killed = killed || strings.Contains(c, "kill -s KILL -- -")
	}
	if !killed {
		t.Errorf("the agent command was not stopped through the sudo kill: %q", sudoCalls(t, e.sudoLog))
	}
}

func TestHandOverRefRemove(t *testing.T) {
	repo, _ := setupTestRepoWithRemote(t)
	ref := handOverRef(handOverTestTask)
	tests := []struct {
		name   string
		exists bool
	}{
		{"existing ref", true},
		{"missing ref is no error", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.exists {
				wsGit(t, repo, "update-ref", ref, "HEAD")
			}
			if err := removeHandOverRef(repo, handOverTestTask); err != nil {
				t.Fatalf("removeHandOverRef: %v", err)
			}
			if refs := wsGit(t, repo, "for-each-ref", ref); refs != "" {
				t.Errorf("ref still present: %s", refs)
			}
		})
	}
}

// handOverStreamSudo is the shell code of the mock sudo that answers the
// bundle command of the agent user with the contents of path.
func handOverStreamSudo(path string) string {
	return fmt.Sprintf(`case "$*" in
*"bundle create - "*) exec cat '%s' ;;
esac`, path)
}

// writeHandOverStream writes content into a file of the test and returns it.
func writeHandOverStream(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stream")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// forgedHandOverBundle is a v2 bundle that names refs/heads/BRANCH with the
// object obj and holds the objects of obj ^base, with base as prerequisite
// unless base is "". Built from git pack-objects in dir, so the header can
// name any object, a tag included.
func forgedHandOverBundle(t *testing.T, dir, branch, obj, base string) string {
	t.Helper()
	header := "# v2 git bundle\n"
	revs := obj + "\n"
	if base != "" {
		header += "-" + base + "\n"
		revs += "^" + base + "\n"
	}
	header += obj + " refs/heads/" + branch + "\n\n"
	cmd := exec.Command("git", "-C", dir, "pack-objects", "--stdout", "--revs", "-q")
	cmd.Stdin = strings.NewReader(revs)
	pack, err := cmd.Output()
	if err != nil {
		t.Fatalf("pack-objects: %v", err)
	}
	return writeHandOverStream(t, append([]byte(header), pack...))
}

// runForgedHandOver runs the hand-over with the mock sudo answering the bundle
// command with the file stream and checks that it fails with an error that
// holds want, removes the temporary file and leaves no hand-over ref and no
// new ref in the daemon's repository.
func runForgedHandOver(t *testing.T, e *handOverEnv, stream, want string) error {
	t.Helper()
	e.sudoLog = mockSudoAgent(t, e.repo, handOverStreamSudo(stream))
	tmp := handOverTmpDir(t)
	before := e.daemonRefs(t)
	sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
	if err == nil {
		t.Fatalf("handOverBranch = %s, want an error", sha)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want %q", err, want)
	}
	assertEmptyDir(t, tmp)
	e.assertNoHandOverRef(t)
	if got := e.daemonRefs(t); !slices.Equal(got, before) {
		t.Errorf("daemon refs after the hand-over:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
	return err
}

// mutation pin: remove the call of checkHandOverBundleHeader -> this test fails
func TestHandOverBranchRefusesGitdirStream(t *testing.T) {
	e := newHandOverEnv(t)
	// A second repository the agent user cannot read, with the task branch
	// above the base: served to the daemon, it would pass every later check.
	other := filepath.Join(t.TempDir(), "other")
	wsGit(t, e.repo, "clone", "-q", e.repo, other)
	wsGit(t, other, "checkout", "-q", "-b", e.branch)
	secret := commitFile(t, other, "secret.txt", "secret")
	stream := writeHandOverStream(t, []byte("gitdir: "+filepath.Join(other, ".git")+"\n"))
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("GIT_TRACE", trace)

	err := runForgedHandOver(t, e, stream, errHandOverNotBundle.Error())
	if !errors.Is(err, errHandOverNotBundle) {
		t.Errorf("error = %v, want errHandOverNotBundle", err)
	}
	if raw, _ := os.ReadFile(trace); strings.Contains(string(raw), "upload-pack") {
		t.Errorf("git-upload-pack ran during the hand-over:\n%s", raw)
	}
	if exec.Command("git", "-C", e.repo, "cat-file", "-e", secret).Run() == nil {
		t.Errorf("commit %s of the second repository reached the daemon repository", secret)
	}
}

func TestHandOverBranchRefusesNonBundleStreams(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"empty stream", ""},
		{"garbage stream", "not a bundle at all\n\x00\x01\x02"},
		{"short signature", "# v2 git bun"},
		{"v1 signature", "# v1 git bundle\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newHandOverEnv(t)
			commitFile(t, e.ws, "a.txt", "one")
			err := runForgedHandOver(t, e, writeHandOverStream(t, []byte(tc.content)), errHandOverNotBundle.Error())
			if !errors.Is(err, errHandOverNotBundle) {
				t.Errorf("error = %v, want errHandOverNotBundle", err)
			}
		})
	}
}

func TestCheckHandOverBundleHeader(t *testing.T) {
	for _, tc := range []struct {
		content string
		ok      bool
	}{
		{"# v2 git bundle\n", true},
		{"# v3 git bundle\n@object-format=sha256\n", true},
		{"# v2 git bundle", false},
		{"# v4 git bundle\n", false},
		{"gitdir: /x/.git\n", false},
		{"", false},
	} {
		err := checkHandOverBundleHeader(writeHandOverStream(t, []byte(tc.content)))
		if (err == nil) != tc.ok {
			t.Errorf("checkHandOverBundleHeader(%q) = %v, want ok=%v", tc.content, err, tc.ok)
		}
	}
}

func TestHandOverBranchRefusesBranchAtBase(t *testing.T) {
	e := newHandOverEnv(t)
	// The branch of the workspace is at the base: a bundle of the whole
	// history names the base itself.
	_ = runForgedHandOver(t, e, forgedHandOverBundle(t, e.ws, e.branch, e.base, ""), "above the base")
}

// mutation pin: remove the ancestor check -> this test fails
func TestHandOverBranchRefusesOrphanHistory(t *testing.T) {
	e := newHandOverEnv(t)
	orphan := wsGit(t, e.ws, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit-tree", "-m", "orphan", "HEAD^{tree}")
	_ = runForgedHandOver(t, e, forgedHandOverBundle(t, e.ws, e.branch, orphan, ""), "not an ancestor")
	if exec.Command("git", "-C", e.repo, "merge-base", "--is-ancestor", e.base, orphan).Run() == nil {
		t.Fatal("the orphan commit has the base as an ancestor; the fixture is wrong")
	}
}

func TestHandOverBranchRefusesAnnotatedTag(t *testing.T) {
	e := newHandOverEnv(t)
	commitFile(t, e.ws, "a.txt", "one")
	wsGit(t, e.ws, "-c", "user.email=t@example.com", "-c", "user.name=T", "tag", "-a", "-m", "tag", "forged", "HEAD")
	tag := wsGit(t, e.ws, "rev-parse", "refs/tags/forged")
	_ = runForgedHandOver(t, e, forgedHandOverBundle(t, e.ws, e.branch, tag, e.base), "must be a commit")
}

func TestHandOverBranchCancelDuringFetch(t *testing.T) {
	e := newHandOverEnv(t)
	commitFile(t, e.ws, "a.txt", "one")
	e.sudoLog = mockSudoAgent(t, e.repo, "")
	tmp := handOverTmpDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The fetch is slow and has a child that holds the output pipe, as
	// index-pack does: only a kill of the group ends the wait early.
	prev := execCommandContext
	execCommandContext = func(c context.Context, name string, args ...string) *exec.Cmd {
		if name == "git" && slices.Contains(args, "fetch") {
			time.AfterFunc(200*time.Millisecond, cancel)
			return prev(c, "sh", "-c", "sleep 60 & sleep 60")
		}
		return prev(c, name, args...)
	}
	t.Cleanup(func() { execCommandContext = prev })

	start := time.Now()
	sha, err := handOverBranch(ctx, e.ws, e.repo, handOverTestTask, e.branch, e.base)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("handOverBranch = %s after the cancel, want an error", sha)
	}
	if elapsed >= procgroup.DefaultWaitDelay {
		t.Errorf("handOverBranch returned %v after the start, want within the wait delay %v", elapsed, procgroup.DefaultWaitDelay)
	}
	assertEmptyDir(t, tmp)
	e.assertNoHandOverRef(t)
}

func TestHandOverBranchIgnoresGitDir(t *testing.T) {
	e := newHandOverEnv(t)
	head := commitFile(t, e.ws, "a.txt", "one")
	e.sudoLog = mockSudoAgent(t, e.repo, "")
	tmp := handOverTmpDir(t)
	other := t.TempDir()
	wsGit(t, other, "init", "-q")
	otherRefs := wsGit(t, other, "for-each-ref")

	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", t.TempDir())
	sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
	for _, name := range []string{"GIT_DIR", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
		_ = os.Unsetenv(name)
	}
	if err != nil {
		t.Fatalf("handOverBranch with GIT_DIR of another repository: %v", err)
	}
	if sha != head {
		t.Errorf("handOverBranch = %s, want %s", sha, head)
	}
	if got := wsGit(t, e.repo, "rev-parse", handOverRef(handOverTestTask)); got != head {
		t.Errorf("%s in the daemon repository = %s, want %s", handOverRef(handOverTestTask), got, head)
	}
	if got := wsGit(t, other, "for-each-ref"); got != otherRefs {
		t.Errorf("refs of the other repository changed: %q, want %q", got, otherRefs)
	}
	assertEmptyDir(t, tmp)
}
