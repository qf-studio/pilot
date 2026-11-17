package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// TASK-651 (design: .agent/system/agent-isolation-design.md): the hand-over of
// the commits of a run out of the agent workspace. The daemon never runs git
// with the workspace as its repository: the agent controls the git config, the
// hooks and the refs there. Instead a git command of the agent user writes a
// bundle of the branch to its standard output, the daemon copies that stream
// into a temporary file of its own and fetches from it, in its own repository
// and as the daemon user, into one ref of its own. No caller yet: the push
// from that ref comes in a later leg. With the option empty nothing calls it.

// handOverRefPrefix holds the ref of the daemon's repository that a hand-over
// writes: refs/pilot/handover/TASK.
const handOverRefPrefix = "refs/pilot/handover/"

// handOverMaxBundleBytes bounds the bundle stream of a hand-over: past it the
// agent command is stopped and the hand-over fails. A var for the tests.
var handOverMaxBundleBytes int64 = 2 << 30 // 2 GB

// errHandOverTooLarge is the error of a bundle stream past the limit.
var errHandOverTooLarge = errors.New("hand-over: the bundle stream is over the size limit")

// handOverStderrMax bounds what the daemon keeps of the standard error of
// the agent command.
const handOverStderrMax = 64 << 10

// handOverRef is the hand-over ref of a task in the daemon's repository.
func handOverRef(taskID string) string {
	return handOverRefPrefix + sanitizeBranchName(taskID)
}

// handOverGitArgs is the hardened form of a git command of the daemon in its
// own repository during a hand-over: the settings of the secret gate
// (GitScanConfig, hooks off included) before args.
func handOverGitArgs(args ...string) []string {
	return append(secretpatterns.GitScanConfig(), args...)
}

