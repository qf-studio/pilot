package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// TASK-651 (design: .agent/system/agent-isolation-design.md): the hand-over of
// the commits of a run out of the agent workspace. The daemon never runs git
// with the workspace as its repository: the agent controls the git config, the
// hooks and the refs there. Instead a git command of the agent user writes a
// pack of the branch (not thin) to its standard output, the daemon copies that
// stream into a temporary file of its own, indexes it into a quarantine object
// directory of its own repository, checks every object of the range there, and
// only then moves the pack into its object store and sets one ref of its own.
// The push of an agent workspace (GitOperations.pushHandOver) gates and pushes
// from that ref. With the option empty nothing calls it.

// handOverRefPrefix holds the ref of the daemon's repository that a hand-over
// writes: refs/pilot/handover/TASK.
const handOverRefPrefix = "refs/pilot/handover/"

// handOverMaxPackBytes bounds the pack stream of a hand-over: past it the
// agent command is stopped and the hand-over fails. A var for the tests.
var handOverMaxPackBytes int64 = 2 << 30 // 2 GB

// errHandOverTooLarge is the error of a pack stream past the limit.
var errHandOverTooLarge = errors.New("hand-over: the pack stream is over the size limit")

// errHandOverNothingNew is the error of a branch with no commit above the
// base: there is nothing to hand over.
var errHandOverNothingNew = errors.New("hand-over: the branch has no commit above the base")

// handOverStderrMax bounds what the daemon keeps of the standard error of
// the agent command.
const handOverStderrMax = 64 << 10

// errHandOverNotPack is the error of a stream that does not start with the
// header of a version 2 git pack.
var errHandOverNotPack = errors.New("hand-over: the stream is not a git pack")

// handOverPackHeader is the start of a git pack that the hand-over accepts:
// the signature PACK and the version 2, in network byte order.
var handOverPackHeader = []byte{'P', 'A', 'C', 'K', 0, 0, 0, 2}

// handOverQuarantinePrefix names the quarantine object directories of the
// hand-over inside the object directory of the daemon's repository.
const handOverQuarantinePrefix = "pilot-handover-incoming-"

// handOverQuarantineMaxAge is the age past which a quarantine directory is a
// left-over of a daemon that was killed and the next hand-over removes it.
const handOverQuarantineMaxAge = time.Hour

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

// handOverEnvAllowed reports whether the variable name of the daemon
// environment reaches a git command of the daemon during a hand-over: a name
// that starts with GIT_ never does. Decision: an allow rule, not a list of
// denied names; GIT_COMMON_DIR, GIT_EXEC_PATH, GIT_GRAFT_FILE and GIT_TRACE
// each passed the list. The GIT_ values a hand-over needs are set by the code
// after the filter (handOverDaemonGit, handOverQuarantine.git, appendGitEnv).
func handOverEnvAllowed(name string) bool {
	return !strings.HasPrefix(name, "GIT_")
}

// handOverDaemonGit builds a git command of the daemon in daemonRepo during a
// hand-over (args as given): the environment of the daemon without any GIT_
// name (handOverEnvAllowed) plus GIT_TERMINAL_PROMPT=0, a process group of its
// own, and a cancel of ctx that kills that group, so a child such as
// index-pack that holds the output pipe dies with it; the wait delay of
// configureProcessGroup bounds the wait. A command the constructor failed
// closed, or one of the agent route
// (agentCommand already kills its group through sudo), is left as it is.
func handOverDaemonGit(ctx context.Context, daemonRepo string, args ...string) *exec.Cmd {
	cmd := gitCommandIn(ctx, daemonRepo, args...)
	if gitRouteFailed(cmd) {
		return cmd
	}
	if _, agent := agentCmdUser(cmd); agent {
		return cmd
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	kept := make([]string, 0, len(env))
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); handOverEnvAllowed(name) {
			kept = append(kept, kv)
		}
	}
	cmd.Env = append(kept, "GIT_TERMINAL_PROMPT=0")
	configureProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd, syscall.SIGKILL) }
	return cmd
}

// handOverQuarantine is the quarantine object directory of one hand-over:
// dir inside objects, the object directory of the daemon's repository.
type handOverQuarantine struct {
	dir, objects string
}

