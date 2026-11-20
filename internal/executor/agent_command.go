package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
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
	"github.com/qf-studio/pilot/internal/procgroup"
	"github.com/qf-studio/pilot/internal/quality"
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

// runAsKillGrace is how long killAgentProcessGroup waits, after a failed
// sudo kill, for the process to be gone before it reports the failure.
const runAsKillGrace = 2 * time.Second

// runAsTTYCheck is the preflight check that fails when sudo gives the agent a
// terminal: with `Defaults use_pty` sudo then runs the agent in a session of
// its own, outside the group of the sudo process, and the kill misses it.
const runAsTTYCheck = "run_as_user: sudo gives the agent a terminal (use_pty); the kill would not reach it"

// runAsTTYProbe prints the terminal of the probe shell: "?" when it has none.
const runAsTTYProbe = "ps -o tty= -p $$"

// runAsSettings is the resolved run_as_user option: what the spawn sites need
// at every start, resolved once.
type runAsSettings struct {
	user  string
	group string
	home  string // home of the agent user
	uid   string // uid of the agent user, the owner of the workspace root
	gid   int    // gid of group, -1 when no group is set
	// workspaceRoot is run_as_workspace_dir, defaultRunAsWorkspaceDir when
	// empty: a git command in a directory below it runs as the agent user
	// (gitCommand).
	workspaceRoot string
	// dataDir is run_as_data_dir, defaultRunAsDataDir when empty: the data
	// directory of the daemon that checkRunAsDataDirModes reads.
	dataDir string
	// gatewayPort is the gateway port of the config that checkRunAsFirewall
	// probes; 0 skips that part of the probe.
	gatewayPort int
	// gatewayHost is the gateway host of the config: it decides the
	// addresses on which checkRunAsFirewall probes the gateway port
	// (gatewayProbeAddrs).
	gatewayHost string
	// gatewayRequired is RunAsGatewayRequired: set by the daemon start, where
	// a gateway port of 0 or below is a refusal.
	gatewayRequired bool
}

