package alerts

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// mockChannel is a test mock for the Channel interface
type mockChannel struct {
	name   string
	typ    string
	alerts []*Alert
	mu     sync.Mutex
	err    error
}

func newMockChannel(name, typ string) *mockChannel {
	return &mockChannel{
		name:   name,
		typ:    typ,
		alerts: make([]*Alert, 0),
	}
}

func (m *mockChannel) Name() string { return m.name }
func (m *mockChannel) Type() string { return m.typ }

func (m *mockChannel) Send(ctx context.Context, alert *Alert) error {
	if m.err != nil {
		return m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts = append(m.alerts, alert)
	return nil
}

func (m *mockChannel) getAlerts() []*Alert {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*Alert, len(m.alerts))
	copy(result, m.alerts)
	return result
}

func (m *mockChannel) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// =============================================================================
// Engine Tests
// =============================================================================

func TestNewEngine(t *testing.T) {
	tests := []struct {
		name        string
		config      *AlertConfig
		wantEnabled bool
	}{
		{
			name: "enabled config",
			config: &AlertConfig{
				Enabled: true,
				Rules:   []AlertRule{},
			},
			wantEnabled: true,
		},
		{
			name: "disabled config",
			config: &AlertConfig{
				Enabled: false,
			},
			wantEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewEngine(tt.config)
			if engine == nil {
				t.Fatal("expected non-nil engine")
			}
			if engine.config.Enabled != tt.wantEnabled {
				t.Errorf("expected Enabled=%v, got %v", tt.wantEnabled, engine.config.Enabled)
			}
		})
	}
}

func TestEngine_WithOptions(t *testing.T) {
	config := &AlertConfig{Enabled: true}
	dispatcher := NewDispatcher(config)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	if engine.dispatcher != dispatcher {
		t.Error("expected dispatcher to be set via option")
	}
}

func TestEngine_Start_Disabled(t *testing.T) {
	config := &AlertConfig{Enabled: false}
	engine := NewEngine(config)

	err := engine.Start(context.Background())
	if err != nil {
		t.Errorf("expected no error for disabled engine, got %v", err)
	}
}

func TestEngine_Start_Enabled(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Rules:   []AlertRule{},
	}
	engine := NewEngine(config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := engine.Start(ctx)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	// Stop should not panic
	engine.Stop()
}

func TestEngine_ProcessEvent_Disabled(t *testing.T) {
	config := &AlertConfig{Enabled: false}
	mockCh := newMockChannel("test", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	engine.ProcessEvent(Event{
		Type:      EventTypeTaskFailed,
		TaskID:    "TASK-1",
		Timestamp: time.Now(),
	})

	// Give time for any potential processing
	time.Sleep(50 * time.Millisecond)

	if len(mockCh.getAlerts()) != 0 {
		t.Error("expected no alerts when engine is disabled")
	}
}

func TestEngine_ProcessTaskFailedEvent(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{
				Name:       "test-slack",
				Type:       "slack",
				Enabled:    true,
				Severities: []Severity{SeverityWarning, SeverityCritical},
			},
		},
		Rules: []AlertRule{
			{
				Name:        "task_failed",
				Type:        AlertTypeTaskFailed,
				Enabled:     true,
				Condition:   RuleCondition{},
				Severity:    SeverityWarning,
				Channels:    []string{"test-slack"},
				Cooldown:    0,
				Description: "Alert when task fails",
			},
		},
		Defaults: AlertDefaults{
			Cooldown:           5 * time.Minute,
			DefaultSeverity:    SeverityWarning,
			SuppressDuplicates: true,
		},
	}

	mockCh := newMockChannel("test-slack", "slack")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := engine.Start(ctx)
	if err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}

	// Process a task failed event
	engine.ProcessEvent(Event{
		Type:      EventTypeTaskFailed,
		TaskID:    "TASK-123",
		TaskTitle: "Test Task",
		Project:   "/test/project",
		Error:     "test error message",
		Timestamp: time.Now(),
	})

	// Wait for event processing
	time.Sleep(100 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 1 {
		t.Errorf("expected 1 alert, got %d", len(alerts))
		return
	}

	alert := alerts[0]
	if alert.Type != AlertTypeTaskFailed {
		t.Errorf("expected alert type %s, got %s", AlertTypeTaskFailed, alert.Type)
	}
	if alert.Severity != SeverityWarning {
		t.Errorf("expected severity %s, got %s", SeverityWarning, alert.Severity)
	}
	if alert.Source != "task:TASK-123" {
		t.Errorf("expected source task:TASK-123, got %s", alert.Source)
	}
}

func TestEngine_TaskStartedEvent(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Rules:   []AlertRule{},
	}

	engine := NewEngine(config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = engine.Start(ctx)

	now := time.Now()
	engine.ProcessEvent(Event{
		Type:      EventTypeTaskStarted,
		TaskID:    "TASK-100",
		Phase:     "planning",
		Timestamp: now,
	})

	time.Sleep(50 * time.Millisecond)

	engine.mu.RLock()
	state, exists := engine.taskLastProgress["TASK-100"]
	engine.mu.RUnlock()

	if !exists {
		t.Fatal("expected task progress state to be tracked")
	}
	if state.Progress != 0 {
		t.Errorf("expected progress 0, got %d", state.Progress)
	}
	if state.Phase != "planning" {
		t.Errorf("expected phase 'planning', got '%s'", state.Phase)
	}
}

