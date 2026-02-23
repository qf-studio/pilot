package alerts

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// Dispatcher Creation Tests
// =============================================================================

func TestNewDispatcher(t *testing.T) {
	config := &AlertConfig{Enabled: true}
	d := NewDispatcher(config)

	if d == nil {
		t.Fatal("expected non-nil dispatcher")
	}
	if d.config != config {
		t.Error("expected config to be set")
	}
	if d.channels == nil {
		t.Error("expected channels map to be initialized")
	}
	if d.logger == nil {
		t.Error("expected default logger")
	}
}

func TestNewDispatcher_WithLogger(t *testing.T) {
	config := &AlertConfig{Enabled: true}
	logger := slog.Default()
	d := NewDispatcher(config, WithDispatcherLogger(logger))

	if d.logger != logger {
		t.Error("expected custom logger to be set")
	}
}

// =============================================================================
// Channel Registration Tests
// =============================================================================

func TestDispatcher_RegisterChannel(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})
	ch := newMockChannel("test-ch", "mock")

	d.RegisterChannel(ch)

	got, ok := d.GetChannel("test-ch")
	if !ok {
		t.Fatal("expected channel to be registered")
	}
	if got.Name() != "test-ch" {
		t.Errorf("expected name 'test-ch', got '%s'", got.Name())
	}
}

func TestDispatcher_UnregisterChannel(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})
	ch := newMockChannel("remove-me", "mock")

	d.RegisterChannel(ch)
	d.UnregisterChannel("remove-me")

	_, ok := d.GetChannel("remove-me")
	if ok {
		t.Error("expected channel to be unregistered")
	}
}

func TestDispatcher_GetChannel_NotFound(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})

	_, ok := d.GetChannel("nonexistent")
	if ok {
		t.Error("expected channel not found")
	}
}

func TestDispatcher_ListChannels_AllRegistered(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})
	d.RegisterChannel(newMockChannel("ch-a", "mock"))
	d.RegisterChannel(newMockChannel("ch-b", "mock"))
	d.RegisterChannel(newMockChannel("ch-c", "mock"))

	names := d.ListChannels()
	if len(names) != 3 {
		t.Fatalf("expected 3 channels, got %d", len(names))
	}

	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	for _, expected := range []string{"ch-a", "ch-b", "ch-c"} {
		if !nameSet[expected] {
			t.Errorf("expected channel %q in list", expected)
		}
	}
}

func TestDispatcher_ListChannels_Empty(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})
	names := d.ListChannels()
	if len(names) != 0 {
		t.Errorf("expected 0 channels, got %d", len(names))
	}
}

// =============================================================================
// Dispatch Tests
// =============================================================================

func TestDispatcher_Dispatch_SingleChannelSuccess(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})
	ch := newMockChannel("slack", "slack")
	d.RegisterChannel(ch)

	alert := &Alert{
		ID:       "alert-1",
		Type:     AlertTypeTaskFailed,
		Severity: SeverityWarning,
		Title:    "Task Failed",
		Message:  "Something broke",
	}

	results := d.Dispatch(context.Background(), alert, []string{"slack"})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Success {
		t.Errorf("expected success, got error: %v", results[0].Error)
	}
	if results[0].ChannelName != "slack" {
		t.Errorf("expected channel name 'slack', got '%s'", results[0].ChannelName)
	}

	alerts := ch.getAlerts()
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert sent, got %d", len(alerts))
	}
	if alerts[0].ID != "alert-1" {
		t.Errorf("expected alert ID 'alert-1', got '%s'", alerts[0].ID)
	}
}

func TestDispatcher_Dispatch_MultiChannel_PartialFailure(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})

	successCh := newMockChannel("success-ch", "mock")
	failCh := newMockChannel("fail-ch", "mock")
	failCh.setError(errors.New("send failed"))
	successCh2 := newMockChannel("success-ch-2", "mock")

	d.RegisterChannel(successCh)
	d.RegisterChannel(failCh)
	d.RegisterChannel(successCh2)

	alert := &Alert{ID: "alert-2", Type: AlertTypeTaskFailed, Severity: SeverityCritical}

	results := d.Dispatch(context.Background(), alert, []string{"success-ch", "fail-ch", "success-ch-2"})

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	successCount := 0
	failCount := 0
	for _, r := range results {
		if r.Success {
			successCount++
		} else {
			failCount++
		}
	}

	if successCount != 2 {
		t.Errorf("expected 2 successes, got %d", successCount)
	}
	if failCount != 1 {
		t.Errorf("expected 1 failure, got %d", failCount)
	}
}

