package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

const capTestProject = "/tmp/daily-cap-project"

// fakeCapAlerts records the events DailyCapGate emits.
type fakeCapAlerts struct {
	mu     sync.Mutex
	events []AlertEvent
}

func (f *fakeCapAlerts) ProcessEvent(e AlertEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeCapAlerts) snapshot() []AlertEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]AlertEvent(nil), f.events...)
}

// newTestCapGate builds a gate whose clock reads *now.
func newTestCapGate(t *testing.T, dailyCap int, tz string, now *time.Time) *DailyCapGate {
	t.Helper()
	g := NewDailyCapGate(dailyCap, tz)
	g.now = func() time.Time { return *now }
	return g
}

// seedTicket writes an executions row for taskID created at `at`.
func seedTicket(t *testing.T, store *memory.Store, taskID string, at time.Time) {
	t.Helper()
	if err := store.SaveExecution(&memory.Execution{
		ID: "seed-" + taskID, TaskID: taskID, ProjectPath: capTestProject,
		Status: "completed", CreatedAt: at,
	}); err != nil {
		t.Fatalf("seed %s: %v", taskID, err)
	}
}

func seedTickets(t *testing.T, store *memory.Store, n int, at time.Time) {
	t.Helper()
	for i := 1; i <= n; i++ {
		seedTicket(t, store, fmt.Sprintf("GH-%d", i), at)
	}
}

func countExecutions(t *testing.T, store *memory.Store) int {
	t.Helper()
	execs, err := store.GetRecentExecutions(1000, "")
	if err != nil {
		t.Fatalf("GetRecentExecutions: %v", err)
	}
	return len(execs)
}

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	return loc
}

// TestExecutionLifecycle_Begin_DailyTicketCap covers the hold rules of GH-5808:
// the 16th ticket on cap 15, cap 0, retry pass-through, chat/canary/subtask
// bypass, and that a held Begin writes nothing.
func TestExecutionLifecycle_Begin_DailyTicketCap(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		cap      int
		seed     int  // distinct tickets already begun today
		task     Task // the task beginning
		gen      int
		priorRow bool // task already has a row today (counted) before Begin
		wantHeld bool
	}{
		{name: "15th ticket on cap 15 passes", cap: 15, seed: 14, task: Task{ID: "GH-100"}},
		{name: "16th ticket on cap 15 is held", cap: 15, seed: 15, task: Task{ID: "GH-100"}, wantHeld: true},
		{name: "cap 0 never holds", cap: 0, seed: 40, task: Task{ID: "GH-100"}},
		{name: "retry generation of a counted task passes", cap: 15, seed: 15, task: Task{ID: "GH-3"}, gen: 1, priorRow: true},
		{name: "chat-filed task is not held", cap: 15, seed: 15, task: Task{ID: "TG-1", SourceAdapter: TaskSourceAdapterChat}},
		{name: "canary task is not held", cap: 15, seed: 15, task: Task{ID: "GH-100", IsCanary: true}},
		{name: "decomposed subtask is not held", cap: 15, seed: 15, task: Task{ID: "GH-100-1", ParentExecutionID: "parent-exec"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			clock := now
			seedTickets(t, store, tt.seed, now.Add(-time.Hour))

			task := tt.task
			task.ProjectPath = capTestProject
			task.Title = "t"
			before := countExecutions(t, store)

			l := NewExecutionLifecycle(store)
			l.SetDailyCapGate(newTestCapGate(t, tt.cap, "UTC", &clock))
			execID, err := l.Begin(&task, ExecStatusQueued, tt.gen)

			if tt.wantHeld {
				var hold *DailyTicketCapHold
				if !errors.As(err, &hold) {
					t.Fatalf("Begin err = %v, want *DailyTicketCapHold", err)
				}
				if !errors.Is(err, ErrDailyTicketCapReached) || !errors.Is(err, ErrDispatchGated) {
					t.Errorf("hold must wrap ErrDailyTicketCapReached and ErrDispatchGated, got %v", err)
				}
				if hold.Cap != tt.cap || hold.Dispatched != tt.seed {
					t.Errorf("hold = %+v, want cap %d dispatched %d", hold, tt.cap, tt.seed)
				}
				if want := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC); !hold.ResumesAt.Equal(want) {
					t.Errorf("ResumesAt = %v, want %v", hold.ResumesAt, want)
				}
				if execID != "" || task.ExecutionID != "" {
					t.Errorf("held Begin stamped an execution id: %q / %q", execID, task.ExecutionID)
				}
				if got := countExecutions(t, store); got != before {
					t.Errorf("executions rows = %d, want %d (a held Begin writes nothing)", got, before)
				}
				if _, _, found, err := store.LatestClaimGeneration(task.ID, task.ProjectPath); err != nil || found {
					t.Errorf("held Begin left a claim row (found=%v err=%v)", found, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Begin err = %v, want nil", err)
			}
			if execID == "" || task.ExecutionID != execID {
				t.Errorf("Begin did not stamp the execution id: %q / %q", execID, task.ExecutionID)
			}
			if _, err := store.GetExecution(execID); err != nil {
				t.Errorf("Begin did not write the execution row: %v", err)
			}
		})
	}
}