func TestEngine_TaskProgressEvent(t *testing.T) {
	tests := []struct {
		name           string
		initialState   *progressState
		event          Event
		expectProgress int
		expectPhase    string
	}{
		{
			name:         "new task progress",
			initialState: nil,
			event: Event{
				Type:      EventTypeTaskProgress,
				TaskID:    "TASK-1",
				Progress:  50,
				Phase:     "coding",
				Timestamp: time.Now(),
			},
			expectProgress: 50,
			expectPhase:    "coding",
		},
		{
			name: "progress increases",
			initialState: &progressState{
				Progress:  30,
				Phase:     "planning",
				UpdatedAt: time.Now().Add(-1 * time.Minute),
			},
			event: Event{
				Type:      EventTypeTaskProgress,
				TaskID:    "TASK-2",
				Progress:  60,
				Phase:     "coding",
				Timestamp: time.Now(),
			},
			expectProgress: 60,
			expectPhase:    "coding",
		},
		{
			name: "progress unchanged lower value ignored",
			initialState: &progressState{
				Progress:  80,
				Phase:     "testing",
				UpdatedAt: time.Now().Add(-1 * time.Minute),
			},
			event: Event{
				Type:      EventTypeTaskProgress,
				TaskID:    "TASK-3",
				Progress:  50,
				Phase:     "testing",
				Timestamp: time.Now(),
			},
			expectProgress: 80,
			expectPhase:    "testing",
		},
		{
			name: "phase change updates state",
			initialState: &progressState{
				Progress:  50,
				Phase:     "coding",
				UpdatedAt: time.Now().Add(-1 * time.Minute),
			},
			event: Event{
				Type:      EventTypeTaskProgress,
				TaskID:    "TASK-4",
				Progress:  50,
				Phase:     "testing",
				Timestamp: time.Now(),
			},
			expectProgress: 50,
			expectPhase:    "testing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AlertConfig{Enabled: true, Rules: []AlertRule{}}
			engine := NewEngine(config)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_ = engine.Start(ctx)

			if tt.initialState != nil {
				engine.mu.Lock()
				engine.taskLastProgress[tt.event.TaskID] = *tt.initialState
				engine.mu.Unlock()
			}

			engine.ProcessEvent(tt.event)
			time.Sleep(50 * time.Millisecond)

			engine.mu.RLock()
			state := engine.taskLastProgress[tt.event.TaskID]
			engine.mu.RUnlock()

			if state.Progress != tt.expectProgress {
				t.Errorf("expected progress %d, got %d", tt.expectProgress, state.Progress)
			}
			if state.Phase != tt.expectPhase {
				t.Errorf("expected phase '%s', got '%s'", tt.expectPhase, state.Phase)
			}
		})
	}
}

func TestEngine_ConsecutiveFailures(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{
				Name:       "test-channel",
				Type:       "webhook",
				Enabled:    true,
				Severities: []Severity{SeverityCritical},
			},
		},
		Rules: []AlertRule{
			{
				Name:    "consecutive_failures",
				Type:    AlertTypeConsecutiveFails,
				Enabled: true,
				Condition: RuleCondition{
					ConsecutiveFailures: 3,
				},
				Severity:    SeverityCritical,
				Channels:    []string{"test-channel"},
				Cooldown:    0,
				Description: "Alert on consecutive failures",
			},
		},
		Defaults: AlertDefaults{
			Cooldown:        5 * time.Minute,
			DefaultSeverity: SeverityWarning,
		},
	}

	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = engine.Start(ctx)

	project := "/test/project"

	// Send 3 consecutive failures
	for i := 1; i <= 3; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    "TASK-" + string(rune('0'+i)),
			TaskTitle: "Test Task",
			Project:   project,
			Error:     "test error",
			Timestamp: time.Now(),
		})
	}

	// Wait for event processing
	time.Sleep(100 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 1 {
		t.Errorf("expected 1 consecutive failures alert, got %d", len(alerts))
		return
	}

	alert := alerts[0]
	if alert.Type != AlertTypeConsecutiveFails {
		t.Errorf("expected alert type %s, got %s", AlertTypeConsecutiveFails, alert.Type)
	}
	if alert.Severity != SeverityCritical {
		t.Errorf("expected severity %s, got %s", SeverityCritical, alert.Severity)
	}
}

func TestEngine_CooldownRespected(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{
				Name:       "test-channel",
				Type:       "webhook",
				Enabled:    true,
				Severities: []Severity{SeverityWarning},
			},
		},
		Rules: []AlertRule{
			{
				Name:        "task_failed",
				Type:        AlertTypeTaskFailed,
				Enabled:     true,
				Condition:   RuleCondition{},
				Severity:    SeverityWarning,
				Channels:    []string{"test-channel"},
				Cooldown:    1 * time.Hour, // Long cooldown
				Description: "Alert on task failure",
			},
		},
		Defaults: AlertDefaults{
			Cooldown:        5 * time.Minute,
			DefaultSeverity: SeverityWarning,
		},
	}

	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = engine.Start(ctx)

	// Send first failure - should trigger alert
	engine.ProcessEvent(Event{
		Type:      EventTypeTaskFailed,
		TaskID:    "TASK-1",
		TaskTitle: "Test Task 1",
		Project:   "/test/project",
		Error:     "test error",
		Timestamp: time.Now(),
	})

	time.Sleep(50 * time.Millisecond)

	// Send second failure - should be suppressed due to cooldown
	engine.ProcessEvent(Event{
		Type:      EventTypeTaskFailed,
		TaskID:    "TASK-2",
		TaskTitle: "Test Task 2",
		Project:   "/test/project",
		Error:     "test error",
		Timestamp: time.Now(),
	})

	time.Sleep(50 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 1 {
		t.Errorf("expected 1 alert (second should be suppressed by cooldown), got %d", len(alerts))
	}
}

func TestEngine_ZeroCooldown(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{
				Name:       "test-channel",
				Type:       "webhook",
				Enabled:    true,
				Severities: []Severity{SeverityWarning},
			},
		},
		Rules: []AlertRule{
			{
				Name:        "task_failed",
				Type:        AlertTypeTaskFailed,
				Enabled:     true,
				Condition:   RuleCondition{},
				Severity:    SeverityWarning,
				Channels:    []string{"test-channel"},
				Cooldown:    0, // Zero cooldown - should fire every time
				Description: "Alert on task failure",
			},
		},
	}

	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = engine.Start(ctx)

	// Send multiple failures - all should trigger alerts
	for i := 1; i <= 3; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    "TASK-" + string(rune('0'+i)),
			Project:   "/test/project",
			Error:     "test error",
			Timestamp: time.Now(),
		})
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(100 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 3 {
		t.Errorf("expected 3 alerts with zero cooldown, got %d", len(alerts))
	}
}

func TestEngine_TaskCompletedResetsFails(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{
				Name:       "test-channel",
				Type:       "webhook",
				Enabled:    true,
				Severities: []Severity{SeverityCritical},
			},
		},
		Rules: []AlertRule{
			{
				Name:    "consecutive_failures",
				Type:    AlertTypeConsecutiveFails,
				Enabled: true,
				Condition: RuleCondition{
					ConsecutiveFailures: 3,
				},
				Severity:    SeverityCritical,
				Channels:    []string{"test-channel"},
				Cooldown:    0,
				Description: "Alert on consecutive failures",
			},
		},
		Defaults: AlertDefaults{
			Cooldown:        5 * time.Minute,
			DefaultSeverity: SeverityWarning,
		},
	}

	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = engine.Start(ctx)

	project := "/test/project"

	// Send 2 failures
	for i := 1; i <= 2; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    "TASK-" + string(rune('0'+i)),
			Project:   project,
			Error:     "test error",
			Timestamp: time.Now(),
		})
	}

	time.Sleep(50 * time.Millisecond)

	// Send a success - should reset counter
	engine.ProcessEvent(Event{
		Type:      EventTypeTaskCompleted,
		TaskID:    "TASK-3",
		Project:   project,
		Timestamp: time.Now(),
	})

	time.Sleep(50 * time.Millisecond)

	// Send 2 more failures - should not trigger (counter was reset)
	for i := 4; i <= 5; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    "TASK-" + string(rune('0'+i)),
			Project:   project,
			Error:     "test error",
			Timestamp: time.Now(),
		})
	}

	time.Sleep(100 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts (success reset the counter), got %d", len(alerts))
	}
}

