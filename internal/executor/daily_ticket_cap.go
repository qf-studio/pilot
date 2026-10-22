package executor

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// ErrDailyTicketCapReached marks a Begin that was held because the org already
// started executor.daily_ticket_cap tickets in its current calendar day
// (GH-5808, TASK-589). It wraps ErrDispatchGated: nothing ran and nothing was
// written, so every caller that already treats a gated decline as "leave the
// task where it is" (no failure, no label change, no comment) handles it the
// same way. The concrete error returned by Begin is a *DailyTicketCapError
// carrying the resume instant; use errors.As to read it.
var ErrDailyTicketCapReached = fmt.Errorf("daily ticket cap reached: %w", ErrDispatchGated)

// DailyTicketCapError is the typed form of ErrDailyTicketCapReached.
type DailyTicketCapError struct {
	Cap        int
	Dispatched int
	// Day is the org-local calendar day the cap applies to ("2006-01-02").
	Day string
	// ResumesAt is the next org-local midnight, the earliest instant a held
	// task can be admitted again.
	ResumesAt time.Time
}

func (e *DailyTicketCapError) Error() string {
	return fmt.Sprintf("daily ticket cap reached: %d of %d tickets started on %s, resumes at %s",
		e.Dispatched, e.Cap, e.Day, e.ResumesAt.Format(time.RFC3339))
}

// Unwrap makes errors.Is match ErrDailyTicketCapReached and, through it,
// ErrDispatchGated.
func (e *DailyTicketCapError) Unwrap() error { return ErrDailyTicketCapReached }

// dailyTicketCapPolicy is the in-process daily ticket cap. Unlike most
// executor config it is package-level because ExecutionLifecycle is
// constructed fresh at every call site (dispatcher, epic loop, comms, CLI) and
// the point of holding at Begin is that no path can skip it.
type dailyTicketCapPolicy struct {
	cap int
	loc *time.Location
	// now is the clock; nil means time.Now. Tests inject a fixed one.
	now func() time.Time
}

var (
	dailyTicketCapMu     sync.RWMutex
	currentDailyCapState = dailyTicketCapPolicy{loc: time.UTC}

	// dailyTicketCapAdmitMu serializes check-then-write in Begin when a cap is
	// set, so two concurrent Begins cannot both observe cap-1 tickets and both
	// admit.
	dailyTicketCapAdmitMu sync.Mutex
)

// SetDailyTicketCap installs executor.daily_ticket_cap and
// executor.daily_ticket_cap_timezone (GH-5808). Called from NewRunnerWithConfig
// so every runner-construction path wires it. limit <= 0 disables the hold. An
// empty timezone means UTC; config.Load already rejects "Local" and unknown
// names, so a bad name here falls back to UTC and is reported to the caller.
func SetDailyTicketCap(limit int, timezone string) error {
	loc := time.UTC
	var err error
	if timezone != "" {
		if l, lerr := time.LoadLocation(timezone); lerr != nil {
			err = fmt.Errorf("daily_ticket_cap_timezone %q: %w (using UTC)", timezone, lerr)
		} else {
			loc = l
		}
	}
	if limit < 0 {
		limit = 0
	}
	setDailyTicketCapPolicy(dailyTicketCapPolicy{cap: limit, loc: loc})
	return err
}

func setDailyTicketCapPolicy(p dailyTicketCapPolicy) {
	if p.loc == nil {
		p.loc = time.UTC
	}
	dailyTicketCapMu.Lock()
	currentDailyCapState = p
	dailyTicketCapMu.Unlock()
}

func currentDailyTicketCapPolicy() dailyTicketCapPolicy {
	dailyTicketCapMu.RLock()
	defer dailyTicketCapMu.RUnlock()
	return currentDailyCapState
}

// DailyTicketCapLocation returns the configured cap and the zone its day is
// measured in (cap 0 = unlimited).
func DailyTicketCapLocation() (int, *time.Location) {
	p := currentDailyTicketCapPolicy()
	return p.cap, p.loc
}