func TestDispatcher_Dispatch_AllChannelsFail(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})

	ch1 := newMockChannel("ch1", "mock")
	ch1.setError(errors.New("fail-1"))
	ch2 := newMockChannel("ch2", "mock")
	ch2.setError(errors.New("fail-2"))

	d.RegisterChannel(ch1)
	d.RegisterChannel(ch2)

	alert := &Alert{ID: "alert-3", Severity: SeverityCritical}

	results := d.Dispatch(context.Background(), alert, []string{"ch1", "ch2"})

	for _, r := range results {
		if r.Success {
			t.Errorf("expected failure for channel %s", r.ChannelName)
		}
		if r.Error == nil {
			t.Errorf("expected error for channel %s", r.ChannelName)
		}
	}
}

func TestDispatcher_Dispatch_ChannelNotFound(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})
	d.RegisterChannel(newMockChannel("existing", "mock"))

	alert := &Alert{ID: "alert-4"}
	results := d.Dispatch(context.Background(), alert, []string{"nonexistent"})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Success {
		t.Error("expected failure for nonexistent channel")
	}
	if !errors.Is(results[0].Error, ErrChannelNotFound) {
		t.Errorf("expected ErrChannelNotFound, got %v", results[0].Error)
	}
}

func TestDispatcher_Dispatch_MixedFoundAndNotFound(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})
	ch := newMockChannel("found", "mock")
	d.RegisterChannel(ch)

	alert := &Alert{ID: "alert-5"}
	results := d.Dispatch(context.Background(), alert, []string{"found", "missing"})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	foundResult := false
	missingResult := false
	for _, r := range results {
		if r.ChannelName == "found" && r.Success {
			foundResult = true
		}
		if r.ChannelName == "missing" && !r.Success {
			missingResult = true
		}
	}
	if !foundResult {
		t.Error("expected success for 'found' channel")
	}
	if !missingResult {
		t.Error("expected failure for 'missing' channel")
	}
}

func TestDispatcher_Dispatch_EmptyChannelList(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})
	alert := &Alert{ID: "alert-6"}

	results := d.Dispatch(context.Background(), alert, []string{})
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestDispatcher_Dispatch_ContextCancellation(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})

	// Channel that blocks until context is cancelled
	slowCh := &slowMockChannel{
		name:  "slow",
		typ:   "mock",
		delay: 5 * time.Second,
	}
	d.RegisterChannel(slowCh)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	alert := &Alert{ID: "alert-ctx"}
	results := d.Dispatch(ctx, alert, []string{"slow"})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	// The channel should get a context error since sendToChannel wraps with timeout
	// but the parent context is already cancelled
	if results[0].Success {
		t.Error("expected failure due to cancelled context")
	}
}

// =============================================================================
// DispatchAll Tests
// =============================================================================

func TestDispatcher_DispatchAll_EnabledChannelsOnly(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "enabled-ch", Type: "mock", Enabled: true},
			{Name: "disabled-ch", Type: "mock", Enabled: false},
		},
	}

	d := NewDispatcher(config)
	enabledCh := newMockChannel("enabled-ch", "mock")
	disabledCh := newMockChannel("disabled-ch", "mock")
	d.RegisterChannel(enabledCh)
	d.RegisterChannel(disabledCh)

	alert := &Alert{ID: "alert-all", Severity: SeverityWarning}
	results := d.DispatchAll(context.Background(), alert)

	if len(results) != 1 {
		t.Fatalf("expected 1 result (only enabled), got %d", len(results))
	}
	if results[0].ChannelName != "enabled-ch" {
		t.Errorf("expected 'enabled-ch', got '%s'", results[0].ChannelName)
	}
}

func TestDispatcher_DispatchAll_SeverityFiltering(t *testing.T) {
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{
				Name:       "critical-only",
				Type:       "mock",
				Enabled:    true,
				Severities: []Severity{SeverityCritical},
			},
			{
				Name:       "all-severities",
				Type:       "mock",
				Enabled:    true,
				Severities: []Severity{}, // empty = accept all
			},
		},
	}

	d := NewDispatcher(config)
	criticalCh := newMockChannel("critical-only", "mock")
	allCh := newMockChannel("all-severities", "mock")
	d.RegisterChannel(criticalCh)
	d.RegisterChannel(allCh)

	// Send info alert — should only go to "all-severities"
	infoAlert := &Alert{ID: "info-alert", Severity: SeverityInfo}
	results := d.DispatchAll(context.Background(), infoAlert)

	if len(results) != 1 {
		t.Fatalf("expected 1 result for info alert, got %d", len(results))
	}
	if results[0].ChannelName != "all-severities" {
		t.Errorf("expected 'all-severities', got '%s'", results[0].ChannelName)
	}

	// Send critical alert — should go to both
	critAlert := &Alert{ID: "crit-alert", Severity: SeverityCritical}
	results = d.DispatchAll(context.Background(), critAlert)

	if len(results) != 2 {
		t.Fatalf("expected 2 results for critical alert, got %d", len(results))
	}
}