func TestEngine_DisabledRulesIgnored(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{
				Name:       "test-channel",
				Type:       "webhook",
				Enabled:    true,
				Severities: []Severity{SeverityWarning},
			},
		},
		Rules: []AlertRule{
			{
				Name:        "task_failed",
				Type:        AlertTypeTaskFailed,
				Enabled:     false, // Disabled
				Condition:   RuleCondition{},
				Severity:    SeverityWarning,
				Channels:    []string{"test-channel"},
				Cooldown:    0,
				Description: "Alert on task failure",
			},
		},
		Defaults: AlertDefaults{
			Cooldown:        5 * time.Minute,
			DefaultSeverity: SeverityWarning,
		},
	}

	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = engine.Start(ctx)

	engine.ProcessEvent(Event{
		Type:      EventTypeTaskFailed,
		TaskID:    "TASK-1",
		TaskTitle: "Test Task",
		Project:   "/test/project",
		Error:     "test error",
		Timestamp: time.Now(),
	})

	time.Sleep(100 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts (rule disabled), got %d", len(alerts))
	}
}

func TestEngine_CostUpdate_DailySpend(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []AlertRule{
			{
				Name:    "daily_spend",
				Type:    AlertTypeDailySpend,
				Enabled: true,
				Condition: RuleCondition{
					DailySpendThreshold: 50.0,
				},
				Severity: SeverityWarning,
				Channels: []string{"test-channel"},
				Cooldown: 0,
			},
		},
	}

	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	tests := []struct {
		name         string
		dailySpend   string
		expectAlerts int
	}{
		{
			name:         "below threshold",
			dailySpend:   "40.00",
			expectAlerts: 0,
		},
		{
			name:         "above threshold",
			dailySpend:   "60.00",
			expectAlerts: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCh.mu.Lock()
			mockCh.alerts = make([]*Alert, 0)
			mockCh.mu.Unlock()

			engine.ProcessEvent(Event{
				Type:      EventTypeCostUpdate,
				Metadata:  map[string]string{"daily_spend": tt.dailySpend},
				Timestamp: time.Now(),
			})

			time.Sleep(100 * time.Millisecond)

			alerts := mockCh.getAlerts()
			if len(alerts) != tt.expectAlerts {
				t.Errorf("expected %d alerts, got %d", tt.expectAlerts, len(alerts))
			}
		})
	}
}

func TestEngine_CostUpdate_BudgetDepleted(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []AlertRule{
			{
				Name:    "budget_depleted",
				Type:    AlertTypeBudgetDepleted,
				Enabled: true,
				Condition: RuleCondition{
					BudgetLimit: 500.0,
				},
				Severity: SeverityCritical,
				Channels: []string{"test-channel"},
				Cooldown: 0,
			},
		},
	}

	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	engine.ProcessEvent(Event{
		Type: EventTypeCostUpdate,
		Metadata: map[string]string{
			"daily_spend": "20.00",
			"total_spend": "550.00",
		},
		Timestamp: time.Now(),
	})

	time.Sleep(100 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 1 {
		t.Errorf("expected 1 budget depleted alert, got %d", len(alerts))
		return
	}

	if alerts[0].Type != AlertTypeBudgetDepleted {
		t.Errorf("expected alert type %s, got %s", AlertTypeBudgetDepleted, alerts[0].Type)
	}
}

// GH-539: Test that EventTypeBudgetExceeded routes through cost update rules
func TestEngine_BudgetExceeded_RoutesToCostRules(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []AlertRule{
			{
				Name:    "budget_depleted",
				Type:    AlertTypeBudgetDepleted,
				Enabled: true,
				Condition: RuleCondition{
					BudgetLimit: 500.0,
				},
				Severity: SeverityCritical,
				Channels: []string{"test-channel"},
				Cooldown: 0,
			},
		},
	}

	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	// Budget exceeded event should be routed through handleBudgetEvent → handleCostUpdate
	engine.ProcessEvent(Event{
		Type:   EventTypeBudgetExceeded,
		TaskID: "GH-100",
		Error:  "Daily budget exceeded: $55.00 / $50.00",
		Metadata: map[string]string{
			"daily_spend":  "55.00",
			"total_spend":  "600.00",
			"daily_left":   "0.00",
			"monthly_left": "0.00",
			"action":       "stop",
		},
		Timestamp: time.Now(),
	})

	time.Sleep(100 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 1 {
		t.Errorf("expected 1 budget depleted alert from BudgetExceeded event, got %d", len(alerts))
		return
	}

	if alerts[0].Type != AlertTypeBudgetDepleted {
		t.Errorf("expected alert type %s, got %s", AlertTypeBudgetDepleted, alerts[0].Type)
	}
}

// GH-539: Test that EventTypeBudgetWarning routes through cost update rules
func TestEngine_BudgetWarning_RoutesToCostRules(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []AlertRule{
			{
				Name:    "daily_spend_alert",
				Type:    AlertTypeDailySpend,
				Enabled: true,
				Condition: RuleCondition{
					DailySpendThreshold: 40.0,
				},
				Severity: SeverityWarning,
				Channels: []string{"test-channel"},
				Cooldown: 0,
			},
		},
	}

	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	// Budget warning event should route to daily spend rule
	engine.ProcessEvent(Event{
		Type:  EventTypeBudgetWarning,
		Error: "Daily budget at 90%: $45.00 / $50.00",
		Metadata: map[string]string{
			"daily_spend": "45.00",
			"alert_type":  "daily_budget_warning",
			"severity":    "warning",
		},
		Timestamp: time.Now(),
	})

	time.Sleep(100 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 1 {
		t.Errorf("expected 1 daily spend alert from BudgetWarning event, got %d", len(alerts))
		return
	}

	if alerts[0].Type != AlertTypeDailySpend {
		t.Errorf("expected alert type %s, got %s", AlertTypeDailySpend, alerts[0].Type)
	}
}

