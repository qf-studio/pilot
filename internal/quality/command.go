package quality

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"time"

	"github.com/qf-studio/pilot/internal/procgroup"
)

// CommandBuilder creates and controls the processes the package runs in a
// task directory (the gate commands, git and go list of the scope) and reads
// the files of that directory. The default runs them as the daemon; with
// run_as_user the executor sets one that runs them as the agent user, which
// this package cannot import (GH-6549).
type CommandBuilder interface {
	// Build returns a command for name and args that runs in dir. A nil env
	// inherits the environment of the daemon.
	Build(ctx context.Context, dir string, env []string, name string, args ...string) (*exec.Cmd, error)
	// Configure prepares cmd before Start: its process group, the wait delay
	// on its output pipes, and cmd.Cancel, so a cancelled context ends cmd
	// through Kill.
	Configure(cmd *exec.Cmd, waitDelay time.Duration)
	// Kill sends sig to cmd and the processes it started.
	Kill(cmd *exec.Cmd, sig syscall.Signal) error
	// ReadFile reads the file at path.
	ReadFile(path string) ([]byte, error)
	// Stat returns the file info of path, following symlinks.
	Stat(path string) (os.FileInfo, error)
	// ReadDir lists the directory at path.
	ReadDir(path string) ([]os.DirEntry, error)
	// EvalSymlinks returns path with every symlink resolved.
	EvalSymlinks(path string) (string, error)
}

// errNoCommand is the result of a nil builder or of a Build that returns
// neither a command nor an error (GH-6555).
var errNoCommand = errors.New("quality: the command builder gave no command")

// defaultCommandBuilder runs commands as the daemon itself.
type defaultCommandBuilder struct{}

func (defaultCommandBuilder) Build(ctx context.Context, dir string, env []string, name string, args ...string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd, nil
}

func (b defaultCommandBuilder) Configure(cmd *exec.Cmd, waitDelay time.Duration) {
	procgroup.Configure(cmd, waitDelay)
	cmd.Cancel = func() error {
		return b.Kill(cmd, syscall.SIGKILL)
	}
}

func (defaultCommandBuilder) Kill(cmd *exec.Cmd, sig syscall.Signal) error {
	return procgroup.Kill(cmd, sig)
}

func (defaultCommandBuilder) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (defaultCommandBuilder) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func (defaultCommandBuilder) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

func (defaultCommandBuilder) EvalSymlinks(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

var (
	commandBuilderMu sync.RWMutex
	commandBuilder   CommandBuilder = defaultCommandBuilder{}
)

// SetCommandBuilder sets the builder of the package; nil, or a typed nil,
// sets the default.
func SetCommandBuilder(b CommandBuilder) {
	if isNilBuilder(b) {
		b = defaultCommandBuilder{}
	}
	commandBuilderMu.Lock()
	commandBuilder = b
	commandBuilderMu.Unlock()
}

// CurrentCommandBuilder returns the builder of the package.
func CurrentCommandBuilder() CommandBuilder {
	commandBuilderMu.RLock()
	defer commandBuilderMu.RUnlock()
	return commandBuilder
}

// isNilBuilder reports whether b is nil or an interface holding a nil value.
func isNilBuilder(b CommandBuilder) bool {
	if b == nil {
		return true
	}
	v := reflect.ValueOf(b)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return v.IsNil()
	}
	return false
}

// buildCommand builds a command with b; a nil b or a nil command is
// errNoCommand, not a panic later in Configure or Run.
func buildCommand(b CommandBuilder, ctx context.Context, dir string, env []string, name string, args ...string) (*exec.Cmd, error) {
	if isNilBuilder(b) {
		return nil, errNoCommand
	}
	cmd, err := b.Build(ctx, dir, env, name, args...)
	if err != nil {
		return nil, err
	}
	if cmd == nil {
		return nil, errNoCommand
	}
	return cmd, nil
}

// The reads of the task directory: each goes through the current builder, a
// nil builder reads nothing.

func readTaskFile(path string) ([]byte, error) {
	b := CurrentCommandBuilder()
	if isNilBuilder(b) {
		return nil, errNoCommand
	}
	return b.ReadFile(path)
}

func statTaskFile(path string) (os.FileInfo, error) {
	b := CurrentCommandBuilder()
	if isNilBuilder(b) {
		return nil, errNoCommand
	}
	return b.Stat(path)
}

func readTaskDir(path string) ([]os.DirEntry, error) {
	b := CurrentCommandBuilder()
	if isNilBuilder(b) {
		return nil, errNoCommand
	}
	return b.ReadDir(path)
}

func evalTaskSymlinks(path string) (string, error) {
	b := CurrentCommandBuilder()
	if isNilBuilder(b) {
		return "", errNoCommand
	}
	return b.EvalSymlinks(path)
}
