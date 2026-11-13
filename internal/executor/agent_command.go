package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
)

// TASK-651 (design: .agent/system/agent-isolation-design.md, 4.1 and 4.3):
// with claude_code.run_as_user set, every model process starts as that Unix
// user through sudo. With the option empty nothing here changes behaviour:
// agentCommand is exec.CommandContext and setAgentEnv assigns cmd.Env as is.

// runAsTimeout bounds the preflight sudo probe and the sudo kill.
const runAsTimeout = 5 * time.Second

// runAsWaitDelay bounds cmd.Wait after the cancel of a command made by
// agentCommand with the option set.
const runAsWaitDelay = 10 * time.Second

// runAsPreserveFlag is the sudo flag that carries the environment names.
const runAsPreserveFlag = "--preserve-env="

// runAsSettings is the resolved run_as_user option: what the spawn sites need
// at every start, resolved once.
type runAsSettings struct {
	user  string
	group string
	home  string // home of the agent user
	gid   int    // gid of group, -1 when no group is set
}

// runAsState is the process-wide state of the option: the settings and the
// fail-closed flag of the preflight.
type runAsState struct {
	settings *runAsSettings // nil: option empty
	failed   string         // name of the failed preflight check, empty: none
	checked  bool           // the preflight ran for these settings
	backend  string         // backend type the preflight ran for
}

var (
	runAsMu    sync.RWMutex
	runAsCur   runAsState
	runAsGOOS  = runtime.GOOS
	lookupUser = user.Lookup
	lookupGrp  = user.LookupGroup
	currentUID = func() (string, error) {
		u, err := user.Current()
		if err != nil {
			return "", err
		}
		return u.Uid, nil
	}
	// daemonGroups returns the group ids of the daemon process: chown to a
	// group works only for a group the process is a member of.
	daemonGroups = func() ([]int, error) {
		gids, err := os.Getgroups()
		if err != nil {
			return nil, err
		}
		return append(gids, os.Getegid()), nil
	}
	// runAsSnapshots holds the settings one command was built with, from
	// agentCommand until setAgentEnv takes them: a reload in between cannot
	// give the command a preserve list that differs from cmd.Env.
	runAsSnapshots sync.Map // *exec.Cmd -> *runAsSettings
)

// runAsAllowedEnv lists the names a model process keeps when it runs as the
// agent user. Everything else, the GitHub names included, is dropped: the
// GitHub token for the agent comes in a later leg of TASK-651. The gh guard
// names are kept separately (runAsGhGuardEnv).
var runAsAllowedEnv = map[string]bool{
	"PATH": true, "LANG": true, "LC_ALL": true, "TERM": true, "TZ": true, "TMPDIR": true,
	"GOCACHE": true, "GOMODCACHE": true, "GOFLAGS": true, "GOTOOLCHAIN": true,
	"GOPROXY": true, "GONOSUMDB": true, "GOPRIVATE": true,
	"NODE_OPTIONS": true, "PILOT_EXECUTOR": true,
}

// runAsGhGuardEnv lists the names the gh guard shim hands to the `pilot
// gh-guard` process inside the agent: the task identity, the real gh path, the
// shim directory (ghguard.ResolveFallbackGh excludes it from the PATH search)
// and the denial journal. PATH keeps the shim directory first, so dropping any
// of these breaks the guard: with the shim directory lost the fallback search
// finds the shim itself and every allowed gh call re-enters it.
var runAsGhGuardEnv = map[string]bool{
	ghguard.EnvTaskIssue: true, ghguard.EnvTaskRepo: true, ghguard.EnvTaskBranch: true,
	ghguard.EnvRealGh: true, ghguard.EnvShimDir: true, ghguard.EnvJournalPath: true,
}

// runAsAllowedEnvPrefixes are the prefixes the allowlist keeps: how the CLI
// is configured, routed and billed.
var runAsAllowedEnvPrefixes = []string{"CLAUDE_", "ANTHROPIC_"}

// runAsSettingsNow returns the current settings, nil when the option is empty.
func runAsSettingsNow() *runAsSettings {
	runAsMu.RLock()
	defer runAsMu.RUnlock()
	return runAsCur.settings
}

// runAsSpawnError is the error every model spawn returns while the preflight
// flag is set, nil otherwise.
func runAsSpawnError() error {
	runAsMu.RLock()
	defer runAsMu.RUnlock()
	if runAsCur.failed == "" {
		return nil
	}
	return fmt.Errorf("run_as_user preflight failed: %s", runAsCur.failed)
}

