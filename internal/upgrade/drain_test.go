package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeClock is a deterministic clock for testing waitForDrain's poll loop
// without real sleeps. Each After(d) call advances Now() by d immediately
// and fires right away, so a bounded timeout resolves in a fixed number of
// loop iterations instead of wall-clock time.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	f.now = f.now.Add(d)
	now := f.now
	f.mu.Unlock()

	ch := make(chan time.Time, 1)
	ch <- now
	return ch
}

// ---------------------------------------------------------------------------
// DrainStatus persistence
// ---------------------------------------------------------------------------

func TestDrainStatus_SaveAndLoad_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drain-status.json")

	original := &DrainStatus{
		PID:           1234,
		Draining:      true,
		InFlightCount: 2,
		UpdatedAt:     time.Now().Truncate(time.Second),
	}

	if err := original.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := LoadDrainStatus(path)
	if err != nil {
		t.Fatalf("LoadDrainStatus() error = %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded status is nil")
	}
	if loaded.PID != original.PID {
		t.Errorf("PID = %d, want %d", loaded.PID, original.PID)
	}
	if loaded.Draining != original.Draining {
		t.Errorf("Draining = %v, want %v", loaded.Draining, original.Draining)
	}
	if loaded.InFlightCount != original.InFlightCount {
		t.Errorf("InFlightCount = %d, want %d", loaded.InFlightCount, original.InFlightCount)
	}
}

func TestLoadDrainStatus_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")

	status, err := LoadDrainStatus(path)
	if err != nil {
		t.Fatalf("LoadDrainStatus() error = %v, want nil for missing file", err)
	}
	if status != nil {
		t.Errorf("status = %+v, want nil for missing file", status)
	}
}

func TestLoadDrainStatus_CorruptedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadDrainStatus(path)
	if err == nil {
		t.Fatal("LoadDrainStatus() expected error for corrupted file, got nil")
	}
}

func TestDrainStatus_SaveCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "drain-status.json")

	status := &DrainStatus{PID: 1, Draining: true}
	if err := status.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := LoadDrainStatus(path)
	if err != nil {
		t.Fatalf("LoadDrainStatus() error = %v", err)
	}
	if loaded == nil || !loaded.Draining {
		t.Errorf("loaded = %+v, want Draining=true", loaded)
	}
}

func TestReportDrainStatus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drain-status.json")

	if err := ReportDrainStatus(path, 999, true, 3); err != nil {
		t.Fatalf("ReportDrainStatus() error = %v", err)
	}

	loaded, err := LoadDrainStatus(path)
	if err != nil {
		t.Fatalf("LoadDrainStatus() error = %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded status is nil")
	}
	if loaded.PID != 999 || !loaded.Draining || loaded.InFlightCount != 3 {
		t.Errorf("loaded = %+v, want {PID:999 Draining:true InFlightCount:3}", loaded)
	}
}

// ---------------------------------------------------------------------------
// DrainOutcome
// ---------------------------------------------------------------------------

func TestDrainOutcome_String(t *testing.T) {
	tests := []struct {
		outcome DrainOutcome
		want    string
	}{
		{Drained, "drained"},
		{TimedOut, "timed_out"},
		{DrainUnknown, "unknown"},
	}
	for _, tt := range tests {
		if got := tt.outcome.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// waitForDrain poll loop (fake clock — no real sleeping)
// ---------------------------------------------------------------------------

func TestWaitForDrain_DrainedImmediately(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drain-status.json")

	status := &DrainStatus{PID: 1, Draining: true, InFlightCount: 0}
	if err := status.Save(path); err != nil {
		t.Fatal(err)
	}

	clk := newFakeClock(time.Now())
	outcome, err := waitForDrain(context.Background(), path, 5*time.Second, 100*time.Millisecond, clk)
	if err != nil {
		t.Fatalf("waitForDrain() error = %v", err)
	}
	if outcome != Drained {
		t.Errorf("outcome = %v, want Drained", outcome)
	}
}

func TestWaitForDrain_DrainedAfterFewPolls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drain-status.json")

	// Not draining yet — still has an in-flight execution.
	status := &DrainStatus{PID: 1, Draining: true, InFlightCount: 1}
	if err := status.Save(path); err != nil {
		t.Fatal(err)
	}

	// This test exercises real concurrent updates against a real-time poll
	// loop, so it uses realClock rather than fakeClock: fakeClock's virtual
	// time advances instantly on every poll tick, decoupled from wall-clock
	// time, which would race ahead of the background goroutine's real sleep
	// below and time out before the status file is ever updated.
	done := make(chan struct{})
	go func() {
		// Simulate the target process finishing its last execution
		// shortly after being signaled.
		time.Sleep(20 * time.Millisecond)
		final := &DrainStatus{PID: 1, Draining: true, InFlightCount: 0}
		_ = final.Save(path)
		close(done)
	}()

	outcome, err := waitForDrain(context.Background(), path, 2*time.Second, 5*time.Millisecond, realClock{})
	<-done
	if err != nil {
		t.Fatalf("waitForDrain() error = %v", err)
	}
	if outcome != Drained {
		t.Errorf("outcome = %v, want Drained", outcome)
	}
}

func TestWaitForDrain_TimedOut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drain-status.json")

	// Perpetually busy — never reports zero in-flight.
	status := &DrainStatus{PID: 1, Draining: true, InFlightCount: 4}
	if err := status.Save(path); err != nil {
		t.Fatal(err)
	}

	clk := newFakeClock(time.Now())
	outcome, err := waitForDrain(context.Background(), path, 1*time.Second, 100*time.Millisecond, clk)
	if err != nil {
		t.Fatalf("waitForDrain() error = %v", err)
	}
	if outcome != TimedOut {
		t.Errorf("outcome = %v, want TimedOut", outcome)
	}
}

