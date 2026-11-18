package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TASK-651 (design: .agent/system/agent-isolation-design.md, 4.2): with
// claude_code.run_as_user set, a task run gets an agent workspace instead of a
// git worktree: a clone that the agent user creates and owns, a direct child
// of the workspace root, named pilot-ws-PROJECT-TASK-NANOSECONDS. It shares no
// .git directory, no alternates file and no object file with the daemon's
// repository, and the agent user never reads that repository: the daemon
// sends the base commit as a bundle on standard input. Every command that
// creates or changes a file in the workspace runs as the agent user (gitCmd,
// agentCommand). With the option empty nothing here runs.

const (
	// agentWorkspacePrefix starts the name of every workspace and of its
	// bundle file.
	agentWorkspacePrefix = "pilot-ws-"
	// agentWorkspaceBundleSuffix ends the name of the bundle file of a
	// workspace: <workspace name>.bundle, next to the workspace.
	agentWorkspaceBundleSuffix = ".bundle"
	// agentWorkspaceBaseRefPrefix holds the temporary ref in the daemon's
	// repository that names the base commit for git bundle create.
	agentWorkspaceBaseRefPrefix = "refs/pilot/ws-base/"
	// agentWorkspaceRemoveTimeout bounds the rm -rf of a workspace.
	agentWorkspaceRemoveTimeout = 60 * time.Second
	// agentWorkspaceProjectLen is the length of PROJECT in a workspace name.
	agentWorkspaceProjectLen = 8
)

// agentWorkspaceStoreScript stores standard input as the file $1, which must
// not exist, readable by the agent user only.
const agentWorkspaceStoreScript = `set -C; umask 077; exec cat > "$1"`

// agentWorkspaceAfterTransport, when set, runs between the transport and the
// checks of createAgentWorkspace. Tests only.
var agentWorkspaceAfterTransport func(wsPath string)

// workspaceProjectID is PROJECT of a workspace name: the first 8 hex
// characters of the SHA-256 of the canonical path of the daemon's repository.
func workspaceProjectID(repoPath string) (string, error) {
	canon, err := resolveGitRoutePath(repoPath)
	if err != nil {
		return "", fmt.Errorf("agent workspace: resolve repository %q: %w", repoPath, err)
	}
	sum := sha256.Sum256([]byte(canon))
	return hex.EncodeToString(sum[:])[:agentWorkspaceProjectLen], nil
}

