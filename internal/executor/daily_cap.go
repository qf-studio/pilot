package executor

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
)

// TaskSourceAdapterChat marks a task filed from a chat surface (internal/comms).
// Chat-filed tasks are not tickets: the daily ticket cap neither counts nor
// holds them (GH-5808). Persisted in executions.task_source_adapter so the
// count query can exclude them.
const TaskSourceAdapterChat = memory.TaskSourceAdapterChat

// ErrDailyTicketCapReached is the sentinel ExecutionLifecycle.Begin returns
// when executor.daily_ticket_cap is already reached for the current org-local
// day (GH-5808). It wraps ErrDispatchGated: the task was declined by an
// admission check, not failed, so callers must leave it where it is — no
// label change, no issue comment, no failure alert — and re-offer it after the
// resume instant. The concrete error is a *DailyTicketCapHold; extract it with
// errors.As to read the resume instant.
var ErrDailyTicketCapReached = fmt.Errorf("daily ticket cap reached: %w", ErrDispatchGated)

// DailyTicketCapHold is the error Begin returns while the daily ticket cap
// holds a task. It satisfies errors.Is(err, ErrDailyTicketCapReached) and
// errors.Is(err, ErrDispatchGated).
type DailyTicketCapHold struct {
	// Cap is the configured executor.daily_ticket_cap.
	Cap int
	// Dispatched is how many distinct tickets already began in the day.
	Dispatched int
	// Day is the org-local calendar day (YYYY-MM-DD) being held.
	Day string
	// ResumesAt is the next org-local midnight, when the cap lifts.
	ResumesAt time.Time
}

func (h *DailyTicketCapHold) Error() string {
	return fmt.Sprintf("daily ticket cap reached (%d of %d on %s), resumes at %s",
		h.Dispatched, h.Cap, h.Day, h.ResumesAt.Format(time.RFC3339))
}

// Unwrap lets errors.Is match ErrDailyTicketCapReached (and, through it,
// ErrDispatchGated).
func (h *DailyTicketCapHold) Unwrap() error { return ErrDailyTicketCapReached }

// DailyCapGate decides whether ExecutionLifecycle.Begin may start another
// ticket today (GH-5808) and owns the once-per-day announcements, so every
// ExecutionLifecycle built from the same Runner shares one set of "already
// announced" state. A nil gate, or one with Cap <= 0, never holds.
type DailyCapGate struct {
	cap    int
	tzName string
	loc    *time.Location
	now    func() time.Time // test seam; time.Now when nil
	log    *slog.Logger
	// alerts resolves the processor at emit time, because the Runner's
	// processor is wired after the gate can first be asked for.
	alerts func() AlertEventProcessor

	mu       sync.Mutex
	stateDay string          // org-local day the two fields below describe
	logged   map[string]bool // task ids whose hold was already logged on stateDay
	alerted  bool            // daily_ticket_cap_hit already fired on stateDay
}

// NewDailyCapGate builds a gate for executor.daily_ticket_cap and
// executor.daily_ticket_cap_timezone. An empty timezone means UTC. config
// validation already rejects "Local" and unknown names, so an unloadable zone
// here is a programming error; it degrades to UTC with a warning rather than
// disabling the cap.
func NewDailyCapGate(dailyCap int, timezone string) *DailyCapGate {
	g := &DailyCapGate{
		cap:    dailyCap,
		tzName: timezone,
		log:    logging.WithComponent("daily-cap"),
		logged: make(map[string]bool),
	}
	if g.tzName == "" {
		g.tzName = "UTC"
	}
	loc, err := time.LoadLocation(g.tzName)
	if err != nil {
		g.log.Warn("invalid daily_ticket_cap_timezone, using UTC",
			slog.String("timezone", g.tzName), slog.Any("error", err))
		loc = time.UTC
	}
	g.loc = loc
	return g
}

// Enabled reports whether the gate can ever hold.
func (g *DailyCapGate) Enabled() bool { return g != nil && g.cap > 0 }

// day returns the org-local calendar day containing now and its bounds.
//
// Decision: bounds are built with time.Date in the org zone, not start+24h, so
// a DST change yields a 23/25-hour day — the same construction as the standing
// endpoint (gateway.dailyTicketUsage), keeping both counts over one window.
func (g *DailyCapGate) day() (label string, start, end time.Time) {
	now := time.Now()
	if g.now != nil {
		now = g.now()
	}
	now = now.In(g.loc)
	start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, g.loc)
	end = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, g.loc)
	return start.Format("2006-01-02"), start, end
}