// git builds a git command of the daemon (handOverDaemonGit, hardened args)
// that reads and writes objects in the quarantine: GIT_OBJECT_DIRECTORY is the
// quarantine and, with alternate, GIT_ALTERNATE_OBJECT_DIRECTORIES is the
// object directory of the daemon's repository; without it the quarantine is
// the only object directory. Decision: handOverDaemonGit removes every GIT_
// name of the daemon environment, these are set on purpose. A command that
// routes to the agent user would lose them, so it fails closed.
func (q handOverQuarantine) git(ctx context.Context, daemonRepo string, alternate bool, args ...string) *exec.Cmd {
	cmd := handOverDaemonGit(ctx, daemonRepo, handOverGitArgs(args...)...)
	if gitRouteFailed(cmd) {
		return cmd
	}
	if _, agent := agentCmdUser(cmd); agent {
		return failedGitCommand(cmd.Args[1:], fmt.Errorf("hand-over: the daemon's repository %q routes to the agent user", daemonRepo))
	}
	cmd.Env = append(cmd.Env, "GIT_NO_REPLACE_OBJECTS=1", "GIT_OBJECT_DIRECTORY="+q.dir)
	if alternate {
		cmd.Env = append(cmd.Env, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+q.objects)
	}
	return cmd
}

// checkHandOverPackHeader fails with errHandOverNotPack unless the file at
// path starts with the signature PACK and the version 2. Decision: it runs
// before any git command reads the file, so a stream of another kind (a
// gitfile, another git file format, nothing) never reaches git.
func checkHandOverPackHeader(path string) error {
	f, err := os.Open(path) //nolint:gosec // the temporary file of the daemon
	if err != nil {
		return fmt.Errorf("hand-over: open the pack file: %w", err)
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, len(handOverPackHeader))
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, handOverPackHeader) {
		return errHandOverNotPack
	}
	return nil
}

