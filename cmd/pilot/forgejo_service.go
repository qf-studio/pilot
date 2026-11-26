package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/forgejo"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
)

// TASK-657: Forgejo intake. One service, shared by both startup paths of
// `pilot start` (gateway mode and polling mode):
//
//   - signed webhook at forgejoWebhookPath (opened / label_updated issues);
//   - one paginated catch-up scan per configured repo after startup recovery
//     (no periodic polling);
//   - admission into the durable dispatcher queue, then a completion comment
//     once the execution reaches a terminal status.
//
// Known limitations:
//   - the completion comment is best effort. A terminal execution whose comment
//     failed (or whose daemon stopped before it was posted) is not re-announced
//     after a restart, because re-announcing could duplicate comments;
//   - a closed-unmerged PR on the task branch holds the issue until a human
//     deletes the branch/PR or relabels under a new issue;
//   - git push uses the project checkout's own origin credentials; the Forgejo
//     token is never passed to git or to the agent subprocess;
//   - there is no periodic polling: issues labelled while the daemon is down
//     are picked up by the single startup scan, later ones by webhook only.
//
// Both entry points funnel into admitIssue, which re-reads live state, applies
// the duplicate / repick / hold / existing-PR guards and queues at most once.

const (
	// forgejoWebhookPath is the gateway route the instance's webhook targets.
	forgejoWebhookPath = "/webhooks/forgejo"

	forgejoSourceAdapter = "forgejo"
	forgejoTaskPrefix    = "FJ-"

	forgejoAdmitTimeout = 90 * time.Second
	forgejoScanTimeout  = 10 * time.Minute
	forgejoWaitPoll     = 5 * time.Second
)

// Retry schedules are variables so tests can shorten them.
var (
	forgejoScanRetryDelays    = []time.Duration{2 * time.Second, 5 * time.Second}
	forgejoCommentRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second}
)

// forgejoAPI is the slice of *forgejo.Client the service uses.
type forgejoAPI interface {
	GetIssue(ctx context.Context, number int) (forgejo.Issue, error)
	DefaultBranch(ctx context.Context) (string, error)
	ListOpenIssues(ctx context.Context, label string, visit func(forgejo.Issue) error) error
	FindPullRequest(ctx context.Context, head, base string) (forgejo.PullRequest, error)
	CreatePR(ctx context.Context, source, target, title, body string) (string, error)
	AddComment(ctx context.Context, number int, body string) error
}

// forgejoQueue is the slice of *executor.Dispatcher the service uses.
type forgejoQueue interface {
	IsActive(taskID, projectPath string) bool
	HasTerminalCompletion(taskID, projectPath string) (bool, error)
	QueueTaskOutcome(ctx context.Context, task *executor.Task) (executor.QueueOutcome, error)
	WaitForExecution(ctx context.Context, execID string, pollInterval time.Duration) (*memory.Execution, error)
}

// forgejoStore is the slice of *memory.Store used to re-attach waiters.
type forgejoStore interface {
	GetNonTerminalExecutions(projectPath string) ([]*memory.Execution, error)
}

type forgejoClientFactory func(baseURL, token, repo string) (forgejoAPI, error)

func defaultForgejoClientFactory(baseURL, token, repo string) (forgejoAPI, error) {
	return forgejo.NewClient(baseURL, token, repo)
}

// forgejoRepo is one allowlisted repository and the project it maps to.
type forgejoRepo struct {
	name    string
	project *config.ProjectConfig
	path    string
	api     forgejoAPI
}

// forgejoRuntime owns everything created before the dispatcher exists: the
// allowlist, the per-project PR creators and the webhook handler. The webhook
// route must be mounted before the gateway starts, which in polling mode is
// before the dispatcher is created, so the handler answers 503 until Start
// binds the service.
type forgejoRuntime struct {
	baseURL string
	label   string
	secret  string
	repos   map[string]*forgejoRepo
	handler http.Handler
	svc     atomic.Pointer[forgejoService]
}

// forgejoEnabled reports whether adapters.forgejo is switched on.
func forgejoEnabled(cfg *config.Config) bool {
	return cfg != nil && cfg.Adapters != nil && cfg.Adapters.Forgejo != nil && cfg.Adapters.Forgejo.Enabled
}