// Check returns a *DailyTicketCapHold when starting task would exceed the cap,
// and nil otherwise. Never holds: with cap 0, a nil store, a canary task, a
// chat-filed task, or a task id that already has an execution row in today's
// window (a generation+1 retry or repick was counted when first begun). A
// store error fails open — a broken count query must not stop the pipeline.
//
// On a hold it logs once per task per day and emits one daily_ticket_cap_hit
// alert per day. It writes nothing to the store.
//
// Decision: the count-then-claim sequence is not atomic, so two tickets racing
// at cap-1 can both begin and overshoot by the number of racers. The
// dispatcher serializes QueueTask (dispatchMu) and the epic loop is
// sequential, so the window is a few concurrent channels at most; a hard
// ceiling would need the claim insert itself to carry the count.
func (g *DailyCapGate) Check(store *memory.Store, task *Task) *DailyTicketCapHold {
	if !g.Enabled() || store == nil || task == nil {
		return nil
	}
	// A decomposed subtask runs inline inside its parent ticket's execution
	// (ParentExecutionID set, see TaskDecomposer.createSubtasks): the parent
	// was counted, and holding a subtask here would strand it, because the
	// parent's decomposition is never re-run. Canary and chat-filed tasks are
	// not tickets.
	if task.IsCanary || task.ParentExecutionID != "" || task.SourceAdapter == TaskSourceAdapterChat {
		return nil
	}

	day, start, end := g.day()

	counted, err := store.TaskHasExecutionInWindow(task.ID, start, end)
	if err != nil {
		g.log.Warn("daily ticket cap: could not look up task in today's window, not holding",
			slog.String("task_id", task.ID), slog.Any("error", err))
		return nil
	}
	if counted {
		return nil
	}
	// A live owner (queued/running, e.g. begun before midnight) is not a new
	// ticket either; the claim, not the cap, decides what happens to it.
	if live, err := store.IsTaskQueued(task.ID, task.ProjectPath); err == nil && live {
		return nil
	}

	dispatched, err := store.CountDistinctTasksInWindow(start, end)
	if err != nil {
		g.log.Warn("daily ticket cap: could not count today's tickets, not holding",
			slog.String("task_id", task.ID), slog.Any("error", err))
		return nil
	}
	if dispatched < g.cap {
		return nil
	}

	hold := &DailyTicketCapHold{Cap: g.cap, Dispatched: dispatched, Day: day, ResumesAt: end}
	g.announce(task, hold)
	return hold
}

// announce logs the hold once per task per day and emits the
// daily_ticket_cap_hit alert once per day.
func (g *DailyCapGate) announce(task *Task, hold *DailyTicketCapHold) {
	g.mu.Lock()
	if g.stateDay != hold.Day {
		g.stateDay, g.logged, g.alerted = hold.Day, make(map[string]bool), false
	}
	logIt := !g.logged[task.ID]
	g.logged[task.ID] = true
	// Only mark the day alerted when a processor can take the event: the
	// alerts engine is wired after the first ticket can already be held.
	var processor AlertEventProcessor
	if g.alerts != nil {
		processor = g.alerts()
	}
	alertIt := !g.alerted && processor != nil
	if alertIt {
		g.alerted = true
	}
	g.mu.Unlock()

	if logIt {
		g.log.Info("daily ticket cap reached, holding task until the cap lifts",
			slog.String("task_id", task.ID),
			slog.String("project", task.ProjectPath),
			slog.Int("cap", hold.Cap),
			slog.Int("dispatched_today", hold.Dispatched),
			slog.String("day", hold.Day),
			slog.String("timezone", g.tzName),
			slog.Time("resumes_at", hold.ResumesAt),
		)
	}
	if !alertIt {
		return
	}
	// The day rides in the alert message (alerts.handleDailyTicketCapHit), not
	// in a cooldown: cooldown is per rule, so it cannot express "once per day".
	processor.ProcessEvent(AlertEvent{
		Type:      AlertEventTypeDailyTicketCapHit,
		TaskID:    task.ID,
		TaskTitle: task.Title,
		Project:   task.ProjectPath,
		Metadata: map[string]string{
			"day":        hold.Day,
			"cap":        fmt.Sprintf("%d", hold.Cap),
			"dispatched": fmt.Sprintf("%d", hold.Dispatched),
			"timezone":   g.tzName,
			"resumes_at": hold.ResumesAt.Format(time.RFC3339),
		},
		Timestamp: time.Now(),
	})
}

// epicHeldResult turns an executeSubIssuesTracked error caused by the daily
// ticket cap into the epic parent's result (GH-5808): not a failure, so no
// pilot-failed label and no failure alert, and not Success either, since no
// PR was opened for a partial epic. Outcome "skipped" is the same benign
// non-failure ErrParentDone already maps to; the row stays re-pickable
// (HasTerminalCompletion does not count it as done), so a later pick resumes
// the open children through resumeDecomposedParent once the cap lifts. Any
// other err (including nil) reports ok=false.
func epicHeldResult(task *Task, err error, plan *EpicPlan, start time.Time) (*ExecutionResult, bool) {
	var hold *DailyTicketCapHold
	if !errors.As(err, &hold) {
		return nil, false
	}
	return &ExecutionResult{
		TaskID:   task.ID,
		Success:  false,
		Outcome:  "skipped",
		Error:    fmt.Sprintf("epic sub-issues held: %v", err),
		Duration: time.Since(start),
		IsEpic:   true,
		EpicPlan: plan,
	}, true
}