func TestEngine_SecurityEvent(t *testing.T) {
	tests := []struct {
		name      string
		alertType AlertType
		metadata  map[string]string
	}{
		{
			name:      "unauthorized access",
			alertType: AlertTypeUnauthorizedAccess,
			metadata:  map[string]string{"user": "unknown"},
		},
		{
			name:      "sensitive file modified",
			alertType: AlertTypeSensitiveFile,
			metadata:  map[string]string{"file_path": "/etc/passwd"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AlertConfig{
				Enabled: true,
				Channels: []ChannelConfig{
					{Name: "test-channel", Type: "webhook", Enabled: true},
				},
				Rules: []AlertRule{
					{
						Name:     tt.name,
						Type:     tt.alertType,
						Enabled:  true,
						Severity: SeverityCritical,
						Channels: []string{"test-channel"},
						Cooldown: 0,
					},
				},
			}

			mockCh := newMockChannel("test-channel", "webhook")
			dispatcher := NewDispatcher(config)
			dispatcher.RegisterChannel(mockCh)

			engine := NewEngine(config, WithDispatcher(dispatcher))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_ = engine.Start(ctx)

			engine.ProcessEvent(Event{
				Type:      EventTypeSecurityEvent,
				Metadata:  tt.metadata,
				Timestamp: time.Now(),
			})

			time.Sleep(100 * time.Millisecond)

			alerts := mockCh.getAlerts()
			if len(alerts) != 1 {
				t.Errorf("expected 1 alert, got %d", len(alerts))
				return
			}

			if alerts[0].Type != tt.alertType {
				t.Errorf("expected alert type %s, got %s", tt.alertType, alerts[0].Type)
			}
		})
	}
}

func TestEngine_ChannelAcceptsSeverity(t *testing.T) {
	config := &AlertConfig{Enabled: true}
	engine := NewEngine(config)

	tests := []struct {
		name       string
		channel    ChannelConfig
		severity   Severity
		wantAccept bool
	}{
		{
			name: "empty severities accepts all",
			channel: ChannelConfig{
				Name:       "test",
				Severities: []Severity{},
			},
			severity:   SeverityCritical,
			wantAccept: true,
		},
		{
			name: "matching severity",
			channel: ChannelConfig{
				Name:       "test",
				Severities: []Severity{SeverityWarning, SeverityCritical},
			},
			severity:   SeverityWarning,
			wantAccept: true,
		},
		{
			name: "non-matching severity",
			channel: ChannelConfig{
				Name:       "test",
				Severities: []Severity{SeverityCritical},
			},
			severity:   SeverityInfo,
			wantAccept: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := engine.channelAcceptsSeverity(tt.channel, tt.severity)
			if got != tt.wantAccept {
				t.Errorf("channelAcceptsSeverity() = %v, want %v", got, tt.wantAccept)
			}
		})
	}
}

func TestEngine_FireAlertWithoutDispatcher(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Rules: []AlertRule{
			{
				Name:     "task_failed",
				Type:     AlertTypeTaskFailed,
				Enabled:  true,
				Severity: SeverityWarning,
				Channels: []string{"test"},
				Cooldown: 0,
			},
		},
	}

	// Engine without dispatcher
	engine := NewEngine(config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	// Should not panic
	engine.ProcessEvent(Event{
		Type:      EventTypeTaskFailed,
		TaskID:    "TASK-1",
		Timestamp: time.Now(),
	})

	time.Sleep(100 * time.Millisecond)

	// Verify history is still recorded
	history := engine.GetAlertHistory(10)
	// No history recorded because dispatcher is nil
	if len(history) != 0 {
		t.Errorf("expected 0 history entries without dispatcher, got %d", len(history))
	}
}

func TestEngine_EmptyChannelList(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "enabled-channel", Type: "webhook", Enabled: true},
			{Name: "disabled-channel", Type: "webhook", Enabled: false},
		},
		Rules: []AlertRule{
			{
				Name:     "task_failed",
				Type:     AlertTypeTaskFailed,
				Enabled:  true,
				Severity: SeverityWarning,
				Channels: []string{}, // Empty - should use all enabled channels
				Cooldown: 0,
			},
		},
	}

	mockEnabled := newMockChannel("enabled-channel", "webhook")
	mockDisabled := newMockChannel("disabled-channel", "webhook")

	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockEnabled)
	dispatcher.RegisterChannel(mockDisabled)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	engine.ProcessEvent(Event{
		Type:      EventTypeTaskFailed,
		TaskID:    "TASK-1",
		Timestamp: time.Now(),
	})

	time.Sleep(100 * time.Millisecond)

	if len(mockEnabled.getAlerts()) != 1 {
		t.Error("expected enabled channel to receive alert")
	}
	if len(mockDisabled.getAlerts()) != 0 {
		t.Error("expected disabled channel to not receive alert")
	}
}

func TestAlertHistory(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{
				Name:       "test-channel",
				Type:       "webhook",
				Enabled:    true,
				Severities: []Severity{SeverityWarning},
			},
		},
		Rules: []AlertRule{
			{
				Name:        "task_failed",
				Type:        AlertTypeTaskFailed,
				Enabled:     true,
				Condition:   RuleCondition{},
				Severity:    SeverityWarning,
				Channels:    []string{"test-channel"},
				Cooldown:    0,
				Description: "Alert on task failure",
			},
		},
	}

	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = engine.Start(ctx)

	// Send some failures
	for i := 1; i <= 3; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    "TASK-" + string(rune('0'+i)),
			Project:   "/test/project",
			Error:     "test error",
			Timestamp: time.Now(),
		})
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(100 * time.Millisecond)

	history := engine.GetAlertHistory(10)
	if len(history) != 3 {
		t.Errorf("expected 3 history entries, got %d", len(history))
	}

	// Check that history is in reverse order (most recent first)
	if len(history) >= 2 && history[0].FiredAt.Before(history[1].FiredAt) {
		t.Error("expected history to be in reverse chronological order")
	}
}

