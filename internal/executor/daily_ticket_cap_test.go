package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// setDailyCapForTest installs a daily ticket cap policy with a controllable
// clock and restores the previous policy on cleanup. Tests using it must not
// run in parallel: the policy is package state, like the production wiring.
func setDailyCapForTest(t *testing.T, limit int, loc *time.Location, now *time.Time) {
	t.Helper()
	prev := currentDailyTicketCapPolicy()
	setDailyTicketCapPolicy(dailyTicketCapPolicy{
		cap: limit,
		loc: loc,
		now: func() time.Time { return *now },
	})
	t.Cleanup(func() { setDailyTicketCapPolicy(prev) })
}

func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("tz database unavailable for %s: %v", name, err)
	}
	return loc
}

func capTestTask(id string) *Task {
	return &Task{ID: id, Title: id, ProjectPath: "/tmp/cap-project"}
}

// beginN begins n distinct tickets (T-1..T-n) and fails the test on any error.
func beginN(t *testing.T, lc *ExecutionLifecycle, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if _, err := lc.Begin(capTestTask(fmt.Sprintf("T-%d", i)), ExecStatusQueued); err != nil {
			t.Fatalf("Begin T-%d: %v", i, err)
		}
	}
}

func TestExecutionLifecycle_Begin_DailyTicketCap(t *testing.T) {
	berlin := mustLoadLocation(t, "Europe/Berlin")
	// 2026-10-22 12:00 UTC is 14:00 CEST (UTC+2) the same day.
	noon := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	t.Run("16th Begin on cap 15 is held and writes nothing", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		now := noon
		setDailyCapForTest(t, 15, time.UTC, &now)
		lc := NewExecutionLifecycle(store)

		beginN(t, lc, 15)

		held := capTestTask("T-16")
		execID, err := lc.Begin(held, ExecStatusQueued)
		if err == nil {
			t.Fatal("16th Begin on cap 15 succeeded, want hold")
		}
		if execID != "" || held.ExecutionID != "" {
			t.Errorf("held Begin returned execID %q / stamped %q, want none", execID, held.ExecutionID)
		}
		if !errors.Is(err, ErrDailyTicketCapReached) || !errors.Is(err, ErrDispatchGated) {
			t.Errorf("err = %v, want ErrDailyTicketCapReached wrapping ErrDispatchGated", err)
		}
		capErr, ok := asDailyTicketCapError(err)
		if !ok {
			t.Fatalf("err %v carries no *DailyTicketCapError", err)
		}
		wantResume := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
		if !capErr.ResumesAt.Equal(wantResume) || capErr.Cap != 15 || capErr.Dispatched != 15 || capErr.Day != "2026-10-22" {
			t.Errorf("DailyTicketCapError = %+v, want cap 15 dispatched 15 day 2026-10-22 resumes %s", capErr, wantResume)
		}

		// Nothing was written for the held task: no row, no claim.
		if rows, lerr := store.ListExecutionsForTask("T-16", held.ProjectPath); lerr != nil || len(rows) != 0 {
			t.Errorf("executions for held task = %d (err %v), want 0", len(rows), lerr)
		}
		if _, _, found, cerr := store.LatestClaimGeneration("T-16", held.ProjectPath); cerr != nil || found {
			t.Errorf("claim for held task found=%v err=%v, want none", found, cerr)
		}
		start, end := memory.LocalDayWindow(now, time.UTC)
		if n, cerr := store.CountDistinctTasksInWindow(start, end); cerr != nil || n != 15 {
			t.Errorf("day count = %d (err %v), want 15", n, cerr)
		}
	})

	t.Run("generation+1 retry of an already counted task passes", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		now := noon
		setDailyCapForTest(t, 15, time.UTC, &now)
		lc := NewExecutionLifecycle(store)

		beginN(t, lc, 15)

		retry := capTestTask("T-3")
		if _, err := lc.Begin(retry, ExecStatusQueued, 1); err != nil {
			t.Fatalf("retry Begin(T-3, gen 1) held: %v", err)
		}
		start, end := memory.LocalDayWindow(now, time.UTC)
		if n, err := store.CountDistinctTasksInWindow(start, end); err != nil || n != 15 {
			t.Errorf("day count after retry = %d (err %v), want 15 (retry is free)", n, err)
		}
		if _, err := lc.Begin(capTestTask("T-16"), ExecStatusQueued); !errors.Is(err, ErrDailyTicketCapReached) {
			t.Errorf("new task after retry: err = %v, want hold", err)
		}
	})

	t.Run("cap 0 never holds", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		now := noon
		setDailyCapForTest(t, 0, time.UTC, &now)

		beginN(t, NewExecutionLifecycle(store), 40)
	})

	t.Run("chat task is neither held nor counted", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		now := noon
		setDailyCapForTest(t, 2, time.UTC, &now)
		lc := NewExecutionLifecycle(store)

		// Chat first: must not use up the budget.
		for i := 1; i <= 3; i++ {
			chat := capTestTask(fmt.Sprintf("TG-%d", i))
			chat.IsChat = true
			if _, err := lc.Begin(chat, ExecStatusRunning); err != nil {
				t.Fatalf("chat Begin %d: %v", i, err)
			}
		}
		beginN(t, lc, 2)

		// At the cap: tickets are held, chat still goes through.
		if _, err := lc.Begin(capTestTask("T-3"), ExecStatusQueued); !errors.Is(err, ErrDailyTicketCapReached) {
			t.Errorf("ticket at cap: err = %v, want hold", err)
		}
		chat := capTestTask("TG-4")
		chat.IsChat = true
		if _, err := lc.Begin(chat, ExecStatusRunning); err != nil {
			t.Errorf("chat Begin at cap held: %v", err)
		}
		start, end := memory.LocalDayWindow(now, time.UTC)
		if n, err := store.CountDistinctTasksInWindow(start, end); err != nil || n != 2 {
			t.Errorf("day count = %d (err %v), want 2 (chat not counted)", n, err)
		}
	})

	t.Run("decomposed subtask rides on its parent's ticket", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		now := noon
		setDailyCapForTest(t, 1, time.UTC, &now)
		lc := NewExecutionLifecycle(store)

		parent := capTestTask("T-1")
		if _, err := lc.Begin(parent, ExecStatusDecomposed); err != nil {
			t.Fatalf("parent Begin: %v", err)
		}
		sub := capTestTask("T-1-1")
		sub.ParentExecutionID = parent.ExecutionID
		if _, err := lc.Begin(sub, ExecStatusQueued); err != nil {
			t.Fatalf("subtask Begin held at cap: %v", err)
		}
		start, end := memory.LocalDayWindow(now, time.UTC)
		if n, err := store.CountDistinctTasksInWindow(start, end); err != nil || n != 1 {
			t.Errorf("day count = %d (err %v), want 1", n, err)
		}
	})

	t.Run("day follows the configured timezone", func(t *testing.T) {
		// One ticket began at 21:30 UTC on Oct 22 = 23:30 CEST Oct 22. At 23:30
		// UTC (01:30 CEST Oct 23) it is yesterday in Berlin, today in UTC.
		began := time.Date(2026, 10, 22, 21, 30, 0, 0, time.UTC)
		later := time.Date(2026, 10, 22, 23, 30, 0, 0, time.UTC)

		tests := []struct {
			name       string
			loc        *time.Location
			wantHeld   bool
			wantResume time.Time
		}{
			{"UTC still the same day: held", time.UTC, true, time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)},
			{"Berlin already next day: admitted", berlin, false, time.Time{}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				store, cleanup := setupTestStore(t)
				defer cleanup()
				now := began
				setDailyCapForTest(t, 1, tt.loc, &now)
				lc := NewExecutionLifecycle(store)
				if _, err := lc.Begin(capTestTask("T-1"), ExecStatusQueued); err != nil {
					t.Fatalf("seed Begin: %v", err)
				}

				now = later
				_, err := lc.Begin(capTestTask("T-2"), ExecStatusQueued)
				if tt.wantHeld {
					capErr, ok := asDailyTicketCapError(err)
					if !ok {
						t.Fatalf("err = %v, want hold", err)
					}
					if !capErr.ResumesAt.Equal(tt.wantResume) {
						t.Errorf("ResumesAt = %s, want %s", capErr.ResumesAt, tt.wantResume)
					}
				} else if err != nil {
					t.Fatalf("err = %v, want admitted", err)
				}
			})
		}

		t.Run("Berlin resume instant is Berlin midnight", func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			now := later // 01:30 CEST Oct 23
			setDailyCapForTest(t, 1, berlin, &now)
			lc := NewExecutionLifecycle(store)
			if _, err := lc.Begin(capTestTask("T-1"), ExecStatusQueued); err != nil {
				t.Fatalf("seed Begin: %v", err)
			}
			_, err := lc.Begin(capTestTask("T-2"), ExecStatusQueued)
			capErr, ok := asDailyTicketCapError(err)
			if !ok {
				t.Fatalf("err = %v, want hold", err)
			}
			want := time.Date(2026, 10, 24, 0, 0, 0, 0, berlin) // 2026-10-23T22:00:00Z
			if !capErr.ResumesAt.Equal(want) || capErr.Day != "2026-10-23" {
				t.Errorf("hold = day %s resumes %s, want day 2026-10-23 resumes %s", capErr.Day, capErr.ResumesAt, want)
			}
		})
	})

	t.Run("a new day admits again", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		now := noon
		setDailyCapForTest(t, 1, time.UTC, &now)
		lc := NewExecutionLifecycle(store)

		if _, err := lc.Begin(capTestTask("T-1"), ExecStatusQueued); err != nil {
			t.Fatalf("seed Begin: %v", err)
		}
		_, err := lc.Begin(capTestTask("T-2"), ExecStatusQueued)
		capErr, ok := asDailyTicketCapError(err)
		if !ok {
			t.Fatalf("err = %v, want hold", err)
		}
		now = capErr.ResumesAt
		if _, err := lc.Begin(capTestTask("T-2"), ExecStatusQueued); err != nil {
			t.Errorf("Begin at the resume instant still held: %v", err)
		}
	})
}

