package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

// Engine is the core alerting engine that processes events and triggers alerts
type Engine struct {
	config     *AlertConfig
	dispatcher *Dispatcher
	logger     *slog.Logger

	// State tracking
	mu                  sync.RWMutex
	lastAlertTimes      map[string]time.Time     // rule name -> last fired time
	consecutiveFailures map[string]int           // project -> consecutive failure count
	taskLastProgress    map[string]progressState // task ID -> last progress state
	alertHistory        []AlertHistory
	retryTracker        map[string]int       // source (issue/PR) -> consecutive failure count (GH-848)
	retryLastSeen       map[string]time.Time // source -> last failure time, for TTL eviction (TASK-357 E7)
	activeAlerts        map[string]*activeAlert

	// Channels for events. priorityCh carries high-severity events
	// (escalation / OOM / budget / security) on a dedicated buffer so a flood of
	// ordinary task events filling eventCh cannot starve a critical alert (E1).
	eventCh    chan Event
	priorityCh chan Event
	done       chan struct{}

	// dispatchCh feeds a single background delivery worker. fireAlert enqueues
	// here instead of calling Dispatch inline, so a slow/hung channel can never
	// block the event loop (E1). Sequential delivery keeps ordering and adds no
	// new concurrency. dispatchWG tracks in-flight deliveries for WaitForDispatch.
	dispatchCh chan dispatchJob
	dispatchWG sync.WaitGroup
	// started is set once Start() launches the dispatch worker. Before that
	// (direct callers / tests) fireAlert delivers inline so behavior is synchronous.
	started atomic.Bool

	// recentAlerts deduplicates identical alerts ({rule|source|message}) within
	// duplicateSuppressTTL when config.Defaults.SuppressDuplicates is set (E5).
	recentAlerts map[string]time.Time

	// metrics accumulates fired/dropped counters; share the same instance with the
	// Dispatcher via WithAlertMetrics+WithDispatcherMetrics so delivery counts appear
	// in AlertSnapshot too.
	metrics *AlertMetrics

	// lifecycle transitions an orphan-evicted task's execution row to
	// "stalled" via the ExecutionLifecycle chokepoint (GH-4562). nil (the
	// zero value, and the default for every call site that doesn't pass
	// WithExecutionLifecycle) makes evictStuckExecution a no-op — matching
	// every other nil-store guard already established across
	// executor.ExecutionLifecycle and epic.go's sweep helpers, and letting
	// short-lived CLI callers (pilot alerts test, eval regression alerts)
	// skip wiring a store for a code path they never meaningfully exercise.
	lifecycle *executor.ExecutionLifecycle

	// deadManTrackers holds every registered DeadManTracker (TASK-441 L2,
	// GH-4709), keyed by name — memoized by RegisterDeadManTracker so
	// repeated registration (e.g. a per-repo wrapper rebuilt each poll
	// cycle) shares one set of counters instead of resetting them.
	deadManTrackers map[string]*DeadManTracker

	// activeAlertStore persists activeAlerts through fire/resolve so a
	// condition that recovers while the daemon is down still emits its
	// resolution once the daemon restarts (GH-4890). nil (the default,
	// unless WithActiveAlertStore is passed) makes every persistence call a
	// no-op — the engine then behaves exactly as it did before this store
	// existed. Writes are best-effort and off the alerting path: a store
	// error is logged and the alert still fires/resolves.
	activeAlertStore ActiveAlertStore
}

// minOrphanEvictionThreshold floors evaluateStuckTasks' orphan-eviction window
// regardless of how short the task_stuck rule's ProgressUnchangedFor is
// configured. It mirrors the executor's worst-case single-attempt budget: a
// Complex task's 60m default timeout doubled by the runner's watchdog
// (runner.go watchdogTimeout = 2 * timeout) = 120m. GH-4092: eviction fired at
// a fixed 4×threshold (40m at the 10m default) while workers were still
// legitimately executing, mistaking a flat-progress self-correction loop for a
// crashed one.
const minOrphanEvictionThreshold = 120 * time.Minute

// taskStuckQueuedPhase is the exact Phase string the dispatcher emits
// (internal/executor/dispatcher.go queueSingleTask, via runner.EmitProgress)
// the moment a task is admitted to a project's queue, before any worker has
// picked it up. evaluateStuckTasks treats entries still carrying this phase
// as merely waiting in line, not stuck (GH-4416).
const taskStuckQueuedPhase = "Queued"

type progressState struct {
	Progress      int
	UpdatedAt     time.Time
	Phase         string
	LastAlertedAt time.Time // Per-task alert cooldown (GH-2204)
}

// Event represents an event that might trigger an alert
type Event struct {
	Type      EventType
	TaskID    string
	TaskTitle string
	Project   string
	Phase     string
	Progress  int
	Error     string
	Source    string // e.g. "adapter:github"; keys active-alert state when TaskID is empty
	Metadata  map[string]string
	Timestamp time.Time
	// test-only: set by flushForTest to drain the event queue
	testFlushResp chan struct{}
}

// EventType categorizes incoming events
type EventType string

const (
	EventTypeTaskStarted    EventType = "task_started"
	EventTypeTaskProgress   EventType = "task_progress"
	EventTypeTaskCompleted  EventType = "task_completed"
	EventTypeTaskFailed     EventType = "task_failed"
	EventTypeCostUpdate     EventType = "cost_update"
	EventTypeSecurityEvent  EventType = "security_event"
	EventTypeBudgetExceeded EventType = "budget_exceeded"
	EventTypeBudgetWarning  EventType = "budget_warning"

	// Autopilot health events (GH-728)
	EventTypeAutopilotMetrics EventType = "autopilot_metrics"

	// Escalation events (GH-885)
	EventTypeEscalation EventType = "escalation"

	// Eval regression events (GH-2065)
	EventTypeEvalRegression EventType = "eval_regression"

	// OOM-killed backend events (GH-2332). Routed through the task-failed
	// handler so consecutive-failure tracking keeps working, but kept as a
	// distinct type so rules and dashboards can single these out.
	EventTypeOOMKilled EventType = "oom_killed"

	// Config/credential health events (GH-3718): fired when a resolved
	// credential (e.g. a GitHub token) fails an authenticated validation
	// call at startup, so a dead credential doesn't fail silently.
	EventTypeConfigError EventType = "config_error"

	EventTypeConfigHealthy EventType = "config_healthy"

	// Release missing events (GH-3952): fired when a merged pilot/GH-* PR
	// did not produce its expected release tag, so a stalled release
	// pipeline doesn't go unnoticed.
	EventTypeReleaseMissing EventType = "release_missing"

	// Lane starvation events (GH-4454): fired every poll cycle a project
	// lane has open pilot-labeled issues but zero queued/running executions.
	// Metadata carries repo, project_path, open_issue_count, and
	// poll_cycles_starved (the running streak) — see
	// autopilot.Controller.reconcileLaneStarvation, the sole emitter.
	EventTypeLaneStarvation EventType = "lane_starvation"

	// Dispatch loop breaker events (GH-4469): fired exactly once by
	// handleIssueGeneric (cmd/pilot/handler_common.go) when a task's
	// consecutive dispatch-and-reject count reaches repickLoopBreakerThreshold
	// (10) — regardless of whether the rejections were backoff gates or
	// genuine failures. Metadata carries task_id and consecutive_drops.
	EventTypeDispatchLoopBreaker EventType = "dispatch_loop_breaker"

	// GitHub side-effect events (GH-4670): fired by the executor's post-run
	// audit (executor.auditGithubSideEffects) when a GitHub issue in the
	// task's own repo was closed or reopened during the run window OTHER
	// than the issue the session was dispatched to fix — the GH-4649
	// incident class. Metadata carries repo, issue, state, and task_issue.
	EventTypeGithubSideEffect EventType = "github_sideeffect"

	// Intent judge failure streak events (GH-4669): fired exactly once by
	// the pre-flight judge wrapper (cmd/pilot/poller_github.go,
	// sdkPreFlightJudge.JudgeIssue) when its own consecutive-failure counter
	// reaches judgeFailureStreakAlertThreshold — the fail-open path that
	// hid a 17-day, 100%-failure incident (4,321 context_deadline kills)
	// until it was caught while diagnosing GH-4648. Metadata carries repo
	// and consecutive_failures.
	EventTypeIntentJudgeFailureStreak EventType = "intent_judge_failure_streak"
)

