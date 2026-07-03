package alerts

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// blockingChannel blocks in Send until release is closed (with a 2s safety
// timeout so it can never hang the suite). Used to prove a hung channel cannot
// block the event loop (E1 / TASK-337).
type blockingChannel struct {
	name    string
	release chan struct{}
	mu      sync.Mutex
	count   int
}

func (b *blockingChannel) Name() string { return b.name }
func (b *blockingChannel) Type() string { return "webhook" }
func (b *blockingChannel) Send(ctx context.Context, _ *Alert) error {
	b.mu.Lock()
	b.count++
	b.mu.Unlock()
	select {
	case <-b.release:
	case <-ctx.Done():
	case <-time.After(2 * time.Second): // safety: never hang the test suite
	}
	return nil
}

func mkTask337Engine(t *testing.T, suppress bool, ch Channel) *Engine {
	t.Helper()
	config := &AlertConfig{
		Enabled:  true,
		Channels: []ChannelConfig{{Name: ch.Name(), Type: ch.Type(), Enabled: true}},
		Rules: []AlertRule{{
			Name: "task_failed", Type: AlertTypeTaskFailed, Severity: SeverityWarning,
			Enabled: true, Channels: []string{ch.Name()},
		}},
		Defaults: AlertDefaults{SuppressDuplicates: suppress},
	}
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(ch)
	return NewEngine(config, WithDispatcher(dispatcher))
}

// TestFireAlert_SuppressDuplicates verifies that identical {rule|source|message}
// alerts are coalesced within the TTL window when SuppressDuplicates is set (E5).
func TestFireAlert_SuppressDuplicates(t *testing.T) {
	mock := newMockChannel("mock", "webhook")
	engine := mkTask337Engine(t, true, mock)
	rule := engine.config.Rules[0]
	ctx := context.Background()

	mk := func(id, src, msg string) *Alert {
		return &Alert{ID: id, Type: AlertTypeTaskFailed, Severity: SeverityWarning, Source: src, Message: msg, CreatedAt: time.Now()}
	}

	// engine not Started → fireAlert delivers inline (synchronous).
	engine.fireAlert(ctx, rule, mk("1", "task:X", "boom"))  // delivered
	engine.fireAlert(ctx, rule, mk("2", "task:X", "boom"))  // duplicate → suppressed
	engine.fireAlert(ctx, rule, mk("3", "task:X", "other")) // distinct message → delivered
	engine.fireAlert(ctx, rule, mk("4", "task:Y", "boom"))  // distinct source → delivered

	if got := len(mock.getAlerts()); got != 3 {
		t.Fatalf("expected 3 delivered (1 duplicate suppressed), got %d", got)
	}
	if engine.metrics.Snapshot().DroppedTotal != 1 {
		t.Errorf("expected 1 suppressed alert counted as dropped, got %d", engine.metrics.Snapshot().DroppedTotal)
	}
}

// TestFireAlert_SuppressDuplicatesDisabled verifies identical alerts all fire
// when the feature is off (preserving prior behavior).
func TestFireAlert_SuppressDuplicatesDisabled(t *testing.T) {
	mock := newMockChannel("mock", "webhook")
	engine := mkTask337Engine(t, false, mock)
	rule := engine.config.Rules[0]
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		engine.fireAlert(ctx, rule, &Alert{ID: fmt.Sprintf("%d", i), Type: AlertTypeTaskFailed, Severity: SeverityWarning, Source: "task:X", Message: "boom", CreatedAt: time.Now()})
	}
	if got := len(mock.getAlerts()); got != 3 {
		t.Fatalf("expected 3 delivered with suppression off, got %d", got)
	}
}

// TestProcessEvent_PriorityQueueNotStarvedByFlood verifies that a flood of
// ordinary events filling eventCh does not cause critical events to be dropped —
// they ride the independent priority queue (E1).
func TestProcessEvent_PriorityQueueNotStarvedByFlood(t *testing.T) {
	engine := NewEngine(&AlertConfig{Enabled: true}) // not Started → nothing drains the queues

	// Overflow eventCh (cap 100) with ordinary events.
	for i := 0; i < 150; i++ {
		engine.ProcessEvent(Event{Type: EventTypeTaskProgress, TaskID: fmt.Sprintf("t%d", i)})
	}

	// A critical escalation event must still be queued (separate buffer).
	engine.ProcessEvent(Event{Type: EventTypeEscalation, TaskID: "crit"})

	if len(engine.priorityCh) == 0 {
		t.Fatal("escalation event was not enqueued on the priority queue despite eventCh flood")
	}
	if engine.metrics.Snapshot().DroppedTotal == 0 {
		t.Error("expected overflowed ordinary events to be counted as dropped")
	}
}

// TestProcessEvents_HungChannelDoesNotBlockEventLoop verifies that a wedged
// channel delivery (held in the dispatch worker) does not stall the event loop:
// subsequent events are still processed (E1).
func TestProcessEvents_HungChannelDoesNotBlockEventLoop(t *testing.T) {
	release := make(chan struct{})
	bc := &blockingChannel{name: "blocking", release: release}
	engine := mkTask337Engine(t, false, bc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)
	defer engine.Stop()

	// Fire an alert routed to the hung channel → the dispatch worker blocks on Send.
	engine.ProcessEvent(Event{Type: EventTypeTaskFailed, TaskID: "A", Project: "p", Error: "boom", Timestamp: time.Now()})

	// While the worker is wedged, the event loop must keep processing: a later
	// task_started must update engine state within a short bound.
	engine.ProcessEvent(Event{Type: EventTypeTaskStarted, TaskID: "B", Timestamp: time.Now()})

	processed := false
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		engine.mu.RLock()
		_, processed = engine.taskLastProgress["B"]
		engine.mu.RUnlock()
		if processed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(release) // unblock the wedged delivery before cleanup
	if !processed {
		t.Fatal("event loop blocked: task_started for B was not processed while a channel was hung")
	}
	engine.WaitForDispatch()
}

func TestIsHighPriorityEvent(t *testing.T) {
	high := []EventType{EventTypeEscalation, EventTypeOOMKilled, EventTypeBudgetExceeded, EventTypeSecurityEvent, EventTypeConfigError}
	for _, et := range high {
		if !isHighPriorityEvent(et) {
			t.Errorf("%s should be high priority", et)
		}
	}
	normal := []EventType{EventTypeTaskStarted, EventTypeTaskProgress, EventTypeTaskCompleted, EventTypeTaskFailed, EventTypeCostUpdate, EventTypeAutopilotMetrics}
	for _, et := range normal {
		if isHighPriorityEvent(et) {
			t.Errorf("%s should not be high priority", et)
		}
	}
}