// TestExecutionLifecycle_Begin_DailyTicketCap_RetryOfRealBegin drives the retry
// rule end to end through Begin itself: ticket 1 begins and fails, the cap is
// then reached by other tickets, and generation+1 of ticket 1 still passes
// while a brand-new ticket is held.
func TestExecutionLifecycle_Begin_DailyTicketCap_RetryOfRealBegin(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	// Begin stamps rows with the wall clock, so the gate reads the wall clock
	// too; only a midnight rollover inside the test's few milliseconds could
	// split the day.
	gate := NewDailyCapGate(2, "UTC")

	l := NewExecutionLifecycle(store)
	l.SetDailyCapGate(gate)
	begin := func(id string, gen int) error {
		task := &Task{ID: id, Title: id, ProjectPath: capTestProject}
		_, err := l.Begin(task, ExecStatusQueued, gen)
		return err
	}

	if err := begin("GH-1", 0); err != nil {
		t.Fatalf("GH-1: %v", err)
	}
	if err := begin("GH-2", 0); err != nil {
		t.Fatalf("GH-2: %v", err)
	}
	if err := begin("GH-3", 0); !errors.Is(err, ErrDailyTicketCapReached) {
		t.Fatalf("GH-3 err = %v, want held", err)
	}
	exec, err := store.GetLatestExecutionByTaskID("GH-1", capTestProject)
	if err != nil {
		t.Fatalf("lookup GH-1: %v", err)
	}
	if _, err := store.UpdateExecutionStatusIfNotTerminal(exec.ID, string(ExecStatusFailed)); err != nil {
		t.Fatalf("fail GH-1: %v", err)
	}
	if err := begin("GH-1", 1); err != nil {
		t.Fatalf("generation+1 retry of counted GH-1 err = %v, want nil", err)
	}
	if err := begin("GH-3", 0); !errors.Is(err, ErrDailyTicketCapReached) {
		t.Fatalf("GH-3 after the retry err = %v, want still held (a retry must not consume a slot)", err)
	}
}

