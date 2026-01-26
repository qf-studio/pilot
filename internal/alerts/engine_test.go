package alerts

import (
	"context"
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