// parseAgentWorkspaceName returns PROJECT and TASK of a workspace name or of
// the name of its bundle file. The project of a workspace is read from its
// name only, never from a file inside it.
func parseAgentWorkspaceName(name string) (project, task string, ok bool) {
	rest, ok := strings.CutPrefix(name, agentWorkspacePrefix)
	if !ok {
		return "", "", false
	}
	rest = strings.TrimSuffix(rest, agentWorkspaceBundleSuffix)
	if len(rest) < agentWorkspaceProjectLen+2 || rest[agentWorkspaceProjectLen] != '-' {
		return "", "", false
	}
	project, rest = rest[:agentWorkspaceProjectLen], rest[agentWorkspaceProjectLen+1:]
	if !isLowerHex(project) {
		return "", "", false
	}
	i := strings.LastIndexByte(rest, '-')
	if i <= 0 || !isDigits(rest[i+1:]) {
		return "", "", false
	}
	return project, rest[:i], true
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return s != ""
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// agentWorkspaceRoot is the resolved workspace root of s.
func agentWorkspaceRoot(s *runAsSettings) (string, error) {
	if s == nil || s.workspaceRoot == "" {
		return "", errors.New("agent workspace: run_as_user is not set")
	}
	root, err := resolveGitRoutePath(s.workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("agent workspace: resolve run_as_workspace_dir %q: %w", s.workspaceRoot, err)
	}
	return root, nil
}

// createAgentWorkspace creates the agent workspace of a task run and returns
// its path and a cleanup function with the contract of the worktree cleanup
// (safe to call more than once; it removes the workspace through
// removeAgentWorkspace and deletes no branch of the daemon's repository).
//
// baseRef has the meaning of the baseBranch of CreateWorktreeWithBranch: an
// explicit ref (origin/BRANCH, a commit SHA, FETCH_HEAD) or "" for a fresh
// fetch of the base branch of m. The daemon resolves it to a commit SHA in its
// own repository. Before it returns, the daemon checks the result
// (verifyAgentWorkspace); a failed check removes the workspace.
//
// Decision: a method of WorktreeManager, not a function of the repository
// path: an empty baseRef needs the configured base branch (SetBaseBranch)
// exactly as CreateWorktreeWithBranch does.
func (m *WorktreeManager) createAgentWorkspace(ctx context.Context, taskID, branch, baseRef string) (string, func(), error) {
	if err := runAsSpawnError(); err != nil {
		return "", nil, err
	}
	s := runAsSettingsNow()
	root, err := agentWorkspaceRoot(s)
	if err != nil {
		return "", nil, err
	}
	// The preflight result lives as long as the process; the daemon creates
	// data entries at run time, so the modes are read again for each run.
	if failed := checkRunAsDataDirModes(s); failed != "" {
		return "", nil, fmt.Errorf("agent workspace: %s", failed)
	}
	if taskID == "" {
		// A name without TASK is never parsed, so no sweep would remove it.
		return "", nil, errors.New("agent workspace: no task ID")
	}
	if branch == "" {
		return "", nil, errors.New("agent workspace: no task branch")
	}
	project, err := workspaceProjectID(m.repoPath)
	if err != nil {
		return "", nil, err
	}
	// Before the first git command of the agent user in the workspace.
	rememberAgentGitIdentity(ctx, project, m.repoPath)

	base := m.resolveBaseBranch(ctx)
	ref := baseRef
	if ref == "" {
		if ref, err = fetchOriginBranchForWorktree(ctx, m.repoPath, base); err != nil {
			return "", nil, err
		}
	}
	sha, err := resolveCommitSHA(ctx, m.repoPath, ref)
	if err != nil {
		return "", nil, err
	}

	name := fmt.Sprintf("%s%s-%s-%d", agentWorkspacePrefix, project, sanitizeBranchName(taskID), time.Now().UnixNano())
	wsPath := filepath.Join(root, name)
	remove := func() {
		agentWorkspaceBases.Delete(wsPath)
		_ = removeAgentWorkspace(s, wsPath)
	}

	if err := transportWorkspaceBase(ctx, m.repoPath, wsPath, sha, branch, base); err != nil {
		remove()
		return "", nil, err
	}
	if agentWorkspaceAfterTransport != nil {
		agentWorkspaceAfterTransport(wsPath)
	}
	if err := verifyAgentWorkspace(ctx, m.repoPath, wsPath, sha, branch, base); err != nil {
		remove()
		return "", nil, err
	}

	agentWorkspaceBases.Store(wsPath, sha)

	slog.Info("Created agent workspace with branch",
		slog.String("task_id", taskID),
		slog.String("branch", branch),
		slog.String("base_ref", ref),
		slog.String("base_sha", sha),
		slog.String("workspace", wsPath),
	)
	var once sync.Once
	return wsPath, func() { once.Do(remove) }, nil
}

// agentWorkspaceBases holds the base commit of each live agent workspace by
// its path, the ^BASE of the hand-over (handOverBranch). The daemon records
// it: the refs of the workspace are the agent's. Stored when
// createAgentWorkspace returns, deleted by its cleanup.
var agentWorkspaceBases sync.Map

// agentWorkspaceBase is the base commit of the workspace at path, "" when
// none is on record.
func agentWorkspaceBase(path string) string {
	if v, ok := agentWorkspaceBases.Load(filepath.Clean(path)); ok {
		sha, _ := v.(string)
		return sha
	}
	return ""
}

// resolveCommitSHA resolves ref to a commit SHA in the daemon's repository.
func resolveCommitSHA(ctx context.Context, repoPath, ref string) (string, error) {
	out, err := gitCommandIn(ctx, repoPath, "rev-parse", "--verify", "-q", ref+"^{commit}").Output()
	sha := strings.TrimSpace(string(out))
	if err != nil || sha == "" || !isLowerHex(sha) {
		return "", fmt.Errorf("agent workspace: base %q is not a commit in %s: %v", ref, repoPath, err)
	}
	return sha, nil
}

// transportWorkspaceBase is the whole transport of the base commit from the
// daemon's repository into a new workspace. The daemon names sha with a
// temporary ref, runs git bundle create with the output on standard output
// and pipes it into a process of the agent user that stores it as the bundle
// file next to wsPath. Then, as the agent user: git init wsPath, one fetch
// from the bundle file, the task branch at sha, refs/remotes/origin/BASE at
// sha (base is the base branch of the run), the origin URL of the daemon's
// repository without a user or password part, and the removal of the bundle
// file. The temporary ref and the bundle file are removed in every outcome.
// The agent user never reads the daemon's repository and the daemon writes no
// file into the workspace root.
//
// Decision: origin/BASE is at the base commit, also for an explicit base (a
// fix continuation): the commit count and the merge-base of a run compare
// against origin/BASE (CountNewCommitsAgainstOrigin, resolveMergeBaseSHA), and
// the agent user gets no credential to fetch it; at the base commit they
// count the commits of this run.
func transportWorkspaceBase(ctx context.Context, repoPath, wsPath, sha, branch, base string) error {
	root := filepath.Dir(wsPath)
	bundlePath := wsPath + agentWorkspaceBundleSuffix
	tmpRef := agentWorkspaceBaseRefPrefix + filepath.Base(wsPath)
	// Cleanup runs even when ctx is done.
	cleanupCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.WithoutCancel(ctx), agentWorkspaceRemoveTimeout)
	}

	if out, err := gitCommandIn(ctx, repoPath, "update-ref", tmpRef, sha).CombinedOutput(); err != nil {
		return fmt.Errorf("agent workspace: name the base commit: %w: %s", err, out)
	}
	defer func() {
		cctx, cancel := cleanupCtx()
		defer cancel()
		if out, err := gitCommandIn(cctx, repoPath, "update-ref", "-d", tmpRef).CombinedOutput(); err != nil {
			slog.Warn("agent workspace: delete of the temporary base ref failed",
				slog.String("ref", tmpRef), slog.Any("error", err), slog.String("output", strings.TrimSpace(string(out))))
		}
	}()

	// Removal of the bundle file, as the agent user, in every outcome after
	// the store started.
	defer func() {
		cctx, cancel := cleanupCtx()
		defer cancel()
		cmd, err := agentCommand(cctx, "rm", "-f", "--", bundlePath)
		if err == nil {
			setAgentEnv(cmd, gitAgentBaseEnv())
			var out []byte
			if out, err = cmd.CombinedOutput(); err != nil {
				err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
			}
		}
		if err != nil {
			slog.Warn("agent workspace: removal of the bundle file failed",
				slog.String("path", bundlePath), slog.Any("error", err))
		}
	}()

	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("agent workspace: pipe: %w", err)
	}
	bundle := gitCommandIn(ctx, repoPath, "bundle", "create", "-", tmpRef)
	var bundleErr bytes.Buffer
	bundle.Stdout, bundle.Stderr = pw, &bundleErr
	store, err := agentCommand(ctx, "sh", "-c", agentWorkspaceStoreScript, "sh", bundlePath)
	if err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return fmt.Errorf("agent workspace: store the bundle: %w", err)
	}
	setAgentEnv(store, gitAgentBaseEnv())
	var storeOut bytes.Buffer
	store.Stdin, store.Stdout, store.Stderr = pr, &storeOut, &storeOut
	if err := bundle.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return fmt.Errorf("agent workspace: git bundle create: %w", err)
	}
	storeStartErr := store.Start()
	// The children hold their own copies; closed here, the store sees the end
	// of the stream and a store that died gives git bundle a broken pipe.
	_ = pr.Close()
	_ = pw.Close()
	var storeErr error
	if storeStartErr != nil {
		storeErr = storeStartErr
	} else {
		storeErr = store.Wait()
	}
	bundleWaitErr := bundle.Wait()
	// A store that failed breaks the pipe of git bundle: its error is the cause.
	if storeErr != nil {
		return fmt.Errorf("agent workspace: store the bundle as the agent user: %w: %s", storeErr, strings.TrimSpace(storeOut.String()))
	}
	if bundleWaitErr != nil {
		return fmt.Errorf("agent workspace: git bundle create: %w: %s", bundleWaitErr, strings.TrimSpace(bundleErr.String()))
	}

	steps := []struct {
		what string
		dir  string
		args []string
	}{
		{what: "git init", dir: root, args: []string{"init", "-q", wsPath}},
		{what: "fetch from the bundle", dir: wsPath, args: []string{"fetch", "-q", "--no-tags", bundlePath, tmpRef}},
		{what: "task branch", dir: wsPath, args: []string{"checkout", "-q", "-B", branch, sha}},
		{what: "origin/" + base, dir: wsPath, args: []string{"update-ref", "refs/remotes/origin/" + base, sha}},
	}
	for _, st := range steps {
		if out, err := gitCommandIn(ctx, st.dir, st.args...).CombinedOutput(); err != nil {
			return fmt.Errorf("agent workspace: %s: %w: %s", st.what, err, strings.TrimSpace(string(out)))
		}
	}

	// The origin URL is the daemon's, read in the daemon's repository; a
	// repository without one gives a workspace without one.
	if out, err := gitCommandIn(ctx, repoPath, "remote", "get-url", "origin").Output(); err == nil {
		originURL, err := stripURLCredentials(strings.TrimSpace(string(out)))
		if err != nil {
			return err
		}
		if out, err := gitCommandIn(ctx, wsPath, "remote", "add", "origin", originURL).CombinedOutput(); err != nil {
			return fmt.Errorf("agent workspace: origin URL: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// stripURLCredentials removes the user and password part of a remote URL: the
// userinfo of a URL with a scheme, the user@ of the scp form user@host:path.
// The error never holds the URL, which may carry a credential.
func stripURLCredentials(raw string) (string, error) {
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", errors.New("agent workspace: the origin URL of the repository does not parse")
		}
		u.User = nil
		return u.String(), nil
	}
	colon := strings.IndexByte(raw, ':')
	slash := strings.IndexByte(raw, '/')
	if colon > 0 && (slash < 0 || colon < slash) {
		if at := strings.LastIndexByte(raw[:colon], '@'); at >= 0 {
			return raw[at+1:], nil
		}
	}
	return raw, nil
}

// verifyAgentWorkspace is the check of the daemon before createAgentWorkspace
// returns: the daemon user can list the workspace; the task branch is at sha;
// the objects directory is a directory, not a symbolic link; the repository
// has no alternates file and no object file that is a hard link into the
// daemon's repository; it holds no ref other than the task branch and
// refs/remotes/origin/BASE, both at sha (the fetched base is FETCH_HEAD); no
// bundle file is left in the workspace root. The git commands run in the
// workspace, so as the agent user.
func verifyAgentWorkspace(ctx context.Context, repoPath, wsPath, sha, branch, base string) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("agent workspace check failed: "+format, args...)
	}
	if _, err := os.ReadDir(wsPath); err != nil {
		return fail("the daemon user cannot list %s: %v", wsPath, err)
	}
	head, err := gitCommandIn(ctx, wsPath, "rev-parse", "--verify", "-q", "refs/heads/"+branch).Output()
	if got := strings.TrimSpace(string(head)); err != nil || got != sha {
		return fail("head of %s is %q, want the base commit %s (%v)", branch, got, sha, err)
	}
	gitDir := filepath.Join(wsPath, ".git")
	if fi, err := os.Lstat(gitDir); err != nil || !fi.IsDir() {
		return fail(".git of %s is not a directory", wsPath)
	}
	objects := filepath.Join(gitDir, "objects")
	if fi, err := os.Lstat(objects); err != nil || !fi.IsDir() {
		return fail("the objects directory of %s is not a directory (a symbolic link?)", wsPath)
	}
	for _, name := range []string{"alternates", "http-alternates"} {
		if _, err := os.Lstat(filepath.Join(gitDir, "objects", "info", name)); !errors.Is(err, fs.ErrNotExist) {
			return fail("%s has an objects/info/%s file", wsPath, name)
		}
	}
	if linked, err := hardLinkedObject(ctx, repoPath, objects); err != nil || linked != "" {
		return fail("object file %q is a hard link into the daemon's repository (%v)", linked, err)
	}
	refs, err := gitCommandIn(ctx, wsPath, "for-each-ref", "--format=%(refname) %(objectname)").Output()
	if err != nil {
		return fail("list the refs: %v", err)
	}
	want := map[string]bool{"refs/heads/" + branch + " " + sha: true, "refs/remotes/origin/" + base + " " + sha: true}
	got := strings.Split(strings.TrimSpace(string(refs)), "\n")
	ok := len(got) == len(want)
	for _, line := range got {
		ok = ok && want[line]
	}
	if !ok {
		return fail("refs are %q, want only refs/heads/%s and refs/remotes/origin/%s at %s", got, branch, base, sha)
	}
	if _, err := os.Lstat(wsPath + agentWorkspaceBundleSuffix); !errors.Is(err, fs.ErrNotExist) {
		return fail("the bundle file %s is still in the workspace root", wsPath+agentWorkspaceBundleSuffix)
	}
	return nil
}