// TestDailyCapGate_DayBoundaryFollowsTimezone is the Berlin case: two tickets
// created at 23:00 Berlin on Oct 22 sit inside the UTC day Oct 22 but outside
// the Berlin day Oct 23 that "now" (00:30 Berlin) belongs to.
func TestDailyCapGate_DayBoundaryFollowsTimezone(t *testing.T) {
	berlin := mustLoc(t, "Europe/Berlin")
	// 23:30 Berlin Oct 22 == 21:30Z; 00:30 Berlin Oct 23 == 22:30Z.
	createdAt := time.Date(2026, 10, 22, 23, 0, 0, 0, berlin)
	now := time.Date(2026, 10, 23, 0, 30, 0, 0, berlin)

	tests := []struct {
		name       string
		tz         string
		wantHeld   bool
		wantDay    string
		wantResume time.Time
	}{
		{name: "UTC counts both tickets in Oct 22", tz: "UTC", wantHeld: true,
			wantDay: "2026-10-22", wantResume: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)},
		{name: "Berlin starts a fresh day at 00:00 local", tz: "Europe/Berlin", wantHeld: false},
		{name: "empty timezone means UTC", tz: "", wantHeld: true,
			wantDay: "2026-10-22", wantResume: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			seedTicket(t, store, "GH-1", createdAt)
			seedTicket(t, store, "GH-2", createdAt)

			clock := now
			hold := newTestCapGate(t, 2, tt.tz, &clock).Check(store, &Task{ID: "GH-9", ProjectPath: capTestProject})
			if (hold != nil) != tt.wantHeld {
				t.Fatalf("hold = %v, want held=%v", hold, tt.wantHeld)
			}
			if hold == nil {
				return
			}
			if hold.Day != tt.wantDay || !hold.ResumesAt.Equal(tt.wantResume) {
				t.Errorf("hold day/resume = %s / %v, want %s / %v", hold.Day, hold.ResumesAt, tt.wantDay, tt.wantResume)
			}
		})
	}

	t.Run("Berlin holds its own day and resumes at local midnight", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		// 00:10 Berlin Oct 23 == 22:10Z Oct 22: the UTC day Oct 22, the Berlin day Oct 23.
		seedTicket(t, store, "GH-1", time.Date(2026, 10, 23, 0, 10, 0, 0, berlin))
		seedTicket(t, store, "GH-2", time.Date(2026, 10, 23, 0, 20, 0, 0, berlin))
		clock := now
		hold := newTestCapGate(t, 2, "Europe/Berlin", &clock).Check(store, &Task{ID: "GH-9", ProjectPath: capTestProject})
		if hold == nil {
			t.Fatal("want a hold")
		}
		if want := time.Date(2026, 10, 24, 0, 0, 0, 0, berlin); hold.Day != "2026-10-23" || !hold.ResumesAt.Equal(want) {
			t.Errorf("hold day/resume = %s / %v, want 2026-10-23 / %v", hold.Day, hold.ResumesAt, want)
		}
	})
}

// TestDailyCapGate_LiftsAtResumeInstant: the same task is held right up to the
// resume instant and passes once the clock reaches it.
func TestDailyCapGate_LiftsAtResumeInstant(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	berlin := mustLoc(t, "Europe/Berlin")
	seedTicket(t, store, "GH-1", time.Date(2026, 10, 22, 9, 0, 0, 0, berlin))
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, berlin)
	gate := newTestCapGate(t, 1, "Europe/Berlin", &clock)
	task := &Task{ID: "GH-2", ProjectPath: capTestProject}

	hold := gate.Check(store, task)
	if hold == nil {
		t.Fatal("want a hold before the resume instant")
	}
	clock = hold.ResumesAt.Add(-time.Nanosecond)
	if gate.Check(store, task) == nil {
		t.Error("want a hold one nanosecond before the resume instant")
	}
	clock = hold.ResumesAt
	if h := gate.Check(store, task); h != nil {
		t.Errorf("hold at the resume instant = %v, want none", h)
	}
}

// TestDailyCapGate_AnnouncesOncePerDay: one Info line per held task per day and
// one daily_ticket_cap_hit event per day, with the day in the metadata.
func TestDailyCapGate_AnnouncesOncePerDay(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	seedTicket(t, store, "GH-1", time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC))
	seedTicket(t, store, "GH-1b", time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC))

	var logs bytes.Buffer
	alerts := &fakeCapAlerts{}
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	gate := newTestCapGate(t, 1, "UTC", &clock)
	gate.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	gate.alerts = func() AlertEventProcessor { return alerts }

	check := func(id string) {
		t.Helper()
		if gate.Check(store, &Task{ID: id, ProjectPath: capTestProject}) == nil {
			t.Fatalf("%s: want a hold", id)
		}
	}
	for i := 0; i < 3; i++ { // polled repeatedly
		check("GH-2")
	}
	check("GH-3")

	if got := strings.Count(logs.String(), "task_id=GH-2"); got != 1 {
		t.Errorf("GH-2 hold logged %d times, want once per day:\n%s", got, logs.String())
	}
	if got := strings.Count(logs.String(), "task_id=GH-3"); got != 1 {
		t.Errorf("GH-3 hold logged %d times, want once:\n%s", got, logs.String())
	}
	if !strings.Contains(logs.String(), "level=INFO") {
		t.Errorf("hold must log at Info:\n%s", logs.String())
	}
	events := alerts.snapshot()
	if len(events) != 1 {
		t.Fatalf("alert events = %d, want 1 per org per day", len(events))
	}
	if events[0].Type != AlertEventTypeDailyTicketCapHit || events[0].Metadata["day"] != "2026-10-22" ||
		events[0].Metadata["cap"] != "1" || events[0].Metadata["dispatched"] != "1" {
		t.Errorf("event = %+v", events[0])
	}

	// Next day: GH-2 is held again, logs again, and the org alerts again.
	clock = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	check("GH-2")
	if got := strings.Count(logs.String(), "task_id=GH-2"); got != 2 {
		t.Errorf("GH-2 logged %d times after day change, want 2", got)
	}
	events = alerts.snapshot()
	if len(events) != 2 || events[1].Metadata["day"] != "2026-10-23" {
		t.Errorf("events after day change = %+v, want a second one for 2026-10-23", events)
	}
}