// TestDispatcher_QueueTask_DailyTicketCapHold covers the dispatcher half of
// GH-5808: a held task is declined with the gated sentinel, left exactly where
// it is (no ledger row, no label change, no issue comment), and is admitted by
// a later QueueTask once the resume instant has passed.
func TestDispatcher_QueueTask_DailyTicketCapHold(t *testing.T) {
	berlin := mustLoadLocation(t, "Europe/Berlin")

	// Any gh invocation would be a label or comment write on the issue.
	fakeBin := t.TempDir()
	ghLog := filepath.Join(t.TempDir(), "gh-calls.log")
	script := "#!/bin/sh\necho \"$@\" >> \"" + ghLog + "\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))

	store, cleanup := setupTestStore(t)
	defer cleanup()

	// 2026-10-22 08:00 UTC = 10:00 CEST.
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	setDailyCapForTest(t, 1, berlin, &now)

	capDay := "2026-10-22"
	d := NewDispatcher(store, NewRunner(), nil)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	defer d.Stop()
	ctx := context.Background()

	newTask := func(id string) *Task {
		return &Task{
			ID: id, Title: id, Description: "do " + id,
			ProjectPath:   "/tmp/cap-dispatch-project",
			SourceAdapter: "github", SourceIssueID: "5808",
		}
	}

	// The one allowed ticket is seeded straight into the ledger so no worker
	// runs (a running task may legitimately call gh).
	if _, err := NewExecutionLifecycle(store).Begin(newTask("GH-1"), ExecStatusQueued); err != nil {
		t.Fatalf("first ticket: %v", err)
	}

	held := newTask("GH-2")
	execID, err := d.QueueTask(ctx, held)
	if execID != "" || !errors.Is(err, ErrDispatchGated) || !errors.Is(err, ErrDailyTicketCapReached) {
		t.Fatalf("held QueueTask = (%q, %v), want (\"\", gated ErrDailyTicketCapReached)", execID, err)
	}
	capErr, ok := asDailyTicketCapError(err)
	if !ok {
		t.Fatalf("held error carries no resume instant: %v", err)
	}
	wantResume := time.Date(2026, 10, 23, 0, 0, 0, 0, berlin)
	if !capErr.ResumesAt.Equal(wantResume) || capErr.Day != capDay {
		t.Errorf("hold = day %s resumes %s, want day %s resumes %s", capErr.Day, capErr.ResumesAt, capDay, wantResume)
	}

	// Offered again before the resume instant: still held, still nothing written.
	now = wantResume.Add(-time.Minute)
	if _, err := d.QueueTask(ctx, held); !errors.Is(err, ErrDailyTicketCapReached) {
		t.Fatalf("re-offer before resume: err = %v, want hold", err)
	}
	if rows, lerr := store.ListExecutionsForTask("GH-2", held.ProjectPath); lerr != nil || len(rows) != 0 {
		t.Errorf("held task has %d ledger rows (err %v), want 0", len(rows), lerr)
	}

	// Held the whole time: no label mutation, no issue comment — the fake gh
	// was never invoked.
	if data, rerr := os.ReadFile(ghLog); rerr == nil {
		t.Errorf("gh was invoked (label/comment write): %s", data)
	} else if !os.IsNotExist(rerr) {
		t.Fatalf("read gh log: %v", rerr)
	}

	// After the resume instant the same task is re-picked and admitted.
	now = wantResume
	execID, err = d.QueueTask(ctx, held)
	if err != nil || execID == "" {
		t.Fatalf("re-pick after resume = (%q, %v), want admitted", execID, err)
	}
}

