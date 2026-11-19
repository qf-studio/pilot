package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// defaultExcludeDirs are top-level directory prefixes whose contents are never
// auto-staged by the executor's commit step. They represent project-management
// state, build artifacts, or third-party caches that should not appear in
// Pilot-authored commits unless the task explicitly references them.
var defaultExcludeDirs = []string{
	".agent/",
	".claude/",
	"node_modules/",
	"dist/",
	"build/",
	"coverage/",
	".cache/",
}

// defaultExcludeGlobs are base-name patterns whose matching files are never
// auto-staged. Lock files and OS-junk dominate this list.
var defaultExcludeGlobs = []string{
	"*.lock",
	"package-lock.json",
	"pnpm-lock.yaml",
	".DS_Store",
	"Thumbs.db",
}

// ErrNoStageableChanges signals that all dirty paths matched the exclude list —
// the commit step refuses to produce an empty commit rather than committing
// excluded paths.
var ErrNoStageableChanges = fmt.Errorf("no stageable changes after applying default excludes")

// isExcluded returns true if the given porcelain path matches any default
// exclude rule.
func isExcluded(path string) bool {
	for _, dir := range defaultExcludeDirs {
		if strings.HasPrefix(path, dir) {
			return true
		}
	}
	base := filepath.Base(path)
	for _, glob := range defaultExcludeGlobs {
		if matched, _ := filepath.Match(glob, base); matched {
			return true
		}
	}
	return false
}

// GitOperations handles git operations for tasks
type GitOperations struct {
	projectPath string
	// daemonRepo is the daemon's repository of the task when projectPath is an
	// agent workspace (TASK-651, run_as_user set): the secret gate, the push and
	// every remote command run there, on the hand-over ref. "" otherwise, and
	// every method works on projectPath alone.
	daemonRepo string
	// handOverBase is the base commit of the workspace, the ^BASE of the
	// hand-over; "" when the workspace has none on record.
	handOverBase string
	// workspace is the daemon's record of the agent workspace (the task
	// branch, the push and recovery state); nil when none is on record.
	workspace *agentWorkspaceRecord

	handOverMu sync.Mutex
	// attemptSHA is the hand-over SHA of the current push attempt: cleared at
	// its start, set after its hand-over. A failed push is compared with it,
	// never with the HEAD of the workspace (pushFailureForgivenHead).
	attemptSHA string
	// attemptBranch is the branch of the current (or last) push attempt, set
	// at its start; "" before the first attempt of g.
	attemptBranch string
	// publishedSHA is the head that the last published push (a success or a
	// forgiven failure, recordPublishedHandOver) put on publishedBranch; a
	// failed attempt keeps both. The count of new commits reads them.
	publishedSHA    string
	publishedBranch string
}

// NewGitOperations creates new git operations for a project
func NewGitOperations(projectPath string) *GitOperations {
	return &GitOperations{projectPath: projectPath}
}

// newGitOperationsForWorkspace is NewGitOperations for the execution path of
// a run, with daemonRepo the project path of the task. When the option is set
// and workspace is an agent workspace (inside the workspace root), the
// secret gate, the push and the remote commands run in daemonRepo
// (TASK-651); otherwise it is NewGitOperations(workspace).
func newGitOperationsForWorkspace(workspace, daemonRepo string) *GitOperations {
	g := NewGitOperations(workspace)
	s := runAsSettingsNow()
	if s == nil || daemonRepo == "" {
		return g
	}
	if agent, err := gitRoutesToAgent(s.workspaceRoot, workspace); err != nil || !agent {
		return g
	}
	g.daemonRepo = daemonRepo
	g.workspace = agentWorkspaceRecordOf(workspace)
	g.handOverBase = agentWorkspaceBase(workspace)
	return g
}

// taskBranch is the task branch of the agent workspace as the daemon recorded
// it, "" when none is on record. The HEAD of the workspace is the agent's.
func (g *GitOperations) taskBranch() string {
	if g.workspace == nil {
		return ""
	}
	return g.workspace.branch
}

// ghDirForDaemonRepo is the directory of a gh command of g and the arguments
// that name its repository. With an agent workspace (TASK-651) it is the
// daemon's repository and `--repo` from the origin remote of that repository
// (ghRepoArg): gh reads the repository from the git config of its directory,
// and the git config of the workspace is the agent's, so the agent would
// choose the repository the daemon token acts on. Otherwise it is projectPath
// and no argument, the call of today.
func (g *GitOperations) ghDirForDaemonRepo(ctx context.Context) (string, []string, error) {
	if g.daemonRepo == "" {
		return g.projectPath, nil, nil
	}
	repoArgs, err := ghRepoArgsOfDaemonRepo(ctx, g.daemonRepo)
	if err != nil {
		return "", nil, err
	}
	return g.daemonRepo, repoArgs, nil
}

// ghDirForExecutionPath is ghDirForDaemonRepo for a gh command that takes an
// execution path as its directory (the gh calls of epic.go). For an agent
// workspace on record it is the daemon's repository of the record and the
// `--repo` argument of its origin remote; a path inside the workspace root
// with no record (or one that the route rule refuses) is an error, and no gh
// command runs in it. Each other path, and every path with the option
// empty, is the path and no argument, the call of today.
func ghDirForExecutionPath(ctx context.Context, path string) (string, []string, error) {
	s := runAsSettingsNow()
	if s == nil || path == "" {
		return path, nil, nil
	}
	agent, err := gitRoutesToAgent(s.workspaceRoot, path)
	if err != nil {
		return "", nil, fmt.Errorf("gh: %w", err)
	}
	if !agent {
		return path, nil, nil
	}
	rec := agentWorkspaceRecordOf(path)
	if rec == nil || rec.daemonRepo == "" {
		return "", nil, fmt.Errorf("gh: no record of the agent workspace %s: no gh command runs in it", path)
	}
	repoArgs, err := ghRepoArgsOfDaemonRepo(ctx, rec.daemonRepo)
	if err != nil {
		return "", nil, err
	}
	return rec.daemonRepo, repoArgs, nil
}

// ghRepoArgsOfDaemonRepo is `--repo ARG` for the origin remote of the
// daemon's repository daemonRepo, read by the daemon (ghRepoArg).
func ghRepoArgsOfDaemonRepo(ctx context.Context, daemonRepo string) ([]string, error) {
	cmd := handOverDaemonGit(ctx, daemonRepo, handOverGitArgs("remote", "get-url", "origin")...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh: read the origin remote of the daemon's repository %s: %w", daemonRepo, err)
	}
	repo, err := ghRepoArg(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, fmt.Errorf("gh: the origin remote of the daemon's repository %s: %w", daemonRepo, err)
	}
	return []string{"--repo", repo}, nil
}

// ghRepoArg is the value of the --repo argument of gh for the remote URL
// remote: OWNER/NAME for the host github.com, HOST/OWNER/NAME for each other
// host (GitHub Enterprise), so gh never resolves the repository of another
// host against github.com. It takes the https and ssh URL forms (a port is
// dropped) and the scp-like form; a final `/` and then the suffix `.git` are
// removed. The user part of the URL is never in the result, and its error
// holds the URL only through redactRemoteURL.
func ghRepoArg(remote string) (string, error) {
	s := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(remote), "/"), ".git")
	var authority, path string
	if i := strings.Index(s, "://"); i >= 0 {
		var ok bool
		if authority, path, ok = strings.Cut(s[i+3:], "/"); !ok {
			return "", fmt.Errorf("remote %q: no path", redactRemoteURL(remote))
		}
	} else if colon := strings.Index(s, ":"); colon > 0 && !strings.Contains(s[:colon], "/") {
		authority, path = s[:colon], s[colon+1:]
	} else {
		return "", fmt.Errorf("remote %q: no host", redactRemoteURL(remote))
	}
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		authority = authority[at+1:]
	}
	host := authority
	if strings.HasPrefix(host, "[") {
		if end := strings.Index(host, "]"); end >= 0 {
			host = host[:end+1]
		}
	} else if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	host = strings.ToLower(host)
	parts := strings.Split(strings.TrimLeft(path, "/"), "/")
	if host == "" || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("remote %q: want HOST and OWNER/NAME", redactRemoteURL(remote))
	}
	if host == "github.com" {
		return parts[0] + "/" + parts[1], nil
	}
	return host + "/" + parts[0] + "/" + parts[1], nil
}

// redactRemoteURL is remote with the part before its last `@` (the user and
// a password or token) replaced by REDACTED; the scheme stays.
func redactRemoteURL(remote string) string {
	at := strings.LastIndex(remote, "@")
	if at < 0 {
		return remote
	}
	prefix := ""
	if i := strings.Index(remote, "://"); i >= 0 && i < at {
		prefix = remote[:i+3]
	}
	return prefix + "REDACTED" + remote[at:]
}

// remoteDir is the directory of the remote commands: the daemon's repository
// of an agent workspace, projectPath otherwise.
func (g *GitOperations) remoteDir() string {
	if g.daemonRepo != "" {
		return g.daemonRepo
	}
	return g.projectPath
}

// beginHandOverAttempt starts a push attempt of branch for an agent
// workspace: the attempt SHA is cleared before any step that can fail, so a
// failed attempt is never compared with the SHA of an earlier one, and the
// workspace record says "not pushed" until this attempt is published.
// Decision: the record is written under handOverMu, as in
// recordPublishedHandOver and pushHandOver, so the record and the attempt and
// published SHAs change in one order: a concurrent Push of g cannot reset the
// record to "not pushed" after another attempt recorded its push.
func (g *GitOperations) beginHandOverAttempt(branch string) {
	g.handOverMu.Lock()
	defer g.handOverMu.Unlock()
	g.attemptSHA, g.attemptBranch = "", strings.TrimPrefix(branch, "refs/heads/")
	if g.workspace != nil {
		g.workspace.setHandOver("", false)
	}
}

// recordPublishedHandOver records that the remote branch holds sha, the
// hand-over SHA of this attempt: the published SHA and its branch, and the
// workspace record as pushed. The success path of pushHandOver and the
// forgiveness of a failed push (acceptForgivenPush) both call it.
func (g *GitOperations) recordPublishedHandOver(sha, branch string) {
	g.handOverMu.Lock()
	defer g.handOverMu.Unlock()
	g.publishedSHA, g.publishedBranch = sha, strings.TrimPrefix(branch, "refs/heads/")
	if g.workspace != nil {
		g.workspace.setHandOver(sha, true)
	}
}

// countHandOverSHA is the head the count of new commits of an agent
// workspace reads: the published SHA when it was recorded for the branch of
// the current attempt, "" when nothing is published for that branch.
// attempted is false before the first push attempt of g.
func (g *GitOperations) countHandOverSHA() (sha string, attempted bool) {
	g.handOverMu.Lock()
	defer g.handOverMu.Unlock()
	if g.attemptBranch == "" {
		return "", false
	}
	if g.publishedBranch != g.attemptBranch {
		return "", true
	}
	return g.publishedSHA, true
}

// attemptHandOverSHA is the hand-over SHA of the current (or last) push
// attempt, "" when its hand-over did not succeed.
func (g *GitOperations) attemptHandOverSHA() string {
	g.handOverMu.Lock()
	defer g.handOverMu.Unlock()
	return g.attemptSHA
}

// publishedHandOverSHA is the head that the last published hand-over push
// put on the remote (recordPublishedHandOver), "" when none was published.
func (g *GitOperations) publishedHandOverSHA() string {
	g.handOverMu.Lock()
	defer g.handOverMu.Unlock()
	return g.publishedSHA
}

// daemonGitEnvSet are the GIT_ names the code sets on purpose on a
// daemon-side git command of the delivery of an agent workspace, after the
// environment of the daemon and before daemonGitEnv: GIT_ASKPASS and
// GIT_TERMINAL_PROMPT (withGitCredentials; its PILOT_GIT_TOKEN is no GIT_
// name), and the names of appendGitEnv (gitAgentEnv): GIT_NO_REPLACE_OBJECTS
// (the co-author read, the gate), GIT_AUTHOR_NAME, GIT_AUTHOR_EMAIL,
// GIT_AUTHOR_DATE, GIT_COMMITTER_NAME, GIT_COMMITTER_EMAIL and
// GIT_COMMITTER_DATE (the co-author rewrite).
var daemonGitEnvSet = func() map[string]bool {
	set := map[string]bool{"GIT_ASKPASS": true, "GIT_TERMINAL_PROMPT": true}
	for name := range gitAgentEnv {
		set[name] = true
	}
	return set
}()

// daemonGitEnv is the environment of the daemon-side git commands of the
// delivery of an agent workspace (TASK-651: the gate, the push, the remote
// probes, the fetch and the count, the co-author read), so each works in the
// daemon's repository and not in one that a variable of the daemon names:
// inherited (the environment of the daemon) without each name that starts
// with GIT_, then set (the values the code added to the command) without each
// GIT_ name that is not in daemonGitEnvSet. Decision: an allow rule, not a
// list of denied names: GIT_COMMON_DIR sent the push to the remote of
// another repository, GIT_GRAFT_FILE and GIT_SHALLOW_FILE made the gate read
// another history than the push sends, and git keeps adding such names.
func daemonGitEnv(inherited, set []string) []string {
	env := slices.DeleteFunc(slices.Clone(inherited), func(kv string) bool {
		return strings.HasPrefix(kv, "GIT_")
	})
	for _, kv := range set {
		if name, _, _ := strings.Cut(kv, "="); !strings.HasPrefix(name, "GIT_") || daemonGitEnvSet[name] {
			env = append(env, kv)
		}
	}
	return env
}