const (
	// dispatchBacklog bounds the delivery queue feeding the dispatch worker (E1).
	// When the worker is stuck on a hung channel and the backlog fills, further
	// deliveries are dropped with a counter rather than blocking the event loop.
	dispatchBacklog = 256
	// duplicateSuppressTTL is the window within which an identical alert
	// ({rule|source|message}) is suppressed when SuppressDuplicates is enabled (E5).
	duplicateSuppressTTL = 5 * time.Minute
)

// dispatchJob is a queued alert delivery handled by the dispatch worker (E1).
type dispatchJob struct {
	rule     AlertRule
	alert    *Alert
	channels []string
}

// EngineOption configures the Engine
type EngineOption func(*Engine)

// WithLogger sets the logger
func WithLogger(logger *slog.Logger) EngineOption {
	return func(e *Engine) {
		e.logger = logger
	}
}

// WithDispatcher sets the dispatcher
func WithDispatcher(d *Dispatcher) EngineOption {
	return func(e *Engine) {
		e.dispatcher = d
	}
}

// WithAlertMetrics injects a shared AlertMetrics instance.
// Pass the same instance to WithDispatcherMetrics so delivery counters from the
// Dispatcher appear in Engine.AlertSnapshot().
func WithAlertMetrics(m *AlertMetrics) EngineOption {
	return func(e *Engine) {
		e.metrics = m
	}
}

// WithExecutionLifecycle wires the ExecutionLifecycle chokepoint the
// stuck-task evictor uses to transition an orphan-evicted task's still-alive
// execution row to "stalled" (GH-4562), instead of dropping the in-memory
// tracker entry and silently orphaning the row. Passing lifecycle through the
// constructor — rather than the evictor reaching into internal/memory or
// internal/executor ad hoc — keeps Engine's only executor-package dependency
// at this one seam.
func WithExecutionLifecycle(lifecycle *executor.ExecutionLifecycle) EngineOption {
	return func(e *Engine) {
		e.lifecycle = lifecycle
	}
}

// WireLifecycleAlertProcessor propagates processor into the
// ExecutionLifecycle passed via WithExecutionLifecycle at construction time
// (TASK-441 L5, GH-4716), so that lifecycle's own terminal writes (the
// stuck-task evictor's stall Finish, engine.go's evictStalledTasks) run the
// finish-tripwire sweep with somewhere to relay dead-man attempt/success/
// failure signals — the same processor Engine itself is normally wrapped in
// via NewEngineAdapter. Must be called after the adapter exists, which is
// necessarily after NewEngine returns (see initAlerts/runPollingMode call
// sites) — a chicken-and-egg WithExecutionLifecycle can't resolve at
// construction time. No-op if no lifecycle was wired at construction (e.g. a
// caller that never passed WithExecutionLifecycle).
func (e *Engine) WireLifecycleAlertProcessor(processor executor.AlertEventProcessor) {
	if e == nil || e.lifecycle == nil {
		return
	}
	e.lifecycle.SetAlertProcessor(processor)
}

// WithActiveAlertStore wires the optional persistence store for currently-firing
// alerts (GH-4890). When set, the engine writes through to the store on fire
// (markActive) and resolve (handleConfigHealthy), and NewEngine rehydrates the
// in-memory activeAlerts map from it — so an alert that recovered while the
// daemon was down still emits its resolution, to the exact channels the
// original alert was delivered to, once the daemon restarts. Omitting this
// option (the default) makes the engine behave exactly as before: active-alert
// state lives only in the in-memory map and is lost on restart.
func WithActiveAlertStore(store ActiveAlertStore) EngineOption {
	return func(e *Engine) {
		e.activeAlertStore = store
	}
}

// NewEngine creates a new alerting engine
func NewEngine(config *AlertConfig, opts ...EngineOption) *Engine {
	e := &Engine{
		config:              config,
		logger:              slog.Default(),
		lastAlertTimes:      make(map[string]time.Time),
		consecutiveFailures: make(map[string]int),
		taskLastProgress:    make(map[string]progressState),
		alertHistory:        make([]AlertHistory, 0),
		retryTracker:        make(map[string]int),
		activeAlerts:        make(map[string]*activeAlert),
		retryLastSeen:       make(map[string]time.Time),
		eventCh:             make(chan Event, 100),
		priorityCh:          make(chan Event, 100),
		done:                make(chan struct{}),
		dispatchCh:          make(chan dispatchJob, dispatchBacklog),
		recentAlerts:        make(map[string]time.Time),
		metrics:             NewAlertMetrics(),
	}

	for _, opt := range opts {
		opt(e)
	}

	e.rehydrateActiveAlerts()

	return e
}

// rehydrateActiveAlerts loads persisted active-alert rows (if a store was
// wired via WithActiveAlertStore) and re-seeds the in-memory activeAlerts map
// (GH-4890). Runs once, at construction, so a restart during an outage
// doesn't lose the record that something is still firing. A load failure is
// logged and treated as "nothing to rehydrate" — best-effort, off the
// alerting path, matching persistActiveAlert/deletePersistedActiveAlert.
func (e *Engine) rehydrateActiveAlerts() {
	if e.activeAlertStore == nil {
		return
	}
	rows, err := e.activeAlertStore.LoadActiveAlerts()
	if err != nil {
		e.logger.Warn("failed to rehydrate active alerts from store", "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}

	e.mu.Lock()
	for _, row := range rows {
		e.activeAlerts[activeAlertKey(row.RuleName, row.Source)] = &activeAlert{
			rule: AlertRule{Name: row.RuleName},
			alert: &Alert{
				ID:          row.AlertID,
				Type:        AlertType(row.AlertType),
				Title:       row.Title,
				Message:     row.Message,
				Source:      row.Source,
				ProjectPath: row.ProjectPath,
				Metadata:    row.Metadata,
				CreatedAt:   row.CreatedAt,
			},
			// channels is the set the original alert was delivered to — carried
			// through so the rehydrated resolution reaches those exact
			// destinations instead of being re-filtered by its own info
			// severity (dispatchResolution dispatches directly to
			// active.channels, bypassing resolveChannels).
			channels: row.Channels,
		}
	}
	e.mu.Unlock()

	e.logger.Info("rehydrated active alerts from store", "count", len(rows))
}

// persistActiveAlert writes active through to the store, if one is wired
// (GH-4890). Best-effort and off the alerting path: a store error is logged,
// never returned or surfaced — the alert has already fired by the time this
// runs (called from markActive after the in-memory map is updated).
func (e *Engine) persistActiveAlert(active *activeAlert) {
	if e.activeAlertStore == nil {
		return
	}
	row := &memory.ActiveAlert{
		RuleName:    active.rule.Name,
		Source:      active.alert.Source,
		AlertID:     active.alert.ID,
		AlertType:   string(active.alert.Type),
		Title:       active.alert.Title,
		Message:     active.alert.Message,
		ProjectPath: active.alert.ProjectPath,
		Metadata:    active.alert.Metadata,
		Channels:    active.channels,
		CreatedAt:   active.alert.CreatedAt,
	}
	if err := e.activeAlertStore.UpsertActiveAlert(row); err != nil {
		e.logger.Warn("failed to persist active alert",
			"rule", active.rule.Name,
			"source", active.alert.Source,
			"error", err,
		)
	}
}

// deletePersistedActiveAlert removes the persisted row on resolution, if a
// store is wired (GH-4890). Best-effort and off the alerting path: a store
// error is logged, never returned — the resolution still dispatches.
func (e *Engine) deletePersistedActiveAlert(ruleName, source string) {
	if e.activeAlertStore == nil {
		return
	}
	if err := e.activeAlertStore.DeleteActiveAlert(ruleName, source); err != nil {
		e.logger.Warn("failed to delete persisted active alert",
			"rule", ruleName,
			"source", source,
			"error", err,
		)
	}
}

// Start starts the alerting engine
func (e *Engine) Start(ctx context.Context) error {
	if !e.config.Enabled {
		e.logger.Info("alerting engine disabled")
		return nil
	}

	e.logger.Info("starting alerting engine",
		"rules", len(e.config.Rules),
		"channels", len(e.config.Channels),
	)

	// Start event processor
	go e.processEvents(ctx)

	// Start the delivery worker that drains dispatchCh (E1).
	go e.dispatchWorker(ctx)

	// Start stuck task checker
	go e.checkStuckTasks(ctx)

	e.started.Store(true)

	return nil
}

// dispatchWorker drains queued deliveries sequentially so channel I/O never
// blocks the event loop (E1).
func (e *Engine) dispatchWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.done:
			return
		case job := <-e.dispatchCh:
			e.dispatchAndRecord(ctx, job.rule, job.alert, job.channels)
			e.dispatchWG.Done()
		}
	}
}

