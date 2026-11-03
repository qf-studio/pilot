package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// GH-6021: fast-follow to the billing-mode work (GH-6016).

// progressRecorder collects the phases emitted per task.
type progressRecorder struct {
	mu     sync.Mutex
	phases map[string][]string
}

func newProgressRecorder() *progressRecorder {
	return &progressRecorder{phases: make(map[string][]string)}
}

func (p *progressRecorder) callback(taskID, phase string, _ int, _ string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phases[taskID] = append(p.phases[taskID], phase)
}

func (p *progressRecorder) has(taskID, phase string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ph := range p.phases[taskID] {
		if ph == phase {
			return true
		}
	}
	return false
}

// billingWorkerRig is an unstarted dispatcher plus a worker driven by hand
// with processQueue, so a test controls exactly which rows the worker sees.
type billingWorkerRig struct {
	d        *Dispatcher
	w        *ProjectWorker
	progress *progressRecorder
	proj     string
}

func newBillingWorkerRig(t *testing.T, billing *BillingConfig) *billingWorkerRig {
	t.Helper()
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)

	runner := NewRunnerWithBackend(&countingBackend{})
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{Type: BackendTypeClaudeCode, SkipSelfReview: true, ClaudeCode: &ClaudeCodeConfig{Billing: billing}}
	runner.SetMonitor(NewMonitor())
	rec := newProgressRecorder()
	runner.OnProgress(rec.callback)

	d := NewDispatcher(store, runner, nil)
	t.Cleanup(d.stopBillingState)
	proj := "/tmp/pilot-gh6021-" + strings.ReplaceAll(t.Name(), "/", "-")
	w := NewProjectWorker(proj, store, runner, d.log)
	w.billingCredentialFor = d.billingCredentialForRun
	w.discardBillingCredential = d.discardBillingCredential
	return &billingWorkerRig{d: d, w: w, progress: rec, proj: proj}
}

func (r *billingWorkerRig) stashed(execID string) *BillingCredential {
	r.d.billingMu.Lock()
	defer r.d.billingMu.Unlock()
	return r.d.billingCreds[execID]
}

func (r *billingWorkerRig) stashCount() int {
	r.d.billingMu.Lock()
	defer r.d.billingMu.Unlock()
	return len(r.d.billingCreds)
}