// daemonGateEnv is the environment of the git commands of the secret gate for
// an agent workspace: daemonGitEnv of the daemon environment without each
// name handOverEnvAllowed refuses (only PATH, HOME, TMPDIR, USER and LOGNAME
// pass). The gate adds GIT_NO_REPLACE_OBJECTS itself.
// Decision: the gate commands are local, so no token of the daemon
// (GITHUB_TOKEN, PILOT_GIT_TOKEN) reaches them.
func daemonGateEnv(env []string) []string {
	return slices.DeleteFunc(daemonGitEnv(env, nil), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return !handOverEnvAllowed(name)
	})
}

// withDaemonGitEnv sets the environment of cmd to daemonGitEnv. It runs after
// withGitCredentials and appendGitEnv, which build cmd.Env as os.Environ()
// followed by the values they set: those values are the set part, the
// os.Environ() prefix is inherited. A cmd.Env of another shape is all
// inherited, so it keeps no GIT_ name. A command the constructor failed
// closed, or one of the agent route, is left as it is.
func withDaemonGitEnv(cmd *exec.Cmd) *exec.Cmd {
	if gitRouteFailed(cmd) {
		return cmd
	}
	if _, agent := agentCmdUser(cmd); agent {
		return cmd
	}
	base := os.Environ()
	inherited, set := cmd.Env, []string(nil)
	switch {
	case cmd.Env == nil:
		inherited = base
	case len(cmd.Env) >= len(base) && slices.Equal(cmd.Env[:len(base)], base):
		inherited, set = cmd.Env[:len(base)], cmd.Env[len(base):]
	}
	cmd.Env = daemonGitEnv(inherited, set)
	return cmd
}

// remoteGitCmd builds a remote command of the delivery (a fetch, an
// ls-remote) in remoteDir: hooks off (secretpatterns.HooksOffArgs), the
// credentials of withGitCredentials and, for an agent workspace, the
// environment of daemonGitEnv. Decision: hooks off with the option empty too:
// a reference-transaction hook of the repository would run with the token in
// its environment.
func (g *GitOperations) remoteGitCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := gitCommandIn(ctx, g.remoteDir(), append(secretpatterns.HooksOffArgs(), args...)...)
	withGitCredentials(ctx, cmd)
	if g.daemonRepo != "" {
		withDaemonGitEnv(cmd)
	}
	return cmd
}

// ProjectPath returns the working-tree directory these operations act on
// (GH-5435/GH-5437: needed by the acceptance-evidence runner to execute
// paste-output commands and apply/revert mutations in the same worktree the
// PR is being created from, rather than the process's ambient CWD).
func (g *GitOperations) ProjectPath() string {
	return g.projectPath
}

// CreateBranch creates a new branch
func (g *GitOperations) CreateBranch(ctx context.Context, branchName string) error {
	cmd := g.gitCmd(ctx, "checkout", "-b", branchName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create branch: %w: %s", err, output)
	}
	return nil
}

// CreateOrResetBranch creates a branch or resets it if it already exists.
// Uses git checkout -B (uppercase) which force-creates the branch.
// This is safe when worktree already created the branch (GH-1235).
func (g *GitOperations) CreateOrResetBranch(ctx context.Context, branchName string) error {
	cmd := g.gitCmd(ctx, "checkout", "-B", branchName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create/reset branch: %w: %s", err, output)
	}
	return nil
}

// CreateOrResetBranchFromOrigin fetches origin/<baseBranch> (best-effort,
// via resolveGuardBaseRef) and force-creates/resets branchName directly from
// the resolved ref via `git checkout -B <branchName> <baseRef>`. Falls back
// to the local <baseBranch> ref when origin isn't reachable (offline, no
// "origin" remote configured — some test fixtures), preserving behavior in
// that environment.
//
// This unconditionally discards any commits an existing branchName already
// carried, so it is only safe to use where a pre-existing branch cannot yet
// hold real work — e.g. the decomposed-parent branch step, which runs before
// any subtask executes. Callers where a task branch may already carry
// legitimate in-progress commits from an earlier attempt (the common
// single-task execution path) must use EnsureBranchFromOrigin instead, which
// only recreates the branch when it is actually stale.
func (g *GitOperations) CreateOrResetBranchFromOrigin(ctx context.Context, branchName, baseBranch string) (string, error) {
	baseRef := g.resolveGuardBaseRef(ctx, baseBranch)

	cmd := g.gitCmd(ctx, "checkout", "-B", branchName, baseRef)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to create/reset branch %s from %s: %w: %s", branchName, baseRef, err, output)
	}
	return baseRef, nil
}

// EnsureBranchFromOrigin fetches origin/<baseBranch> (best-effort, via
// resolveGuardBaseRef) and ensures branchName exists, cut from that resolved
// ref:
//
//   - branchName doesn't exist locally yet: created directly from baseRef.
//   - branchName exists but is behind baseRef (stale — e.g. left over from a
//     prior, now-superseded run, GH-912): deleted and recreated from baseRef.
//     If it is the branch the clone is currently on (GH-5829), HEAD is first
//     detached at baseRef (clean tree) or the call errors (dirty tree).
//   - branchName exists and is NOT behind baseRef (it may carry legitimate
//     work-in-progress commits from an earlier attempt on this same clone):
//     switched to as-is, preserving those commits.
//
// GH-4594: direct-mode (non-worktree) task branches used to be cut via
// SwitchToDefaultBranchAndPull/SwitchToBranchAndPull (checkout the LOCAL base
// branch, then `git pull` — a merge) followed by CreateBranch (checkout -b
// with no explicit start point, i.e. from whatever HEAD the pull left
// behind) — and, on the stale-branch path, CommitsBehindMain's own fresh
// `git fetch` was discarded immediately afterward because the recreate step
// (CreateBranch) still branched off that same ambient HEAD instead of the
// ref CommitsBehindMain had just resolved. Pull failure is also silently
// swallowed (non-fatal, to support offline use), so any fetch hiccup left
// the branch-doesn't-exist-yet path cutting from stale local state too. This
// method fixes both: baseRef is resolved once up front from a real fetch,
// and every branch-creating code path below is given that ref explicitly —
// never ambient HEAD.
//
// Returns the base ref actually used, and whether the branch was freshly
// (re)created from it (false when an existing non-stale branch was merely
// switched to).
func (g *GitOperations) EnsureBranchFromOrigin(ctx context.Context, branchName, baseBranch string) (baseRef string, created bool, err error) {
	baseRef = g.resolveGuardBaseRef(ctx, baseBranch)

	if !g.branchExists(ctx, branchName) {
		if createErr := g.checkoutNewBranchFrom(ctx, branchName, baseRef); createErr != nil {
			return baseRef, false, fmt.Errorf("failed to create branch %s from %s: %w", branchName, baseRef, createErr)
		}
		return baseRef, true, nil
	}

	stale, staleErr := g.branchIsBehind(ctx, branchName, baseRef)
	if staleErr != nil {
		// Fail open on the staleness check itself (mirrors CommitsBehindMain's
		// prior non-fatal logging): fall through to switching to the existing
		// branch as-is rather than risking discarding unverified work.
		stale = false
	}

	if !stale {
		if switchErr := g.SwitchBranch(ctx, branchName); switchErr != nil {
			return baseRef, false, fmt.Errorf("failed to switch to existing branch %s: %w", branchName, switchErr)
		}
		return baseRef, false, nil
	}

	// Decision (GH-5829): git refuses `branch -D` on the branch HEAD is on, and
	// direct mode never switches the clone back to the default branch after a
	// task, so a retry after a closed PR always starts on the stale branch.
	// Detach HEAD at baseRef first (clean tree only — a dirty tree could hold
	// unreviewed work, so refuse with the real cause instead of git's message).
	current, curErr := g.GetCurrentBranch(ctx)
	if curErr == nil && current == branchName {
		dirty, dirtyErr := g.HasUncommittedChanges(ctx)
		if dirtyErr != nil {
			return baseRef, false, fmt.Errorf("failed to check working tree before recreating stale branch %s: %w", branchName, dirtyErr)
		}
		if dirty {
			return baseRef, false, fmt.Errorf("stale branch %s is checked out in the clone, refusing to recreate it: working tree has uncommitted changes", branchName)
		}
		if detachErr := g.detachHeadAt(ctx, baseRef); detachErr != nil {
			return baseRef, false, fmt.Errorf("failed to detach HEAD from stale branch %s: %w", branchName, detachErr)
		}
	}

	if delErr := g.DeleteBranch(ctx, branchName); delErr != nil {
		return baseRef, false, fmt.Errorf("failed to delete stale branch %s: %w", branchName, delErr)
	}
	if createErr := g.checkoutNewBranchFrom(ctx, branchName, baseRef); createErr != nil {
		return baseRef, false, fmt.Errorf("failed to recreate stale branch %s from %s: %w", branchName, baseRef, createErr)
	}
	return baseRef, true, nil
}

// detachHeadAt runs `git checkout --detach <ref>`.
func (g *GitOperations) detachHeadAt(ctx context.Context, ref string) error {
	cmd := g.gitCmd(ctx, "checkout", "--detach", ref)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, output)
	}
	return nil
}

// checkoutNewBranchFrom runs `git checkout -b <branchName> <startPoint>`.
// Assumes branchName does not already exist locally (callers are
// responsible for that precondition — EnsureBranchFromOrigin only calls this
// after confirming absence or deleting a stale branch).
func (g *GitOperations) checkoutNewBranchFrom(ctx context.Context, branchName, startPoint string) error {
	cmd := g.gitCmd(ctx, "checkout", "-b", branchName, startPoint)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, output)
	}
	return nil
}

// branchIsBehind reports whether branchName is missing any commits that
// baseRef has — i.e. `git rev-list --count <branchName>..<baseRef>` > 0.
// This only detects "behind"; a branch that has both unique commits AND is
// missing baseRef commits (diverged) is also reported stale here, matching
// CommitsBehindMain's prior GH-912 semantics.
func (g *GitOperations) branchIsBehind(ctx context.Context, branchName, baseRef string) (bool, error) {
	cmd := g.gitCmd(ctx, "rev-list", "--count", branchName+".."+baseRef)
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to count commits behind: %w", err)
	}
	var count int
	if _, scanErr := fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &count); scanErr != nil {
		return false, fmt.Errorf("failed to parse behind count %q: %w", strings.TrimSpace(string(output)), scanErr)
	}
	return count > 0, nil
}

// SwitchBranch switches to an existing branch
func (g *GitOperations) SwitchBranch(ctx context.Context, branchName string) error {
	cmd := g.gitCmd(ctx, "checkout", branchName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to switch branch: %w: %s", err, output)
	}
	return nil
}

// Commit stages filtered changes and commits. Files matching defaultExcludeDirs
// or defaultExcludeGlobs are never auto-staged. Returns ErrNoStageableChanges
// (wrapped) when all dirty paths are excluded.
func (g *GitOperations) Commit(ctx context.Context, message string) (string, error) {
	// Enumerate dirty paths via NUL-delimited porcelain output.
	statusCmd := g.gitCmd(ctx, "status", "--porcelain", "-z")
	statusOut, err := statusCmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to enumerate dirty paths: %w", err)
	}

	var stage []string
	var skipped []string
	// -z output: NUL-terminated entries, each "XY path" (3-char prefix + path).
	for _, entry := range strings.Split(strings.TrimRight(string(statusOut), "\x00"), "\x00") {
		if len(entry) < 4 {
			continue
		}
		path := entry[3:]
		if isExcluded(path) {
			skipped = append(skipped, path)
			continue
		}
		stage = append(stage, path)
	}

	if len(stage) == 0 {
		return "", fmt.Errorf("%w: skipped paths: %v", ErrNoStageableChanges, skipped)
	}

	// Stage filtered set; -- disambiguates paths from flags.
	addArgs := append([]string{"add", "--"}, stage...)
	addCmd := g.gitCmd(ctx, addArgs...)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to stage changes: %w: %s", err, output)
	}

	// Commit
	commitCmd := g.gitCmd(ctx, "commit", "-m", message)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to commit: %w: %s", err, output)
	}

	// Get commit SHA
	shaCmd := g.gitCmd(ctx, "rev-parse", "HEAD")
	output, err := shaCmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get commit SHA: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// memoryDocsRelDir and graphJSONRelPath are the Navigator knowledge-graph
// paths that scripts/check-graph.py's Knowledge Graph Drift Gate validates
// (GH-4286).
const (
	memoryDocsRelDir = ".agent/knowledge/memories"
	graphJSONRelPath = ".agent/knowledge/graph.json"
)

// graphJSONPathFields are the node fields check-graph.py accepts as a memory
// file reference. Unlike check-graph.py (which takes the first field present
// and stops), the Go readers below check ALL of these per node — GH-4496
// found the "stop at first field" shortcut can leave a resolvable path
// ignored when a node carries more than one path-shaped field.
var graphJSONPathFields = []string{"file", "path", "memory_file"}

// memoryGraphDoc is the subset of .agent/knowledge/graph.json the executor's
// memory-doc guards read: node groups (only "memories" carries file paths)
// plus concept_index, which GH-4496 strike 3 showed can carry a memory doc's
// slug even when no node's path field resolves cleanly.
type memoryGraphDoc struct {
	Nodes struct {
		Memories map[string]map[string]any `json:"memories"`
	} `json:"nodes"`
	ConceptIndex map[string][]string `json:"concept_index"`
}