// RunAsUserPreflightError returns the error every model spawn gets right now:
// "run_as_user preflight failed: <check>", or nil when the option is empty or
// its preflight passed.
func RunAsUserPreflightError() error { return runAsSpawnError() }

// sudoArgs is the sudo prefix shared by the spawn, the probe and the kill.
func (s *runAsSettings) sudoArgs() []string {
	args := []string{"-n", "-u", s.user}
	if s.group != "" {
		args = append(args, "-g", s.group)
	}
	return args
}

// validRunAsName reports whether name is a safe user or group name: a to z or
// an underscore first, then a to z, 0 to 9, underscore or dash, at most 32
// characters. It keeps option-like and shell-like values ("-u root", "#0",
// "a b") out of the sudo argv before any lookup.
func validRunAsName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c == '_':
		case i > 0 && (c >= '0' && c <= '9' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// backendRunsToolsInProcess reports whether a backend type executes the tool
// calls the model chose inside the daemon process, as the daemon user: the
// two API backends (execBash in backend_anthropic.go, shared by the OpenAI
// one). The CLI backends spawn the model through agentCommand.
func backendRunsToolsInProcess(backendType string) bool {
	switch backendType {
	case BackendTypeAnthropicAPI, BackendTypeOpenAIAPI:
		return true
	}
	return false
}

// resolveRunAs resolves user, group and home once with os/user. The returned
// check name is empty on success.
func resolveRunAs(userName, group string) (*runAsSettings, string) {
	s := &runAsSettings{user: userName, group: group, gid: -1}
	u, err := lookupUser(userName)
	if err != nil || u == nil {
		return s, "user exists"
	}
	s.home = u.HomeDir
	if u.Uid == "0" {
		return s, "run_as_user has uid 0"
	}
	g, err := lookupGrp(group)
	if err != nil || g == nil {
		return s, "group exists"
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return s, "group exists"
	}
	if gid == 0 {
		return s, "run_as_group has gid 0"
	}
	s.gid = gid
	uid, err := currentUID()
	if err != nil || uid == u.Uid {
		return s, "user differs from the daemon user"
	}
	gids, err := daemonGroups()
	if err != nil {
		return s, "daemon user is a member of the group"
	}
	member := false
	for _, id := range gids {
		member = member || id == gid
	}
	if !member {
		return s, "daemon user is a member of the group"
	}
	return s, ""
}

// PreflightRunAsUser reads claude_code.run_as_user / run_as_group, resolves
// them and checks the host. backendType is the configured executor backend
// ("" is claude-code). The checks: the names are valid, not root, run_as_group
// is set, the backend does not run model-chosen commands in the daemon
// process, Linux, user and group exist with uid and gid not 0, the user is not
// the daemon user, the daemon user is a member of the group, `sudo -n -u <user>
// -g <group> true` exits 0 within 5 seconds, and the home of the user exists.
// On any failure it logs one Error line naming the check and sets the
// process-wide flag: every model spawn then returns an error and no process
// starts. It never falls back to a run as the daemon user. With the option
// empty it clears any earlier state and does nothing else. Called at the same
// place as the read guard config, for every entrypoint that spawns.
func PreflightRunAsUser(cfg *ClaudeCodeConfig, backendType string) {
	var userName, group string
	if cfg != nil {
		userName, group = strings.TrimSpace(cfg.RunAsUser), strings.TrimSpace(cfg.RunAsGroup)
	}
	if userName == "" {
		runAsMu.Lock()
		runAsCur = runAsState{}
		runAsMu.Unlock()
		return
	}

	runAsMu.RLock()
	same := runAsCur.checked && runAsCur.settings != nil && runAsCur.backend == backendType &&
		runAsCur.settings.user == userName && runAsCur.settings.group == group
	runAsMu.RUnlock()
	if same {
		return
	}

	s := &runAsSettings{user: userName, group: group, gid: -1}
	var failed string
	switch {
	case !validRunAsName(userName) || userName == "root":
		failed = "run_as_user name is not valid"
	case group == "":
		failed = "run_as_group is required with run_as_user"
	case !validRunAsName(group):
		failed = "run_as_group name is not valid"
	case backendRunsToolsInProcess(backendType):
		failed = "run_as_user is not supported with the " + backendType + " backend"
	case runAsGOOS != "linux":
		failed = "linux only"
	default:
		s, failed = resolveRunAs(userName, group)
		switch {
		case failed != "":
		case !sudoProbeOK(s):
			failed = "sudo -n -u " + userName + " -g " + group + " true"
		case !dirExists(s.home):
			failed = "home exists"
		}
	}
	if failed != "" {
		slog.Default().Error("run_as_user preflight failed: model processes will not start",
			slog.String("check", failed),
			slog.String("run_as_user", userName),
		)
	}
	runAsMu.Lock()
	runAsCur = runAsState{settings: s, failed: failed, checked: true, backend: backendType}
	runAsMu.Unlock()
}

// sudoProbeOK runs `sudo -n -u <user> [-g <group>] true` with the 5 second
// bound and reports whether it exited 0.
func sudoProbeOK(s *runAsSettings) bool {
	ctx, cancel := context.WithTimeout(context.Background(), runAsTimeout)
	defer cancel()
	args := append(s.sudoArgs(), "true")
	return exec.CommandContext(ctx, "sudo", args...).Run() == nil
}

func dirExists(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// agentCommand is the constructor of every model process. With the option
// empty it is exec.CommandContext(ctx, name, args...), no field changed. With
// it set (Linux) it builds `sudo -n -u <user> [-g <group>] --preserve-env=<names>
// -- <name> <args...>`; no environment value is ever placed in argv, and the
// caller sets the environment with setAgentEnv so the names and cmd.Env always
// match. It also puts the command in its own process group and makes the
// cancel of the context kill that group through sudo (SIGKILL, WaitDelay 10
// seconds): the default cancel of os/exec signals only the sudo process, which
// the daemon user may not signal, and the model would outlive the timeout. A
// site that sets its own Cancel after the call keeps it. While the preflight
// flag is set it returns an error and builds nothing.
func agentCommand(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	if err := runAsSpawnError(); err != nil {
		return nil, err
	}
	s := runAsSettingsNow()
	if s == nil || runAsGOOS != "linux" {
		return exec.CommandContext(ctx, name, args...), nil
	}
	argv := append(s.sudoArgs(), runAsPreserveFlag+strings.Join(agentEnvNames(s.envBase()), ","), "--", name)
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, "sudo", argv...)
	configureProcessGroup(cmd)
	cmd.Cancel = func() error { return killAgentProcessGroup(cmd, s.user, syscall.SIGKILL) }
	cmd.WaitDelay = runAsWaitDelay
	runAsSnapshots.Store(cmd, s)
	return cmd, nil
}

// envBase is the part of the environment the agent user always gets.
func (s *runAsSettings) envBase() []string {
	return []string{"HOME=" + s.home, "CLAUDE_CONFIG_DIR=" + filepath.Join(s.home, ".claude")}
}

// agentEnvNames returns the names of the entries in env, in order.
func agentEnvNames(env []string) []string {
	names := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	return names
}

func runAsEnvAllowed(name string) bool {
	if runAsAllowedEnv[name] || runAsGhGuardEnv[name] {
		return true
	}
	for _, p := range runAsAllowedEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// agentEnvFor applies the allowlist to env (the result of the deny-list scrub
// plus what the caller appended): allowlisted names only, HOME and
// CLAUDE_CONFIG_DIR of the agent user, one entry per name, the last one wins.
func (s *runAsSettings) agentEnvFor(env []string) []string {
	last := map[string]string{}
	var order []string
	add := func(kv string) {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			return
		}
		if _, seen := last[name]; !seen {
			order = append(order, name)
		}
		last[name] = kv
	}
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); runAsEnvAllowed(name) && name != "CLAUDE_CONFIG_DIR" {
			add(kv)
		}
	}
	for _, kv := range s.envBase() {
		add(kv)
	}
	out := make([]string, 0, len(order))
	for _, name := range order {
		out = append(out, last[name])
	}
	return out
}

