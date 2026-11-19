package quality

import (
	"context"
	"os"
	"os/exec"
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
}

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

var (
	commandBuilderMu sync.RWMutex
	commandBuilder   CommandBuilder = defaultCommandBuilder{}
)

// SetCommandBuilder sets the builder of the package; nil sets the default.
func SetCommandBuilder(b CommandBuilder) {
	if b == nil {
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