// WaitForDispatch blocks until all enqueued alert deliveries have completed.
// Useful for graceful shutdown and for deterministic tests.
func (e *Engine) WaitForDispatch() {
	e.dispatchWG.Wait()
}

// flushForTest blocks until all events currently in the event queue have been
// processed and all in-flight dispatches complete. test-only: requires Start().
func (e *Engine) flushForTest() {
	resp := make(chan struct{})
	e.eventCh <- Event{testFlushResp: resp}
	<-resp
	e.WaitForDispatch()
}

// Stop stops the alerting engine
func (e *Engine) Stop() {
	close(e.done)
}

// ProcessEvent adds an event to the processing queue. High-severity events
// (escalation / OOM / budget / security) go on a dedicated priority queue so a
// flood of ordinary events that fills eventCh cannot drop them (E1).
func (e *Engine) ProcessEvent(event Event) {
	if !e.config.Enabled {
		return
	}

	if isHighPriorityEvent(event.Type) {
		select {
		case e.priorityCh <- event:
		default:
			// The priority queue is independent of eventCh, so this only happens
			// under a sustained critical-event storm. Log loudly and count it.
			e.logger.Error("CRITICAL alert event dropped — priority queue full",
				"type", event.Type,
				"task_id", event.TaskID,
			)
			e.metrics.RecordDropped()
		}
		return
	}

	select {
	case e.eventCh <- event:
	default:
		e.logger.Warn("alert event queue full, dropping event",
			"type", event.Type,
			"task_id", event.TaskID,
		)
		e.metrics.RecordDropped()
	}
}

// isHighPriorityEvent reports whether an event must not be lost under load.
func isHighPriorityEvent(t EventType) bool {
	switch t {
	case EventTypeEscalation, EventTypeOOMKilled, EventTypeBudgetExceeded, EventTypeSecurityEvent, EventTypeConfigError:
		return true
	default:
		return false
	}
}

// processEvents processes incoming events. The priority queue is drained ahead
// of the normal queue so critical alerts are handled first under load.
func (e *Engine) processEvents(ctx context.Context) {
	for {
		// Fast path: always prefer a pending priority event.
		select {
		case <-ctx.Done():
			return
		case <-e.done:
			return
		case event := <-e.priorityCh:
			e.handleEvent(ctx, event)
			continue
		default:
		}

		select {
		case <-ctx.Done():
			return
		case <-e.done:
			return
		case event := <-e.priorityCh:
			e.handleEvent(ctx, event)
		case event := <-e.eventCh:
			e.handleEvent(ctx, event)
		}
	}
}

// handleEvent processes a single event
func (e *Engine) handleEvent(ctx context.Context, event Event) {
	if event.testFlushResp != nil {
		close(event.testFlushResp)
		return
	}
	switch event.Type {
	case EventTypeTaskStarted:
		e.handleTaskStarted(event)
	case EventTypeTaskProgress:
		e.handleTaskProgress(event)
	case EventTypeTaskCompleted:
		e.handleTaskCompleted(ctx, event)
	case EventTypeTaskFailed, EventTypeOOMKilled:
		// GH-2332: OOM kills are a strict subset of failures — route through
		// the same handler so consecutive-failure counters and escalation
		// rules fire, but preserve the distinct type for logging/metadata.
		e.handleTaskFailed(ctx, event)
	case EventTypeCostUpdate:
		e.handleCostUpdate(ctx, event)
	case EventTypeSecurityEvent:
		e.handleSecurityEvent(ctx, event)
	case EventTypeConfigError:
		e.handleConfigError(ctx, event)
	case EventTypeConfigHealthy:
		e.handleConfigHealthy(ctx, event)
	case EventTypeReleaseMissing:
		e.handleReleaseMissing(ctx, event)
	case EventTypeLaneStarvation:
		e.handleLaneStarvation(ctx, event)
	case EventTypeDispatchLoopBreaker:
		e.handleDispatchLoopBreaker(ctx, event)
	case EventTypeBudgetExceeded, EventTypeBudgetWarning:
		e.handleBudgetEvent(ctx, event)
	case EventTypeAutopilotMetrics:
		e.handleAutopilotMetrics(ctx, event)
	case EventTypeEscalation:
		e.handleEscalation(ctx, event)
	case EventTypeEvalRegression:
		e.handleEvalRegression(ctx, event)
	case EventTypeGithubSideEffect:
		e.handleGithubSideEffect(ctx, event)
	case EventTypeIntentJudgeFailureStreak:
		e.handleIntentJudgeFailureStreak(ctx, event)
	case EventTypeDeadManAttempt:
		e.handleDeadManAttempt(event)
	case EventTypeDeadManSuccess:
		e.handleDeadManSuccess(event)
	case EventTypeDeadManFailure:
		e.handleDeadManFailure(event)
	case EventTypeDeadManStreak:
		e.handleDeadManStreak(ctx, event)
	}
}

func (e *Engine) handleTaskStarted(event Event) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.taskLastProgress[event.TaskID] = progressState{
		Progress:  0,
		UpdatedAt: event.Timestamp,
		Phase:     event.Phase,
	}
}

func (e *Engine) handleTaskProgress(event Event) {
	e.mu.Lock()
	defer e.mu.Unlock()

	current, exists := e.taskLastProgress[event.TaskID]
	if !exists || event.Progress > current.Progress || event.Phase != current.Phase {
		e.taskLastProgress[event.TaskID] = progressState{
			Progress:  event.Progress,
			UpdatedAt: event.Timestamp,
			Phase:     event.Phase,
			// Reset per-task alert cooldown when progress advances (GH-2204)
			LastAlertedAt: time.Time{},
		}
	}
}