// TestReportDailyTicketCapHold_OncePerDay pins the operator-visible dedupe: one
// alert per org per day (day embedded in the message metadata), one Info log
// per held task per day.
func TestReportDailyTicketCapHold_OncePerDay(t *testing.T) {
	resetCapHoldReports := func() {
		capHoldReports.Lock()
		capHoldReports.day, capHoldReports.tasks, capHoldReports.alerted = "", nil, false
		capHoldReports.Unlock()
	}
	resetCapHoldReports()
	t.Cleanup(resetCapHoldReports)

	var events []AlertEvent
	emit := func(ev AlertEvent) { events = append(events, ev) }
	log := NewRunner().log

	day1 := &DailyTicketCapError{Cap: 15, Dispatched: 15, Day: "2026-10-22", ResumesAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)}
	day2 := &DailyTicketCapError{Cap: 15, Dispatched: 15, Day: "2026-10-23", ResumesAt: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)}

	for _, id := range []string{"GH-1", "GH-2", "GH-1", "GH-2", "GH-3"} {
		reportDailyTicketCapHold(log, emit, capTestTask(id), day1)
	}
	if len(events) != 1 {
		t.Fatalf("day 1: %d alerts, want 1", len(events))
	}
	if events[0].Type != AlertEventTypeDailyTicketCapHit || events[0].Metadata["day"] != "2026-10-22" {
		t.Errorf("alert = %+v, want daily_ticket_cap_hit for 2026-10-22", events[0])
	}

	reportDailyTicketCapHold(log, emit, capTestTask("GH-1"), day2)
	reportDailyTicketCapHold(log, emit, capTestTask("GH-2"), day2)
	if len(events) != 2 || events[1].Metadata["day"] != "2026-10-23" {
		t.Fatalf("after day 2: %d alerts (%+v), want a second one for 2026-10-23", len(events), events)
	}

	// A nil emitter (no alerts engine) must not panic.
	reportDailyTicketCapHold(log, nil, capTestTask("GH-9"), day2)
}

