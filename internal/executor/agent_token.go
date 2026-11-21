package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
)

// AgentReadTokenProvider mints a GitHub token that can only read repo
// ("owner/name"), for the agent user of a run_as_user run, which holds no
// GitHub credential of its own (TASK-651). It returns the token with its
// expiry. Implementations must never log the token.
type AgentReadTokenProvider func(ctx context.Context, repo string) (token string, expires time.Time, err error)

var (
	agentReadTokenProviderMu sync.RWMutex
	agentReadTokenProvider   AgentReadTokenProvider
)

// SetAgentReadTokenProvider installs the provider of read tokens for one
// repository. cmd/pilot calls it at daemon startup with a provider that
// mints a scoped GitHub App installation token when adapters.github.app is
// configured, and with nil otherwise. Passing nil clears it.
//
// Decision: there is no fallback to the token of the daemon. With no
// provider the agent gets no token, since the daemon's token can write to
// each repository it reaches.
func SetAgentReadTokenProvider(provider AgentReadTokenProvider) {
	agentReadTokenProviderMu.Lock()
	defer agentReadTokenProviderMu.Unlock()
	agentReadTokenProvider = provider
}

// GetAgentReadTokenProvider returns the installed provider, or nil when no
// GitHub App is configured.
func GetAgentReadTokenProvider() AgentReadTokenProvider {
	agentReadTokenProviderMu.RLock()
	defer agentReadTokenProviderMu.RUnlock()
	return agentReadTokenProvider
}

// agentTokenFileName is the name of the file with the read token in the token
// directory of a run.
const agentTokenFileName = "github-token"

// agentTokenLockName is the name of the lock file in the token directory of a
// run: the process of the run holds an flock on it for the life of the
// directory, so the sweep of another daemon process keeps the directory.
const agentTokenLockName = "lock"

// agentGitCredentialHelperName is the name of the git credential helper
// script in the token directory of a run.
const agentGitCredentialHelperName = "git-credential-pilot"

// agentGitCredentialURL is the URL whose credential helper of git is the
// script of the run: github.com over https, no other host. Tests replace it
// with the URL of a local server.
var agentGitCredentialURL = "https://github.com"

// agentTokenRefreshMargin is how long before its expiry the token in the file
// is replaced. The expiry is the clock of GitHub, so the margin is wide.
const agentTokenRefreshMargin = 15 * time.Minute

// agentTokenErrorMax is the number of characters of a provider error a
// warning holds.
const agentTokenErrorMax = 200

// agentTokenRetryWait is the shortest wait before a refresh of a token that
// expires within the margin. Tests lower it.
var agentTokenRetryWait = time.Minute

// agentTokenErrorWait is the wait after the first failed refresh; each next
// failure doubles it, up to agentTokenErrorWaitMax. A success resets it.
const (
	agentTokenErrorWait    = 30 * time.Second
	agentTokenErrorWaitMax = 5 * time.Minute
)

// agentTokenStopWait bounds how long Stop waits for a provider call in flight
// once its context is cancelled.
const agentTokenStopWait = 2 * time.Second

// agentTokenTimer starts a wait of the refresh: the channel receives once d
// has passed, stop ends the wait. Tests replace it to see the waits.
var agentTokenTimer = func(d time.Duration) (c <-chan time.Time, stop func() bool) {
	t := time.NewTimer(d)
	return t.C, t.Stop
}

// agentTokenDirPattern is the name pattern of the token directory of a run in
// the temporary directory.
const agentTokenDirPattern = "pilot-ghtoken-*"

// agentTokenSweepDir is the directory SweepAgentTokenDirs scans: the one
// os.MkdirTemp makes the token directories in. Tests replace it.
var agentTokenSweepDir = os.TempDir

// agentTokenDirOwned reports whether fi (of an Lstat) belongs to the daemon
// user; the sweep removes no other directory. Tests replace it.
var agentTokenDirOwned = ownedByDaemonUser

// AgentTokenRepoLookup returns the "OWNER/NAME" of the GitHub repository of the
// project config entry whose path is projectPath, or "" when no entry has
// that path or the entry has no GitHub repository (TASK-651). entry reports
// whether a project config entry has the path, so the run can say which of
// the two it was. cmd/pilot supplies the config-backed implementation.
type AgentTokenRepoLookup func(projectPath string) (repo string, entry bool)

// agentTokenFileKey is the context key of the token file of a run.
type agentTokenFileKey struct{}