// loadMemoryGraph reads and parses .agent/knowledge/graph.json once for all
// of the memory-doc guards below. Returns (nil, nil) when the file is
// absent — a project without one has no drift gate to protect.
func (g *GitOperations) loadMemoryGraph(ctx context.Context) (*memoryGraphDoc, error) {
	raw, err := readMemoryGraphFile(ctx, filepath.Join(g.projectPath, graphJSONRelPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read graph.json: %w", err)
	}

	var graph memoryGraphDoc
	if err := json.Unmarshal(raw, &graph); err != nil {
		return nil, fmt.Errorf("failed to parse graph.json: %w", err)
	}
	return &graph, nil
}

// memoryGraphReadTimeout bounds one read of readMemoryGraphFile as the agent
// user.
const memoryGraphReadTimeout = 10 * time.Second

// readMemoryGraphFile is os.ReadFile for the reads of the memory-doc guards.
// TASK-651: with the option set and a path inside the workspace root the file
// is read through agentReadRegularFile, so a link the agent put at the path is
// followed as the agent user, never as the daemon user; a path that is not a
// regular file (a FIFO the agent put there) is refused, and the read has a
// time limit of memoryGraphReadTimeout. A read error, a refusal or the time
// limit there gives the result of today for a missing file (an error that
// wraps fs.ErrNotExist) and one Warn line.
func readMemoryGraphFile(ctx context.Context, path string) ([]byte, error) {
	if runAsSettingsNow() == nil {
		return os.ReadFile(path) // #nosec G304 -- the os call of today
	}
	if agent, err := agentFileRoute(path); err == nil && !agent {
		return os.ReadFile(path) // #nosec G304 -- a path outside the workspace root
	}
	readCtx, cancel := context.WithTimeout(ctx, memoryGraphReadTimeout)
	defer cancel()
	raw, err := agentReadRegularFile(readCtx, path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("memory-doc guard: read as the agent user failed, taken as a missing file",
				slog.String("path", path), slog.Any("error", err))
		}
		return nil, &fs.PathError{Op: "read", Path: path, Err: fs.ErrNotExist}
	}
	return raw, nil
}

// memoryGraphFileExists is the existence check of a memory doc that a
// graph.json node names: os.Stat with the option empty or a path outside the
// workspace root, readMemoryGraphFile otherwise.
func memoryGraphFileExists(ctx context.Context, path string) bool {
	if runAsSettingsNow() == nil {
		_, err := os.Stat(path)
		return err == nil
	}
	if agent, err := agentFileRoute(path); err == nil && !agent {
		_, err := os.Stat(path)
		return err == nil
	}
	_, err := readMemoryGraphFile(ctx, path)
	return err == nil
}

// loadMemoryGraphAtRef reads and parses graph.json as it existed at ref
// (e.g. baseBranch), independent of whatever the current working tree/HEAD
// holds. This matters for both EnforceMemoryDocDeletionGuard (the veto leg)
// and graphIndexedMemoryNodes (the restore leg, GH-4581): strikes 1-2 of the
// TASK-410 series deleted a memory doc AND its graph.json node in the same
// commit, so checking indexed-status against HEAD's graph.json sees no
// dangling reference and misses it entirely. Checking against baseBranch's
// graph.json instead answers the question that actually matters: was this
// doc part of the curated knowledge graph before this branch touched it?
// Returns (nil, nil) when graph.json didn't exist at ref.
func (g *GitOperations) loadMemoryGraphAtRef(ctx context.Context, ref string) (*memoryGraphDoc, error) {
	cmd := g.gitCmd(ctx, "show", ref+":"+graphJSONRelPath)
	raw, err := cmd.Output()
	if err != nil {
		// Most common cause: graph.json didn't exist at ref. Treat the same
		// as "no graph to protect" rather than failing the guard closed.
		return nil, nil
	}

	var graph memoryGraphDoc
	if err := json.Unmarshal(raw, &graph); err != nil {
		return nil, fmt.Errorf("failed to parse graph.json at %s: %w", ref, err)
	}
	return &graph, nil
}

// memorySlug returns a memory doc's graph-identity slug: its basename with
// the .md extension stripped. Current-convention nodes key nodes.memories by
// this slug directly; legacy nodes instead carry a path-shaped field whose
// basename is this slug.
func memorySlug(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".md")
}

// indexedMemorySlugs returns every memory-doc slug that graph.json
// references ANYWHERE: as a nodes.memories node ID (current convention), as
// the basename of a node's file/path/memory_file field (regardless of
// whether that field resolves to an existing path on disk), or as a value
// inside concept_index. This is intentionally broader and more permissive
// than resolving exact paths — GH-4496 strike 3 (PR #4495, commit 78870958)
// deleted a doc whose node WAS present in nodes.memories with a correct
// "file" field; whatever the strip pass's exact-path check missed, a
// slug appearing anywhere in the graph must still veto deletion.
func indexedMemorySlugs(graph *memoryGraphDoc) map[string]bool {
	slugs := make(map[string]bool)
	if graph == nil {
		return slugs
	}
	for nodeID, node := range graph.Nodes.Memories {
		slugs[nodeID] = true
		for _, field := range graphJSONPathFields {
			if rawPath, ok := node[field].(string); ok {
				slugs[memorySlug(rawPath)] = true
			}
		}
	}
	for _, values := range graph.ConceptIndex {
		for _, v := range values {
			slugs[v] = true
		}
	}
	return slugs
}

// addedMemoryDocs returns memory doc paths (relative to the repo root) added
// between baseBranch and HEAD. Mirrors the file selection in
// scripts/check-graph.py's find_unindexed_memory_files: only *.md files,
// skipping the "resolved" archive subtree and README*.
func (g *GitOperations) addedMemoryDocs(ctx context.Context, baseBranch string) ([]string, error) {
	cmd := g.gitCmd(ctx, "diff", "--name-only", "--diff-filter=A",
		baseBranch+"...HEAD", "--", memoryDocsRelDir)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to diff added memory docs: %w", err)
	}

	var docs []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" || !strings.HasSuffix(line, ".md") {
			continue
		}
		rel := strings.TrimPrefix(line, memoryDocsRelDir+"/")
		if parts := strings.SplitN(rel, "/", 2); parts[0] == "resolved" {
			continue
		}
		if strings.HasPrefix(filepath.Base(line), "README") {
			continue
		}
		docs = append(docs, line)
	}
	return docs, nil
}

// indexedMemoryPaths reads .agent/knowledge/graph.json and returns the set of
// repo-relative memory doc paths referenced by a node's file/path/memory_file
// field, resolved the same way scripts/check-graph.py's resolve_path does:
// tried both as root-relative and as relative to .agent/knowledge/, keeping
// whichever candidate exists on disk. Returns (nil, nil) when graph.json is
// absent — a project without the file has no drift gate to protect.
func (g *GitOperations) indexedMemoryPaths(ctx context.Context) (map[string]bool, error) {
	graph, err := g.loadMemoryGraph(ctx)
	if err != nil || graph == nil {
		return nil, err
	}

	indexed := make(map[string]bool)
	for _, node := range graph.Nodes.Memories {
		// GH-4496: check every path-shaped field on the node, not just the
		// first one present — a node carrying both a resolvable and a stale
		// field must not have the resolvable one shadowed.
		for _, field := range graphJSONPathFields {
			rawPath, ok := node[field].(string)
			if !ok {
				continue
			}
			for _, candidate := range []string{
				filepath.Join(g.projectPath, rawPath),
				filepath.Join(g.projectPath, ".agent", "knowledge", rawPath),
			} {
				if !memoryGraphFileExists(ctx, candidate) {
					continue
				}
				if rel, relErr := filepath.Rel(g.projectPath, candidate); relErr == nil {
					indexed[filepath.ToSlash(rel)] = true
				}
			}
		}
	}
	return indexed, nil
}

// StripUnindexedMemoryDocs removes newly-added .agent/knowledge/memories/**.md
// files (relative to baseBranch) that have no corresponding node in
// .agent/knowledge/graph.json, committing the removal as a follow-up commit.
// Returns the list of stripped paths (nil if none).
//
// GH-4286: a Pilot execution that commits a memory doc without indexing it in
// graph.json trips the Knowledge Graph Drift Gate CI check
// (scripts/check-graph.py), which the autopilot CI-fix path then treats as a
// real build failure — up to closing the PR via the size guard (PR #4279 was
// lost this way). Memory-doc authoring is Navigator's lane, not an
// execution's (project CLAUDE.md "Memory: Navigator only"), so an execution
// that added one unindexed is out of its lane; stripping it here keeps the
// rest of the diff intact instead of failing the whole task.
//
// GH-4496: a doc only counts as unindexed when BOTH the exact-path check
// (indexedMemoryPaths) AND the slug-anywhere-in-the-graph check
// (indexedMemorySlugs) come back negative — three strikes in 26 hours showed
// the exact-path check alone can misjudge a genuinely indexed doc (strike 3,
// PR #4495/commit 78870958, deleted a doc whose node had a correct "file"
// field). Every doc considered and the verdict is logged so a future
// misjudgment is diagnosable from the run log instead of only from a later
// graph-vs-disk audit.
func (g *GitOperations) StripUnindexedMemoryDocs(ctx context.Context, baseBranch string) ([]string, error) {
	// GH-4582: resolve baseRef once (origin/<baseBranch> when it resolves,
	// falling back to the raw baseBranch) so this leg agrees with its
	// siblings RestoreDeletedIndexedMemoryDocs and EnforceMemoryDocDeletionGuard
	// on what the base branch contains — a stale local baseBranch made this
	// leg misclassify pre-existing origin docs as branch-added, which the
	// origin-relative veto leg then correctly refused to let it delete.
	baseRef := g.resolveGuardBaseRef(ctx, baseBranch)

	added, err := g.addedMemoryDocs(ctx, baseRef)
	if err != nil || len(added) == 0 {
		return nil, err
	}

	graph, err := g.loadMemoryGraph(ctx)
	if err != nil {
		return nil, err
	}
	if graph == nil {
		// No graph.json in this project - nothing to reconcile against.
		return nil, nil
	}

	indexed, err := g.indexedMemoryPaths(ctx)
	if err != nil {
		return nil, err
	}
	slugs := indexedMemorySlugs(graph)

	var unindexed []string
	for _, doc := range added {
		slug := memorySlug(doc)
		byPath := indexed[doc]
		bySlug := slugs[slug]
		slog.Default().Debug("strip-unindexed-memory-docs: checked added doc",
			slog.String("doc", doc),
			slog.String("slug", slug),
			slog.Bool("indexed_by_path", byPath),
			slog.Bool("indexed_by_slug", bySlug),
		)
		if byPath || bySlug {
			continue
		}
		unindexed = append(unindexed, doc)
	}
	if len(unindexed) == 0 {
		return nil, nil
	}

	rmArgs := append([]string{"rm", "--"}, unindexed...)
	rmCmd := g.gitCmd(ctx, rmArgs...)
	if output, err := rmCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to remove unindexed memory doc(s): %w: %s", err, output)
	}

	message := "chore(memory): strip unindexed memory doc(s) added during execution\n\n" +
		"GH-4286: memory docs must be indexed in .agent/knowledge/graph.json in the\n" +
		"same commit or they trip the Knowledge Graph Drift Gate. Removed:\n" +
		strings.Join(unindexed, "\n")
	commitCmd := g.gitCmd(ctx, "commit", "-m", message)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to commit removal of unindexed memory doc(s): %w: %s", err, output)
	}

	return unindexed, nil
}

// resolveGuardBaseRef resolves the ref the memory-doc guards should diff
// against: origin/<baseBranch> when it resolves locally, falling back to the
// local <baseBranch> ref otherwise. Mirrors CountNewCommitsAgainstOrigin's
// GH-4566 fix for the no-commits guard.
//
// TASK-424: worktrees are cut from a freshly fetched origin/<base>
// (worktree.go), but nothing fast-forwards the shared clone's local <base>
// ref on every path — so a memory doc merged to origin/<base> after the
// local ref last synced was invisible to every guard leg that diffed against
// the stale local baseBranch. The doc was neither seen as deleted (strip
// leg diffs against a base that never had it) nor protected (the veto leg's
// baseBranch graph.json also predates it), so the deletion only materialized
// at squash-merge on GitHub, where no guard runs (strikes 4/5: #4534/#4535,
// #4551).
//
// Fetches origin/<baseBranch> first (best-effort — offline / no-remote
// environments fall through). If origin/<baseBranch> still doesn't resolve
// locally (no "origin" remote configured — bare local repos, some unit
// tests), returns the local <baseBranch> ref unchanged, preserving prior
// behavior in that environment.
func (g *GitOperations) resolveGuardBaseRef(ctx context.Context, baseBranch string) string {
	fetchCmd := g.gitCmd(ctx, "fetch", "origin", baseBranch)
	withGitCredentials(ctx, fetchCmd)
	_ = fetchCmd.Run() // best-effort; fall back to whatever origin/<base> already resolves to (or local base)

	verifyCmd := g.gitCmd(ctx, "rev-parse", "--verify", "-q", "origin/"+baseBranch)
	if err := verifyCmd.Run(); err == nil {
		return "origin/" + baseBranch
	}
	return baseBranch
}