func TestEngine_GetAlertHistory_LimitBehavior(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "test", Type: "webhook", Enabled: true},
		},
		Rules: []AlertRule{
			{
				Name:     "task_failed",
				Type:     AlertTypeTaskFailed,
				Enabled:  true,
				Severity: SeverityWarning,
				Channels: []string{"test"},
				Cooldown: 0,
			},
		},
	}

	mockCh := newMockChannel("test", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	// Send 5 failures
	for i := 0; i < 5; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    "TASK-" + string(rune('0'+i)),
			Timestamp: time.Now(),
		})
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)

	tests := []struct {
		name      string
		limit     int
		wantCount int
	}{
		{"zero limit returns all", 0, 5},
		{"negative limit returns all", -1, 5},
		{"limit exceeds count", 10, 5},
		{"limit less than count", 2, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			history := engine.GetAlertHistory(tt.limit)
			if len(history) != tt.wantCount {
				t.Errorf("GetAlertHistory(%d) = %d entries, want %d", tt.limit, len(history), tt.wantCount)
			}
		})
	}
}

func TestEngine_UpdateConfig(t *testing.T) {
	config1 := &AlertConfig{Enabled: true}
	config2 := &AlertConfig{Enabled: false}

	engine := NewEngine(config1)

	if !engine.GetConfig().Enabled {
		t.Error("expected config to be enabled initially")
	}

	engine.UpdateConfig(config2)

	if engine.GetConfig().Enabled {
		t.Error("expected config to be disabled after update")
	}
}

// =============================================================================
// Dispatcher Tests
// =============================================================================

func TestDispatcher_DispatchToMultipleChannels(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "channel-1", Type: "webhook", Enabled: true},
			{Name: "channel-2", Type: "webhook", Enabled: true},
			{Name: "channel-3", Type: "webhook", Enabled: true},
		},
	}

	ch1 := newMockChannel("channel-1", "webhook")
	ch2 := newMockChannel("channel-2", "webhook")
	ch3 := newMockChannel("channel-3", "webhook")

	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(ch1)
	dispatcher.RegisterChannel(ch2)
	dispatcher.RegisterChannel(ch3)

	alert := &Alert{
		ID:       "test-alert-1",
		Type:     AlertTypeTaskFailed,
		Severity: SeverityWarning,
		Title:    "Test Alert",
		Message:  "Test message",
	}

	results := dispatcher.Dispatch(context.Background(), alert, []string{"channel-1", "channel-2"})

	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}

	for _, r := range results {
		if !r.Success {
			t.Errorf("expected success for channel %s", r.ChannelName)
		}
	}

	if len(ch1.getAlerts()) != 1 {
		t.Error("channel-1 should have received 1 alert")
	}
	if len(ch2.getAlerts()) != 1 {
		t.Error("channel-2 should have received 1 alert")
	}
	if len(ch3.getAlerts()) != 0 {
		t.Error("channel-3 should not have received any alerts")
	}
}

func TestDispatcher_ChannelNotFound(t *testing.T) {
	config := &AlertConfig{
		Enabled:  true,
		Channels: []ChannelConfig{},
	}

	dispatcher := NewDispatcher(config)

	alert := &Alert{
		ID:       "test-alert-1",
		Type:     AlertTypeTaskFailed,
		Severity: SeverityWarning,
		Title:    "Test Alert",
		Message:  "Test message",
	}

	results := dispatcher.Dispatch(context.Background(), alert, []string{"nonexistent"})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Success {
		t.Error("expected failure for nonexistent channel")
	}
	if results[0].Error != ErrChannelNotFound {
		t.Errorf("expected ErrChannelNotFound, got %v", results[0].Error)
	}
}

func TestDispatcher_RegisterUnregisterChannel(t *testing.T) {
	config := &AlertConfig{Enabled: true}
	dispatcher := NewDispatcher(config)

	ch := newMockChannel("test-channel", "webhook")
	dispatcher.RegisterChannel(ch)

	// Verify channel is registered
	found, ok := dispatcher.GetChannel("test-channel")
	if !ok {
		t.Error("expected channel to be found after registration")
	}
	if found.Name() != "test-channel" {
		t.Errorf("expected channel name 'test-channel', got '%s'", found.Name())
	}

	// Unregister
	dispatcher.UnregisterChannel("test-channel")

	// Verify channel is removed
	_, ok = dispatcher.GetChannel("test-channel")
	if ok {
		t.Error("expected channel to not be found after unregistration")
	}
}

func TestDispatcher_ListChannels(t *testing.T) {
	config := &AlertConfig{Enabled: true}
	dispatcher := NewDispatcher(config)

	dispatcher.RegisterChannel(newMockChannel("ch-1", "webhook"))
	dispatcher.RegisterChannel(newMockChannel("ch-2", "slack"))
	dispatcher.RegisterChannel(newMockChannel("ch-3", "telegram"))

	channels := dispatcher.ListChannels()
	if len(channels) != 3 {
		t.Errorf("expected 3 channels, got %d", len(channels))
	}

	// Verify all names are present
	nameSet := make(map[string]bool)
	for _, name := range channels {
		nameSet[name] = true
	}

	for _, expected := range []string{"ch-1", "ch-2", "ch-3"} {
		if !nameSet[expected] {
			t.Errorf("expected channel '%s' in list", expected)
		}
	}
}

func TestDispatcher_DispatchAll(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "enabled-warning", Type: "webhook", Enabled: true, Severities: []Severity{SeverityWarning}},
			{Name: "enabled-critical", Type: "webhook", Enabled: true, Severities: []Severity{SeverityCritical}},
			{Name: "disabled", Type: "webhook", Enabled: false, Severities: []Severity{SeverityWarning}},
		},
	}

	chWarning := newMockChannel("enabled-warning", "webhook")
	chCritical := newMockChannel("enabled-critical", "webhook")
	chDisabled := newMockChannel("disabled", "webhook")

	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(chWarning)
	dispatcher.RegisterChannel(chCritical)
	dispatcher.RegisterChannel(chDisabled)

	alert := &Alert{
		ID:       "test",
		Severity: SeverityWarning,
	}

	results := dispatcher.DispatchAll(context.Background(), alert)

	// Should only dispatch to enabled-warning (enabled + matching severity)
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}

	if len(chWarning.getAlerts()) != 1 {
		t.Error("expected enabled-warning to receive alert")
	}
	if len(chCritical.getAlerts()) != 0 {
		t.Error("expected enabled-critical to not receive alert (severity mismatch)")
	}
	if len(chDisabled.getAlerts()) != 0 {
		t.Error("expected disabled to not receive alert")
	}
}

func TestDispatcher_ChannelSendError(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "error-channel", Type: "webhook", Enabled: true},
		},
	}

	ch := newMockChannel("error-channel", "webhook")
	ch.setError(errors.New("send failed"))

	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(ch)

	alert := &Alert{
		ID:       "test",
		Severity: SeverityWarning,
	}

	results := dispatcher.Dispatch(context.Background(), alert, []string{"error-channel"})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Success {
		t.Error("expected failure when channel returns error")
	}
	if results[0].Error == nil {
		t.Error("expected error to be set")
	}
}