func (e *Engine) handleTaskCompleted(ctx context.Context, event Event) {
	// Determine source for retry tracking (GH-848)
	source := event.TaskID
	if s, ok := event.Metadata["source"]; ok && s != "" {
		source = s
	}

	e.mu.Lock()
	// Reset consecutive failures on success
	e.consecutiveFailures[event.Project] = 0
	delete(e.taskLastProgress, event.TaskID)
	// Reset per-source retry counter on success (GH-848)
	delete(e.retryTracker, source)
	delete(e.retryLastSeen, source) // TASK-357 (E7): keep TTL map in lockstep
	e.mu.Unlock()
}

func (e *Engine) handleTaskFailed(ctx context.Context, event Event) {
	// Determine source for retry tracking (GH-848)
	// Source can be passed in Metadata["source"] or default to TaskID
	source := event.TaskID
	if s, ok := event.Metadata["source"]; ok && s != "" {
		source = s
	}

	e.mu.Lock()
	delete(e.taskLastProgress, event.TaskID)
	e.consecutiveFailures[event.Project]++
	failCount := e.consecutiveFailures[event.Project]

	// Track per-source retries (GH-848)
	e.retryTracker[source]++
	retryCount := e.retryTracker[source]
	// TASK-357 (E7): stamp last-seen so abandoned sources (failed, escalated, never
	// re-attempted to success) can be evicted on a TTL instead of leaking forever.
	e.retryLastSeen[source] = time.Now()
	e.mu.Unlock()

	// Check task_failed rule
	for _, rule := range e.config.Rules {
		if !rule.Enabled {
			continue
		}

		switch rule.Type {
		case AlertTypeTaskFailed:
			if e.shouldFire(rule) {
				alert := e.createAlert(rule, event, fmt.Sprintf("Task %s failed: %s", event.TaskID, event.Error))
				e.fireAlert(ctx, rule, alert)
			}

		case AlertTypeConsecutiveFails:
			if failCount >= rule.Condition.ConsecutiveFailures && e.shouldFire(rule) {
				alert := e.createAlert(rule, event,
					fmt.Sprintf("%d consecutive task failures in project %s", failCount, event.Project))
				e.fireAlert(ctx, rule, alert)
			}

		case AlertTypeEscalation:
			// Escalate to PagerDuty after N consecutive failures for the same source (GH-848)
			threshold := rule.Condition.EscalationRetries
			if threshold == 0 {
				threshold = 3 // Default
			}
			if retryCount >= threshold && e.shouldFire(rule) {
				alert := e.createEscalationAlert(rule, event, source, retryCount)
				e.fireAlert(ctx, rule, alert)
			}
		}
	}
}

func (e *Engine) handleCostUpdate(ctx context.Context, event Event) {
	dailySpend := 0.0
	if v, ok := event.Metadata["daily_spend"]; ok {
		_, _ = fmt.Sscanf(v, "%f", &dailySpend)
	}

	for _, rule := range e.config.Rules {
		if !rule.Enabled {
			continue
		}

		switch rule.Type {
		case AlertTypeDailySpend:
			if dailySpend > rule.Condition.DailySpendThreshold && e.shouldFire(rule) {
				alert := e.createAlert(rule, event,
					fmt.Sprintf("Daily spend $%.2f exceeds threshold $%.2f",
						dailySpend, rule.Condition.DailySpendThreshold))
				e.fireAlert(ctx, rule, alert)
			}

		case AlertTypeBudgetDepleted:
			totalSpend := 0.0
			if v, ok := event.Metadata["total_spend"]; ok {
				_, _ = fmt.Sscanf(v, "%f", &totalSpend)
			}
			if totalSpend > rule.Condition.BudgetLimit && e.shouldFire(rule) {
				alert := e.createAlert(rule, event,
					fmt.Sprintf("Budget limit $%.2f exceeded (current: $%.2f)",
						rule.Condition.BudgetLimit, totalSpend))
				e.fireAlert(ctx, rule, alert)
			}
		}
	}
}

func (e *Engine) handleBudgetEvent(ctx context.Context, event Event) {
	// Route budget events through cost update handler so existing
	// AlertTypeDailySpend / AlertTypeBudgetDepleted rules fire
	e.handleCostUpdate(ctx, event)
}

func (e *Engine) handleSecurityEvent(ctx context.Context, event Event) {
	for _, rule := range e.config.Rules {
		if !rule.Enabled {
			continue
		}

		switch rule.Type {
		case AlertTypeUnauthorizedAccess:
			if e.shouldFire(rule) {
				alert := e.createAlert(rule, event, "Unauthorized access attempt detected")
				e.fireAlert(ctx, rule, alert)
			}
		case AlertTypeSensitiveFile:
			if e.shouldFire(rule) {
				filePath := event.Metadata["file_path"]
				alert := e.createAlert(rule, event,
					fmt.Sprintf("Sensitive file modified: %s", filePath))
				e.fireAlert(ctx, rule, alert)
			}
		}
	}
}

type activeAlert struct {
	rule     AlertRule
	alert    *Alert
	channels []string
}

func activeAlertKey(ruleName, source string) string {
	return ruleName + "|" + source
}

// handleConfigError fires AlertTypeServiceUnhealthy rules when a resolved
// credential fails validation (GH-3718), e.g. a dead GitHub token detected at
// startup. Message comes from event.Error, set by the caller.
func (e *Engine) handleConfigError(ctx context.Context, event Event) {
	for _, rule := range e.config.Rules {
		if !rule.Enabled {
			continue
		}
		if rule.Type == AlertTypeServiceUnhealthy && e.shouldFire(rule) {
			alert := e.createAlert(rule, event, event.Error)
			e.markActive(rule, alert)
			e.fireAlert(ctx, rule, alert)
		}
	}
}

func (e *Engine) markActive(rule AlertRule, alert *Alert) {
	if !e.config.Defaults.ResolveNotificationsEnabled() || alert.Source == "" {
		return
	}
	channels := e.resolveChannels(rule, alert)

	active := &activeAlert{
		rule:     rule,
		alert:    alert,
		channels: channels,
	}

	e.mu.Lock()
	e.activeAlerts[activeAlertKey(rule.Name, alert.Source)] = active
	e.mu.Unlock()

	// GH-4890: persist off the alerting path — the alert has already fired.
	e.persistActiveAlert(active)
}

func (e *Engine) handleConfigHealthy(ctx context.Context, event Event) {
	if !e.config.Defaults.ResolveNotificationsEnabled() || event.Source == "" {
		return
	}
	for _, rule := range e.config.Rules {
		if !rule.Enabled || rule.Type != AlertTypeServiceUnhealthy {
			continue
		}
		key := activeAlertKey(rule.Name, event.Source)

		e.mu.Lock()
		active, ok := e.activeAlerts[key]
		if ok {
			delete(e.activeAlerts, key)
		}
		e.mu.Unlock()

		if !ok {
			continue
		}
		// GH-4890: delete off the alerting path before dispatching, so the
		// persisted row can never outlive the in-memory state it mirrors.
		e.deletePersistedActiveAlert(rule.Name, event.Source)
		e.dispatchResolution(ctx, active)
	}
}