// deletedMemoryDocs returns memory doc paths (relative to the repo root)
// deleted between baseRef and HEAD. Mirrors addedMemoryDocs's file
// selection but with --diff-filter=D: GH-4398's restore guard cares about
// removals, not additions, of .agent/knowledge/memories/**.md. Callers pass
// an already-resolved ref (see resolveGuardBaseRef) rather than a raw branch
// name.
func (g *GitOperations) deletedMemoryDocs(ctx context.Context, baseRef string) ([]string, error) {
	cmd := g.gitCmd(ctx, "diff", "--name-only", "--diff-filter=D",
		baseRef+"...HEAD", "--", memoryDocsRelDir)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to diff deleted memory docs: %w", err)
	}

	var docs []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" || !strings.HasSuffix(line, ".md") {
			continue
		}
		docs = append(docs, line)
	}
	return docs, nil
}

// graphIndexedMemoryNodes reads .agent/knowledge/graph.json AT baseRef (not
// the working tree's current/HEAD copy) and returns a map from repo-relative
// memory doc path to the graph node id that references it, covering both the
// "file"/"memory_file" (root-relative) and legacy "path" (relative to
// .agent/knowledge/) key shapes. Unlike indexedMemoryPaths, this does NOT
// require the file to exist on disk: GH-4398's restore guard runs precisely
// when the file is missing (deleted), so an on-disk existence check would
// always fail the case it exists to catch. Returns (nil, nil) when
// graph.json is absent at baseRef — no drift gate to protect.
//
// GH-4581 (8th occurrence of the TASK-410 phantom-deletion class): this used
// to read graph.json via loadMemoryGraph() — the working tree's current/HEAD
// copy — exactly the shape loadMemoryGraphAtRef's doc comment already warned
// against: strikes 1-2 (GH-4484, GH-4489) deleted a memory doc AND its
// graph.json node in the same commit, so a HEAD-relative read sees no
// dangling reference and the restore leg silently found nothing to restore.
// EnforceMemoryDocDeletionGuard (the veto leg) was fixed to read baseRef's
// graph.json for exactly this reason, but this restore leg — which runs
// BEFORE the veto leg and would have made the veto moot by restoring the
// file — was never given the same fix, so it kept missing precisely the
// deletions the veto leg still (correctly) hard-blocks pointer dispatch on.
// Reading baseRef here closes that gap: the restore leg now sees the same
// graph the veto leg does.
func (g *GitOperations) graphIndexedMemoryNodes(ctx context.Context, baseRef string) (map[string]string, error) {
	graph, err := g.loadMemoryGraphAtRef(ctx, baseRef)
	if err != nil || graph == nil {
		return nil, err
	}

	nodes := make(map[string]string)
	for nodeID, node := range graph.Nodes.Memories {
		// GH-4496: check every path-shaped field, not just the first present.
		for _, field := range graphJSONPathFields {
			rawPath, ok := node[field].(string)
			if !ok {
				continue
			}
			// Register both the root-relative ("file"/"memory_file") and the
			// legacy .agent/knowledge/-relative ("path") candidate forms —
			// whichever matches the diff's path wins, the other is simply
			// never looked up. Mirrors indexedMemoryPaths' candidate order.
			nodes[filepath.ToSlash(rawPath)] = nodeID
			nodes[filepath.ToSlash(filepath.Join(".agent", "knowledge", rawPath))] = nodeID
		}
	}
	return nodes, nil
}

// RestoredMemoryDoc describes one graph-indexed memory file that
// RestoreDeletedIndexedMemoryDocs restored after finding it deleted relative
// to baseBranch, plus the graph node id that still references it.
type RestoredMemoryDoc struct {
	Path   string
	NodeID string
}

// RestoreDeletedIndexedMemoryDocs is the restore leg of the GH-4387 protected-
// memory guard (GH-4398): it finds any .agent/knowledge/memories/**.md file
// deleted between baseBranch and HEAD that is still referenced by a node in
// .agent/knowledge/graph.json, restores it via `git checkout <baseBranch> --
// <path>` as a fail-safe, and stages the restoration as a follow-up commit so
// the file rides the same PR the guard is protecting.
//
// Deleting a genuinely unindexed memory doc remains allowed: only paths with
// a surviving graph node are restored. Returns (nil, nil) when there is
// nothing to restore (no deletions under memories/, no graph.json, or none of
// the deletions are graph-indexed).
//
// Callers own logging + ledger visibility for each restored doc (see
// Runner.recordMemoryGuardRestoreEvents) and deciding when in the push/PR
// path this runs (GH-4397); this method only performs the git-level restore.
func (g *GitOperations) RestoreDeletedIndexedMemoryDocs(ctx context.Context, baseBranch string) ([]RestoredMemoryDoc, error) {
	baseRef := g.resolveGuardBaseRef(ctx, baseBranch)

	deleted, err := g.deletedMemoryDocs(ctx, baseRef)
	if err != nil || len(deleted) == 0 {
		return nil, err
	}

	nodes, err := g.graphIndexedMemoryNodes(ctx, baseRef)
	if err != nil || len(nodes) == 0 {
		return nil, err
	}

	var restored []RestoredMemoryDoc
	for _, doc := range deleted {
		if nodeID, ok := nodes[doc]; ok {
			restored = append(restored, RestoredMemoryDoc{Path: doc, NodeID: nodeID})
		}
	}
	if len(restored) == 0 {
		return nil, nil
	}

	// TASK-424: restore from baseRef (origin/<baseBranch> when it resolves),
	// not the raw baseBranch — a doc merged to origin after the local branch
	// last synced exists there even though the stale local ref never saw it.
	checkoutArgs := append([]string{"checkout", baseRef, "--"}, restoredPaths(restored)...)
	checkoutCmd := g.gitCmd(ctx, checkoutArgs...)
	if output, err := checkoutCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to restore protected memory doc(s): %w: %s", err, output)
	}

	addArgs := append([]string{"add", "--"}, restoredPaths(restored)...)
	addCmd := g.gitCmd(ctx, addArgs...)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to stage restored memory doc(s): %w: %s", err, output)
	}

	message := "fix(memory): restore graph-indexed memory doc(s) deleted during execution\n\n" +
		"GH-4387/GH-4398: the protected-memory guard detected deletion of file(s)\n" +
		"still referenced by .agent/knowledge/graph.json and restored them as a\n" +
		"fail-safe so the Knowledge Graph Drift Gate does not wedge this PR.\n" +
		"Restored:\n" + strings.Join(restoredPaths(restored), "\n")
	commitCmd := g.gitCmd(ctx, "commit", "-m", message)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to commit restored memory doc(s): %w: %s", err, output)
	}

	return restored, nil
}

// restoredPaths extracts the Path field from a []RestoredMemoryDoc, for use
// as git command arguments.
func restoredPaths(docs []RestoredMemoryDoc) []string {
	paths := make([]string, len(docs))
	for i, d := range docs {
		paths[i] = d.Path
	}
	return paths
}

// ErrMemoryDocDeletionVetoed is returned by EnforceMemoryDocDeletionGuard when
// the branch still carries a net deletion of a pre-existing
// .agent/knowledge/memories/**.md file after the strip/restore passes have
// already run, and the task never said it was allowed to touch memory docs.
var ErrMemoryDocDeletionVetoed = fmt.Errorf("execution deleted memory doc(s) outside its lane")

// EnforceMemoryDocDeletionGuard is the GH-4496 hard-veto leg of the TASK-410
// memory-loss series: it must run AFTER StripUnindexedMemoryDocs and
// RestoreDeletedIndexedMemoryDocs, and reports any file under
// .agent/knowledge/memories/ that is STILL net-deleted relative to
// baseBranch AND was indexed in baseBranch's graph.json before this branch
// touched it.
//
// The baseBranch check (not HEAD's current graph.json) is deliberate: in
// strikes 1-2 of the TASK-410 series (GH-4484, GH-4489) the offending commit
// deleted the memory doc AND its graph.json node together, so by the time
// any HEAD-relative check runs there is no dangling reference left to
// notice — the tree looks internally consistent even though it destroyed
// curated knowledge. Checking against the graph as it stood on baseBranch
// catches exactly that case. Deleting a doc that was never indexed on
// baseBranch either (see TestFinalizeDecomposedParentPR_AllowsDeletingUnindexedMemoryDoc)
// remains allowed — this guard is about protecting the graph's existing
// knowledge, not blocking all memory-doc deletions outright.
//
// Unless allowMemoryChanges is true — the task itself explicitly targets
// memory files — this is a hard veto (the caller must block push and fail
// the run) rather than advisory-only, converting a silent-data-loss class
// into a loud, reviewable failure.
func (g *GitOperations) EnforceMemoryDocDeletionGuard(ctx context.Context, baseBranch string, allowMemoryChanges bool) ([]string, error) {
	if allowMemoryChanges {
		return nil, nil
	}
	baseRef := g.resolveGuardBaseRef(ctx, baseBranch)

	deleted, err := g.deletedMemoryDocs(ctx, baseRef)
	if err != nil || len(deleted) == 0 {
		return nil, err
	}

	// TASK-424: read baseRef's graph.json (origin/<baseBranch> when it
	// resolves), not the raw baseBranch's — the stale local base's graph.json
	// can predate a doc's indexing (or deletion) that already landed on
	// origin, letting a genuinely-protected doc slip past the veto.
	baseGraph, err := g.loadMemoryGraphAtRef(ctx, baseRef)
	if err != nil {
		return nil, err
	}
	if baseGraph == nil {
		return nil, nil
	}
	baseSlugs := indexedMemorySlugs(baseGraph)

	var vetoed []string
	for _, doc := range deleted {
		if baseSlugs[memorySlug(doc)] {
			vetoed = append(vetoed, doc)
		}
	}
	if len(vetoed) == 0 {
		return nil, nil
	}
	return vetoed, fmt.Errorf("%w: %v", ErrMemoryDocDeletionVetoed, vetoed)
}

// memoryFileIntentKeywords are lower-cased substrings in a task's title or
// description that indicate the task itself is expected to touch
// .agent/knowledge/memories/** — e.g. a Navigator memory-authoring or
// memory-tooling task — so EnforceMemoryDocDeletionGuard should not veto its
// deletions.
var memoryFileIntentKeywords = []string{
	".agent/knowledge/memories",
	"memory doc",
	"memory-doc",
	"knowledge graph",
	"graph.json",
}

// taskExplicitlyTargetsMemoryFiles reports whether task's title or
// description mentions the memory-doc/knowledge-graph system explicitly,
// per memoryFileIntentKeywords. Used to scope EnforceMemoryDocDeletionGuard's
// veto to executions that never said they needed to touch memory docs.
func taskExplicitlyTargetsMemoryFiles(task *Task) bool {
	if task == nil {
		return false
	}
	haystack := strings.ToLower(task.Title + "\n" + task.Description)
	for _, kw := range memoryFileIntentKeywords {
		if strings.Contains(haystack, kw) {
			return true
		}
	}
	return false
}

// commitTrailerGuidance is appended to every execution prompt (GH-5711).
const commitTrailerGuidance = "Commit messages: no `Co-Authored-By` trailer and no mention of Claude, Claude Code or any AI tool in the subject or body.\n"

// anthropicCoAuthorRe matches the default Claude Code co-author trailer line.
var anthropicCoAuthorRe = regexp.MustCompile(`(?im)^[ \t]*Co-Authored-By:[^\n]*@anthropic\.com[^\n]*(?:\n|$)`)

// StripAnthropicCoAuthor removes any `Co-Authored-By:` trailer carrying an
// Anthropic address from every commit the branch is about to publish (GH-5711,
// GH-5719). Returns true when at least one commit was rewritten; commits
// without the trailer are untouched. See StripAnthropicCoAuthorRange.
func (g *GitOperations) StripAnthropicCoAuthor(ctx context.Context) (bool, error) {
	shas, err := g.StripAnthropicCoAuthorRange(ctx)
	return len(shas) > 0, err
}

// StripAnthropicCoAuthorRange is StripAnthropicCoAuthor returning the original
// shas of the rewritten commits. The GitHub squash body aggregates co-authors
// from every commit in the PR, so HEAD alone is not enough: the session, the
// memory-doc fixups and the wip auto-preserve commit all stack on one branch.
//
// The range is `HEAD --not --remotes=origin` (see unpublishedCommits).
// Decision: commits already reachable from any origin ref are excluded — they
// are published, and rewriting them would turn the next non-force push into a
// rejected non-fast-forward.
//
// When only HEAD matches it is amended (`--only`, no paths); otherwise the
// range is rewritten message-only via commit-tree so every tree hash is
// unchanged.
func (g *GitOperations) StripAnthropicCoAuthorRange(ctx context.Context) ([]string, error) {
	shas, _, err := g.stripAnthropicCoAuthorRange(ctx)
	return shas, err
}

// stripAnthropicCoAuthorRange does the work of StripAnthropicCoAuthorRange and
// additionally reports headOnly when the range was limited to HEAD because no
// origin refs exist, so the caller can warn.
func (g *GitOperations) stripAnthropicCoAuthorRange(ctx context.Context) (shas []string, headOnly bool, err error) {
	commits, headOnly, err := g.unpublishedCommits(ctx)
	if err != nil {
		return nil, false, err
	}
	var matching []string
	for _, sha := range commits {
		msg, err := g.commitMessage(ctx, sha)
		if err != nil {
			return nil, headOnly, err
		}
		if anthropicCoAuthorRe.MatchString(msg) {
			matching = append(matching, sha)
		}
	}
	if len(matching) == 0 {
		return nil, headOnly, nil
	}
	if len(matching) == 1 && matching[0] == commits[0] {
		return matching, headOnly, g.amendHeadWithoutAnthropicCoAuthor(ctx)
	}
	if err := g.rewriteCommitMessages(ctx, commits); err != nil {
		return nil, headOnly, err
	}
	return matching, headOnly, nil
}

