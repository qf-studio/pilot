package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
	"github.com/qf-studio/pilot/internal/testutil"
)

// TASK-651: with run_as_user set, the push of an agent workspace runs in the
// daemon's repository on the hand-over ref: the secret gate, the push and the
// remote probes. No test needs root: sudo is the mock of the workspace tests.

// handOverPushEnv is a handOverEnv with GitOperations of the run site, a git
// wrapper first on PATH that logs every git command of the daemon user, and a
// mock sudo that marks the agent side.
type handOverPushEnv struct {
	*handOverEnv
	git    *GitOperations
	gitLog string
}

const handOverPushAgentMark = "PILOT_TEST_AGENT_SIDE"

func newHandOverPushEnv(t *testing.T) *handOverPushEnv {
	t.Helper()
	secretpatterns.ResetSecretValuesForTest()
	ResetSecretGateConfigForTest()
	t.Cleanup(func() {
		secretpatterns.ResetSecretValuesForTest()
		ResetSecretGateConfigForTest()
	})
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	e := newHandOverEnv(t)
	// A second mock sudo, first on PATH: the agent side exports the mark, so
	// the wrapper below logs only the git commands of the daemon user.
	e.sudoLog = mockSudoAgent(t, e.repo, "export "+handOverPushAgentMark+"=1")
	wsGit(t, e.ws, "config", "user.name", "Agent")
	wsGit(t, e.ws, "config", "user.email", "agent@example.com")

	dir := t.TempDir()
	gitLog := filepath.Join(t.TempDir(), "daemon-git")
	wrapper := fmt.Sprintf(`#!/bin/sh
if [ -n "$PILOT_TEST_GIT_LOG" ] && [ -z "$%s" ]; then
  echo "$(pwd -P)| $*" >> "$PILOT_TEST_GIT_LOG"
fi
exec '%s' "$@"
`, handOverPushAgentMark, realGit)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(wrapper), 0o755); err != nil { //nolint:gosec // wrapper must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	g := newGitOperationsForWorkspace(e.ws, e.repo)
	if g.daemonRepo != e.repo || g.handOverBase != e.base {
		t.Fatalf("newGitOperationsForWorkspace: daemonRepo=%q base=%q, want %q and %q", g.daemonRepo, g.handOverBase, e.repo, e.base)
	}
	return &handOverPushEnv{handOverEnv: e, git: g, gitLog: gitLog}
}

// logDaemonGit runs fn with the wrapper logging the git commands of the daemon
// user; the git commands of the test outside fn are not logged.
func (e *handOverPushEnv) logDaemonGit(t *testing.T, fn func()) {
	t.Helper()
	t.Setenv("PILOT_TEST_GIT_LOG", e.gitLog)
	fn()
	if err := os.Unsetenv("PILOT_TEST_GIT_LOG"); err != nil {
		t.Fatal(err)
	}
}

// daemonGitCalls is the log of the wrapper: "DIR| ARGS" per command.
func (e *handOverPushEnv) daemonGitCalls(t *testing.T) []string {
	t.Helper()
	return sudoCalls(t, e.gitLog)
}

// assertNoDaemonGitInWorkspace fails on a git command of the daemon user with
// the workspace as its directory or in its arguments.
func (e *handOverPushEnv) assertNoDaemonGitInWorkspace(t *testing.T) {
	t.Helper()
	calls := e.daemonGitCalls(t)
	if len(calls) == 0 {
		t.Fatal("the wrapper logged no git command of the daemon user")
	}
	for _, c := range calls {
		dir, args, _ := strings.Cut(c, "| ")
		if pathInside(e.ws, dir) || strings.Contains(args, e.ws) {
			t.Errorf("git command of the daemon user in the workspace: dir=%s args=%s", dir, args)
		}
	}
}

// remoteHead is the head of the branch on the bare remote, "" when absent.
func (e *handOverPushEnv) remoteHead(t *testing.T, branch string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", e.remote, "rev-parse", "--verify", "-q", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func TestHandOverPushPublishesTheWorkspaceHead(t *testing.T) {
	e := newHandOverPushEnv(t)
	ctx := context.Background()
	commitFile(t, e.ws, "a.txt", "one")
	head := commitFile(t, e.ws, "b.txt", "two")

	if ok, err := e.git.ProbeRemoteBranch(ctx, e.branch); err != nil || ok {
		t.Fatalf("ProbeRemoteBranch before the push = %v, %v; want false, nil", ok, err)
	}
	if n, err := e.git.CountNewCommitsAgainstOrigin(ctx, "main"); err != nil || n != 2 {
		t.Fatalf("CountNewCommitsAgainstOrigin before the push = %d, %v; want 2", n, err)
	}

	var pushErr error
	e.logDaemonGit(t, func() { pushErr = e.git.Push(ctx, e.branch, handOverTestTask) })
	if pushErr != nil {
		t.Fatalf("Push: %v", pushErr)
	}
	if got := e.remoteHead(t, e.branch); got != head {
		t.Fatalf("remote head = %q, want the workspace head %q", got, head)
	}
	e.assertNoDaemonGitInWorkspace(t)
	var pushes int
	for _, c := range e.daemonGitCalls(t) {
		dir, args, _ := strings.Cut(c, "| ")
		if strings.Contains(args, " push ") {
			pushes++
			if !pathInside(resolvedTestPath(t, e.repo), dir) {
				t.Errorf("push ran in %s, want the daemon's repository", dir)
			}
			if strings.Contains(args, " -u ") || !strings.Contains(args, head+":refs/heads/"+e.branch) {
				t.Errorf("push args %q: want the refspec HANDOVER_SHA:refs/heads/BRANCH and no -u", args)
			}
		}
	}
	if pushes != 1 {
		t.Errorf("%d push commands of the daemon user, want 1", pushes)
	}

	if sha, ok := e.git.RemoteBranchHead(ctx, e.branch); !ok || sha != head {
		t.Errorf("RemoteBranchHead = %q, %v; want %q", sha, ok, head)
	}
	if !e.git.RemoteBranchExists(ctx, e.branch) {
		t.Error("RemoteBranchExists = false after the push")
	}
	if n, err := e.git.CountNewCommitsAgainstOrigin(ctx, "main"); err != nil || n != 2 {
		t.Errorf("CountNewCommitsAgainstOrigin after the push = %d, %v; want 2", n, err)
	}
}

