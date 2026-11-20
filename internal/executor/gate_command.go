package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
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

// gateReadGrace is how long a read waits past gateReadTimeout for the kill of
// the agent route to end it (boundGateRead).
const gateReadGrace = time.Second

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

// registerGateCommandBuilder sets gateCommandBuilder as the builder of
// internal/quality.
func registerGateCommandBuilder() { quality.SetCommandBuilder(gateCommandBuilder{}) }

// errGateNoAgentBuilder is the error of a gate start with the option set while
// the builder of internal/quality is not gateCommandBuilder: the gates would
// run as the daemon user with its full environment.
var errGateNoAgentBuilder = errors.New("run_as_user: the quality gates have no agent command builder")

// gateBuilderError returns errGateNoAgentBuilder when the option is set and
// the builder of internal/quality is not gateCommandBuilder, nil otherwise.
func gateBuilderError() error {
	if runAsSettingsNow() == nil {
		return nil
	}
	if _, ok := quality.CurrentCommandBuilder().(gateCommandBuilder); !ok {
		return errGateNoAgentBuilder
	}
	return nil
}

// GateBuilderError is gateBuilderError for the quality checker factory of the
// daemon (cmd/pilot): with the option set and any builder but the gate
// builder, the detection of the build and test command would read the
// workspace as the daemon user.
func GateBuilderError() error { return gateBuilderError() }

// refusedGateChecker is the checker of a gate start whose command detection
// was refused (gateBuilderError): it returns the error and runs nothing.
type refusedGateChecker struct{ err error }

func (c refusedGateChecker) Check(context.Context) (*QualityOutcome, error) { return nil, c.err }

// runAsGateChecker is a QualityChecker that refuses to start the gates on
// gateBuilderError: the registration of the builder does not depend on one
// call (PreflightRunAsUser) that can be skipped in silence.
type runAsGateChecker struct{ QualityChecker }

func (c runAsGateChecker) Check(ctx context.Context) (*QualityOutcome, error) {
	if err := gateBuilderError(); err != nil {
		return nil, err
	}
	return c.QualityChecker.Check(ctx)
}

// guardGateChecker wraps the checker of a gate start in runAsGateChecker; a
// nil checker stays nil.
func guardGateChecker(c QualityChecker) QualityChecker {
	if c == nil {
		return nil
	}
	return runAsGateChecker{c}
}