func (e *Engine) dispatchResolution(ctx context.Context, active *activeAlert) {
	now := time.Now()
	active.alert.ResolvedAt = &now

	resolution := &Alert{
		ID:          fmt.Sprintf("%s-resolved", active.alert.ID),
		Type:        active.alert.Type,
		Severity:    SeverityInfo,
		Title:       active.alert.Title,
		Message:     active.alert.Message,
		Source:      active.alert.Source,
		ProjectPath: active.alert.ProjectPath,
		Metadata:    active.alert.Metadata,
		CreatedAt:   active.alert.CreatedAt,
		ResolvedAt:  &now,
	}

	e.metrics.RecordFired(active.rule.Name, string(resolution.Severity))
	if e.dispatcher == nil {
		e.logger.Warn("no dispatcher configured, resolution not sent",
			"rule", active.rule.Name,
			"alert_id", resolution.ID,
		)
		return
	}
	e.enqueueDispatch(ctx, active.rule, resolution, active.channels)
}

// handleReleaseMissing fires AlertTypeReleaseMissing rules when a merged PR
// did not produce its expected release tag (GH-3952). Metadata carries repo,
// tag, and pr so operators can find the stalled release from the alert alone.
func (e *Engine) handleReleaseMissing(ctx context.Context, event Event) {
	for _, rule := range e.config.Rules {
		if !rule.Enabled {
			continue
		}
		if rule.Type == AlertTypeReleaseMissing && e.shouldFire(rule) {
			message := fmt.Sprintf("Release missing for %s: expected tag %s after PR #%s was merged",
				event.Metadata["repo"], event.Metadata["tag"], event.Metadata["pr"])
			alert := e.createAlert(rule, event, message)
			e.fireAlert(ctx, rule, alert)
		}
	}
}

// handleGithubSideEffect fires AlertTypeGithubSideEffect rules when the
// executor's post-run audit (GH-4670) finds a sibling GitHub issue closed or
// reopened during a session's run window — the GH-4649 incident class.
// Detection-only, same severity channel as task_failed; no Condition-based
// counting, mirroring handleReleaseMissing — the executor-side audit already
// did all the filtering (own-issue exclusion, window bounds) before emitting.
func (e *Engine) handleGithubSideEffect(ctx context.Context, event Event) {
	for _, rule := range e.config.Rules {
		if !rule.Enabled {
			continue
		}
		if rule.Type == AlertTypeGithubSideEffect && e.shouldFire(rule) {
			message := fmt.Sprintf("Session dispatched for %s#%s mutated sibling issue #%s (%s)",
				event.Metadata["repo"], event.Metadata["task_issue"], event.Metadata["issue"], event.Metadata["state"])
			alert := e.createAlert(rule, event, message)
			e.fireAlert(ctx, rule, alert)
		}
	}
}

// handleLaneStarvation fires AlertTypeLaneStarvation rules when a project
// lane's poll_cycles_starved streak (GH-4454, emitted by
// autopilot.Controller.reconcileLaneStarvation every poll cycle the lane
// looks starved) reaches RuleCondition.LaneStarvationPollCycles. The emitting
// side does no threshold filtering of its own — mirroring
// handleAutopilotMetrics's ownership of FailedQueueThreshold/PRStuckTimeout —
// so a rule override here takes effect without any autopilot-side change.
func (e *Engine) handleLaneStarvation(ctx context.Context, event Event) {
	streak := 0
	if v, ok := event.Metadata["poll_cycles_starved"]; ok {
		_, _ = fmt.Sscanf(v, "%d", &streak)
	}
	openIssues := event.Metadata["open_issue_count"]
	repo := event.Metadata["repo"]

	for _, rule := range e.config.Rules {
		if !rule.Enabled || rule.Type != AlertTypeLaneStarvation {
			continue
		}

		threshold := rule.Condition.LaneStarvationPollCycles
		if threshold <= 0 {
			threshold = 3
		}

		if streak >= threshold && e.shouldFire(rule) {
			alert := e.createAlert(rule, event,
				fmt.Sprintf("Lane %s has %s open pilot-labeled issue(s) but nothing queued/running for %d consecutive poll cycles",
					repo, openIssues, streak))
			e.fireAlert(ctx, rule, alert)
		}
	}
}

// handleDispatchLoopBreaker fires AlertTypeDispatchLoopBreaker rules when a
// task's consecutive dispatch-and-reject count reaches the caller's
// threshold (GH-4469). The caller (handleIssueGeneric) already computed and
// gated on the exact threshold before emitting this event, so — like
// handleReleaseMissing — no Condition-based counting happens here.
func (e *Engine) handleDispatchLoopBreaker(ctx context.Context, event Event) {
	matched := false
	for _, rule := range e.config.Rules {
		if rule.Type != AlertTypeDispatchLoopBreaker {
			continue
		}
		matched = true
		if rule.Enabled && e.shouldFire(rule) {
			message := fmt.Sprintf("Task %s has been dispatched-and-rejected %s consecutive times without completing — stopping until operator action or backoff expiry",
				event.TaskID, event.Metadata["consecutive_drops"])
			alert := e.createAlert(rule, event, message)
			e.fireAlert(ctx, rule, alert)
		}
	}
	if !matched {
		e.logger.Warn("dispatch loop breaker threshold event has no matching rule — alert dropped silently",
			slog.String("task_id", event.TaskID),
			slog.String("consecutive_drops", event.Metadata["consecutive_drops"]))
	}
}

// handleIntentJudgeFailureStreak fires AlertTypeIntentJudgeFailureStreak
// rules when the intent judge's consecutive fail-open streak reaches the
// caller's threshold (GH-4669). The caller (sdkPreFlightJudge.JudgeIssue,
// cmd/pilot/poller_github.go) already computed and gated on the exact
// threshold before emitting this event — mirroring handleDispatchLoopBreaker
// — so no Condition-based counting happens here.
func (e *Engine) handleIntentJudgeFailureStreak(ctx context.Context, event Event) {
	matched := false
	for _, rule := range e.config.Rules {
		if rule.Type != AlertTypeIntentJudgeFailureStreak {
			continue
		}
		matched = true
		if rule.Enabled && e.shouldFire(rule) {
			message := fmt.Sprintf("Pre-flight intent judge for %s has failed open %s consecutive times — judge is not producing verdicts",
				event.Metadata["repo"], event.Metadata["consecutive_failures"])
			alert := e.createAlert(rule, event, message)
			e.fireAlert(ctx, rule, alert)
		}
	}
	if !matched {
		e.logger.Warn("intent judge failure streak event has no matching rule — alert dropped silently",
			slog.String("repo", event.Metadata["repo"]),
			slog.String("consecutive_failures", event.Metadata["consecutive_failures"]))
	}
}

// checkStuckTasks periodically checks for stuck tasks
func (e *Engine) checkStuckTasks(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.done:
			return
		case <-ticker.C:
			e.evaluateStuckTasks(ctx)
		}
	}
}