// withAgentTokenFile returns ctx carrying f, the token file of the run.
func withAgentTokenFile(ctx context.Context, f *agentTokenFile) context.Context {
	return context.WithValue(ctx, agentTokenFileKey{}, f)
}

// agentTokenFileFrom returns the token file of the run of ctx, nil when none.
func agentTokenFileFrom(ctx context.Context) *agentTokenFile {
	f, _ := ctx.Value(agentTokenFileKey{}).(*agentTokenFile)
	return f
}

// SweepAgentTokenDirs removes the token directories a daemon that was killed
// left behind, each holding a token valid up to an hour that the agent user
// of any later run could read. With the option empty it does nothing. The
// daemon start calls it once, before any run. It returns the number removed.
func SweepAgentTokenDirs(log *slog.Logger) int {
	if runAsSettingsNow() == nil {
		return 0
	}
	if log == nil {
		log = slog.Default()
	}
	return sweepAgentTokenDirs(log, agentTokenSweepDir())
}

// sweepAgentTokenDirs removes each directory pilot-ghtoken-* in dir that the
// daemon user owns. It checks with Lstat: a link of that name is not
// followed and is left alone, as is anything not owned by the daemon user. A
// directory whose lock is held belongs to a live run of another daemon
// process of the same user and is kept, with one Info line that names it; one
// with no lock file or a free lock is removed.
//
// Decision: the lock is tried with no wait. The gateway start takes no daemon
// lock, so a second daemon process of the user can run at the same time; the
// creator makes the lock right after the directory, so only a directory in
// that short window can still go.
func sweepAgentTokenDirs(log *slog.Logger, dir string) int {
	matches, err := filepath.Glob(filepath.Join(dir, agentTokenDirPattern))
	if err != nil {
		return 0
	}
	removed := 0
	for _, m := range matches {
		fi, err := os.Lstat(m)
		if err != nil || !fi.IsDir() || !agentTokenDirOwned(fi) {
			continue
		}
		if agentTokenDirLocked(m) {
			log.Info("run_as_user: token directory of a live run kept", slog.String("dir", m))
			continue
		}
		if err := os.RemoveAll(m); err != nil {
			log.Warn("run_as_user: left token directory not removed",
				slog.String("dir", m), slog.String("error", cutAgentTokenError(err)))
			continue
		}
		removed++
	}
	if removed > 0 {
		log.Info("run_as_user: left token directories removed", slog.Int("removed", removed))
	}
	return removed
}

// startRunAgentTokenFile makes the token file of the run of task: the read
// token of the repository of the project config entry whose path is
// task.ProjectPath. It returns nil, and the run goes on with no token, when the
// option is empty, no provider or lookup is installed, no project config entry
// has the path, the entry has no GitHub repository, or the full repository
// name the task carries differs (letter case ignored) from that of the
// project. With the option set each of these logs one Warn line for the run
// that says why. Callers defer Stop on a non-nil result.
//
// Decision: the repository of the task is never the input. On the paths with
// no full name (webhook, retry) it was matched by the name only, and of two
// projects with the same name under two owners one run got the token of the
// other.
func (r *Runner) startRunAgentTokenFile(ctx context.Context, task *Task) *agentTokenFile {
	if runAsSettingsNow() == nil {
		return nil
	}
	noToken := func(reason string) *agentTokenFile {
		r.log.Warn("run_as_user: no GitHub read token for the agent: "+reason,
			slog.String("task_id", task.ID),
			slog.String("project_path", task.ProjectPath))
		return nil
	}
	if GetAgentReadTokenProvider() == nil {
		return noToken("no read token provider is installed (it needs adapters.github.app)")
	}
	if r.agentTokenRepoLookup == nil {
		return noToken("no repository lookup is installed on the runner")
	}
	repo, entry := r.agentTokenRepoLookup(task.ProjectPath)
	if !entry {
		return noToken("no project config entry has the path")
	}
	if repo == "" {
		return noToken("the project config entry has no GitHub repository")
	}
	if task.SourceRepo != "" && !strings.EqualFold(task.SourceRepo, repo) {
		r.log.Warn("run_as_user: no GitHub read token for the agent: the repository of the task is not the repository of its project",
			slog.String("task_id", task.ID),
			slog.String("project_path", task.ProjectPath),
			slog.String("task_repo", task.SourceRepo),
			slog.String("project_repo", repo))
		return nil
	}
	// Each line of the token file code names the run.
	log := r.log.With(slog.String("task_id", task.ID), slog.String("project_path", task.ProjectPath))
	return startAgentTokenFile(ctx, log, repo)
}