// checkHandOverTip checks the tip through the quarantine with the alternate:
// a commit, not the base, with the base as an ancestor. On a failure the
// error names the rule.
func checkHandOverTip(ctx context.Context, daemonRepo string, q handOverQuarantine, tip, baseSHA string) error {
	out, err := q.git(ctx, daemonRepo, true, "cat-file", "-t", tip).Output()
	if err != nil {
		return fmt.Errorf("hand-over: read the type of the tip %s: %v", tip, err)
	}
	if t := strings.TrimSpace(string(out)); t != "commit" {
		return fmt.Errorf("hand-over: the tip %s is a %s, the tip must be a commit", tip, t)
	}
	if tip == baseSHA {
		return fmt.Errorf("hand-over: the tip %s is the base, the tip must be a commit above the base", tip)
	}
	if out, err := q.git(ctx, daemonRepo, true, "merge-base", "--is-ancestor", baseSHA, tip).CombinedOutput(); err != nil {
		return fmt.Errorf("hand-over: the base %s is not an ancestor of the tip %s: %w: %s", baseSHA, tip, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// verifyHandOverObjects checks that each object of `git rev-list --objects
// TIP --not BASE` (read through the quarantine with the alternate) exists in
// the quarantine alone: a cat-file with the quarantine as the only object
// directory. An object that exists only through the alternate is an object of
// the daemon's repository, not of the workspace. Decision: the push publishes
// the range with the credentials of the daemon, so "index-pack accepted the
// pack" is no proof; the range must be what the agent sent.
func verifyHandOverObjects(ctx context.Context, daemonRepo string, q handOverQuarantine, tip, baseSHA string) error {
	list := q.git(ctx, daemonRepo, true, "rev-list", "--objects", tip, "--not", baseSHA)
	stderr := &cappedBuffer{max: handOverStderrMax}
	list.Stderr = stderr
	out, err := list.Output()
	if err != nil {
		return fmt.Errorf("hand-over: list the objects of %s --not %s: %w: %s", tip, baseSHA, err, strings.TrimSpace(stderr.String()))
	}
	var names strings.Builder
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if name, _, _ := strings.Cut(line, " "); name != "" {
			names.WriteString(name + "\n")
			n++
		}
	}
	check := q.git(ctx, daemonRepo, false, "cat-file", "--batch-check=%(objectname)")
	check.Stdin = strings.NewReader(names.String())
	stderr = &cappedBuffer{max: handOverStderrMax}
	check.Stderr = stderr
	out, err = check.Output()
	if err != nil {
		return fmt.Errorf("hand-over: check the objects in the quarantine: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if n == 0 {
		lines = nil
	}
	for _, line := range lines {
		if sha, ok := strings.CutSuffix(line, " missing"); ok {
			return fmt.Errorf("hand-over: object %s of the branch did not come from the workspace", sha)
		}
	}
	if len(lines) != n {
		return fmt.Errorf("hand-over: the quarantine check answered %d lines for %d objects", len(lines), n)
	}
	return nil
}

// handOverObjectDir is the absolute object directory of the daemon's
// repository (the common one of a linked worktree).
func handOverObjectDir(ctx context.Context, daemonRepo string) (string, error) {
	out, err := handOverDaemonGit(ctx, daemonRepo, handOverGitArgs("rev-parse", "--git-path", "objects")...).Output()
	dir := strings.TrimSpace(string(out))
	if err != nil || dir == "" {
		return "", fmt.Errorf("hand-over: the object directory of %s: %v", daemonRepo, err)
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(daemonRepo, dir)
	}
	return dir, nil
}

// sweepHandOverQuarantines removes the quarantine directories in objects
// older than handOverQuarantineMaxAge: the left-overs of a daemon that was
// killed during a hand-over.
func sweepHandOverQuarantines(objects string) {
	entries, err := os.ReadDir(objects)
	if err != nil {
		return
	}
	for _, en := range entries {
		if !en.IsDir() || !strings.HasPrefix(en.Name(), handOverQuarantinePrefix) {
			continue
		}
		if info, err := en.Info(); err == nil && time.Since(info.ModTime()) > handOverQuarantineMaxAge {
			_ = os.RemoveAll(filepath.Join(objects, en.Name()))
		}
	}
}

// moveHandOverPack links the pack files that index-pack wrote into the
// quarantine into objects/pack of the daemon's repository, the index last (git
// sees a pack once its index exists), and returns the paths it created. A file
// that is there already (the same pack name, so the same objects) is kept and
// not returned, so a removal of the returned paths never removes a pack of
// the daemon. On an error the created paths are removed again.
func moveHandOverPack(q handOverQuarantine) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(q.dir, "pack"))
	if err != nil {
		return nil, fmt.Errorf("hand-over: read the quarantine pack directory: %w", err)
	}
	var names []string
	for _, en := range entries {
		name := en.Name()
		if strings.HasPrefix(name, "pack-") && slices.Contains([]string{".pack", ".rev", ".idx"}, filepath.Ext(name)) {
			names = append(names, name)
		}
	}
	// The index of a pack last.
	slices.SortStableFunc(names, func(a, b string) int {
		ai, bi := filepath.Ext(a) == ".idx", filepath.Ext(b) == ".idx"
		switch {
		case ai == bi:
			return strings.Compare(a, b)
		case ai:
			return 1
		default:
			return -1
		}
	})
	if !slices.ContainsFunc(names, func(n string) bool { return filepath.Ext(n) == ".idx" }) {
		return nil, errors.New("hand-over: index-pack wrote no pack index into the quarantine")
	}
	dst := filepath.Join(q.objects, "pack")
	if err := os.MkdirAll(dst, 0o755); err != nil { //nolint:gosec // the pack directory of a git repository
		return nil, fmt.Errorf("hand-over: the pack directory: %w", err)
	}
	var moved []string
	for _, name := range names {
		to := filepath.Join(dst, name)
		err := os.Link(filepath.Join(q.dir, "pack", name), to)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			removeHandOverFiles(moved)
			return nil, fmt.Errorf("hand-over: move %s into the pack directory: %w", name, err)
		}
		moved = append(moved, to)
	}
	return moved, nil
}

// removeHandOverFiles removes the moved pack files, the index first.
func removeHandOverFiles(paths []string) {
	for i := len(paths) - 1; i >= 0; i-- {
		_ = os.Remove(paths[i])
	}
}