// runAsState is the process-wide state of the option: the settings and the
// fail-closed flag of the preflight.
type runAsState struct {
	settings *runAsSettings // nil: option empty
	failed   string         // name of the failed preflight check, empty: none
	checked  bool           // the preflight ran for these settings
	backend  string         // backend type the preflight ran for
	// useWorktree and poolSize are the worktree settings the preflight ran for.
	useWorktree bool
	poolSize    int
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
	// execCommandContext is exec.CommandContext; tests wrap it to see the
	// command every spawn site ends up with.
	execCommandContext = exec.CommandContext
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

// runAsCodeSetEnv lists the names only the code sets: the task id and the
// change scope of the quality gates. The value the daemon process holds for
// one of them never reaches the agent user (dropDaemonCodeSetEnv); a value
// the caller put into the environment on purpose does.
var runAsCodeSetEnv = map[string]bool{
	"PILOT_TASK_ID":            true,
	quality.EnvChangedPackages: true, quality.EnvChangedFull: true, quality.EnvChangedReason: true,
}

// runAsGhGuardEnv lists the names the gh guard shim hands to the `pilot
// gh-guard` process inside the agent: the task identity, the real gh path, the
// shim directory (ghguard.ResolveFallbackGh excludes it from the PATH search)
// and the denial journal. PATH keeps the shim directory first, so dropping any
// of these breaks the guard: with the shim directory lost the fallback search
// finds the shim itself and every allowed gh call re-enters it. The path of
// the read token file of the run (agentTokenFile) passes too, never the token.
var runAsGhGuardEnv = map[string]bool{
	ghguard.EnvTaskIssue: true, ghguard.EnvTaskRepo: true, ghguard.EnvTaskBranch: true,
	ghguard.EnvRealGh: true, ghguard.EnvShimDir: true, ghguard.EnvJournalPath: true,
	ghguard.EnvTokenFile: true,
}

// runAsAllowedEnvPrefixes are the prefixes the allowlist keeps: how the CLI
// is configured, routed and billed.
var runAsAllowedEnvPrefixes = []string{"CLAUDE_", "ANTHROPIC_"}

// runAsDevNull is the null device every sudo command of the option reads as
// standard input when the caller sets none. Opened once, never closed: each
// child gets its own copy of the descriptor.
var runAsDevNull = sync.OnceValues(func() (*os.File, error) { return os.Open(os.DevNull) })

// detachSudoCommand starts cmd in a new session, without a controlling
// terminal, with standard input from the null device unless the caller set
// one. sudo then has no terminal to hand to the agent, so even with
// `Defaults use_pty` it runs the agent in its own process group, and pid,
// process group and session of the sudo process are equal: the kill of the
// negative pid reaches the agent.
//
// Decision: Setsid, not Setpgid (setsid fails for a group leader). A sudo
// with use_pty and a controlling terminal (a daemon in tmux) runs the command
// in a new session under a second sudo; the kill of the first group then
// fails and the agent lives on. Without a terminal sudo uses no pty.
func detachSudoCommand(cmd *exec.Cmd) error {
	procgroup.ConfigureSession(cmd)
	if cmd.Stdin == nil {
		devNull, err := runAsDevNull()
		if err != nil {
			return fmt.Errorf("run_as_user: open %s: %w", os.DevNull, err)
		}
		cmd.Stdin = devNull
	}
	return nil
}

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
// two API backends (executeTool in backend_anthropic.go, shared by the OpenAI
// one). The CLI backends spawn the model through agentCommand.
func backendRunsToolsInProcess(backendType string) bool {
	switch backendType {
	case BackendTypeAnthropicAPI, BackendTypeOpenAIAPI:
		return true
	}
	return false
}

// claudeCode returns the claude_code block of c, nil when c is nil.
func (c *BackendConfig) claudeCode() *ClaudeCodeConfig {
	if c == nil {
		return nil
	}
	return c.ClaudeCode
}

// runAsBackendError returns "run_as_user is not supported with the <type>
// backend" when the option is set and backendType runs the tools of the model
// inside the daemon process, nil otherwise. The option counts as set when cfg
// names a run_as_user (a blank value included) or the preflight ran for one.
// The API backends call it before any request and before any tool, and
// NewBackend before it builds one, so no tool runs as the daemon user.
func runAsBackendError(backendType string, cfg *ClaudeCodeConfig) error {
	if !backendRunsToolsInProcess(backendType) {
		return nil
	}
	if (cfg == nil || cfg.RunAsUser == "") && runAsSettingsNow() == nil {
		return nil
	}
	return fmt.Errorf("run_as_user is not supported with the %s backend", backendType)
}

// resolveRunAs resolves user, group and home once with os/user. The returned
// check name is empty on success.
func resolveRunAs(userName, group string) (*runAsSettings, string) {
	s := &runAsSettings{user: userName, group: group, gid: -1}
	u, err := lookupUser(userName)
	if err != nil || u == nil {
		return s, "user exists"
	}
	s.home, s.uid = u.HomeDir, u.Uid
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
// ("" is claude-code). The checks: neither value is blank (white space only),
// the names are valid, not root, run_as_group is set, the backend does not run
// model-chosen commands in the daemon process, GOPROXY of the daemon (and a
// GOPROXY= in its GOFLAGS) holds no credentials, Linux, user and group exist
// with uid and gid not 0, the user is not the daemon user, the daemon user is
// a member of the group, `sudo -n -u <user> -g <group> true` exits 0 within 5
// seconds, the agent gets no terminal from sudo, and the home of the user
// exists. After the worktree checks below, the agent user with the
// environment of a gate finds sh, git, and make and go when the daemon finds
// them, and can make a file in its temporary directory (checkRunAsGateTools). Before all of them the refusal of runAsIncompleteRefusal fails
// every host, and before that a daemon start (RunAsGatewayRequired) with a
// gateway port of 0 or below is refused (runAsGatewayPortCheck). Then the workspace (TASK-651 4.2): use_worktree is true,
// worktree_pool_size is 0, the workspace root is not /, the home of the daemon
// user, the data directory of the daemon or a parent of one of them, and the
// host checks of checkRunAsWorkspaceRoot pass. Last, the modes of the data
// directory of the daemon (checkRunAsDataDirModes) and, as the last probe,
// the firewall of the agent user (checkRunAsFirewall). On any failure it logs one Error line naming the check and sets the
// process-wide flag: every model spawn then returns an error and no process
// starts, and the API backends run no request and no tool. It never falls
// back to a run as the daemon user. A passed result is kept for the same
// settings; a failed one is not, and the next call runs the checks again.
// With the option empty it clears any earlier state and does nothing else. Called at the same place as the read
// guard config, for every entrypoint that spawns.
func PreflightRunAsUser(cfg *ClaudeCodeConfig, backendType string, useWorktree bool, worktreePoolSize int) {
	// The gates run through the builder of the executor; with the option
	// empty it is the default builder of internal/quality.
	registerGateCommandBuilder()
	var rawUser, rawGroup, rawRoot, rawDataDir string
	var gatewayPort int
	var gatewayHost string
	var gatewayRequired bool
	if cfg != nil {
		rawUser, rawGroup, rawRoot, rawDataDir = cfg.RunAsUser, cfg.RunAsGroup, cfg.RunAsWorkspaceDir, cfg.RunAsDataDir
		gatewayPort, gatewayHost, gatewayRequired = cfg.RunAsGatewayPort, cfg.RunAsGatewayHost, cfg.RunAsGatewayRequired
	}
	userName, group := strings.TrimSpace(rawUser), strings.TrimSpace(rawGroup)
	root := strings.TrimSpace(rawRoot)
	if root == "" {
		root = defaultRunAsWorkspaceDir
	}
	dataDir := strings.TrimSpace(rawDataDir)
	if dataDir == "" {
		dataDir = defaultRunAsDataDir
	}
	// A value of white space only is a mistake, not an empty option: the
	// model would run as the daemon user without a word.
	var blank string
	switch {
	case rawUser != "" && userName == "":
		blank = "run_as_user is blank"
	case rawGroup != "" && group == "":
		blank = "run_as_group is blank"
	}
	if userName == "" && blank == "" {
		runAsMu.Lock()
		runAsCur = runAsState{}
		runAsMu.Unlock()
		return
	}

	// Only a passed result is kept: a failed one may come from a state of
	// the host that has changed since (a port held by a daemon that stopped).
	runAsMu.RLock()
	same := blank == "" && runAsCur.checked && runAsCur.failed == "" && runAsCur.settings != nil && runAsCur.backend == backendType &&
		runAsCur.settings.user == userName && runAsCur.settings.group == group &&
		runAsCur.settings.workspaceRoot == root && runAsCur.settings.dataDir == dataDir &&
		runAsCur.settings.gatewayPort == gatewayPort && runAsCur.settings.gatewayHost == gatewayHost &&
		runAsCur.settings.gatewayRequired == gatewayRequired &&
		runAsCur.useWorktree == useWorktree && runAsCur.poolSize == worktreePoolSize
	runAsMu.RUnlock()
	if same {
		return
	}

	s := &runAsSettings{user: userName, group: group, gid: -1}
	var failed string
	switch {
	case blank != "":
		failed = blank
	// Decision: a config check, so it comes before the general refusal and
	// the host checks: the daemon start names this cause on every host
	// (GH-6480).
	case gatewayRequired && gatewayPort <= 0:
		failed = runAsGatewayPortCheck
	case runAsIncompleteRefusal:
		failed = runAsIncompleteCheck
	case !validRunAsName(userName) || userName == "root":
		failed = "run_as_user name is not valid"
	case group == "":
		failed = "run_as_group is required with run_as_user"
	case !validRunAsName(group):
		failed = "run_as_group name is not valid"
	case !filepath.IsAbs(root):
		failed = "run_as_workspace_dir is not an absolute path"
	case !filepath.IsAbs(dataDir):
		failed = "run_as_data_dir is not an absolute path"
	case backendRunsToolsInProcess(backendType):
		failed = "run_as_user is not supported with the " + backendType + " backend"
	case goProxyHasCredentials(runAsGetenv("GOPROXY")):
		failed = runAsGoProxyCheck
	case goFlagsProxyHasCredentials(runAsGetenv("GOFLAGS")):
		failed = runAsGoFlagsProxyCheck
	case runAsGOOS != "linux":
		failed = "linux only"
	default:
		s, failed = resolveRunAs(userName, group)
		switch {
		case failed != "":
		case !sudoProbeOK(s):
			failed = "sudo -n -u " + userName + " -g " + group + " true"
		case !sudoNoTerminal(s):
			failed = runAsTTYCheck
		case !dirExists(s.home):
			failed = "home exists"
		case !useWorktree:
			failed = "run_as_user requires use_worktree"
		case worktreePoolSize > 0:
			failed = "run_as_user requires worktree_pool_size 0"
		default:
			s.workspaceRoot, s.dataDir, s.gatewayPort, s.gatewayHost = root, dataDir, gatewayPort, gatewayHost
			// The tools and the temporary directory of a gate, as the agent
			// user with the environment of a gate (checkRunAsGateTools).
			failed = runAsGateToolsCheck(s)
			if failed == "" {
				failed = refusedRunAsWorkspaceRoot(root, dataDir, cfg)
			}
			if failed == "" {
				failed = runAsWorkspaceCheck(s)
			}
			if failed == "" {
				failed = runAsDataDirCheck(s)
			}
			if failed == "" {
				failed = runAsFirewallCheck(s)
			}
		}
	}
	s.workspaceRoot, s.dataDir, s.gatewayPort, s.gatewayHost = root, dataDir, gatewayPort, gatewayHost
	s.gatewayRequired = gatewayRequired
	if failed != "" {
		slog.Default().Error("run_as_user preflight failed: no model process and no tool of the model will run",
			slog.String("check", failed),
			slog.String("run_as_user", userName),
		)
	}
	runAsMu.Lock()
	runAsCur = runAsState{settings: s, failed: failed, checked: true, backend: backendType,
		useWorktree: useWorktree, poolSize: worktreePoolSize}
	runAsMu.Unlock()
}

// runAsIncompleteRefusal makes PreflightRunAsUser fail for every host while
// the option is set. Until the hand-over leg and the gates leg of TASK-651
// merge, processes of the daemon user still run inside the agent-owned
// workspace: the workflow hooks and quality gates of the runner, the gate
// commands of internal/quality and its inline git command (scope.go). The
// gates leg removes the refusal. Tests of the package switch it off
// (TestMain).
var runAsIncompleteRefusal = true

// runAsGatewayPortCheck is the failed check of a daemon start with run_as_user
// set and a gateway port of 0 or below: the firewall probe cannot check the
// gateway port (GH-6480).
const runAsGatewayPortCheck = "run_as_user needs a gateway port for the firewall probe"

// runAsIncompleteCheck is the failed check of the refusal.
const runAsIncompleteCheck = "run_as_user cannot be set yet: the hand-over leg and the gates leg of TASK-651 are not merged"

// runAsGetenv is os.Getenv for the Go proxy check of the preflight; tests
// replace it.
var runAsGetenv = os.Getenv

// Decision (founder, 2026-11-19): GOPROXY, GOFLAGS, GONOSUMDB and GOPRIVATE
// of the daemon pass to the agent user (runAsAllowedEnv), so a proxy URL with
// credentials in them would hand the credentials to the model. The start is
// refused instead; the checks name the variable and never print its value.
const (
	runAsGoProxyCheck      = "GOPROXY of the daemon holds credentials: a proxy with credentials cannot pass to the agent user"
	runAsGoFlagsProxyCheck = "GOPROXY in GOFLAGS of the daemon holds credentials: a proxy with credentials cannot pass to the agent user"
)

// goProxyHasCredentials reports whether a GOPROXY value, a list with the
// separators comma and pipe, holds an element that parses as a URL with a
// user part (a name, or a name and a password). The elements direct and off
// and an element that does not parse are not credentials.
func goProxyHasCredentials(value string) bool {
	for _, elem := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '|' }) {
		elem = strings.TrimSpace(elem)
		if elem == "" || elem == "direct" || elem == "off" {
			continue
		}
		u, err := url.Parse(elem)
		if err != nil || u.User == nil {
			continue
		}
		if _, hasPassword := u.User.Password(); u.User.Username() != "" || hasPassword {
			return true
		}
	}
	return false
}