// validateForgejoStartup fails clearly when Forgejo is enabled in a run mode
// that cannot receive its webhook.
func validateForgejoStartup(cfg *config.Config, noGateway bool) error {
	if !forgejoEnabled(cfg) {
		return nil
	}
	if noGateway {
		return errors.New("adapters.forgejo.enabled requires the HTTP gateway for its webhook; remove --no-gateway")
	}
	if cfg.Gateway == nil {
		return errors.New("adapters.forgejo.enabled requires a gateway block (host/port) for its webhook")
	}
	return nil
}

// newForgejoRuntime builds the allowlist and registers one PR creator per
// Forgejo project on runner. It must run before the dispatcher starts: recovery
// can run a restored task immediately, and without a creator the runner would
// fall back to `gh pr create`. Returns nil when the adapter is disabled.
func newForgejoRuntime(cfg *config.Config, runner *executor.Runner) (*forgejoRuntime, error) {
	return newForgejoRuntimeWith(cfg, runner, defaultForgejoClientFactory)
}

func newForgejoRuntimeWith(cfg *config.Config, runner *executor.Runner, factory forgejoClientFactory) (*forgejoRuntime, error) {
	if !forgejoEnabled(cfg) {
		return nil, nil
	}
	if runner == nil {
		return nil, errors.New("adapters.forgejo.enabled requires an executor runner")
	}
	fj := cfg.Adapters.Forgejo
	baseURL, err := fj.NormalizedBaseURL()
	if err != nil {
		return nil, err
	}
	rt := &forgejoRuntime{
		baseURL: baseURL,
		label:   fj.ResolvedPilotLabel(),
		secret:  fj.WebhookSecret,
		repos:   map[string]*forgejoRepo{},
	}
	for _, p := range cfg.Projects {
		if p == nil || p.Forgejo == nil {
			continue
		}
		api, err := factory(baseURL, fj.Token, p.Forgejo.Repo)
		if err != nil {
			return nil, fmt.Errorf("forgejo: project %q: %w", p.Name, err)
		}
		key := executor.ProjectPRCreatorKey(p.Path)
		if key == "" {
			return nil, fmt.Errorf("forgejo: project %q has no path", p.Name)
		}
		rt.repos[p.Forgejo.Repo] = &forgejoRepo{name: p.Forgejo.Repo, project: p, path: p.Path, api: api}
		runner.RegisterPRCreator(key, &forgejoPRCreator{api: api, baseURL: baseURL, repo: p.Forgejo.Repo})
		logging.WithComponent("forgejo").Info("Registered project PR creator",
			slog.String("project", p.Name), slog.String("repo", p.Forgejo.Repo), slog.String("key", key))
	}
	if len(rt.repos) == 0 {
		return nil, errors.New("adapters.forgejo.enabled but no project has a forgejo.repo block")
	}
	rt.handler = forgejo.NewWebhookHandler(rt.secret, rt.onWebhook)
	return rt, nil
}

// Handler is the signed webhook handler for forgejoWebhookPath.
func (rt *forgejoRuntime) Handler() http.Handler { return rt.handler }

func (rt *forgejoRuntime) onWebhook(ctx context.Context, repo string, number int) error {
	svc := rt.svc.Load()
	if svc == nil {
		return errors.New("forgejo service not started")
	}
	return svc.admitNumber(ctx, repo, number)
}

// Start binds the service to the started dispatcher, re-attaches completion
// waiters for recovered executions and launches the single catch-up scan. It
// returns immediately; the scan runs in the background under ctx.
func (rt *forgejoRuntime) Start(ctx context.Context, queue forgejoQueue, store forgejoStore) error {
	if queue == nil {
		return errors.New("adapters.forgejo.enabled requires the durable task dispatcher, which is not running")
	}
	svc := &forgejoService{
		ctx: ctx, rt: rt, queue: queue, store: store,
		inflight: map[string]*forgejoAdmission{}, watching: map[string]struct{}{},
		originCheck: checkForgejoOrigin,
	}
	svc.restoreWatchers()
	rt.svc.Store(svc)
	logging.SafeGo("forgejo-scan", svc.scanOnce)
	return nil
}

type forgejoService struct {
	ctx   context.Context
	rt    *forgejoRuntime
	queue forgejoQueue
	store forgejoStore

	// originCheck verifies the project's git origin points at the allowlisted
	// repository. A variable so tests need no git checkout.
	originCheck func(ctx context.Context, path, baseURL, repo string) error

	mu       sync.Mutex
	inflight map[string]*forgejoAdmission // task IDs being admitted
	watching map[string]struct{}          // execution IDs with a waiter
}