func stripAnthropicCoAuthorLines(msg string) string {
	return strings.TrimRight(anthropicCoAuthorRe.ReplaceAllString(msg, ""), " \t\r\n") + "\n"
}

// gitHardened is the one constructor of the git commands of the co-author
// strip, which runs in the worktree the session can write directly before the
// push secret gate (GH-6353). Decision: it states what the command needs instead
// of reading the repository's config: hooks off and the gate's scan settings
// (secretpatterns.GitScanConfig: no signature display, no gpg program), no
// commit signing and no fsmonitor program, GIT_NO_REPLACE_OBJECTS=1 so a replace
// ref of the session cannot substitute a commit or tree the strip reads, and
// --no-show-signature on every git log and git show. Without it the amend, the
// rewrite and the ref update ran the session's hooks and gpg.program with the
// daemon's environment.
func (g *GitOperations) gitHardened(ctx context.Context, args ...string) *exec.Cmd {
	if len(args) > 0 && (args[0] == "log" || args[0] == "show") {
		args = append([]string{args[0], "--no-show-signature"}, args[1:]...)
	}
	full := append(secretpatterns.GitScanConfig(), "-c", "commit.gpgSign=false", "-c", "core.fsmonitor=false")
	cmd := g.gitCmd(ctx, append(full, args...)...)
	appendGitEnv(cmd, "GIT_NO_REPLACE_OBJECTS=1")
	return cmd
}

func (g *GitOperations) gitOutput(ctx context.Context, args ...string) (string, error) {
	cmd := g.gitHardened(ctx, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (g *GitOperations) commitMessage(ctx context.Context, sha string) (string, error) {
	out, err := g.gitOutput(ctx, "log", "-1", "--format=%B", sha)
	if err != nil {
		return "", fmt.Errorf("failed to read commit message: %w", err)
	}
	return out, nil
}

// unpublishedCommits lists the commits the next push would publish, newest
// first (HEAD at index 0). Decision: the range is `HEAD --not --remotes=origin`
// — everything not yet on any origin ref — so no default-branch resolution is
// needed (a failed lookup used to degrade silently to HEAD-only), and commits
// already published on another origin ref (epic children) are left alone.
// `--topo-order` guarantees children before parents even across merges, which
// rewriteCommitMessages relies on to re-parent correctly.
//
// Decision: when no ref exists under refs/remotes/origin/ (no remote, or only
// a remote with another name) `--not --remotes=origin` excludes nothing and
// rev-list would return the whole history, which the strip would then rewrite
// back to the root before a push that cannot succeed. In that case the range
// degrades to HEAD alone and headOnly is true so the caller can warn.
func (g *GitOperations) unpublishedCommits(ctx context.Context) (commits []string, headOnly bool, err error) {
	head, err := g.gitOutput(ctx, "rev-parse", "HEAD")
	if err != nil {
		return nil, false, fmt.Errorf("failed to resolve HEAD: %w", err)
	}
	head = strings.TrimSpace(head)

	refs, err := g.gitOutput(ctx, "for-each-ref", "--count=1", "--format=%(refname)", "refs/remotes/origin/")
	if err != nil {
		return nil, false, fmt.Errorf("failed to list origin refs: %w", err)
	}
	if strings.TrimSpace(refs) == "" {
		return []string{head}, true, nil
	}

	out, err := g.gitOutput(ctx, "rev-list", "--topo-order", "HEAD", "--not", "--remotes=origin")
	if err != nil {
		return nil, false, fmt.Errorf("failed to list unpublished commits: %w", err)
	}
	commits = strings.Fields(out)
	if len(commits) == 0 || commits[0] != head {
		// HEAD is already published (or the range is empty): nothing to rewrite.
		return nil, false, nil
	}
	return commits, false, nil
}

// amendHeadWithoutAnthropicCoAuthor replaces HEAD with a commit of the same
// tree and parents, the author of HEAD and the stripped message; the committer
// is the present identity, as with an amend.
//
// Decision: commit-tree and update-ref, not `git commit --amend`: git commit
// refreshes the index and so runs the clean filters of the repository
// (.gitattributes plus filter.*.clean, both writable by the session) as the
// user of the strip (TASK-651).
func (g *GitOperations) amendHeadWithoutAnthropicCoAuthor(ctx context.Context) error {
	msg, err := g.commitMessage(ctx, "HEAD")
	if err != nil {
		return err
	}
	meta, err := g.gitOutput(ctx, "log", "-1", "--date=raw", "--format=%H%n%an%n%ae%n%ad%n%T%n%P", "HEAD")
	if err != nil {
		return err
	}
	// A root commit has an empty %P line, which the trim drops.
	f := strings.Split(strings.TrimRight(meta, "\n"), "\n")
	if len(f) == 5 {
		f = append(f, "")
	}
	if len(f) < 6 {
		return fmt.Errorf("unexpected commit metadata for HEAD")
	}
	args := []string{"commit-tree", f[4]}
	for _, p := range strings.Fields(f[5]) {
		args = append(args, "-p", p)
	}
	amend := g.gitHardened(ctx, append(args, "-F", "-")...)
	appendGitEnv(amend, "GIT_AUTHOR_NAME="+f[1], "GIT_AUTHOR_EMAIL="+f[2], "GIT_AUTHOR_DATE="+f[3])
	amend.Stdin = strings.NewReader(stripAnthropicCoAuthorLines(msg))
	out, err := amend.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		return fmt.Errorf("failed to amend commit message: %w: %s", err, stderr)
	}
	if output, err := g.gitHardened(ctx, "update-ref",
		"-m", "pilot: strip Anthropic co-author trailer", "HEAD", strings.TrimSpace(string(out)), f[0]).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to amend commit message: %w: %s", err, output)
	}
	return nil
}

// rewriteCommitMessages rebuilds the given commits (newest first, HEAD first)
// with the Anthropic trailer removed, keeping each tree, author and committer
// identity and date. Commits whose ancestors were rewritten are re-created on
// top of the new parents; untouched leading commits keep their shas. Finally
// the checked-out branch is moved to the rewritten HEAD. Trees are identical,
// so the index and working tree stay valid.
func (g *GitOperations) rewriteCommitMessages(ctx context.Context, commits []string) error {
	oldHead := commits[0]
	rewritten := make(map[string]string, len(commits))
	inRange := make(map[string]bool, len(commits))
	for _, sha := range commits {
		inRange[sha] = true
	}
	// topo order lists children before parents; walk it backwards so parents are created first.
	for i := len(commits) - 1; i >= 0; i-- {
		sha := commits[i]
		meta, err := g.gitOutput(ctx, "log", "-1", "--date=raw", "--format=%an%n%ae%n%ad%n%cn%n%ce%n%cd%n%T%n%P", sha)
		if err != nil {
			return err
		}
		// A root commit has an empty %P line, which the trim above drops.
		f := strings.Split(strings.TrimRight(meta, "\n"), "\n")
		if len(f) == 7 {
			f = append(f, "")
		}
		if len(f) < 8 {
			return fmt.Errorf("unexpected commit metadata for %s", sha)
		}
		msg, err := g.commitMessage(ctx, sha)
		if err != nil {
			return err
		}

		changed := anthropicCoAuthorRe.MatchString(msg)
		var parents []string
		for _, p := range strings.Fields(f[7]) {
			if np, ok := rewritten[p]; ok {
				p = np
				changed = true
			}
			parents = append(parents, p)
		}
		if !changed {
			continue
		}
		args := []string{"commit-tree", f[6]}
		for _, p := range parents {
			args = append(args, "-p", p)
		}
		args = append(args, "-F", "-")
		cmd := g.gitHardened(ctx, args...)
		appendGitEnv(cmd,
			"GIT_AUTHOR_NAME="+f[0], "GIT_AUTHOR_EMAIL="+f[1], "GIT_AUTHOR_DATE="+f[2],
			"GIT_COMMITTER_NAME="+f[3], "GIT_COMMITTER_EMAIL="+f[4], "GIT_COMMITTER_DATE="+f[5])
		cmd.Stdin = strings.NewReader(stripAnthropicCoAuthorLines(msg))
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to rewrite commit %s: %w", sha, err)
		}
		rewritten[sha] = strings.TrimSpace(string(out))
	}

	newHead, ok := rewritten[oldHead]
	if !ok {
		return nil
	}
	if out, err := g.gitHardened(ctx, "update-ref",
		"-m", "pilot: strip Anthropic co-author trailer", "HEAD", newHead, oldHead).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to move branch to rewritten commits: %w: %s", err, out)
	}
	return nil
}

// stripCoAuthorBeforePush strips the Claude co-author trailer from every commit
// the next push would publish (GH-5711, GH-5719), logging instead of failing.
func (g *GitOperations) stripCoAuthorBeforePush(ctx context.Context, branchName, taskID string) {
	shas, headOnly, err := g.stripAnthropicCoAuthorRange(ctx)
	if headOnly {
		slog.Warn("Co-author strip limited to HEAD",
			slog.String("task_id", taskID), slog.String("branch", branchName),
			slog.String("reason", "no origin refs; co-author strip limited to HEAD"))
	}
	if err != nil {
		slog.Warn("Failed to strip co-author trailer before push",
			slog.String("task_id", taskID), slog.String("branch", branchName), slog.Any("error", err))
		return
	}
	if len(shas) == 0 {
		return
	}
	short := make([]string, len(shas))
	for i, s := range shas {
		short[i] = s[:min(7, len(s))]
	}
	slog.Warn("Stripped Anthropic Co-Authored-By trailer from commits before push",
		slog.String("task_id", taskID), slog.String("branch", branchName),
		slog.Any("rewritten_shas", short))
}

// branchRefspec is the full refspec of a branch push, refs/heads on both sides:
// git sends the commit under refs/heads/<branch>, the one the secret gate scans
// (it qualifies the name the same way), whatever else the session wrote under
// the same short name (GH-6334).
func branchRefspec(branch string) string {
	branch = strings.TrimPrefix(branch, "refs/heads/")
	return "refs/heads/" + branch + ":refs/heads/" + branch
}

// pushArgs is the git push command line of the three push methods. Decision:
// push.followTags is set to false on the command line and --no-follow-tags
// repeats it, so a session that sets the config cannot make the push publish an
// annotated tag the gate does not scan (GH-6334). The same two-fold form turns
// submodule recursion off (push.recurseSubmodules=on-demand would send a branch
// of a submodule the gate does not scan), and hooks are off through an empty
// core.hooksPath that cannot exist (secretpatterns.NoHooksPath), so a pre-push
// hook the session wrote does not run with the push's credentials (GH-6343,
// GH-6353).
func pushArgs(options ...string) []string {
	args := append(secretpatterns.HooksOffArgs(), "-c", "push.followTags=false", "-c", "push.recurseSubmodules=no",
		"push", "--no-follow-tags", "--no-recurse-submodules")
	return append(args, options...)
}

// PushArgs is pushArgs for the push sites outside this package: the git argument
// list of a push that the secret gate cleared, followed by options.
func PushArgs(options ...string) []string { return pushArgs(options...) }

// Push pushes the current branch to remote. Before pushing it strips the
// Claude co-author trailer from every commit the push would publish (GH-5711,
// GH-5719): belt and braces for backends that ignore includeCoAuthoredBy.
// Covers every push call site. taskID is only used to name the task in logs.
func (g *GitOperations) Push(ctx context.Context, branchName, taskID string) error {
	if g.daemonRepo != "" {
		g.beginHandOverAttempt(branchName)
	}
	g.stripCoAuthorBeforePush(ctx, branchName, taskID)
	if g.daemonRepo != "" {
		return g.pushHandOver(ctx, branchName, "", taskID)
	}
	if err := CheckSecretGate(ctx, g.projectPath, branchName, taskID); err != nil {
		return err
	}
	cmd := g.gitCmd(ctx, pushArgs("-u", "origin", branchRefspec(branchName))...)
	withGitCredentials(ctx, cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to push: %w: %s", err, output)
	}
	return nil
}

// PushForceWithLease pushes branchName with a lease pinned to expectedRemoteSHA
// (GH-5729): the push replaces the remote branch only while it still points at
// that SHA, so a rebased autopilot-fix continuation can publish its history
// without clobbering anything that landed on the branch since the footer SHA
// was recorded. A plain --force is never used.
func (g *GitOperations) PushForceWithLease(ctx context.Context, branchName, expectedRemoteSHA, taskID string) error {
	if g.daemonRepo != "" {
		g.beginHandOverAttempt(branchName)
	}
	g.stripCoAuthorBeforePush(ctx, branchName, taskID)
	if g.daemonRepo != "" {
		return g.pushHandOver(ctx, branchName, expectedRemoteSHA, taskID)
	}
	if err := CheckSecretGate(ctx, g.projectPath, branchName, taskID); err != nil {
		return err
	}
	lease := fmt.Sprintf("--force-with-lease=refs/heads/%s:%s", branchName, expectedRemoteSHA)
	cmd := g.gitCmd(ctx, pushArgs("-u", lease, "origin", branchRefspec(branchName))...)
	withGitCredentials(ctx, cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to push with lease: %w: %s", err, output)
	}
	return nil
}

