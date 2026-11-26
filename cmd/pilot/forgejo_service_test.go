package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/forgejo"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/gateway"
	"github.com/qf-studio/pilot/internal/memory"
)

const (
	fjTestBase   = "https://forge.example.test"
	fjTestSecret = "fake-webhook-secret"
	fjTestRepo   = "acme/app"
)

type fjFakeAPI struct {
	mu          sync.Mutex
	issues      map[int]forgejo.Issue
	getErr      error
	defBranch   string
	prs         map[string]forgejo.PullRequest // head -> match
	prErr       error
	listErrs    []error // consumed one per ListOpenIssues call
	listCalls   int
	comments    []string
	commentErrs []error
	created     []string
}

func (f *fjFakeAPI) GetIssue(_ context.Context, n int) (forgejo.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return forgejo.Issue{}, f.getErr
	}
	is, ok := f.issues[n]
	if !ok {
		return forgejo.Issue{}, &forgejo.APIError{Op: "get issue", Status: 404}
	}
	return is, nil
}

func (f *fjFakeAPI) DefaultBranch(context.Context) (string, error) {
	if f.defBranch == "" {
		return "main", nil
	}
	return f.defBranch, nil
}

func (f *fjFakeAPI) ListOpenIssues(_ context.Context, _ string, visit func(forgejo.Issue) error) error {
	f.mu.Lock()
	f.listCalls++
	var err error
	if len(f.listErrs) > 0 {
		err, f.listErrs = f.listErrs[0], f.listErrs[1:]
	}
	nums := make([]int, 0, len(f.issues))
	for n := range f.issues {
		nums = append(nums, n)
	}
	issues := f.issues
	f.mu.Unlock()
	if err != nil {
		return err
	}
	for i := 1; i <= 100; i++ { // deterministic order
		if is, ok := issues[i]; ok {
			if verr := visit(is); verr != nil {
				return verr
			}
		}
	}
	return nil
}

func (f *fjFakeAPI) FindPullRequest(_ context.Context, head, _ string) (forgejo.PullRequest, error) {
	if f.prErr != nil {
		return forgejo.PullRequest{}, f.prErr
	}
	return f.prs[head], nil
}

func (f *fjFakeAPI) CreatePR(_ context.Context, source, target, title, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, body)
	return "https://forge.example.test/acme/app/pulls/1", nil
}

func (f *fjFakeAPI) AddComment(_ context.Context, _ int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.commentErrs) > 0 {
		var err error
		err, f.commentErrs = f.commentErrs[0], f.commentErrs[1:]
		if err != nil {
			return err
		}
	}
	f.comments = append(f.comments, body)
	return nil
}

type fjFakeQueue struct {
	mu       sync.Mutex
	tasks    []*executor.Task
	active   map[string]bool
	done     map[string]bool
	queueErr error
	delay    time.Duration
	waitExec *memory.Execution
	waitCh   chan struct{}
	enter    chan struct{} // receives once per QueueTaskOutcome call, when set
	gate     chan struct{} // QueueTaskOutcome blocks until closed, when set
	outcome  *executor.QueueOutcome
}

func newFjQueue() *fjFakeQueue {
	return &fjFakeQueue{active: map[string]bool{}, done: map[string]bool{}}
}

func (q *fjFakeQueue) IsActive(id, _ string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.active[id]
}

func (q *fjFakeQueue) HasTerminalCompletion(id, _ string) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.done[id], nil
}