func TestDispatcher_DispatchAll_NoChannels(t *testing.T) {
	config := &AlertConfig{Enabled: true, Channels: []ChannelConfig{}}
	d := NewDispatcher(config)

	alert := &Alert{ID: "alert-empty"}
	results := d.DispatchAll(context.Background(), alert)

	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

// =============================================================================
// Severity Matching Tests
// =============================================================================

func TestDispatcher_SeverityMatches_TableDriven(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})

	tests := []struct {
		name       string
		severities []Severity
		alert      Severity
		want       bool
	}{
		{"empty filter accepts all", []Severity{}, SeverityInfo, true},
		{"nil filter accepts all", nil, SeverityCritical, true},
		{"matches critical", []Severity{SeverityCritical}, SeverityCritical, true},
		{"no match", []Severity{SeverityCritical}, SeverityInfo, false},
		{"multi-severity match", []Severity{SeverityWarning, SeverityCritical}, SeverityWarning, true},
		{"multi-severity no match", []Severity{SeverityWarning, SeverityCritical}, SeverityInfo, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.severityMatches(tt.severities, tt.alert)
			if got != tt.want {
				t.Errorf("severityMatches(%v, %s) = %v, want %v", tt.severities, tt.alert, got, tt.want)
			}
		})
	}
}

// =============================================================================
// Concurrent Access Tests
// =============================================================================

func TestDispatcher_ConcurrentRegistration(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ch := newMockChannel(
				"ch-"+string(rune('A'+idx%26)),
				"mock",
			)
			d.RegisterChannel(ch)
		}(i)
	}
	wg.Wait()

	// Should not panic or deadlock
	names := d.ListChannels()
	if len(names) == 0 {
		t.Error("expected some channels to be registered")
	}
}

func TestDispatcher_ConcurrentDispatch(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})
	ch := newMockChannel("concurrent-ch", "mock")
	d.RegisterChannel(ch)

	alert := &Alert{ID: "concurrent", Severity: SeverityInfo}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results := d.Dispatch(context.Background(), alert, []string{"concurrent-ch"})
			if len(results) != 1 {
				t.Errorf("expected 1 result, got %d", len(results))
			}
		}()
	}
	wg.Wait()

	alerts := ch.getAlerts()
	if len(alerts) != 20 {
		t.Errorf("expected 20 alerts dispatched, got %d", len(alerts))
	}
}

// =============================================================================
// ChannelError Tests
// =============================================================================

func TestChannelError_Message(t *testing.T) {
	err := &ChannelError{Message: "test error"}
	if err.Error() != "test error" {
		t.Errorf("expected 'test error', got '%s'", err.Error())
	}
}

func TestErrChannelNotFound(t *testing.T) {
	if ErrChannelNotFound == nil {
		t.Fatal("ErrChannelNotFound should not be nil")
	}
	if ErrChannelNotFound.Error() != "channel not found" {
		t.Errorf("expected 'channel not found', got '%s'", ErrChannelNotFound.Error())
	}
}

// =============================================================================
// sendToChannel Tests
// =============================================================================

func TestDispatcher_SendToChannel_SetsTimestamp(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})
	ch := newMockChannel("ts-ch", "mock")

	before := time.Now()
	result := d.sendToChannel(context.Background(), ch, &Alert{ID: "ts-test"})
	after := time.Now()

	if result.SentAt.Before(before) || result.SentAt.After(after) {
		t.Error("expected SentAt to be within test bounds")
	}
}

func TestDispatcher_SendToChannel_ErrorResult(t *testing.T) {
	d := NewDispatcher(&AlertConfig{})
	ch := newMockChannel("err-ch", "mock")
	ch.setError(errors.New("send error"))

	result := d.sendToChannel(context.Background(), ch, &Alert{ID: "err-test"})

	if result.Success {
		t.Error("expected failure")
	}
	if result.Error == nil {
		t.Error("expected error to be set")
	}
}

// =============================================================================
// Helpers
// =============================================================================

// slowMockChannel simulates a slow channel for timeout testing.
type slowMockChannel struct {
	name  string
	typ   string
	delay time.Duration
}

func (s *slowMockChannel) Name() string { return s.name }
func (s *slowMockChannel) Type() string { return s.typ }
func (s *slowMockChannel) Send(ctx context.Context, alert *Alert) error {
	select {
	case <-time.After(s.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