// HandOverError is the error of a failed hand-over of a push of an agent
// workspace (TASK-651): the branch was not taken over into the daemon's
// repository, so this attempt published nothing. A hand-over error is never
// forgiven (pushNeverForgiven), whatever the remote holds: another attempt
// (a concurrent Push of the same GitOperations) may have put its own head
// there. It wraps the cause; IsPushNothingNew sees through it.
type HandOverError struct{ Err error }

func (e *HandOverError) Error() string { return e.Err.Error() }
func (e *HandOverError) Unwrap() error { return e.Err }

// IsHandOverError reports whether err is the error of a failed hand-over.
func IsHandOverError(err error) bool {
	var h *HandOverError
	return errors.As(err, &h)
}

// IsPushNothingNew reports whether err is the result of a hand-over push of a
// branch with no commit above the base (TASK-651): nothing to push, not a
// failure. The push retry loop does not retry it, and the run takes the path
// of a run with no new commit.
func IsPushNothingNew(err error) bool {
	return errors.Is(err, errHandOverNothingNew)
}

// handOverBeforeGate runs between the hand-over and the secret gate of a
// hand-over push. A test seam: a test moves the hand-over ref there.
var handOverBeforeGate = func(daemonRepo, taskID string) {}

// pushHandOver is Push (lease "") and PushForceWithLease for an agent
// workspace (TASK-651). The branch is taken out of the workspace into
// refs/pilot/handover/TASK of the daemon's repository (handOverBranch); the
// secret gate scans the hand-over SHA there, with the project's settings, and
// the push runs there as the daemon user: the refspec
// HANDOVER_SHA:refs/heads/BRANCH, the arguments of pushArgs, the explicit lease
// and the credentials of today, without -u (no local branch to track). No git
// command of the daemon runs with the workspace as its directory, so its
// config, hooks and remotes have no effect; the push and the co-author read
// get the environment of daemonGitEnv, the gate the one of daemonGateEnv. A
// failed hand-over (a branch with no commit above the base satisfies
// IsPushNothingNew) or a gate stop returns before the push.
//
// The caller started the attempt (beginHandOverAttempt); attemptSHA is set
// after the hand-over, the published SHA only after the push succeeded
// (recordPublishedHandOver). A hand-over error is a HandOverError.
func (g *GitOperations) pushHandOver(ctx context.Context, branchName, expectedRemoteSHA, taskID string) error {
	branch := strings.TrimPrefix(branchName, "refs/heads/")
	sha, err := handOverBranch(ctx, g.projectPath, g.daemonRepo, taskID, branch, g.handOverBase)
	if err != nil {
		return &HandOverError{Err: err}
	}
	g.handOverMu.Lock()
	g.attemptSHA = sha
	if g.workspace != nil {
		g.workspace.setHandOver(sha, false)
	}
	g.handOverMu.Unlock()
	handOverBeforeGate(g.daemonRepo, taskID)
	gateCtx := secretpatterns.WithGitEnv(ctx, daemonGateEnv(os.Environ()))
	if err := checkSecretGateTip(gateCtx, g.daemonRepo, sha, branchName, taskID); err != nil {
		return err
	}
	var opts []string
	what := "failed to push"
	if expectedRemoteSHA != "" {
		opts = append(opts, fmt.Sprintf("--force-with-lease=refs/heads/%s:%s", branch, expectedRemoteSHA))
		what = "failed to push with lease"
	}
	opts = append(opts, "origin", sha+":refs/heads/"+branch)
	cmd := gitCommandIn(ctx, g.daemonRepo, pushArgs(opts...)...)
	withGitCredentials(ctx, cmd)
	withDaemonGitEnv(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", what, err, output)
	}
	g.recordPublishedHandOver(sha, branch)
	g.warnPushedCoAuthor(ctx, branch, sha, taskID)
	return nil
}

// warnPushedCoAuthor reads the commits a hand-over push published (base..sha)
// in the daemon's repository and logs one Warn line per commit whose message
// still holds the co-author trailer of the model. The strip ran before, as the
// agent user; this is the check of the daemon on what left the machine.
func (g *GitOperations) warnPushedCoAuthor(ctx context.Context, branch, sha, taskID string) {
	rng := sha
	if g.handOverBase != "" {
		rng = g.handOverBase + ".." + sha
	}
	cmd := gitCommandIn(ctx, g.daemonRepo, handOverGitArgs("log", "--no-show-signature", "--format=%H%x00%B%x00", rng)...)
	appendGitEnv(cmd, "GIT_NO_REPLACE_OBJECTS=1")
	withDaemonGitEnv(cmd)
	out, err := cmd.Output()
	if err != nil {
		slog.Warn("Could not read the pushed commits for the co-author check",
			slog.String("task_id", taskID), slog.String("branch", branch), slog.Any("error", err))
		return
	}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		if anthropicCoAuthorRe.MatchString(fields[i+1]) {
			slog.Warn("Pushed commit holds the Anthropic Co-Authored-By trailer",
				slog.String("task_id", taskID), slog.String("branch", branch),
				slog.String("sha", strings.TrimSpace(fields[i])))
		}
	}
}

// CreatePR creates a pull request using gh CLI.
// GH-2325: title is validated against the conventional commit format before
// the remote call so malformed titles cannot reach main (and public release
// notes). The expected shape is "<issue-id>: <type>(<scope>)?: <subject>",
// which matches what the squash-merge path strips back to a conventional
// commit message.
func (g *GitOperations) CreatePR(ctx context.Context, title, body, baseBranch string) (string, error) {
	// GH-6263: a registered value or secret shape never reaches a public PR.
	title, body = secretpatterns.Redact(title), secretpatterns.Redact(body)
	if err := validatePRTitle(title); err != nil {
		return "", err
	}

	dir, repoArgs, err := g.ghDirForDaemonRepo(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to create PR: %w", err)
	}

	// GH-2177: Detect current branch to pass --head explicitly.
	// In worktree mode, gh may see uncommitted changes and refuse to infer the head branch.
	// Using --head bypasses the dirty working tree check.
	// TASK-651: an agent workspace gives the task branch the daemon recorded;
	// the HEAD of the workspace is the agent's and is not read.
	headBranch := ""
	if g.daemonRepo != "" {
		if headBranch = g.taskBranch(); headBranch == "" {
			return "", fmt.Errorf("failed to create PR: no task branch on record for the agent workspace %s", g.projectPath)
		}
	} else if branchCmd := g.gitCmd(ctx, "rev-parse", "--abbrev-ref", "HEAD"); branchCmd != nil {
		if out, err := branchCmd.Output(); err == nil {
			headBranch = strings.TrimSpace(string(out))
		}
	}

	args := []string{"pr", "create",
		"--title", title,
		"--body", body,
		"--base", baseBranch,
	}
	if headBranch != "" {
		args = append(args, "--head", headBranch)
	}
	args = append(args, repoArgs...)

	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", args...))
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	if err != nil {
		// Check if PR already exists - gh returns exit 1 but includes URL in output
		if strings.Contains(outputStr, "already exists") {
			if url := extractPRURL(outputStr); url != "" {
				return url, nil
			}
		}
		return "", fmt.Errorf("failed to create PR: %w: %s", err, output)
	}

	return parseCreatePROutput(outputStr), nil
}

// parseCreatePROutput extracts the PR URL from `gh pr create`'s combined
// stdout+stderr on the success path. GH-5459: `gh` can print warnings (e.g.
// "Warning: 1 uncommitted change") to stderr ahead of the URL on stdout, and
// CombinedOutput() interleaves both streams — returning the raw output
// verbatim leaks those warnings into pr_url wherever it's stored or
// displayed. Prefer the extracted URL; fall back to the trimmed raw output
// (with a warning log) only when no URL pattern is found.
func parseCreatePROutput(outputStr string) string {
	if url := extractPRURL(outputStr); url != "" {
		return url
	}
	trimmed := strings.TrimSpace(outputStr)
	slog.Warn("CreatePR: no PR URL found in gh output, returning raw output",
		slog.String("output", trimmed))
	return trimmed
}

// extractPRURL extracts a GitHub PR URL from text
func extractPRURL(text string) string {
	// Look for GitHub PR URL pattern: https://github.com/owner/repo/pull/123
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "github.com") && strings.Contains(line, "/pull/") {
			// Extract just the URL if there's other text
			if idx := strings.Index(line, "https://"); idx >= 0 {
				url := line[idx:]
				// Trim any trailing text after the URL
				if spaceIdx := strings.IndexAny(url, " \t\n"); spaceIdx > 0 {
					url = url[:spaceIdx]
				}
				return url
			}
		}
	}
	return ""
}

// FindMergedPRByBranch returns the URL of a merged PR whose head is branch, or
// "" if none exists. TASK-359 Layer 1 (Shape C): lets the executor skip opening
// a duplicate PR for work that is already merged. It uses the gh CLI — the same
// dependency CreatePR already relies on — so the executor needs no github.Client
// wiring (the merge-detection API lives only on *github.Client).
func (g *GitOperations) FindMergedPRByBranch(ctx context.Context, branch string) (string, error) {
	dir, repoArgs, err := g.ghDirForDaemonRepo(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list merged PRs for %s: %w", branch, err)
	}
	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", append([]string{"pr", "list",
		"--head", branch,
		"--state", "merged",
		"--json", "url",
		"--limit", "1",
	}, repoArgs...)...))
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to list merged PRs for %s: %w: %s", branch, err, output)
	}
	return parseFirstPRURL(output), nil
}

// FindOpenPRByBranch returns the URL of an open PR whose head is branch, or ""
// if none exists. GH-4022: lets the direct (non-epic) executor path adopt an
// already-open PR from a prior/retried dispatch of the same branch instead of
// racing gh CLI into a duplicate PR.
func (g *GitOperations) FindOpenPRByBranch(ctx context.Context, branch string) (string, error) {
	dir, repoArgs, err := g.ghDirForDaemonRepo(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list open PRs for %s: %w", branch, err)
	}
	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", append([]string{"pr", "list",
		"--head", branch,
		"--state", "open",
		"--json", "url",
		"--limit", "1",
	}, repoArgs...)...))
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to list open PRs for %s: %w: %s", branch, err, output)
	}
	return parseFirstPRURL(output), nil
}

// GetPRStateByURL returns GitHub's own state for the PR at prURL: "OPEN",
// "MERGED" or "CLOSED" (GH-5991). Never derived from mergedAt, which can read
// null on an already-merged squash-merge PR.
func (g *GitOperations) GetPRStateByURL(ctx context.Context, prURL string) (string, error) {
	dir, repoArgs, err := g.ghDirForDaemonRepo(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to read PR state for %s: %w", prURL, err)
	}
	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", append([]string{"pr", "view", prURL,
		"--json", "state",
		"--jq", ".state",
	}, repoArgs...)...))
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to read PR state for %s: %w: %s", prURL, err, output)
	}
	return strings.ToUpper(strings.TrimSpace(string(output))), nil
}

// parseFirstPRURL extracts the first "url" field from `gh pr list --json url`
// output (a JSON array like [{"url":"https://github.com/o/r/pull/1"}]). Returns
// "" for an empty array or unparseable input.
func parseFirstPRURL(jsonOutput []byte) string {
	var prs []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(jsonOutput, &prs); err != nil {
		return ""
	}
	if len(prs) == 0 {
		return ""
	}
	return prs[0].URL
}

// prURLNumberRe extracts the numeric PR id from a GitHub PR URL
// (".../pull/123" or ".../pulls/123"). Mirrors adapters/github.ExtractPRNumber
// byte-for-byte; duplicated locally because internal/executor cannot import
// internal/adapters/github without an import cycle (see issue_state.go's
// doc comment for the same constraint on this package).
var prURLNumberRe = regexp.MustCompile(`/pulls?/(\d+)`)