// handOverBranch takes the commits of branch since baseSHA out of the agent
// workspace into refs/pilot/handover/TASK of daemonRepo and returns the head
// SHA of that ref.
//
// Step one runs `git bundle create - refs/heads/BRANCH ^BASE` in the
// workspace through the git constructor, so as the agent user, and copies its
// standard output into a temporary file of the daemon outside the workspace
// root; the daemon reads and writes no file of the workspace. The stream is
// bounded by handOverMaxBundleBytes and by ctx: past the limit the context of
// the agent command is cancelled, which kills it through sudo (the Cancel of
// agentCommand). Step two runs in daemonRepo as the daemon user: one fetch
// from the bundle file with the explicit refspec
// +refs/heads/BRANCH:refs/pilot/handover/TASK, no tags, fetch.fsckObjects on,
// hooks off and GIT_NO_REPLACE_OBJECTS=1. A ref of the bundle other than the
// branch never reaches the daemon's repository. The temporary file is removed
// on every path; a failed hand-over leaves no hand-over ref.
func handOverBranch(ctx context.Context, workspace, daemonRepo, taskID, branch, baseSHA string) (string, error) {
	if sanitizeBranchName(taskID) == "" {
		return "", errors.New("hand-over: no task ID")
	}
	if !isLowerHex(baseSHA) || (len(baseSHA) != 40 && len(baseSHA) != 64) {
		return "", fmt.Errorf("hand-over: base %q is not a full commit SHA", baseSHA)
	}
	branchRef := "refs/heads/" + branch
	if branch == "" {
		return "", errors.New("hand-over: no branch")
	}
	if s := runAsSettingsNow(); s != nil {
		if agent, err := gitRoutesToAgent(s.workspaceRoot, workspace); err != nil || !agent {
			return "", fmt.Errorf("hand-over: workspace %q is not inside the workspace root %q (%v)", workspace, s.workspaceRoot, err)
		}
		if agent, err := gitRoutesToAgent(s.workspaceRoot, daemonRepo); err != nil || agent {
			return "", fmt.Errorf("hand-over: the daemon's repository %q is inside the workspace root %q (%v)", daemonRepo, s.workspaceRoot, err)
		}
	}
	// A ref name holds no ':', '*', space or '^': the refspec below cannot be
	// widened through the branch name.
	if out, err := gitCommandIn(ctx, daemonRepo, "check-ref-format", branchRef).CombinedOutput(); err != nil {
		return "", fmt.Errorf("hand-over: %q is not a valid branch name: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}

	ref := handOverRef(taskID)
	// A ref of an earlier hand-over of the task is never mistaken for this one.
	if err := removeHandOverRef(daemonRepo, taskID); err != nil {
		return "", err
	}

	tmp, err := os.CreateTemp("", "pilot-handover-*.bundle")
	if err != nil {
		return "", fmt.Errorf("hand-over: temporary bundle file: %w", err)
	}
	bundlePath := tmp.Name()
	defer func() { _ = os.Remove(bundlePath) }()
	if s := runAsSettingsNow(); s != nil {
		if agent, _ := gitRoutesToAgent(s.workspaceRoot, bundlePath); agent {
			_ = tmp.Close()
			return "", fmt.Errorf("hand-over: the temporary directory of the daemon %q is inside the workspace root %q", bundlePath, s.workspaceRoot)
		}
	}

	if err := streamHandOverBundle(ctx, workspace, branchRef, baseSHA, tmp); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("hand-over: write the bundle file: %w", err)
	}

	fetch := gitCommandIn(ctx, daemonRepo, handOverGitArgs(
		"-c", "fetch.fsckObjects=true",
		"-c", "gc.auto=0",
		"fetch", "-q", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules",
		bundlePath, "+"+branchRef+":"+ref)...)
	appendGitEnv(fetch, "GIT_NO_REPLACE_OBJECTS=1")
	if out, err := fetch.CombinedOutput(); err != nil {
		_ = removeHandOverRef(daemonRepo, taskID)
		return "", fmt.Errorf("hand-over: fetch from the bundle into %s: %w: %s", ref, err, strings.TrimSpace(string(out)))
	}

	read := gitCommandIn(ctx, daemonRepo, handOverGitArgs("rev-parse", "--verify", "-q", ref+"^{commit}")...)
	appendGitEnv(read, "GIT_NO_REPLACE_OBJECTS=1")
	out, err := read.Output()
	sha := strings.TrimSpace(string(out))
	if err != nil || !isLowerHex(sha) {
		_ = removeHandOverRef(daemonRepo, taskID)
		return "", fmt.Errorf("hand-over: read %s: %v", ref, err)
	}
	return sha, nil
}

// streamHandOverBundle runs `git bundle create - BRANCHREF ^BASE` in the
// workspace through the constructor, with its standard output written into dst
// through a handOverLimitWriter. The writer counts the stream: the write that
// passes handOverMaxBundleBytes is refused and cancels the context of the
// agent command, which kills it (the Cancel of agentCommand, a sudo kill of
// its process group; WaitDelay bounds the wait). ctx bounds the stream too.
func streamHandOverBundle(ctx context.Context, workspace, branchRef, baseSHA string, dst io.Writer) error {
	bctx, cancel := context.WithCancel(ctx)
	defer cancel()
	bundle := gitCommandIn(bctx, workspace, append(secretpatterns.HooksOffArgs(), "bundle", "create", "-", branchRef, "^"+baseSHA)...)
	appendGitEnv(bundle, "GIT_NO_REPLACE_OBJECTS=1")
	out := &handOverLimitWriter{w: dst, max: handOverMaxBundleBytes, over: cancel}
	stderr := &cappedBuffer{max: handOverStderrMax}
	bundle.Stdout, bundle.Stderr = out, stderr
	runErr := bundle.Run()
	switch {
	case out.exceeded:
		return fmt.Errorf("%w of %d bytes", errHandOverTooLarge, out.max)
	case ctx.Err() != nil:
		return fmt.Errorf("hand-over: git bundle create: %w", ctx.Err())
	case out.err != nil:
		return fmt.Errorf("hand-over: copy the bundle stream: %w", out.err)
	case runErr != nil:
		return fmt.Errorf("hand-over: git bundle create as the agent user: %w: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// handOverLimitWriter writes at most max bytes into w. The write that would
// pass max writes nothing, calls over once and fails, as does every later
// write; a write error of w also calls over.
type handOverLimitWriter struct {
	w        io.Writer
	max, n   int64
	over     func()
	exceeded bool
	err      error
}

func (l *handOverLimitWriter) Write(p []byte) (int, error) {
	if l.err != nil {
		return 0, l.err
	}
	if int64(len(p)) > l.max-l.n {
		l.exceeded = true
		l.err = errHandOverTooLarge
		l.over()
		return 0, l.err
	}
	n, err := l.w.Write(p)
	l.n += int64(n)
	if err != nil {
		l.err = err
		l.over()
	}
	return n, err
}

// removeHandOverRef deletes refs/pilot/handover/TASK in daemonRepo, as the
// daemon user with hooks off; a missing ref is no error.
func removeHandOverRef(daemonRepo, taskID string) error {
	if sanitizeBranchName(taskID) == "" {
		return errors.New("hand-over: no task ID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentWorkspaceRemoveTimeout)
	defer cancel()
	ref := handOverRef(taskID)
	cmd := gitCommandIn(ctx, daemonRepo, handOverGitArgs("update-ref", "-d", ref)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("hand-over: delete %s: %w: %s", ref, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// cappedBuffer keeps the first max bytes written to it and drops the rest;
// every write reports success, so the writer is never blocked by it.
type cappedBuffer struct {
	buf []byte
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - len(b.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		b.buf = append(b.buf, p[:room]...)
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return string(b.buf) }