// goFlagsProxyHasCredentials checks, with goProxyHasCredentials, the text
// after each GOPROXY= in a GOFLAGS value up to the next space.
func goFlagsProxyHasCredentials(flags string) bool {
	const key = "GOPROXY="
	for rest := flags; ; {
		i := strings.Index(rest, key)
		if i < 0 {
			return false
		}
		rest = rest[i+len(key):]
		value := rest
		if j := strings.IndexByte(rest, ' '); j >= 0 {
			value = rest[:j]
		}
		if goProxyHasCredentials(value) {
			return true
		}
	}
}

// daemonHomeDir is os.UserHomeDir; tests replace it.
var daemonHomeDir = os.UserHomeDir

// refusedRunAsWorkspaceRoot returns the failed check when root is /, the home
// of the daemon user, the data directory of the daemon (the Pilot home and
// the directory of the loaded config file) or a parent of one of them: a
// removal as the agent user below the root must never reach daemon files.
// Root may not be dataDir (run_as_data_dir) or a parent of it either: the
// mode check of checkRunAsDataDirModes would then skip nothing or everything;
// that refusal names both paths. Paths are compared as given and resolved.
func refusedRunAsWorkspaceRoot(root, dataDir string, cfg *ClaudeCodeConfig) string {
	const check = "run_as_workspace_dir is /, the daemon home, the daemon data directory or a parent of one of them"
	var protected []string
	home, err := daemonHomeDir()
	if err != nil || home == "" {
		return "run_as_workspace_dir: the home of the daemon user cannot be resolved"
	}
	protected = append(protected, home, filepath.Join(home, ".pilot"))
	if cfg != nil && cfg.PilotHome != "" {
		protected = append(protected, cfg.PilotHome)
	}
	if cfg != nil && cfg.ConfigPath != "" {
		protected = append(protected, filepath.Dir(cfg.ConfigPath))
	}
	roots := pathForms(root)
	if len(roots) == 0 {
		return check
	}
	for _, r := range roots {
		if r == string(filepath.Separator) {
			return check
		}
		for _, p := range protected {
			for _, pf := range pathForms(p) {
				if pathInside(r, pf) {
					return check
				}
			}
		}
	}
	for _, r := range roots {
		for _, d := range pathForms(dataDir) {
			if pathInside(r, d) {
				return fmt.Sprintf("run_as_workspace_dir %s is run_as_data_dir %s or a parent of it", root, dataDir)
			}
		}
	}
	return ""
}