// setAgentEnv sets cmd.Env for a command made by agentCommand. With the option
// empty (or cmd built without sudo) it assigns env unchanged. With the option
// set it assigns the allowlisted env and writes the names of those entries
// into the --preserve-env flag.
func setAgentEnv(cmd *exec.Cmd, env []string) {
	if _, ok := agentCmdUser(cmd); !ok {
		cmd.Env = env
		return
	}
	// The settings agentCommand built this command with, not the current ones.
	snap, found := runAsSnapshots.LoadAndDelete(cmd)
	s, _ := snap.(*runAsSettings)
	if !found || s == nil {
		s = runAsSettingsNow()
	}
	if s == nil {
		// cmd was built under settings that are gone; keep it closed.
		cmd.Env = env
		return
	}
	out := s.agentEnvFor(env)
	cmd.Env = out
	for i, a := range cmd.Args {
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, runAsPreserveFlag) {
			cmd.Args[i] = runAsPreserveFlag + strings.Join(agentEnvNames(out), ",")
			return
		}
	}
}

// agentCmdUser returns the agent user of a command made by agentCommand with
// the option set: argv is `sudo -n -u <user> ...`.
func agentCmdUser(cmd *exec.Cmd) (string, bool) {
	if cmd == nil || len(cmd.Args) < 4 || cmd.Args[0] != "sudo" || cmd.Args[1] != "-n" || cmd.Args[2] != "-u" {
		return "", false
	}
	return cmd.Args[3], true
}

