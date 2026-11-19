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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/qf-studio/pilot/internal/procgroup"
	"github.com/qf-studio/pilot/internal/quality"
)

// TASK-651 (design: .agent/system/agent-isolation-design.md): with
// claude_code.run_as_user set, the quality gates (the gate commands, git and
// go list of the change scope) run as the agent user with the allowlisted
// environment, and the reads of the task directory (the Makefile, the marker
// files, the scope reads) run as the agent user too. With the option empty
// every method is what the default builder of internal/quality does.

// gateReadTimeout bounds each read of the task directory as the agent user:
// internal/quality gives the scope reads and the command detection no time
// limit of their own, so a read that does not return (a FIFO) would block a
// run before any gate limit.
const gateReadTimeout = 10 * time.Second

// The scripts run as `sh -c SCRIPT sh PATH` as the agent user; exit 3 is a
// path that does not exist (agentFileMissingExit).
const (
	// Prints the type of the file at $1, links followed, as os.Stat.
	gateStatScript = `[ -e "$1" ] || exit 3
if [ -d "$1" ]; then echo d; elif [ -f "$1" ]; then echo f; elif [ -p "$1" ]; then echo p; else echo o; fi`
	// Prints "TYPE NAME\0" for each entry of the directory $1, the type of the
	// entry itself (a link is l), as os.ReadDir.
	gateReadDirScript = `[ -e "$1" ] || exit 3
cd -- "$1" || exit 1
for f in * .[!.]* ..?*; do
  [ -e "$f" ] || [ -L "$f" ] || continue
  if [ -L "$f" ]; then t=l; elif [ -d "$f" ]; then t=d; elif [ -f "$f" ]; then t=f; elif [ -p "$f" ]; then t=p; else t=o; fi
  printf '%s %s\0' "$t" "$f"
done`
	// Prints $1 with every link resolved, as filepath.EvalSymlinks.
	gateEvalSymlinksScript = `[ -e "$1" ] || exit 3; exec realpath -- "$1"`
)

// gateCommandBuilder is the quality.CommandBuilder of the executor.
type gateCommandBuilder struct{}

var _ quality.CommandBuilder = gateCommandBuilder{}

var registerGateCommandBuilderOnce sync.Once

// registerGateCommandBuilder sets gateCommandBuilder as the builder of
// internal/quality, once per process.
func registerGateCommandBuilder() {
	registerGateCommandBuilderOnce.Do(func() { quality.SetCommandBuilder(gateCommandBuilder{}) })
}