// TestExecuteSubIssues_DailyTicketCapHoldsChildrenWithoutFailingParent (GH-5808):
// epic sub-issues go through Begin like any ticket. A child past the cap is
// neither run nor failed, writes no ledger row, and the parent carries on.
func TestExecuteSubIssues_DailyTicketCapHoldsChildrenWithoutFailingParent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	setDailyCapForTest(t, 1, time.UTC, &now)

	issues := makeSubIssues(3, 5808)
	parent := &Task{ID: "GH-5807", Title: "[epic] capped", ProjectPath: "/project-5808"}

	var ran []string
	runner := newTestRunnerWithExecFunc(func(_ context.Context, task *Task) (*ExecutionResult, error) {
		ran = append(ran, task.ID)
		return &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "deadbeef", PRUrl: "https://github.com/owner/repo/pull/1"}, nil
	})
	runner.logStore = store

	childStates, _, err := runner.executeSubIssuesTracked(context.Background(), parent, issues, parent.ProjectPath, "")
	if err != nil {
		t.Fatalf("held children failed the parent: %v", err)
	}
	if want := []string{"completed", "held", "held"}; fmt.Sprint(childStates) != fmt.Sprint(want) {
		t.Errorf("childStates = %v, want %v", childStates, want)
	}
	if len(ran) != 1 || ran[0] != "GH-5808" {
		t.Errorf("executed children = %v, want only GH-5808", ran)
	}
	for _, id := range []string{"GH-5809", "GH-5810"} {
		if rows, lerr := store.ListExecutionsForTask(id, parent.ProjectPath); lerr != nil || len(rows) != 0 {
			t.Errorf("%s has %d ledger rows (err %v), want 0 (held)", id, len(rows), lerr)
		}
	}
}