// signalName is the name `kill -s` takes.
func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGKILL:
		return "KILL"
	case syscall.SIGTERM:
		return "TERM"
	case syscall.SIGINT:
		return "INT"
	case syscall.SIGHUP:
		return "HUP"
	}
	return strconv.Itoa(int(sig))
}

// agentKillCommand builds `sudo -n -u <user> kill -s <signal> -- -<pgid>`.
func agentKillCommand(ctx context.Context, agentUser string, pgid int, sig syscall.Signal) *exec.Cmd {
	return exec.CommandContext(ctx, "sudo", "-n", "-u", agentUser, "kill", "-s", signalName(sig), "--", "-"+strconv.Itoa(pgid))
}

// killAgentProcessGroup signals the process group of an agent command through
// sudo: the daemon user may not signal the agent user or sudo itself. The
// group id is the pid of the sudo process (configureProcessGroup). One Error
// line with the pgid when it fails; there is no direct fallback, it would
// only fail the same way.
func killAgentProcessGroup(cmd *exec.Cmd, agentUser string, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	pgid := cmd.Process.Pid
	ctx, cancel := context.WithTimeout(context.Background(), runAsTimeout)
	defer cancel()
	if out, err := agentKillCommand(ctx, agentUser, pgid, sig).CombinedOutput(); err != nil {
		slog.Default().Error("run_as_user kill through sudo failed",
			slog.Int("pgid", pgid),
			slog.String("run_as_user", agentUser),
			slog.String("signal", signalName(sig)),
			slog.Any("error", err),
			slog.String("output", strings.TrimSpace(string(out))),
		)
		return fmt.Errorf("sudo kill of process group %d: %w: %w", pgid, errAgentKillLogged, err)
	}
	return nil
}

// errAgentKillLogged marks the error of a failed sudo kill: killAgentProcessGroup
// has already logged it at Error.
var errAgentKillLogged = errors.New("failure logged")

// killFailureLevel is the level a caller logs a failed kill at: Error, except
// for a failed sudo kill, which killAgentProcessGroup logged at Error already
// (one Error line per failed kill).
func killFailureLevel(err error) slog.Level {
	if errors.Is(err, errAgentKillLogged) {
		return slog.LevelWarn
	}
	return slog.LevelError
}

// guardChown is os.Chown; tests replace it to fail a change of group.
var guardChown = os.Chown

// errRunAsGuardPerm is wrapped around a failed chown/chmod of guard files.
var errRunAsGuardPerm = errors.New("run_as_user: guard files not readable by the agent")

// applyGuardPerm opens a guard directory (0750) or file (0640, scripts 0750)
// to the group of the agent user: with the option set it sets the mode and,
// with a run_as_group, the group (the owner stays the daemon user, a member of
// the group). A failure is an error that stops the spawn. With the option
// empty it does nothing: the modes stay 0700 and 0600 (scripts 0755).
func applyGuardPerm(path string, mode os.FileMode) error {
	s := runAsSettingsNow()
	if s == nil {
		return nil
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("%w: %s: chmod: %v", errRunAsGuardPerm, path, err)
	}
	if s.gid >= 0 {
		if err := guardChown(path, -1, s.gid); err != nil {
			return fmt.Errorf("%w: %s: chown: %v", errRunAsGuardPerm, path, err)
		}
	}
	return nil
}