// runAsWorkspaceCheck is checkRunAsWorkspaceRoot; tests that stub the host
// replace it.
var runAsWorkspaceCheck = checkRunAsWorkspaceRoot

// runAsWorkspaceProbeName is the prefix of the probe directory of the
// preflight; it never matches a workspace name (agentWorkspacePrefix).
const runAsWorkspaceProbeName = "pilot-preflight-probe-"

// checkRunAsWorkspaceRoot checks the workspace root on the host: it exists,
// the agent user owns it, the daemon user can list it, and a directory that
// the agent user creates in it (a probe, removed again) can be listed by the
// daemon user. The returned check name is empty on success.
func checkRunAsWorkspaceRoot(s *runAsSettings) string {
	root := s.workspaceRoot
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		return "workspace root exists"
	}
	if owner, ok := fileOwnerUID(fi); !ok || owner != s.uid {
		return "workspace root is owned by the agent user"
	}
	if _, err := os.ReadDir(root); err != nil {
		return "daemon user can list the workspace root"
	}
	probe := filepath.Join(root, runAsWorkspaceProbeName+strconv.FormatInt(time.Now().UnixNano(), 10))
	run := func(name string, args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), runAsTimeout)
		defer cancel()
		cmd, err := agentCommandAs(ctx, s, name, args...)
		if err != nil {
			return err
		}
		setAgentEnv(cmd, gitAgentBaseEnv())
		return cmd.Run()
	}
	const probeCheck = "daemon user can list a directory the agent user creates in the workspace root"
	if err := run("mkdir", "--", probe); err != nil {
		return probeCheck
	}
	_, listErr := os.ReadDir(probe)
	rmErr := run("rmdir", "--", probe)
	if listErr != nil {
		return probeCheck
	}
	if rmErr != nil {
		return "agent user can remove its probe directory in the workspace root"
	}
	return ""
}

// runAsDataDirCheck is checkRunAsDataDirModes for the preflight; tests that
// stub the host replace it. The creation of an agent workspace calls
// checkRunAsDataDirModes itself.
var runAsDataDirCheck = checkRunAsDataDirModes

// checkRunAsDataDirModes checks, and only reads, the modes of the data
// directory of the daemon with the rule of step_daemon_private in
// scripts/box/setup-agent-user.sh: the data directory gives the group and
// other users no read and no write bit; each top-level entry gives other
// users no bit, and gives its group no bit when the group is run_as_group.
// The entry that holds the workspace root (the agent root) is skipped. A link
// is resolved first, as `realpath -m` would (resolveLinkPath): a link out of
// the data directory, or one to the data directory, to the agent root or a
// path in it, fails for a look by hand, as in the script, whatever the mode of
// its target and whether or not the target exists (the agent user may create a
// missing one). Any other link is checked by its target (os.Stat); a dangling
// one reaches nothing and is skipped. A data directory that does not exist
// fails. The returned check names the first entry that fails and its mode;
// empty on success.
//
// Decision: the preflight result is kept for the life of the process, and the
// daemon creates entries at run time (logs, state files) with mode 0755, so
// createAgentWorkspace runs the check again before each run.
func checkRunAsDataDirModes(s *runAsSettings) string {
	dir := s.dataDir
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return "run_as_data_dir exists: " + dir
	}
	if perm := fi.Mode().Perm(); perm&0o066 != 0 {
		return fmt.Sprintf("run_as_data_dir %s gives the group or other users read or write (mode %04o)", dir, perm)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "daemon user can list run_as_data_dir " + dir
	}
	dirForms := pathForms(dir)
	holders := map[string]bool{}
	for _, d := range dirForms {
		for _, r := range pathForms(s.workspaceRoot) {
			if rel, err := filepath.Rel(d, r); err == nil && rel != "." && pathInside(d, r) {
				first, _, _ := strings.Cut(rel, string(filepath.Separator))
				holders[first] = true
			}
		}
	}
	for _, e := range entries {
		if holders[e.Name()] {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if e.Type()&os.ModeSymlink != 0 {
			real, ok := resolveLinkPath(p)
			if !ok {
				return "run_as_data_dir entry " + p + ": the link cannot be resolved"
			}
			inside, reserved := false, false
			for _, d := range dirForms {
				if !pathInside(d, real) {
					continue
				}
				inside = true
				rel, _ := filepath.Rel(d, real)
				first, _, _ := strings.Cut(rel, string(filepath.Separator))
				if rel == "." || holders[first] {
					reserved = true
				}
			}
			if !inside {
				return fmt.Sprintf("run_as_data_dir entry %s is a link out of run_as_data_dir (to %s): look at it by hand", p, real)
			}
			if reserved {
				return fmt.Sprintf("run_as_data_dir entry %s resolves to run_as_data_dir or to the agent root or a path in it (%s): look at it by hand", p, real)
			}
		}
		efi, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "run_as_data_dir entry " + p + " cannot be read"
		}
		perm := efi.Mode().Perm()
		if perm&0o007 != 0 {
			return fmt.Sprintf("run_as_data_dir entry %s is open to other users (mode %04o)", p, perm)
		}
		gid, ok := fileOwnerGID(efi)
		if !ok {
			return "run_as_data_dir entry " + p + ": the group cannot be read"
		}
		if gid == s.gid && perm&0o070 != 0 {
			return fmt.Sprintf("run_as_data_dir entry %s is open to run_as_group %s (mode %04o)", p, s.group, perm)
		}
	}
	return ""
}

