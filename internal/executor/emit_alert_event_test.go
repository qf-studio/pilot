package executor

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// fakeAlertProcessor implements AlertEventProcessor for this test file.
// mockAlertProcessor in runner_integration_test.go is gated behind the
// "integration" build tag and unavailable to default `go test` runs.
type fakeAlertProcessor struct {
	events []AlertEvent
}

func (f *fakeAlertProcessor) ProcessEvent(event AlertEvent) {
	f.events = append(f.events, event)
}

// TestEmitAlertEvent_NilProcessorLogsOnce covers GH-3734: emitAlertEvent must
// warn exactly once when r.alertProcessor is nil, not on every dropped event.
func TestEmitAlertEvent_NilProcessorLogsOnce(t *testing.T) {
	tests := []struct {
		name       string
		calls      []AlertEvent
		wantWarns  int
		wantEvents int // number of ProcessEvent calls expected on a configured processor
	}{
		{
			name: "two events with nil processor logs a single warning",
			calls: []AlertEvent{
				{Type: AlertEventTypeTaskStarted, TaskID: "T-1"},
				{Type: AlertEventTypeTaskFailed, TaskID: "T-1"},
			},
			wantWarns: 1,
		},
		{
			name: "three events with nil processor still logs a single warning",
			calls: []AlertEvent{
				{Type: AlertEventTypeTaskStarted, TaskID: "T-2"},
				{Type: AlertEventTypeTaskProgress, TaskID: "T-2"},
				{Type: AlertEventTypeTaskCompleted, TaskID: "T-2"},
			},
			wantWarns: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := &Runner{log: slog.New(slog.NewTextHandler(&buf, nil))}

			for _, event := range tt.calls {
				r.emitAlertEvent(event)
			}

			gotWarns := strings.Count(buf.String(), "Alert processor not configured")
			if gotWarns != tt.wantWarns {
				t.Errorf("emitAlertEvent() logged %d warnings, want %d; log output:\n%s", gotWarns, tt.wantWarns, buf.String())
			}
		})
	}
}

// TestEmitAlertEvent_ConfiguredProcessorNeverWarns ensures the warn path is
// scoped to the nil-processor branch: a configured processor should receive
// every event and never trigger the drop warning.
func TestEmitAlertEvent_ConfiguredProcessorNeverWarns(t *testing.T) {
	var buf bytes.Buffer
	processor := &fakeAlertProcessor{}
	r := &Runner{
		log:            slog.New(slog.NewTextHandler(&buf, nil)),
		alertProcessor: processor,
	}

	events := []AlertEvent{
		{Type: AlertEventTypeTaskStarted, TaskID: "T-3"},
		{Type: AlertEventTypeTaskFailed, TaskID: "T-3"},
	}
	for _, event := range events {
		r.emitAlertEvent(event)
	}

	if len(processor.events) != len(events) {
		t.Fatalf("processor received %d events, want %d", len(processor.events), len(events))
	}
	if strings.Contains(buf.String(), "Alert processor not configured") {
		t.Errorf("emitAlertEvent() logged a drop warning despite a configured processor; log output:\n%s", buf.String())
	}
}