// forgejoTaskID is the stable task identity, scoped to instance and repo.
func forgejoTaskID(baseURL, repo string, number int) string {
	return fmt.Sprintf("%s%s-%d", forgejoTaskPrefix, forgejoScope(baseURL, repo), number)
}

func forgejoScope(baseURL, repo string) string {
	sum := sha256.Sum256([]byte(baseURL + "\n" + repo))
	return hex.EncodeToString(sum[:])[:8]
}

// forgejoBranch is the deterministic work branch of an issue.
func forgejoBranch(number int) string { return "pilot/forgejo-" + strconv.Itoa(number) }

// admitNumber is the webhook entry: it re-reads the live issue before
// admitting. The admission itself runs under the service context, never under
// the HTTP request's, so a dropped connection cannot abort a half-done queue
// write; the request context only bounds how long a duplicate waits.
func (s *forgejoService) admitNumber(reqCtx context.Context, repo string, number int) error {
	r := s.rt.repos[repo]
	if r == nil {
		return nil // not allowlisted: ignored, never admitted
	}
	ctx, cancel := context.WithTimeout(s.ctx, forgejoAdmitTimeout)
	defer cancel()
	return s.admitLive(ctx, reqCtx.Done(), r, number)
}

// admitLive re-reads the issue from Forgejo and admits it. The webhook and the
// startup scan both go through it, so neither trusts a payload or a list page.
func (s *forgejoService) admitLive(ctx context.Context, reqDone <-chan struct{}, r *forgejoRepo, number int) error {
	is, err := r.api.GetIssue(ctx, number)
	if err != nil {
		if errors.Is(err, forgejo.ErrNotFound) {
			return nil
		}
		return err
	}
	return s.admitIssueWait(ctx, reqDone, r, is)
}

// errForgejoAdmissionAborted is what a waiting duplicate sees when the
// admission it waited on ended without reporting a result.
var errForgejoAdmissionAborted = errors.New("forgejo admission ended without a result")

// forgejoAdmission is one in-flight admission. Duplicates wait on done and
// take err from the first caller.
type forgejoAdmission struct {
	done chan struct{}
	err  error
}

// admitIssue applies every guard and queues the issue at most once. A nil
// return means admitted, already handled or intentionally skipped; a non-nil
// error is retryable.
func (s *forgejoService) admitIssue(ctx context.Context, r *forgejoRepo, is forgejo.Issue) error {
	return s.admitIssueWait(ctx, nil, r, is)
}

// admitIssueWait is admitIssue where a duplicate also stops waiting when
// reqDone closes. The first caller for a task does the work; a racing duplicate
// waits for that result instead of answering success early, so an
// acknowledgement is never sent before the first admission is durable, and a
// failed first admission fails the duplicate too (the sender retries).
func (s *forgejoService) admitIssueWait(ctx context.Context, reqDone <-chan struct{}, r *forgejoRepo, is forgejo.Issue) (err error) {
	if is.IsPullRequest() || is.State != "open" || !is.HasLabel(s.rt.label) {
		return nil
	}
	taskID := forgejoTaskID(s.rt.baseURL, r.name, is.Number)
	adm, leader := s.beginAdmission(taskID)
	if !leader {
		wctx, cancel := context.WithTimeout(ctx, forgejoAdmitTimeout)
		defer cancel()
		select {
		case <-adm.done:
			return adm.err
		case <-wctx.Done():
			return fmt.Errorf("waiting for the in-flight admission: %w", wctx.Err())
		case <-reqDone:
			return fmt.Errorf("waiting for the in-flight admission: %w", context.Canceled)
		}
	}
	finished := false
	defer func() {
		if !finished {
			err = errForgejoAdmissionAborted
		}
		s.endAdmission(taskID, adm, err)
	}()
	err = s.admitGuarded(ctx, r, is, taskID)
	finished = true
	return err
}

