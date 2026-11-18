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

// packFiles lists objects/pack of the daemon's repository.
func (e *handOverEnv) packFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.repo, ".git", "objects", "pack"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

// assertNothingLeft checks the daemon's repository after a failed hand-over:
// no hand-over ref, the file list of objects/pack as before and no quarantine
// directory.
func (e *handOverEnv) assertNothingLeft(t *testing.T, packsBefore []string) {
	t.Helper()
	e.assertNoHandOverRef(t)
	if got := e.packFiles(t); !slices.Equal(got, packsBefore) {
		t.Errorf("objects/pack after the hand-over: %q, want %q", got, packsBefore)
	}
	quarantines, err := filepath.Glob(filepath.Join(e.repo, ".git", "objects", handOverQuarantinePrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(quarantines) != 0 {
		t.Errorf("quarantine directories left: %q", quarantines)
	}
}

// daemonOnlyCommit commits name with content on a branch of the daemon's
// repository that the workspace does not have, above the base, and returns it.
func (e *handOverEnv) daemonOnlyCommit(t *testing.T, name, content string) string {
	t.Helper()
	wsGit(t, e.repo, "checkout", "-q", "-b", "private", e.base)
	sha := commitFile(t, e.repo, name, content)
	wsGit(t, e.repo, "checkout", "-q", "main")
	return sha
}

// cloneOfDaemon is a clone of the daemon's repository, so it holds the
// commits that only the daemon's repository has, for forged packs.
func (e *handOverEnv) cloneOfDaemon(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	wsGit(t, e.repo, "clone", "-q", "--no-local", e.repo, dir)
	return dir
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
				commitFile(t, e.ws, "b.txt", "two")
				return e.branch, commitFile(t, e.ws, "c.txt", "three"), ""
			},
			check: func(t *testing.T, e *handOverEnv, sha string, _ []string) {
				if got := wsGit(t, e.repo, "rev-list", "--count", e.base+".."+sha); got != "3" {
					t.Errorf("daemon repository has %s commits after the base, want 3", got)
				}
				if got := wsGit(t, e.repo, "show", sha+":c.txt"); got != "three" {
					t.Errorf("c.txt in the daemon repository = %q", got)
				}
				var pack, daemonSide int
				for _, c := range sudoCalls(t, e.sudoLog) {
					if strings.Contains(c, " pack-objects --stdout --revs ") && !strings.Contains(c, "--thin") {
						pack++
					}
					for _, cmd := range []string{" fetch ", " index-pack ", " cat-file ", " update-ref ", " bundle "} {
						if strings.Contains(c, cmd) {
							daemonSide++
						}
					}
				}
				if pack != 1 || daemonSide != 0 {
					t.Errorf("sudo calls %q: want pack-objects (not thin) as the agent user once and no command of the daemon side", sudoCalls(t, e.sudoLog))
				}
				if out, err := exec.Command("git", "-C", e.repo, "fsck", "--connectivity-only", "--no-dangling").CombinedOutput(); err != nil {
					t.Errorf("git fsck of the daemon repository: %v: %s", err, out)
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
			packsBefore := e.packFiles(t)

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
				e.assertNothingLeft(t, packsBefore)
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

// mutation pin: remove the size limit of the pack stream -> this test fails
func TestHandOverBranchSizeLimit(t *testing.T) {
	e := newHandOverEnv(t)
	commitFile(t, e.ws, "a.txt", strings.Repeat("x", 4096))
	// The kill of the agent command: `sudo ... kill -s KILL -- -PGID` reaches
	// the mock as "-PGID" after the "--". pack-objects stays alive after git
	// exits: a small pack fits in the pipe, and git could exit before the
	// cancel, so only the sudo kill ends it.
	e.sudoLog = mockSudoAgent(t, e.repo, `case "$1" in -[0-9]*) exec kill -s KILL -- "$1" ;; esac
case "$*" in *pack-objects*) "$@"; exec sleep 60 ;; esac`)
	prev := handOverMaxPackBytes
	handOverMaxPackBytes = 64
	t.Cleanup(func() { handOverMaxPackBytes = prev })
	tmp := handOverTmpDir(t)
	packsBefore := e.packFiles(t)

	sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
	if err == nil {
		t.Fatalf("handOverBranch = %s over the limit, want an error", sha)
	}
	if !errors.Is(err, errHandOverTooLarge) || !strings.Contains(err.Error(), "64 bytes") {
		t.Errorf("error = %v, want the size limit of 64 bytes", err)
	}
	assertEmptyDir(t, tmp)
	e.assertNothingLeft(t, packsBefore)
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
// pack-objects command of the agent user with the contents of path. With tip
// set, the agent also answers the count with 1 and the rev-parse with tip.
func handOverStreamSudo(path, tip string) string {
	answers := ""
	if tip != "" {
		answers = fmt.Sprintf(`*" rev-list --count "*) echo 1; exit 0 ;;
*" rev-parse --verify "*) echo %s; exit 0 ;;
`, tip)
	}
	return fmt.Sprintf(`case "$*" in
%s*" pack-objects "*) exec cat '%s' ;;
esac`, answers, path)
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

// handOverPackBytes is the output of `git pack-objects --stdout` in dir with
// the lines on its standard input (args before them, such as --revs).
func handOverPackBytes(t *testing.T, dir string, args []string, lines ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "pack-objects", "--stdout", "-q"}, args...)...)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	pack, err := cmd.Output()
	if err != nil {
		t.Fatalf("pack-objects %v: %v", args, err)
	}
	return pack
}

// forgedHandOverPack is a pack of `git pack-objects --stdout --revs` in dir
// with the rev lines, in a file of the test.
func forgedHandOverPack(t *testing.T, dir string, revs ...string) string {
	t.Helper()
	return writeHandOverStream(t, handOverPackBytes(t, dir, []string{"--revs"}, revs...))
}

// gitCommandsSeen wraps execCommandContext for one test and returns the
// argument lists of the git commands of the daemon side it built.
func gitCommandsSeen(t *testing.T) *[]string {
	t.Helper()
	var seen []string
	prev := execCommandContext
	execCommandContext = func(c context.Context, name string, args ...string) *exec.Cmd {
		if name == "git" {
			seen = append(seen, strings.Join(args, " "))
		}
		return prev(c, name, args...)
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &seen
}

// runForgedHandOver runs the hand-over with the mock sudo answering the
// pack-objects command with the file stream (and the tip, when set) and
// checks that it fails with an error that holds want, removes the temporary
// file and leaves no hand-over ref, no new ref, no quarantine and the file
// list of objects/pack of before in the daemon's repository.
func runForgedHandOver(t *testing.T, e *handOverEnv, stream, tip, want string) error {
	t.Helper()
	e.sudoLog = mockSudoAgent(t, e.repo, handOverStreamSudo(stream, tip))
	tmp := handOverTmpDir(t)
	before := e.daemonRefs(t)
	packsBefore := e.packFiles(t)
	sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
	if err == nil {
		t.Fatalf("handOverBranch = %s, want an error", sha)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want %q", err, want)
	}
	assertEmptyDir(t, tmp)
	e.assertNothingLeft(t, packsBefore)
	if got := e.daemonRefs(t); !slices.Equal(got, before) {
		t.Errorf("daemon refs after the hand-over:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
	return err
}

// mutation pin: remove the call of checkHandOverPackHeader -> this test fails
func TestHandOverBranchRefusesNonPackStreams(t *testing.T) {
	for _, tc := range []struct {
		name string
		// stream builds the stream the agent sends.
		stream func(t *testing.T, e *handOverEnv) []byte
	}{
		{"gitdir stream", func(t *testing.T, e *handOverEnv) []byte {
			// A second repository the agent user cannot read, with the task
			// branch above the base.
			other := filepath.Join(t.TempDir(), "other")
			wsGit(t, e.repo, "clone", "-q", e.repo, other)
			wsGit(t, other, "checkout", "-q", "-b", e.branch)
			commitFile(t, other, "secret.txt", "secret")
			return []byte("gitdir: " + filepath.Join(other, ".git") + "\n")
		}},
		{"empty stream", func(*testing.T, *handOverEnv) []byte { return nil }},
		{"bundle file", func(t *testing.T, e *handOverEnv) []byte {
			path := filepath.Join(t.TempDir(), "b.bundle")
			wsGit(t, e.ws, "bundle", "create", "-q", path, "refs/heads/"+e.branch, "^"+e.base)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			return raw
		}},
		{"garbage stream", func(*testing.T, *handOverEnv) []byte { return []byte("not a pack at all\n\x00\x01\x02") }},
		{"version 3 pack", func(*testing.T, *handOverEnv) []byte { return []byte("PACK\x00\x00\x00\x03\x00\x00\x00\x00") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newHandOverEnv(t)
			commitFile(t, e.ws, "a.txt", "one")
			stream := writeHandOverStream(t, tc.stream(t, e))
			trace := filepath.Join(t.TempDir(), "trace")
			t.Setenv("GIT_TRACE", trace)
			seen := gitCommandsSeen(t)

			err := runForgedHandOver(t, e, stream, "", errHandOverNotPack.Error())
			if !errors.Is(err, errHandOverNotPack) {
				t.Errorf("error = %v, want errHandOverNotPack", err)
			}
			// The marker: no git command of the daemon read the file.
			for _, c := range *seen {
				for _, cmd := range []string{"index-pack", "fetch", "bundle", "upload-pack"} {
					if strings.Contains(c, cmd) {
						t.Errorf("git %s ran on the stream", c)
					}
				}
			}
			if raw, _ := os.ReadFile(trace); strings.Contains(string(raw), "upload-pack") || strings.Contains(string(raw), "index-pack") {
				t.Errorf("a git command read the stream:\n%s", raw)
			}
		})
	}
}

func TestCheckHandOverPackHeader(t *testing.T) {
	for _, tc := range []struct {
		content string
		ok      bool
	}{
		{"PACK\x00\x00\x00\x02\x00\x00\x00\x00", true},
		{"PACK\x00\x00\x00\x02", true},
		{"PACK\x00\x00\x00\x03", false},
		{"PACK\x00\x00\x00", false},
		{"# v2 git bundle\n", false},
		{"gitdir: /x/.git\n", false},
		{"", false},
	} {
		err := checkHandOverPackHeader(writeHandOverStream(t, []byte(tc.content)))
		if (err == nil) != tc.ok {
			t.Errorf("checkHandOverPackHeader(%q) = %v, want ok=%v", tc.content, err, tc.ok)
		}
		if err != nil && !errors.Is(err, errHandOverNotPack) {
			t.Errorf("checkHandOverPackHeader(%q) = %v, want errHandOverNotPack", tc.content, err)
		}
	}
}

// handOverSecretText is a file large enough that git stores a small change of
// it as a delta against it.
func handOverSecretText() string {
	var b strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "secret line %d of the daemon repository %x\n", i, i*7919)
	}
	return b.String()
}

// mutation pin: add --fix-thin to the index-pack command -> this test fails
func TestHandOverBranchRefusesThinPack(t *testing.T) {
	e := newHandOverEnv(t)
	secret := handOverSecretText()
	private := e.daemonOnlyCommit(t, "secret.txt", secret)
	// The agent has a commit above the private commit whose blob is a delta
	// against the blob of the private commit; a thin pack leaves that base
	// out (pack-objects takes thin bases only from the edge commits).
	clone := e.cloneOfDaemon(t)
	wsGit(t, clone, "checkout", "-q", "-b", "forged", private)
	tip := commitFile(t, clone, "secret.txt", secret+"one more line\n")
	pack := handOverPackBytes(t, clone, []string{"--revs", "--thin"}, tip, "^"+private)
	// The fixture: the pack is thin, it cannot be indexed on its own.
	empty := t.TempDir()
	wsGit(t, empty, "init", "-q")
	idx := exec.Command("git", "-C", empty, "index-pack", "--stdin")
	idx.Stdin = strings.NewReader(string(pack))
	if out, err := idx.CombinedOutput(); err == nil {
		t.Fatalf("the forged pack is not thin: %s", out)
	}

	_ = runForgedHandOver(t, e, writeHandOverStream(t, pack), tip, "index-pack")
	if exec.Command("git", "-C", e.repo, "cat-file", "-e", tip).Run() == nil {
		t.Errorf("the forged commit %s reached the daemon repository", tip)
	}
}

// mutation pin: remove the call of verifyHandOverObjects -> this test fails
func TestHandOverBranchRefusesDaemonOnlyParent(t *testing.T) {
	e := newHandOverEnv(t)
	private := e.daemonOnlyCommit(t, "private.txt", "private")
	// The agent commits on top of the private commit and sends a pack that
	// does not hold it: index-pack finds the parent through the alternate.
	clone := e.cloneOfDaemon(t)
	wsGit(t, clone, "checkout", "-q", "-b", "forged", private)
	tip := commitFile(t, clone, "agent.txt", "agent")
	stream := forgedHandOverPack(t, clone, tip, "^"+private)

	err := runForgedHandOver(t, e, stream, tip, "did not come from the workspace")
	if err != nil && !strings.Contains(err.Error(), private) {
		t.Errorf("error = %v, want the private commit %s named", err, private)
	}
}

func TestHandOverBranchRefusesDamagedPack(t *testing.T) {
	for _, tc := range []struct {
		name string
		// pack returns the stream and the tip the agent names.
		pack func(t *testing.T, e *handOverEnv) ([]byte, string)
	}{
		{"truncated pack", func(t *testing.T, e *handOverEnv) ([]byte, string) {
			tip := commitFile(t, e.ws, "a.txt", handOverSecretText())
			pack := handOverPackBytes(t, e.ws, []string{"--revs"}, tip, "^"+e.base)
			return pack[:len(pack)-40], tip
		}},
		{"pack with a damaged byte", func(t *testing.T, e *handOverEnv) ([]byte, string) {
			tip := commitFile(t, e.ws, "a.txt", handOverSecretText())
			pack := handOverPackBytes(t, e.ws, []string{"--revs"}, tip, "^"+e.base)
			pack[len(pack)/2] ^= 0xff
			return pack, tip
		}},
		{"pack with a corrupt commit", func(t *testing.T, e *handOverEnv) ([]byte, string) {
			tree := wsGit(t, e.ws, "rev-parse", e.base+"^{tree}")
			cmd := exec.Command("git", "-C", e.ws, "hash-object", "-t", "commit", "-w", "--literally", "--stdin")
			cmd.Stdin = strings.NewReader("tree " + tree + "\nparent " + e.base + "\nauthor nobody\ncommitter nobody\n\nbroken\n")
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("hash-object: %v", err)
			}
			tip := strings.TrimSpace(string(out))
			return handOverPackBytes(t, e.ws, nil, tip), tip
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newHandOverEnv(t)
			pack, tip := tc.pack(t, e)
			_ = runForgedHandOver(t, e, writeHandOverStream(t, pack), tip, "index-pack")
		})
	}
}

func TestHandOverBranchUpdateRefFails(t *testing.T) {
	e := newHandOverEnv(t)
	commitFile(t, e.ws, "a.txt", handOverSecretText())
	e.sudoLog = mockSudoAgent(t, e.repo, "")
	tmp := handOverTmpDir(t)
	packsBefore := e.packFiles(t)
	var failed bool
	prev := execCommandContext
	execCommandContext = func(c context.Context, name string, args ...string) *exec.Cmd {
		if name == "git" && slices.Contains(args, "update-ref") && !slices.Contains(args, "-d") {
			failed = true
			return prev(c, "false")
		}
		return prev(c, name, args...)
	}
	t.Cleanup(func() { execCommandContext = prev })

	sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
	if err == nil {
		t.Fatalf("handOverBranch = %s with a failed update-ref, want an error", sha)
	}
	if !failed {
		t.Fatalf("the update-ref seam did not run: %v", err)
	}
	assertEmptyDir(t, tmp)
	e.assertNothingLeft(t, packsBefore)
}

func TestHandOverBranchNothingNew(t *testing.T) {
	e := newHandOverEnv(t)
	e.sudoLog = mockSudoAgent(t, e.repo, "")
	tmp := handOverTmpDir(t)
	packsBefore := e.packFiles(t)

	sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
	if !errors.Is(err, errHandOverNothingNew) {
		t.Fatalf("handOverBranch = %s, %v, want errHandOverNothingNew", sha, err)
	}
	for _, c := range sudoCalls(t, e.sudoLog) {
		if strings.Contains(c, "pack-objects") {
			t.Errorf("pack-objects ran for a branch at the base: %q", c)
		}
	}
	assertEmptyDir(t, tmp)
	e.assertNothingLeft(t, packsBefore)
}

func TestHandOverBranchRefusesTip(t *testing.T) {
	for _, tc := range []struct {
		name string
		// forge returns the pack the agent sends and the tip it names.
		forge func(t *testing.T, e *handOverEnv) (stream, tip string)
		want  string
	}{
		{"annotated tag", func(t *testing.T, e *handOverEnv) (string, string) {
			commitFile(t, e.ws, "a.txt", "one")
			wsGit(t, e.ws, "-c", "user.email=t@example.com", "-c", "user.name=T", "tag", "-a", "-m", "tag", "forged", "HEAD")
			tag := wsGit(t, e.ws, "rev-parse", "refs/tags/forged")
			return forgedHandOverPack(t, e.ws, tag, "^"+e.base), tag
		}, "must be a commit"},
		{"tree", func(t *testing.T, e *handOverEnv) (string, string) {
			head := commitFile(t, e.ws, "a.txt", "one")
			return forgedHandOverPack(t, e.ws, head, "^"+e.base), wsGit(t, e.ws, "rev-parse", head+"^{tree}")
		}, "must be a commit"},
		{"blob", func(t *testing.T, e *handOverEnv) (string, string) {
			head := commitFile(t, e.ws, "a.txt", "one")
			return forgedHandOverPack(t, e.ws, head, "^"+e.base), wsGit(t, e.ws, "rev-parse", head+":a.txt")
		}, "must be a commit"},
		{"the base", func(t *testing.T, e *handOverEnv) (string, string) {
			head := commitFile(t, e.ws, "a.txt", "one")
			return forgedHandOverPack(t, e.ws, head, "^"+e.base), e.base
		}, "above the base"},
		// mutation pin: remove the ancestor check -> this case fails
		{"orphan history", func(t *testing.T, e *handOverEnv) (string, string) {
			orphan := wsGit(t, e.ws, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit-tree", "-m", "orphan", "HEAD^{tree}")
			if exec.Command("git", "-C", e.ws, "merge-base", "--is-ancestor", e.base, orphan).Run() == nil {
				t.Fatal("the orphan commit has the base as an ancestor; the fixture is wrong")
			}
			return forgedHandOverPack(t, e.ws, orphan), orphan
		}, "not an ancestor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newHandOverEnv(t)
			stream, tip := tc.forge(t, e)
			_ = runForgedHandOver(t, e, stream, tip, tc.want)
		})
	}
}

func TestHandOverBranchSweepsOldQuarantines(t *testing.T) {
	e := newHandOverEnv(t)
	head := commitFile(t, e.ws, "a.txt", "one")
	e.sudoLog = mockSudoAgent(t, e.repo, "")
	handOverTmpDir(t)
	objects := filepath.Join(e.repo, ".git", "objects")
	old := filepath.Join(objects, handOverQuarantinePrefix+"old")
	fresh := filepath.Join(objects, handOverQuarantinePrefix+"fresh")
	for _, dir := range []string{old, fresh} {
		if err := os.MkdirAll(filepath.Join(dir, "pack"), 0o755); err != nil { //nolint:gosec // test directory
			t.Fatal(err)
		}
	}
	twoHours := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, twoHours, twoHours); err != nil {
		t.Fatal(err)
	}

	sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
	if err != nil || sha != head {
		t.Fatalf("handOverBranch = %s, %v, want %s", sha, err, head)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the quarantine of over an hour %s is still there: %v", old, err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("the quarantine of under an hour %s was removed: %v", fresh, err)
	}
}

func TestHandOverBranchCancelDuringIndexPack(t *testing.T) {
	e := newHandOverEnv(t)
	commitFile(t, e.ws, "a.txt", "one")
	e.sudoLog = mockSudoAgent(t, e.repo, "")
	tmp := handOverTmpDir(t)
	packsBefore := e.packFiles(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// index-pack is slow and has a child that holds the output pipe: only a
	// kill of the group ends the wait early.
	prev := execCommandContext
	execCommandContext = func(c context.Context, name string, args ...string) *exec.Cmd {
		if name == "git" && slices.Contains(args, "index-pack") {
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
	e.assertNothingLeft(t, packsBefore)
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
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(other, ".git", "objects"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", t.TempDir())
	sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
	for _, name := range []string{"GIT_DIR", "GIT_OBJECT_DIRECTORY", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
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