// extractPRNumberFromURL parses the PR number out of a GitHub pull-request
// URL, returning ok=false if none could be found.
func extractPRNumberFromURL(prURL string) (num int, ok bool) {
	m := prURLNumberRe.FindStringSubmatch(prURL)
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// isBorrowedOriginPR reports whether prURL points at task.FromPR — the
// origin PR whose branch a fix-issue task is continuing (resolveAutopilotFixBranch
// in cmd/pilot/handlers.go reuses the origin PR's branch name and records its
// number as FromPR for --from-pr session resumption, GH-1267/GH-5379).
//
// GH-5400: FindMergedPRByBranch/FindOpenPRByBranch key purely on branch name.
// For a fresh dispatch of a fix issue that has not yet pushed a single commit
// of its own, that reused branch's merged/open PR is still task.FromPR itself
// — evidence the ORIGIN issue's work shipped, not this fix issue's. Treating
// it as "my own work is already done" (the pre-GH-5400 behavior of every
// FindMergedPRByBranch/FindOpenPRByBranch call site in this file) let a fix
// issue with no PR of its own short-circuit to "Completed" on its very first
// dispatch tick and get closed citing the origin PR — the incident behind
// fix issue #5385 closing "done" against PR #5384's merge. A merged/open PR
// found on the branch that is NOT task.FromPR is still trusted as evidence of
// this task's own (possibly retried) work, unchanged from prior behavior.
func isBorrowedOriginPR(task *Task, prURL string) bool {
	if task == nil || task.FromPR <= 0 {
		return false
	}
	num, ok := extractPRNumberFromURL(prURL)
	return ok && num == task.FromPR
}

// GetCurrentBranch returns the current branch name
func (g *GitOperations) GetCurrentBranch(ctx context.Context) (string, error) {
	cmd := g.gitCmd(ctx, "branch", "--show-current")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// GetDefaultBranch returns the default branch (main or master)
func (g *GitOperations) GetDefaultBranch(ctx context.Context) (string, error) {
	cmd := g.gitCmd(ctx, "symbolic-ref", "refs/remotes/origin/HEAD")
	output, err := cmd.Output()
	if err != nil {
		// Fallback to checking for main or master
		if g.branchExists(ctx, "main") {
			return "main", nil
		}
		if g.branchExists(ctx, "master") {
			return "master", nil
		}
		return "", fmt.Errorf("could not determine default branch: %w", err)
	}

	ref := strings.TrimSpace(string(output))
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1], nil
}

// branchExists checks if a branch exists
func (g *GitOperations) branchExists(ctx context.Context, branch string) bool {
	cmd := g.gitCmd(ctx, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return cmd.Run() == nil
}

// GetChangedFiles returns list of changed files
func (g *GitOperations) GetChangedFiles(ctx context.Context) ([]string, error) {
	cmd := g.gitCmd(ctx, "diff", "--name-only", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get changed files: %w", err)
	}

	files := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(files) == 1 && files[0] == "" {
		return []string{}, nil
	}
	return files, nil
}

// HasUncommittedChanges checks if there are uncommitted changes
func (g *GitOperations) HasUncommittedChanges(ctx context.Context) (bool, error) {
	cmd := g.gitCmd(ctx, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to check status: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// PushToMain pushes changes directly to the main/default branch. taskID names
// the task for the secret gate's alert, which alerts once per (task, commit).
func (g *GitOperations) PushToMain(ctx context.Context, taskID string) error {
	defaultBranch, err := g.GetDefaultBranch(ctx)
	if err != nil {
		defaultBranch = "main"
	}
	if err := CheckSecretGate(ctx, g.projectPath, defaultBranch, taskID); err != nil {
		return err
	}
	cmd := g.gitCmd(ctx, pushArgs("origin", branchRefspec(defaultBranch))...)
	withGitCredentials(ctx, cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to push to %s: %w: %s", defaultBranch, err, output)
	}
	return nil
}

// CountNewCommits returns the number of commits on the current branch
// that are not on the base branch. Uses `git rev-list --count base..HEAD`.
// Returns 0 if the base branch doesn't exist or there are no new commits.
func (g *GitOperations) CountNewCommits(ctx context.Context, baseBranch string) (int, error) {
	return g.countNewCommits(ctx, baseBranch, "HEAD")
}

// countNewCommits is CountNewCommits for the commits of head.
func (g *GitOperations) countNewCommits(ctx context.Context, baseBranch, head string) (int, error) {
	return parseNewCommitCount(g.gitCmd(ctx, "rev-list", "--count", baseBranch+".."+head))
}

// daemonCountNewCommits is countNewCommits in the daemon's repository of an
// agent workspace: hooks off and the environment of daemonGitEnv.
func (g *GitOperations) daemonCountNewCommits(ctx context.Context, baseBranch, head string) (int, error) {
	cmd := gitCommandIn(ctx, g.daemonRepo, append(secretpatterns.HooksOffArgs(), "rev-list", "--count", baseBranch+".."+head)...)
	return parseNewCommitCount(withDaemonGitEnv(cmd))
}

// parseNewCommitCount runs a `git rev-list --count` command and parses its
// output.
func parseNewCommitCount(cmd *exec.Cmd) (int, error) {
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to count new commits: %w", err)
	}
	countStr := strings.TrimSpace(string(output))
	var count int
	if _, parseErr := fmt.Sscanf(countStr, "%d", &count); parseErr != nil {
		return 0, fmt.Errorf("failed to parse commit count %q: %w", countStr, parseErr)
	}
	return count, nil
}

// CountNewCommitsAgainstOrigin is CountNewCommits compared against the
// origin-tracked base branch instead of the local base branch ref. GH-4566:
// every worktree (parent epic's and children's alike) is cut from a freshly
// fetched origin/<base> (worktree.go), but nothing fast-forwards the shared
// clone's local <base> ref during a sequential epic run — so on a stale local
// <base>, CountNewCommits(ctx, "main") counts commits that landed on origin
// via other sessions, not commits this branch actually added, and the
// no-commits guard fires open on an empty umbrella branch.
//
// Fetches origin/<baseBranch> first so the comparison reflects the ref the
// branch was actually cut from. The fetch is best-effort: on failure this
// still compares against whatever origin/<baseBranch> already resolves to
// locally, which is strictly fresher than the local <baseBranch> branch in
// this codepath (worktree creation already fetched it before cutting the
// branch). If origin/<baseBranch> doesn't resolve at all (no "origin" remote
// configured — bare local repos, some unit tests), falls back to comparing
// against the local <baseBranch> ref, preserving CountNewCommits' original
// behavior in that environment.
//
// TASK-651: for an agent workspace the fetch runs in the daemon's repository,
// and once g made a push attempt the count is of the head published for the
// branch of that attempt (countHandOverSHA) there, and 0 when nothing is
// published for it: a refused or failed attempt counts no commit that never
// left the machine, and the head of another branch is not counted. Before
// the first attempt it counts in the workspace, whose origin/<baseBranch> is
// the base commit. Decision: the guards before the push (the no-commits guard
// of the PR path, the epic guard, the timeout salvage) count before any
// attempt; a 0 there would end every run before its push. That count only
// lets the push run, and the hand-over decides what it publishes
// (IsPushNothingNew). The fetch runs with hooks off (remoteGitCmd); the fetch
// and the count in the daemon's repository get the environment of
// daemonGitEnv.
func (g *GitOperations) CountNewCommitsAgainstOrigin(ctx context.Context, baseBranch string) (int, error) {
	fetchCmd := g.remoteGitCmd(ctx, "fetch", "origin", baseBranch)
	_ = fetchCmd.Run() // best-effort; fall back to the existing tracking ref on failure

	if g.daemonRepo != "" {
		if sha, attempted := g.countHandOverSHA(); attempted {
			if sha == "" {
				return 0, nil
			}
			if count, err := g.daemonCountNewCommits(ctx, "origin/"+baseBranch, sha); err == nil {
				return count, nil
			}
			return g.daemonCountNewCommits(ctx, baseBranch, sha)
		}
	}

	if count, err := g.CountNewCommits(ctx, "origin/"+baseBranch); err == nil {
		return count, nil
	}
	return g.CountNewCommits(ctx, baseBranch)
}

// GetCurrentCommitSHA returns the SHA of the current HEAD commit
func (g *GitOperations) GetCurrentCommitSHA(ctx context.Context) (string, error) {
	cmd := g.gitCmd(ctx, "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current commit SHA: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// ResetHardToCommit discards every change made since sha — committed or
// uncommitted, tracked or untracked — restoring the working tree to exactly
// that commit's state.
//
// GH-4594: direct-mode (non-worktree) quality-gate retries re-invoked Claude
// Code in place, on top of whatever the previous (rejected) attempt left
// behind. Since that attempt's edits were never undone, the retry's new
// edits landed on top of the old ones instead of a clean slate — the
// "leftover ` M version.go` observed x3" symptom from the incident this
// fixes: three retries, three stacked partial edits to the same file, none
// of them ever fully applied or cleanly discarded. Worktree mode doesn't
// need this: each execution already gets an isolated copy created fresh
// from a commit.
//
// Untracked paths matching defaultExcludeDirs/defaultExcludeGlobs (Navigator
// scaffold, lock files, build artifacts) are preserved via `git clean`'s -e
// excludes — the same allowlist Commit() and checkGitClean() already use to
// decide what isn't really a project change.
func (g *GitOperations) ResetHardToCommit(ctx context.Context, sha string) error {
	resetCmd := g.gitCmd(ctx, "reset", "--hard", sha)
	if output, err := resetCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to reset to %s: %w: %s", sha, err, output)
	}

	cleanArgs := []string{"clean", "-fd"}
	for _, dir := range defaultExcludeDirs {
		cleanArgs = append(cleanArgs, "-e", dir)
	}
	for _, glob := range defaultExcludeGlobs {
		cleanArgs = append(cleanArgs, "-e", glob)
	}
	cleanCmd := g.gitCmd(ctx, cleanArgs...)
	if output, err := cleanCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to clean untracked files after reset to %s: %w: %s", sha, err, output)
	}
	return nil
}

// GetDiff returns the diff between the base branch and HEAD.
// Uses three-dot notation (base...HEAD) to show changes on the current branch.
func (g *GitOperations) GetDiff(ctx context.Context, baseBranch string) (string, error) {
	cmd := g.gitCmd(ctx, "diff", baseBranch+"...HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %w", err)
	}
	return string(output), nil
}

// GetDiffAgainstOrigin is GetDiff compared against a freshly fetched
// origin/<baseBranch> instead of the possibly-stale local <baseBranch> ref.
// GH-4988: the intent judge calls GetDiff with a local branch name (task's
// BaseBranch, or GetDefaultBranch's local resolution), which — exactly like
// the GH-4566 CountNewCommits class this mirrors — can be behind
// origin/<baseBranch> in a long-lived worktree or shared clone. Other
// issues merging into origin/main mid-task then show up inside the judge's
// diff as if this branch produced them, and the judge correctly (but
// wrongly, from the operator's perspective) vetoes them as scope creep.
//
// Fetches origin/<baseBranch> (best-effort — falls back to whatever
// tracking ref already resolves locally on failure, same as
// CountNewCommitsAgainstOrigin), resolves the merge-base of that ref and
// HEAD, and diffs from there. Diffing from the merge-base — rather than
// origin/<baseBranch>'s tip — guarantees the result never contains commits
// reachable from current origin/<baseBranch> even when HEAD is also behind
// tip for unrelated reasons.
//
// Returns the diff and the base SHA it was computed against, so callers can
// log which commit the judge actually evaluated from.
func (g *GitOperations) GetDiffAgainstOrigin(ctx context.Context, baseBranch string) (diff string, baseSHA string, err error) {
	fetchCmd := g.gitCmd(ctx, "fetch", "origin", baseBranch)
	withGitCredentials(ctx, fetchCmd)
	_ = fetchCmd.Run() // best-effort; fall back to whatever origin/<baseBranch> already resolves to locally

	baseSHA, err = g.resolveMergeBaseSHA(ctx, baseBranch)
	if err != nil {
		return "", "", err
	}

	cmd := g.gitCmd(ctx, "diff", baseSHA+"...HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", baseSHA, fmt.Errorf("git diff failed: %w", err)
	}
	return string(output), baseSHA, nil
}

// resolveMergeBaseSHA resolves the merge-base commit SHA between HEAD and
// origin/<baseBranch>, falling back to the local <baseBranch> ref when
// origin/<baseBranch> doesn't resolve at all (no "origin" remote — bare
// local repos, some unit tests). Mirrors the fallback order
// CountNewCommitsAgainstOrigin uses.
func (g *GitOperations) resolveMergeBaseSHA(ctx context.Context, baseBranch string) (string, error) {
	for _, ref := range []string{"origin/" + baseBranch, baseBranch} {
		cmd := g.gitCmd(ctx, "merge-base", ref, "HEAD")
		output, err := cmd.Output()
		if err == nil {
			return strings.TrimSpace(string(output)), nil
		}
	}
	return "", fmt.Errorf("could not resolve merge-base for %q against origin/%s or local %s", baseBranch, baseBranch, baseBranch)
}

// Pull fetches and merges changes from remote for the specified branch
func (g *GitOperations) Pull(ctx context.Context, branch string) error {
	cmd := g.gitCmd(ctx, "pull", "origin", branch)
	withGitCredentials(ctx, cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to pull %s: %w: %s", branch, err, output)
	}
	return nil
}

// SwitchToBranchAndPull switches to the given branch and pulls latest changes.
// Used to honor project.default_branch / branch_from overrides (GH-2290).
// Pull failures are non-fatal so offline / no-upstream scenarios still work.
func (g *GitOperations) SwitchToBranchAndPull(ctx context.Context, branch string) (string, error) {
	if branch == "" {
		return g.SwitchToDefaultBranchAndPull(ctx)
	}
	if err := g.SwitchBranch(ctx, branch); err != nil {
		return branch, fmt.Errorf("failed to switch to %s: %w", branch, err)
	}
	if err := g.Pull(ctx, branch); err != nil {
		return branch, nil
	}
	return branch, nil
}

// SwitchToDefaultBranchAndPull switches to the default branch and pulls latest changes.
// This ensures new branches are created from the latest default branch, not from
// whatever branch was previously checked out (fixes GH-279).
func (g *GitOperations) SwitchToDefaultBranchAndPull(ctx context.Context) (string, error) {
	// Get default branch name
	defaultBranch, err := g.GetDefaultBranch(ctx)
	if err != nil {
		defaultBranch = "main" // fallback
	}

	// Switch to default branch
	if err := g.SwitchBranch(ctx, defaultBranch); err != nil {
		return defaultBranch, fmt.Errorf("failed to switch to %s: %w", defaultBranch, err)
	}

	// Pull latest changes
	if err := g.Pull(ctx, defaultBranch); err != nil {
		// Pull failure is non-fatal - we can still create branch from local state
		// This handles offline scenarios or repos without upstream configured
		return defaultBranch, nil
	}

	return defaultBranch, nil
}

// CommitsBehindMain returns how many commits the given branch is behind origin/main.
// Returns 0 if the branch is up-to-date or ahead.
// GH-912: Used to detect stale branches that need to be recreated.
func (g *GitOperations) CommitsBehindMain(ctx context.Context, branchName string) (int, error) {
	// First fetch to ensure we have latest remote state
	fetchCmd := g.gitCmd(ctx, "fetch", "origin")
	withGitCredentials(ctx, fetchCmd)
	_ = fetchCmd.Run() // Ignore fetch errors - might be offline

	// Get default branch
	defaultBranch, err := g.GetDefaultBranch(ctx)
	if err != nil {
		defaultBranch = "main"
	}

	// Count commits that are in origin/main but not in the branch
	// git rev-list --count <branch>..origin/main
	cmd := g.gitCmd(ctx, "rev-list", "--count", branchName+"..origin/"+defaultBranch)
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to count commits behind: %w", err)
	}

	countStr := strings.TrimSpace(string(output))
	var count int
	if _, parseErr := fmt.Sscanf(countStr, "%d", &count); parseErr != nil {
		return 0, fmt.Errorf("failed to parse count %q: %w", countStr, parseErr)
	}

	return count, nil
}

// DeleteBranch deletes a local branch.
// GH-912: Used to remove stale branches before recreating them fresh from main.
func (g *GitOperations) DeleteBranch(ctx context.Context, branchName string) error {
	cmd := g.gitCmd(ctx, "branch", "-D", branchName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete branch: %w: %s", err, output)
	}
	return nil
}

// CreateRecoveryRef pins fromRef (typically "HEAD", or "refs/heads/<branch>"
// when called from outside the worktree that made the commits) under a
// dedicated ref namespace (refs/pilot-recovery/<taskID>) so committed work
// survives even if push/PR creation never succeeds and the worktree is
// later removed. Unlike a worktree's own branch, this ref lives in the
// shared repository (not the per-worktree admin area), so `git worktree
// remove` cannot take it with it — the commit stays reachable via `git
// fetch <repo> <returned-ref>` or `git show <sha>` from the main repo.
// GH-3785: prevents child-worker commits from being stranded in the object
// store with no reachable ref once a push/PR failure leads to worktree
// cleanup.
//
// TASK-651: for an agent workspace the ref is created in the daemon's
// repository from the hand-over SHA (createHandOverRecoveryRef); the workspace
// is removed after the run and would take the ref with it.
func (g *GitOperations) CreateRecoveryRef(ctx context.Context, taskID, fromRef string) (string, error) {
	if fromRef == "" {
		fromRef = "HEAD"
	}
	if g.daemonRepo != "" {
		return g.createHandOverRecoveryRef(ctx, taskID, fromRef)
	}
	refName := recoveryRefName(taskID)
	cmd := g.gitCmd(ctx, "update-ref", refName, fromRef)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to create recovery ref %s: %w: %s", refName, err, output)
	}
	return refName, nil
}

