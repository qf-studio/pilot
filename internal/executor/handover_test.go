package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TASK-651: the hand-over of the commits of the branch out of the agent
// workspace into a ref of the daemon's repository. No test needs root: sudo is
// the mock of the workspace tests, for which the daemon's repository is
// unreadable.

// handOverEnv is an agent workspace with the task branch at the base commit.
type handOverEnv struct {
	*agentWorkspaceEnv
	ws, branch, base string
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
	return &handOverEnv{agentWorkspaceEnv: e, ws: ws, branch: branch, base: wsGit(t, e.repo, "rev-parse", "HEAD")}
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
