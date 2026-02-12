package executor

import (
	"testing"
	"time"
)

func TestAlertEventTypes(t *testing.T) {
	// Verify all expected event types exist
	eventTypes := []AlertEventType{
		AlertEventTypeTaskStarted,
		AlertEventTypeTaskProgress,
		AlertEventTypeTaskCompleted,
		AlertEventTypeTaskFailed,
		AlertEventTypeTaskRetry,
		AlertEventTypeTaskTimeout,
		AlertEventTypeHeartbeatTimeout,
		AlertEventTypeWatchdogKill,
	}

	expectedValues := []string{
		"task_started",
		"task_progress",
		"task_completed",
		"task_failed",
		"task_retry",
		"task_timeout",
		"heartbeat_timeout",
		"watchdog_kill",
	}

	for i, et := range eventTypes {
		if string(et) != expectedValues[i] {
			t.Errorf("AlertEventType %d = %q, want %q", i, et, expectedValues[i])
		}
	}
}

func TestAlertEventStructure(t *testing.T) {
	event := AlertEvent{
		Type:      AlertEventTypeTaskTimeout,
		TaskID:    "GH-148",
		TaskTitle: "Test timeout",
		Project:   "/path/to/project",
		Phase:     "Implementing",
		Progress:  50,
		Error:     "task timed out after 5m",
		Metadata: map[string]string{
			"complexity": "medium",
			"timeout":    "5m0s",
		},
		Timestamp: time.Now(),
	}

	if event.Type != AlertEventTypeTaskTimeout {
		t.Errorf("Type = %v, want %v", event.Type, AlertEventTypeTaskTimeout)
	}
	if event.TaskID != "GH-148" {
		t.Errorf("TaskID = %q, want GH-148", event.TaskID)
	}
	if event.Metadata["complexity"] != "medium" {
		t.Errorf("Metadata[complexity] = %q, want medium", event.Metadata["complexity"])
	}
}