func (q *fjFakeQueue) QueueTaskOutcome(_ context.Context, t *executor.Task) (executor.QueueOutcome, error) {
	if q.enter != nil {
		q.enter <- struct{}{}
	}
	if q.gate != nil {
		<-q.gate
	}
	if q.delay > 0 {
		time.Sleep(q.delay)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.queueErr != nil {
		return executor.QueueOutcome{}, q.queueErr
	}
	if q.active[t.ID] {
		return executor.QueueOutcome{}, fmt.Errorf("task %s: %w", t.ID, executor.ErrTaskAlreadyActive)
	}
	if q.outcome != nil {
		return *q.outcome, nil
	}
	q.active[t.ID] = true
	q.tasks = append(q.tasks, t)
	return executor.QueueOutcome{ExecutionID: "exec-" + t.ID}, nil
}

func (q *fjFakeQueue) WaitForExecution(ctx context.Context, id string, _ time.Duration) (*memory.Execution, error) {
	if q.waitCh == nil {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	select {
	case <-q.waitCh:
		e := *q.waitExec
		e.ID = id
		return &e, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (q *fjFakeQueue) queued() []*executor.Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]*executor.Task(nil), q.tasks...)
}

type fjFakeStore struct {
	execs map[string][]*memory.Execution
}

func (s fjFakeStore) GetNonTerminalExecutions(p string) ([]*memory.Execution, error) {
	return s.execs[p], nil
}

func fjIssue(n int, state string, labels ...string) forgejo.Issue {
	is := forgejo.Issue{Number: n, Title: fmt.Sprintf("issue %d", n), Body: "body", State: state}
	for _, l := range labels {
		is.Labels = append(is.Labels, forgejo.Label{Name: l})
	}
	return is
}

type fjHarness struct {
	rt     *forgejoRuntime
	svc    *forgejoService
	api    *fjFakeAPI
	queue  *fjFakeQueue
	runner *executor.Runner
	path   string
}

func fjConfig(repos ...string) (*config.Config, []string) {
	cfg := &config.Config{Adapters: &config.AdaptersConfig{Forgejo: &config.ForgejoConfig{
		Enabled: true, BaseURL: fjTestBase, Token: "fake-token", WebhookSecret: fjTestSecret,
	}}}
	var paths []string
	for i, r := range repos {
		p := fmt.Sprintf("/tmp/pilot-forgejo-test/proj%d", i)
		paths = append(paths, p)
		cfg.Projects = append(cfg.Projects, &config.ProjectConfig{Name: fmt.Sprintf("p%d", i), Path: p, Forgejo: &config.ProjectForgejoConfig{Repo: r}})
	}
	return cfg, paths
}

func newFjHarness(t *testing.T) *fjHarness {
	t.Helper()
	cfg, paths := fjConfig(fjTestRepo)
	api := &fjFakeAPI{issues: map[int]forgejo.Issue{}, prs: map[string]forgejo.PullRequest{}}
	runner := executor.NewRunner()
	rt, err := newForgejoRuntimeWith(cfg, runner, func(string, string, string) (forgejoAPI, error) { return api, nil })
	if err != nil {
		t.Fatal(err)
	}
	q := newFjQueue()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc := &forgejoService{
		ctx: ctx, rt: rt, queue: q,
		inflight: map[string]*forgejoAdmission{}, watching: map[string]struct{}{},
		originCheck: func(context.Context, string, string, string) error { return nil },
	}
	rt.svc.Store(svc)
	return &fjHarness{rt: rt, svc: svc, api: api, queue: q, runner: runner, path: paths[0]}
}

func fjSign(body []byte) string {
	m := hmac.New(sha256.New, []byte(fjTestSecret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func fjPost(h http.Handler, repo string, number int, action string, signed bool) int {
	body := []byte(fmt.Sprintf(`{"action":%q,"issue":{"number":%d},"repository":{"full_name":%q}}`, action, number, repo))
	r := httptest.NewRequest(http.MethodPost, forgejoWebhookPath, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forgejo-Event", "issues")
	if signed {
		r.Header.Set("X-Forgejo-Signature", fjSign(body))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestForgejoWebhookAdmitsLabelledIssue(t *testing.T) {
	h := newFjHarness(t)
	h.api.issues[7] = fjIssue(7, "open", "pilot", "bug")

	if code := fjPost(h.rt.Handler(), fjTestRepo, 7, "opened", true); code != 204 {
		t.Fatalf("code = %d", code)
	}
	tasks := h.queue.queued()
	if len(tasks) != 1 {
		t.Fatalf("queued %d tasks", len(tasks))
	}
	tk := tasks[0]
	wantID := forgejoTaskID(fjTestBase, fjTestRepo, 7)
	if tk.ID != wantID || !strings.HasPrefix(tk.ID, "FJ-") {
		t.Fatalf("task id = %q want %q", tk.ID, wantID)
	}
	if tk.Branch != "pilot/forgejo-7" || tk.BaseBranch != "main" || !tk.CreatePR {
		t.Fatalf("branch=%q base=%q createPR=%v", tk.Branch, tk.BaseBranch, tk.CreatePR)
	}
	if tk.SourceAdapter != "forgejo" || tk.SourceIssueID != "7" || tk.SourceRepo != "" {
		t.Fatalf("source fields: %+v", tk)
	}
	if tk.ProjectPath != h.path || tk.Title != "issue 7" || tk.Description != "body" {
		t.Fatalf("task fields: %+v", tk)
	}
	if !contains(tk.Labels, "no-decompose") {
		t.Fatalf("labels = %v", tk.Labels)
	}
	if len(h.api.comments) != 0 {
		t.Fatal("comment posted before completion")
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestForgejoWebhookRejectsUnsignedAndUnmapped(t *testing.T) {
	h := newFjHarness(t)
	h.api.issues[1] = fjIssue(1, "open", "pilot")
	if code := fjPost(h.rt.Handler(), fjTestRepo, 1, "opened", false); code != 401 {
		t.Fatalf("unsigned code = %d", code)
	}
	if code := fjPost(h.rt.Handler(), "evil/other", 1, "opened", true); code != 204 {
		t.Fatalf("unmapped code = %d", code)
	}
	if n := len(h.queue.queued()); n != 0 {
		t.Fatalf("queued %d", n)
	}
}

func TestForgejoWebhookNotReadyAnswers503(t *testing.T) {
	cfg, _ := fjConfig(fjTestRepo)
	api := &fjFakeAPI{}
	rt, err := newForgejoRuntimeWith(cfg, executor.NewRunner(), func(string, string, string) (forgejoAPI, error) { return api, nil })
	if err != nil {
		t.Fatal(err)
	}
	if code := fjPost(rt.Handler(), fjTestRepo, 1, "opened", true); code != 503 {
		t.Fatalf("code = %d, want 503 before Start", code)
	}
}

func TestForgejoAdmissionSkips(t *testing.T) {
	id := func(n int) string { return forgejoTaskID(fjTestBase, fjTestRepo, n) }
	tests := []struct {
		name  string
		setup func(h *fjHarness)
		issue forgejo.Issue
	}{
		{"closed", func(*fjHarness) {}, fjIssue(1, "closed", "pilot")},
		{"unlabelled", func(*fjHarness) {}, fjIssue(1, "open", "bug")},
		{"pr backed", func(*fjHarness) {}, func() forgejo.Issue {
			is := fjIssue(1, "open", "pilot")
			is.PullRequest = &forgejo.IssuePullRequest{}
			return is
		}()},
		{"already active", func(h *fjHarness) { h.queue.active[id(1)] = true }, fjIssue(1, "open", "pilot")},
		{"finished", func(h *fjHarness) { h.queue.done[id(1)] = true }, fjIssue(1, "open", "pilot")},
		{"open pr exists", func(h *fjHarness) {
			h.api.prs["pilot/forgejo-1"] = forgejo.PullRequest{URL: "u", Open: true}
		}, fjIssue(1, "open", "pilot")},
		{"merged pr exists", func(h *fjHarness) {
			h.api.prs["pilot/forgejo-1"] = forgejo.PullRequest{URL: "u", Merged: true}
		}, fjIssue(1, "open", "pilot")},
		{"closed unmerged pr holds", func(h *fjHarness) {
			h.api.prs["pilot/forgejo-1"] = forgejo.PullRequest{URL: "u"}
		}, fjIssue(1, "open", "pilot")},
		{"origin mismatch", func(h *fjHarness) {
			h.svc.originCheck = func(context.Context, string, string, string) error { return errors.New("mismatch") }
		}, fjIssue(1, "open", "pilot")},
		{"held by dispatcher", func(h *fjHarness) { h.queue.queueErr = fmt.Errorf("x: %w", executor.ErrDispatchGated) }, fjIssue(1, "open", "pilot")},
		{"already active at queue", func(h *fjHarness) { h.queue.queueErr = fmt.Errorf("x: %w", executor.ErrTaskAlreadyActive) }, fjIssue(1, "open", "pilot")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newFjHarness(t)
			tt.setup(h)
			h.api.issues[1] = tt.issue
			if err := h.svc.admitNumber(context.Background(), fjTestRepo, 1); err != nil {
				t.Fatalf("err = %v", err)
			}
			for _, tk := range h.queue.queued() {
				t.Fatalf("task queued: %+v", tk)
			}
		})
	}
}

func TestForgejoAdmissionRetryableErrors(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name  string
		setup func(h *fjHarness)
	}{
		{"get issue", func(h *fjHarness) { h.api.getErr = boom }},
		{"pr lookup failure is not absence", func(h *fjHarness) { h.api.prErr = boom }},
		{"queue persistence", func(h *fjHarness) { h.queue.queueErr = boom }},
		{"daily cap", func(h *fjHarness) { h.queue.queueErr = &executor.DailyTicketCapHold{} }},
		{"billing hold", func(h *fjHarness) { h.queue.queueErr = executor.ErrBillingKeyHold }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newFjHarness(t)
			h.api.issues[1] = fjIssue(1, "open", "pilot")
			tt.setup(h)
			if err := h.svc.admitNumber(context.Background(), fjTestRepo, 1); err == nil {
				t.Fatal("want retryable error")
			}
			if n := len(h.queue.queued()); n != 0 {
				t.Fatalf("queued %d", n)
			}
		})
	}
}

func TestForgejoDeletedIssueIsIgnored(t *testing.T) {
	h := newFjHarness(t)
	if err := h.svc.admitNumber(context.Background(), fjTestRepo, 42); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestForgejoProjectBaseBranchOverride(t *testing.T) {
	h := newFjHarness(t)
	h.rt.repos[fjTestRepo].project.DefaultBranch = "develop"
	h.api.issues[2] = fjIssue(2, "open", "pilot")
	if err := h.svc.admitNumber(context.Background(), fjTestRepo, 2); err != nil {
		t.Fatal(err)
	}
	if ts := h.queue.queued(); len(ts) != 1 || ts[0].BaseBranch != "develop" {
		t.Fatalf("tasks = %+v", ts)
	}
}

// A webhook and the startup scan racing for the same issue queue it once.
func TestForgejoWebhookAndScanRaceQueueOnce(t *testing.T) {
	for i := 0; i < 20; i++ {
		h := newFjHarness(t)
		h.queue.delay = 2 * time.Millisecond
		h.api.issues[3] = fjIssue(3, "open", "pilot")
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); _ = h.svc.admitNumber(context.Background(), fjTestRepo, 3) }()
		go func() { defer wg.Done(); _ = h.svc.admitNumber(context.Background(), fjTestRepo, 3) }()
		go func() { defer wg.Done(); h.svc.scanRepo(context.Background(), h.rt.repos[fjTestRepo]) }()
		wg.Wait()
		if n := len(h.queue.queued()); n != 1 {
			t.Fatalf("iteration %d: queued %d times", i, n)
		}
	}
}

func TestForgejoScanContinuesAcrossReposAndRetries(t *testing.T) {
	cfg, _ := fjConfig("acme/one", "acme/two")
	apis := map[string]*fjFakeAPI{
		"acme/one": {issues: map[int]forgejo.Issue{}, prs: map[string]forgejo.PullRequest{}, listErrs: []error{&forgejo.APIError{Op: "list", Status: 500}}},
		"acme/two": {issues: map[int]forgejo.Issue{}, prs: map[string]forgejo.PullRequest{}, listErrs: []error{&forgejo.APIError{Op: "list", Status: 403}}},
	}
	apis["acme/one"].issues[1] = fjIssue(1, "open", "pilot")
	apis["acme/two"].issues[1] = fjIssue(1, "open", "pilot")
	rt, err := newForgejoRuntimeWith(cfg, executor.NewRunner(), func(_, _, repo string) (forgejoAPI, error) { return apis[repo], nil })
	if err != nil {
		t.Fatal(err)
	}
	old := forgejoScanRetryDelays
	forgejoScanRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { forgejoScanRetryDelays = old }()

	q := newFjQueue()
	svc := &forgejoService{
		ctx: context.Background(), rt: rt, queue: q,
		inflight: map[string]*forgejoAdmission{}, watching: map[string]struct{}{},
		originCheck: func(context.Context, string, string, string) error { return nil },
	}
	svc.scanOnce()

	if apis["acme/one"].listCalls != 2 {
		t.Fatalf("transient 500 must be retried once, calls = %d", apis["acme/one"].listCalls)
	}
	if apis["acme/two"].listCalls != 1 {
		t.Fatalf("403 must not be retried, calls = %d", apis["acme/two"].listCalls)
	}
	ts := q.queued()
	if len(ts) != 1 || ts[0].ID != forgejoTaskID(fjTestBase, "acme/one", 1) {
		t.Fatalf("queued = %+v", ts)
	}
}

func TestForgejoCompletionCommentPostedWithPRURL(t *testing.T) {
	h := newFjHarness(t)
	h.queue.waitCh = make(chan struct{})
	h.queue.waitExec = &memory.Execution{Status: "completed", PRUrl: "https://forge.example.test/acme/app/pulls/9"}
	h.api.issues[5] = fjIssue(5, "open", "pilot")
	if err := h.svc.admitNumber(context.Background(), fjTestRepo, 5); err != nil {
		t.Fatal(err)
	}
	close(h.queue.waitCh)
	fjWaitFor(t, func() bool { h.api.mu.Lock(); defer h.api.mu.Unlock(); return len(h.api.comments) == 1 })
	if c := h.api.comments[0]; !strings.Contains(c, "pulls/9") {
		t.Fatalf("comment = %q", c)
	}
}

func TestForgejoCommentRetriesAndNeverRequeues(t *testing.T) {
	old := forgejoCommentRetryDelays
	forgejoCommentRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { forgejoCommentRetryDelays = old }()

	h := newFjHarness(t)
	h.api.commentErrs = []error{&forgejo.APIError{Op: "add comment", Status: 502}, &forgejo.APIError{Op: "add comment", Status: 502}, nil}
	h.svc.notify(h.rt.repos[fjTestRepo], 5, &memory.Execution{Status: "completed", PRUrl: "u"})
	if len(h.api.comments) != 1 {
		t.Fatalf("comments = %v", h.api.comments)
	}

	h2 := newFjHarness(t)
	h2.api.commentErrs = []error{&forgejo.APIError{Op: "add comment", Status: 403}}
	h2.svc.notify(h2.rt.repos[fjTestRepo], 5, &memory.Execution{Status: "completed", PRUrl: "u"})
	if len(h2.api.comments) != 0 || len(h2.queue.queued()) != 0 {
		t.Fatal("a failed comment must not retry on 403 nor queue work")
	}
}

func TestForgejoCompletionCommentText(t *testing.T) {
	tests := []struct {
		exec *memory.Execution
		want string // substring; "" means silent
	}{
		{&memory.Execution{Status: "completed", PRUrl: "https://x/pulls/1"}, "https://x/pulls/1"},
		{&memory.Execution{Status: "completed"}, "without opening"},
		{&memory.Execution{Status: "failed", Error: "secret-token-in-error"}, "status: failed"},
		{&memory.Execution{Status: "cancelled"}, ""},
	}
	for _, tt := range tests {
		got := forgejoCompletionComment(tt.exec)
		if tt.want == "" && got != "" && executor.IsTerminalByDesignStatus(tt.exec.Status) {
			t.Fatalf("%s: want silent, got %q", tt.exec.Status, got)
		}
		if tt.want != "" && !strings.Contains(got, tt.want) {
			t.Fatalf("%s: got %q want %q", tt.exec.Status, got, tt.want)
		}
		if strings.Contains(got, "secret-token") {
			t.Fatalf("raw error leaked: %q", got)
		}
	}
}

func TestForgejoRestoreWatchersAttachesRecoveredExecutions(t *testing.T) {
	h := newFjHarness(t)
	h.queue.waitCh = make(chan struct{})
	h.queue.waitExec = &memory.Execution{Status: "completed", PRUrl: "https://x/pulls/2"}
	h.svc.store = fjFakeStore{execs: map[string][]*memory.Execution{h.path: {
		{ID: "e1", TaskID: forgejoTaskID(fjTestBase, fjTestRepo, 11)},
		{ID: "e2", TaskID: "GH-5"},                                      // foreign
		{ID: "e3", TaskID: forgejoTaskID(fjTestBase, "other/repo", 12)}, // other scope
	}}}
	h.svc.restoreWatchers()
	close(h.queue.waitCh)
	fjWaitFor(t, func() bool { h.api.mu.Lock(); defer h.api.mu.Unlock(); return len(h.api.comments) == 1 })
	time.Sleep(20 * time.Millisecond)
	if len(h.api.comments) != 1 {
		t.Fatalf("comments = %v", h.api.comments)
	}
}

func fjWaitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestForgejoRuntimeRegistersProjectPRCreator(t *testing.T) {
	cfg, paths := fjConfig(fjTestRepo)
	runner := executor.NewRunner()
	if _, err := newForgejoRuntimeWith(cfg, runner, func(string, string, string) (forgejoAPI, error) { return &fjFakeAPI{}, nil }); err != nil {
		t.Fatal(err)
	}
	if !runner.HasPRCreator(executor.ProjectPRCreatorKey(paths[0])) {
		t.Fatal("project PR creator not registered")
	}
}

func TestForgejoRuntimeDisabledOrInvalid(t *testing.T) {
	cfg, _ := fjConfig(fjTestRepo)
	cfg.Adapters.Forgejo.Enabled = false
	if rt, err := newForgejoRuntime(cfg, executor.NewRunner()); rt != nil || err != nil {
		t.Fatalf("disabled: rt=%v err=%v", rt, err)
	}
	cfg.Adapters.Forgejo.Enabled = true
	cfg.Projects = nil
	if _, err := newForgejoRuntime(cfg, executor.NewRunner()); err == nil {
		t.Fatal("enabled without any forgejo project must fail")
	}
}

func TestValidateForgejoStartup(t *testing.T) {
	cfg, _ := fjConfig(fjTestRepo)
	cfg.Gateway = &gateway.Config{Host: "127.0.0.1", Port: 9090}
	if err := validateForgejoStartup(cfg, false); err != nil {
		t.Fatal(err)
	}
	if err := validateForgejoStartup(cfg, true); err == nil || !strings.Contains(err.Error(), "--no-gateway") {
		t.Fatalf("--no-gateway must be rejected, got %v", err)
	}
	cfg.Gateway = nil
	if err := validateForgejoStartup(cfg, false); err == nil {
		t.Fatal("missing gateway must be rejected")
	}
	cfg.Adapters.Forgejo.Enabled = false
	if err := validateForgejoStartup(cfg, true); err != nil {
		t.Fatalf("disabled adapter must not constrain startup: %v", err)
	}
}

func TestForgejoStartRequiresDispatcher(t *testing.T) {
	cfg, _ := fjConfig(fjTestRepo)
	rt, err := newForgejoRuntimeWith(cfg, executor.NewRunner(), func(string, string, string) (forgejoAPI, error) { return &fjFakeAPI{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background(), nil, nil); err == nil {
		t.Fatal("Start without a dispatcher must fail")
	}
}

func TestMatchForgejoOrigin(t *testing.T) {
	tests := []struct {
		remote string
		ok     bool
	}{
		{"https://forge.example.test/acme/app.git", true},
		{"https://forge.example.test/acme/app", true},
		{"https://user@forge.example.test/acme/app.git", true},
		{"ssh://git@forge.example.test:2222/acme/app.git", true},
		{"git@forge.example.test:acme/app.git", true},
		{"https://forge.example.test/acme/other.git", false},
		{"https://evil.example.test/acme/app.git", false},
		{"https://github.com/acme/app.git", false},
		{"/srv/git/app.git", false},
		{"http://forge.example.test/acme/app.git", false},       // scheme differs
		{"https://forge.example.test:8443/acme/app.git", false}, // port differs
		{"https://forge.example.test:443/acme/app.git", true},   // explicit default port
		{"https://forge.example.test/git/acme/app.git", false},  // unexpected prefix
		{"git://forge.example.test/acme/app.git", false},        // not http(s)/ssh
		{"file://forge.example.test/acme/app.git", false},       // not http(s)/ssh
		{"ssh://git@evil.example.test/acme/app.git", false},     // host differs
		{"https://forge.example.test/acme/app.git?x=1", false},  // query
		{"", false},
	}
	for _, tt := range tests {
		err := matchForgejoOrigin(tt.remote, fjTestBase, fjTestRepo)
		if (err == nil) != tt.ok {
			t.Errorf("%q: err = %v, want ok=%v", tt.remote, err, tt.ok)
		}
	}
	if err := matchForgejoOrigin("https://forge.example.test/git/acme/app.git", fjTestBase+"/git", fjTestRepo); err != nil {
		t.Errorf("subpath instance: %v", err)
	}
	for _, bad := range []string{
		"https://forge.example.test/acme/app.git",      // prefix missing
		"https://forge.example.test/gitx/acme/app.git", // prefix not segment aligned
	} {
		if err := matchForgejoOrigin(bad, fjTestBase+"/git", fjTestRepo); err == nil {
			t.Errorf("%q must not match a /git instance", bad)
		}
	}
	if err := matchForgejoOrigin("http://127.0.0.1:3000/acme/app.git", "http://127.0.0.1:3000", fjTestRepo); err != nil {
		t.Errorf("explicit port: %v", err)
	}
	if err := matchForgejoOrigin("http://127.0.0.1:3001/acme/app.git", "http://127.0.0.1:3000", fjTestRepo); err == nil {
		t.Error("other port must fail")
	}
}

func TestForgejoPRCreatorLinksIssue(t *testing.T) {
	api := &fjFakeAPI{}
	c := &forgejoPRCreator{api: api, baseURL: fjTestBase, repo: fjTestRepo}
	id := forgejoTaskID(fjTestBase, fjTestRepo, 8)
	url, err := c.CreatePR(context.Background(), "pilot/forgejo-8", "main", "t", "summary\n\nSource: forgejo "+id)
	if err != nil || url == "" {
		t.Fatalf("url=%q err=%v", url, err)
	}
	if got := api.created[0]; !strings.Contains(got, "Source: "+fjTestBase+"/"+fjTestRepo+"/issues/8") {
		t.Fatalf("body = %q", got)
	}
	// A task ID of another repo's scope is left untouched.
	api.created = nil
	other := forgejoTaskID(fjTestBase, "x/y", 8)
	_, _ = c.CreatePR(context.Background(), "a", "b", "t", "Source: forgejo "+other)
	if !strings.Contains(api.created[0], other) {
		t.Fatalf("foreign scope rewritten: %q", api.created[0])
	}
}

func TestForgejoTaskIdentityIsScoped(t *testing.T) {
	a := forgejoTaskID(fjTestBase, "acme/app", 1)
	b := forgejoTaskID(fjTestBase, "acme/other", 1)
	c := forgejoTaskID("https://other.example.test", "acme/app", 1)
	if a == b || a == c || a != forgejoTaskID(fjTestBase, "acme/app", 1) {
		t.Fatalf("ids not stable/scoped: %s %s %s", a, b, c)
	}
	if forgejoBranch(12) != "pilot/forgejo-12" {
		t.Fatal(forgejoBranch(12))
	}
}

// Real memory store and dispatcher: a webhook delivery, a duplicate delivery and
// the startup scan racing for one issue leave exactly one durable queued row,
// and a restart (new dispatcher, same ledger) does not queue it again.
func TestForgejoRealDispatcherQueuesOnceAcrossRestart(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	cfg, paths := fjConfig(fjTestRepo)
	api := &fjFakeAPI{issues: map[int]forgejo.Issue{4: fjIssue(4, "open", "pilot")}, prs: map[string]forgejo.PullRequest{}}
	factory := func(string, string, string) (forgejoAPI, error) { return api, nil }

	boot := func() (*forgejoRuntime, *executor.Dispatcher, context.CancelFunc) {
		runner := executor.NewRunner()
		rt, err := newForgejoRuntimeWith(cfg, runner, factory)
		if err != nil {
			t.Fatal(err)
		}
		d := executor.NewDispatcher(store, runner, nil)
		d.PauseAdmission() // keep workers from running the (fake) executor
		ctx, cancel := context.WithCancel(context.Background())
		if err := d.Start(ctx); err != nil {
			t.Fatal(err)
		}
		svc := &forgejoService{
			ctx: ctx, rt: rt, queue: d, store: store,
			inflight: map[string]*forgejoAdmission{}, watching: map[string]struct{}{},
			originCheck: func(context.Context, string, string, string) error { return nil },
		}
		rt.svc.Store(svc)
		return rt, d, func() { cancel(); d.Stop() }
	}

	rt, d, stop := boot()
	for i := 0; i < 2; i++ {
		if code := fjPost(rt.Handler(), fjTestRepo, 4, "opened", true); code != 204 {
			t.Fatalf("delivery %d: code %d", i, code)
		}
	}
	rt.svc.Load().scanRepo(context.Background(), rt.repos[fjTestRepo])
	id := forgejoTaskID(fjTestBase, fjTestRepo, 4)
	if !d.IsActive(id, paths[0]) {
		t.Fatal("task not durably queued")
	}
	rows, err := store.ListExecutionsForTask(id, paths[0])
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d err=%v, want exactly one", len(rows), err)
	}
	stop()

	rt2, d2, stop2 := boot()
	defer stop2()
	rt2.svc.Load().scanRepo(context.Background(), rt2.repos[fjTestRepo])
	if code := fjPost(rt2.Handler(), fjTestRepo, 4, "label_updated", true); code != 204 {
		t.Fatalf("post-restart delivery: code %d", code)
	}
	rows, err = store.ListExecutionsForTask(id, paths[0])
	if err != nil || len(rows) != 1 {
		t.Fatalf("after restart rows = %d err=%v, want still one", len(rows), err)
	}
	_ = d2
}

// A duplicate must not be acknowledged before the first admission is durable.
func TestForgejoDuplicateWaitsForFirstAdmission(t *testing.T) {
	h := newFjHarness(t)
	h.queue.enter = make(chan struct{}, 4)
	h.queue.gate = make(chan struct{})
	h.api.issues[4] = fjIssue(4, "open", "pilot")

	codes := make(chan int, 2)
	go func() { codes <- fjPost(h.rt.Handler(), fjTestRepo, 4, "opened", true) }()
	<-h.queue.enter // first admission is inside the queue write
	go func() { codes <- fjPost(h.rt.Handler(), fjTestRepo, 4, "label_updated", true) }()

	select {
	case c := <-codes:
		t.Fatalf("acknowledged (%d) before the first admission finished", c)
	case <-time.After(150 * time.Millisecond):
	}
	close(h.queue.gate)
	for i := 0; i < 2; i++ {
		if c := <-codes; c != 204 {
			t.Fatalf("code = %d, want 204", c)
		}
	}
	if n := len(h.queue.queued()); n != 1 {
		t.Fatalf("queued %d times", n)
	}
}

// If the first admission fails, the duplicate must fail too (503), not 204.
func TestForgejoDuplicateFailsWhenFirstFails(t *testing.T) {
	h := newFjHarness(t)
	h.queue.enter = make(chan struct{}, 4)
	h.queue.gate = make(chan struct{})
	h.queue.queueErr = errors.New("boom")
	h.api.issues[4] = fjIssue(4, "open", "pilot")

	codes := make(chan int, 2)
	go func() { codes <- fjPost(h.rt.Handler(), fjTestRepo, 4, "opened", true) }()
	<-h.queue.enter
	go func() { codes <- fjPost(h.rt.Handler(), fjTestRepo, 4, "label_updated", true) }()
	time.Sleep(50 * time.Millisecond)
	close(h.queue.gate)
	for i := 0; i < 2; i++ {
		if c := <-codes; c != 503 {
			t.Fatalf("code = %d, want 503", c)
		}
	}
	if n := len(h.queue.queued()); n != 0 {
		t.Fatalf("queued %d", n)
	}
	// The failure is not cached: a later retry admits normally.
	h.queue.queueErr = nil
	if c := fjPost(h.rt.Handler(), fjTestRepo, 4, "label_updated", true); c != 204 || len(h.queue.queued()) != 1 {
		t.Fatalf("retry code = %d queued = %d", c, len(h.queue.queued()))
	}
}

// A duplicate stops waiting when its own request is cancelled.
func TestForgejoDuplicateWaitHonoursRequestCancel(t *testing.T) {
	h := newFjHarness(t)
	h.queue.enter = make(chan struct{}, 4)
	h.queue.gate = make(chan struct{})
	h.api.issues[4] = fjIssue(4, "open", "pilot")
	first := make(chan error, 1)
	go func() { first <- h.svc.admitNumber(context.Background(), fjTestRepo, 4) }()
	<-h.queue.enter

	rctx, cancel := context.WithCancel(context.Background())
	dup := make(chan error, 1)
	go func() { dup <- h.svc.admitNumber(rctx, fjTestRepo, 4) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-dup:
		if err == nil {
			t.Fatal("cancelled duplicate must not report success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("duplicate ignored request cancellation")
	}
	close(h.queue.gate)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if n := len(h.queue.queued()); n != 1 {
		t.Fatalf("queued %d", n)
	}
}

func TestForgejoDispatcherDroppedPickupOutcomes(t *testing.T) {
	h := newFjHarness(t)
	h.api.issues[1] = fjIssue(1, "open", "pilot")
	h.queue.outcome = &executor.QueueOutcome{ClaimLostOwnerStatus: "running"}
	if err := h.svc.admitNumber(context.Background(), fjTestRepo, 1); err != nil {
		t.Fatalf("live owner: %v", err)
	}
	h.queue.outcome = &executor.QueueOutcome{}
	if err := h.svc.admitNumber(context.Background(), fjTestRepo, 1); err == nil {
		t.Fatal("no id and no owner must be retryable")
	}
}

// The scan re-reads each listed issue; a stale list entry is not admitted.
func TestForgejoScanVerifiesLiveState(t *testing.T) {
	h := newFjHarness(t)
	stale := &fjStaleAPI{fjFakeAPI: h.api}
	h.api.issues[1] = fjIssue(1, "closed", "pilot") // live: closed
	h.api.issues[2] = fjIssue(2, "open", "pilot")
	stale.listed = []forgejo.Issue{fjIssue(1, "open", "pilot"), fjIssue(2, "open", "pilot")}
	r := h.rt.repos[fjTestRepo]
	r.api = stale
	h.svc.scanRepo(context.Background(), r)
	ts := h.queue.queued()
	if len(ts) != 1 || ts[0].SourceIssueID != "2" {
		t.Fatalf("queued = %+v", ts)
	}
}

type fjStaleAPI struct {
	*fjFakeAPI
	listed []forgejo.Issue
}

func (f *fjStaleAPI) ListOpenIssues(_ context.Context, _ string, visit func(forgejo.Issue) error) error {
	for _, is := range f.listed {
		if err := visit(is); err != nil {
			return err
		}
	}
	return nil
}

func TestCheckForgejoOriginReadsFetchAndPushURLs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	good := "https://forge.example.test/acme/app.git"
	tests := []struct {
		name  string
		steps [][]string
		ok    bool
	}{
		{"fetch only", [][]string{{"remote", "add", "origin", good}}, true},
		{"matching pushurl", [][]string{{"remote", "add", "origin", good}, {"remote", "set-url", "--push", "origin", "git@forge.example.test:acme/app.git"}}, true},
		{"mismatched pushurl", [][]string{{"remote", "add", "origin", good}, {"remote", "set-url", "--push", "origin", "https://forge.example.test/acme/other.git"}}, false},
		{"mismatched push port", [][]string{{"remote", "add", "origin", good}, {"remote", "set-url", "--push", "origin", "https://forge.example.test:8443/acme/app.git"}}, false},
		{"extra fetch url", [][]string{{"remote", "add", "origin", good}, {"remote", "set-url", "--add", "origin", "https://forge.example.test/acme/other.git"}}, false},
		{"extra push url", [][]string{{"remote", "add", "origin", good}, {"remote", "set-url", "--push", "origin", good}, {"remote", "set-url", "--add", "--push", "origin", good}}, false},
		{"wrong repo", [][]string{{"remote", "add", "origin", "https://forge.example.test/acme/other.git"}}, false},
		{"no origin", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			run := func(args ...string) {
				cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			run("init", "-q")
			for _, s := range tt.steps {
				run(s...)
			}
			err := checkForgejoOrigin(context.Background(), dir, fjTestBase, fjTestRepo)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}
