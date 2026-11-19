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

// inDaemonEnv sets vars in the environment of the test process, the
// environment the git commands of the daemon side inherit, runs fn and unsets
// them again, so the git commands of the test itself (wsGit) never see them;
// t.Setenv puts the values of before back at the end of the test.
func inDaemonEnv(t *testing.T, vars map[string]string, fn func()) {
	t.Helper()
	for name, value := range vars {
		t.Setenv(name, value)
	}
	defer func() {
		for name := range vars {
			_ = os.Unsetenv(name)
		}
	}()
	fn()
}

// mutation pin: let GIT_COMMON_DIR pass handOverEnvAllowed -> a row of this
// test fails
func TestHandOverDaemonGitEnvAllowList(t *testing.T) {
	for _, tc := range []struct {
		name string
		// vars are the names and values set in the daemon environment.
		vars func(t *testing.T) map[string]string
		// handOver, when set, runs a real hand-over with vars and checks it.
		handOver func(t *testing.T, vars map[string]string)
	}{
		{name: "GIT_DIR", vars: func(t *testing.T) map[string]string {
			return map[string]string{"GIT_DIR": filepath.Join(t.TempDir(), ".git")}
		}},
		{name: "GIT_WORK_TREE", vars: func(t *testing.T) map[string]string {
			return map[string]string{"GIT_WORK_TREE": t.TempDir()}
		}},
		{name: "GIT_INDEX_FILE", vars: func(t *testing.T) map[string]string {
			return map[string]string{"GIT_INDEX_FILE": filepath.Join(t.TempDir(), "index")}
		}},
		{name: "GIT_OBJECT_DIRECTORY", vars: func(t *testing.T) map[string]string {
			return map[string]string{"GIT_OBJECT_DIRECTORY": filepath.Join(t.TempDir(), "objects")}
		}},
		{name: "GIT_ALTERNATE_OBJECT_DIRECTORIES", vars: func(t *testing.T) map[string]string {
			return map[string]string{"GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(t.TempDir(), "objects")}
		}},
		{name: "GIT_NAMESPACE", vars: func(*testing.T) map[string]string {
			return map[string]string{"GIT_NAMESPACE": "forged"}
		}},
		{
			name: "GIT_COMMON_DIR",
			vars: func(t *testing.T) map[string]string {
				other := t.TempDir()
				wsGit(t, other, "init", "-q")
				return map[string]string{"GIT_COMMON_DIR": filepath.Join(other, ".git")}
			},
			// The pack and the ref land in the daemon's repository, nothing
			// in the other one.
			handOver: func(t *testing.T, vars map[string]string) {
				e := newHandOverEnv(t)
				head := commitFile(t, e.ws, "a.txt", "one")
				e.sudoLog = mockSudoAgent(t, e.repo, "")
				tmp := handOverTmpDir(t)
				other := filepath.Dir(vars["GIT_COMMON_DIR"])
				var sha string
				var err error
				inDaemonEnv(t, vars, func() {
					sha, err = handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
				})
				if err != nil || sha != head {
					t.Fatalf("handOverBranch = %s, %v, want %s", sha, err, head)
				}
				if got := wsGit(t, e.repo, "rev-parse", handOverRef(handOverTestTask)); got != head {
					t.Errorf("%s in the daemon repository = %s, want %s", handOverRef(handOverTestTask), got, head)
				}
				if out, err := exec.Command("git", "-C", e.repo, "fsck", "--connectivity-only", "--no-dangling").CombinedOutput(); err != nil {
					t.Errorf("git fsck of the daemon repository: %v: %s", err, out)
				}
				if got := wsGit(t, other, "for-each-ref"); got != "" {
					t.Errorf("refs in the other repository: %s", got)
				}
				if exec.Command("git", "-C", other, "cat-file", "-e", head).Run() == nil {
					t.Errorf("the head %s was written into the other repository", head)
				}
				assertEmptyDir(t, tmp)
			},
		},
		{
			name: "GIT_EXEC_PATH",
			vars: func(t *testing.T) map[string]string {
				return map[string]string{"GIT_EXEC_PATH": t.TempDir()}
			},
			// The daemon's repository is a partial clone and the agent sends
			// a branch on a commit only the daemon has: the quarantine check
			// misses it and git starts a lazy fetch, the git program of its
			// exec path. That program is a marker script: it never runs.
			handOver: func(t *testing.T, vars map[string]string) {
				e := newHandOverEnv(t)
				marker := filepath.Join(t.TempDir(), "marker")
				body := fmt.Sprintf("#!/bin/sh\necho \"$0 $*\" >> '%s'\nexit 1\n", marker)
				if err := os.WriteFile(filepath.Join(vars["GIT_EXEC_PATH"], "git"), []byte(body), 0o755); err != nil { //nolint:gosec // the marker must be executable
					t.Fatal(err)
				}
				private := e.daemonOnlyCommit(t, "private.txt", "private")
				clone := e.cloneOfDaemon(t)
				wsGit(t, clone, "checkout", "-q", "-b", "forged", private)
				tip := commitFile(t, clone, "agent.txt", "agent")
				stream := forgedHandOverPack(t, clone, tip, "^"+private)
				wsGit(t, e.repo, "config", "extensions.partialClone", "promisor")
				wsGit(t, e.repo, "config", "remote.promisor.promisor", "true")
				wsGit(t, e.repo, "config", "remote.promisor.url", filepath.Join(t.TempDir(), "none"))
				allowHandOverPartialClone(t)
				inDaemonEnv(t, vars, func() {
					_ = runForgedHandOver(t, e, stream, tip, "did not come from the workspace")
				})
				if raw, err := os.ReadFile(marker); err == nil {
					t.Errorf("a program of GIT_EXEC_PATH ran during the hand-over: %q", raw)
				}
			},
		},
		{
			name: "GIT_GRAFT_FILE",
			vars: func(t *testing.T) map[string]string {
				return map[string]string{"GIT_GRAFT_FILE": filepath.Join(t.TempDir(), "grafts")}
			},
			// The graft file makes the base the parent of an orphan commit
			// the agent sends: the ancestor check still refuses it.
			handOver: func(t *testing.T, vars map[string]string) {
				e := newHandOverEnv(t)
				orphan := wsGit(t, e.ws, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit-tree", "-m", "orphan", "HEAD^{tree}")
				if err := os.WriteFile(vars["GIT_GRAFT_FILE"], []byte(orphan+" "+e.base+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				stream := forgedHandOverPack(t, e.ws, orphan)
				inDaemonEnv(t, vars, func() {
					_ = runForgedHandOver(t, e, stream, orphan, "not an ancestor")
				})
			},
		},
		{name: "GIT_TRACE", vars: func(t *testing.T) map[string]string {
			return map[string]string{"GIT_TRACE": filepath.Join(t.TempDir(), "trace")}
		}},
		{name: "GIT_CONFIG_GLOBAL", vars: func(t *testing.T) map[string]string {
			return map[string]string{"GIT_CONFIG_GLOBAL": filepath.Join(t.TempDir(), "config")}
		}},
		{name: "GIT_CONFIG_COUNT", vars: func(t *testing.T) map[string]string {
			return map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.hooksPath", "GIT_CONFIG_VALUE_0": t.TempDir()}
		}},
		{name: "GIT_CONFIG_PARAMETERS", vars: func(t *testing.T) map[string]string {
			return map[string]string{"GIT_CONFIG_PARAMETERS": "'core.hookspath'='" + t.TempDir() + "'"}
		}},
		{name: "GIT_SSH_COMMAND", vars: func(t *testing.T) map[string]string {
			return map[string]string{"GIT_SSH_COMMAND": filepath.Join(t.TempDir(), "ssh")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := tc.vars(t)
			repo := t.TempDir()
			q := handOverQuarantine{dir: filepath.Join(repo, "quarantine"), objects: filepath.Join(repo, "objects")}
			cmds := map[string]*exec.Cmd{}
			inDaemonEnv(t, vars, func() {
				cmds["handOverDaemonGit"] = handOverDaemonGit(context.Background(), repo, "status")
				cmds["the quarantine"] = q.git(context.Background(), repo, false, "cat-file", "--batch-check")
				cmds["the quarantine with the alternate"] = q.git(context.Background(), repo, true, "cat-file", "-t", "HEAD")
			})
			for kind, cmd := range cmds {
				if cmd.Err != nil {
					t.Fatalf("%s: %v", kind, cmd.Err)
				}
				for name, value := range vars {
					if slices.Contains(cmd.Env, name+"="+value) {
						t.Errorf("%s: the environment holds %s=%s of the daemon", kind, name, value)
					}
				}
			}
			for _, want := range []string{"GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OBJECT_DIRECTORY=" + q.dir, "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + q.objects} {
				if !slices.Contains(cmds["the quarantine with the alternate"].Env, want) {
					t.Errorf("the quarantine with the alternate: the environment lacks %s", want)
				}
			}
			if tc.handOver != nil {
				tc.handOver(t, vars)
			}
		})
	}
}

// mutation pin: remove the removeHandOverRef of the failed update-ref -> this
// test fails
func TestHandOverBranchFailureAfterTheRefRemovesTheRef(t *testing.T) {
	e := newHandOverEnv(t)
	commitFile(t, e.ws, "a.txt", "one")
	e.sudoLog = mockSudoAgent(t, e.repo, "")
	tmp := handOverTmpDir(t)
	packsBefore := e.packFiles(t)
	// The seam: the real update-ref writes the hand-over ref, then the step
	// fails with exit 3 (exit 4: the real update-ref failed).
	prev := execCommandContext
	execCommandContext = func(c context.Context, name string, args ...string) *exec.Cmd {
		if name == "git" && slices.Contains(args, "update-ref") && !slices.Contains(args, "-d") {
			return prev(c, "sh", append([]string{"-c", `git "$@" || exit 4; exit 3`, "sh"}, args...)...)
		}
		return prev(c, name, args...)
	}
	t.Cleanup(func() { execCommandContext = prev })

	sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
	if err == nil {
		t.Fatalf("handOverBranch = %s with a failed step after the ref, want an error", sha)
	}
	if !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("the seam did not write the ref and fail: %v", err)
	}
	assertEmptyDir(t, tmp)
	e.assertNothingLeft(t, packsBefore)
}

// mutation pin: remove GIT_NO_REPLACE_OBJECTS from the tip commands
// (handOverQuarantine.git) -> this test fails
func TestHandOverBranchIgnoresDaemonReplaceRefs(t *testing.T) {
	e := newHandOverEnv(t)
	head := commitFile(t, e.ws, "a.txt", "one")
	// A replace ref of the daemon's repository swaps the tip for an orphan
	// commit: in effect, the base is no ancestor of the tip. update-ref, not
	// git replace: the tip is not in the daemon's repository yet.
	orphan := wsGit(t, e.repo, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit-tree", "-m", "orphan", e.base+"^{tree}")
	wsGit(t, e.repo, "update-ref", "refs/replace/"+head, orphan)
	e.sudoLog = mockSudoAgent(t, e.repo, "")
	handOverTmpDir(t)

	sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
	if err != nil || sha != head {
		t.Fatalf("handOverBranch with a replace ref of the tip = %s, %v, want %s", sha, err, head)
	}
	if got := wsGit(t, e.repo, "rev-parse", handOverRef(handOverTestTask)); got != head {
		t.Errorf("%s = %s, want %s", handOverRef(handOverTestTask), got, head)
	}
}

// mutation pin: set cmd.WaitDelay = 0 at the end of handOverDaemonGit -> this
// test fails
func TestHandOverDaemonGitWaitDelay(t *testing.T) {
	// No environment and no seam: it runs next to the other parallel tests.
	t.Parallel()
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available")
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Cleanup(func() {
		if raw, err := os.ReadFile(pidFile); err == nil {
			_ = exec.Command("kill", "-KILL", strings.TrimSpace(string(raw))).Run()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := handOverDaemonGit(ctx, t.TempDir(), "status")
	if cmd.Err != nil {
		t.Fatal(cmd.Err)
	}
	// The mock git traps the signals it can and leaves a child in a session
	// of its own that holds the output pipe: the kill of the group misses
	// the child, only the wait delay ends the wait.
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Path = sh
	cmd.Args = []string{"sh", "-c", fmt.Sprintf(`trap '' TERM INT HUP; setsid sh -c 'trap "" TERM INT HUP; echo $$ > %s; exec sleep 30' & exec sleep 30`, pidFile)}
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if raw, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(raw), "\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the child of the mock git did not start")
		}
	}
	done := make(chan error, 1)
	start := time.Now()
	cancel()
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the mock git ended with no error after the cancel")
		}
		if elapsed := time.Since(start); elapsed > procgroup.DefaultWaitDelay+2*time.Second {
			t.Errorf("the wait ended %v after the cancel, want within the wait delay %v", elapsed, procgroup.DefaultWaitDelay)
		}
	case <-time.After(procgroup.DefaultWaitDelay + 10*time.Second):
		t.Fatalf("the wait did not end %v after the cancel", procgroup.DefaultWaitDelay+10*time.Second)
	}
}

// allowHandOverPartialClone switches the refusal of a daemon's repository
// that is a partial clone off for one test, so the checks behind it run.
func allowHandOverPartialClone(t *testing.T) {
	t.Helper()
	prev := handOverCheckPartialClone
	handOverCheckPartialClone = func(context.Context, string) error { return nil }
	t.Cleanup(func() { handOverCheckPartialClone = prev })
}

// gitArgsSeen wraps execCommandContext for one test and returns the argument
// lists of the git commands of the daemon side it built.
func gitArgsSeen(t *testing.T) *[][]string {
	t.Helper()
	var seen [][]string
	prev := execCommandContext
	execCommandContext = func(c context.Context, name string, args ...string) *exec.Cmd {
		if name == "git" {
			seen = append(seen, slices.Clone(args))
		}
		return prev(c, name, args...)
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &seen
}

// makePartialClone turns the daemon's repository into a partial clone of its
// bare remote origin: the config `git clone --filter=blob:none` writes.
func (e *handOverEnv) makePartialClone(t *testing.T) {
	t.Helper()
	wsGit(t, e.repo, "config", "core.repositoryformatversion", "1")
	wsGit(t, e.repo, "config", "remote.origin.promisor", "true")
	wsGit(t, e.repo, "config", "remote.origin.partialclonefilter", "blob:none")
	wsGit(t, e.repo, "config", "extensions.partialClone", "origin")
}

// mutation pin: remove GIT_NO_LAZY_FETCH=1 from handOverDaemonEnv -> the row
// "no lazy fetch" of this test fails
func TestHandOverBranchPartialCloneNoLazyFetch(t *testing.T) {
	t.Run("refused before any stream command", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			config [][2]string
		}{
			{"clone --filter", nil},
			{"extensions.partialClone", [][2]string{{"extensions.partialClone", "origin"}}},
			{"promisor remote", [][2]string{{"remote.origin.promisor", "true"}}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e := newHandOverEnv(t)
				commitFile(t, e.ws, "a.txt", "one")
				if tc.config == nil {
					e.makePartialClone(t)
				}
				for _, kv := range tc.config {
					wsGit(t, e.repo, "config", kv[0], kv[1])
				}
				e.sudoLog = mockSudoAgent(t, e.repo, "")
				tmp := handOverTmpDir(t)
				packsBefore := e.packFiles(t)
				seen := gitArgsSeen(t)

				sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
				if !errors.Is(err, errHandOverPartialClone) {
					t.Fatalf("handOverBranch = %s, %v, want %v", sha, err, errHandOverPartialClone)
				}
				if calls := sudoCalls(t, e.sudoLog); len(calls) != 0 {
					t.Errorf("commands of the agent user ran: %q", calls)
				}
				if len(*seen) != 1 || !slices.Contains((*seen)[0], "config") {
					t.Errorf("git commands of the daemon: %q, want the config read alone", *seen)
				}
				assertEmptyDir(t, tmp)
				e.assertNothingLeft(t, packsBefore)
			})
		}
	})

	t.Run("a promisor remote set to false is no partial clone", func(t *testing.T) {
		e := newHandOverEnv(t)
		head := commitFile(t, e.ws, "a.txt", "one")
		wsGit(t, e.repo, "config", "remote.origin.promisor", "false")
		e.sudoLog = mockSudoAgent(t, e.repo, "")
		handOverTmpDir(t)
		sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
		if err != nil || sha != head {
			t.Fatalf("handOverBranch = %s, %v, want %s", sha, err, head)
		}
	})

	t.Run("no lazy fetch", func(t *testing.T) {
		e := newHandOverEnv(t)
		private := e.daemonOnlyCommit(t, "private.txt", "private")
		wsGit(t, e.repo, "push", "-q", "origin", private+":refs/heads/private")
		// The agent commits on top of the private commit and sends a pack
		// that does not hold it.
		clone := e.cloneOfDaemon(t)
		wsGit(t, clone, "checkout", "-q", "-b", "forged", private)
		tip := commitFile(t, clone, "agent.txt", "agent")
		stream := forgedHandOverPack(t, clone, tip, "^"+private)
		e.makePartialClone(t)
		allowHandOverPartialClone(t)
		// The wrapper of git traces each git process the daemon side starts,
		// the fetch a git command starts by itself included. GIT_TRACE is set
		// after the environment filter.
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		trace := filepath.Join(t.TempDir(), "trace")
		wrapper := filepath.Join(t.TempDir(), "git")
		body := fmt.Sprintf("#!/bin/sh\nGIT_TRACE='%s' exec '%s' \"$@\"\n", trace, realGit)
		if err := os.WriteFile(wrapper, []byte(body), 0o755); err != nil { //nolint:gosec // the wrapper must be executable
			t.Fatal(err)
		}
		prev := execCommandContext
		execCommandContext = func(c context.Context, name string, args ...string) *exec.Cmd {
			if name == "git" {
				return prev(c, wrapper, args...)
			}
			return prev(c, name, args...)
		}
		t.Cleanup(func() { execCommandContext = prev })

		err = runForgedHandOver(t, e, stream, tip, "did not come from the workspace")
		if err != nil && !strings.Contains(err.Error(), private) {
			t.Errorf("error = %v, want the private commit %s named", err, private)
		}
		raw, err := os.ReadFile(trace)
		if err != nil {
			t.Fatalf("the wrapper of git did not run: %v", err)
		}
		if strings.Contains(string(raw), "fetch") {
			t.Errorf("a git command of the daemon started a fetch:\n%s", raw)
		}
	})
}

// mutation pin: remove --objects from verifyHandOverObjects -> this test fails
func TestHandOverBranchRefusesPrivateTreeAndBlob(t *testing.T) {
	for _, tc := range []struct {
		name string
		// forge returns, in clone, the objects the pack holds, the tip and
		// the private object the error must name.
		forge func(t *testing.T, e *handOverEnv, clone, private string) (objects []string, tip, missing string)
	}{
		{"the tree of a private commit", func(t *testing.T, e *handOverEnv, clone, private string) ([]string, string, string) {
			tree := wsGit(t, clone, "rev-parse", private+"^{tree}")
			tip := wsGit(t, clone, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit-tree", "-p", e.base, "-m", "forged", tree)
			return []string{tip}, tip, tree
		}},
		{"a tree that names a private blob", func(t *testing.T, e *handOverEnv, clone, private string) ([]string, string, string) {
			blob := wsGit(t, clone, "rev-parse", private+":private.txt")
			mktree := exec.Command("git", "-C", clone, "mktree")
			mktree.Stdin = strings.NewReader("100644 blob " + blob + "\tstolen.txt\n")
			out, err := mktree.Output()
			if err != nil {
				t.Fatalf("mktree: %v", err)
			}
			tree := strings.TrimSpace(string(out))
			tip := wsGit(t, clone, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit-tree", "-p", e.base, "-m", "forged", tree)
			return []string{tip, tree}, tip, blob
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newHandOverEnv(t)
			private := e.daemonOnlyCommit(t, "private.txt", "private")
			clone := e.cloneOfDaemon(t)
			objects, tip, missing := tc.forge(t, e, clone, private)
			stream := writeHandOverStream(t, handOverPackBytes(t, clone, nil, objects...))

			err := runForgedHandOver(t, e, stream, tip, "did not come from the workspace")
			if err != nil && !strings.Contains(err.Error(), missing) {
				t.Errorf("error = %v, want the private object %s named", err, missing)
			}
		})
	}
}

// mutation pin: remove the format check of the tip in handOverTip -> this
// test fails
func TestHandOverBranchRefusesTipText(t *testing.T) {
	for _, tc := range []struct {
		name string
		text func(head string) string
	}{
		{"upper case", strings.ToUpper},
		{"a ref name", func(string) string { return "refs/heads/main" }},
		{"an option", func(string) string { return "--upload-pack=x" }},
		{"two lines", func(head string) string { return head + "\n" + head }},
		{"12 characters", func(head string) string { return head[:12] }},
		{"empty", func(string) string { return "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newHandOverEnv(t)
			head := commitFile(t, e.ws, "a.txt", "one")
			text := tc.text(head)
			answer := writeHandOverStream(t, []byte(text+"\n"))
			e.sudoLog = mockSudoAgent(t, e.repo, fmt.Sprintf(`case "$*" in
*" rev-parse --verify "*) exec cat '%s' ;;
esac`, answer))
			tmp := handOverTmpDir(t)
			packsBefore := e.packFiles(t)
			seen := gitArgsSeen(t)

			sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
			if err == nil {
				t.Fatalf("handOverBranch = %s for the tip text %q, want an error", sha, text)
			}
			if !strings.Contains(err.Error(), "not a full object name") {
				t.Errorf("error = %v, want the format of the tip refused", err)
			}
			for _, args := range *seen {
				for _, arg := range args {
					if arg == text || (text != "" && strings.Contains(arg, text)) {
						t.Errorf("the git command %q of the daemon got the tip text %q", args, text)
					}
				}
			}
			for _, c := range sudoCalls(t, e.sudoLog) {
				if strings.Contains(c, "pack-objects") {
					t.Errorf("pack-objects ran for the tip text %q: %q", text, c)
				}
			}
			assertEmptyDir(t, tmp)
			e.assertNothingLeft(t, packsBefore)
		})
	}
}

// mutation pin: remove the size checks of verifyHandOverObjects -> this test
// fails
func TestHandOverBranchObjectSizeLimits(t *testing.T) {
	for _, tc := range []struct {
		name           string
		object, total  int64
		want           string
		files, content int
	}{
		{"one object over the limit", 1024, handOverMaxTotalBytes, "1024 bytes for one object", 1, 4096},
		{"the sum over the limit", handOverMaxObjectBytes, 6000, "over 6000 bytes", 2, 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newHandOverEnv(t)
			for i := 0; i < tc.files; i++ {
				// Zeros: the pack holds them in a few bytes.
				commitFile(t, e.ws, fmt.Sprintf("zeros%d.bin", i), strings.Repeat("\x00", tc.content-i))
			}
			e.sudoLog = mockSudoAgent(t, e.repo, "")
			prevObject, prevTotal := handOverMaxObjectBytes, handOverMaxTotalBytes
			handOverMaxObjectBytes, handOverMaxTotalBytes = tc.object, tc.total
			t.Cleanup(func() { handOverMaxObjectBytes, handOverMaxTotalBytes = prevObject, prevTotal })
			tmp := handOverTmpDir(t)
			packsBefore := e.packFiles(t)

			sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
			if err == nil {
				t.Fatalf("handOverBranch = %s over the limit, want an error", sha)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want %q", err, tc.want)
			}
			assertEmptyDir(t, tmp)
			e.assertNothingLeft(t, packsBefore)
		})
	}
}

// packFilesUnder lists the files with the suffix .pack below dir.
func packFilesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".pack") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestHandOverBranchKilledStreamLeavesNoPackFile(t *testing.T) {
	e := newHandOverEnv(t)
	commitFile(t, e.ws, "a.txt", handOverSecretText())
	// The agent command writes the start of the pack and dies by a kill.
	e.sudoLog = mockSudoAgent(t, e.repo, `case "$*" in *" pack-objects "*) "$@" | head -c 100; kill -s KILL $$ ;; esac`)
	tmp := handOverTmpDir(t)
	packDir := filepath.Join(e.repo, ".git", "objects", "pack")
	before := packFilesUnder(t, filepath.Join(e.repo, ".git", "objects"))
	packsBefore := e.packFiles(t)

	sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
	if err == nil {
		t.Fatalf("handOverBranch = %s after a kill of the stream, want an error", sha)
	}
	if !strings.Contains(err.Error(), "pack-objects") {
		t.Errorf("error = %v, want the failed pack-objects", err)
	}
	assertEmptyDir(t, tmp)
	e.assertNothingLeft(t, packsBefore)
	for _, path := range packFilesUnder(t, filepath.Join(e.repo, ".git", "objects")) {
		if filepath.Dir(path) != packDir || !slices.Contains(before, path) {
			t.Errorf("pack file left after the hand-over: %s", path)
		}
	}
}

// mutation pin: create the temporary pack file in the temporary directory of
// the daemon -> this test fails
func TestHandOverBranchPackFileInTheQuarantine(t *testing.T) {
	e := newHandOverEnv(t)
	head := commitFile(t, e.ws, "a.txt", "one")
	e.sudoLog = mockSudoAgent(t, e.repo, "")
	tmp := handOverTmpDir(t)
	objects := filepath.Join(e.repo, ".git", "objects")
	// Left-overs of the layout of before: the old one goes, the fresh one
	// and a directory of the name stay.
	old := filepath.Join(tmp, "pilot-handover-old.pack")
	fresh := filepath.Join(tmp, "pilot-handover-fresh.pack")
	dir := filepath.Join(tmp, "pilot-handover-dir.pack")
	for _, path := range []string{old, fresh} {
		if err := os.WriteFile(path, []byte("PACK"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	twoHours := time.Now().Add(-2 * time.Hour)
	for _, path := range []string{old, dir} {
		if err := os.Chtimes(path, twoHours, twoHours); err != nil {
			t.Fatal(err)
		}
	}
	// At the index-pack, the pack file is in the quarantine and not in the
	// temporary directory.
	var inQuarantine []string
	var tmpAtIndex []string
	prev := execCommandContext
	execCommandContext = func(c context.Context, name string, args ...string) *exec.Cmd {
		if name == "git" && slices.Contains(args, "index-pack") {
			inQuarantine, _ = filepath.Glob(filepath.Join(objects, handOverQuarantinePrefix+"*", handOverPackPattern))
			tmpAtIndex, _ = filepath.Glob(filepath.Join(tmp, "*"))
		}
		return prev(c, name, args...)
	}
	t.Cleanup(func() { execCommandContext = prev })

	sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
	if err != nil || sha != head {
		t.Fatalf("handOverBranch = %s, %v, want %s", sha, err, head)
	}
	if len(inQuarantine) != 1 {
		t.Errorf("pack files in the quarantine at the index-pack: %q, want one", inQuarantine)
	}
	if want := []string{dir, fresh}; !slices.Equal(tmpAtIndex, want) {
		t.Errorf("the temporary directory at the index-pack: %q, want %q", tmpAtIndex, want)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the pack file of over an hour %s is still there: %v", old, err)
	}
	for _, path := range []string{fresh, dir} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was removed: %v", path, err)
		}
	}
	if quarantines, _ := filepath.Glob(filepath.Join(objects, handOverQuarantinePrefix+"*")); len(quarantines) != 0 {
		t.Errorf("quarantine directories left: %q", quarantines)
	}
}

// mutation pin: drop the check of "reference already exists" after the
// failed update-ref -> this test fails
func TestHandOverBranchUpdateRefFindsTheRef(t *testing.T) {
	e := newHandOverEnv(t)
	head := commitFile(t, e.ws, "a.txt", handOverSecretText())
	e.sudoLog = mockSudoAgent(t, e.repo, "")
	handOverTmpDir(t)
	ref := handOverRef(handOverTestTask)
	// The seam: another hand-over of the task sets the ref (to the same tip,
	// with no pack of its own) right before the update-ref.
	prev := execCommandContext
	execCommandContext = func(c context.Context, name string, args ...string) *exec.Cmd {
		if name == "git" && slices.Contains(args, "update-ref") && !slices.Contains(args, "-d") {
			return prev(c, "sh", append([]string{"-c", `git update-ref "$0" "$1" && shift && exec git "$@"`, ref, head}, args...)...)
		}
		return prev(c, name, args...)
	}
	t.Cleanup(func() { execCommandContext = prev })

	sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
	if !errors.Is(err, errHandOverInProgress) {
		t.Fatalf("handOverBranch = %s, %v, want %v", sha, err, errHandOverInProgress)
	}
	if got := wsGit(t, e.repo, "rev-parse", ref); got != head {
		t.Errorf("%s = %s, want the ref of the other hand-over %s", ref, got, head)
	}
	if out, err := exec.Command("git", "-C", e.repo, "fsck", "--connectivity-only", "--no-dangling").CombinedOutput(); err != nil {
		t.Errorf("git fsck of the daemon repository: %v: %s", err, out)
	}
}

func TestHandOverBranchConcurrentSameTask(t *testing.T) {
	e := newHandOverEnv(t)
	head := commitFile(t, e.ws, "a.txt", handOverSecretText())
	e.sudoLog = mockSudoAgent(t, e.repo, "")
	handOverTmpDir(t)
	ref := handOverRef(handOverTestTask)
	for round := 0; round < 10; round++ {
		type result struct {
			sha string
			err error
		}
		results := make(chan result, 2)
		start := make(chan struct{})
		for i := 0; i < 2; i++ {
			go func() {
				<-start
				sha, err := handOverBranch(context.Background(), e.ws, e.repo, handOverTestTask, e.branch, e.base)
				results <- result{sha, err}
			}()
		}
		close(start)
		var shas []string
		for i := 0; i < 2; i++ {
			r := <-results
			if r.err != nil {
				t.Logf("round %d: %v", round, r.err)
				continue
			}
			shas = append(shas, r.sha)
		}
		if len(shas) == 0 {
			t.Fatalf("round %d: no hand-over returned a SHA", round)
		}
		for _, sha := range shas {
			if sha != head {
				t.Errorf("round %d: handOverBranch = %s, want %s", round, sha, head)
			}
		}
		out, err := exec.Command("git", "-C", e.repo, "rev-parse", "--verify", "-q", ref).Output()
		if got := strings.TrimSpace(string(out)); err != nil || got != head {
			t.Fatalf("round %d: %s = %q (%v) after both returned, want %s", round, ref, got, err, head)
		}
		if out, err := exec.Command("git", "-C", e.repo, "rev-list", "--objects", ref).CombinedOutput(); err != nil {
			t.Fatalf("round %d: the objects of %s: %v: %s", round, ref, err, out)
		}
	}
}
