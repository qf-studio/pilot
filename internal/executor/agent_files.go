package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TASK-651 (design: .agent/system/agent-isolation-design.md, 4.2): the files
// the daemon creates, changes, removes or reads in the workspace of a run (the
// task document, the Navigator files, .claude/settings.json, the simplified
// files). With claude_code.run_as_user set and a path inside the workspace
// root (the path rule of the git constructor, gitRoutesToAgent) the operation
// runs as the agent user through agentCommand: the daemon process creates,
// changes and removes no file inside the root, and a symbolic link the agent
// put at the path is followed with the rights of the agent user only. Data
// goes on standard input, never in the argv. With the option empty, or a path
// outside the root, each helper is the os call of today. An error of
// agentCommand is returned; there is no fallback to the os call.

// agentFileTimeout bounds one file operation as the agent user.
const agentFileTimeout = 30 * time.Second

// agentFileMissingExit is the exit code of the scripts below for a path that
// does not exist: the helper turns it into an error that wraps
// fs.ErrNotExist, as the os call returns.
const agentFileMissingExit = 3

// The scripts run as `sh -c SCRIPT sh ARGS...` as the agent user.
const (
	// $1 path. Standard output is the content.
	agentReadScript = `[ -e "$1" ] || exit 3; exec cat -- "$1"`
	// $1 path, $2 umask. Standard input is the content: create or truncate,
	// as os.WriteFile (the mode of an existing file stays).
	agentWriteScript = `umask "$2" && exec cat > "$1"`
	// $1 path, $2 umask.
	agentMkdirScript = `umask "$2" && exec mkdir -p -- "$1"`
	// $1 old path, $2 new path: os.Rename, a file at $2 is replaced.
	agentRenameScript = `[ -e "$1" ] || [ -L "$1" ] || exit 3; exec mv -f -T -- "$1" "$2"`
	// $1 path: os.Remove, a file or an empty directory.
	agentRemoveScript = `[ -e "$1" ] || [ -L "$1" ] || exit 3
if [ -d "$1" ] && [ ! -L "$1" ]; then exec rmdir -- "$1"; fi
exec rm -f -- "$1"`
	// $1 path: remove the directory when it is an empty directory, else
	// nothing.
	agentRemoveEmptyDirScript = `{ [ -d "$1" ] && [ ! -L "$1" ]; } || exit 0
[ -z "$(ls -A -- "$1")" ] || exit 0
exec rmdir -- "$1"`
)

