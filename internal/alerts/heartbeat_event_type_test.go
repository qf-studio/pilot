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
