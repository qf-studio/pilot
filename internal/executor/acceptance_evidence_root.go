package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
)

// GH-6664: with the option empty the mutation write and the restore write of
// a mutation line open the file through the workspace root as one step. The
// root is opened once (os.OpenRoot), the parent directory is opened through it,
// and the temp file, the write and the rename go through that directory: a
// path part that the command made a link to a directory outside the workspace
// between the check and the write is an error of the root, never a file
// outside. With the option set the writes run as the agent user, as before.

// mutationRootWriteFile writes data to path, a file under the task directory
// dir, through a new file beside it that is renamed over path (the
// agentReplaceFile contract): as the agent user with the option set, else
// through the workspace root.
func mutationRootWriteFile(ctx context.Context, dir, path string, data []byte, mode os.FileMode) error {
	agent, err := agentFileRoute(path)
	if err != nil {
		return err
	}
	if agent {
		return evidenceWriteFile(ctx, path, data, mode)
	}
	return inMutationParentRoot(dir, path, func(parent *os.Root, name string) error {
		return rootReplaceFile(parent, name, data, mode)
	})
}

// mutationRootRecreateFile removes path and makes a new regular file there
// with data and mode (the agentRecreateFile contract): as the agent user with
// the option set, else through the workspace root.
func mutationRootRecreateFile(ctx context.Context, dir, path string, data []byte, mode os.FileMode) error {
	agent, err := agentFileRoute(path)
	if err != nil {
		return err
	}
	if agent {
		return agentRecreateFile(ctx, path, data, mode)
	}
	return inMutationParentRoot(dir, path, func(parent *os.Root, name string) error {
		if err := parent.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		f, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
		if err != nil {
			return err
		}
		return writeThenChmod(f, data, mode)
	})
}

// inMutationParentRoot opens the task directory dir as a root, opens the
// parent directory of path through it and runs fn on that directory and the
// base name of path. A path that is not under dir is errPathEscapesWorktree.
func inMutationParentRoot(dir, path string, fn func(parent *os.Root, name string) error) error {
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolving worktree root: %w", err)
	}
	rel, err := filepath.Rel(resolvedDir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errPathEscapesWorktree
	}
	root, err := os.OpenRoot(resolvedDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	parent, err := root.OpenRoot(filepath.Dir(rel))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	return fn(parent, filepath.Base(rel))
}

// rootReplaceFile writes data to name in parent through a new file beside it
// that gets mode (with the setuid, setgid and sticky bits) after the content
// and is renamed over name: a link at name is replaced, never followed.
func rootReplaceFile(parent *os.Root, name string, data []byte, mode os.FileMode) error {
	var (
		f   *os.File
		tmp string
		err error
	)
	for range 10 {
		tmp = fmt.Sprintf(".%s.pilot-%d", name, rand.Uint64())
		f, err = parent.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	if err != nil {
		return err
	}
	if err := writeThenChmod(f, data, mode); err != nil {
		_ = parent.Remove(tmp)
		return err
	}
	if err := parent.Rename(tmp, name); err != nil {
		_ = parent.Remove(tmp)
		return err
	}
	return nil
}
