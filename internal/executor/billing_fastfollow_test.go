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

// A row between the worker's Transition to running and its key take is
// running: the sweep keeps its key, and the take then empties the stash.
func TestBillingCredential_SweepKeepsRunningRowUntilTaken(t *testing.T) {
	skipWithoutShell(t)
	rig := newBillingWorkerRig(t, apiKeyConfig("printf "+testBillingKey))
	task := &Task{ID: "GH-6026-A", Title: "running", ProjectPath: rig.proj}
	execID, err := rig.d.beginWithGenerationRetry(task, ExecStatusQueued)
	if err != nil || execID == "" {
		t.Fatalf("beginWithGenerationRetry = %q, %v", execID, err)
	}
	cred := rig.stashed(execID)
	if cred == nil {
		t.Fatal("no stashed credential")
	}
	if err := rig.w.lifecycle.Transition(execID, ExecStatusRunning); err != nil {
		t.Fatal(err)
	}

	rig.d.sweepBillingCredentials()
	if got := rig.stashed(execID); got != cred || len(cred.Env()) != 1 {
		t.Fatalf("sweep dropped the key of a running row: stashed=%v env=%v", got, cred.Env())
	}

	taken, err := rig.d.billingCredentialForRun(execID, task.ID, rig.proj)
	if err != nil || taken != cred {
		t.Fatalf("billingCredentialForRun = %v, %v; want the admission credential", taken, err)
	}
	if rig.stashCount() != 0 {
		t.Errorf("stash count after the take = %d, want 0", rig.stashCount())
	}
}

// The sweep zeroes the keys of rows that ended and of rows that are gone.
func TestBillingCredential_SweepDropsTerminalAndMissingRows(t *testing.T) {
	skipWithoutShell(t)
	rig := newBillingWorkerRig(t, apiKeyConfig("printf "+testBillingKey))
	task := &Task{ID: "GH-6026-B", Title: "failed", ProjectPath: rig.proj}
	failedID, err := rig.d.beginWithGenerationRetry(task, ExecStatusQueued)
	if err != nil || failedID == "" {
		t.Fatalf("beginWithGenerationRetry = %q, %v", failedID, err)
	}
	failedCred := rig.stashed(failedID)
	if failedCred == nil {
		t.Fatal("no stashed credential")
	}
	if err := rig.d.store.UpdateExecutionStatus(failedID, string(ExecStatusFailed)); err != nil {
		t.Fatal(err)
	}
	missingCred := &BillingCredential{mode: BillingModeAPIKey, key: []byte(testBillingKey)}
	rig.d.stashBillingCredential("exec-gh6026-deleted", missingCred)

	rig.d.sweepBillingCredentials()

	if rig.stashCount() != 0 {
		t.Errorf("stash count = %d, want 0 after the sweep", rig.stashCount())
	}
	if len(failedCred.Env()) != 0 || len(missingCred.Env()) != 0 {
		t.Errorf("credentials not zeroed: failed=%v missing=%v", failedCred.Env(), missingCred.Env())
	}
}