// runAsMaxLinkHops bounds the links resolveLinkPath follows, as the kernel
// does (ELOOP after 40).
const runAsMaxLinkHops = 40

// resolveLinkPath is `realpath -m` of the absolute path p: each part that
// exists is resolved, links included, and the missing rest is kept as named,
// so a dangling link gets the path it would reach. False when a link cannot be
// read, there are more than runAsMaxLinkHops links or p is not absolute.
func resolveLinkPath(p string) (string, bool) {
	if !filepath.IsAbs(p) {
		return "", false
	}
	sep := string(filepath.Separator)
	out := sep
	parts := strings.Split(p, sep)
	hops := 0
	for i := 0; i < len(parts); i++ {
		switch c := parts[i]; c {
		case "", ".":
			continue
		case "..":
			out = filepath.Dir(out)
			continue
		}
		next := filepath.Join(out, parts[i])
		fi, err := os.Lstat(next)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			out = next
			continue
		}
		if hops++; hops > runAsMaxLinkHops {
			return "", false
		}
		t, err := os.Readlink(next)
		if err != nil {
			return "", false
		}
		if filepath.IsAbs(t) {
			out = sep
		}
		parts = append(strings.Split(t, sep), parts[i+1:]...)
		i = -1
	}
	return out, true
}

// runAsFirewallCheck is checkRunAsFirewall for the preflight; tests that stub
// the host replace it.
var runAsFirewallCheck = checkRunAsFirewall

// agentDialResult is how one connection of the firewall probe ended.
type agentDialResult int

const (
	// agentDialFailed: the probe ran and its connection failed before the
	// time limit, as a reject rule with a TCP reset makes it fail.
	agentDialFailed agentDialResult = iota
	// agentDialConnected: the probe ran and its connection was made.
	agentDialConnected
	// agentDialNoAnswer: the connection ended by the time limit or by the
	// context, with no answer, after the start line of the probe
	// (runAsProbeStart) and before any marker line.
	agentDialNoAnswer
	// agentDialNotRun: the probe ended before the time limit without one
	// start line before one marker line (runAsProbeMarker), or by the time
	// limit without its start line, or wrote more than runAsProbeOutputLimit
	// bytes, or ended with its output held open: it never reported a
	// connection try, as when sudo fails or hangs for the agent user, bash is
	// missing or the probe is killed (GH-6480, GH-6516, GH-6530).
	agentDialNotRun
)

// runAsProbeMarker starts the one line the probe prints after its connection
// try, followed by the exit status of the redirect.
const runAsProbeMarker = "pilot-probe="

// runAsProbeStart is the line the probe prints before its connection try. It
// is not a status line of probeMarkerResult.
const runAsProbeStart = runAsProbeMarker + "start"

// runAsProbeOutputLimit bounds the output of one probe in bytes; a probe that
// writes more did not run (GH-6516).
const runAsProbeOutputLimit = 4096

// runAsAgentDial opens one TCP connection to addr as the agent user, bounded
// by timeout, and reports how it ended; tests replace it to stand for a
// reject rule.
var runAsAgentDial = agentDialAs

// runAsMetadataAddr is the instance metadata address that the firewall of
// scripts/box/setup-agent-user.sh closes to the agent user. Tests point it at
// a loopback port: the real one may answer on a cloud host.
var runAsMetadataAddr = "169.254.169.254:80"

// runAsDialTimeout bounds each connection of the firewall probe but the
// control one; tests shorten it.
var runAsDialTimeout = 3 * time.Second

// runAsControlDialTimeout bounds the control connection of the firewall
// probe. Tests do not shorten it: the control connection must connect, and a
// short limit on a slow host (mock sudo under -race) reads as a probe tool
// that does not work (GH-6477).
const runAsControlDialTimeout = 3 * time.Second

// runAsLocalIPv4s returns the local IPv4 addresses of the host that are
// neither loopback nor link-local, in the order of the interface list; tests
// replace it with a fake list.
var runAsLocalIPv4s = localIPv4s