// recoveryRefName is the recovery ref of a task: refs/pilot-recovery/TASK.
func recoveryRefName(taskID string) string {
	return "refs/pilot-recovery/" + sanitizeBranchName(taskID)
}

// createHandOverRecoveryRef is CreateRecoveryRef for an agent workspace: the
// ref is created in the daemon's repository, as the daemon user with hooks
// off, at the hand-over SHA of this run. When no hand-over ref of this run
// exists, the branch is handed over first (handOverBranch): fromRef
// refs/heads/BRANCH names it, any other fromRef means the task branch on
// record. A failure is logged as one Error line that names the task and the
// workspace path: the commits are then in the workspace only.
func (g *GitOperations) createHandOverRecoveryRef(ctx context.Context, taskID, fromRef string) (string, error) {
	refName := recoveryRefName(taskID)
	sha, err := g.recoveryHandOverSHA(ctx, taskID, fromRef)
	if err == nil {
		cmd := handOverDaemonGit(ctx, g.daemonRepo, handOverGitArgs("update-ref", refName, sha)...)
		if out, uerr := cmd.CombinedOutput(); uerr != nil {
			err = fmt.Errorf("failed to create recovery ref %s: %w: %s", refName, uerr, strings.TrimSpace(string(out)))
		}
	}
	if err != nil {
		slog.Error("Recovery ref of the agent workspace failed: the commits are in the workspace only",
			slog.String("task_id", taskID),
			slog.String("workspace", g.projectPath),
			slog.Any("error", err),
		)
		return "", err
	}
	if g.workspace != nil {
		g.workspace.setRecovery(sha)
	}
	return refName, nil
}

// recoveryHandOverSHA is the head of the hand-over ref of this run in the
// daemon's repository; without one it hands the branch over first.
func (g *GitOperations) recoveryHandOverSHA(ctx context.Context, taskID, fromRef string) (string, error) {
	if sha := daemonRefSHA(ctx, g.daemonRepo, handOverRef(taskID)); sha != "" {
		// A ref left by an earlier run of the task is not this run's.
		if g.workspace == nil {
			return sha, nil
		}
		if last, _, _ := g.workspace.state(); last == sha {
			return sha, nil
		}
	}
	branch := g.taskBranch()
	if b, ok := strings.CutPrefix(fromRef, "refs/heads/"); ok {
		branch = b
	}
	sha, err := handOverBranch(ctx, g.projectPath, g.daemonRepo, taskID, branch, g.handOverBase)
	if err != nil {
		return "", err
	}
	if g.workspace != nil {
		g.workspace.setHandOver(sha, false)
	}
	return sha, nil
}

// GitDiff holds numstat output from `git diff --numstat origin/main...HEAD`.
type GitDiff struct {
	// Files is the list of changed file paths.
	Files []string
	// Added is the total number of inserted lines across all changed files.
	Added int
	// Removed is the total number of deleted lines across all changed files.
	Removed int
}

// GetDiffStats returns line-level diff statistics between origin/baseBranch and HEAD.
// Uses three-dot notation so only commits on the current branch are counted.
func (g *GitOperations) GetDiffStats(ctx context.Context, baseBranch string) (GitDiff, error) {
	cmd := g.gitCmd(ctx, "diff", "--numstat", "origin/"+baseBranch+"...HEAD")
	output, err := cmd.Output()
	if err != nil {
		return GitDiff{}, fmt.Errorf("git diff --numstat failed: %w", err)
	}

	var diff GitDiff
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		var added, removed int
		var path string
		if _, scanErr := fmt.Sscanf(line, "%d %d %s", &added, &removed, &path); scanErr != nil {
			continue
		}
		diff.Files = append(diff.Files, path)
		diff.Added += added
		diff.Removed += removed
	}
	return diff, nil
}

// ChangedFilesAgainstOrigin returns the set of file paths changed on the
// current branch relative to a freshly fetched origin/<baseBranch> (GH-5466
// diff-coverage check), plus the merge-base SHA the comparison was made
// against (so callers can also run FileExistsAtRef against the exact same
// base). Mirrors GetDiffAgainstOrigin's freshness handling — fetch best-
// effort, then merge-base, then diff from there — so a stale local
// <baseBranch> ref can't make the diff-coverage check see a file that
// already landed on origin as still "not modified". Uses --name-only
// (rather than reusing GetDiffStats' --numstat parsing) because numstat
// reports "-\t-\t<path>" for binary files, which GetDiffStats' Sscanf-based
// parser silently drops from Files — this check must see every changed
// path, binary or not.
func (g *GitOperations) ChangedFilesAgainstOrigin(ctx context.Context, baseBranch string) (files []string, baseSHA string, err error) {
	fetchCmd := g.gitCmd(ctx, "fetch", "origin", baseBranch)
	withGitCredentials(ctx, fetchCmd)
	_ = fetchCmd.Run() // best-effort; fall back to whatever origin/<baseBranch> already resolves to locally

	baseSHA, err = g.resolveMergeBaseSHA(ctx, baseBranch)
	if err != nil {
		return nil, "", err
	}

	cmd := g.gitCmd(ctx, "diff", "--name-only", baseSHA+"...HEAD")
	output, err := cmd.Output()
	if err != nil {
		return nil, baseSHA, fmt.Errorf("git diff --name-only failed: %w", err)
	}

	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, baseSHA, nil
}

// FileExistsAtRef reports whether path exists in the tree at ref (typically
// a merge-base SHA resolved by ChangedFilesAgainstOrigin) — a local,
// network-free existence check, distinct from base_presence.go's
// FileExistsOnDefaultBranch (which probes GitHub's API and is used at
// dispatch time, before a worktree exists). Any git error (path absent,
// ref unresolvable) reports false rather than distinguishing the two —
// callers only use this to decide whether an absent path is base-presence's
// class of finding rather than this check's, so "false" is the correct
// answer for both cases.
func (g *GitOperations) FileExistsAtRef(ctx context.Context, ref, path string) bool {
	cmd := g.gitCmd(ctx, "cat-file", "-e", ref+":"+path)
	return cmd.Run() == nil
}

// RemoteBranchExists checks if a branch exists on the remote (origin).
// GH-1389: Used to verify if push actually succeeded despite worktree chdir errors.
// Any probe error reports false; callers that must tell "branch is gone" from
// "the remote could not be queried" use ProbeRemoteBranch.
func (g *GitOperations) RemoteBranchExists(ctx context.Context, branchName string) bool {
	exists, err := g.ProbeRemoteBranch(ctx, branchName)
	return err == nil && exists
}

// ProbeRemoteBranch reports whether branchName exists on origin and, unlike
// RemoteBranchExists, returns the git error when the remote cannot be queried
// (network, auth), so that failure is never mistaken for an absent branch
// (GH-6191).
func (g *GitOperations) ProbeRemoteBranch(ctx context.Context, branchName string) (bool, error) {
	cmd := g.remoteGitCmd(ctx, "ls-remote", "--heads", "origin", branchName)
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}
	// ls-remote returns non-empty output if branch exists
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// RemoteBranchHead returns the commit SHA origin's branch points at (GH-5729).
// ok is false when the branch is absent or the remote cannot be queried.
// Unlike RemoteBranchExists it distinguishes "pushed my commit" from "a branch
// by that name exists", which is what a rejected push must be checked against.
func (g *GitOperations) RemoteBranchHead(ctx context.Context, branchName string) (sha string, ok bool) {
	cmd := g.remoteGitCmd(ctx, "ls-remote", "--heads", "origin", "refs/heads/"+branchName)
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 || fields[1] != "refs/heads/"+branchName {
		return "", false
	}
	return fields[0], true
}

// LocalHead returns the SHA of the working tree's HEAD commit.
func (g *GitOperations) LocalHead(ctx context.Context) (sha string, err error) {
	return g.GetCurrentCommitSHA(ctx)
}

// ResolveFixContinuationBaseRef resolves the ref an autopilot-fix task's
// worktree must be cut from (GH-5348, pilot-console #275 incident).
//
// Fix issues reuse the original PR's branch name (branchName) so the fix
// session continues the original diff instead of silently rebuilding it from
// main. This only works if the branch's actual commits are used as the
// worktree's base:
//
//   - If origin/<branchName> still resolves (the PR branch wasn't deleted, or
//     a prior fix iteration already recreated it), that branch's tip is
//     returned — preserving any commits already on it.
//   - Otherwise, if fallbackSHA (the original PR's recorded head commit) is
//     set, it is fetched directly from origin — `git fetch origin <sha>`
//     works for any commit still reachable/retained on the remote, even with
//     no branch pointing at it — and returned so the caller recreates the
//     branch from that exact commit instead of from main.
//   - If neither resolves, "" is returned. The caller's response to that
//     depends on why: no fallbackSHA at all (pre-GH-5348 fix issues, or a
//     blanked HeadSHA) means there was never an exact commit to recover, so
//     the caller degrades to its own default base (origin/main). A
//     fallbackSHA that was supplied but could not be fetched (e.g.
//     garbage-collected on the remote) is different — silently defaulting to
//     main there would discard a real, identified commit, so the caller
//     (runner.go, GH-5351) escalates instead of falling back.
//
// Callers pass "" for fallbackSHA when there is no recorded SHA to fall back
// to (e.g. fix issues created before GH-5348); in that case this degrades to
// the existing origin/<branchName>-or-nothing behavior.
func (g *GitOperations) ResolveFixContinuationBaseRef(ctx context.Context, branchName, fallbackSHA string) string {
	if branchName != "" {
		fetchCmd := g.gitCmd(ctx, "fetch", "origin", branchName)
		withGitCredentials(ctx, fetchCmd)
		_ = fetchCmd.Run() // best-effort; fall through to the SHA (or "") if the branch is gone

		verifyCmd := g.gitCmd(ctx, "rev-parse", "--verify", "-q", "origin/"+branchName)
		if err := verifyCmd.Run(); err == nil {
			return "origin/" + branchName
		}
	}

	if fallbackSHA == "" {
		return ""
	}

	fetchShaCmd := g.gitCmd(ctx, "fetch", "origin", fallbackSHA)
	withGitCredentials(ctx, fetchShaCmd)
	if output, err := fetchShaCmd.CombinedOutput(); err != nil {
		// GH-5351: this no longer means "the caller falls back to main" — a
		// recorded SHA existed and identified a real commit, so the caller
		// (runner.go's escalateUnfetchableFixSHA) escalates to a human
		// instead of silently discarding it. Only report the fetch failure
		// here; the caller decides what "" means for its own fallbackSHA.
		slog.Warn("autopilot-fix: recorded SHA unfetchable",
			slog.String("branch", branchName),
			slog.String("sha", fallbackSHA),
			slog.Any("error", err),
			slog.String("output", string(output)),
		)
		return ""
	}

	verifyShaCmd := g.gitCmd(ctx, "rev-parse", "--verify", "-q", fallbackSHA)
	if err := verifyShaCmd.Run(); err != nil {
		return ""
	}
	return fallbackSHA
}
