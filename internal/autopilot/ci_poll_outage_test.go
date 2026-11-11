package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// ciOutageMode is what the fake GitHub answers to a check-runs request.
type ciOutageMode int32

const (
	ciOutageDown        ciOutageMode = iota // HTTP 500
	ciOutageGreen                           // one completed, successful check
	ciOutagePending                         // one in-progress check
	ciOutageForbidden                       // 403 on check-runs, one green Actions run (GH-6283)
	ciOutageUnreadable                      // 403 on check-runs, Actions runs and commit statuses (GH-6304)
	ciOutageFallback5xx                     // 403 on check-runs, HTTP 500 on Actions runs and statuses (GH-6304)
)

// ciOutageFixture is a controller wired to a fake GitHub whose check-runs
// endpoint can be flipped between failing, green and pending, with a
// controllable clock (GH-6056).
type ciOutageFixture struct {
	t       *testing.T
	c       *Controller
	pr      *PRState
	sink    *fakeAlertSink
	store   *StateStore
	mem     *memory.Store
	now     time.Time
	mode    atomic.Int32
	ciCalls atomic.Int32
	// comments counts POSTs to the PR's issue-comments endpoint (GH-6304).
	comments    atomic.Int32
	commentBody atomic.Value // string: body of the last comment
	commentFail atomic.Bool  // answer the comment POST with HTTP 500
	ctx         context.Context
	srvURL      string
}