// When the move to running fails the worker drops the key; the row stays
// queued, so the sweep alone would keep it.
func TestBillingCredential_TransitionFailureDrops(t *testing.T) {
	skipWithoutShell(t)
	stubFetchIssueState(t, func(context.Context, *Runner, *Task, string) (IssueState, error) {
		return IssueState{}, nil
	})
	rig := newBillingWorkerRig(t, apiKeyConfig("printf "+testBillingKey))
	task := &Task{ID: "GH-6026-C", Title: "transition fails", ProjectPath: rig.proj}
	execID, err := rig.d.beginWithGenerationRetry(task, ExecStatusQueued)
	if err != nil || execID == "" {
		t.Fatalf("beginWithGenerationRetry = %q, %v", execID, err)
	}
	cred := rig.stashed(execID)
	if cred == nil {
		t.Fatal("no stashed credential")
	}

	// A closed store makes the lifecycle's status write fail while the worker
	// still reads its queue from the open one.
	broken, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.Close(); err != nil {
		t.Fatal(err)
	}
	rig.w.lifecycle = NewExecutionLifecycle(broken)

	// The failed row stays queued; processQueue skips it for the rest of the
	// call (GH-6028) and returns.
	rig.w.processQueue(context.Background())

	row, err := rig.d.store.GetExecution(execID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != string(ExecStatusQueued) {
		t.Fatalf("row status = %q, want queued (the Transition must have failed)", row.Status)
	}
	if rig.stashCount() != 0 || len(cred.Env()) != 0 {
		t.Errorf("credential left behind: stashed=%d env=%v", rig.stashCount(), cred.Env())
	}
}

// A failed move to running must not make processQueue re-pick the same still
// queued row at once with no backoff (GH-6028). Mutation pin: drop the
// transitionFailed skip in processQueue and the older row is re-picked forever,
// so the call never returns and the newer row's key is never reached.
func TestProcessQueue_TransitionFailureDoesNotSpin(t *testing.T) {
	skipWithoutShell(t)
	stubFetchIssueState(t, func(context.Context, *Runner, *Task, string) (IssueState, error) {
		return IssueState{}, nil
	})
	rig := newBillingWorkerRig(t, apiKeyConfig("printf "+testBillingKey))
	var ids []string
	for _, id := range []string{"GH-6028-A", "GH-6028-B"} {
		execID, err := rig.d.beginWithGenerationRetry(&Task{ID: id, Title: id, ProjectPath: rig.proj}, ExecStatusQueued)
		if err != nil || execID == "" {
			t.Fatalf("beginWithGenerationRetry(%s) = %q, %v", id, execID, err)
		}
		ids = append(ids, execID)
	}

	broken, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.Close(); err != nil {
		t.Fatal(err)
	}
	rig.w.lifecycle = NewExecutionLifecycle(broken)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rig.w.processQueue(ctx)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("processQueue kept re-picking a row whose move to running failed")
	}

	for _, id := range ids {
		row, err := rig.d.store.GetExecution(id)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != string(ExecStatusQueued) {
			t.Errorf("%s status = %q, want queued", id, row.Status)
		}
	}
	if n := rig.stashCount(); n != 0 {
		t.Errorf("stash count = %d, want 0 (both rows were attempted once and dropped)", n)
	}
}

// The base-presence park and the closed-issue supersede also end the stash.
func TestBillingCredential_WorkerSkipPathsDiscard_ParkAndSupersede(t *testing.T) {
	skipWithoutShell(t)

	t.Run("base-presence park", func(t *testing.T) {
		setupFakeGhCLI(t)
		stubFetchIssueState(t, func(context.Context, *Runner, *Task, string) (IssueState, error) {
			return IssueState{}, nil
		})
		stubCheckBasePresence(t, func(context.Context, *Runner, *Task, string, []int, []string) (BasePresenceHold, error) {
			return BasePresenceHold{Held: true, Reason: "referenced PR #1 is still open (not merged)"}, nil
		})
		rig := newBillingWorkerRig(t, apiKeyConfig("printf "+testBillingKey))
		rig.w.setBasePresenceHoldMaxCycles(1)
		task := &Task{ID: "GH-6026-D", Title: "held", Description: "Depends on: #1", ProjectPath: rig.proj}
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
		if row.Status != string(ExecStatusSkipped) {
			t.Fatalf("row status = %q, want skipped (parked by the hold)", row.Status)
		}
		if rig.stashCount() != 0 || len(cred.Env()) != 0 {
			t.Errorf("credential left behind: stashed=%d env=%v", rig.stashCount(), cred.Env())
		}
	})

	t.Run("supersede", func(t *testing.T) {
		stubFetchIssueState(t, func(context.Context, *Runner, *Task, string) (IssueState, error) {
			return IssueState{Closed: true, Labels: []string{"pilot-superseded"}}, nil
		})
		rig := newBillingWorkerRig(t, apiKeyConfig("printf "+testBillingKey))
		task := &Task{ID: "GH-6026-E", Title: "closed", ProjectPath: rig.proj}
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
		if row.Status != string(ExecStatusSuperseded) {
			t.Fatalf("row status = %q, want superseded", row.Status)
		}
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
	real      Backend // when set, each call also runs the real claude backend (GH-6028)
	dump      string  // env the real backend's fake CLI wrote, one block per call
}

func (b *credEnvBackend) Name() string      { return "cred-env" }
func (b *credEnvBackend) IsAvailable() bool { return true }
func (b *credEnvBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	b.envs = append(b.envs, BillingCredentialFromContext(ctx).Env())
	n := len(b.envs)
	b.mu.Unlock()
	if b.real != nil {
		// The real backend builds the subprocess env (backend_claudecode.go);
		// its fake CLI records what it was handed. The result is ignored.
		_, _ = b.real.Execute(ctx, ExecuteOptions{Prompt: "x", ProjectPath: opts.ProjectPath, EventHandler: func(BackendEvent) {}})
	}
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

const testInheritedKey = "inherited-fake-key"

// withRealProcEnv makes b also run the real Claude Code backend against a fake
// CLI that appends its environment to a dump, one "--CALL--"-terminated block
// per run. The daemon env carries an inherited key the backend must replace.
func withRealProcEnv(t *testing.T, b *credEnvBackend) *credEnvBackend {
	t.Helper()
	skipWithoutShell(t)
	t.Setenv("ANTHROPIC_API_KEY", testInheritedKey)
	dir := t.TempDir()
	b.dump = filepath.Join(dir, "env.txt")
	script := filepath.Join(dir, "claude")
	body := "#!/bin/sh\n{ /usr/bin/env; echo --CALL--; } >> " + b.dump + "\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil { //nolint:gosec // fake CLI must be executable
		t.Fatal(err)
	}
	b.real = NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script})
	return b
}