// checkRunAsFirewall proves that the firewall rules of the agent user are
// active. All connections run as the agent user (runAsAgentDial). A control
// connection to a listener of the daemon on 127.0.0.1 must connect; then the
// connections to the metadata address and to the gateway port on each
// address of gatewayProbeAddrs must not, all of them in parallel. A probe
// that did not run (agentDialNotRun) proves nothing and is a refusal. With no
// gateway port (every entry point but the daemon start) the gateway part is
// skipped. The result is empty on success, else the failed check, which
// names the address or the port.
//
// Decision: the preflight runs before the gateway starts, and a closed port
// fails as a reject rule does, so the daemon listens on the gateway port for
// the time of the probe. A probe tool that does not work fails every
// connection too and would read as a blocked one; the control connection
// tells them apart, and the marker line of each probe tells a failed
// connection from a probe that never tried one (GH-6480). The rules of the
// host script reject the gateway port with a TCP reset, so a blocked
// connection to it fails at once: one with no answer by the time limit
// proves no rule (a slow sudo) and is a refusal. The metadata address is
// rejected without a reset, and a timeout there is a block.
func checkRunAsFirewall(s *runAsSettings) string {
	// A port above 65535 cannot be listened on; refuse it before the control
	// connection, which would make the refusal wait (GH-6516).
	if s.gatewayPort > 65535 {
		return fmt.Sprintf("firewall probe: the gateway port %d is above 65535, not a TCP port", s.gatewayPort)
	}
	control, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "firewall probe: the daemon cannot open a control listener on 127.0.0.1"
	}
	ok := runAsAgentDial(s, control.Addr().String(), runAsControlDialTimeout) == agentDialConnected
	_ = control.Close()
	if !ok {
		return "firewall probe: the agent user cannot connect to a control listener on 127.0.0.1, the probe tool (bash /dev/tcp) does not work"
	}

	var gateways []net.Listener
	defer func() {
		for _, l := range gateways {
			_ = l.Close()
		}
	}()
	if port := s.gatewayPort; port <= 0 {
		slog.Default().Debug("run_as_user firewall probe: no gateway port, the gateway port is not probed",
			slog.String("run_as_user", s.user))
	} else {
		addrs := gatewayProbeAddrs(s.gatewayHost)
		if len(addrs) == 1 && addrs[0] == "127.0.0.1" && !loopbackGatewayHost(s.gatewayHost) {
			slog.Default().Warn("run_as_user firewall probe: the gateway host is not a loopback address and the host has no non-loopback IPv4 address, the gateway port is probed on 127.0.0.1 only",
				slog.String("run_as_user", s.user),
				slog.String("gateway_host", s.gatewayHost))
		}
		for _, h := range addrs {
			l, err := net.Listen("tcp", net.JoinHostPort(h, strconv.Itoa(port)))
			if err != nil {
				return fmt.Sprintf("firewall probe: the daemon cannot listen on the gateway port %d on %s", port, h)
			}
			gateways = append(gateways, l)
		}
	}

	var wg sync.WaitGroup
	var metadata agentDialResult
	results := make([]agentDialResult, len(gateways))
	wg.Add(1)
	go func() {
		defer wg.Done()
		metadata = runAsAgentDial(s, runAsMetadataAddr, runAsDialTimeout)
	}()
	for i, l := range gateways {
		wg.Add(1)
		go func(i int, addr string) {
			defer wg.Done()
			results[i] = runAsAgentDial(s, addr, runAsDialTimeout)
		}(i, l.Addr().String())
	}
	wg.Wait()
	switch metadata {
	case agentDialConnected:
		return "firewall: the agent user reached the metadata address " + runAsMetadataAddr
	case agentDialNotRun:
		return "firewall probe: the probe of the metadata address " + runAsMetadataAddr + " did not run as the agent user"
	}
	for i, l := range gateways {
		host := l.Addr().(*net.TCPAddr).IP.String()
		switch results[i] {
		case agentDialConnected:
			return fmt.Sprintf("firewall: the agent user reached the gateway port %d on %s", s.gatewayPort, host)
		case agentDialNoAnswer:
			return fmt.Sprintf("firewall probe: no answer from the gateway port %d on %s within %s, a reject rule answers at once", s.gatewayPort, host, runAsDialTimeout)
		case agentDialNotRun:
			return fmt.Sprintf("firewall probe: the probe of the gateway port %d on %s did not run as the agent user", s.gatewayPort, host)
		}
	}
	return ""
}

// loopbackGatewayHost reports whether the gateway host binds loopback only:
// "localhost" or a loopback IP. An empty host binds every address.
func loopbackGatewayHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// gatewayProbeAddrs returns the addresses on which the probe listens on the
// gateway port (GH-6480). A host that is a literal IPv4 address is probed on
// that address alone: a local one is where the gateway listens (127.0.0.2
// and the other loopback addresses too), and for one that is not local the
// listen fails, which is the refusal. An empty host, a wildcard address or a
// host name gives 127.0.0.1 and each local IPv4 address that is neither
// loopback nor link-local (runAsLocalIPv4s). IPv6 addresses are not probed: an
// IPv6 host gives 127.0.0.1 only.
func gatewayProbeAddrs(host string) []string {
	ip := net.ParseIP(host)
	if ip4 := ip.To4(); ip4 != nil && !ip4.IsUnspecified() {
		return []string{ip4.String()}
	}
	addrs := []string{"127.0.0.1"}
	if ip != nil && !ip.IsUnspecified() {
		return addrs
	}
	return append(addrs, runAsLocalIPv4s()...)
}

// localIPv4s is localIPv4sOf over the interface list of the host, nil when
// it cannot be read.
func localIPv4s() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	return localIPv4sOf(addrs)
}

// localIPv4sOf returns the IPv4 addresses of addrs that are neither loopback
// nor link-local, in order. A link-local address (169.254.0.0/16) is
// skipped: no gateway is reached there (GH-6477).
func localIPv4sOf(addrs []net.Addr) []string {
	var out []string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			if ip4 := n.IP.To4(); ip4 != nil {
				out = append(out, ip4.String())
			}
		}
	}
	return out
}

// agentDialAs runs `bash --norc --noprofile -p -c 'echo pilot-probe=start; :
// 2>/dev/null </dev/tcp/HOST/PORT; echo pilot-probe=$?'` as the agent user
// with the agent environment, bounded by timeout, and reports how it ended:
// the marker with 0 is a connection, the marker with 1 a failed connection,
// an end by the time limit after the start line and with no marker no
// answer, and any other end a probe that did not run (agentDialNotRun). The
// output is read up to runAsProbeOutputLimit bytes, and what the probe wrote
// before a kill is kept. A host that is not an IP address or a port outside
// 1 to 65535 never reaches the script: the probe does not run.
//
// Decision: no answer by the time limit is the normal end of a blocked
// metadata connection, and a sudo that hangs ends the same way without a
// connection try; the start line tells them apart (GH-6516). A marker with 0
// read at the time limit is a connection that was made, from a sudo that
// ends late (GH-6530). --norc and --noprofile keep a file of the agent user
// from printing the marker; -p keeps BASH_ENV and an exported function from
// doing it. A process that keeps the output open after the probe ended makes
// Wait end by the wait delay (runAsProbeWaitDelay), not by the context.
func agentDialAs(s *runAsSettings, addr string, timeout time.Duration) agentDialResult {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) == nil {
		return agentDialNotRun
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return agentDialNotRun
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	script := "echo " + runAsProbeStart + "; : 2>/dev/null </dev/tcp/" + host + "/" + strconv.Itoa(port) + "; echo " + runAsProbeMarker + "$?"
	cmd, err := agentCommandAs(ctx, s, "bash", "--norc", "--noprofile", "-p", "-c", script)
	if err != nil {
		return agentDialNotRun
	}
	cmd.WaitDelay = runAsProbeWaitDelay
	setAgentEnv(cmd, gitAgentBaseEnv())
	var out probeOutput
	cmd.Stdout = &out
	err = cmd.Run()
	switch {
	case out.over, errors.Is(err, exec.ErrWaitDelay):
		return agentDialNotRun
	case ctx.Err() != nil:
		return probeTimeLimitResult(string(out.buf))
	case err != nil:
		return agentDialNotRun
	}
	return probeMarkerResult(string(out.buf))
}