func (p dailyTicketCapPolicy) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// checkDailyTicketCap is the admission rule behind Begin and the dispatcher's
// pre-check. It writes nothing. A nil return means admit.
//
//   - cap 0 never holds.
//   - A chat-filed task or a decomposed subtask is never held (and never
//     counted: see not_ticket).
//   - A task id that already has a countable row inside today's window passes:
//     a generation+1 retry or repick was counted when it first began.
//   - Otherwise hold when the distinct task ids already started today have
//     reached the cap.
//
// A store error fails open (logged): the cap is a plan allowance, not a safety
// gate, and a broken count must not wedge every dispatch.
func checkDailyTicketCap(store *memory.Store, p dailyTicketCapPolicy, task *Task) error {
	if store == nil || p.cap <= 0 || task == nil || task.notATicket() {
		return nil
	}
	now := p.clock()
	start, end := memory.LocalDayWindow(now, p.loc)

	counted, err := store.HasCountedTaskInWindow(task.ID, start, end)
	if err != nil {
		slog.Warn("daily ticket cap: task lookup failed, admitting (fail-open)",
			slog.String("task_id", task.ID), slog.Any("error", err))
		return nil
	}
	if counted {
		return nil
	}
	n, err := store.CountDistinctTasksInWindow(start, end)
	if err != nil {
		slog.Warn("daily ticket cap: count failed, admitting (fail-open)",
			slog.String("task_id", task.ID), slog.Any("error", err))
		return nil
	}
	if n < p.cap {
		return nil
	}
	return &DailyTicketCapError{
		Cap:        p.cap,
		Dispatched: n,
		Day:        start.Format("2006-01-02"),
		ResumesAt:  end,
	}
}

// notATicket reports whether the task is outside the daily ticket cap: chat
// work has its own cap (adapters.chat.daily_turn_cap), a decomposed subtask
// (ParentExecutionID set, decompose.go) rides on its parent's ticket — holding
// it would strand a half-decomposed parent that was already admitted — and a
// canary task is synthetic (the count already excludes canary rows).
func (t *Task) notATicket() bool {
	return t.IsChat || t.IsCanary || t.ParentExecutionID != ""
}

// CheckDailyTicketCap reports whether Begin would hold task right now, without
// writing anything. The dispatcher uses it to decline a held task before any
// repick-backoff bookkeeping runs.
func (l *ExecutionLifecycle) CheckDailyTicketCap(task *Task) error {
	return checkDailyTicketCap(l.store, currentDailyTicketCapPolicy(), task)
}

// capHoldReports dedupes the operator-visible side of a hold: one Info log per
// held task per day and one alert per org per day. A hold is reported from
// every call site that can see one (dispatcher, epic loop), so the dedupe
// lives here rather than at any one of them.
var capHoldReports = struct {
	sync.Mutex
	day     string
	tasks   map[string]struct{}
	alerted bool
}{}

// reportDailyTicketCapHold logs and alerts a hold exactly once per task and
// once per org per day. emit may be nil (no alerts engine). The day is part of
// the alert message, so the alert rule needs no cooldown to stay at one per
// day.
func reportDailyTicketCapHold(log *slog.Logger, emit func(AlertEvent), task *Task, capErr *DailyTicketCapError) {
	capHoldReports.Lock()
	if capHoldReports.day != capErr.Day {
		capHoldReports.day = capErr.Day
		capHoldReports.tasks = make(map[string]struct{})
		capHoldReports.alerted = false
	}
	key := task.ProjectPath + "\x00" + task.ID
	_, seen := capHoldReports.tasks[key]
	capHoldReports.tasks[key] = struct{}{}
	alertNow := !capHoldReports.alerted
	capHoldReports.alerted = true
	capHoldReports.Unlock()

	if !seen {
		log.Info("daily ticket cap reached — task held, will be re-picked after the resume time",
			slog.String("task_id", task.ID),
			slog.String("project", task.ProjectPath),
			slog.Int("daily_ticket_cap", capErr.Cap),
			slog.Int("dispatched_today", capErr.Dispatched),
			slog.String("day", capErr.Day),
			slog.Time("resumes_at", capErr.ResumesAt),
		)
	}
	if alertNow && emit != nil {
		emit(AlertEvent{
			Type:      AlertEventTypeDailyTicketCapHit,
			TaskID:    task.ID,
			TaskTitle: task.Title,
			Project:   task.ProjectPath,
			Metadata: map[string]string{
				"day":              capErr.Day,
				"daily_ticket_cap": fmt.Sprintf("%d", capErr.Cap),
				"dispatched_today": fmt.Sprintf("%d", capErr.Dispatched),
				"resumes_at":       capErr.ResumesAt.Format(time.RFC3339),
			},
			Timestamp: time.Now(),
		})
	}
}

// asDailyTicketCapError unwraps err to a *DailyTicketCapError.
func asDailyTicketCapError(err error) (*DailyTicketCapError, bool) {
	var capErr *DailyTicketCapError
	if errors.As(err, &capErr) {
		return capErr, true
	}
	return nil, false
}
