package executor

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TASK-651 (design: .agent/system/agent-isolation-design.md, 4.2): the
// Navigator copy in both directions with claude_code.run_as_user set. The
// daemon process creates, changes and removes no file inside the workspace
// root, and a read of the daemon from a workspace never leaves it.
//
//   - in: the daemon reads the .agent directory of its repository (the files
//     CopyNavigatorToWorktree selects) and sends them as one tar stream
//     (navigatorTarStream) to a tar process that runs as the agent user in the
//     workspace. The agent user needs no read access to the daemon's tree.
//   - out: the daemon opens every path element below the workspace without
//     following a link (openWorkspaceNoFollow), reads regular files only and
//     writes each one inside the .agent directory of its repository
//     (navigatorCopyBackDest). The tracked-path lists of the workspace come
//     through the git constructor; a failure of one stops the copy.

// navigatorCopyTimeout bounds the tar process of the copy into a workspace.
const navigatorCopyTimeout = 5 * time.Minute

// errNavigatorTarExited is the error of a write to the stream after the tar
// process has exited.
var errNavigatorTarExited = errors.New("tar exited")

// copyNavigatorToAgentWorkspace is CopyNavigatorToWorktree for a workspace
// inside the workspace root: the same files, extracted by tar as the agent
// user from standard input.
func copyNavigatorToAgentWorkspace(sourceRepo, worktreePath string) error {
	sourceAgent := filepath.Join(sourceRepo, ".agent")
	info, err := os.Stat(sourceAgent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to stat source .agent: %w", err)
	}
	if !info.IsDir() {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), navigatorCopyTimeout)
	defer cancel()
	cmd, err := agentCommand(ctx, "tar", "-C", worktreePath, "-x", "-f", "-")
	if err != nil {
		return fmt.Errorf("run_as_user: navigator copy into %s as the agent user: %w", worktreePath, err)
	}
	setAgentEnv(cmd, gitAgentBaseEnv())
	pr, pw := io.Pipe()
	cmd.Stdin = pr
	var once sync.Once
	stop := func() {
		once.Do(func() {
			if cmd.Cancel == nil || cmd.Cancel() != nil {
				cancel()
			}
		})
	}
	stdout := &agentLimitedBuffer{name: "standard output", limit: agentFileStderrLimit, stop: stop}
	stderr := &agentLimitedBuffer{name: "standard error", limit: agentFileStderrLimit, stop: stop}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		return fmt.Errorf("run_as_user: navigator copy into %s as the agent user: %w", worktreePath, err)
	}
	streamErr := make(chan error, 1)
	go func() {
		err := navigatorTarStream(pw, sourceAgent)
		_ = pw.CloseWithError(err)
		streamErr <- err
	}()
	waitErr := cmd.Wait()
	// A tar that exited reads no more: unblock the stream.
	_ = pr.CloseWithError(errNavigatorTarExited)
	if err := <-streamErr; err != nil && !errors.Is(err, errNavigatorTarExited) {
		return fmt.Errorf("run_as_user: navigator copy into %s: %w", worktreePath, err)
	}
	for _, b := range []*agentLimitedBuffer{stdout, stderr} {
		if b.over {
			return fmt.Errorf("run_as_user: navigator copy into %s as the agent user: the %s of tar is over the limit of %d bytes, the command was stopped", worktreePath, b.name, b.limit)
		}
	}
	if waitErr != nil {
		return fmt.Errorf("run_as_user: navigator copy into %s as the agent user: %w: %s", worktreePath, waitErr, strings.TrimSpace(stderr.buf.String()))
	}
	return nil
}

// navigatorTarStream writes the directory sourceAgent (the .agent directory of
// the daemon's repository) to w as one tar archive with names below ".agent/":
// the files copyDir copies today, with their permission bits and modification
// times (the newer-wins rule of the copy back depends on them). As copyDir, it
// descends directories and reads every other entry through its link; an entry
// that cannot be read as a file is an error.
func navigatorTarStream(w io.Writer, sourceAgent string) error {
	tw := tar.NewWriter(w)
	if err := navigatorTarDir(tw, sourceAgent, ".agent"); err != nil {
		return err
	}
	return tw.Close()
}

func navigatorTarDir(tw *tar.Writer, src, name string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     name + "/",
		Mode:     int64(info.Mode().Perm()),
		ModTime:  info.ModTime(),
		Format:   tar.FormatPAX,
	}); err != nil {
		return fmt.Errorf("tar header %s: %w", name, err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("failed to read directory %s: %w", src, err)
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		entryName := name + "/" + entry.Name()
		if entry.IsDir() {
			if err := navigatorTarDir(tw, srcPath, entryName); err != nil {
				return err
			}
			continue
		}
		if err := navigatorTarFile(tw, srcPath, entryName); err != nil {
			return err
		}
	}
	return nil
}

func navigatorTarFile(tw *tar.Writer, src, name string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(src) // #nosec G304 -- the daemon's own .agent directory, as copyFile reads it.
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", src, err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Mode:     int64(info.Mode().Perm()),
		Size:     int64(len(content)),
		ModTime:  info.ModTime(),
		Format:   tar.FormatPAX,
	}); err != nil {
		return fmt.Errorf("tar header %s: %w", name, err)
	}
	if _, err := tw.Write(content); err != nil {
		return fmt.Errorf("tar %s: %w", name, err)
	}
	return nil
}