// handOverBranch takes the commits of branch since baseSHA out of the agent
// workspace into refs/pilot/handover/TASK of daemonRepo and returns the head
// SHA of that ref.
//
// Step one runs in the workspace through the git constructor, so as the agent
// user: `git rev-list --count BASE..refs/heads/BRANCH` (0 is
// errHandOverNothingNew), `git rev-parse --verify refs/heads/BRANCH^{commit}`
// for the tip, and `git pack-objects --stdout --revs` (TIP and ^BASE on its
// standard input, not thin), whose standard output is copied into a temporary
// file of the daemon outside the workspace root; the daemon reads and writes
// no file of the workspace. The stream is bounded by handOverMaxPackBytes and
// by ctx: past the limit the context of the agent command is cancelled, which
// kills it through sudo (the Cancel of agentCommand).
//
// Step two runs in daemonRepo as the daemon user, only after the file passed
// checkHandOverPackHeader: `git index-pack --strict --stdin` without
// --fix-thin into a quarantine object directory (handOverQuarantine), so a
// delta whose base is not in the pack fails; then checkHandOverTip and
// verifyHandOverObjects through the quarantine. Only then the pack moves into
// objects/pack and update-ref sets the hand-over ref; a failed update-ref
// removes the moved files. Every git command of this step runs through
// handOverDaemonGit (no GIT_ name of the daemon environment, a group kill on
// a cancel of ctx). The temporary file and the quarantine are removed on every
// path; a failed hand-over leaves objects/pack as it was and no hand-over ref.
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
	// A ref name holds no ':', '*', space or '^': the revision arguments below
	// cannot be widened through the branch name.
	if out, err := handOverDaemonGit(ctx, daemonRepo, "check-ref-format", branchRef).CombinedOutput(); err != nil {
		return "", fmt.Errorf("hand-over: %q is not a valid branch name: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}

	ref := handOverRef(taskID)
	// A ref of an earlier hand-over of the task is never mistaken for this one.
	if err := removeHandOverRef(daemonRepo, taskID); err != nil {
		return "", err
	}
	objects, err := handOverObjectDir(ctx, daemonRepo)
	if err != nil {
		return "", err
	}
	sweepHandOverQuarantines(objects)

	tip, err := handOverTip(ctx, workspace, branchRef, baseSHA)
	if err != nil {
		return "", err
	}

	tmp, err := os.CreateTemp("", "pilot-handover-*.pack")
	if err != nil {
		return "", fmt.Errorf("hand-over: temporary pack file: %w", err)
	}
	packPath := tmp.Name()
	defer func() { _ = os.Remove(packPath) }()
	if s := runAsSettingsNow(); s != nil {
		if agent, _ := gitRoutesToAgent(s.workspaceRoot, packPath); agent {
			_ = tmp.Close()
			return "", fmt.Errorf("hand-over: the temporary directory of the daemon %q is inside the workspace root %q", packPath, s.workspaceRoot)
		}
	}

	if err := streamHandOverPack(ctx, workspace, tip, baseSHA, tmp); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("hand-over: write the pack file: %w", err)
	}
	if err := checkHandOverPackHeader(packPath); err != nil {
		_ = os.Remove(packPath)
		return "", err
	}

	qdir, err := os.MkdirTemp(objects, handOverQuarantinePrefix+"*")
	if err != nil {
		return "", fmt.Errorf("hand-over: the quarantine directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(qdir) }()
	q := handOverQuarantine{dir: qdir, objects: objects}
	if err := os.Mkdir(filepath.Join(qdir, "pack"), 0o755); err != nil { //nolint:gosec // the pack directory of a git object directory
		return "", fmt.Errorf("hand-over: the quarantine pack directory: %w", err)
	}

	pack, err := os.Open(packPath) //nolint:gosec // the temporary file of the daemon
	if err != nil {
		return "", fmt.Errorf("hand-over: open the pack file: %w", err)
	}
	index := q.git(ctx, daemonRepo, true, "index-pack", "--strict", "--stdin")
	index.Stdin = pack
	out, err := index.CombinedOutput()
	_ = pack.Close()
	if err != nil {
		return "", fmt.Errorf("hand-over: git index-pack into the quarantine: %w: %s", err, strings.TrimSpace(string(out)))
	}

	if err := checkHandOverTip(ctx, daemonRepo, q, tip, baseSHA); err != nil {
		return "", err
	}
	if err := verifyHandOverObjects(ctx, daemonRepo, q, tip, baseSHA); err != nil {
		return "", err
	}

	moved, err := moveHandOverPack(q)
	if err != nil {
		return "", err
	}
	// The empty old value: the ref must not exist, it was removed above.
	update := handOverDaemonGit(ctx, daemonRepo, handOverGitArgs("update-ref", ref, tip, "")...)
	appendGitEnv(update, "GIT_NO_REPLACE_OBJECTS=1")
	if out, err := update.CombinedOutput(); err != nil {
		_ = removeHandOverRef(daemonRepo, taskID)
		removeHandOverFiles(moved)
		return "", fmt.Errorf("hand-over: set %s to %s: %w: %s", ref, tip, err, strings.TrimSpace(string(out)))
	}
	return tip, nil
}

// handOverTip runs, as the agent user in the workspace, `git rev-list --count
// BASE..BRANCHREF` (0 is errHandOverNothingNew, a text that is not a plain
// number an error) and `git rev-parse --verify BRANCHREF^{commit}`, and
// returns the tip, accepted only as a full lower-case hex object name of the
// length of the base.
func handOverTip(ctx context.Context, workspace, branchRef, baseSHA string) (string, error) {
	count := gitCommandIn(ctx, workspace, append(secretpatterns.HooksOffArgs(), "rev-list", "--count", baseSHA+".."+branchRef)...)
	appendGitEnv(count, "GIT_NO_REPLACE_OBJECTS=1")
	stderr := &cappedBuffer{max: handOverStderrMax}
	count.Stderr = stderr
	out, err := count.Output()
	if err != nil {
		return "", fmt.Errorf("hand-over: git rev-list --count as the agent user: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	n := strings.TrimSpace(string(out))
	if !isDigits(n) {
		return "", fmt.Errorf("hand-over: git rev-list --count as the agent user answered %q, not a number", n)
	}
	if strings.TrimLeft(n, "0") == "" {
		return "", errHandOverNothingNew
	}
	parse := gitCommandIn(ctx, workspace, append(secretpatterns.HooksOffArgs(), "rev-parse", "--verify", branchRef+"^{commit}")...)
	appendGitEnv(parse, "GIT_NO_REPLACE_OBJECTS=1")
	stderr = &cappedBuffer{max: handOverStderrMax}
	parse.Stderr = stderr
	out, err = parse.Output()
	if err != nil {
		return "", fmt.Errorf("hand-over: git rev-parse %s as the agent user: %w: %s", branchRef, err, strings.TrimSpace(stderr.String()))
	}
	tip := strings.TrimSpace(string(out))
	if !isLowerHex(tip) || len(tip) != len(baseSHA) {
		return "", fmt.Errorf("hand-over: git rev-parse %s as the agent user answered %q, not a full object name", branchRef, tip)
	}
	return tip, nil
}

// streamHandOverPack runs `git pack-objects --stdout --revs` (no --thin) in
// the workspace through the constructor with the lines TIP and ^BASE on its
// standard input and its standard output written into dst through a
// handOverLimitWriter. The writer counts the stream: the write that passes
// handOverMaxPackBytes is refused and cancels the context of the agent
// command, which kills it (the Cancel of agentCommand, a sudo kill of its
// process group; WaitDelay bounds the wait). ctx bounds the stream too.
func streamHandOverPack(ctx context.Context, workspace, tip, baseSHA string, dst io.Writer) error {
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pack := gitCommandIn(pctx, workspace, append(secretpatterns.HooksOffArgs(), "pack-objects", "--stdout", "--revs", "-q")...)
	appendGitEnv(pack, "GIT_NO_REPLACE_OBJECTS=1")
	out := &handOverLimitWriter{w: dst, max: handOverMaxPackBytes, over: cancel}
	stderr := &cappedBuffer{max: handOverStderrMax}
	pack.Stdin = strings.NewReader(tip + "\n^" + baseSHA + "\n")
	pack.Stdout, pack.Stderr = out, stderr
	runErr := pack.Run()
	switch {
	case out.exceeded:
		return fmt.Errorf("%w of %d bytes", errHandOverTooLarge, out.max)
	case ctx.Err() != nil:
		return fmt.Errorf("hand-over: git pack-objects: %w", ctx.Err())
	case out.err != nil:
		return fmt.Errorf("hand-over: copy the pack stream: %w", out.err)
	case runErr != nil:
		return fmt.Errorf("hand-over: git pack-objects as the agent user: %w: %s", runErr, strings.TrimSpace(stderr.String()))
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
	cmd := handOverDaemonGit(ctx, daemonRepo, handOverGitArgs("update-ref", "-d", ref)...)
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