func (e *Engine) evaluateStuckTasks(ctx context.Context) {
	now := time.Now()

	// Collect orphan IDs under read lock, then evict under write lock (GH-2204)
	e.mu.RLock()
	tasks := make(map[string]progressState)
	var orphans []string
	for k, v := range e.taskLastProgress {
		tasks[k] = v
	}
	e.mu.RUnlock()

	for _, rule := range e.config.Rules {
		if !rule.Enabled || rule.Type != AlertTypeTaskStuck {
			continue
		}

		threshold := rule.Condition.ProgressUnchangedFor
		if threshold == 0 {
			threshold = 10 * time.Minute
		}

		cooldown := rule.Cooldown
		orphanThreshold := 4 * threshold // Evict entries stuck for 4× the threshold (GH-2204)
		// GH-4092: a 4×threshold orphan window (40m at the 10m default) evicted
		// entries for tasks that were still demonstrably alive — a single
		// long-running Claude turn (self-review/intent-judge veto retry loops)
		// can legitimately produce no progress-milestone update for tens of
		// minutes, well inside the executor's own Complex-task timeout (60m
		// default) and its 2× watchdog kill ceiling (120m, runner.go
		// watchdogTimeout). Floor the orphan window at that ceiling so eviction
		// never preempts a task the runner itself hasn't given up on yet.
		if orphanThreshold < minOrphanEvictionThreshold {
			orphanThreshold = minOrphanEvictionThreshold
		}

		for taskID, state := range tasks {
			stuckDuration := now.Sub(state.UpdatedAt)

			// Orphan eviction: remove entries that have been stuck far too long (GH-2204)
			if stuckDuration > orphanThreshold {
				orphans = append(orphans, taskID)
				e.logger.Warn("evicting orphaned stuck-task entry",
					"task_id", taskID,
					"stuck_for", stuckDuration.Round(time.Minute),
					"orphan_threshold", orphanThreshold,
				)
				e.stallOrphanedExecution(taskID, stuckDuration)
				continue
			}

			if stuckDuration <= threshold {
				continue
			}

			// GH-4416: a task still sitting in the dispatcher's "Queued" phase
			// hasn't been picked up by a project worker yet — queue-wait behind
			// a busy single-lane worker is normal operation (deep queues are
			// routine now that the box runs multi-project with lane
			// concurrency 1), not stuckness. Mirrors GH-4033's staleReference
			// fix in memory/store.go (measure staleness from started_at, not
			// queue/created time): only arm task_stuck once the task has
			// actually reached a running/progress stage. Without this, every
			// queued task past the threshold fires its own warning the moment
			// the sweep ticks, so a deep queue backlog spams one alert per
			// waiting task instead of surfacing the (separate) queue-depth
			// concern.
			if state.Phase == taskStuckQueuedPhase {
				continue
			}

			// Per-task cooldown: skip if already alerted recently for THIS task (GH-2204)
			if !state.LastAlertedAt.IsZero() && cooldown > 0 && now.Sub(state.LastAlertedAt) < cooldown {
				continue
			}

			event := Event{
				Type:      EventTypeTaskProgress,
				TaskID:    taskID,
				Phase:     state.Phase,
				Progress:  state.Progress,
				Timestamp: now,
			}
			alert := e.createAlert(rule, event,
				fmt.Sprintf("Task %s stuck at %d%% (%s) for %v",
					taskID, state.Progress, state.Phase, stuckDuration.Round(time.Minute)))
			e.fireAlert(ctx, rule, alert)

			// Record per-task alert time (GH-2204)
			e.mu.Lock()
			if s, ok := e.taskLastProgress[taskID]; ok {
				s.LastAlertedAt = now
				e.taskLastProgress[taskID] = s
			}
			e.mu.Unlock()
		}
	}

	// Evict orphans
	if len(orphans) > 0 {
		e.mu.Lock()
		for _, id := range orphans {
			delete(e.taskLastProgress, id)
		}
		e.mu.Unlock()
	}

	// TASK-357 (E7): evict stale retryTracker entries. A source that fails, is
	// escalated, and is then abandoned (never re-attempted to success) never hits
	// the delete in handleTaskCompleted, so without a TTL its counter leaks for the
	// daemon lifetime. Mirror the GH-2204 orphan-eviction for taskLastProgress.
	e.evictStaleRetryTrackers(now)
}

// stallOrphanedExecution transitions taskID's still-non-terminal execution
// row to "stalled" via the ExecutionLifecycle chokepoint when the stuck-task
// evictor drops its in-memory tracker entry (GH-4562). Without this, evicting
// the tracker entry only removes the alerts engine's own bookkeeping — the
// underlying execution row (and its claim on (task_id, project_path,
// generation), per TASK-407) is left running/queued forever, since nothing
// else observes the in-memory eviction. That mirrors the claim-release hazard
// GH-4561/#4563 fixed for aborted epic parents' orphaned children: "stalled"
// is itself a terminal status (dispatcher.go's terminalExecutionStatuses), so
// this transition IS the claim release — the next dispatch pass grants a
// fresh generation with no separate release call.
//
// e.lifecycle is nil for every call site that doesn't pass
// WithExecutionLifecycle (short-lived CLI callers that never wire a store),
// and LatestExecution returns (nil, nil) on a nil store or a lookup miss —
// both cases are a silent no-op, matching epic.go's sweepStalledEpicChildren.
//
// Guarding on IsTerminalStatus before calling Finish makes a second eviction
// sweep over an already-terminal row an explicit no-op rather than leaning
// solely on Finish's CAS-guarded idempotency (GH-4423's
// UpdateExecutionStatusIfNotTerminal) — belt-and-suspenders, since either one
// alone is sufficient to prevent a double-Finish error.
func (e *Engine) stallOrphanedExecution(taskID string, stuckFor time.Duration) {
	if e.lifecycle == nil {
		return
	}
	exec, err := e.lifecycle.LatestExecution(taskID, "")
	if err != nil || exec == nil {
		return
	}
	if executor.IsTerminalStatus(exec.Status) {
		return
	}

	reason := fmt.Sprintf("orphan eviction after %s stuck", stuckFor.Round(time.Minute))
	if _, finishErr := e.lifecycle.Finish(exec.ID, nil, errors.New(reason), 0, executor.ExecStatusStalled); finishErr != nil {
		e.logger.Warn("stallOrphanedExecution: failed to stall orphaned execution",
			"task_id", taskID,
			"execution_id", exec.ID,
			"error", finishErr,
		)
		return
	}
	e.logger.Warn("stallOrphanedExecution: stalled orphaned execution after stuck-task eviction",
		"task_id", taskID,
		"execution_id", exec.ID,
		"prior_status", exec.Status,
		"stuck_for", stuckFor.Round(time.Minute),
	)
}

// retryTrackerTTL bounds how long an idle per-source retry counter is retained.
// A source with no failure for this long is treated as abandoned/resolved and
// its retryTracker entry is evicted to keep the map bounded (TASK-357 E7).
const retryTrackerTTL = 24 * time.Hour

func (e *Engine) evictStaleRetryTrackers(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for source, lastSeen := range e.retryLastSeen {
		if now.Sub(lastSeen) <= retryTrackerTTL {
			continue
		}
		delete(e.retryTracker, source)
		delete(e.retryLastSeen, source)
		e.logger.Warn("evicting stale retry-tracker entry",
			"source", source,
			"idle_for", now.Sub(lastSeen).Round(time.Minute),
			"ttl", retryTrackerTTL,
		)
	}
}

// shouldFire checks if a rule should fire based on cooldown
func (e *Engine) shouldFire(rule AlertRule) bool {
	if rule.Cooldown == 0 {
		return true
	}

	e.mu.RLock()
	lastFired, exists := e.lastAlertTimes[rule.Name]
	e.mu.RUnlock()

	if !exists {
		return true
	}

	return time.Since(lastFired) >= rule.Cooldown
}

// createAlert creates an alert from a rule and event
func (e *Engine) createAlert(rule AlertRule, event Event, message string) *Alert {
	source := event.Source
	if source == "" && event.TaskID != "" {
		source = fmt.Sprintf("task:%s", event.TaskID)
	}

	return &Alert{
		ID:          uuid.New().String(),
		Type:        rule.Type,
		Severity:    rule.Severity,
		Title:       rule.Description,
		Message:     message,
		Source:      source,
		ProjectPath: event.Project,
		Metadata:    event.Metadata,
		CreatedAt:   time.Now(),
	}
}