func TestDispatcher_SeverityMatches(t *testing.T) {
	config := &AlertConfig{Enabled: true}
	dispatcher := NewDispatcher(config)

	tests := []struct {
		name       string
		severities []Severity
		alert      Severity
		want       bool
	}{
		{
			name:       "empty severities matches all",
			severities: []Severity{},
			alert:      SeverityCritical,
			want:       true,
		},
		{
			name:       "matching severity",
			severities: []Severity{SeverityWarning, SeverityCritical},
			alert:      SeverityWarning,
			want:       true,
		},
		{
			name:       "non-matching severity",
			severities: []Severity{SeverityCritical},
			alert:      SeverityInfo,
			want:       false,
		},
		{
			name:       "nil-like empty",
			severities: nil,
			alert:      SeverityWarning,
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dispatcher.severityMatches(tt.severities, tt.alert)
			if got != tt.want {
				t.Errorf("severityMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

// =============================================================================
// Alert Creation Tests
// =============================================================================

func TestEngine_CreateAlert(t *testing.T) {
	config := &AlertConfig{Enabled: true}
	engine := NewEngine(config)

	tests := []struct {
		name       string
		rule       AlertRule
		event      Event
		message    string
		wantSource string
	}{
		{
			name: "with task ID",
			rule: AlertRule{
				Type:        AlertTypeTaskFailed,
				Severity:    SeverityWarning,
				Description: "Task failed alert",
			},
			event: Event{
				TaskID:  "TASK-123",
				Project: "/my/project",
				Metadata: map[string]string{
					"custom": "value",
				},
			},
			message:    "Task failed",
			wantSource: "task:TASK-123",
		},
		{
			name: "without task ID",
			rule: AlertRule{
				Type:        AlertTypeDailySpend,
				Severity:    SeverityCritical,
				Description: "Budget alert",
			},
			event: Event{
				Project: "/my/project",
			},
			message:    "Budget exceeded",
			wantSource: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alert := engine.createAlert(tt.rule, tt.event, tt.message)

			if alert.ID == "" {
				t.Error("expected non-empty alert ID")
			}
			if alert.Type != tt.rule.Type {
				t.Errorf("expected type %s, got %s", tt.rule.Type, alert.Type)
			}
			if alert.Severity != tt.rule.Severity {
				t.Errorf("expected severity %s, got %s", tt.rule.Severity, alert.Severity)
			}
			if alert.Title != tt.rule.Description {
				t.Errorf("expected title '%s', got '%s'", tt.rule.Description, alert.Title)
			}
			if alert.Message != tt.message {
				t.Errorf("expected message '%s', got '%s'", tt.message, alert.Message)
			}
			if alert.Source != tt.wantSource {
				t.Errorf("expected source '%s', got '%s'", tt.wantSource, alert.Source)
			}
			if alert.ProjectPath != tt.event.Project {
				t.Errorf("expected project path '%s', got '%s'", tt.event.Project, alert.ProjectPath)
			}
			if alert.CreatedAt.IsZero() {
				t.Error("expected non-zero CreatedAt")
			}
		})
	}
}

// =============================================================================
// Channel Error Tests
// =============================================================================

func TestChannelError(t *testing.T) {
	err := &ChannelError{Message: "test error message"}

	if err.Error() != "test error message" {
		t.Errorf("expected 'test error message', got '%s'", err.Error())
	}
}

// =============================================================================
// Engine Option Tests
// =============================================================================

func TestEngine_WithLogger(t *testing.T) {
	config := &AlertConfig{Enabled: true}
	logger := slog.Default()

	engine := NewEngine(config, WithLogger(logger))

	if engine.logger != logger {
		t.Error("expected logger to be set via option")
	}
}

func TestDispatcher_WithDispatcherLogger(t *testing.T) {
	config := &AlertConfig{Enabled: true}
	logger := slog.Default()

	dispatcher := NewDispatcher(config, WithDispatcherLogger(logger))

	if dispatcher.logger != logger {
		t.Error("expected logger to be set via option")
	}
}

// =============================================================================
// Stuck Tasks Evaluation Tests
// =============================================================================

func TestEngine_EvaluateStuckTasks(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []AlertRule{
			{
				Name:    "task_stuck",
				Type:    AlertTypeTaskStuck,
				Enabled: true,
				Condition: RuleCondition{
					ProgressUnchangedFor: 1 * time.Millisecond, // Very short for testing
				},
				Severity: SeverityWarning,
				Channels: []string{"test-channel"},
				Cooldown: 0,
			},
		},
	}

	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	// Add a stuck task
	engine.mu.Lock()
	engine.taskLastProgress["TASK-STUCK"] = progressState{
		Progress:  50,
		Phase:     "coding",
		UpdatedAt: time.Now().Add(-1 * time.Hour), // Old timestamp - will be stuck
	}
	engine.mu.Unlock()

	// Manually trigger evaluation
	ctx := context.Background()
	engine.evaluateStuckTasks(ctx)

	time.Sleep(50 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 1 {
		t.Errorf("expected 1 stuck task alert, got %d", len(alerts))
		return
	}

	if alerts[0].Type != AlertTypeTaskStuck {
		t.Errorf("expected alert type %s, got %s", AlertTypeTaskStuck, alerts[0].Type)
	}
}

func TestEngine_EvaluateStuckTasks_DefaultThreshold(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []AlertRule{
			{
				Name:    "task_stuck",
				Type:    AlertTypeTaskStuck,
				Enabled: true,
				Condition: RuleCondition{
					ProgressUnchangedFor: 0, // Zero - should use default
				},
				Severity: SeverityWarning,
				Channels: []string{"test-channel"},
				Cooldown: 0,
			},
		},
	}

	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	// Add a task that was just updated - should NOT trigger alert with default 10 min threshold
	engine.mu.Lock()
	engine.taskLastProgress["TASK-RECENT"] = progressState{
		Progress:  50,
		Phase:     "coding",
		UpdatedAt: time.Now(),
	}
	engine.mu.Unlock()

	// Manually trigger evaluation
	ctx := context.Background()
	engine.evaluateStuckTasks(ctx)

	time.Sleep(50 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts for recent task, got %d", len(alerts))
	}
}

func TestEngine_EvaluateStuckTasks_DisabledRule(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []AlertRule{
			{
				Name:    "task_stuck",
				Type:    AlertTypeTaskStuck,
				Enabled: false, // Disabled
				Condition: RuleCondition{
					ProgressUnchangedFor: 1 * time.Millisecond,
				},
				Severity: SeverityWarning,
				Channels: []string{"test-channel"},
				Cooldown: 0,
			},
		},
	}

	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	// Add a stuck task
	engine.mu.Lock()
	engine.taskLastProgress["TASK-STUCK"] = progressState{
		Progress:  50,
		Phase:     "coding",
		UpdatedAt: time.Now().Add(-1 * time.Hour),
	}
	engine.mu.Unlock()

	// Manually trigger evaluation
	ctx := context.Background()
	engine.evaluateStuckTasks(ctx)

	time.Sleep(50 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts (rule disabled), got %d", len(alerts))
	}
}

func TestEngine_EvaluateStuckTasks_WrongRuleType(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []AlertRule{
			{
				Name:     "task_failed", // Wrong type - should not evaluate stuck
				Type:     AlertTypeTaskFailed,
				Enabled:  true,
				Severity: SeverityWarning,
				Channels: []string{"test-channel"},
				Cooldown: 0,
			},
		},
	}

	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	// Add a stuck task
	engine.mu.Lock()
	engine.taskLastProgress["TASK-STUCK"] = progressState{
		Progress:  50,
		Phase:     "coding",
		UpdatedAt: time.Now().Add(-1 * time.Hour),
	}
	engine.mu.Unlock()

	// Manually trigger evaluation
	ctx := context.Background()
	engine.evaluateStuckTasks(ctx)

	time.Sleep(50 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts (wrong rule type), got %d", len(alerts))
	}
}

// =============================================================================
// Event Queue Full Test
// =============================================================================

func TestEngine_ProcessEvent_QueueFull(t *testing.T) {
	config := &AlertConfig{Enabled: true}
	engine := NewEngine(config)

	// Fill up the event queue (capacity is 100)
	for i := 0; i < 110; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    "TASK-" + string(rune('0'+i)),
			Timestamp: time.Now(),
		})
	}

	// Should not panic - events beyond capacity are dropped
}