// seenProcEnvs returns the env the real backend's subprocess got on each call.
func (b *credEnvBackend) seenProcEnvs() [][]string {
	raw, err := os.ReadFile(b.dump)
	if err != nil {
		return nil
	}
	var calls [][]string
	var cur []string
	for _, line := range strings.Split(string(raw), "\n") {
		switch line {
		case "--CALL--":
			calls = append(calls, cur)
			cur = nil
		case "":
		default:
			cur = append(cur, line)
		}
	}
	return calls
}

// assertProcEnvCarriesKey checks the env built for each backend call holds
// the credential's key exactly once and no inherited key (GH-6026).
func assertProcEnvCarriesKey(t *testing.T, envs [][]string) {
	t.Helper()
	if len(envs) < 2 {
		t.Fatalf("backend ran %d time(s), want the retry to run too", len(envs))
	}
	want := "ANTHROPIC_API_KEY=" + testBillingKey
	for i, env := range envs {
		keys := 0
		for _, kv := range env {
			if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
				keys++
				if kv != want {
					t.Errorf("call %d: %s, want %s", i+1, kv, want)
				}
			}
			if strings.Contains(kv, testInheritedKey) {
				t.Errorf("call %d: inherited key leaked into the subprocess env", i+1)
			}
		}
		if keys != 1 {
			t.Errorf("call %d: %d ANTHROPIC_API_KEY entries, want exactly 1", i+1, keys)
		}
	}
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

// runSmartRetry drives a task whose first backend call fails retryably, so the
// smart retry re-executes on the retry ctx.
func runSmartRetry(t *testing.T) *credEnvBackend {
	t.Helper()
	backend := withRealProcEnv(t, &credEnvBackend{failFirst: true})
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
	return backend
}

// runGateFeedbackRetry drives a task whose first quality gate fails, so the
// feedback retry re-invokes the backend on the retry ctx.
func runGateFeedbackRetry(t *testing.T) *credEnvBackend {
	t.Helper()
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	t.Cleanup(func() { _ = os.RemoveAll(localRepo) })
	t.Cleanup(func() { _ = os.RemoveAll(remoteRepo) })

	backend := withRealProcEnv(t, &credEnvBackend{inner: &stackingAttemptBackend{}})
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
	return backend
}

// The smart retry re-executes on a context that still carries the key.
func TestBillingRetryCtx_SmartRetryKeepsCredential(t *testing.T) {
	assertRetryCarriesKey(t, runSmartRetry(t).seen())
}

// The quality-gate feedback retry re-invokes the backend on a context that
// still carries the key.
func TestBillingRetryCtx_GateFeedbackRetryKeepsCredential(t *testing.T) {
	assertRetryCarriesKey(t, runGateFeedbackRetry(t).seen())
}

// The env the real Claude Code backend hands its subprocess on each retry ctx
// carries the credential's key once and none of the inherited one (GH-6026,
// GH-6028). Mutation pin: change backend_claudecode.go's
// `modelSubprocessEnvFor(ctx, os.Environ())` to `os.Environ()` and this fails.
func TestBillingRetryCtx_SubprocessEnvCarriesKey(t *testing.T) {
	t.Run("smart retry", func(t *testing.T) {
		assertProcEnvCarriesKey(t, runSmartRetry(t).seenProcEnvs())
	})
	t.Run("gate feedback retry", func(t *testing.T) {
		assertProcEnvCarriesKey(t, runGateFeedbackRetry(t).seenProcEnvs())
	})
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