// agentTokenFile is the read token of one run_as_user run: a directory of its
// own (owner the daemon, group the agent group, 0750) with the token in
// github-token (0640), replaced before each expiry until Stop. The agent user
// can read the file and cannot write the directory. helper is the git
// credential helper script next to the token (0750), "" when it could not be
// written. lock is the lock file of the directory, held until Stop.
type agentTokenFile struct {
	dir    string
	path   string
	helper string
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	mu      sync.Mutex // guards lock and stopped, and is held over each write of the refresh
	lock    io.Closer
	stopped bool
}

// startAgentTokenFile writes the read token of repo for the run and starts its
// refresh. repo is "OWNER/NAME" of the project config entry of the task
// (startRunAgentTokenFile), never a value of the workspace. It returns nil, and the run
// goes on with no token, when the option is empty, no provider is installed,
// repo is empty or the provider fails: no other credential takes its place.
// Callers defer Stop on a non-nil result.
func startAgentTokenFile(ctx context.Context, log *slog.Logger, repo string) *agentTokenFile {
	if runAsSettingsNow() == nil {
		return nil
	}
	provider := GetAgentReadTokenProvider()
	if provider == nil || repo == "" {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	token, expires, err := provider(ctx, repo)
	if err != nil {
		log.Warn("run_as_user: no GitHub read token for the agent",
			slog.String("repo", repo), slog.String("error", cutAgentTokenError(err)))
		return nil
	}
	dir, err := os.MkdirTemp("", agentTokenDirPattern)
	if err != nil {
		log.Warn("run_as_user: no GitHub read token for the agent",
			slog.String("repo", repo), slog.String("error", cutAgentTokenError(err)))
		return nil
	}
	lock, err := lockAgentTokenDir(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		log.Warn("run_as_user: no GitHub read token for the agent",
			slog.String("repo", repo), slog.String("error", cutAgentTokenError(err)))
		return nil
	}
	if err := applyGuardPerm(dir, 0o750); err != nil {
		_ = os.RemoveAll(dir)
		_ = lock.Close()
		log.Warn("run_as_user: no GitHub read token for the agent",
			slog.String("repo", repo), slog.String("error", cutAgentTokenError(err)))
		return nil
	}
	f := &agentTokenFile{dir: dir, path: filepath.Join(dir, agentTokenFileName), done: make(chan struct{}), lock: lock}
	if err := writeAgentTokenFile(dir, token); err != nil {
		_ = os.RemoveAll(dir)
		_ = lock.Close()
		log.Warn("run_as_user: no GitHub read token for the agent",
			slog.String("repo", repo), slog.String("error", cutAgentTokenError(err)))
		return nil
	}
	if helper, err := writeAgentGitCredentialHelper(dir, f.path); err != nil {
		log.Warn("run_as_user: no git credential helper for the agent, git of the agent gets no GitHub token",
			slog.String("repo", repo), slog.String("error", cutAgentTokenError(err)))
	} else {
		f.helper = helper
	}
	refreshCtx, cancel := context.WithCancel(ctx)
	f.cancel = cancel
	go f.refresh(refreshCtx, log, provider, repo, expires)
	return f
}

// refresh replaces the token agentTokenRefreshMargin before its expiry until
// Stop cancels ctx. A failed refresh keeps the old token (valid up to its
// expiry) and is retried after agentTokenErrorWait, doubled for each next
// failure up to agentTokenErrorWaitMax. When the write fails because the
// directory is gone, the directory is made again (remake); when that fails
// too, one Error line is logged and the refresh stops: no more provider calls
// for the run.
func (f *agentTokenFile) refresh(ctx context.Context, log *slog.Logger, provider AgentReadTokenProvider, repo string, expires time.Time) {
	defer close(f.done)
	var errWait time.Duration
	for {
		wait := errWait
		if wait == 0 {
			wait = time.Until(expires.Add(-agentTokenRefreshMargin))
			if wait < agentTokenRetryWait {
				wait = agentTokenRetryWait
			}
		}
		c, stop := agentTokenTimer(wait)
		select {
		case <-ctx.Done():
			stop()
			return
		case <-c:
		}
		token, next, err := provider(ctx, repo)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			var gone bool
			if gone, err = f.write(ctx, token); gone {
				log.Error("run_as_user: the token directory of the run is gone and could not be made again; the refresh of the GitHub read token of the agent stops",
					slog.String("repo", repo), slog.String("dir", f.dir),
					slog.String("error", cutAgentTokenError(err)))
				return
			}
		}
		if err != nil {
			errWait = nextAgentTokenErrorWait(errWait)
			log.Warn("run_as_user: refresh of the GitHub read token of the agent failed",
				slog.String("repo", repo), slog.String("error", cutAgentTokenError(err)),
				slog.Duration("retry_in", errWait))
			continue
		}
		errWait = 0
		expires = next
	}
}

