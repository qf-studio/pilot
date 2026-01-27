package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
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

	// Channels for events
	eventCh chan Event
	done    chan struct{}
}

type progressState struct {
	Progress  int
	UpdatedAt time.Time
	Phase     string
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
	Metadata  map[string]string
	Timestamp time.Time
}

// EventType categorizes incoming events
type EventType string

const (
	EventTypeTaskStarted   EventType = "task_started"
	EventTypeTaskProgress  EventType = "task_progress"
	EventTypeTaskCompleted EventType = "task_completed"
	EventTypeTaskFailed    EventType = "task_failed"
	EventTypeCostUpdate    EventType = "cost_update"
	EventTypeSecurityEvent EventType = "security_event"
)

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

// NewEngine creates a new alerting engine
func NewEngine(config *AlertConfig, opts ...EngineOption) *Engine {
	e := &Engine{
		config:              config,
		logger:              slog.Default(),
		lastAlertTimes:      make(map[string]time.Time),
		consecutiveFailures: make(map[string]int),
		taskLastProgress:    make(map[string]progressState),
		alertHistory:        make([]AlertHistory, 0),
		eventCh:             make(chan Event, 100),
		done:                make(chan struct{}),
	}

	for _, opt := range opts {
		opt(e)
	}

	return e
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

	// Start stuck task checker
	go e.checkStuckTasks(ctx)

	return nil
}

// Stop stops the alerting engine
func (e *Engine) Stop() {
	close(e.done)
}

// ProcessEvent adds an event to the processing queue
func (e *Engine) ProcessEvent(event Event) {
	if !e.config.Enabled {
		return
	}

	select {
	case e.eventCh <- event:
	default:
		e.logger.Warn("alert event queue full, dropping event",
			"type", event.Type,
			"task_id", event.TaskID,
		)
	}
}

// processEvents processes incoming events
func (e *Engine) processEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.done:
			return
		case event := <-e.eventCh:
			e.handleEvent(ctx, event)
		}
	}
}

// handleEvent processes a single event
func (e *Engine) handleEvent(ctx context.Context, event Event) {
	switch event.Type {
	case EventTypeTaskStarted:
		e.handleTaskStarted(event)
	case EventTypeTaskProgress:
		e.handleTaskProgress(event)
	case EventTypeTaskCompleted:
		e.handleTaskCompleted(ctx, event)
	case EventTypeTaskFailed:
		e.handleTaskFailed(ctx, event)
	case EventTypeCostUpdate:
		e.handleCostUpdate(ctx, event)
	case EventTypeSecurityEvent:
		e.handleSecurityEvent(ctx, event)
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
		}
	}
}

func (e *Engine) handleTaskCompleted(ctx context.Context, event Event) {
	e.mu.Lock()
	// Reset consecutive failures on success
	e.consecutiveFailures[event.Project] = 0
	delete(e.taskLastProgress, event.TaskID)
	e.mu.Unlock()
}

func (e *Engine) handleTaskFailed(ctx context.Context, event Event) {
	e.mu.Lock()
	delete(e.taskLastProgress, event.TaskID)
	e.consecutiveFailures[event.Project]++
	failCount := e.consecutiveFailures[event.Project]
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
	e.mu.RLock()
	tasks := make(map[string]progressState)
	for k, v := range e.taskLastProgress {
		tasks[k] = v
	}
	e.mu.RUnlock()

	now := time.Now()

	for _, rule := range e.config.Rules {
		if !rule.Enabled || rule.Type != AlertTypeTaskStuck {
			continue
		}

		threshold := rule.Condition.ProgressUnchangedFor
		if threshold == 0 {
			threshold = 10 * time.Minute
		}

		for taskID, state := range tasks {
			if now.Sub(state.UpdatedAt) > threshold && e.shouldFire(rule) {
				event := Event{
					Type:      EventTypeTaskProgress,
					TaskID:    taskID,
					Phase:     state.Phase,
					Progress:  state.Progress,
					Timestamp: now,
				}
				alert := e.createAlert(rule, event,
					fmt.Sprintf("Task %s stuck at %d%% (%s) for %v",
						taskID, state.Progress, state.Phase, now.Sub(state.UpdatedAt).Round(time.Minute)))
				e.fireAlert(ctx, rule, alert)
			}
		}
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
	source := ""
	if event.TaskID != "" {
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

// fireAlert sends an alert through configured channels
func (e *Engine) fireAlert(ctx context.Context, rule AlertRule, alert *Alert) {
	e.mu.Lock()
	e.lastAlertTimes[rule.Name] = time.Now()
	e.mu.Unlock()

	if e.dispatcher == nil {
		e.logger.Warn("no dispatcher configured, alert not sent",
			"rule", rule.Name,
			"alert_id", alert.ID,
		)
		return
	}

	// Determine which channels to send to
	channels := rule.Channels
	if len(channels) == 0 {
		// Send to all channels that accept this severity
		for _, ch := range e.config.Channels {
			if ch.Enabled && e.channelAcceptsSeverity(ch, alert.Severity) {
				channels = append(channels, ch.Name)
			}
		}
	}

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
