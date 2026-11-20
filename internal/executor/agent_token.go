package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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

// agentTokenRetryWait is the shortest wait between two calls of the provider:
// after an error, or for a token that expires within the margin. Tests lower it.
var agentTokenRetryWait = time.Minute

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
// refresh. repo is "OWNER/NAME" from the config of the daemon (the source repo
// of the task), never a value of the workspace. It returns nil, and the run
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
	dir, err := os.MkdirTemp("", "pilot-ghtoken-*")
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
// expiry) and is retried after agentTokenRetryWait.
func (f *agentTokenFile) refresh(ctx context.Context, log *slog.Logger, provider AgentReadTokenProvider, repo string, expires time.Time) {
	defer close(f.done)
	for {
		wait := time.Until(expires.Add(-agentTokenRefreshMargin))
		if wait < agentTokenRetryWait {
			wait = agentTokenRetryWait
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		token, next, err := provider(ctx, repo)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = writeAgentTokenFile(f.dir, token)
		}
		if err != nil {
			log.Warn("run_as_user: refresh of the GitHub read token of the agent failed",
				slog.String("repo", repo), slog.String("error", cutAgentTokenError(err)))
			continue
		}
		expires = next
	}
}

// Env returns the environment entry that points the gh guard at the file.
func (f *agentTokenFile) Env() string {
	return ghguard.EnvTokenFile + "=" + f.path
}

// Stop ends the refresh, waits for a provider call in flight, and removes the
// directory. The provider is not called after Stop returns. Safe to call twice.
func (f *agentTokenFile) Stop() {
	f.once.Do(func() {
		f.cancel()
		<-f.done
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