// runAsProbeWaitDelay bounds cmd.Wait of one probe after its end or the
// cancel of its context.
const runAsProbeWaitDelay = 2 * time.Second

// errProbeOutputLimit ends the read of a probe that wrote more than
// runAsProbeOutputLimit bytes.
var errProbeOutputLimit = errors.New("firewall probe: output above the limit")

// probeOutput keeps the output of a probe up to runAsProbeOutputLimit bytes.
// A write past the limit sets over and fails, which ends the read.
type probeOutput struct {
	buf  []byte
	over bool
}

func (o *probeOutput) Write(p []byte) (int, error) {
	if len(o.buf)+len(p) > runAsProbeOutputLimit {
		o.over = true
		return 0, errProbeOutputLimit
	}
	o.buf = append(o.buf, p...)
	return len(p), nil
}

// probeLines reads the marker lines of a probe output: the number of start
// lines (runAsProbeStart, the whole line), the status texts of the other
// marker lines in order, and whether every status line comes after the
// first start line.
func probeLines(out string) (starts int, statuses []string, ordered bool) {
	ordered = true
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == runAsProbeStart {
			starts++
			continue
		}
		if v, ok := strings.CutPrefix(line, runAsProbeMarker); ok {
			if starts == 0 {
				ordered = false
			}
			statuses = append(statuses, v)
		}
	}
	return starts, statuses, ordered
}

// probeMarkerResult reads the output of a probe that exited 0: exactly one
// start line before exactly one marker line with the status 0 (connected)
// or 1 (failed), as bash prints it, else the probe did not run.
func probeMarkerResult(out string) agentDialResult {
	starts, statuses, ordered := probeLines(out)
	switch {
	case starts != 1 || len(statuses) != 1 || !ordered:
		return agentDialNotRun
	case statuses[0] == "0":
		return agentDialConnected
	case statuses[0] == "1":
		return agentDialFailed
	}
	return agentDialNotRun
}

// probeTimeLimitResult reads the output of a probe that ended by the time
// limit: exactly one start line and no marker line is no answer, a marker
// line read by probeMarkerResult as a connection is a connection, and
// anything else did not run (GH-6530).
func probeTimeLimitResult(out string) agentDialResult {
	starts, statuses, _ := probeLines(out)
	switch {
	case starts == 1 && len(statuses) == 0:
		return agentDialNoAnswer
	case probeMarkerResult(out) == agentDialConnected:
		return agentDialConnected
	}
	return agentDialNotRun
}

// sudoProbeOK runs `sudo -n -u <user> [-g <group>] true` with the 5 second
// bound, detached as every spawn is (detachSudoCommand), and reports whether
// it exited 0.
func sudoProbeOK(s *runAsSettings) bool {
	ctx, cancel := context.WithTimeout(context.Background(), runAsTimeout)
	defer cancel()
	cmd := execCommandContext(ctx, "sudo", append(s.sudoArgs(), "true")...)
	if detachSudoCommand(cmd) != nil {
		return false
	}
	return cmd.Run() == nil
}