// =============================================================================
// Escalation Tests (GH-848)
// =============================================================================

func TestEngine_Escalation_AfterThreeFailures(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{
				Name:       "pagerduty",
				Type:       "pagerduty",
				Enabled:    true,
				Severities: []Severity{SeverityCritical},
			},
		},
		Rules: []AlertRule{
			{
				Name:    "escalation",
				Type:    AlertTypeEscalation,
				Enabled: true,
				Condition: RuleCondition{
					EscalationRetries: 3,
				},
				Severity:    SeverityCritical,
				Channels:    []string{"pagerduty"},
				Cooldown:    0,
				Description: "Escalate after repeated failures",
			},
		},
	}

	mockCh := newMockChannel("pagerduty", "pagerduty")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	source := "issue:GH-123"

	// Send 3 failures for the same source
	for i := 1; i <= 3; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    "TASK-" + string(rune('0'+i)),
			Project:   "/test/project",
			Error:     "test error",
			Metadata:  map[string]string{"source": source},
			Timestamp: time.Now(),
		})
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(100 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 1 {
		t.Errorf("expected 1 escalation alert, got %d", len(alerts))
		return
	}

	alert := alerts[0]
	if alert.Type != AlertTypeEscalation {
		t.Errorf("expected alert type %s, got %s", AlertTypeEscalation, alert.Type)
	}
	if alert.Severity != SeverityCritical {
		t.Errorf("expected severity %s, got %s", SeverityCritical, alert.Severity)
	}
	if alert.Source != source {
		t.Errorf("expected source %s, got %s", source, alert.Source)
	}
	if alert.Metadata["retry_count"] != "3" {
		t.Errorf("expected retry_count=3 in metadata, got %s", alert.Metadata["retry_count"])
	}
}

func TestEngine_Escalation_NoEscalationBeforeThreshold(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{
				Name:       "pagerduty",
				Type:       "pagerduty",
				Enabled:    true,
				Severities: []Severity{SeverityCritical},
			},
		},
		Rules: []AlertRule{
			{
				Name:    "escalation",
				Type:    AlertTypeEscalation,
				Enabled: true,
				Condition: RuleCondition{
					EscalationRetries: 3,
				},
				Severity:    SeverityCritical,
				Channels:    []string{"pagerduty"},
				Cooldown:    0,
				Description: "Escalate after repeated failures",
			},
		},
	}

	mockCh := newMockChannel("pagerduty", "pagerduty")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	source := "issue:GH-456"

	// Send only 2 failures (below threshold)
	for i := 1; i <= 2; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    "TASK-" + string(rune('0'+i)),
			Project:   "/test/project",
			Error:     "test error",
			Metadata:  map[string]string{"source": source},
			Timestamp: time.Now(),
		})
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(100 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 0 {
		t.Errorf("expected 0 escalation alerts (below threshold), got %d", len(alerts))
	}
}

func TestEngine_Escalation_ResetOnSuccess(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{
				Name:       "pagerduty",
				Type:       "pagerduty",
				Enabled:    true,
				Severities: []Severity{SeverityCritical},
			},
		},
		Rules: []AlertRule{
			{
				Name:    "escalation",
				Type:    AlertTypeEscalation,
				Enabled: true,
				Condition: RuleCondition{
					EscalationRetries: 3,
				},
				Severity:    SeverityCritical,
				Channels:    []string{"pagerduty"},
				Cooldown:    0,
				Description: "Escalate after repeated failures",
			},
		},
	}

	mockCh := newMockChannel("pagerduty", "pagerduty")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	source := "issue:GH-789"

	// Send 2 failures
	for i := 1; i <= 2; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    "TASK-" + string(rune('0'+i)),
			Project:   "/test/project",
			Error:     "test error",
			Metadata:  map[string]string{"source": source},
			Timestamp: time.Now(),
		})
	}
	time.Sleep(50 * time.Millisecond)

	// Send success - should reset counter
	engine.ProcessEvent(Event{
		Type:      EventTypeTaskCompleted,
		TaskID:    "TASK-3",
		Project:   "/test/project",
		Metadata:  map[string]string{"source": source},
		Timestamp: time.Now(),
	})
	time.Sleep(50 * time.Millisecond)

	// Send 2 more failures - should not escalate (counter was reset)
	for i := 4; i <= 5; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    "TASK-" + string(rune('0'+i)),
			Project:   "/test/project",
			Error:     "test error",
			Metadata:  map[string]string{"source": source},
			Timestamp: time.Now(),
		})
	}
	time.Sleep(100 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 0 {
		t.Errorf("expected 0 escalation alerts (success reset counter), got %d", len(alerts))
	}
}

