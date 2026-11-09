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