// write writes token into the directory of f. When the directory is gone it
// makes it again with the same path, mode, group, lock file and helper script
// and writes the token there; gone is true when that failed. Nothing is
// written once Stop has begun.
func (f *agentTokenFile) write(ctx context.Context, token string) (gone bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped || ctx.Err() != nil {
		return false, nil
	}
	err = writeAgentTokenFile(f.dir, token)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if _, serr := os.Lstat(f.dir); !errors.Is(serr, fs.ErrNotExist) {
		return false, err
	}
	if err := f.remake(token); err != nil {
		return true, err
	}
	return false, nil
}

// remake makes the directory of f again (Mkdir refuses anything at the path,
// a link too), locks it, sets its mode and group and writes token and the
// helper script. When a step after the Mkdir fails it removes the directory it
// made; it never removes what it did not make. The caller holds f.mu.
func (f *agentTokenFile) remake(token string) (err error) {
	if err := os.Mkdir(f.dir, 0o700); err != nil {
		return fmt.Errorf("agent token file: make %s again: %w", f.dir, err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(f.dir)
		}
	}()
	lock, err := lockAgentTokenDir(f.dir)
	if err != nil {
		return err
	}
	if f.lock != nil {
		_ = f.lock.Close()
	}
	f.lock = lock
	if err := applyGuardPerm(f.dir, 0o750); err != nil {
		return err
	}
	if err := writeAgentTokenFile(f.dir, token); err != nil {
		return err
	}
	if f.helper != "" {
		if _, err := writeAgentGitCredentialHelper(f.dir, f.path); err != nil {
			return err
		}
	}
	return nil
}

// nextAgentTokenErrorWait is the wait after a failed refresh that followed a
// wait of prev (0: the last refresh did not fail).
func nextAgentTokenErrorWait(prev time.Duration) time.Duration {
	if prev <= 0 {
		return agentTokenErrorWait
	}
	if next := 2 * prev; next < agentTokenErrorWaitMax {
		return next
	}
	return agentTokenErrorWaitMax
}

// Env returns the environment entry that points the gh guard at the file.
func (f *agentTokenFile) Env() string {
	return ghguard.EnvTokenFile + "=" + f.path
}

// appendAgentGitCredentialEntries returns env plus the config entries of git
// (GIT_CONFIG_COUNT, GIT_CONFIG_KEY_n, GIT_CONFIG_VALUE_n) that make the
// helper script of f the credential helper of agentGitCredentialURL. env is
// the environment of an agent command after the allowlist, so a config entry
// in it was set by the code: the new entry takes the next index. With no
// token file or no helper env is returned unchanged.
//
// Decision: the entries hold the path of the script, never the token: the
// environment of a process can be read by other processes of the agent user
// and lands in logs of the argv and env; the script reads the file at each
// call, so a refreshed token is used too.
func appendAgentGitCredentialEntries(env []string, f *agentTokenFile) []string {
	if f == nil || f.helper == "" {
		return env
	}
	return appendGitConfigEntry(env, "credential."+agentGitCredentialURL+".helper", f.helper)
}

// appendGitConfigEntry adds key=value as the next config entry of env in the
// GIT_CONFIG_COUNT form; a count that is not a number counts as none.
func appendGitConfigEntry(env []string, key, value string) []string {
	n := 0
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GIT_CONFIG_COUNT="); ok {
			if n, _ = strconv.Atoi(v); n < 0 {
				n = 0
			}
		}
	}
	i := strconv.Itoa(n)
	return lastEnvWins(append(env,
		"GIT_CONFIG_KEY_"+i+"="+key,
		"GIT_CONFIG_VALUE_"+i+"="+value,
		"GIT_CONFIG_COUNT="+strconv.Itoa(n+1)))
}

