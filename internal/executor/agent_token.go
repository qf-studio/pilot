package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
// followed and is left alone, as is anything not owned by the daemon user.
func sweepAgentTokenDirs(log *slog.Logger, dir string) int {
	matches, err := filepath.Glob(filepath.Join(dir, agentTokenDirPattern))
	if err != nil {
		return 0
	}
	removed := 0
	for _, m := range matches {
		fi, err := os.Lstat(m)
		if err != nil || !fi.IsDir() || !ownedByDaemonUser(fi) {
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
			slog.String("task_repo", task.SourceRepo),
			slog.String("project_repo", repo))
		return nil
	}
	return startAgentTokenFile(ctx, r.log, repo)
}

// agentTokenFile is the read token of one run_as_user run: a directory of its
// own (owner the daemon, group the agent group, 0750) with the token in
// github-token (0640), replaced before each expiry until Stop. The agent user
// can read the file and cannot write the directory.
type agentTokenFile struct {
	dir    string
	path   string
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
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
	if err := applyGuardPerm(dir, 0o750); err != nil {
		_ = os.RemoveAll(dir)
		log.Warn("run_as_user: no GitHub read token for the agent",
			slog.String("repo", repo), slog.String("error", cutAgentTokenError(err)))
		return nil
	}
	f := &agentTokenFile{dir: dir, path: filepath.Join(dir, agentTokenFileName), done: make(chan struct{})}
	if err := writeAgentTokenFile(dir, token); err != nil {
		_ = os.RemoveAll(dir)
		log.Warn("run_as_user: no GitHub read token for the agent",
			slog.String("repo", repo), slog.String("error", cutAgentTokenError(err)))
		return nil
	}
	refreshCtx, cancel := context.WithCancel(ctx)
	f.cancel = cancel
	go f.refresh(refreshCtx, log, provider, repo, expires)
	return f
}

// refresh replaces the token agentTokenRefreshMargin before its expiry until
// Stop cancels ctx. A failed refresh keeps the old token (valid up to its
// expiry) and is retried after agentTokenErrorWait, doubled for each next
// failure up to agentTokenErrorWaitMax.
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
			err = writeAgentTokenFile(f.dir, token)
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

// Stop ends the refresh: it cancels the context of a provider call in flight,
// waits for it at most agentTokenStopWait, and removes the directory. No new
// provider call starts and the file is not written after Stop returns. Safe
// to call twice.
func (f *agentTokenFile) Stop() {
	f.once.Do(func() {
		f.cancel()
		timer := time.NewTimer(agentTokenStopWait)
		select {
		case <-f.done:
		case <-timer.C:
		}
		timer.Stop()
		_ = os.RemoveAll(f.dir)
	})
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