// Build returns the gate command. With the option set it runs through sudo as
// the agent user (agentCommand) with the allowlist applied to env, after the
// model-env deny list: a gate runs code the agent wrote and needs no model
// credential. A nil env is the environment of the daemon, as for the default.
func (gateCommandBuilder) Build(ctx context.Context, dir string, env []string, name string, args ...string) (*exec.Cmd, error) {
	if runAsSettingsNow() == nil {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		cmd.Env = env
		return cmd, nil
	}
	cmd, err := agentCommand(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	if cmd == nil {
		return nil, errors.New("run_as_user: no gate command was built")
	}
	cmd.Dir = dir
	if env == nil {
		env = os.Environ()
	}
	var kept []string
	for _, kv := range env {
		if n, _, _ := strings.Cut(kv, "="); !isModelEnvDenied(n) {
			kept = append(kept, kv)
		}
	}
	setAgentEnvIn(cmd, kept, dir)
	return cmd, nil
}

// Configure sets the wait delay of a command built as the agent user:
// agentCommand made its session and its cancel (the kill through sudo).
// Any other command gets the process group of the default.
func (b gateCommandBuilder) Configure(cmd *exec.Cmd, waitDelay time.Duration) {
	if _, ok := agentCmdUser(cmd); ok {
		cmd.WaitDelay = waitDelay
		return
	}
	procgroup.Configure(cmd, waitDelay)
	cmd.Cancel = func() error {
		return b.Kill(cmd, syscall.SIGKILL)
	}
}

// Kill signals the process group of cmd, through sudo for a command of the
// agent user.
func (gateCommandBuilder) Kill(cmd *exec.Cmd, sig syscall.Signal) error {
	return killProcessGroup(cmd, sig)
}

func (gateCommandBuilder) ReadFile(path string) ([]byte, error) {
	if runAsSettingsNow() == nil {
		return os.ReadFile(path) // #nosec G304 -- the read of the default builder.
	}
	ctx, cancel := context.WithTimeout(context.Background(), gateReadTimeout)
	defer cancel()
	return agentReadFile(ctx, path)
}

func (gateCommandBuilder) Stat(path string) (os.FileInfo, error) {
	if runAsSettingsNow() == nil {
		return os.Stat(path)
	}
	out, agent, err := runGateRead("stat", path, gateStatScript)
	if err != nil {
		return nil, err
	}
	if !agent {
		return os.Stat(path)
	}
	return gateFileInfo{name: filepath.Base(path), mode: gateFileMode(strings.TrimSpace(string(out)))}, nil
}

func (gateCommandBuilder) ReadDir(path string) ([]os.DirEntry, error) {
	if runAsSettingsNow() == nil {
		return os.ReadDir(path)
	}
	out, agent, err := runGateRead("readdir", path, gateReadDirScript)
	if err != nil {
		return nil, err
	}
	if !agent {
		return os.ReadDir(path)
	}
	var entries []os.DirEntry
	for _, rec := range bytes.Split(out, []byte{0}) {
		kind, name, ok := strings.Cut(string(rec), " ")
		if !ok || name == "" {
			continue
		}
		entries = append(entries, gateFileInfo{name: name, mode: gateFileMode(kind)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func (gateCommandBuilder) EvalSymlinks(path string) (string, error) {
	if runAsSettingsNow() == nil {
		return filepath.EvalSymlinks(path)
	}
	out, agent, err := runGateRead("realpath", path, gateEvalSymlinksScript)
	if err != nil {
		return "", err
	}
	if !agent {
		return filepath.EvalSymlinks(path)
	}
	resolved := strings.TrimSuffix(string(out), "\n")
	if resolved == "" {
		return "", fmt.Errorf("run_as_user: realpath %s as the agent user: no output", path)
	}
	return resolved, nil
}

// runGateRead runs script for path as the agent user within gateReadTimeout
// when path takes the agent route of the file helpers (agentFileRoute);
// agent is false for a path outside the workspace root, which the caller
// reads with the os call.
func runGateRead(op, path, script string) (out []byte, agent bool, err error) {
	agent, err = agentFileRoute(path)
	if err != nil || !agent {
		return nil, agent, err
	}
	abs, err := absCleanPath(path)
	if err != nil {
		return nil, true, fmt.Errorf("run_as_user: %s %q: %w", op, path, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gateReadTimeout)
	defer cancel()
	out, err = runAgentFileScript(ctx, op, path, script, nil, abs)
	return out, true, err
}

// gateFileMode is the mode of a type letter of the scripts above.
func gateFileMode(kind string) fs.FileMode {
	switch kind {
	case "d":
		return fs.ModeDir | 0o755
	case "f":
		return 0o644
	case "l":
		return fs.ModeSymlink | 0o777
	case "p":
		return fs.ModeNamedPipe | 0o644
	}
	return fs.ModeIrregular
}

// gateFileInfo is the file info of a read as the agent user: the name and the
// type, no size and no time (internal/quality reads neither). It is also the
// directory entry of ReadDir.
type gateFileInfo struct {
	name string
	mode fs.FileMode
}

func (i gateFileInfo) Name() string               { return i.name }
func (i gateFileInfo) Size() int64                { return 0 }
func (i gateFileInfo) Mode() fs.FileMode          { return i.mode }
func (i gateFileInfo) ModTime() time.Time         { return time.Time{} }
func (i gateFileInfo) IsDir() bool                { return i.mode.IsDir() }
func (i gateFileInfo) Sys() any                   { return nil }
func (i gateFileInfo) Type() fs.FileMode          { return i.mode.Type() }
func (i gateFileInfo) Info() (fs.FileInfo, error) { return i, nil }