// agentGitCredentialHelperScript is the git credential helper for the token
// file at tokenPath: for `get` it prints the user name of a GitHub App token
// and the content of the file as the password, for any other operation
// (store, erase) nothing. It reads the request of git and holds the path of
// the file, not the token.
func agentGitCredentialHelperScript(tokenPath string) string {
	return `#!/bin/sh
# git credential helper of a run_as_user run (TASK-651): the read token of the
# run, read from its file at each call.
cat >/dev/null
[ "$1" = get ] || exit 0
t=$(cat -- ` + shellQuote(tokenPath) + ` 2>/dev/null) || exit 0
[ -n "$t" ] || exit 0
printf 'username=x-access-token\npassword=%s\n' "$t"
`
}

// writeAgentGitCredentialHelper writes the helper script of the token file at
// tokenPath into dir/git-credential-pilot (0750, group of the agent): created
// exclusively with no follow of a link. It returns the path of the script.
func writeAgentGitCredentialHelper(dir, tokenPath string) (string, error) {
	path := filepath.Join(dir, agentGitCredentialHelperName)
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|agentTokenNoFollow, 0o700)
	if err != nil {
		return "", fmt.Errorf("agent git credential helper: create %s: %w", path, err)
	}
	_, werr := out.Write([]byte(agentGitCredentialHelperScript(tokenPath)))
	cerr := out.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("agent git credential helper: write %s: %w", path, err)
	}
	if err := applyGuardPerm(path, 0o750); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// Stop ends the refresh: it cancels the context of a provider call in flight,
// waits for it at most agentTokenStopWait, removes the directory and then
// frees its lock. No new provider call starts and the file is not written
// after Stop returns. Safe to call twice.
func (f *agentTokenFile) Stop() {
	f.once.Do(func() {
		f.cancel()
		timer := time.NewTimer(agentTokenStopWait)
		select {
		case <-f.done:
		case <-timer.C:
		}
		timer.Stop()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.stopped = true
		_ = os.RemoveAll(f.dir)
		if f.lock != nil {
			_ = f.lock.Close()
		}
	})
}

// lockAgentTokenDir creates the lock file of the token directory dir (0600,
// only the daemon user can write it; created exclusively with no follow of a
// link) and takes an flock on it with no wait. The returned Closer frees the
// lock.
func lockAgentTokenDir(dir string) (io.Closer, error) {
	path := filepath.Join(dir, agentTokenLockName)
	lf, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|agentTokenNoFollow, 0o600)
	if err != nil {
		return nil, fmt.Errorf("agent token file: create %s: %w", path, err)
	}
	if err := tryLockAgentTokenFile(lf); err != nil {
		_ = lf.Close()
		return nil, fmt.Errorf("agent token file: lock %s: %w", path, err)
	}
	return lf, nil
}

// agentTokenDirLocked reports whether another open of the lock file of the
// token directory dir holds its lock: false when there is no lock file.
func agentTokenDirLocked(dir string) bool {
	lf, err := os.OpenFile(filepath.Join(dir, agentTokenLockName), os.O_RDONLY|agentTokenNoFollow, 0)
	if err != nil {
		return false
	}
	defer func() { _ = lf.Close() }()
	return tryLockAgentTokenFile(lf) != nil
}

// writeAgentTokenFile writes token into dir/github-token: a Go file call only
// (the token never goes through a shell or an argument list), into a temporary
// file created exclusively with no follow of a link, then renamed. A link or a
// file at the temporary name is an error and is not touched.
func writeAgentTokenFile(dir, token string) (err error) {
	tmp := filepath.Join(dir, agentTokenFileName+".tmp")
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|agentTokenNoFollow, 0o600)
	if err != nil {
		return fmt.Errorf("agent token file: create %s: %w", tmp, err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	_, werr := out.Write([]byte(token))
	cerr := out.Close()
	if err := errors.Join(werr, cerr); err != nil {
		return fmt.Errorf("agent token file: write %s: %w", tmp, err)
	}
	if err := applyGuardPerm(tmp, 0o640); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, agentTokenFileName)); err != nil {
		return fmt.Errorf("agent token file: rename %s: %w", tmp, err)
	}
	return nil
}

// cutAgentTokenError is the text of err cut to agentTokenErrorMax characters.
func cutAgentTokenError(err error) string {
	r := []rune(err.Error())
	if len(r) > agentTokenErrorMax {
		r = r[:agentTokenErrorMax]
	}
	return string(r)
}