// createEscalationAlert creates an escalation alert for PagerDuty incident creation (GH-848)
func (e *Engine) createEscalationAlert(rule AlertRule, event Event, source string, retryCount int) *Alert {
	metadata := make(map[string]string)
	for k, v := range event.Metadata {
		metadata[k] = v
	}
	metadata["retry_count"] = fmt.Sprintf("%d", retryCount)
	metadata["escalation_source"] = source

	return &Alert{
		ID:          uuid.New().String(),
		Type:        AlertTypeEscalation,
		Severity:    SeverityCritical,
		Title:       "Escalation: Repeated failures require human intervention",
		Message:     fmt.Sprintf("Source %s has failed %d consecutive times. Last error: %s", source, retryCount, event.Error),
		Source:      source,
		ProjectPath: event.Project,
		Metadata:    metadata,
		CreatedAt:   time.Now(),
	}
}

// fireAlert sends an alert through configured channels. Delivery is handed to a
// bounded background goroutine so a slow/hung channel cannot block the event
// loop (E1); identical alerts are suppressed within a TTL window (E5).
func (e *Engine) fireAlert(ctx context.Context, rule AlertRule, alert *Alert) {
	now := time.Now()

	e.mu.Lock()
	// E5: suppress identical alerts ({rule|source|message}) within the TTL window.
	if e.config.Defaults.SuppressDuplicates {
		key := dedupeKey(rule.Name, alert.Source, alert.Message)
		if last, ok := e.recentAlerts[key]; ok && now.Sub(last) < duplicateSuppressTTL {
			e.mu.Unlock()
			e.metrics.RecordDropped()
			e.logger.Debug("duplicate alert suppressed",
				"rule", rule.Name,
				"source", alert.Source,
				"alert_id", alert.ID,
			)
			return
		}
		e.recentAlerts[key] = now
		e.pruneRecentAlertsLocked(now)
	}
	e.lastAlertTimes[rule.Name] = now
	e.mu.Unlock()

	e.metrics.RecordFired(rule.Name, string(alert.Severity))

	if e.dispatcher == nil {
		e.logger.Warn("no dispatcher configured, alert not sent",
			"rule", rule.Name,
			"alert_id", alert.ID,
		)
		return
	}

	e.enqueueDispatch(ctx, rule, alert, e.resolveChannels(rule, alert))
}

func (e *Engine) resolveChannels(rule AlertRule, alert *Alert) []string {
	if len(rule.Channels) > 0 {
		return rule.Channels
	}
	var channels []string
	for _, ch := range e.config.Channels {
		if ch.Enabled && e.channelAcceptsSeverity(ch, alert.Severity) {
			channels = append(channels, ch.Name)
		}
	}
	return channels
}

// E1: once running, hand delivery to the background worker so a slow/hung
// channel can never block the event loop. Before Start() (direct callers /
// tests) deliver inline so the path stays synchronous.
func (e *Engine) enqueueDispatch(ctx context.Context, rule AlertRule, alert *Alert, channels []string) {
	if !e.started.Load() {
		e.dispatchAndRecord(ctx, rule, alert, channels)
		return
	}
	e.dispatchWG.Add(1)
	select {
	case e.dispatchCh <- dispatchJob{rule: rule, alert: alert, channels: channels}:
	default:
		e.dispatchWG.Done()
		e.metrics.RecordDropped()
		e.logger.Warn("alert dispatch backlog full, dropping delivery",
			"rule", rule.Name,
			"alert_id", alert.ID,
			"severity", alert.Severity,
		)
	}
}

// dispatchAndRecord delivers an alert to its channels and records delivery
// history. Runs in its own goroutine, bounded by dispatchSem (E1).
func (e *Engine) dispatchAndRecord(ctx context.Context, rule AlertRule, alert *Alert, channels []string) {
	results := e.dispatcher.Dispatch(ctx, alert, channels)

	// Track delivery history
	deliveredTo := make([]string, 0)
	for _, r := range results {
		if r.Success {
			deliveredTo = append(deliveredTo, r.ChannelName)
		} else {
			e.logger.Error("failed to deliver alert",
				"channel", r.ChannelName,
				"error", r.Error,
			)
		}
	}

	e.mu.Lock()
	e.alertHistory = append(e.alertHistory, AlertHistory{
		AlertID:     alert.ID,
		RuleName:    rule.Name,
		Source:      alert.Source,
		FiredAt:     alert.CreatedAt,
		DeliveredTo: deliveredTo,
	})
	// Keep only last 1000 alerts in history
	if len(e.alertHistory) > 1000 {
		e.alertHistory = e.alertHistory[len(e.alertHistory)-1000:]
	}
	e.mu.Unlock()

	e.logger.Info("alert fired",
		"rule", rule.Name,
		"alert_id", alert.ID,
		"severity", alert.Severity,
		"delivered_to", deliveredTo,
	)
}

// dedupeKey builds the suppression key for an alert (E5).
func dedupeKey(rule, source, message string) string {
	return rule + "|" + source + "|" + message
}

// pruneRecentAlertsLocked removes dedupe entries older than the TTL.
// The caller must hold e.mu.
func (e *Engine) pruneRecentAlertsLocked(now time.Time) {
	for k, t := range e.recentAlerts {
		if now.Sub(t) >= duplicateSuppressTTL {
			delete(e.recentAlerts, k)
		}
	}
}

func (e *Engine) channelAcceptsSeverity(ch ChannelConfig, severity Severity) bool {
	if len(ch.Severities) == 0 {
		return true // Accept all severities by default
	}
	for _, s := range ch.Severities {
		if s == severity {
			return true
		}
	}
	return false
}

// GetAlertHistory returns recent alert history
func (e *Engine) GetAlertHistory(limit int) []AlertHistory {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if limit <= 0 || limit > len(e.alertHistory) {
		limit = len(e.alertHistory)
	}

	// Return most recent alerts first
	result := make([]AlertHistory, limit)
	for i := 0; i < limit; i++ {
		result[i] = e.alertHistory[len(e.alertHistory)-1-i]
	}
	return result
}

// GetConfig returns the current alert configuration
func (e *Engine) GetConfig() *AlertConfig {
	return e.config
}

// UpdateConfig updates the alert configuration
func (e *Engine) UpdateConfig(config *AlertConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.config = config
}