// admitGuarded is the admission body, run by exactly one caller per task ID.
func (s *forgejoService) admitGuarded(ctx context.Context, r *forgejoRepo, is forgejo.Issue, taskID string) error {
	if s.queue.IsActive(taskID, r.path) {
		return nil
	}
	done, err := s.queue.HasTerminalCompletion(taskID, r.path)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	if !repickBackoffAllows(s.queue, r.path, taskID) {
		return nil
	}
	if err := s.originCheck(ctx, r.path, s.rt.baseURL, r.name); err != nil {
		// A misconfigured checkout does not heal by retrying: log and skip.
		logging.WithComponent("forgejo").Error("Refusing to admit issue: project origin does not match the allowlisted repository",
			slog.String("repo", r.name), slog.Int("issue", is.Number), slog.String("error", err.Error()))
		return nil
	}

	base := strings.TrimSpace(r.project.DefaultBranch)
	if base == "" {
		if base, err = r.api.DefaultBranch(ctx); err != nil {
			return err
		}
	}
	branch := forgejoBranch(is.Number)
	pr, err := r.api.FindPullRequest(ctx, branch, base)
	if err != nil {
		return err // a failed lookup is never treated as "no PR"
	}
	if pr.URL != "" {
		// Open or merged: the work exists. Closed unmerged: held on purpose.
		logging.WithComponent("forgejo").Info("Skipping issue with an existing pull request",
			slog.String("repo", r.name), slog.Int("issue", is.Number),
			slog.Bool("open", pr.Open), slog.Bool("merged", pr.Merged))
		return nil
	}

	task := s.buildTask(r, is, taskID, branch, base)
	out, err := s.queue.QueueTaskOutcome(ctx, task)
	switch {
	case errors.Is(err, executor.ErrTaskAlreadyActive):
		return nil
	case errors.Is(err, executor.ErrDailyTicketCapReached), errors.Is(err, executor.ErrBillingKeyHold):
		return err // temporary admission caps: retryable
	case errors.Is(err, executor.ErrDispatchGated):
		return nil // held by a dispatcher guard
	case err != nil:
		return err
	}
	if out.ExecutionID != "" {
		s.watch(out.ExecutionID, r, is.Number)
		return nil
	}
	// No execution ID and no error: the dispatcher dropped the pickup as a
	// duplicate. With an owner status the claim belongs to a live or terminal
	// execution, which is handled; without one the owner could not be found
	// (lookup failed or the claim vanished), so nothing is known to be queued.
	if out.ClaimLostOwnerStatus == "" {
		return errors.New("dispatcher dropped the task without an identifiable owner")
	}
	return nil
}

func (s *forgejoService) buildTask(r *forgejoRepo, is forgejo.Issue, taskID, branch, base string) *executor.Task {
	labels := []string{"no-decompose"} // one issue, one branch, one PR
	for _, l := range is.Labels {
		labels = append(labels, l.Name)
	}
	// SourceRepo is deliberately empty: executor code reads it as a GitHub
	// owner/repo and would query GitHub for it.
	return &executor.Task{
		ID:            taskID,
		Title:         is.Title,
		Description:   is.Body,
		ProjectPath:   r.path,
		Branch:        branch,
		BaseBranch:    base,
		CreatePR:      true,
		SourceAdapter: forgejoSourceAdapter,
		SourceIssueID: strconv.Itoa(is.Number),
		Labels:        labels,
		IsCanary:      r.project.Canary,
	}
}

// beginAdmission registers taskID and reports whether the caller is the first
// (leader). A non-leader gets the leader's admission to wait on. The mutex
// guards only the map; it is never held while waiting or doing I/O.
func (s *forgejoService) beginAdmission(taskID string) (*forgejoAdmission, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, busy := s.inflight[taskID]; busy {
		return a, false
	}
	a := &forgejoAdmission{done: make(chan struct{})}
	s.inflight[taskID] = a
	return a, true
}

// endAdmission publishes the leader's result to every waiting duplicate.
func (s *forgejoService) endAdmission(taskID string, a *forgejoAdmission, err error) {
	s.mu.Lock()
	delete(s.inflight, taskID)
	s.mu.Unlock()
	a.err = err
	close(a.done)
}

// repickBackoffAllows consults the shared durable repick backoff.
func repickBackoffAllows(q forgejoQueue, projectPath, taskID string) bool {
	if p, ok := q.(repickBackoffPersister); ok {
		repickBackoff.setPersister(p)
	}
	return repickBackoff.allow(repickBackoffKey(projectPath, taskID))
}

// scanOnce is the single post-recovery catch-up: every allowlisted repo's open
// labelled issues, paginated, with bounded retries on transient errors. One
// repo failing does not stop the others.
func (s *forgejoService) scanOnce() {
	ctx, cancel := context.WithTimeout(s.ctx, forgejoScanTimeout)
	defer cancel()
	names := make([]string, 0, len(s.rt.repos))
	for n := range s.rt.repos {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if ctx.Err() != nil {
			return
		}
		s.scanRepo(ctx, s.rt.repos[name])
	}
}

