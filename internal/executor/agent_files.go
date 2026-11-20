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
	"sync"
	"sync/atomic"
	"time"

	"github.com/qf-studio/pilot/internal/executor/workflow"
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

// agentFileReadLimit bounds the standard output of a helper command (the
// content of a read): the agent can put a link to an endless file at a path
// the daemon reads. agentFileStderrLimit bounds the standard error of every
// helper command. One byte over a limit stops the command through the Cancel
// of agentCommand and the helper returns an error, never partial content.
const (
	agentFileReadLimit   = 4 << 20
	agentFileStderrLimit = 64 << 10
)

// The scripts run as `sh -c SCRIPT sh ARGS...` as the agent user.
const (
	// $1 path. Standard output is the content.
	agentReadScript = `[ -e "$1" ] || exit 3; exec cat -- "$1"`
	// $1 path. As agentReadScript; a path that is not a regular file (a
	// FIFO, a device, a directory) is refused with exit 4. The test of the
	// type and the read use one open of the path (descriptor 3, tested
	// through /dev/fd/3): a swap of the file after the open has no effect.
	// The open of a FIFO waits for a writer; the time limit of the caller
	// bounds it.
	agentReadRegularScript = `[ -e "$1" ] || exit 3; exec 3< "$1" || exit 1; [ -f /dev/fd/3 ] || { echo "not a regular file" >&2; exit 4; }; exec cat <&3`
	// $1 path, $2 umask, $3 mode. Standard input is the content: create or
	// truncate, as os.WriteFile (the mode of an existing file stays). A new
	// file is created under the umask (no bit beyond the mode) and gets the
	// mode before the content: the umask alone never sets an execute bit.
	agentWriteScript = `umask "$2" || exit 1; if [ -e "$1" ]; then exec cat > "$1"; fi; { chmod -- "$3" "$1" && exec cat; } > "$1"`
	// $1 path. Standard output is the `ls -ld` line of the path: the type
	// and the permission bits of the path itself, a link is not followed
	// (os.Lstat).
	agentLstatScript = `[ -e "$1" ] || [ -L "$1" ] || exit 3; exec ls -ld -- "$1"`
	// $1 path, $2 umask, $3 mode. Standard input is the content. The path
	// (a file, a link or an empty directory) is removed and a new regular
	// file is created: noclobber makes the create fail on any path that is
	// there again, so the write never follows a link. The file gets the mode
	// before the content.
	agentRecreateScript = `if [ -d "$1" ] && [ ! -L "$1" ]; then rmdir -- "$1" || exit 1
elif [ -e "$1" ] || [ -L "$1" ]; then rm -f -- "$1" || exit 1; fi
umask "$2" || exit 1; set -C; { chmod -- "$3" "$1" && exec cat; } > "$1"`
	// $1 path, $2 umask.
	agentMkdirScript = `umask "$2" && exec mkdir -p -- "$1"`
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
// Its standard output is bounded by agentFileReadLimit and its standard error
// by agentFileStderrLimit (agentLimitedBuffer).
func runAgentFileScript(ctx context.Context, op, path, script string, stdin []byte, args ...string) ([]byte, error) {
	return runAgentFileScriptLimit(ctx, op, path, script, stdin, agentFileReadLimit, args...)
}

// runAgentFileScriptLimit is runAgentFileScript with readLimit in place of
// agentFileReadLimit for its standard output.
func runAgentFileScriptLimit(ctx context.Context, op, path, script string, stdin []byte, readLimit int, args ...string) ([]byte, error) {
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
	var once sync.Once
	stop := func() {
		once.Do(func() {
			if cmd.Cancel == nil || cmd.Cancel() != nil {
				cancel()
			}
		})
	}
	stdout := &agentLimitedBuffer{name: "standard output", limit: readLimit, stop: stop}
	stderr := &agentLimitedBuffer{name: "standard error", limit: agentFileStderrLimit, stop: stop}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err = cmd.Start(); err == nil {
		if rec, ok := ctx.Value(agentPidRecorderKey{}).(*atomic.Int64); ok {
			rec.Store(int64(cmd.Process.Pid))
		}
		err = cmd.Wait()
	}
	for _, b := range []*agentLimitedBuffer{stdout, stderr} {
		if b.over {
			return nil, fmt.Errorf("run_as_user: %s %s as the agent user: the %s of the command is over the limit of %d bytes, the command was stopped", op, path, b.name, b.limit)
		}
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == agentFileMissingExit {
			return nil, &fs.PathError{Op: op, Path: path, Err: fs.ErrNotExist}
		}
		return nil, fmt.Errorf("run_as_user: %s %s as the agent user: %w: %s", op, path, err, strings.TrimSpace(stderr.buf.String()))
	}
	return stdout.buf.Bytes(), nil
}

// agentPidRecorderKey is the context key of an *atomic.Int64 that
// runAgentFileScriptLimit sets to the process id of its command once it
// started: a caller that gives up the wait (boundGateRead) can name it.
type agentPidRecorderKey struct{}

// errAgentOutputLimit is the error of a write to an agentLimitedBuffer that
// is over its limit: os/exec stops the copy of the stream.
var errAgentOutputLimit = errors.New("output over the limit")

// agentLimitedBuffer keeps up to limit bytes of one output stream of a helper
// command. The write that goes over the limit calls stop (the Cancel of
// agentCommand, which kills the process group) before it returns: the kill
// never races the command's own exit. Each stream is written by its own copy
// goroutine of os/exec and read after Wait.
type agentLimitedBuffer struct {
	name  string
	limit int
	stop  func()
	buf   bytes.Buffer
	over  bool
}

func (b *agentLimitedBuffer) Write(p []byte) (int, error) {
	if b.over {
		return 0, errAgentOutputLimit
	}
	if b.buf.Len()+len(p) > b.limit {
		b.over = true
		b.buf.Reset()
		b.stop()
		return 0, errAgentOutputLimit
	}
	return b.buf.Write(p)
}

// agentUmask is the umask under which a new file or directory gets the
// permission bits of mode, as the os call gives it under umask 0.
func agentUmask(mode os.FileMode) string {
	return "0" + strconv.FormatUint(uint64(0o777&^mode.Perm()), 8)
}

// agentModeArg is the permission bits of mode as an octal chmod argument.
func agentModeArg(mode os.FileMode) string {
	return "0" + strconv.FormatUint(uint64(mode.Perm()), 8)
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
	_, err = runAgentFileScript(ctx, "write", path, agentWriteScript, data, path, agentUmask(mode), agentModeArg(mode))
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

// agentReadRegularFile is agentReadFile for a path that must be a regular
// file: as the agent user the test of the file type and the read are one
// command on one open of the path, and a FIFO or a device at the path is an
// error, never a read that waits for a writer (the open of a FIFO waits until
// the time limit of ctx). At most limit bytes are read: a larger file is an
// error. Outside the workspace root it is os.ReadFile.
func agentReadRegularFile(ctx context.Context, path string, limit int) ([]byte, error) {
	agent, err := agentFileRoute(path)
	if err != nil {
		return nil, err
	}
	if !agent {
		return os.ReadFile(path) // #nosec G304 -- the os call of today; workspace paths take the agent route.
	}
	return runAgentFileScriptLimit(ctx, "read", path, agentReadRegularScript, nil, limit, path)
}

// agentLstatMode is os.Lstat(path).Mode(), as the agent user for a path
// inside the workspace root: the type and the permission bits of the path
// itself, a link is not followed.
func agentLstatMode(ctx context.Context, path string) (os.FileMode, error) {
	agent, err := agentFileRoute(path)
	if err != nil {
		return 0, err
	}
	if !agent {
		fi, err := os.Lstat(path)
		if err != nil {
			return 0, err
		}
		return fi.Mode(), nil
	}
	out, err := runAgentFileScript(ctx, "lstat", path, agentLstatScript, nil, path)
	if err != nil {
		return 0, err
	}
	mode, ok := parseLsMode(string(out))
	if !ok {
		return 0, fmt.Errorf("run_as_user: lstat %s as the agent user: no mode in %q", path, strings.TrimSpace(string(out)))
	}
	return mode, nil
}

// parseLsMode reads the type and the permission bits from the first ten
// characters of an `ls -l` line ("-rwxr-xr-x ...").
func parseLsMode(line string) (os.FileMode, bool) {
	if len(line) < 10 {
		return 0, false
	}
	var mode os.FileMode
	switch line[0] {
	case '-':
	case 'd':
		mode = os.ModeDir
	case 'l':
		mode = os.ModeSymlink
	case 'p':
		mode = os.ModeNamedPipe
	case 's':
		mode = os.ModeSocket
	case 'c':
		mode = os.ModeDevice | os.ModeCharDevice
	case 'b':
		mode = os.ModeDevice
	default:
		mode = os.ModeIrregular
	}
	for i, c := range line[1:10] {
		bit := os.FileMode(1) << (8 - i)
		switch {
		case c == '-', c == 'S', c == 'T':
		case c == rune("rwx"[i%3]), i%3 == 2 && (c == 's' || c == 't'):
			mode |= bit
		default:
			return 0, false
		}
	}
	return mode, true
}

// agentRecreateFile removes path (a file, a link or an empty directory) and
// creates a new regular file with data and the permission bits of mode, as the
// agent user for a path inside the workspace root. The create never follows a
// link: a path that is there again is an error. Outside the root it is
// os.Remove and an exclusive create of the os package.
func agentRecreateFile(ctx context.Context, path string, data []byte, mode os.FileMode) error {
	agent, err := agentFileRoute(path)
	if err != nil {
		return err
	}
	if agent {
		if data == nil {
			data = []byte{}
		}
		_, err = runAgentFileScript(ctx, "recreate", path, agentRecreateScript, data, path, agentUmask(mode), agentModeArg(mode))
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm()) // #nosec G304 -- the path of a mutation line, confined to the task directory; O_EXCL never follows a link.
	if err != nil {
		return err
	}
	if err := f.Chmod(mode.Perm()); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
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

// evidenceWriteFile is the write of the mutate and revert steps of an
// evidence command: agentWriteFile, as the agent user for a path on the agent
// side, else os.WriteFile.
func evidenceWriteFile(ctx context.Context, path string, data []byte, mode os.FileMode) error {
	return agentWriteFile(ctx, path, data, mode)
}

// loadRepoWorkflow is workflow.Load(executionPath). With the option set the
// file is read through agentReadFile and the bytes are parsed: a link the
// agent put at .pilot/workflow.yaml is followed as the agent user, and its
// content goes into the prompt and the hook list. A file that does not exist
// gives (nil, nil), as Load.
func loadRepoWorkflow(ctx context.Context, executionPath string) (*workflow.Workflow, error) {
	if runAsSettingsNow() == nil {
		return workflow.Load(executionPath)
	}
	filePath := filepath.Join(executionPath, workflow.FilePath)
	data, err := agentReadFile(ctx, filePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("workflow: read %s: %w", filePath, err)
	}
	return workflow.Parse(data, filePath)
}