// TestDailyCapGate_AlertWaitsForProcessor: a hold before the alerts engine is
// wired must not use up the day's alert.
func TestDailyCapGate_AlertWaitsForProcessor(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	seedTicket(t, store, "GH-1", time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC))
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	gate := newTestCapGate(t, 1, "UTC", &clock)
	var processor AlertEventProcessor
	gate.alerts = func() AlertEventProcessor { return processor }

	task := &Task{ID: "GH-2", ProjectPath: capTestProject}
	if gate.Check(store, task) == nil {
		t.Fatal("want a hold")
	}
	alerts := &fakeCapAlerts{}
	processor = alerts
	if gate.Check(store, task) == nil {
		t.Fatal("want a hold")
	}
	if got := len(alerts.snapshot()); got != 1 {
		t.Errorf("alert events = %d, want 1 once a processor exists", got)
	}
}

// TestDailyCapGate_NilAndDisabledNeverHold: a nil gate, a nil store and a nil
// runner config are all "no cap".
func TestDailyCapGate_NilAndDisabledNeverHold(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	seedTickets(t, store, 5, time.Now().UTC())
	task := &Task{ID: "GH-99", ProjectPath: capTestProject}

	var nilGate *DailyCapGate
	if nilGate.Check(store, task) != nil {
		t.Error("nil gate held")
	}
	if NewDailyCapGate(1, "UTC").Check(nil, task) != nil {
		t.Error("nil store held")
	}
	var nilRunner *Runner
	if nilRunner.DailyCapGate() != nil {
		t.Error("nil runner returned a gate")
	}
	if NewRunner().DailyCapGate() != nil {
		t.Error("runner without config returned a gate")
	}
	r, err := NewRunnerWithConfig(&BackendConfig{DailyTicketCap: 3, DailyTicketCapTimezone: "Europe/Berlin"})
	if err != nil {
		t.Fatalf("NewRunnerWithConfig: %v", err)
	}
	if g := r.DailyCapGate(); g == nil || g.cap != 3 || g.tzName != "Europe/Berlin" {
		t.Errorf("runner gate = %+v, want cap 3 / Europe/Berlin", g)
	}
	r0, err := NewRunnerWithConfig(&BackendConfig{DailyTicketCap: 0})
	if err != nil {
		t.Fatalf("NewRunnerWithConfig: %v", err)
	}
	if r0.DailyCapGate() != nil {
		t.Error("cap 0 runner returned a gate")
	}
}