func resolvedTestPath(t *testing.T, p string) string {
	t.Helper()
	r, err := resolveGitRoutePath(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHandOverPushSecretGateRefusesASecret(t *testing.T) {
	e := newHandOverPushEnv(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	commitFile(t, e.ws, "notes.md", "token = "+secretGateValue+"\n")

	var err error
	e.logDaemonGit(t, func() { err = e.git.Push(context.Background(), e.branch, handOverTestTask) })
	if !IsPushRefused(err) {
		t.Fatalf("Push of a registered secret: %v, want a refusal of the secret gate", err)
	}
	if got := e.remoteHead(t, e.branch); got != "" {
		t.Errorf("the branch reached the remote at %s", got)
	}
	e.assertNoDaemonGitInWorkspace(t)
	for _, c := range e.daemonGitCalls(t) {
		if strings.Contains(c, " push ") {
			t.Errorf("a push ran after the refusal: %s", c)
		}
	}
}

func TestHandOverPushSecretGateAllowPathsOfTheProject(t *testing.T) {
	e := newHandOverPushEnv(t)
	SetSecretGateProject(e.repo, SecretGateProject{AllowPaths: []string{"fixtures/**"}})
	shaped := "1234567890" + ":AA" + strings.Repeat("d", 33)
	if err := os.MkdirAll(filepath.Join(e.ws, "fixtures"), 0o755); err != nil {
		t.Fatal(err)
	}
	head := commitFile(t, e.ws, "fixtures/x.txt", shaped+"\n")

	if err := e.git.Push(context.Background(), e.branch, handOverTestTask); err != nil {
		t.Fatalf("a secret shape in an allow_paths file of the project must pass: %v", err)
	}
	if got := e.remoteHead(t, e.branch); got != head {
		t.Errorf("remote head = %q, want %q", got, head)
	}

	commitFile(t, e.ws, "y.txt", shaped+"\n")
	if err := e.git.Push(context.Background(), e.branch, handOverTestTask); !IsPushRefused(err) {
		t.Errorf("a secret shape outside allow_paths: %v, want a refusal", err)
	}
	if got := e.remoteHead(t, e.branch); got != head {
		t.Errorf("remote head after the refusal = %q, want %q", got, head)
	}
}

// TestHandOverPushHostileWorkspaceConfig: the git config of the workspace is
// the agent's; it has no effect on the gate and the push of the daemon.
func TestHandOverPushHostileWorkspaceConfig(t *testing.T) {
	e := newHandOverPushEnv(t)
	marker := filepath.Join(t.TempDir(), "marker")
	script := filepath.Join(t.TempDir(), "evil")
	if err := os.WriteFile(script, []byte(fmt.Sprintf("#!/bin/sh\necho \"$0 $*\" >> '%s'\ncat\n", marker)), 0o755); err != nil { //nolint:gosec // must be executable
		t.Fatal(err)
	}
	hookRoot := t.TempDir()
	for _, h := range []string{"pre-push", "reference-transaction", "post-commit", "post-rewrite"} {
		writeGitHook(t, hookRoot, h, marker)
	}
	hooks := filepath.Join(hookRoot, "hooks")
	other := filepath.Join(t.TempDir(), "other.git")
	wsGit(t, filepath.Dir(other), "init", "-q", "--bare", other)
	for _, kv := range [][2]string{
		{"core.hooksPath", hooks},
		{"diff.evil.textconv", script},
		{"gpg.program", script},
		{"log.showSignature", "true"},
		{"alias.push", "!" + script},
		{"url." + other + ".insteadOf", e.remote},
		{"url." + other + ".pushInsteadOf", e.remote},
	} {
		wsGit(t, e.ws, "config", kv[0], kv[1])
	}
	if err := os.WriteFile(filepath.Join(e.ws, ".git", "info", "attributes"), []byte("* diff=evil\n"), 0o644); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}

	// The hooks also fire for the git commands of the test in the workspace:
	// the marker counts from the start of each push only.
	noProgramRan := func(during string) {
		t.Helper()
		if raw, err := os.ReadFile(marker); err == nil {
			t.Errorf("a program of the workspace config ran during %s: %q", during, raw)
		}
	}

	secretpatterns.RegisterSecretValues(secretGateValue)
	commitFile(t, e.ws, "notes.md", "token = "+secretGateValue+"\n")
	_ = os.Remove(marker)
	if err := e.git.Push(context.Background(), e.branch, handOverTestTask); !IsPushRefused(err) {
		t.Fatalf("Push of a secret with a hostile workspace config: %v, want a refusal", err)
	}
	noProgramRan("the refused push")
	if got := e.remoteHead(t, e.branch); got != "" {
		t.Errorf("the refused branch reached the remote at %s", got)
	}
	wsGit(t, e.ws, "reset", "-q", "--hard", "HEAD~1")
	head := commitFile(t, e.ws, "clean.txt", "clean\n")

	var err error
	_ = os.Remove(marker)
	e.logDaemonGit(t, func() { err = e.git.Push(context.Background(), e.branch, handOverTestTask) })
	if err != nil {
		t.Fatalf("Push with a hostile workspace config: %v", err)
	}
	noProgramRan("the push")
	if got := e.remoteHead(t, e.branch); got != head {
		t.Errorf("remote head = %q, want %q", got, head)
	}
	if refs := wsGit(t, other, "for-each-ref"); refs != "" {
		t.Errorf("the url rewrite of the workspace sent refs to the other remote: %s", refs)
	}
	e.assertNoDaemonGitInWorkspace(t)
}

func TestHandOverPushForceWithLeaseRefusesAMovedRemote(t *testing.T) {
	e := newHandOverPushEnv(t)
	ctx := context.Background()
	first := commitFile(t, e.ws, "a.txt", "one")
	if err := e.git.Push(ctx, e.branch, handOverTestTask); err != nil {
		t.Fatalf("first Push: %v", err)
	}

	// The remote moves: another commit lands on the branch.
	moved := wsGit(t, e.repo, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit-tree", "-p", first, "-m", "moved", first+"^{tree}")
	wsGit(t, e.repo, "push", "-q", "origin", moved+":refs/heads/"+e.branch)

	wsGit(t, e.ws, "commit", "-q", "--amend", "-m", "rewritten")
	rewritten := wsGit(t, e.ws, "rev-parse", "HEAD")
	if err := e.git.PushForceWithLease(ctx, e.branch, first, handOverTestTask); err == nil {
		t.Fatal("PushForceWithLease with a stale lease succeeded")
	}
	if got := e.remoteHead(t, e.branch); got != moved {
		t.Fatalf("remote head = %q, want the moved head %q", got, moved)
	}

	var err error
	e.logDaemonGit(t, func() { err = e.git.PushForceWithLease(ctx, e.branch, moved, handOverTestTask) })
	if err != nil {
		t.Fatalf("PushForceWithLease with the current lease: %v", err)
	}
	if got := e.remoteHead(t, e.branch); got != rewritten {
		t.Errorf("remote head = %q, want the rewritten head %q", got, rewritten)
	}
	e.assertNoDaemonGitInWorkspace(t)
}

// TestHandOverPushWarnsOnAPushedCoAuthorTrailer: the agent hides the commits
// from the strip (an origin ref of its own at HEAD); the daemon reads the
// pushed commits in its repository and logs one Warn line with the SHA.
func TestHandOverPushWarnsOnAPushedCoAuthorTrailer(t *testing.T) {
	e := newHandOverPushEnv(t)
	logs := captureLog(t)
	wsGit(t, e.ws, "commit", "-q", "--allow-empty", "-m", "feat: x\n\nCo-Authored-By: Claude <noreply@anthropic.com>")
	trailer := wsGit(t, e.ws, "rev-parse", "HEAD")
	wsGit(t, e.ws, "update-ref", "refs/remotes/origin/hide", "HEAD")
	clean := commitFile(t, e.ws, "a.txt", "one")
	wsGit(t, e.ws, "update-ref", "refs/remotes/origin/hide", "HEAD")

	if err := e.git.Push(context.Background(), e.branch, handOverTestTask); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got := e.remoteHead(t, e.branch); got != clean {
		t.Fatalf("remote head = %q, want %q", got, clean)
	}
	var hits []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "Pushed commit holds the Anthropic Co-Authored-By trailer") {
			hits = append(hits, line)
		}
	}
	if len(hits) != 1 || !strings.Contains(hits[0], "level=WARN") || !strings.Contains(hits[0], "sha="+trailer) {
		t.Errorf("co-author warnings %q: want one Warn line with sha=%s", hits, trailer)
	}
}

func TestHandOverPushFailedHandOverStopsBeforeThePush(t *testing.T) {
	e := newHandOverPushEnv(t)
	commitFile(t, e.ws, "a.txt", "one")
	e.git.handOverBase = ""

	var err error
	e.logDaemonGit(t, func() { err = e.git.Push(context.Background(), e.branch, handOverTestTask) })
	if err == nil || !strings.Contains(err.Error(), "hand-over") {
		t.Fatalf("Push without a base: %v, want the hand-over error", err)
	}
	if got := e.remoteHead(t, e.branch); got != "" {
		t.Errorf("the branch reached the remote at %s", got)
	}
	for _, c := range e.daemonGitCalls(t) {
		if strings.Contains(c, " push ") {
			t.Errorf("a push ran after a failed hand-over: %s", c)
		}
	}
}

func TestHandOverPushOptionEmptyKeepsTheWorkspacePath(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	repo := t.TempDir()
	g := newGitOperationsForWorkspace(repo, filepath.Join(repo, "daemon"))
	if g.daemonRepo != "" || g.handOverBase != "" || g.remoteDir() != repo || g.ProjectPath() != repo {
		t.Errorf("option empty: daemonRepo=%q base=%q remoteDir=%q, want the plain operations of %q", g.daemonRepo, g.handOverBase, g.remoteDir(), repo)
	}
}

// remoteCount is the number of commits of the branch on the bare remote above
// its main.
func (e *handOverPushEnv) remoteCount(t *testing.T, branch string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(wsGit(t, e.remote, "rev-list", "--count", "main..refs/heads/"+branch), "%d", &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestHandOverPushDeletedBranchRefIsNotForgiven: after a first push the agent
// deletes its branch ref and leaves HEAD on the remote head. The hand-over of
// the next push fails; the remote head equals the HEAD of the workspace, but
// this attempt handed nothing over, so the loop returns an error and the run
// stops before the PR.
// mutation pin: pushFailureForgivenHead compares with LocalHead for an agent workspace -> this test fails
func TestHandOverPushDeletedBranchRefIsNotForgiven(t *testing.T) {
	e := newHandOverPushEnv(t)
	ctx := context.Background()
	head := commitFile(t, e.ws, "a.txt", "one")
	if err := e.git.Push(ctx, e.branch, handOverTestTask); err != nil {
		t.Fatalf("first Push: %v", err)
	}
	wsGit(t, e.ws, "checkout", "-q", "--detach")
	wsGit(t, e.ws, "update-ref", "-d", "refs/heads/"+e.branch)
	if got, err := e.git.LocalHead(ctx); err != nil || got != head || e.remoteHead(t, e.branch) != head {
		t.Fatalf("setup: LocalHead = %q, %v; remote head %q; want both %q", got, err, e.remoteHead(t, e.branch), head)
	}

	r := newSilentRunnerTask359()
	task := &Task{ID: handOverTestTask, Branch: e.branch}
	var pushes int
	err := r.pushBranchCounting(ctx, e.git, task, r.log, &pushes)
	if err == nil {
		t.Fatal("the push loop forgave a failed hand-over because the remote head equals the HEAD of the workspace")
	}
	if !strings.Contains(err.Error(), "hand-over") {
		t.Errorf("push loop error %v, want the hand-over error", err)
	}
	if sha := e.git.attemptHandOverSHA(); sha != "" {
		t.Errorf("attemptHandOverSHA after a failed hand-over = %q, want empty", sha)
	}
	if pushFailureForgiven(ctx, e.git, e.branch, err) {
		t.Error("pushFailureForgiven forgave a failed hand-over")
	}

	result := &ExecutionResult{TaskID: handOverTestTask, Success: true}
	if r.pushTaskBranch(ctx, task, e.git, result, nil, r.log) {
		t.Fatal("pushTaskBranch went on to the PR after a failed hand-over")
	}
	if result.Success || result.PRUrl != "" || !strings.Contains(result.Error, "push") {
		t.Errorf("result after a failed hand-over: success=%v pr=%q error=%q, want a push failure and no PR", result.Success, result.PRUrl, result.Error)
	}
}

// TestHandOverPushRefusedGateKeepsThePublishedCount: a refused gate after a
// published push leaves the hand-over SHA of the push empty, and the count of
// new commits is the count on the remote, not the refused commit.
func TestHandOverPushRefusedGateKeepsThePublishedCount(t *testing.T) {
	e := newHandOverPushEnv(t)
	ctx := context.Background()
	head := commitFile(t, e.ws, "a.txt", "one")
	if err := e.git.Push(ctx, e.branch, handOverTestTask); err != nil {
		t.Fatalf("first Push: %v", err)
	}
	if sha := e.git.lastHandOverSHA(); sha != head {
		t.Fatalf("lastHandOverSHA after the push = %q, want %q", sha, head)
	}
	secretpatterns.RegisterSecretValues(secretGateValue)
	commitFile(t, e.ws, "notes.md", "token = "+secretGateValue+"\n")

	if err := e.git.Push(ctx, e.branch, handOverTestTask); !IsPushRefused(err) {
		t.Fatalf("Push of a registered secret: %v, want a refusal", err)
	}
	if sha := e.git.lastHandOverSHA(); sha != "" {
		t.Errorf("lastHandOverSHA after a refused gate = %q, want empty", sha)
	}
	if got := e.remoteHead(t, e.branch); got != head {
		t.Fatalf("remote head = %q, want %q", got, head)
	}
	want := e.remoteCount(t, e.branch)
	if n, err := e.git.CountNewCommitsAgainstOrigin(ctx, "main"); err != nil || n != want {
		t.Errorf("CountNewCommitsAgainstOrigin after a refused gate = %d, %v; want the count on the remote %d", n, err, want)
	}
}

// TestHandOverPushNothingNew: a branch equal to the base gives the typed
// result after one attempt, and the run ends as a run with no new commit,
// not as a push failure.
func TestHandOverPushNothingNew(t *testing.T) {
	e := newHandOverPushEnv(t)
	ctx := context.Background()
	r := newSilentRunnerTask359()
	task := &Task{ID: handOverTestTask, Branch: e.branch}

	var pushes int
	err := r.pushBranchCounting(ctx, e.git, task, r.log, &pushes)
	if !IsPushNothingNew(err) {
		t.Fatalf("push loop of a branch at the base: %v, want IsPushNothingNew", err)
	}
	if pushes != 1 {
		t.Errorf("%d pushes, want 1 (nothing new is not retried)", pushes)
	}
	if IsPushRefused(err) || IsSecretGateUnavailable(err) {
		t.Errorf("nothing new classified as a gate stop: %v", err)
	}

	result := &ExecutionResult{TaskID: handOverTestTask, Success: true}
	if r.pushTaskBranch(ctx, task, e.git, result, nil, r.log) {
		t.Fatal("pushTaskBranch went on to the PR with nothing new")
	}
	want := &ExecutionResult{}
	evaluateEmptyBranchPRGuard(false, nil, want)
	if result.Error != want.Error || result.Success {
		t.Errorf("result.Error = %q, want the no-commit guard %q", result.Error, want.Error)
	}
	if got := e.remoteHead(t, e.branch); got != "" {
		t.Errorf("the branch reached the remote at %s", got)
	}
}

// TestHandOverPushHooksOffInTheDaemonRepository: a reference-transaction hook
// and a pre-push hook of the daemon's repository write a marker; the push,
// its probes and the count (whose fetch moves origin/main) run none of them.
// mutation pin: pushArgs without secretpatterns.HooksOffArgs -> this test fails
func TestHandOverPushHooksOffInTheDaemonRepository(t *testing.T) {
	e := newHandOverPushEnv(t)
	ctx := context.Background()
	marker := filepath.Join(t.TempDir(), "marker")
	for _, h := range []string{"reference-transaction", "pre-push"} {
		writeGitHook(t, filepath.Join(e.repo, ".git"), h, marker)
	}
	head := commitFile(t, e.ws, "a.txt", "one")
	// origin/main moves, so the fetch of the count updates a ref.
	moved := wsGit(t, e.remote, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit-tree", "-p", e.base, "-m", "moved", e.base+"^{tree}")
	wsGit(t, e.remote, "update-ref", "refs/heads/main", moved)
	_ = os.Remove(marker)

	e.logDaemonGit(t, func() {
		if err := e.git.Push(ctx, e.branch, handOverTestTask); err != nil {
			t.Errorf("Push: %v", err)
		}
		if sha, ok := e.git.RemoteBranchHead(ctx, e.branch); !ok || sha != head {
			t.Errorf("RemoteBranchHead = %q, %v; want %q", sha, ok, head)
		}
		if ok, err := e.git.ProbeRemoteBranch(ctx, e.branch); err != nil || !ok {
			t.Errorf("ProbeRemoteBranch = %v, %v; want true", ok, err)
		}
		if n, err := e.git.CountNewCommitsAgainstOrigin(ctx, "main"); err != nil || n != 1 {
			t.Errorf("CountNewCommitsAgainstOrigin = %d, %v; want 1", n, err)
		}
	})
	if raw, err := os.ReadFile(marker); err == nil {
		t.Errorf("a hook of the daemon's repository ran: %q", raw)
	}
	if got := wsGit(t, e.repo, "rev-parse", "refs/remotes/origin/main"); got != moved {
		t.Errorf("origin/main = %s after the count, want the fetched %s", got, moved)
	}
	hooksOff := strings.Join(secretpatterns.HooksOffArgs(), " ")
	var remote int
	for _, c := range e.daemonGitCalls(t) {
		_, args, _ := strings.Cut(c, "| ")
		if strings.Contains(args, " ls-remote ") || strings.Contains(args, " fetch ") || strings.Contains(args, " push ") {
			remote++
			if !strings.Contains(args, hooksOff) {
				t.Errorf("remote command without the hooks-off arguments: %s", args)
			}
		}
	}
	if remote < 4 {
		t.Errorf("%d remote commands of the daemon user logged, want the push, two probes and the fetch", remote)
	}
}

// TestHandOverPushIgnoresGitDirOfTheDaemonEnvironment: GIT_DIR and the other
// repository variables of the daemon environment name another repository; the
// gate, the push, the probes and the count work in the daemon's repository and
// the push lands on its remote.
func TestHandOverPushIgnoresGitDirOfTheDaemonEnvironment(t *testing.T) {
	e := newHandOverPushEnv(t)
	ctx := context.Background()
	head := commitFile(t, e.ws, "a.txt", "one")
	otherRemote := filepath.Join(t.TempDir(), "other-remote.git")
	wsGit(t, filepath.Dir(otherRemote), "init", "-q", "--bare", otherRemote)
	other := filepath.Join(t.TempDir(), "other")
	wsGit(t, filepath.Dir(other), "init", "-q", other)
	wsGit(t, other, "remote", "add", "origin", otherRemote)
	gitTestCommit(t, other, "other")
	wsGit(t, other, "push", "-q", "origin", "HEAD:refs/heads/main")
	otherRefs := wsGit(t, otherRemote, "for-each-ref")

	for k, v := range map[string]string{
		"GIT_DIR":              filepath.Join(other, ".git"),
		"GIT_WORK_TREE":        other,
		"GIT_NAMESPACE":        "evil",
		"GIT_CONFIG_COUNT":     "1",
		"GIT_CONFIG_KEY_0":     "remote.origin.url",
		"GIT_CONFIG_VALUE_0":   otherRemote,
		"GIT_OBJECT_DIRECTORY": filepath.Join(other, ".git", "objects"),
	} {
		t.Setenv(k, v)
	}
	pushErr := e.git.Push(ctx, e.branch, handOverTestTask)
	remoteSHA, remoteOK := e.git.RemoteBranchHead(ctx, e.branch)
	count, countErr := e.git.CountNewCommitsAgainstOrigin(ctx, "main")
	testutil.UnsetLeakedGitEnv()
	for _, k := range []string{"GIT_NAMESPACE", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_OBJECT_DIRECTORY"} {
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}

	if pushErr != nil {
		t.Fatalf("Push with GIT_DIR of another repository: %v", pushErr)
	}
	if got := e.remoteHead(t, e.branch); got != head {
		t.Errorf("remote head = %q, want %q", got, head)
	}
	if got := wsGit(t, otherRemote, "for-each-ref"); got != otherRefs {
		t.Errorf("the push reached the remote of the other repository: %s", got)
	}
	if !remoteOK || remoteSHA != head {
		t.Errorf("RemoteBranchHead = %q, %v; want %q", remoteSHA, remoteOK, head)
	}
	if countErr != nil || count != 1 {
		t.Errorf("CountNewCommitsAgainstOrigin = %d, %v; want 1", count, countErr)
	}
}

// TestDaemonGitEnv: the names of another repository leave the environment,
// the rest stays.
func TestDaemonGitEnv(t *testing.T) {
	got := daemonGitEnv([]string{
		"PATH=/bin", "GIT_DIR=/x", "GIT_WORK_TREE=/x", "GIT_INDEX_FILE=/x/i", "GIT_OBJECT_DIRECTORY=/x/o",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/x/a", "GIT_NAMESPACE=n", "GIT_CONFIG=/x/c", "GIT_CONFIG_GLOBAL=/x/g",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=k", "GIT_CONFIG_VALUE_0=v", "GIT_ASKPASS=/a", "GIT_TERMINAL_PROMPT=0", "HOME=/h",
	})
	want := []string{"PATH=/bin", "GIT_ASKPASS=/a", "GIT_TERMINAL_PROMPT=0", "HOME=/h"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("daemonGitEnv = %q, want %q", got, want)
	}
}

// TestHandOverPushGatesTheSHANotTheRef: the hand-over ref moves to a commit
// with a secret between the hand-over and the gate; the gate scans the
// hand-over SHA and the push sends that SHA.
func TestHandOverPushGatesTheSHANotTheRef(t *testing.T) {
	e := newHandOverPushEnv(t)
	ctx := context.Background()
	secretpatterns.RegisterSecretValues(secretGateValue)
	secret := e.daemonOnlyCommit(t, "notes.md", "token = "+secretGateValue+"\n")
	head := commitFile(t, e.ws, "a.txt", "one")

	prevSeam := handOverBeforeGate
	t.Cleanup(func() { handOverBeforeGate = prevSeam })
	moved := false
	handOverBeforeGate = func(daemonRepo, taskID string) {
		wsGit(t, daemonRepo, "update-ref", handOverRef(taskID), secret)
		moved = true
	}
	prevGate := checkPushForGate
	t.Cleanup(func() { checkPushForGate = prevGate })
	var gated []string
	checkPushForGate = func(ctx context.Context, dir, tip string, opts secretpatterns.PushOptions) error {
		gated = append(gated, tip)
		return prevGate(ctx, dir, tip, opts)
	}

	if err := e.git.Push(ctx, e.branch, handOverTestTask); err != nil {
		t.Fatalf("Push: %v (the gate scanned the moved ref, not the hand-over SHA)", err)
	}
	if !moved {
		t.Fatal("the seam between the hand-over and the gate did not run")
	}
	if len(gated) != 1 || gated[0] != head {
		t.Errorf("gate tips %q, want the hand-over SHA %s", gated, head)
	}
	if got := e.remoteHead(t, e.branch); got != head {
		t.Errorf("remote head = %q, want the gated commit %q", got, head)
	}
}

// detachedWork commits a file on a detached HEAD of the workspace: the HEAD
// of the workspace holds a commit above the base, the task branch (the
// branch of the hand-over) stays at the base, so a push finds nothing new.
func (e *handOverPushEnv) detachedWork(t *testing.T) string {
	t.Helper()
	wsGit(t, e.ws, "checkout", "-q", "--detach")
	return commitFile(t, e.ws, "detached.txt", "work")
}

// TestHandOverPushNothingNewIsTheNoChangesErrorType: the nothing-new path of
// pushTaskBranch is the no-commit path of the PR guard, ErrorTypeNoChanges of
// the backend result included, and the run ends no_op.
// mutation pin: the nothing-new path of pushTaskBranch without backendResult.ErrorType = ErrorTypeNoChanges -> this test fails
func TestHandOverPushNothingNewIsTheNoChangesErrorType(t *testing.T) {
	e := newHandOverPushEnv(t)
	r := newSilentRunnerTask359()
	task := &Task{ID: handOverTestTask, Branch: e.branch}
	result := &ExecutionResult{TaskID: handOverTestTask, Success: true}
	backend := &BackendResult{}

	if r.pushTaskBranch(context.Background(), task, e.git, result, backend, r.log) {
		t.Fatal("pushTaskBranch went on to the PR with nothing new")
	}
	if backend.ErrorType != string(ErrorTypeNoChanges) {
		t.Errorf("backend ErrorType = %q, want %q (the no-commit path)", backend.ErrorType, ErrorTypeNoChanges)
	}
	if got := TerminalStatus(result); got != "no_op" {
		t.Errorf("TerminalStatus = %q, want no_op", got)
	}
}

// TestHandOverPushNothingNewDecomposedParent: the push loop of a decomposed
// parent stops on nothing new after one attempt and ends as its no-commits
// guard ends, not as a push failure after the retries.
// mutation pin: the decomposed-parent push loop retries IsPushNothingNew and fails it as a push failure -> this test fails
func TestHandOverPushNothingNewDecomposedParent(t *testing.T) {
	e := newHandOverPushEnv(t)
	// The guard of the decomposed parent counts against the local base branch.
	wsGit(t, e.ws, "branch", "main", e.base)
	e.detachedWork(t)
	r := newSilentRunnerTask359()
	task := &Task{ID: handOverTestTask, Title: "add feature", Branch: e.branch, BaseBranch: "main", CreatePR: true}
	result := &ExecutionResult{TaskID: handOverTestTask, Success: true}

	r.finalizeDecomposedParentPR(context.Background(), task, e.git, result)

	if result.Success || result.Error != decomposedParentNoCommitsError {
		t.Errorf("result: success=%v error=%q, want the no-commits guard %q", result.Success, result.Error, decomposedParentNoCommitsError)
	}
	var tips int
	for _, c := range sudoCalls(t, e.sudoLog) {
		if strings.Contains(c, "rev-list --count "+e.base+"..refs/heads/"+e.branch) {
			tips++
		}
	}
	if tips != 1 {
		t.Errorf("%d hand-over attempts, want 1 (nothing new is not retried)", tips)
	}
	if got := e.remoteHead(t, e.branch); got != "" {
		t.Errorf("the branch reached the remote at %s", got)
	}
}

// TestHandOverPushNothingNewHoldIsNotAPushFailure: the hold push of a timeout
// salvage with nothing new is not a failed push: the worktree is not kept
// for it and the hold reason does not say the push failed.
// mutation pin: holdPushedBranch sends IsPushNothingNew to pushFailureForgiven and takes the push-failure path -> this test fails
func TestHandOverPushNothingNewHoldIsNotAPushFailure(t *testing.T) {
	e := newHandOverPushEnv(t)
	r := newSilentRunnerTask359()
	// Not a GitHub task: the hold posts no issue comment.
	task := &Task{ID: handOverTestTask, Branch: e.branch, SourceAdapter: "linear"}
	result := &ExecutionResult{TaskID: handOverTestTask}
	var preserve bool

	r.holdPushedBranch(context.Background(), task, e.git, result, r.log, "backend timeout", e.ws, &preserve)

	if preserve {
		t.Error("worktree preserved for a hold with nothing to push")
	}
	if result.Error != "backend timeout" {
		t.Errorf("hold reason %q, want %q (no push failure)", result.Error, "backend timeout")
	}
	if result.BranchPushed == nil || *result.BranchPushed {
		t.Errorf("BranchPushed = %v, want false: nothing reached the remote", result.BranchPushed)
	}
	if result.Outcome != "needs_human" {
		t.Errorf("Outcome = %q, want needs_human (the hold itself)", result.Outcome)
	}
}

// TestRemoteGitCmdHooksOffWithTheOptionEmpty: with the option empty a
// reference-transaction hook of the repository runs for none of the remote
// commands (the probes and the fetch of the count, which moves origin/main).
// mutation pin: remoteGitCmd adds secretpatterns.HooksOffArgs only for an agent workspace -> this test fails
func TestRemoteGitCmdHooksOffWithTheOptionEmpty(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	repo, remote := setupTestRepoWithRemote(t)
	t.Cleanup(func() { _ = os.RemoveAll(repo); _ = os.RemoveAll(remote) })
	g := newGitOperationsForWorkspace(repo, repo)
	if g.daemonRepo != "" {
		t.Fatalf("option empty: daemonRepo = %q, want empty", g.daemonRepo)
	}
	ctx := context.Background()
	base := wsGit(t, repo, "rev-parse", "HEAD")
	moved := wsGit(t, remote, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit-tree", "-p", base, "-m", "moved", base+"^{tree}")
	wsGit(t, remote, "update-ref", "refs/heads/main", moved)
	marker := filepath.Join(t.TempDir(), "marker")
	writeGitHook(t, filepath.Join(repo, ".git"), "reference-transaction", marker)

	if sha, ok := g.RemoteBranchHead(ctx, "main"); !ok || sha != moved {
		t.Errorf("RemoteBranchHead = %q, %v; want %q", sha, ok, moved)
	}
	if ok, err := g.ProbeRemoteBranch(ctx, "main"); err != nil || !ok {
		t.Errorf("ProbeRemoteBranch = %v, %v; want true", ok, err)
	}
	if _, err := g.CountNewCommitsAgainstOrigin(ctx, "main"); err != nil {
		t.Errorf("CountNewCommitsAgainstOrigin: %v", err)
	}
	if got := wsGit(t, repo, "rev-parse", "refs/remotes/origin/main"); got != moved {
		t.Fatalf("origin/main = %s after the count, want the fetched %s", got, moved)
	}
	if raw, err := os.ReadFile(marker); err == nil {
		t.Errorf("a hook of the repository ran with the option empty: %q", raw)
	}
	hooksOff := strings.Join(secretpatterns.HooksOffArgs(), " ")
	if args := strings.Join(g.remoteGitCmd(ctx, "fetch", "origin").Args, " "); !strings.Contains(args, hooksOff) {
		t.Errorf("remote command with the option empty without the hooks-off arguments: %s", args)
	}
}

// TestHandOverPushLeaseRecordsTheHandOverSHA: the branch is rewritten after
// this run's own push and HEAD of the workspace is left on the old head; the
// lease push publishes the hand-over SHA, and that SHA, not the HEAD of the
// workspace, is what the run records as its push.
// mutation pin: the lease push records task.pushedWIPSHA = LocalHead for an agent workspace -> this test fails
func TestHandOverPushLeaseRecordsTheHandOverSHA(t *testing.T) {
	e := newHandOverPushEnv(t)
	ctx := context.Background()
	first := commitFile(t, e.ws, "a.txt", "one")
	if err := e.git.Push(ctx, e.branch, handOverTestTask); err != nil {
		t.Fatalf("first Push: %v", err)
	}
	rewritten := wsGit(t, e.ws, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit-tree", "-p", e.base, "-m", "rewritten", first+"^{tree}")
	wsGit(t, e.ws, "checkout", "-q", "--detach")
	wsGit(t, e.ws, "update-ref", "refs/heads/"+e.branch, rewritten)
	if got, err := e.git.LocalHead(ctx); err != nil || got != first {
		t.Fatalf("setup: LocalHead = %q, %v; want %q", got, err, first)
	}

	r := newSilentRunnerTask359()
	task := &Task{ID: handOverTestTask, Branch: e.branch, pushedWIPSHA: first}
	var pushes int
	if err := r.pushBranchCounting(ctx, e.git, task, r.log, &pushes); err != nil {
		t.Fatalf("push loop: %v", err)
	}
	if pushes != 2 {
		t.Errorf("%d pushes, want the rejected push and the lease push", pushes)
	}
	if got := e.remoteHead(t, e.branch); got != rewritten {
		t.Fatalf("remote head = %q, want the hand-over SHA %q", got, rewritten)
	}
	if task.pushedWIPSHA != rewritten {
		t.Errorf("pushedWIPSHA = %q, want the hand-over SHA %q, not the HEAD of the workspace %q", task.pushedWIPSHA, rewritten, first)
	}
}

// TestHandOverPushNothingNewEpic: the epic push with nothing new ends as the
// epic's no-commits guard ends (no_op when every child no-op'd), not as a
// failed epic push.
// mutation pin: the epic push sends IsPushNothingNew to pushFailureForgiven and fails as "epic branch push failed" -> this test fails
func TestHandOverPushNothingNewEpic(t *testing.T) {
	e := newHandOverPushEnv(t)
	e.detachedWork(t)
	r := newSilentRunnerTask359()
	task := &Task{ID: handOverTestTask, Title: "add feature", Branch: e.branch, BaseBranch: "main", CreatePR: true}
	result := &ExecutionResult{TaskID: handOverTestTask, Success: true, IsEpic: true}

	r.finalizeEpicBranchPR(context.Background(), task, e.git, result, []string{"no_op"})

	want := &ExecutionResult{}
	evaluateEmptyBranchPRGuard(true, []string{"no_op"}, want)
	if result.Outcome != "no_op" || result.Error != want.Error {
		t.Errorf("result: outcome=%q error=%q, want the epic no-commits guard %q", result.Outcome, result.Error, want.Error)
	}
	if got := TerminalStatus(result); got != "no_op" {
		t.Errorf("TerminalStatus = %q, want no_op", got)
	}
}

// TestHandOverPushNothingNewTimeoutSalvage: the timeout-salvage push with
// nothing new ends as the PR guard's no-commit path, not on hold.
// mutation pin: the timeout-salvage push sends IsPushNothingNew to pushFailureForgiven and holds the branch -> this test fails
func TestHandOverPushNothingNewTimeoutSalvage(t *testing.T) {
	e := newHandOverPushEnv(t)
	r := newSilentRunnerTask359()
	task := &Task{ID: handOverTestTask, Title: "add feature", Branch: e.branch, BaseBranch: "main", CreatePR: true, SourceAdapter: "linear"}
	result := &ExecutionResult{TaskID: handOverTestTask}
	var preserve bool

	r.pushAndCreatePRAfterTimeout(context.Background(), task, e.git, result, nil, r.log, e.ws, &preserve)

	want := &ExecutionResult{}
	evaluateEmptyBranchPRGuard(false, nil, want)
	if result.Success || result.Error != want.Error {
		t.Errorf("result: success=%v error=%q, want the no-commit guard %q", result.Success, result.Error, want.Error)
	}
	if result.Outcome == "needs_human" || preserve {
		t.Errorf("outcome=%q preserve=%v: the salvage held the branch for nothing new", result.Outcome, preserve)
	}
}
