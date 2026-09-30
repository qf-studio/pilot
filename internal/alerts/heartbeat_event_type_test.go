package alerts

import (
	"testing"

	"github.com/qf-studio/pilot/internal/executor"
)

// TestHeartbeatTimeoutEventTypeMatchesExecutor pins the GH-5498 contract:
// EngineAdapter casts the executor's AlertEventType string straight to
// EventType, so the two constants must stay equal. (The pin lives here
// because alerts imports executor, not the reverse.)
func TestHeartbeatTimeoutEventTypeMatchesExecutor(t *testing.T) {
	if string(executor.AlertEventTypeHeartbeatTimeout) != string(EventTypeHeartbeatTimeout) {
		t.Errorf("executor.AlertEventTypeHeartbeatTimeout = %q, alerts.EventTypeHeartbeatTimeout = %q",
			executor.AlertEventTypeHeartbeatTimeout, EventTypeHeartbeatTimeout)
	}
}

// TestTaskTimeoutAndWatchdogKillEventTypesMatchExecutor pins the GH-5500
// contract for the same EngineAdapter string cast: editing either side alone
// makes the events silently drop.
func TestTaskTimeoutAndWatchdogKillEventTypesMatchExecutor(t *testing.T) {
	pairs := []struct {
		name     string
		executor executor.AlertEventType
		engine   EventType
	}{
		{"task_timeout", executor.AlertEventTypeTaskTimeout, EventTypeTaskTimeout},
		{"watchdog_kill", executor.AlertEventTypeWatchdogKill, EventTypeWatchdogKill},
	}
	for _, p := range pairs {
		if string(p.executor) != string(p.engine) {
			t.Errorf("%s: executor = %q, alerts = %q", p.name, p.executor, p.engine)
		}
	}
}
