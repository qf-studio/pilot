package executor

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestHeartbeatTimeoutCallback_EmitsAlert covers GH-5498: the callback wired
// into ExecuteOptions.HeartbeatCallback must emit exactly one heartbeat_timeout
// alert event carrying pid and whole-minute idle_minutes metadata.
func TestHeartbeatTimeoutCallback_EmitsAlert(t *testing.T) {
	var buf bytes.Buffer
	processor := &fakeAlertProcessor{}
	r := &Runner{
		log:            slog.New(slog.NewTextHandler(&buf, nil)),
		alertProcessor: processor,
	}
	task := &Task{ID: "GH-5498", Title: "hang", ProjectPath: "/proj"}

	r.heartbeatTimeoutCallback(task)(4242, 5*time.Minute+20*time.Second)

	// reportProgress also emits a task_progress event; only count ours.
	var hb []AlertEvent
	for _, e := range processor.events {
		if e.Type == AlertEventTypeHeartbeatTimeout {
			hb = append(hb, e)
		}
	}
	if len(hb) != 1 {
		t.Fatalf("expected exactly 1 heartbeat_timeout event, got %d (all events: %+v)", len(hb), processor.events)
	}
	ev := hb[0]
	if ev.Type != AlertEventTypeHeartbeatTimeout {
		t.Errorf("type = %q, want %q", ev.Type, AlertEventTypeHeartbeatTimeout)
	}
	if ev.TaskID != "GH-5498" || ev.TaskTitle != "hang" || ev.Project != "/proj" {
		t.Errorf("unexpected task fields: %+v", ev)
	}
	if ev.Metadata["pid"] != "4242" {
		t.Errorf("pid = %q, want 4242", ev.Metadata["pid"])
	}
	if ev.Metadata["idle_minutes"] != "5" {
		t.Errorf("idle_minutes = %q, want 5", ev.Metadata["idle_minutes"])
	}
	if ev.Metadata["heartbeat_timeout"] != DefaultHeartbeatTimeout.String() {
		t.Errorf("heartbeat_timeout = %q, want %q", ev.Metadata["heartbeat_timeout"], DefaultHeartbeatTimeout)
	}
	if ev.Metadata["last_event_age"] == "" {
		t.Error("last_event_age metadata missing")
	}
	if !strings.Contains(ev.Error, "no stream events for 5m") || !strings.Contains(ev.Error, "process 4242") {
		t.Errorf("unexpected error text: %q", ev.Error)
	}
	if ev.Timestamp.IsZero() {
		t.Error("timestamp not set")
	}
}