func (s *forgejoService) scanRepo(ctx context.Context, r *forgejoRepo) {
	log := logging.WithComponent("forgejo")
	for attempt := 0; ; attempt++ {
		failed := 0
		err := r.api.ListOpenIssues(ctx, s.rt.label, func(is forgejo.Issue) error {
			ictx, icancel := context.WithTimeout(ctx, forgejoAdmitTimeout)
			aerr := s.admitLive(ictx, nil, r, is.Number)
			icancel()
			if aerr != nil {
				failed++
				log.Warn("Startup scan could not admit issue",
					slog.String("repo", r.name), slog.Int("issue", is.Number), slog.String("error", aerr.Error()))
			}
			return nil
		})
		if err == nil {
			log.Info("Startup scan finished", slog.String("repo", r.name), slog.Int("admit_failures", failed))
			return
		}
		if attempt >= len(forgejoScanRetryDelays) || !forgejoTransient(err) {
			log.Error("Startup scan failed", slog.String("repo", r.name), slog.String("error", err.Error()))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(forgejoScanRetryDelays[attempt]):
		}
	}
}

// forgejoTransient reports whether err is worth retrying: a 429/5xx answer or
// a network-level failure. Everything else (4xx, malformed JSON, page cap) is
// final.
func forgejoTransient(err error) bool {
	var ae *forgejo.APIError
	if errors.As(err, &ae) {
		return ae.Retryable()
	}
	if errors.Is(err, forgejo.ErrPageCap) || errors.Is(err, forgejo.ErrNotFound) {
		return false
	}
	var ne net.Error
	return errors.As(err, &ne) || strings.Contains(err.Error(), "request failed")
}

// restoreWatchers re-attaches a completion waiter to every Forgejo execution
// the recovery left queued or running.
func (s *forgejoService) restoreWatchers() {
	if s.store == nil {
		return
	}
	for _, r := range s.rt.repos {
		execs, err := s.store.GetNonTerminalExecutions(r.path)
		if err != nil {
			logging.WithComponent("forgejo").Warn("Could not list recovered executions",
				slog.String("repo", r.name), slog.String("error", err.Error()))
			continue
		}
		prefix := forgejoTaskPrefix + forgejoScope(s.rt.baseURL, r.name) + "-"
		for _, e := range execs {
			if !strings.HasPrefix(e.TaskID, prefix) {
				continue
			}
			if n, err := strconv.Atoi(strings.TrimPrefix(e.TaskID, prefix)); err == nil && n > 0 {
				s.watch(e.ID, r, n)
			}
		}
	}
}

// watch waits (once per execution) for the terminal status and notifies.
func (s *forgejoService) watch(execID string, r *forgejoRepo, number int) {
	s.mu.Lock()
	if _, ok := s.watching[execID]; ok {
		s.mu.Unlock()
		return
	}
	s.watching[execID] = struct{}{}
	s.mu.Unlock()
	logging.SafeGo("forgejo-wait", func() {
		defer func() {
			s.mu.Lock()
			delete(s.watching, execID)
			s.mu.Unlock()
		}()
		ex, err := s.queue.WaitForExecution(s.ctx, execID, forgejoWaitPoll)
		if err != nil || ex == nil {
			return // daemon stopping or row gone; a restart re-attaches non-terminal rows
		}
		s.notify(r, number, ex)
	})
}

// forgejoCompletionComment renders the terminal comment, or "" for an outcome
// that is deliberately silent. It never includes raw error or agent output.
func forgejoCompletionComment(exec *memory.Execution) string {
	switch {
	case executor.IsTerminalByDesignStatus(exec.Status):
		return ""
	case exec.Status == "completed" && exec.PRUrl != "":
		return "Pilot opened a pull request for this issue: " + exec.PRUrl
	case exec.Status == "completed":
		return "Pilot finished this issue without opening a pull request."
	default:
		return fmt.Sprintf("Pilot stopped working on this issue (status: %s). Check the Pilot logs for details.", exec.Status)
	}
}

// notify posts the completion comment with bounded retries. Failing to post
// never re-queues work: the comment is the last step and nothing depends on it.
func (s *forgejoService) notify(r *forgejoRepo, number int, exec *memory.Execution) {
	body := forgejoCompletionComment(exec)
	if body == "" {
		return
	}
	log := logging.WithComponent("forgejo")
	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(s.ctx, forgejoAdmitTimeout)
		err := r.api.AddComment(ctx, number, body)
		cancel()
		if err == nil {
			return
		}
		if attempt >= len(forgejoCommentRetryDelays) || !forgejoTransient(err) {
			log.Warn("Could not post completion comment",
				slog.String("repo", r.name), slog.Int("issue", number), slog.String("error", err.Error()))
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(forgejoCommentRetryDelays[attempt]):
		}
	}
}