// copyNavigatorBackNoFollow is CopyNavigatorBackFromWorktree with the option
// set: the same rules, files and graph merge, but every read from worktreePath
// goes through openWorkspaceNoFollow, an entry that is not a regular file or
// a directory is skipped, and a failure to list the tracked paths of the
// worktree stops the copy before any file is written.
func copyNavigatorBackNoFollow(worktreePath, sourceRepo string) (int, error) {
	// Lstat: the entry itself, never the target of a link at .agent.
	if _, err := os.Lstat(filepath.Join(worktreePath, ".agent")); os.IsNotExist(err) {
		return 0, nil
	}

	navigatorCopyBackMu.Lock()
	defer navigatorCopyBackMu.Unlock()

	srcTracked, err := gitListPaths(sourceRepo, ".agent")
	if err != nil {
		return 0, fmt.Errorf("list tracked navigator files in %s: %w", sourceRepo, err)
	}
	// Decision: unlike the plain copy, the worktree lists are required. They
	// run as the agent user through the git constructor; without them a file
	// that came from the branch base would land as an untracked file on the
	// clone.
	wtTracked, err := gitListPaths(worktreePath, ".agent")
	if err != nil {
		return 0, fmt.Errorf("list tracked navigator files in %s: %w", worktreePath, err)
	}
	wtModified, err := gitListPaths(worktreePath, ".agent", "-m")
	if err != nil {
		return 0, fmt.Errorf("list modified navigator files in %s: %w", worktreePath, err)
	}

	skip := func(rel string) bool {
		if srcTracked[rel] {
			return true
		}
		return wtTracked[rel] && !wtModified[rel]
	}

	merged := 0
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	mergeFile := func(rel string) {
		if skip(rel) {
			return
		}
		content, info, err := readWorkspaceFileNoFollow(worktreePath, rel)
		if err != nil || info == nil {
			note(err)
			return
		}
		dst, err := navigatorCopyBackDest(sourceRepo, rel)
		if err != nil {
			note(err)
			return
		}
		wrote, err := mergeNavigatorContentBack(content, info, dst)
		note(err)
		if wrote {
			merged++
		}
	}

	var walk func(rel string)
	walk = func(rel string) {
		dir, err := openWorkspaceNoFollow(worktreePath, rel, true)
		if err != nil {
			if !noFollowSkipped(err) {
				note(err)
			}
			return
		}
		entries, err := dir.ReadDir(-1)
		_ = dir.Close()
		note(err)
		for _, e := range entries {
			child := rel + "/" + e.Name()
			switch {
			case e.IsDir():
				walk(child)
			case e.Type().IsRegular():
				mergeFile(child)
			}
			// symlinks and specials are ignored
		}
	}
	for _, dir := range navigatorCopyBackDirs {
		walk(dir)
	}

	if !skip(navigatorCopyBackGraph) {
		data, info, err := readWorkspaceFileNoFollow(worktreePath, navigatorCopyBackGraph)
		note(err)
		if err == nil && info != nil {
			dst, destErr := navigatorCopyBackDest(sourceRepo, navigatorCopyBackGraph)
			note(destErr)
			if destErr == nil {
				wtPath := filepath.Join(worktreePath, filepath.FromSlash(navigatorCopyBackGraph))
				wrote, graphErr := mergeNavigatorGraphDataBack(wtPath, data, info, dst)
				note(graphErr)
				if wrote {
					merged++
				}
			}
		}
	}

	return merged, firstErr
}

// readWorkspaceFileNoFollow reads the file rel (slash-separated) below root
// through openWorkspaceNoFollow. A missing path, a link at any element and an
// entry that is not a regular file give nil content, nil info and no error:
// they are skipped, as the plain copy skips a link.
func readWorkspaceFileNoFollow(root, rel string) ([]byte, os.FileInfo, error) {
	f, err := openWorkspaceNoFollow(root, rel, false)
	if err != nil {
		if noFollowSkipped(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, nil
	}
	content, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", f.Name(), err)
	}
	return content, info, nil
}

// noFollowSkipped reports whether err of openWorkspaceNoFollow means the path
// is not there as a directory chain of real entries: missing, a link at an
// element, or a file where a directory should be.
func noFollowSkipped(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || noFollowRefused(err)
}

// navigatorCopyBackDest is the destination of the worktree path rel
// (slash-separated, below .agent) in sourceRepo. It must stay inside the
// .agent directory of sourceRepo as given and as resolved: the nearest
// existing directory that holds it resolves inside that directory (or is the
// repository itself, before .agent exists). Any other path is an error.
func navigatorCopyBackDest(sourceRepo, rel string) (string, error) {
	repo, err := filepath.EvalSymlinks(sourceRepo)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", sourceRepo, err)
	}
	navDir := filepath.Join(repo, ".agent")
	dst := filepath.Join(repo, filepath.FromSlash(rel))
	if dst == navDir || !pathInside(navDir, dst) {
		return "", fmt.Errorf("navigator copy-back: %q leaves %s", rel, navDir)
	}
	for p := filepath.Dir(dst); ; p = filepath.Dir(p) {
		if _, err := os.Lstat(p); err != nil {
			if p == repo {
				return "", fmt.Errorf("navigator copy-back: stat %s: %w", p, err)
			}
			continue
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return "", fmt.Errorf("navigator copy-back: resolve %s: %w", p, err)
		}
		if real != repo && !pathInside(navDir, real) {
			return "", fmt.Errorf("navigator copy-back: %q resolves to %s, outside %s", rel, real, navDir)
		}
		return dst, nil
	}
}
