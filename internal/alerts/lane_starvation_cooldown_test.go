package alerts

import (
	"context"
	"strings"
	"testing"
	"time"
)

func laneStarvationCooldownConfig() *AlertConfig {
	return &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []AlertRule{
			{
				Name:      "lane_starvation",
				Type:      AlertTypeLaneStarvation,
				Enabled:   true,
				Condition: RuleCondition{LaneStarvationPollCycles: 3},
				Severity:  SeverityWarning,
				Channels:  []string{"test-channel"},
				Cooldown:  30 * time.Minute,
			},
		},
	}
}

func laneStarvationCooldownEvent(repo string) Event {
	return Event{
		Type:    EventTypeLaneStarvation,
		Project: repo,
		Metadata: map[string]string{
			"repo":                repo,
			"project_path":        "/" + repo,
			"open_issue_count":    "1",
			"open_issue_numbers":  "#7",
			"poll_cycles_starved": "3",
		},
		Timestamp: time.Now(),
	}
}

// TestLaneStarvationCooldown_PerLane pins GH-6391: the lane_starvation
// cooldown is kept per lane (rule name + event project), so a second lane
// starving inside the first lane's window still alerts, while one lane
// alerts once inside its own window.
func TestLaneStarvationCooldown_PerLane(t *testing.T) {
	tests := []struct {
		name       string
		repos      []string
		wantAlerts int
	}{
		{name: "two lanes inside one window both alert", repos: []string{"owner/a", "owner/b"}, wantAlerts: 2},
		{name: "one lane alerts once inside the window", repos: []string{"owner/a", "owner/a"}, wantAlerts: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := laneStarvationCooldownConfig()
			mockCh := newMockChannel("test-channel", "webhook")
			dispatcher := NewDispatcher(config)
			dispatcher.RegisterChannel(mockCh)
			engine := NewEngine(config, WithDispatcher(dispatcher))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_ = engine.Start(ctx)

			for _, repo := range tt.repos {
				engine.ProcessEvent(laneStarvationCooldownEvent(repo))
				engine.flushForTest()
			}
			engine.WaitForDispatch()

			got := mockCh.getAlerts()
			if len(got) != tt.wantAlerts {
				t.Fatalf("expected %d alerts, got %d", tt.wantAlerts, len(got))
			}
			for _, a := range got {
				if !strings.Contains(a.Message, "#7") {
					t.Errorf("expected alert text to name the counted issue #7, got %q", a.Message)
				}
			}
		})
	}
}

// TestLaneStarvationCooldown_AlertStatus pins the evidence query the
// emitter logs from: reached only at the threshold, due while the lane's
// own cooldown is clear, and another lane's fire does not make it not due.
func TestLaneStarvationCooldown_AlertStatus(t *testing.T) {
	engine := NewEngine(laneStarvationCooldownConfig())
	rule := engine.config.Rules[0]
	engine.lastAlertTimes[cooldownKey(rule, "owner/b")] = time.Now()

	tests := []struct {
		name        string
		project     string
		streak      int
		wantReached bool
		wantDue     bool
	}{
		{name: "below threshold", project: "owner/a", streak: 2},
		{name: "at threshold", project: "owner/a", streak: 3, wantReached: true, wantDue: true},
		{name: "past threshold", project: "owner/a", streak: 4, wantDue: true},
		{name: "own lane in cooldown", project: "owner/b", streak: 3, wantReached: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached, due := engine.LaneStarvationAlertStatus(tt.project, tt.streak)
			if reached != tt.wantReached || due != tt.wantDue {
				t.Errorf("LaneStarvationAlertStatus(%q, %d) = (%v, %v), want (%v, %v)",
					tt.project, tt.streak, reached, due, tt.wantReached, tt.wantDue)
			}
		})
	}
}

// TestCooldownKey pins that only lane_starvation keys on the project; every
// other rule — including the persisted config-error rows rehydrateActiveAlerts
// seeds — keeps the rule-name key.
func TestCooldownKey(t *testing.T) {
	tests := []struct {
		name    string
		rule    AlertRule
		project string
		want    string
	}{
		{name: "lane starvation keys on project", rule: AlertRule{Name: "lane_starvation", Type: AlertTypeLaneStarvation}, project: "owner/a", want: "lane_starvation|owner/a"},
		{name: "other rule keys on name", rule: AlertRule{Name: "config-error", Type: AlertTypeServiceUnhealthy}, project: "owner/a", want: "config-error"},
		{name: "other rule without project", rule: AlertRule{Name: "task_failed", Type: AlertTypeTaskFailed}, want: "task_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cooldownKey(tt.rule, tt.project); got != tt.want {
				t.Errorf("cooldownKey = %q, want %q", got, tt.want)
			}
		})
	}
}
