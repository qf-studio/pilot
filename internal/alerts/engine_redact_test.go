package alerts

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestProcessEvent_RedactsSecretsBeforeChannels(t *testing.T) {
	// Assembled at runtime: a literal would trip the secret-pattern scan.
	token := "123456789:AA" + strings.Repeat("a", 33)
	tests := []struct {
		name  string
		event Event
	}{
		{
			name: "token in Error",
			event: Event{
				Type:      EventTypeEscalation,
				TaskID:    "adapter-verify",
				Project:   "qf-studio/pilot",
				Error:     `Get "https://api.telegram.org/bot` + token + `/getUpdates": EOF`,
				Timestamp: time.Now(),
			},
		},
		{
			name: "token in Metadata",
			event: Event{
				Type:      EventTypeEscalation,
				TaskID:    "adapter-verify",
				Project:   "qf-studio/pilot",
				Error:     "adapter verification failed",
				Metadata:  map[string]string{"detail": "bot" + token},
				Timestamp: time.Now(),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, mockCh := mkGH5065EscalationEngine(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_ = engine.Start(ctx)
			defer engine.Stop()

			engine.ProcessEvent(tt.event)
			engine.flushForTest()

			alerts := mockCh.getAlerts()
			if len(alerts) == 0 {
				t.Fatal("expected an alert on the channel")
			}
			for _, a := range alerts {
				if text := a.Title + "\n" + a.Message; strings.Contains(text, token) {
					t.Errorf("alert text holds the token: %s", text)
				}
				for k, v := range a.Metadata {
					if strings.Contains(v, token) {
						t.Errorf("alert metadata %q holds the token", k)
					}
				}
			}
			if tt.name == "token in Error" && !strings.Contains(alerts[0].Message, "<redacted>") {
				t.Errorf("alert message lacks <redacted>: %s", alerts[0].Message)
			}
		})
	}
}

// mutation pin: redactEvent leaves Event.TaskTitle unredacted, so the escalation fallback text carries the secret -> this test fails
func TestProcessEvent_RedactsTaskTitle(t *testing.T) {
	token := "123456789:AA" + strings.Repeat("a", 33)
	engine, mockCh := mkGH5065EscalationEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)
	defer engine.Stop()

	// No Error and no circuit-breaker metadata: handleEscalation falls back to
	// "Escalation event received for <TaskTitle> ...".
	engine.ProcessEvent(Event{
		Type:      EventTypeEscalation,
		TaskID:    "adapter-verify",
		TaskTitle: "bot" + token + " getUpdates",
		Project:   "qf-studio/pilot",
		Timestamp: time.Now(),
	})
	engine.flushForTest()

	alerts := mockCh.getAlerts()
	if len(alerts) == 0 {
		t.Fatal("expected an alert on the channel")
	}
	for _, a := range alerts {
		if text := a.Title + "\n" + a.Message; strings.Contains(text, token) {
			t.Errorf("alert text holds the token: %s", text)
		}
	}
	if !strings.Contains(alerts[0].Message, "<redacted>") {
		t.Errorf("alert message lacks <redacted>: %s", alerts[0].Message)
	}
}

// mutation pin: redactEvent stops redacting Metadata, or the config_error path stops reading the redacted Error and Metadata -> this test fails
func TestProcessEvent_RedactsConfigErrorAndMetadata(t *testing.T) {
	token := "123456789:AA" + strings.Repeat("a", 33)
	config := &AlertConfig{
		Enabled:  true,
		Channels: []ChannelConfig{{Name: "test-channel", Type: "webhook", Enabled: true}},
		Rules: []AlertRule{{
			Name:     "service-unhealthy",
			Type:     AlertTypeServiceUnhealthy,
			Enabled:  true,
			Severity: SeverityWarning,
			Channels: []string{"test-channel"},
		}},
	}
	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)
	engine := NewEngine(config, WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)
	defer engine.Stop()

	// The shape cmd/pilot/adapter_preflight.go emits on a failed adapter probe.
	engine.ProcessEvent(Event{
		Type:      EventTypeConfigError,
		Source:    "adapter:telegram",
		Error:     `telegram adapter verification failed: Get "https://api.telegram.org/bot` + token + `/getMe": EOF`,
		Metadata:  map[string]string{"detail": "bot" + token},
		Timestamp: time.Now(),
	})

	waitForAlerts(t, mockCh, 1, 2*time.Second)
	a := mockCh.getAlerts()[0]
	if strings.Contains(a.Title+"\n"+a.Message, token) {
		t.Errorf("alert text holds the token: %s", a.Message)
	}
	if !strings.Contains(a.Message, "<redacted>") {
		t.Errorf("alert message lacks <redacted>: %s", a.Message)
	}
	if got := a.Metadata["detail"]; strings.Contains(got, token) || !strings.Contains(got, "<redacted>") {
		t.Errorf("alert metadata detail = %q, want the token replaced with <redacted>", got)
	}
}