// A billing-key failure in the worker closes the dashboard card the way the
// normal failure branch does: a Failed progress event and monitor.Fail.
func TestBillingKeyFailure_WorkerEmitsFailedAndMonitorFail(t *testing.T) {
	skipWithoutShell(t)
	rig := newBillingWorkerRig(t, apiKeyConfig("exit 1"))
	task := &Task{ID: "GH-6021-A", Title: "needs key", ProjectPath: rig.proj}
	rig.d.runner.monitor.Register(task.ID, task.Title, "")

	// No stash entry: the worker resolves at run time and the command fails.
	execID, err := rig.d.beginClaimWithGenerationRetry(task, ExecStatusQueued)
	if err != nil || execID == "" {
		t.Fatalf("queue row = %q, %v", execID, err)
	}
	rig.w.processQueue(context.Background())

	if !rig.progress.has(task.ID, "Running") {
		t.Error("worker never emitted Running; the test did not reach the credential step")
	}
	if !rig.progress.has(task.ID, "Failed") {
		t.Errorf("no Failed progress event recorded; phases = %v", rig.progress.phases[task.ID])
	}
	state, ok := rig.d.runner.monitor.Get(task.ID)
	if !ok || state.Status != StatusFailed {
		t.Errorf("monitor state = %+v (found %v), want status failed", state, ok)
	}
	if ok && !strings.HasPrefix(state.Error, "billing_key_unavailable") {
		t.Errorf("monitor error = %q, want the billing_key_unavailable text", state.Error)
	}
	row, err := rig.d.store.GetExecution(execID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != string(ExecStatusInfra) || !strings.HasPrefix(row.Error, "billing_key_unavailable") {
		t.Errorf("row = status %q error %q, want infra / billing_key_unavailable", row.Status, row.Error)
	}
}

// billingOutageRig drives outage and resume cycles through QueueTask.
type billingOutageRig struct {
	t       *testing.T
	d       *Dispatcher
	clock   *fakePauseClock
	keyFile string
	task    *Task
}

func newBillingOutageRig(t *testing.T, taskID string) *billingOutageRig {
	t.Helper()
	skipWithoutShell(t)
	keyFile := filepath.Join(t.TempDir(), "key")
	clock := newFakePauseClock(usageLimitTestNow)
	d, _ := newBillingDispatcher(t, &countingBackend{}, apiKeyConfig("cat "+keyFile+" 2>/dev/null || exit 1"), clock)
	return &billingOutageRig{
		t: t, d: d, clock: clock, keyFile: keyFile,
		task: &Task{ID: taskID, Title: "outage", ProjectPath: "/tmp/pilot-gh6021-outage"},
	}
}

// outage queues the task with the key missing and returns its latest row.
func (r *billingOutageRig) outage() *memory.Execution {
	r.t.Helper()
	_ = os.Remove(r.keyFile)
	if _, err := r.d.QueueTask(context.Background(), r.task); err == nil {
		r.t.Fatal("QueueTask succeeded without a key")
	}
	row, err := r.d.store.GetLatestExecutionByTaskID(r.task.ID, r.task.ProjectPath)
	if err != nil {
		r.t.Fatalf("no outage row: %v", err)
	}
	return row
}

// resume makes the key available and lets the retry timer lift the pause.
func (r *billingOutageRig) resume() {
	r.t.Helper()
	if err := os.WriteFile(r.keyFile, []byte(testBillingKey), 0o600); err != nil {
		r.t.Fatal(err)
	}
	r.clock.Advance(BillingKeyRetryInterval)
	if r.d.IsAdmissionPaused() {
		r.t.Fatalf("admission still paused after the key resolved: %+v", r.d.AdmissionStatus())
	}
}

func (r *billingOutageRig) backoff() (drops int, found bool) {
	r.t.Helper()
	drops, _, found, err := r.d.RepickBackoffState(repickBackoffKey(r.task.ProjectPath, r.task.ID))
	if err != nil {
		r.t.Fatal(err)
	}
	return drops, found
}

// An outage row is infra-class, so the pickups that follow it owe no repick
// backoff and cannot trip the identical-failure streak, however many outages
// repeat with the same error text.
func TestBillingKeyOutage_RowIsInfraNoBackoffNoStreak(t *testing.T) {
	rig := newBillingOutageRig(t, "GH-6021-B")

	row1 := rig.outage()
	if row1.Status != string(ExecStatusInfra) || !strings.HasPrefix(row1.Error, "billing_key_unavailable") {
		t.Fatalf("outage row = status %q error %q, want infra / billing_key_unavailable", row1.Status, row1.Error)
	}
	rig.resume()

	// The row is infra, not done: the next pickup is granted a fresh generation.
	gen, retry, err := rig.d.nextRetryGeneration(rig.task.ID, rig.task.ProjectPath)
	if err != nil || !retry || gen != 1 {
		t.Fatalf("nextRetryGeneration = %d, %v, %v; want 1, true, nil", gen, retry, err)
	}
	if !rig.d.priorClaimWasInfra(rig.task.ID, rig.task.ProjectPath) {
		t.Error("the outage row is not recognised as infra-class")
	}

	// A second outage with the identical error text.
	row2 := rig.outage()
	if row2.ID == row1.ID {
		t.Fatal("the second outage did not record its own row")
	}
	if row2.Status != string(ExecStatusInfra) || row2.Error != row1.Error {
		t.Fatalf("second outage row = status %q error %q, want infra with the identical text %q", row2.Status, row2.Error, row1.Error)
	}
	rig.resume()

	// A third pickup after two identical outages is not a failure streak.
	if streak, _ := rig.d.priorClaimsHadIdenticalFailureStreak(rig.task.ID, rig.task.ProjectPath); streak {
		t.Fatal("two identical outage rows count as an identical-failure streak")
	}
	execID, err := rig.d.QueueTask(context.Background(), rig.task)
	if err != nil || execID == "" {
		t.Fatalf("QueueTask after two outages = %q, %v; want a fresh row", execID, err)
	}
	if drops, found := rig.backoff(); found && drops != 0 {
		t.Errorf("repick backoff consecutive_drops = %d after outages, want none", drops)
	}
	latest, err := rig.d.store.GetExecution(execID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status == string(ExecStatusStalled) {
		t.Errorf("task stalled after outages: %q", latest.Error)
	}
}

// Outage rows count toward the infra cap, and the cap still parks the task.
func TestBillingKeyOutage_InfraCapParks(t *testing.T) {
	rig := newBillingOutageRig(t, "GH-6021-C")
	key := repickBackoffKey(rig.task.ProjectPath, rig.task.ID)
	infra := func() int {
		n, _, err := rig.d.InfraDropCount(key)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	rig.outage()
	rig.resume()
	if err := rig.d.SetInfraDropCount(key, dispatcherInfraRepickCap-1); err != nil {
		t.Fatal(err)
	}

	// The next outage row is granted and takes the counter to the cap.
	row2 := rig.outage()
	if row2.Status != string(ExecStatusInfra) {
		t.Fatalf("second outage row status = %q, want infra", row2.Status)
	}
	if got := infra(); got != dispatcherInfraRepickCap {
		t.Fatalf("infra drop count = %d after the outage row, want %d", got, dispatcherInfraRepickCap)
	}
	rig.resume()

	// At the cap the task is parked, not re-admitted.
	execID, err := rig.d.QueueTask(context.Background(), rig.task)
	if err != nil {
		t.Fatalf("QueueTask at the cap: %v", err)
	}
	if execID != "" {
		t.Errorf("QueueTask at the cap returned row %q, want the task parked", execID)
	}
	latest, err := rig.d.store.GetLatestExecutionByTaskID(rig.task.ID, rig.task.ProjectPath)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != string(ExecStatusStalled) || !strings.Contains(latest.Error, "infra repick cap reached") {
		t.Errorf("latest row = status %q error %q, want stalled by the infra cap", latest.Status, latest.Error)
	}
}

// A queued row canceled outside this process (pilot task cancel) leaves its
// stashed key behind; the stale-recovery loop zeroes it.
func TestBillingCredential_StaleSweepZeroesOrphans(t *testing.T) {
	skipWithoutShell(t)
	cfg := DefaultDispatcherConfig()
	cfg.StaleRecoveryInterval = 20 * time.Millisecond
	store, cleanup := setupTestStore(t)
	defer cleanup()
	runner := NewRunnerWithBackend(&countingBackend{})
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{Type: BackendTypeClaudeCode, ClaudeCode: &ClaudeCodeConfig{Billing: apiKeyConfig("printf " + testBillingKey)}}
	d := NewDispatcher(store, runner, cfg)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	// Begin the row directly so no worker consumes the stash.
	task := &Task{ID: "GH-6021-D", Title: "orphan", ProjectPath: "/tmp/pilot-gh6021-sweep"}
	execID, err := d.beginWithGenerationRetry(task, ExecStatusQueued)
	if err != nil || execID == "" {
		t.Fatalf("beginWithGenerationRetry = %q, %v", execID, err)
	}
	d.billingMu.Lock()
	cred := d.billingCreds[execID]
	d.billingMu.Unlock()
	if cred == nil || len(cred.Env()) != 1 {
		t.Fatalf("no stashed credential for %s", execID)
	}

	// `pilot task cancel` runs lifecycle.Cancel in another process: only the row changes.
	if err := store.UpdateExecutionStatus(execID, string(ExecStatusCanceled)); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d.billingMu.Lock()
		n := len(d.billingCreds)
		d.billingMu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	d.billingMu.Lock()
	left := len(d.billingCreds)
	d.billingMu.Unlock()
	if left != 0 {
		t.Fatalf("%d credentials still stashed after the stale-recovery tick", left)
	}
	if env := cred.Env(); len(env) != 0 {
		t.Errorf("orphaned credential not zeroed: %v", env)
	}
}

// The sweep keeps the credential of a row that is still queued.
func TestBillingCredential_StaleSweepKeepsQueued(t *testing.T) {
	skipWithoutShell(t)
	rig := newBillingWorkerRig(t, apiKeyConfig("printf "+testBillingKey))
	task := &Task{ID: "GH-6021-E", Title: "queued", ProjectPath: rig.proj}
	execID, err := rig.d.beginWithGenerationRetry(task, ExecStatusQueued)
	if err != nil || execID == "" {
		t.Fatalf("beginWithGenerationRetry = %q, %v", execID, err)
	}
	rig.d.sweepBillingCredentials()
	if cred := rig.stashed(execID); cred == nil || len(cred.Env()) != 1 {
		t.Fatalf("sweep dropped the credential of a queued row: %v", cred)
	}
}

// A worker that finishes a queued row without running it discards the stashed
// key instead of leaving it for the process lifetime.
func TestBillingCredential_WorkerSkipPathsDiscard(t *testing.T) {
	skipWithoutShell(t)

	t.Run("merged-PR short-circuit", func(t *testing.T) {
		orig := mergedPRPreflightCheck
		mergedPRPreflightCheck = func(context.Context, string, string) (string, error) {
			return "https://github.com/o/r/pull/9", nil
		}
		t.Cleanup(func() { mergedPRPreflightCheck = orig })

		rig := newBillingWorkerRig(t, apiKeyConfig("printf "+testBillingKey))
		task := &Task{ID: "GH-6021-F", Title: "already merged", ProjectPath: rig.proj, Branch: "pilot/GH-6021-F"}
		execID, err := rig.d.beginWithGenerationRetry(task, ExecStatusQueued)
		if err != nil || execID == "" {
			t.Fatalf("beginWithGenerationRetry = %q, %v", execID, err)
		}
		cred := rig.stashed(execID)
		if cred == nil {
			t.Fatal("no stashed credential")
		}
		rig.w.processQueue(context.Background())

		row, err := rig.d.store.GetExecution(execID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != string(ExecStatusCompleted) {
			t.Fatalf("row status = %q, want completed by the merged-PR short-circuit", row.Status)
		}
		if rig.stashCount() != 0 || len(cred.Env()) != 0 {
			t.Errorf("credential left behind: stashed=%d env=%v", rig.stashCount(), cred.Env())
		}
	})

	t.Run("terminal-success ledger guard", func(t *testing.T) {
		rig := newBillingWorkerRig(t, apiKeyConfig("printf "+testBillingKey))
		done := &memory.Execution{
			ID: "exec-gh6021-done", TaskID: "GH-6021-G", ProjectPath: rig.proj,
			Status: "completed", PRUrl: "https://github.com/o/r/pull/7", CommitSHA: "abc123",
		}
		if err := rig.d.store.SaveExecution(done); err != nil {
			t.Fatal(err)
		}
		task := &Task{ID: "GH-6021-G", Title: "duplicate", ProjectPath: rig.proj}
		execID, err := rig.d.beginWithGenerationRetry(task, ExecStatusQueued)
		if err != nil || execID == "" {
			// A claim held by the completed row drops the pickup; queue the row by hand.
			execID = "exec-gh6021-dup"
			dup := &memory.Execution{ID: execID, TaskID: task.ID, ProjectPath: rig.proj, Status: "queued"}
			if err := rig.d.store.SaveExecution(dup); err != nil {
				t.Fatal(err)
			}
			rig.d.stashBillingCredential(execID, &BillingCredential{mode: BillingModeAPIKey, key: []byte(testBillingKey)})
		}
		cred := rig.stashed(execID)
		if cred == nil {
			t.Fatal("no stashed credential")
		}
		rig.w.processQueue(context.Background())
		if rig.stashCount() != 0 || len(cred.Env()) != 0 {
			t.Errorf("credential left behind: stashed=%d env=%v", rig.stashCount(), cred.Env())
		}
	})
}

// discardBillingCredential zeroes and removes one entry and ignores an unknown id.
func TestBillingCredential_DiscardZeroesEntry(t *testing.T) {
	rig := newBillingWorkerRig(t, nil)
	cred := &BillingCredential{mode: BillingModeAPIKey, key: []byte(testBillingKey)}
	rig.d.stashBillingCredential("exec-1", cred)
	rig.d.discardBillingCredential("exec-unknown") // no-op
	if rig.stashCount() != 1 {
		t.Fatalf("stash count = %d, want 1", rig.stashCount())
	}
	rig.d.discardBillingCredential("exec-1")
	if rig.stashCount() != 0 || len(cred.Env()) != 0 {
		t.Errorf("after discard: stashed=%d env=%v", rig.stashCount(), cred.Env())
	}
}

// credEnvBackend records the credential env each Execute call sees and fails
// the first call with a retryable error when failFirst is set.
type credEnvBackend struct {
	mu        sync.Mutex
	envs      [][]string
	failFirst bool
	inner     Backend
}

func (b *credEnvBackend) Name() string      { return "cred-env" }
func (b *credEnvBackend) IsAvailable() bool { return true }
func (b *credEnvBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	b.envs = append(b.envs, BillingCredentialFromContext(ctx).Env())
	n := len(b.envs)
	b.mu.Unlock()
	if b.failFirst && n == 1 {
		return nil, gh5718BackendErr{}
	}
	if b.inner != nil {
		return b.inner.Execute(ctx, opts)
	}
	return &BackendResult{Success: true, Output: "done"}, nil
}

func (b *credEnvBackend) seen() [][]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([][]string(nil), b.envs...)
}

func assertRetryCarriesKey(t *testing.T, envs [][]string) {
	t.Helper()
	if len(envs) < 2 {
		t.Fatalf("backend ran %d time(s), want the retry to run too", len(envs))
	}
	want := "ANTHROPIC_API_KEY=" + testBillingKey
	for i, env := range envs {
		if len(env) != 1 || env[0] != want {
			t.Errorf("backend call %d credential env = %v, want [%s]", i+1, env, want)
		}
	}
}

func apiKeyCtx() context.Context {
	cred := &BillingCredential{mode: BillingModeAPIKey, key: []byte(testBillingKey)}
	return WithBillingCredential(context.Background(), cred)
}

// The smart retry re-executes on a context that still carries the key.
func TestBillingRetryCtx_SmartRetryKeepsCredential(t *testing.T) {
	backend := &credEnvBackend{failFirst: true}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{}
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.retrier = NewRetrier(&RetryConfig{
		Enabled:  true,
		APIError: &RetryStrategy{MaxAttempts: 1, InitialBackoff: time.Millisecond, BackoffMultiplier: 1},
	})

	task := &Task{
		ID: "GH-6021-H", Title: "smart retry", Description: "test",
		ProjectPath: t.TempDir(), LocalMode: true, SkipQualityGates: true,
	}
	ctx, cancel := context.WithTimeout(apiKeyCtx(), 30*time.Second)
	defer cancel()
	_, _ = runner.Execute(ctx, task)

	assertRetryCarriesKey(t, backend.seen())
}

// The quality-gate feedback retry re-invokes the backend on a context that
// still carries the key.
func TestBillingRetryCtx_GateFeedbackRetryKeepsCredential(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	backend := &credEnvBackend{inner: &stackingAttemptBackend{}}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: boolPtr(false)}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	checker := &gh5718Checker{fn: func(call int) (*QualityOutcome, error) {
		if call == 1 {
			return &QualityOutcome{Passed: false, ShouldRetry: true, RetryFeedback: "synthetic gate failure", Attempt: 1}, nil
		}
		return &QualityOutcome{Passed: true}, nil
	}}
	runner.SetQualityCheckerFactory(func(string, string, string) QualityChecker { return checker })

	task := &Task{
		ID: "GH-6021-I", Title: "gate feedback retry", Description: "gates fail once",
		ProjectPath: localRepo, Branch: "pilot/GH-6021-I", CreatePR: true,
	}
	ctx, cancel := context.WithTimeout(apiKeyCtx(), 60*time.Second)
	defer cancel()
	_, _ = runner.Execute(ctx, task)

	assertRetryCarriesKey(t, backend.seen())
}

// Subtasks created by decomposition run in the parent's billing mode, so
// their cost is recorded under it and not as unknown.
func TestDecompose_SubtasksInheritBillingMode(t *testing.T) {
	branch := "pilot/GH-6021-J"
	dir := setupPRGuardRepo(t, branch, true)

	decomposer := NewTaskDecomposer(&DecomposeConfig{
		Enabled: true, MinComplexity: "complex", MaxSubtasks: 5, MinDescriptionWords: 10,
	})
	parent := &Task{
		ID:    "GH-6021-J",
		Title: "Refactor authentication system",
		Description: `This task requires refactoring the entire authentication system with multiple changes.

## Task 1: Update the user model

Add new fields for MFA.

## Task 2: Refactor the login endpoint

Support the MFA flow.

## Task 3: Add middleware

Add new middleware for session validation.`,
		ProjectPath: dir,
		Branch:      branch,
		billingMode: "api_key",
	}
	res := decomposer.Decompose(parent)
	if !res.Decomposed || len(res.Subtasks) < 2 {
		t.Fatalf("parent did not decompose: %+v", res)
	}
	for _, st := range res.Subtasks {
		if st.billingMode != "api_key" {
			t.Errorf("subtask %s billingMode = %q, want api_key", st.ID, st.billingMode)
		}
	}

	rec := &fakeMetricsRecorder{}
	runner := NewRunnerWithBackend(&mockFixedBackend{result: &BackendResult{
		Success: true, Output: "done", TokensInput: 100, TokensOutput: 50, Model: "claude-sonnet-test",
	}})
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{UseWorktree: boolPtr(false), SkipSelfReview: true}
	runner.SetMetricsRecorder(rec)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = runner.executeDecomposedTask(ctx, parent, res.Subtasks, dir)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.costCalls) == 0 {
		t.Fatal("no subtask recorded a cost")
	}
	for i, c := range rec.costCalls {
		if c.billingMode != "api_key" {
			t.Errorf("subtask cost %d recorded under billing_mode %q, want api_key", i+1, c.billingMode)
		}
	}
	if len(rec.costCalls) != len(res.Subtasks) {
		t.Logf("%d of %d subtasks recorded cost", len(rec.costCalls), len(res.Subtasks))
	}
}

// Both halves of the api_key test resolve their credential through the
// resolver and the fake command, so the classifier's env is what the resolver
// produced rather than a hand-built value.
func TestBillingMode_ClassifierResolvesViaCommand(t *testing.T) {
	skipWithoutShell(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	if err := os.Unsetenv("ANTHROPIC_API_KEY"); err != nil {
		t.Fatal(err)
	}
	script, dump := fakeEnvCLI(t)
	t.Setenv("PATH", filepath.Dir(script)+string(os.PathListSeparator)+os.Getenv("PATH"))

	cred, err := NewBillingResolver(apiKeyConfig("printf " + testBillingKey)).Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer cred.Zero()
	_, _ = NewEffortClassifier().defaultCmdRunner(WithBillingCredential(context.Background(), cred), "-p", "x")
	if got, _ := billingEnvValue(readEnvDump(t, dump), "ANTHROPIC_API_KEY"); got != testBillingKey {
		t.Errorf("effort classifier ANTHROPIC_API_KEY = %q, want %q", got, testBillingKey)
	}
	if v, ok := os.LookupEnv("ANTHROPIC_API_KEY"); ok {
		t.Errorf("daemon process environment gained ANTHROPIC_API_KEY=%q", v)
	}
}