// Build returns the gate command. With the option set it runs through sudo as
// the agent user (agentCommand) with the allowlist applied to env, after the
// model-env deny list: a gate runs code the agent wrote and needs no model
// credential, and no header of the model requests (ANTHROPIC_CUSTOM_HEADERS).
// A nil env is the environment of the daemon, as for the default. A command
// that is not a sudo command of the agent user is an error, never a command
// that would run as the daemon user.
func (gateCommandBuilder) Build(ctx context.Context, dir string, env []string, name string, args ...string) (*exec.Cmd, error) {
	s := runAsSettingsNow()
	if s == nil {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		cmd.Env = env
		return cmd, nil
	}
	cmd, err := agentCommand(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	if err := agentSudoCommandError(cmd, s, "gate"); err != nil {
		return nil, err
	}
	cmd.Dir = dir
	setAgentEnvFrom(cmd, gateEnv(env), dir, false)
	return cmd, nil
}

// gateEnv is env of a gate command before the allowlist: a nil env is the
// environment of the daemon; the model-env deny list, gateEnvDenied and the
// gh guard names (a gate does not call gh) are removed.
func gateEnv(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	// The names of runAsCodeSetEnv leave the copy of the daemon by position:
	// an env built from scratch keeps every entry the caller set, also one
	// with the value of the daemon.
	daemonCopy := daemonEnvCopyLen(env)
	var kept []string
	for i, kv := range env {
		n, _, _ := strings.Cut(kv, "=")
		if isModelEnvDenied(n) || gateEnvDenied[n] || runAsGhGuardEnv[n] || (i < daemonCopy && runAsCodeSetEnv[n]) {
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}

// gateEnvDenied lists the names the allowlist keeps for the model that a gate
// command does not get.
var gateEnvDenied = map[string]bool{"ANTHROPIC_CUSTOM_HEADERS": true}

// runAsGateToolsCheck is the probe of the preflight for the gate environment
// of the agent user (checkRunAsGateTools); tests replace it.
var runAsGateToolsCheck = checkRunAsGateTools

// runAsGateToolsTimeout bounds the gate tool probe; tests replace it.
var runAsGateToolsTimeout = 10 * time.Second

// gateToolsScript runs as `sh -c SCRIPT sh TOOL...` as the agent user with the
// environment of a gate: it prints "missing TOOL" for the first tool not found
// in PATH (mktemp, which the probe itself uses, included), or "tmpdir DIR" when
// no file can be made in the temporary directory. The probe file has a name
// mktemp picks, so no file or link the agent user made before can stand in its
// place; the trap removes it at the exit of the script (GH-6629).
const gateToolsScript = `for t in "$@" mktemp; do command -v "$t" >/dev/null 2>&1 || { echo "missing $t"; exit 4; }; done; ` +
	`d=${TMPDIR:-/tmp}; f=; trap '[ -z "$f" ] || rm -f -- "$f"' EXIT; ` +
	`f=$(mktemp "$d/.pilot-gate-probe.XXXXXXXXXX" 2>/dev/null) || { f=; echo "tmpdir $d"; exit 5; }; exit 0`

// gateToolsStderrMax bounds the line of the standard error of sudo that the
// refusal of a failed probe command holds.
const gateToolsStderrMax = 200

// checkRunAsGateTools runs gateToolsScript as the agent user of s with the
// environment a gate gets (PATH and TMPDIR of the daemon) within
// runAsGateToolsTimeout. The tools are sh and git, and make and go when the
// daemon finds them in its own PATH: an entry the agent user cannot read or a
// TMPDIR it cannot write fails every gate of every run. It returns the failed
// check, "" when the probe passed.
func checkRunAsGateTools(s *runAsSettings) string {
	tools := []string{"sh", "git"}
	for _, t := range []string{"make", "go"} {
		if _, err := exec.LookPath(t); err == nil {
			tools = append(tools, t)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), runAsGateToolsTimeout)
	defer cancel()
	cmd, err := agentCommandAs(ctx, s, "sh", append([]string{"-c", gateToolsScript, "sh"}, tools...)...)
	if err != nil {
		return fmt.Sprintf("run_as_user: the gate tool probe as the agent user %s: %v", s.user, err)
	}
	cmd.Dir = s.home
	setAgentEnvFrom(cmd, gateEnv(nil), s.home, false)
	out, err := cmd.Output()
	line := strings.TrimSpace(string(out))
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fmt.Sprintf("run_as_user: the gate tool probe as the agent user %s failed: no answer in %v seconds", s.user, runAsGateToolsTimeout.Seconds())
	case strings.HasPrefix(line, "missing "):
		return fmt.Sprintf("run_as_user: the agent user %s finds no %s in the PATH of a gate", s.user, strings.TrimPrefix(line, "missing "))
	case strings.HasPrefix(line, "tmpdir "):
		return fmt.Sprintf("run_as_user: the agent user %s cannot make a file in the temporary directory %s of a gate", s.user, strings.TrimPrefix(line, "tmpdir "))
	case err != nil:
		msg := fmt.Sprintf("run_as_user: the gate tool probe as the agent user %s failed: %v", s.user, err)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			first, _, _ := strings.Cut(strings.TrimSpace(string(exitErr.Stderr)), "\n")
			if first = truncateUTF8(strings.TrimSpace(first), gateToolsStderrMax); first != "" {
				msg += ": " + first
			}
		}
		return msg
	}
	return ""
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
	return boundGateRead("ReadFile", "read", path, func(ctx context.Context) ([]byte, error) {
		return agentReadFile(ctx, path)
	})
}

func (gateCommandBuilder) Stat(path string) (os.FileInfo, error) {
	if runAsSettingsNow() == nil {
		return os.Stat(path)
	}
	out, agent, err := runGateRead("Stat", "stat", path, gateStatScript)
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
	out, agent, err := runGateRead("ReadDir", "readdir", path, gateReadDirScript)
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
	out, agent, err := runGateRead("EvalSymlinks", "realpath", path, gateEvalSymlinksScript)
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
// reads with the os call. method is the builder method that reads.
func runGateRead(method, op, path, script string) (out []byte, agent bool, err error) {
	agent, err = agentFileRoute(path)
	if err != nil || !agent {
		return nil, agent, err
	}
	abs, err := absCleanPath(path)
	if err != nil {
		return nil, true, fmt.Errorf("run_as_user: %s %q: %w", op, path, err)
	}
	out, err = boundGateRead(method, op, path, func(ctx context.Context) ([]byte, error) {
		return runAgentFileScript(ctx, op, path, script, nil, abs)
	})
	return out, true, err
}

// boundGateRead runs read with a context of gateReadTimeout and returns after
// gateReadTimeout plus gateReadGrace at most: when the kill through sudo does
// not end the process, the wait of os/exec lasts the wait delay and the kill
// grace on top of the limit. It then stops waiting and returns the time limit
// error; the process is left to the kill route, with one Error line that
// names the builder method, the path and the process id.
func boundGateRead(method, op, path string, read func(ctx context.Context) ([]byte, error)) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gateReadTimeout)
	var pid atomic.Int64
	ctx = context.WithValue(ctx, agentPidRecorderKey{}, &pid)
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer cancel()
		out, err := read(ctx)
		done <- result{out, err}
	}()
	timer := time.NewTimer(gateReadTimeout + gateReadGrace)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.out, r.err
	case <-timer.C:
		slog.Default().Error("run_as_user: a read of the gate builder was given up, its kill through sudo did not end the process",
			slog.String("method", method),
			slog.String("path", path),
			slog.Int64("pid", pid.Load()),
		)
		return nil, fmt.Errorf("run_as_user: %s %s as the agent user: %w", op, path, context.DeadlineExceeded)
	}
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