// forgejoPRCreator is the per-project executor.PRCreator.
type forgejoPRCreator struct {
	api     forgejoAPI
	baseURL string
	repo    string
}

var forgejoSourceLine = regexp.MustCompile(`(?m)^Source: forgejo (FJ-[0-9a-f]{8})-(\d+)[ \t]*$`)

// CreatePR opens the PR (the client checks for an exact existing one first)
// and links the body to the issue instead of the opaque task ID.
func (c *forgejoPRCreator) CreatePR(ctx context.Context, source, target, title, body string) (string, error) {
	scope := forgejoScope(c.baseURL, c.repo)
	body = forgejoSourceLine.ReplaceAllStringFunc(body, func(m string) string {
		sub := forgejoSourceLine.FindStringSubmatch(m)
		if sub[1] != forgejoTaskPrefix+scope {
			return m
		}
		return fmt.Sprintf("Source: %s/%s/issues/%s", c.baseURL, c.repo, sub[2])
	})
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return c.api.CreatePR(ctx, source, target, title, body)
}

// checkForgejoOrigin verifies that the checkout at path fetches from and
// pushes to exactly one remote target, on the configured instance, whose path
// is the allowlisted repo. It runs git with a minimal environment: no daemon
// secrets reach the subprocess.
func checkForgejoOrigin(ctx context.Context, path, baseURL, repo string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for _, args := range [][]string{
		{"remote", "get-url", "--all", "origin"},
		{"remote", "get-url", "--push", "--all", "origin"},
	} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GIT_TERMINAL_PROMPT=0"}
		out, err := cmd.Output()
		if err != nil {
			return errors.New("cannot read the origin remote of the project checkout")
		}
		var urls []string
		for _, l := range strings.Split(string(out), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				urls = append(urls, l)
			}
		}
		if len(urls) != 1 {
			return errors.New("origin must have exactly one fetch url and one push url")
		}
		if err := matchForgejoOrigin(urls[0], baseURL, repo); err != nil {
			return err
		}
	}
	return nil
}

// forgejoPort is the explicit port of u, or the scheme's default.
func forgejoPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "http" {
		return "80"
	}
	return "443"
}

// matchForgejoOrigin compares a git remote URL (http(s), ssh:// or scp-like)
// against the instance base URL and owner/name. An http(s) remote must match
// the base URL's scheme, port and path prefix; an ssh remote matches on host
// only (its port is the ssh port, not the web port). Any other scheme fails.
func matchForgejoOrigin(remote, baseURL, repo string) error {
	base, err := url.Parse(baseURL)
	if err != nil {
		return errors.New("invalid forgejo base url")
	}
	var host, p string
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return errors.New("origin url is not parseable")
		}
		host, p = u.Hostname(), u.Path
		switch u.Scheme {
		case "ssh":
		case "http", "https":
			if u.Scheme != base.Scheme {
				return errors.New("origin scheme differs from adapters.forgejo.base_url")
			}
			if forgejoPort(u) != forgejoPort(base) {
				return errors.New("origin port differs from adapters.forgejo.base_url")
			}
			if u.RawQuery != "" || u.Fragment != "" {
				return errors.New("origin url has a query or fragment")
			}
			prefix := strings.TrimRight(base.Path, "/")
			if prefix != "" {
				if !strings.HasPrefix(p, prefix+"/") {
					return errors.New("origin path is outside the base_url path prefix")
				}
				p = strings.TrimPrefix(p, prefix)
			}
		default:
			return errors.New("origin url is not an ssh or http(s) remote")
		}
	} else if at := strings.LastIndex(remote, "@"); at >= 0 || strings.Contains(remote, ":") {
		rest := remote[at+1:]
		h, pp, ok := strings.Cut(rest, ":")
		if !ok {
			return errors.New("origin url is not an ssh or http(s) remote")
		}
		host, p = h, pp
	} else {
		return errors.New("origin url is not a remote of the forgejo instance")
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	if !strings.EqualFold(host, base.Hostname()) {
		return errors.New("origin host differs from adapters.forgejo.base_url")
	}
	if !strings.EqualFold(p, repo) {
		return errors.New("origin repository differs from the allowlisted forgejo.repo")
	}
	return nil
}