// sudoNoTerminal runs `sh -c 'ps -o tty= -p $$'` as the agent user through
// the sudo argv and the detach of a spawn, and reports whether the shell had
// no controlling terminal ("?"). A sudo that still hands the agent a pty
// (use_pty with a terminal) runs it outside the group the kill signals. Any
// other result, an error or no output included, fails closed.
func sudoNoTerminal(s *runAsSettings) bool {
	ctx, cancel := context.WithTimeout(context.Background(), runAsTimeout)
	defer cancel()
	cmd := execCommandContext(ctx, "sudo", append(s.sudoArgs(), "sh", "-c", runAsTTYProbe)...)
	if detachSudoCommand(cmd) != nil {
		return false
	}
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	tty := strings.TrimSpace(string(out))
	return tty != "" && strings.Trim(tty, "?") == ""
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
// match. It also starts the command in a new session without a terminal,
// standard input from the null device (detachSudoCommand), and makes the
// cancel of the context kill that group through sudo (SIGKILL, WaitDelay 10
// seconds): the default cancel of os/exec signals only the sudo process, which
// the daemon user may not signal, and the model would outlive the timeout. A
// site that sets its own Cancel or Stdin after the call keeps it; a later
// configureProcessGroup keeps the session and the wait delay. While the
// preflight flag is set it returns an error and builds nothing.
func agentCommand(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	if err := runAsSpawnError(); err != nil {
		return nil, err
	}
	s := runAsSettingsNow()
	if s == nil || runAsGOOS != "linux" {
		return execCommandContext(ctx, name, args...), nil
	}
	return agentCommandAs(ctx, s, name, args...)
}

// agentCommandAs is the sudo form of agentCommand for the settings s, without
// the check of the preflight flag: the preflight builds its probe with it
// before the settings are stored, and a workspace cleanup with the settings
// the workspace was created with.
func agentCommandAs(ctx context.Context, s *runAsSettings, name string, args ...string) (*exec.Cmd, error) {
	argv := append(s.sudoArgs(), runAsPreserveFlag+strings.Join(agentEnvNames(s.envBase()), ","), "--", name)
	argv = append(argv, args...)
	cmd := execCommandContext(ctx, "sudo", argv...)
	if err := detachSudoCommand(cmd); err != nil {
		return nil, err
	}
	cmd.Cancel = func() error { return killAgentProcessGroup(cmd, s.user, syscall.SIGKILL) }
	cmd.WaitDelay = runAsWaitDelay
	runAsSnapshots.Store(cmd, s)
	return cmd, nil
}

// envBase is the part of the environment the agent user always gets: its
// home, its Claude config and the Go paths below its home that
// scripts/box/setup-agent-user.sh prepares (go, go/cache, go/mod). The
// daemon's values for these names never reach the agent (agentEnvFor).
func (s *runAsSettings) envBase() []string {
	goDir := filepath.Join(s.home, "go")
	return []string{
		"HOME=" + s.home, "CLAUDE_CONFIG_DIR=" + filepath.Join(s.home, ".claude"),
		"GOPATH=" + goDir, "GOCACHE=" + filepath.Join(goDir, "cache"), "GOMODCACHE=" + filepath.Join(goDir, "mod"),
	}
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
	if runAsAllowedEnv[name] || runAsGhGuardEnv[name] || runAsCodeSetEnv[name] {
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
// plus what the caller appended): allowlisted names only, the names of
// envBase with the values of the agent user, one entry per name, the last one
// wins.
func (s *runAsSettings) agentEnvFor(env []string) []string {
	base := s.envBase()
	own := make(map[string]bool, len(base))
	for _, name := range agentEnvNames(base) {
		own[name] = true
	}
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
		if name, _, _ := strings.Cut(kv, "="); runAsEnvAllowed(name) && !own[name] {
			add(kv)
		}
	}
	for _, kv := range base {
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
// set it assigns the allowlisted env plus the git identity of the agent user
// for the directory cmd.Dir (agentGitIdentityFor) and writes the names of
// those entries into the --preserve-env flag.
func setAgentEnv(cmd *exec.Cmd, env []string) {
	setAgentEnvIn(cmd, env, cmd.Dir)
}

// setAgentEnvIn is setAgentEnv for a command that runs in dir, for the sites
// that set cmd.Dir later or pass the directory in argv (git -C).
func setAgentEnvIn(cmd *exec.Cmd, env []string, dir string) {
	setAgentEnvFrom(cmd, env, dir, true)
}

// setAgentEnvFrom is setAgentEnvIn; dropDaemon says env starts from a copy of
// os.Environ() whose entries of runAsCodeSetEnv are still in it
// (dropDaemonCodeSetEnv removes them). A caller that removed them by position
// (gateCommandBuilder.Build) passes false.
func setAgentEnvFrom(cmd *exec.Cmd, env []string, dir string, dropDaemon bool) {
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
	if dropDaemon {
		env = dropDaemonCodeSetEnv(env)
	}
	out := lastEnvWins(append(s.agentEnvFor(env), agentGitIdentityFor(s, dir)...))
	cmd.Env = out
	setPreserveEnvNames(cmd, out)
}

// dropDaemonCodeSetEnv removes from env, for each name of runAsCodeSetEnv the
// daemon process holds, the first entry with the value of the daemon. env must
// start from a copy of os.Environ(), filtered or not, as at every caller of
// setAgentEnvIn: the copy of the daemon comes first and is the one removed, an
// entry the caller appended stays, also with the same value as the daemon. In
// an env built from scratch the first such entry is one the caller set and it
// would be lost; the gate builder, whose env comes from internal/quality,
// removes the copy by position instead (daemonEnvCopyLen).
func dropDaemonCodeSetEnv(env []string) []string {
	daemon := map[string]string{}
	for name := range runAsCodeSetEnv {
		if v, ok := os.LookupEnv(name); ok {
			daemon[name] = name + "=" + v
		}
	}
	if len(daemon) == 0 {
		return env
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if d, ok := daemon[name]; ok && kv == d {
			delete(daemon, name)
			continue
		}
		out = append(out, kv)
	}
	return out
}

// daemonEnvCopyLen returns the length of the copy of os.Environ() env starts
// from: the names of the first entries of env are the names of os.Environ(),
// in the same order (a value may have changed since the copy). 0 when env does
// not start from such a copy, as an env built from scratch.
func daemonEnvCopyLen(env []string) int {
	daemon := os.Environ()
	if len(env) < len(daemon) {
		return 0
	}
	for i, kv := range daemon {
		name, _, _ := strings.Cut(kv, "=")
		if got, _, _ := strings.Cut(env[i], "="); got != name {
			return 0
		}
	}
	return len(daemon)
}

// setPreserveEnvNames writes the names of env into the --preserve-env flag of
// a command made by agentCommand with the option set.
func setPreserveEnvNames(cmd *exec.Cmd, env []string) {
	for i, a := range cmd.Args {
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, runAsPreserveFlag) {
			cmd.Args[i] = runAsPreserveFlag + strings.Join(agentEnvNames(env), ",")
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

// agentSudoCommandError returns an error when cmd, a command agentCommand
// built as the kind what, is not a sudo command of the agent user of s, and
// drops its snapshot; nil otherwise. A caller with the option set uses it so a
// command never runs as the daemon user.
func agentSudoCommandError(cmd *exec.Cmd, s *runAsSettings, what string) error {
	if cmd == nil {
		return fmt.Errorf("run_as_user: no %s command was built", what)
	}
	if u, ok := agentCmdUser(cmd); !ok || u != s.user {
		runAsSnapshots.Delete(cmd)
		return fmt.Errorf("run_as_user: the %s command %q is not a sudo command of the agent user %q", what, cmd.Args, s.user)
	}
	return nil
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
// group id is the pid of the sudo process (detachSudoCommand). When the kill
// exits non-zero it waits up to 2 seconds for the sudo process to be gone: a
// group that ended on its own is not a failure. A process still alive after
// that is: one Error line with the pid, and the error goes to the caller, so a
// caller without a context never reports a stop that did not happen. There is
// no direct fallback, it would only fail the same way.
func killAgentProcessGroup(cmd *exec.Cmd, agentUser string, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	ctx, cancel := context.WithTimeout(context.Background(), runAsTimeout)
	defer cancel()
	out, err := agentKillCommand(ctx, agentUser, pid, sig).CombinedOutput()
	if err == nil {
		return nil
	}
	for deadline := time.Now().Add(runAsKillGrace); procgroup.Alive(pid); {
		if time.Now().After(deadline) {
			slog.Default().Error("run_as_user kill through sudo failed, the process is still alive",
				slog.Int("pid", pid),
				slog.Int("pgid", pid),
				slog.String("run_as_user", agentUser),
				slog.String("signal", signalName(sig)),
				slog.Any("error", err),
				slog.String("output", strings.TrimSpace(string(out))),
			)
			return fmt.Errorf("sudo kill of process group %d: %w: %w", pid, errAgentKillLogged, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	slog.Default().Debug("run_as_user kill through sudo failed after the process had exited",
		slog.Int("pid", pid),
		slog.String("run_as_user", agentUser),
		slog.Any("error", err),
	)
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