// handleAutopilotMetrics evaluates autopilot health metrics against alert rules.
// Metadata keys: "failed_queue_depth", "circuit_breaker_trips", "api_error_rate",
// "pr_stuck_count", "pr_max_wait_minutes".
func (e *Engine) handleAutopilotMetrics(ctx context.Context, event Event) {
	failedQueueDepth := 0
	if v, ok := event.Metadata["failed_queue_depth"]; ok {
		_, _ = fmt.Sscanf(v, "%d", &failedQueueDepth)
	}

	cbTrips := 0
	if v, ok := event.Metadata["circuit_breaker_trips"]; ok {
		_, _ = fmt.Sscanf(v, "%d", &cbTrips)
	}

	apiErrorRate := 0.0
	if v, ok := event.Metadata["api_error_rate"]; ok {
		_, _ = fmt.Sscanf(v, "%f", &apiErrorRate)
	}

	prStuckCount := 0
	if v, ok := event.Metadata["pr_stuck_count"]; ok {
		_, _ = fmt.Sscanf(v, "%d", &prStuckCount)
	}

	prMaxWaitMin := 0.0
	if v, ok := event.Metadata["pr_max_wait_minutes"]; ok {
		_, _ = fmt.Sscanf(v, "%f", &prMaxWaitMin)
	}

	for _, rule := range e.config.Rules {
		if !rule.Enabled {
			continue
		}

		switch rule.Type {
		case AlertTypeFailedQueueHigh:
			threshold := rule.Condition.FailedQueueThreshold
			if threshold > 0 && failedQueueDepth >= threshold && e.shouldFire(rule) {
				alert := e.createAlert(rule, event,
					fmt.Sprintf("Failed issue queue depth %d exceeds threshold %d",
						failedQueueDepth, threshold))
				e.fireAlert(ctx, rule, alert)
			}

		case AlertTypeCircuitBreakerTrip:
			if cbTrips > 0 && e.shouldFire(rule) {
				alert := e.createAlert(rule, event,
					fmt.Sprintf("Autopilot circuit breaker tripped (%d trips)", cbTrips))
				e.fireAlert(ctx, rule, alert)
			}

		case AlertTypeAPIErrorRateHigh:
			threshold := rule.Condition.APIErrorRatePerMin
			if threshold > 0 && apiErrorRate >= threshold && e.shouldFire(rule) {
				alert := e.createAlert(rule, event,
					fmt.Sprintf("API error rate %.1f/min exceeds threshold %.1f/min",
						apiErrorRate, threshold))
				e.fireAlert(ctx, rule, alert)
			}

		case AlertTypePRStuckWaitingCI:
			timeout := rule.Condition.PRStuckTimeout
			if timeout > 0 && prStuckCount > 0 && prMaxWaitMin >= timeout.Minutes() && e.shouldFire(rule) {
				alert := e.createAlert(rule, event,
					fmt.Sprintf("%d PR(s) stuck in waiting_ci for %.0f+ minutes",
						prStuckCount, prMaxWaitMin))
				e.fireAlert(ctx, rule, alert)
			}

		// GH-849: Deadlock detection
		case AlertTypeDeadlock:
			timeout := rule.Condition.DeadlockTimeout
			if timeout == 0 {
				timeout = 1 * time.Hour // Default to 1 hour
			}

			noProgressMin := 0.0
			if v, ok := event.Metadata["no_progress_minutes"]; ok {
				_, _ = fmt.Sscanf(v, "%f", &noProgressMin)
			}

			deadlockAlertSent := false
			if v, ok := event.Metadata["deadlock_alert_sent"]; ok {
				deadlockAlertSent = v == "true"
			}

			// Only fire if:
			// 1. No progress for longer than timeout
			// 2. We haven't already sent an alert for this stall
			// 3. Rule cooldown allows firing
			if noProgressMin >= timeout.Minutes() && !deadlockAlertSent && e.shouldFire(rule) {
				lastState := event.Metadata["last_known_state"]
				lastPR := event.Metadata["last_known_pr"]

				message := fmt.Sprintf("No state transitions in %.0f minutes.", noProgressMin)
				if lastState != "" && lastPR != "0" {
					message = fmt.Sprintf("No state transitions in %.0f minutes. Last: %s for PR #%s",
						noProgressMin, lastState, lastPR)
				}

				alert := e.createAlert(rule, event, message)
				e.fireAlert(ctx, rule, alert)
			}
		}
	}
}

// handleEvalRegression processes eval regression events (GH-2065).
// Metadata keys: baseline_pass1, current_pass1, delta, regressed_count, recommendation.
func (e *Engine) handleEvalRegression(ctx context.Context, event Event) {
	for _, rule := range e.config.Rules {
		if !rule.Enabled || rule.Type != AlertTypeEvalRegression {
			continue
		}

		if !e.shouldFire(rule) {
			continue
		}

		baselinePass1 := event.Metadata["baseline_pass1"]
		currentPass1 := event.Metadata["current_pass1"]
		delta := event.Metadata["delta"]
		regressedCount := event.Metadata["regressed_count"]
		recommendation := event.Metadata["recommendation"]

		message := fmt.Sprintf(
			"Eval regression detected: pass@1 dropped from %s to %s (delta: %s). %s eval(s) regressed.",
			baselinePass1, currentPass1, delta, regressedCount,
		)
		if recommendation != "" {
			message += " Recommendation: " + recommendation
		}

		alert := e.createAlert(rule, event, message)

		// Escalate to critical if delta exceeds 2× the threshold
		deltaVal := 0.0
		if _, err := fmt.Sscanf(delta, "%f", &deltaVal); err == nil {
			threshold := rule.Condition.UsageSpikePercent // reuse as regression threshold
			if threshold > 0 && deltaVal > 2*threshold {
				alert.Severity = SeverityCritical
			}
		}

		e.fireAlert(ctx, rule, alert)
	}
}

// handleEscalation processes escalation events (GH-885).
// These are critical alerts that should route to PagerDuty.
//
// EventTypeEscalation is shared by every escalation emitter in the codebase,
// but only the circuit-breaker trip emitter (metrics_alerter.go
// emitEscalationAlert) populates the trips_in_hour/escalation_threshold/
// last_pr/last_reason metadata this handler used to render exclusively. The
// autopilot emitters (alertStackedSupersetOnce, alertBaseMismatchOnce,
// alertBranchDeleteHeldOnce, alertUnresolvableBaseOnce) put their real
// diagnostic text in event.Error instead — this handler never read it, so
// every non-circuit-breaker escalation rendered a blank template (GH-5065,
// incident a695c90e: alertBranchDeleteHeldOnce fired correctly but Slack
// delivered "Circuit breaker escalation:  trips in 1 hour (threshold: ).
// Last: PR # -"). Fall back to event.Error whenever the circuit-breaker
// metadata is absent so those emitters' messages actually reach the alert.
func (e *Engine) handleEscalation(ctx context.Context, event Event) {
	for _, rule := range e.config.Rules {
		if !rule.Enabled || rule.Type != AlertTypeEscalation {
			continue
		}

		// NOTE (GH-5065 item 3): this 1h cooldown is keyed globally by
		// rule.Name ("escalation") in e.lastAlertTimes (shouldFire below,
		// engine.go ~1062), so a circuit-breaker trip and an unrelated
		// autopilot escalation (stacked-superset, base-mismatch,
		// branch-delete-held, unresolvable-base) share one bucket — whichever
		// fires first can suppress the other for up to an hour even though
		// they're unrelated conditions. Left as-is per GH-5065 (flag-only;
		// splitting the cooldown key per-source is a follow-up if this proves
		// to actually suppress a real escalation).
		if !e.shouldFire(rule) {
			continue
		}

		tripsInHour := event.Metadata["trips_in_hour"]
		threshold := event.Metadata["escalation_threshold"]
		lastPR := event.Metadata["last_pr"]
		lastReason := event.Metadata["last_reason"]

		var message string
		switch {
		case tripsInHour != "" || threshold != "":
			// Circuit-breaker path — keep byte-identical to the pre-GH-5065
			// template when this metadata is present.
			message = fmt.Sprintf(
				"Circuit breaker escalation: %s trips in 1 hour (threshold: %s). Last: PR #%s - %s",
				tripsInHour, threshold, lastPR, lastReason,
			)
		case event.Error != "":
			message = event.Error
		default:
			message = fmt.Sprintf("Escalation event received for %s with no message content", event.TaskTitle)
		}

		alert := e.createAlert(rule, event, message)
		// Force critical severity for escalations
		alert.Severity = SeverityCritical
		e.fireAlert(ctx, rule, alert)
	}
}

// AlertSnapshot returns a point-in-time copy of alert metrics including the current
// event queue depth. When the same AlertMetrics instance is shared with the Dispatcher
// via WithAlertMetrics + WithDispatcherMetrics, the snapshot also includes delivery counters.
func (e *Engine) AlertSnapshot() AlertMetricsSnapshot {
	snap := e.metrics.Snapshot()
	snap.QueueDepth = len(e.eventCh)
	return snap
}