func TestEngine_Escalation_DifferentSourcesTrackedSeparately(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{
				Name:       "pagerduty",
				Type:       "pagerduty",
				Enabled:    true,
				Severities: []Severity{SeverityCritical},
			},
		},
		Rules: []AlertRule{
			{
				Name:    "escalation",
				Type:    AlertTypeEscalation,
				Enabled: true,
				Condition: RuleCondition{
					EscalationRetries: 3,
				},
				Severity:    SeverityCritical,
				Channels:    []string{"pagerduty"},
				Cooldown:    0,
				Description: "Escalate after repeated failures",
			},
		},
	}

	mockCh := newMockChannel("pagerduty", "pagerduty")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	// Send 2 failures for source A
	for i := 1; i <= 2; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    "TASK-A-" + string(rune('0'+i)),
			Project:   "/test/project",
			Error:     "error A",
			Metadata:  map[string]string{"source": "issue:A"},
			Timestamp: time.Now(),
		})
	}

	// Send 2 failures for source B
	for i := 1; i <= 2; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    "TASK-B-" + string(rune('0'+i)),
			Project:   "/test/project",
			Error:     "error B",
			Metadata:  map[string]string{"source": "issue:B"},
			Timestamp: time.Now(),
		})
	}

	time.Sleep(100 * time.Millisecond)

	// Neither should have escalated (both at 2 failures)
	alerts := mockCh.getAlerts()
	if len(alerts) != 0 {
		t.Errorf("expected 0 escalation alerts (neither source at threshold), got %d", len(alerts))
	}

	// Third failure for source A - should escalate
	engine.ProcessEvent(Event{
		Type:      EventTypeTaskFailed,
		TaskID:    "TASK-A-3",
		Project:   "/test/project",
		Error:     "error A",
		Metadata:  map[string]string{"source": "issue:A"},
		Timestamp: time.Now(),
	})

	time.Sleep(100 * time.Millisecond)

	alerts = mockCh.getAlerts()
	if len(alerts) != 1 {
		t.Errorf("expected 1 escalation alert for source A, got %d", len(alerts))
		return
	}

	if alerts[0].Source != "issue:A" {
		t.Errorf("expected source issue:A, got %s", alerts[0].Source)
	}
}

func TestEngine_Escalation_DefaultThreshold(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{
				Name:       "pagerduty",
				Type:       "pagerduty",
				Enabled:    true,
				Severities: []Severity{SeverityCritical},
			},
		},
		Rules: []AlertRule{
			{
				Name:    "escalation",
				Type:    AlertTypeEscalation,
				Enabled: true,
				Condition: RuleCondition{
					EscalationRetries: 0, // Zero - should use default of 3
				},
				Severity:    SeverityCritical,
				Channels:    []string{"pagerduty"},
				Cooldown:    0,
				Description: "Escalate after repeated failures",
			},
		},
	}

	mockCh := newMockChannel("pagerduty", "pagerduty")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	source := "issue:GH-DEFAULT"

	// Send 3 failures (default threshold)
	for i := 1; i <= 3; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    "TASK-" + string(rune('0'+i)),
			Project:   "/test/project",
			Error:     "test error",
			Metadata:  map[string]string{"source": source},
			Timestamp: time.Now(),
		})
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(100 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 1 {
		t.Errorf("expected 1 escalation alert with default threshold, got %d", len(alerts))
	}
}

func TestEngine_Escalation_FallbackToTaskIDAsSource(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{
				Name:       "pagerduty",
				Type:       "pagerduty",
				Enabled:    true,
				Severities: []Severity{SeverityCritical},
			},
		},
		Rules: []AlertRule{
			{
				Name:    "escalation",
				Type:    AlertTypeEscalation,
				Enabled: true,
				Condition: RuleCondition{
					EscalationRetries: 3,
				},
				Severity:    SeverityCritical,
				Channels:    []string{"pagerduty"},
				Cooldown:    0,
				Description: "Escalate after repeated failures",
			},
		},
	}

	mockCh := newMockChannel("pagerduty", "pagerduty")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	// Send 3 failures with same TaskID but no explicit source in metadata
	taskID := "TASK-SAME"
	for i := 1; i <= 3; i++ {
		engine.ProcessEvent(Event{
			Type:      EventTypeTaskFailed,
			TaskID:    taskID,
			Project:   "/test/project",
			Error:     "test error",
			Timestamp: time.Now(),
		})
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(100 * time.Millisecond)

	alerts := mockCh.getAlerts()
	if len(alerts) != 1 {
		t.Errorf("expected 1 escalation alert using TaskID as source, got %d", len(alerts))
		return
	}

	if alerts[0].Source != taskID {
		t.Errorf("expected source %s (TaskID fallback), got %s", taskID, alerts[0].Source)
	}
}

func TestEngine_CreateEscalationAlert(t *testing.T) {
	config := &AlertConfig{Enabled: true}
	engine := NewEngine(config)

	rule := AlertRule{
		Type:        AlertTypeEscalation,
		Severity:    SeverityCritical,
		Description: "Escalation alert",
	}

	event := Event{
		TaskID:  "TASK-123",
		Project: "/my/project",
		Error:   "original error message",
		Metadata: map[string]string{
			"custom": "value",
		},
	}

	alert := engine.createEscalationAlert(rule, event, "issue:GH-123", 3)

	if alert.ID == "" {
		t.Error("expected non-empty alert ID")
	}
	if alert.Type != AlertTypeEscalation {
		t.Errorf("expected type %s, got %s", AlertTypeEscalation, alert.Type)
	}
	if alert.Severity != SeverityCritical {
		t.Errorf("expected severity %s, got %s", SeverityCritical, alert.Severity)
	}
	if alert.Source != "issue:GH-123" {
		t.Errorf("expected source 'issue:GH-123', got '%s'", alert.Source)
	}
	if alert.Metadata["retry_count"] != "3" {
		t.Errorf("expected retry_count=3, got %s", alert.Metadata["retry_count"])
	}
	if alert.Metadata["escalation_source"] != "issue:GH-123" {
		t.Errorf("expected escalation_source=issue:GH-123, got %s", alert.Metadata["escalation_source"])
	}
	if alert.Metadata["custom"] != "value" {
		t.Errorf("expected custom=value (preserved from event), got %s", alert.Metadata["custom"])
	}
	if alert.ProjectPath != "/my/project" {
		t.Errorf("expected project path '/my/project', got '%s'", alert.ProjectPath)
	}
}