// agentFileRoute reports whether a file operation on path runs as the agent
// user: the option is set and the directory that holds path (its nearest
// existing parent when it does not exist yet) takes the agent route of the
// git constructor. A path inside the root as given whose directory does not
// take the agent route is an error, as is a directory that is inside the
// root in only one of its given and resolved forms (gitRoutesToAgent).
func agentFileRoute(path string) (bool, error) {
	s := runAsSettingsNow()
	if s == nil {
		return false, nil
	}
	given, err := absCleanPath(path)
	if err != nil {
		return false, fmt.Errorf("run_as_user: file %q: %w", path, err)
	}
	dir := filepath.Dir(given)
	for {
		if _, err := os.Lstat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	agent, err := gitRoutesToAgent(s.workspaceRoot, dir)
	if err != nil {
		return false, fmt.Errorf("run_as_user: file %q: %w", path, err)
	}
	if !agent {
		for _, r := range pathForms(s.workspaceRoot) {
			if pathInside(r, given) {
				return false, fmt.Errorf("run_as_user: file %q is inside the workspace root %q, its directory %q is not", path, s.workspaceRoot, dir)
			}
		}
	}
	return agent, nil
}

// runAgentFileScript runs script as the agent user with args, stdin as its
// standard input (the null device when nil), and returns its standard output.
func runAgentFileScript(ctx context.Context, op, path, script string, stdin []byte, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, agentFileTimeout)
	defer cancel()
	cmd, err := agentCommand(ctx, "sh", append([]string{"-c", script, "sh"}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("run_as_user: %s %s as the agent user: %w", op, path, err)
	}
	setAgentEnv(cmd, gitAgentBaseEnv())
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == agentFileMissingExit {
			return nil, &fs.PathError{Op: op, Path: path, Err: fs.ErrNotExist}
		}
		return nil, fmt.Errorf("run_as_user: %s %s as the agent user: %w: %s", op, path, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// agentUmask is the umask under which a new file or directory gets the
// permission bits of mode, as the os call gives it under umask 0.
func agentUmask(mode os.FileMode) string {
	return "0" + strconv.FormatUint(uint64(0o777&^mode.Perm()), 8)
}

// agentWriteFile is os.WriteFile(path, data, mode), as the agent user for a
// path inside the workspace root.
func agentWriteFile(ctx context.Context, path string, data []byte, mode os.FileMode) error {
	agent, err := agentFileRoute(path)
	if err != nil {
		return err
	}
	if !agent {
		return os.WriteFile(path, data, mode)
	}
	if data == nil {
		data = []byte{}
	}
	_, err = runAgentFileScript(ctx, "write", path, agentWriteScript, data, path, agentUmask(mode))
	return err
}

// agentReadFile is os.ReadFile(path), as the agent user for a path inside the
// workspace root: a read through a link the agent put there never gets more
// than the agent user can read.
func agentReadFile(ctx context.Context, path string) ([]byte, error) {
	agent, err := agentFileRoute(path)
	if err != nil {
		return nil, err
	}
	if !agent {
		return os.ReadFile(path) // #nosec G304 -- the os call of today; workspace paths take the agent route.
	}
	return runAgentFileScript(ctx, "read", path, agentReadScript, nil, path)
}

// agentMkdirAll is os.MkdirAll(path, mode), as the agent user for a path
// inside the workspace root.
func agentMkdirAll(ctx context.Context, path string, mode os.FileMode) error {
	agent, err := agentFileRoute(path)
	if err != nil {
		return err
	}
	if !agent {
		return os.MkdirAll(path, mode)
	}
	_, err = runAgentFileScript(ctx, "mkdir", path, agentMkdirScript, nil, path, agentUmask(mode))
	return err
}

// agentRename is os.Rename(oldPath, newPath), as the agent user when both
// paths are inside the workspace root. One path inside and one outside is an
// error.
func agentRename(ctx context.Context, oldPath, newPath string) error {
	oldAgent, err := agentFileRoute(oldPath)
	if err != nil {
		return err
	}
	newAgent, err := agentFileRoute(newPath)
	if err != nil {
		return err
	}
	if oldAgent != newAgent {
		return fmt.Errorf("run_as_user: rename %s to %s: only one of the two is inside the workspace root", oldPath, newPath)
	}
	if !oldAgent {
		return os.Rename(oldPath, newPath)
	}
	_, err = runAgentFileScript(ctx, "rename", oldPath, agentRenameScript, nil, oldPath, newPath)
	return err
}

// agentRemove is os.Remove(path), as the agent user for a path inside the
// workspace root.
func agentRemove(ctx context.Context, path string) error {
	agent, err := agentFileRoute(path)
	if err != nil {
		return err
	}
	if !agent {
		return os.Remove(path)
	}
	_, err = runAgentFileScript(ctx, "remove", path, agentRemoveScript, nil, path)
	return err
}

// agentRemoveDirIfEmpty removes dir when it is an empty directory and does
// nothing when it is missing or holds an entry. Outside the workspace root
// (or with the option empty) it is the os.ReadDir and os.Remove of today.
func agentRemoveDirIfEmpty(ctx context.Context, dir string) error {
	agent, err := agentFileRoute(dir)
	if err != nil {
		return err
	}
	if agent {
		_, err = runAgentFileScript(ctx, "remove empty directory", dir, agentRemoveEmptyDirScript, nil, dir)
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read %s: %w", dir, err)
	}
	if len(entries) != 0 {
		return nil
	}
	if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove empty %s: %w", dir, err)
	}
	return nil
}

// errEvidenceWriteRunAsUser is the error of the mutate and revert writes of
// the evidence commands with the option set: they move to the agent user in
// the gates leg of TASK-651.
var errEvidenceWriteRunAsUser = errors.New("run_as_user: the mutate and revert writes of the evidence commands run as the agent user only after the gates leg of TASK-651 merges")

// evidenceWriteFile is the write of the mutate and revert steps of an
// evidence command: os.WriteFile with the option empty, an error with it set.
func evidenceWriteFile(path string, data []byte, mode os.FileMode) error {
	if runAsSettingsNow() != nil {
		return errEvidenceWriteRunAsUser
	}
	return os.WriteFile(path, data, mode)
}