func newCIOutageFixture(t *testing.T, mutate func(*Config)) *ciOutageFixture {
	t.Helper()
	f := &ciOutageFixture{
		t:    t,
		sink: &fakeAlertSink{},
		now:  time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
		ctx:  context.Background(),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case ciOutageMode(f.mode.Load()) == ciOutageUnreadable &&
			(strings.Contains(r.URL.Path, "/actions/runs") || strings.HasSuffix(r.URL.Path, "/status")):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(forbiddenCheckRunsBody))
		case ciOutageMode(f.mode.Load()) == ciOutageFallback5xx &&
			(strings.Contains(r.URL.Path, "/actions/runs") || strings.HasSuffix(r.URL.Path, "/status")):
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/42/comments") && f.commentFail.Load():
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/42/comments"):
			var in struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.commentBody.Store(in.Body)
			f.comments.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":1}`))
		case strings.Contains(r.URL.Path, "/actions/runs"):
			w.Header().Set("Content-Type", "application/json")
			runs := []map[string]interface{}{{
				"id": 1, "workflow_id": 10, "name": "CI", "status": "completed",
				"conclusion": "success", "head_sha": "abc1234", "run_attempt": 1,
			}}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"total_count": len(runs), "workflow_runs": runs})
		case strings.Contains(r.URL.Path, "check-runs"):
			f.ciCalls.Add(1)
			var checks []map[string]interface{}
			switch ciOutageMode(f.mode.Load()) {
			case ciOutageForbidden, ciOutageUnreadable, ciOutageFallback5xx:
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(forbiddenCheckRunsBody))
				return
			case ciOutageDown:
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"Server Error"}`))
				return
			case ciOutageGreen:
				checks = []map[string]interface{}{{"name": "build", "status": "completed", "conclusion": "success"}}
			case ciOutagePending:
				checks = []map[string]interface{}{{"name": "build", "status": "in_progress"}}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"total_count": len(checks), "check_runs": checks})
		case strings.Contains(r.URL.Path, "/pulls/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"number": 42, "state": "open", "merged": false, "mergeable": true,
				"head": map[string]interface{}{"sha": "abc1234"},
			})
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(srv.Close)
	f.srvURL = srv.URL

	cfg := DefaultConfig()
	if mutate != nil {
		mutate(cfg)
	}
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	f.c = NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath("/proj/pilot"))
	// A real SQLite state store and memory store, so the tests see what a
	// restart would see rather than only the in-memory PRState.
	f.store = newTestStateStore(t)
	f.c.SetStateStore(f.store)
	mem, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	if err := mem.SaveExecution(&memory.Execution{
		ID: "exec-10", TaskID: "GH-10", ProjectPath: "/proj/pilot", Status: "completed",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	f.mem = mem
	f.c.SetMemoryStore(mem)
	f.c.SetAlertsEngine(f.sink)
	f.c.ciPollClock = func() time.Time { return f.now }
	f.c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	f.pr, _ = f.c.GetPRState(42)
	f.pr.Stage = StageWaitingCI
	f.pr.CIWaitStartedAt = f.now
	f.setMode(ciOutageDown)
	return f
}

func (f *ciOutageFixture) setMode(m ciOutageMode) { f.mode.Store(int32(m)) }

// tick runs one polling-loop pass over the PR (redrives, then ProcessPR).
func (f *ciOutageFixture) tick() {
	f.t.Helper()
	f.c.processOnePR(f.ctx, f.pr.snapshot())
}

// startHold drives five failing ProcessPR polls, which start the hold.
func (f *ciOutageFixture) startHold() {
	f.t.Helper()
	for i := 0; i < apiOutageFailureThreshold; i++ {
		if err := f.c.ProcessPR(f.ctx, 42, nil); err != nil {
			f.t.Fatalf("ProcessPR %d: %v", i+1, err)
		}
	}
	if !f.pr.APIOutageHoldActive {
		f.t.Fatalf("hold not active after %d errors (stage %s)", apiOutageFailureThreshold, f.pr.Stage)
	}
}

func (f *ciOutageFixture) outageEvents() []alerts.Event {
	var out []alerts.Event
	for _, e := range f.sink.events {
		if e.Type == alerts.EventTypeCIPollAPIOutage {
			out = append(out, e)
		}
	}
	return out
}

func TestWaitingCI_APIOutageHoldsInsteadOfFailing(t *testing.T) {
	f := newCIOutageFixture(t, nil)

	for i := 1; i < apiOutageFailureThreshold; i++ {
		if err := f.c.ProcessPR(f.ctx, 42, nil); err != nil {
			t.Fatalf("ProcessPR %d: %v", i, err)
		}
		if f.pr.APIOutageHoldActive {
			t.Fatalf("hold active after only %d errors", i)
		}
	}
	if n := len(f.outageEvents()); n != 0 {
		t.Fatalf("alerts before the fifth error = %d, want 0", n)
	}

	if err := f.c.ProcessPR(f.ctx, 42, nil); err != nil {
		t.Fatalf("fifth ProcessPR: %v", err)
	}

	if f.pr.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s", f.pr.Stage, StageWaitingCI)
	}
	if !f.pr.APIOutageHoldActive {
		t.Error("APIOutageHoldActive = false, want true")
	}
	if f.pr.TerminalLabel != "" {
		t.Errorf("TerminalLabel = %q, want empty", f.pr.TerminalLabel)
	}
	if f.pr.Error != "" {
		t.Errorf("Error = %q, want empty", f.pr.Error)
	}
	if !f.pr.APIOutageSince.Equal(f.now) {
		t.Errorf("APIOutageSince = %v, want %v", f.pr.APIOutageSince, f.now)
	}
	evs := f.outageEvents()
	if len(evs) != 1 {
		t.Fatalf("ci_poll_api_outage alerts = %d, want 1", len(evs))
	}
	if got := evs[0].Metadata["outcome"]; got != "hold" {
		t.Errorf("alert outcome = %q, want hold", got)
	}
}

// TestWaitingCI_CheckRunsForbiddenGreenActionsRunLeavesWaitingCI is GH-6283: a
// fine-grained token gets a 403 on check-runs for ever; with a green Actions
// run the PR leaves waiting-CI on the first poll, with no hold and no error.
func TestWaitingCI_CheckRunsForbiddenGreenActionsRunLeavesWaitingCI(t *testing.T) {
	f := newCIOutageFixture(t, nil)
	f.c.ciMonitor.SetStepLogClient(ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, f.srvURL))
	f.setMode(ciOutageForbidden)

	if err := f.c.ProcessPR(f.ctx, 42, nil); err != nil {
		t.Fatalf("ProcessPR: %v", err)
	}

	if f.pr.Stage == StageWaitingCI {
		t.Fatalf("Stage = %s, want it to have left waiting-CI on the first poll", f.pr.Stage)
	}
	if f.pr.Stage != StageCIPassed {
		t.Errorf("Stage = %s, want %s", f.pr.Stage, StageCIPassed)
	}
	if f.pr.APIOutageHoldActive || f.pr.ConsecutiveAPIFailures != 0 {
		t.Errorf("hold=%v consecutive failures=%d, want no hold and no failures", f.pr.APIOutageHoldActive, f.pr.ConsecutiveAPIFailures)
	}
	if n := len(f.outageEvents()); n != 0 {
		t.Errorf("ci_poll_api_outage alerts = %d, want 0", n)
	}
}

func TestWaitingCI_APIOutageBackoffDoublesAndCaps(t *testing.T) {
	f := newCIOutageFixture(t, nil)
	f.startHold()

	wantWaits := []time.Duration{
		30 * time.Second, 60 * time.Second, 120 * time.Second,
		240 * time.Second, 300 * time.Second, 300 * time.Second,
	}
	for i, want := range wantWaits {
		if got := f.pr.APIOutageNextPoll.Sub(f.now); got != want {
			t.Fatalf("wait before poll %d = %s, want %s", i+1, got, want)
		}

		// One second early: the held PR must not be polled.
		f.now = f.pr.APIOutageNextPoll.Add(-time.Second)
		before := f.ciCalls.Load()
		f.tick()
		if got := f.ciCalls.Load(); got != before {
			t.Fatalf("poll %d ran %s early (check-runs calls %d -> %d)", i+1, time.Second, before, got)
		}

		// At the poll time it polls (and, still down, backs off further).
		f.now = f.pr.APIOutageNextPoll
		f.tick()
		if got := f.ciCalls.Load(); got == before {
			t.Fatalf("poll %d did not run at its poll time", i+1)
		}
		if f.pr.Stage != StageWaitingCI || !f.pr.APIOutageHoldActive {
			t.Fatalf("poll %d: stage=%s hold=%v, want held at waiting_ci", i+1, f.pr.Stage, f.pr.APIOutageHoldActive)
		}
	}
	if got := f.pr.APIOutageNextPoll.Sub(f.now); got != apiOutageBackoffCap {
		t.Errorf("backoff after the sequence = %s, want the %s cap", got, apiOutageBackoffCap)
	}
}

func TestWaitingCI_APIOutageRedriveOnFirstGoodPoll(t *testing.T) {
	f := newCIOutageFixture(t, nil)
	f.startHold()

	// Still down at the poll time: stays held.
	f.now = f.pr.APIOutageNextPoll
	f.tick()
	if !f.pr.APIOutageHoldActive {
		t.Fatal("hold cleared by a failing poll")
	}

	// GitHub recovers; the next due poll releases the hold and CI proceeds.
	f.setMode(ciOutageGreen)
	f.now = f.pr.APIOutageNextPoll
	f.tick()

	if f.pr.APIOutageHoldActive {
		t.Error("APIOutageHoldActive = true after a good poll, want false")
	}
	if !f.pr.APIOutageNextPoll.IsZero() || !f.pr.APIOutageSince.IsZero() {
		t.Errorf("outage times not reset: since=%v next=%v", f.pr.APIOutageSince, f.pr.APIOutageNextPoll)
	}
	if f.pr.ConsecutiveAPIFailures != 0 {
		t.Errorf("ConsecutiveAPIFailures = %d, want 0", f.pr.ConsecutiveAPIFailures)
	}
	if f.pr.Stage != StageCIPassed {
		t.Errorf("Stage = %s, want %s (PR proceeds to merge)", f.pr.Stage, StageCIPassed)
	}
	if f.pr.TerminalLabel != "" {
		t.Errorf("TerminalLabel = %q, want empty", f.pr.TerminalLabel)
	}
}

func TestWaitingCI_APIOutageDoesNotCountTowardCITimeout(t *testing.T) {
	f := newCIOutageFixture(t, func(cfg *Config) { cfg.CIWaitTimeout = 5 * time.Minute })
	f.startHold()
	started := f.pr.CIWaitStartedAt

	// Outage far longer than the CI timeout (but inside the 30m budget), then a
	// poll that finds CI still running.
	outage := 20 * time.Minute
	f.setMode(ciOutagePending)
	f.now = f.now.Add(outage)
	f.tick()

	if f.pr.APIOutageHoldActive {
		t.Fatal("hold not cleared by the good poll")
	}
	if f.pr.Stage != StageWaitingCI {
		t.Fatalf("Stage = %s (error %q), want %s: an outage must not trip the CI timeout", f.pr.Stage, f.pr.Error, StageWaitingCI)
	}
	if f.pr.TerminalLabel != "" {
		t.Errorf("TerminalLabel = %q, want empty", f.pr.TerminalLabel)
	}
	if got := f.pr.CIWaitStartedAt.Sub(started); got != outage {
		t.Errorf("CIWaitStartedAt moved by %s, want the outage length %s", got, outage)
	}
}

func TestWaitingCI_APIOutageBudgetFails(t *testing.T) {
	f := newCIOutageFixture(t, nil)
	f.startHold()
	since := f.pr.APIOutageSince
	budget := f.c.apiOutageBudget()
	if budget != 30*time.Minute {
		t.Fatalf("default budget = %s, want 30m", budget)
	}

	for i := 0; i < 40 && f.pr.Stage == StageWaitingCI; i++ {
		f.now = f.pr.APIOutageNextPoll
		f.tick()
		if f.pr.Stage == StageWaitingCI && f.now.Sub(since) > budget {
			t.Fatalf("still held %s into the outage, past the %s budget", f.now.Sub(since), budget)
		}
	}

	if f.pr.Stage != StageFailed {
		t.Fatalf("Stage = %s, want %s once the budget elapsed", f.pr.Stage, StageFailed)
	}
	if f.now.Sub(since) <= budget {
		t.Errorf("failed after only %s, inside the %s budget", f.now.Sub(since), budget)
	}
	if f.pr.TerminalLabel != github.LabelFailed {
		t.Errorf("TerminalLabel = %q, want %q", f.pr.TerminalLabel, github.LabelFailed)
	}
	if !strings.Contains(f.pr.Error, "CI check API failed") || !strings.Contains(f.pr.Error, "consecutive times over") {
		t.Errorf("Error = %q, want the consecutive-failures text with the elapsed time", f.pr.Error)
	}
	if f.pr.APIOutageHoldActive {
		t.Error("APIOutageHoldActive still set on a failed PR")
	}

	// The failure happens in the redrive, before ProcessPR (which returns early
	// on StageFailed), so it must be saved there: read the row back from SQLite.
	saved, err := f.store.GetPRState("owner/repo", 42)
	if err != nil || saved == nil {
		t.Fatalf("GetPRState = %v, %v", saved, err)
	}
	if saved.Stage != StageFailed {
		t.Errorf("persisted Stage = %s, want %s — budget failure was never saved", saved.Stage, StageFailed)
	}
	if saved.APIOutageHoldActive {
		t.Error("persisted APIOutageHoldActive still set on a failed PR")
	}
	if saved.Error != f.pr.Error {
		t.Errorf("persisted Error = %q, want %q", saved.Error, f.pr.Error)
	}
	events, err := f.mem.ListExecutionEvents("exec-10")
	if err != nil {
		t.Fatalf("ListExecutionEvents: %v", err)
	}
	var failedEvents int
	for _, e := range events {
		if e.Stage == memory.StageFailed {
			failedEvents++
		}
	}
	if failedEvents != 1 {
		t.Errorf("failed audit events = %d, want 1 (events: %+v)", failedEvents, events)
	}

	evs := f.outageEvents()
	if len(evs) != 2 {
		t.Fatalf("ci_poll_api_outage alerts = %d, want 2 (hold, budget)", len(evs))
	}
	if got := evs[1].Metadata["outcome"]; got != "budget_exhausted" {
		t.Errorf("second alert outcome = %q, want budget_exhausted", got)
	}
}

func TestWaitingCI_APIOutageBudgetConfigurable(t *testing.T) {
	f := newCIOutageFixture(t, func(cfg *Config) { cfg.CIChecks.APIOutageBudget = 2 * time.Minute })
	f.startHold()

	for i := 0; i < 10 && f.pr.Stage == StageWaitingCI; i++ {
		f.now = f.pr.APIOutageNextPoll
		f.tick()
	}
	if f.pr.Stage != StageFailed {
		t.Fatalf("Stage = %s, want %s with a 2m budget", f.pr.Stage, StageFailed)
	}
}

func TestPRState_APIOutageFieldsPersist(t *testing.T) {
	store := newTestStateStore(t)
	since := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	next := since.Add(90 * time.Second)

	pr := &PRState{
		PRNumber:            42,
		PRURL:               "https://github.com/owner/repo/pull/42",
		Stage:               StageWaitingCI,
		APIOutageHoldActive: true,
		APIOutageSince:      since,
		APIOutageNextPoll:   next,
	}
	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}

	got, err := store.GetPRState("owner/repo", 42)
	if err != nil || got == nil {
		t.Fatalf("GetPRState = %v, %v", got, err)
	}
	if !got.APIOutageHoldActive || !got.APIOutageSince.Equal(since) || !got.APIOutageNextPoll.Equal(next) {
		t.Errorf("GetPRState outage fields = %v %v %v, want true %v %v",
			got.APIOutageHoldActive, got.APIOutageSince, got.APIOutageNextPoll, since, next)
	}

	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil || len(all) != 1 {
		t.Fatalf("LoadAllPRStates = %d rows, %v", len(all), err)
	}
	if !all[0].APIOutageHoldActive || !all[0].APIOutageSince.Equal(since) || !all[0].APIOutageNextPoll.Equal(next) {
		t.Errorf("LoadAllPRStates outage fields = %v %v %v", all[0].APIOutageHoldActive, all[0].APIOutageSince, all[0].APIOutageNextPoll)
	}

	// Clearing the hold round-trips too (upsert overwrites, zero times -> NULL).
	pr.APIOutageHoldActive = false
	pr.APIOutageSince = time.Time{}
	pr.APIOutageNextPoll = time.Time{}
	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState (cleared): %v", err)
	}
	got, err = store.GetPRState("owner/repo", 42)
	if err != nil || got == nil {
		t.Fatalf("GetPRState (cleared) = %v, %v", got, err)
	}
	if got.APIOutageHoldActive || !got.APIOutageSince.IsZero() || !got.APIOutageNextPoll.IsZero() {
		t.Errorf("cleared outage fields = %v %v %v, want false and zero times",
			got.APIOutageHoldActive, got.APIOutageSince, got.APIOutageNextPoll)
	}
}