// TestDispatcher_QueueTask_DailyCapHoldAndRepick: a held task is declined with
// the sentinel, leaves no trace (no row, no claim, task untouched), and the
// same task is accepted by the next QueueTask once the resume instant passes.
// The dispatcher has no forge client, so it cannot touch labels or comments;
// the assertions pin that a hold hands back an unmodified task and an untouched
// ledger for the caller to leave alone.
func TestDispatcher_QueueTask_DailyCapHoldAndRepick(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	berlin := mustLoc(t, "Europe/Berlin")
	seedTicket(t, store, "GH-1", time.Date(2026, 10, 22, 9, 0, 0, 0, berlin))

	runner, err := NewRunnerWithConfig(&BackendConfig{DailyTicketCap: 1, DailyTicketCapTimezone: "Europe/Berlin"})
	if err != nil {
		t.Fatalf("NewRunnerWithConfig: %v", err)
	}
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, berlin)
	gate := runner.DailyCapGate()
	gate.now = func() time.Time { return clock }
	alerts := &fakeCapAlerts{}
	runner.SetAlertProcessor(alerts)

	d := NewDispatcher(store, runner, nil)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	labels := []string{"pilot"}
	task := &Task{ID: "GH-2", Title: "second ticket", Description: "x", ProjectPath: capTestProject, Labels: labels}
	before := countExecutions(t, store)

	// Held, repeatedly (the poller re-offers it every tick).
	var hold *DailyTicketCapHold
	for i := 0; i < 3; i++ {
		execID, err := d.QueueTask(context.Background(), task)
		if !errors.As(err, &hold) || !errors.Is(err, ErrDispatchGated) {
			t.Fatalf("QueueTask #%d err = %v, want a daily-cap hold wrapping ErrDispatchGated", i, err)
		}
		if execID != "" || task.ExecutionID != "" {
			t.Fatalf("held task got an execution id %q / %q", execID, task.ExecutionID)
		}
	}
	if got := countExecutions(t, store); got != before {
		t.Errorf("executions rows = %d, want %d: a held task writes no row", got, before)
	}
	if _, _, found, _ := store.LatestClaimGeneration(task.ID, task.ProjectPath); found {
		t.Error("a held task left a claim row")
	}
	if len(task.Labels) != 1 || task.Labels[0] != "pilot" {
		t.Errorf("task labels mutated: %v", task.Labels)
	}
	if d.IsActive(task.ID, task.ProjectPath) {
		t.Error("held task is active in the dispatcher")
	}
	if want := time.Date(2026, 10, 23, 0, 0, 0, 0, berlin); !hold.ResumesAt.Equal(want) {
		t.Errorf("ResumesAt = %v, want %v", hold.ResumesAt, want)
	}
	if got := len(alerts.snapshot()); got != 1 {
		t.Errorf("daily_ticket_cap_hit events = %d, want exactly 1 for the day", got)
	}

	// Re-pick after the resume instant.
	clock = hold.ResumesAt
	execID, err := d.QueueTask(context.Background(), task)
	if err != nil || execID == "" {
		t.Fatalf("QueueTask after the resume instant = %q, %v, want an execution id", execID, err)
	}
	if got := countExecutions(t, store); got != before+1 {
		t.Errorf("executions rows after re-pick = %d, want %d", got, before+1)
	}
}

// TestExecuteSubIssues_DailyCapHoldDoesNotFailParent: with the cap reached the
// epic loop never starts a child, returns a hold, and the parent's result is a
// benign "skipped" outcome rather than a failure.
func TestExecuteSubIssues_DailyCapHoldDoesNotFailParent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	calls := 0
	runner := newTestRunnerWithExecFunc(func(_ context.Context, task *Task) (*ExecutionResult, error) {
		calls++
		return &ExecutionResult{TaskID: task.ID, Success: true}, nil
	})
	runner.logStore = store
	runner.config = &BackendConfig{DailyTicketCap: 1, DailyTicketCapTimezone: "UTC"}
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	runner.DailyCapGate().now = func() time.Time { return clock }
	// The epic parent was the one ticket of the day.
	seedTicket(t, store, "GH-9300", time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC))

	parent := &Task{ID: "GH-9300", Title: "[epic] cap", ProjectPath: capTestProject}
	_, _, err := runner.executeSubIssuesTracked(context.Background(), parent, makeSubIssues(2, 9401), "", "")
	if !errors.Is(err, ErrDailyTicketCapReached) {
		t.Fatalf("err = %v, want the daily cap hold", err)
	}
	if calls != 0 {
		t.Errorf("backend invoked %d times for held children, want 0", calls)
	}
	if got := countExecutions(t, store); got != 1 {
		t.Errorf("executions rows = %d, want 1 (only the parent): held children write nothing", got)
	}

	res, ok := epicHeldResult(parent, err, nil, time.Now())
	if !ok {
		t.Fatal("epicHeldResult did not recognise the hold")
	}
	if res.Success || TerminalStatus(res) != "skipped" {
		t.Errorf("parent result = success %v status %q, want a non-failed skipped outcome", res.Success, TerminalStatus(res))
	}
	if _, ok := epicHeldResult(parent, errors.New("boom"), nil, time.Now()); ok {
		t.Error("epicHeldResult converted an unrelated error")
	}
	if _, ok := epicHeldResult(parent, nil, nil, time.Now()); ok {
		t.Error("epicHeldResult converted a nil error")
	}
}