func TestWaitForDrain_NoStatusFileYet_TimesOut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "never-written.json")

	clk := newFakeClock(time.Now())
	outcome, err := waitForDrain(context.Background(), path, 1*time.Second, 100*time.Millisecond, clk)
	if err != nil {
		t.Fatalf("waitForDrain() error = %v", err)
	}
	if outcome != TimedOut {
		t.Errorf("outcome = %v, want TimedOut", outcome)
	}
}

func TestWaitForDrain_NotDrainingIgnoresZeroInFlight(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drain-status.json")

	// An idle process that hasn't been signaled yet may happen to have
	// InFlightCount == 0 — that must not read as "drained" without
	// Draining also being true.
	status := &DrainStatus{PID: 1, Draining: false, InFlightCount: 0}
	if err := status.Save(path); err != nil {
		t.Fatal(err)
	}

	clk := newFakeClock(time.Now())
	outcome, err := waitForDrain(context.Background(), path, 1*time.Second, 100*time.Millisecond, clk)
	if err != nil {
		t.Fatalf("waitForDrain() error = %v", err)
	}
	if outcome != TimedOut {
		t.Errorf("outcome = %v, want TimedOut", outcome)
	}
}

func TestWaitForDrain_ContextCancelled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drain-status.json")

	status := &DrainStatus{PID: 1, Draining: true, InFlightCount: 1}
	if err := status.Save(path); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	clk := newFakeClock(time.Now())
	outcome, err := waitForDrain(ctx, path, 5*time.Second, 100*time.Millisecond, clk)
	if err == nil {
		t.Fatal("waitForDrain() expected error for cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if outcome != DrainUnknown {
		t.Errorf("outcome = %v, want DrainUnknown", outcome)
	}
}

func TestWaitForDrain_LoadErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}

	clk := newFakeClock(time.Now())
	outcome, err := waitForDrain(context.Background(), path, 1*time.Second, 100*time.Millisecond, clk)
	if err == nil {
		t.Fatal("waitForDrain() expected error for corrupted status file, got nil")
	}
	if outcome != DrainUnknown {
		t.Errorf("outcome = %v, want DrainUnknown", outcome)
	}
}

// ---------------------------------------------------------------------------
// DrainConfig defaults
// ---------------------------------------------------------------------------

func TestDrainConfig_WithDefaults_NilConfig(t *testing.T) {
	var cfg *DrainConfig
	resolved := cfg.withDefaults()

	if resolved.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", resolved.Timeout)
	}
	if resolved.PollInterval != 500*time.Millisecond {
		t.Errorf("PollInterval = %v, want 500ms", resolved.PollInterval)
	}
	if resolved.StatusPath != DefaultDrainStatusPath() {
		t.Errorf("StatusPath = %q, want %q", resolved.StatusPath, DefaultDrainStatusPath())
	}
}

func TestDrainConfig_WithDefaults_PartialOverride(t *testing.T) {
	cfg := &DrainConfig{Timeout: 2 * time.Second}
	resolved := cfg.withDefaults()

	if resolved.Timeout != 2*time.Second {
		t.Errorf("Timeout = %v, want 2s", resolved.Timeout)
	}
	if resolved.PollInterval != 500*time.Millisecond {
		t.Errorf("PollInterval = %v, want default 500ms", resolved.PollInterval)
	}
}

// GracefulUpgrader.RequestDrain's signal-delivery half depends on SignalDrain,
// which is platform-specific (drain_unix.go / drain_windows.go) — see
// drain_unix_test.go for the live signal+poll integration test.