// hardLinkedObject returns an object file below objectsDir that is a hard link
// of a file in the objects directory of the daemon's repository, "" when there
// is none. Only files with more than one link are compared.
func hardLinkedObject(ctx context.Context, repoPath, objectsDir string) (string, error) {
	type fileID struct{ dev, ino uint64 }
	linked := map[fileID]string{}
	err := filepath.WalkDir(objectsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if dev, ino, nlink, ok := fileLinkID(fi); ok && nlink > 1 {
			linked[fileID{dev, ino}] = p
		}
		return nil
	})
	if err != nil || len(linked) == 0 {
		return "", err
	}
	out, err := gitCommandIn(ctx, repoPath, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", err
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(repoPath, common)
	}
	var found string
	err = filepath.WalkDir(filepath.Join(common, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || found != "" || !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if dev, ino, _, ok := fileLinkID(fi); ok {
			if ws, hit := linked[fileID{dev, ino}]; hit {
				found = ws
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found, err
}

// checkAgentWorkspacePath checks a path before its removal as the agent
// user: a direct child of the resolved workspace root, a name that starts with
// pilot-ws-, and no symbolic-link component (filepath.EvalSymlinks equals the
// path). A path that does not exist gives an error that wraps fs.ErrNotExist.
func checkAgentWorkspacePath(s *runAsSettings, path string) error {
	root, err := agentWorkspaceRoot(s)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("agent workspace %q: not a clean absolute path", path)
	}
	if filepath.Dir(path) != root {
		return fmt.Errorf("agent workspace %q: not a direct child of the workspace root %q", path, root)
	}
	if !strings.HasPrefix(filepath.Base(path), agentWorkspacePrefix) {
		return fmt.Errorf("agent workspace %q: the name does not start with %s", path, agentWorkspacePrefix)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("agent workspace %q: %w", path, err)
	}
	if real != path {
		return fmt.Errorf("agent workspace %q: has a symbolic-link component (resolves to %q)", path, real)
	}
	return nil
}

// removeAgentWorkspace removes a workspace (or its bundle file) with `rm -rf`
// as the agent user of s, bounded by 60 seconds. A path that fails
// checkAgentWorkspacePath is not removed: one Error line. A path that does not
// exist is nothing to remove. No branch of the daemon's repository is
// deleted: the daemon's repository never got the task branch, and a local
// branch with the same name belongs to another flow. While the preflight
// flag is set it starts no process and logs one Warn line: the settings then
// hold names the preflight did not check (runAsIncompleteRefusal fails before
// the name check).
func removeAgentWorkspace(s *runAsSettings, path string) error {
	if err := runAsSpawnError(); err != nil {
		slog.Warn("agent workspace not removed: the run_as_user preflight did not pass",
			slog.Int("directories", 1), slog.String("path", path), slog.Any("error", err))
		return err
	}
	if err := checkAgentWorkspacePath(s, path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		slog.Error("agent workspace not removed: the path failed the check",
			slog.String("path", path), slog.Any("error", err))
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentWorkspaceRemoveTimeout)
	defer cancel()
	cmd, err := agentCommandAs(ctx, s, "rm", "-rf", "--", path)
	if err == nil {
		setAgentEnv(cmd, gitAgentBaseEnv())
		var out []byte
		if out, err = cmd.CombinedOutput(); err != nil {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	if err != nil {
		slog.Error("agent workspace removal as the agent user failed",
			slog.String("path", path), slog.Any("error", err))
		return err
	}
	return nil
}

// agentWorkspaceOrphans returns the direct children of the workspace root
// whose name is a workspace (or bundle file) of the project of repoPath and,
// when taskID is not empty, of that task. Nil when the option is empty. A
// workspace of another project is never returned.
func agentWorkspaceOrphans(repoPath, taskID string) (*runAsSettings, []string, error) {
	s := runAsSettingsNow()
	if s == nil {
		return nil, nil, nil
	}
	root, err := agentWorkspaceRoot(s)
	if err != nil {
		return s, nil, err
	}
	project, err := workspaceProjectID(repoPath)
	if err != nil {
		return s, nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return s, nil, fmt.Errorf("agent workspace: read the workspace root %s: %w", root, err)
	}
	task := sanitizeBranchName(taskID)
	var found []string
	for _, e := range entries {
		p, t, ok := parseAgentWorkspaceName(e.Name())
		if !ok || p != project || (taskID != "" && t != task) {
			continue
		}
		found = append(found, filepath.Join(root, e.Name()))
	}
	return s, found, nil
}

// sweepAgentWorkspaces removes the orphan workspaces of the project of
// repoPath (of one task when taskID is set) through removeAgentWorkspace and
// logs one Info line per removed workspace. It returns the count and the
// bytes freed. Nothing happens with the option empty. While the preflight
// flag is set it removes nothing, starts no process and logs one Warn line
// with the count of the workspaces it found.
func sweepAgentWorkspaces(repoPath, taskID string) (int, int64) {
	s, orphans, err := agentWorkspaceOrphans(repoPath, taskID)
	if err != nil {
		slog.Warn("agent workspace sweep skipped", slog.String("repo", repoPath), slog.Any("error", err))
		return 0, 0
	}
	if s == nil {
		return 0, 0
	}
	if err := runAsSpawnError(); err != nil {
		slog.Warn("agent workspace sweep skipped: the run_as_user preflight did not pass",
			slog.String("repo", repoPath), slog.Int("directories", len(orphans)), slog.Any("error", err))
		return 0, 0
	}
	removed := 0
	var freed int64
	for _, p := range orphans {
		_, task, _ := parseAgentWorkspaceName(filepath.Base(p))
		size := dirSizeBytes(p)
		if removeAgentWorkspace(s, p) != nil {
			continue
		}
		slog.Info("Removed orphaned agent workspace",
			slog.String("task_id", task),
			slog.String("path", p),
			slog.String("size_mb", fmt.Sprintf("%.1f", float64(size)/(1024*1024))),
		)
		removed++
		freed += size
	}
	return removed, freed
}
