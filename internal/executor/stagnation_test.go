package executor

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestStagnationLevel_String(t *testing.T) {
	tests := []struct {
		level    StagnationLevel
		expected string
	}{
		{StagnationNone, "none"},
		{StagnationWarn, "warn"},
		{StagnationPause, "pause"},
		{StagnationAbort, "abort"},
		{StagnationLevel(99), "unknown(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.level.String(); got != tt.expected {
				t.Errorf("StagnationLevel(%d).String() = %q, want %q", tt.level, got, tt.expected)
			}
		})
	}
}

func TestDefaultStagnationConfig(t *testing.T) {
	cfg := DefaultStagnationConfig()

	if cfg.WarnAfterIdentical != 3 {
		t.Errorf("WarnAfterIdentical = %d, want 3", cfg.WarnAfterIdentical)
	}
	if cfg.PauseAfterIdentical != 5 {
		t.Errorf("PauseAfterIdentical = %d, want 5", cfg.PauseAfterIdentical)
	}
	if cfg.WarnAfterNoProgress != 10*time.Minute {
		t.Errorf("WarnAfterNoProgress = %v, want 10m", cfg.WarnAfterNoProgress)
	}
	if cfg.PauseAfterNoProgress != 20*time.Minute {
		t.Errorf("PauseAfterNoProgress = %v, want 20m", cfg.PauseAfterNoProgress)
	}
	if cfg.AbortAfterNoProgress != 30*time.Minute {
		t.Errorf("AbortAfterNoProgress = %v, want 30m", cfg.AbortAfterNoProgress)
	}
	if cfg.MaxIterations != 10 {
		t.Errorf("MaxIterations = %d, want 10", cfg.MaxIterations)
	}
	if cfg.HistorySize != 20 {
		t.Errorf("HistorySize = %d, want 20", cfg.HistorySize)
	}
}

func TestNewStagnationMonitor_Defaults(t *testing.T) {
	// With nil config and logger
	m := NewStagnationMonitor(nil, nil)

	if m.config == nil {
		t.Fatal("config should not be nil")
	}
	if m.log == nil {
		t.Fatal("log should not be nil")
	}
	if m.currentLevel != StagnationNone {
		t.Errorf("initial level = %v, want StagnationNone", m.currentLevel)
	}
	if m.lastProgress != -1 {
		t.Errorf("lastProgress = %d, want -1", m.lastProgress)
	}
}

func TestStagnationMonitor_RecordState_Progress(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	m := NewStagnationMonitor(nil, logger)

	// First state - should be no stagnation
	level := m.RecordState("INIT", 0, 1)
	if level != StagnationNone {
		t.Errorf("first state: level = %v, want StagnationNone", level)
	}

	// Progress increase - should remain no stagnation
	level = m.RecordState("INIT", 25, 1)
	if level != StagnationNone {
		t.Errorf("progress increase: level = %v, want StagnationNone", level)
	}

	// Phase change - should remain no stagnation
	level = m.RecordState("RESEARCH", 25, 1)
	if level != StagnationNone {
		t.Errorf("phase change: level = %v, want StagnationNone", level)
	}

	// Iteration advance - should remain no stagnation
	level = m.RecordState("RESEARCH", 25, 2)
	if level != StagnationNone {
		t.Errorf("iteration advance: level = %v, want StagnationNone", level)
	}
}

func TestStagnationMonitor_IdenticalStateDetection(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := &StagnationConfig{
		WarnAfterIdentical:   3,
		PauseAfterIdentical:  5,
		WarnAfterNoProgress:  1 * time.Hour, // Large timeout to test hash-based detection
		PauseAfterNoProgress: 2 * time.Hour,
		AbortAfterNoProgress: 3 * time.Hour,
		MaxIterations:        100,
		HistorySize:          20,
	}
	m := NewStagnationMonitor(cfg, logger)

	// Record same state multiple times
	phase, progress, iteration := "IMPL", 50, 3

	// First 2 states - no stagnation (need 3 for warn)
	for i := 0; i < 2; i++ {
		level := m.RecordState(phase, progress, iteration)
		if level != StagnationNone {
			t.Errorf("iteration %d: level = %v, want StagnationNone", i, level)
		}
	}

	// 3rd identical state - should warn
	level := m.RecordState(phase, progress, iteration)
	if level != StagnationWarn {
		t.Errorf("3rd identical: level = %v, want StagnationWarn", level)
	}

	// 4th identical state - still warn
	level = m.RecordState(phase, progress, iteration)
	if level != StagnationWarn {
		t.Errorf("4th identical: level = %v, want StagnationWarn", level)
	}

	// 5th identical state - should pause
	level = m.RecordState(phase, progress, iteration)
	if level != StagnationPause {
		t.Errorf("5th identical: level = %v, want StagnationPause", level)
	}
}

func TestStagnationMonitor_TimeoutDetection(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := &StagnationConfig{
		WarnAfterIdentical:   100, // Large to disable hash-based detection
		PauseAfterIdentical:  100,
		WarnAfterNoProgress:  100 * time.Millisecond,
		PauseAfterNoProgress: 200 * time.Millisecond,
		AbortAfterNoProgress: 300 * time.Millisecond,
		MaxIterations:        100,
		HistorySize:          20,
	}
	m := NewStagnationMonitor(cfg, logger)

	// Initial state
	m.RecordState("INIT", 0, 1)

	// Wait for warn threshold
	time.Sleep(150 * time.Millisecond)
	level := m.RecordState("INIT", 0, 1) // Same state, no progress
	if level != StagnationWarn {
		t.Errorf("after warn timeout: level = %v, want StagnationWarn", level)
	}

	// Wait for pause threshold
	time.Sleep(100 * time.Millisecond)
	level = m.RecordState("INIT", 0, 1)
	if level != StagnationPause {
		t.Errorf("after pause timeout: level = %v, want StagnationPause", level)
	}

	// Wait for abort threshold
	time.Sleep(150 * time.Millisecond)
	level = m.RecordState("INIT", 0, 1)
	if level != StagnationAbort {
		t.Errorf("after abort timeout: level = %v, want StagnationAbort", level)
	}
}

func TestStagnationMonitor_MaxIterationsAbort(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := &StagnationConfig{
		WarnAfterIdentical:   100,
		PauseAfterIdentical:  100,
		WarnAfterNoProgress:  1 * time.Hour,
		PauseAfterNoProgress: 2 * time.Hour,
		AbortAfterNoProgress: 3 * time.Hour,
		MaxIterations:        5, // Low for testing
		HistorySize:          20,
	}
	m := NewStagnationMonitor(cfg, logger)

	// Iterations below max - no abort
	for i := 1; i < 5; i++ {
		level := m.RecordState("IMPL", i*20, i)
		if level == StagnationAbort {
			t.Errorf("iteration %d: should not abort yet", i)
		}
	}

	// At max iterations - should abort
	level := m.RecordState("IMPL", 100, 5)
	if level != StagnationAbort {
		t.Errorf("at max iterations: level = %v, want StagnationAbort", level)
	}
}

func TestStagnationMonitor_Reset(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := &StagnationConfig{
		WarnAfterIdentical:   3,
		PauseAfterIdentical:  5,
		WarnAfterNoProgress:  1 * time.Hour,
		PauseAfterNoProgress: 2 * time.Hour,
		AbortAfterNoProgress: 3 * time.Hour,
		MaxIterations:        100,
		HistorySize:          20,
	}
	m := NewStagnationMonitor(cfg, logger)

	// Build up some stagnation
	for i := 0; i < 5; i++ {
		m.RecordState("IMPL", 50, 3)
	}

	if m.GetCurrentLevel() == StagnationNone {
		t.Error("should have stagnation before reset")
	}

	// Reset
	m.Reset()

	if m.GetCurrentLevel() != StagnationNone {
		t.Errorf("after reset: level = %v, want StagnationNone", m.GetCurrentLevel())
	}

	if m.IdenticalHashCount() != 0 {
		t.Errorf("after reset: hash count = %d, want 0", m.IdenticalHashCount())
	}

	// New state after reset should be clean
	level := m.RecordState("INIT", 0, 1)
	if level != StagnationNone {
		t.Errorf("first state after reset: level = %v, want StagnationNone", level)
	}
}

func TestStagnationMonitor_ProgressResetsTimer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := &StagnationConfig{
		WarnAfterIdentical:   100,
		PauseAfterIdentical:  100,
		WarnAfterNoProgress:  100 * time.Millisecond,
		PauseAfterNoProgress: 200 * time.Millisecond,
		AbortAfterNoProgress: 300 * time.Millisecond,
		MaxIterations:        100,
		HistorySize:          20,
	}
	m := NewStagnationMonitor(cfg, logger)

	// Initial state
	m.RecordState("INIT", 0, 1)

	// Wait almost to warn threshold
	time.Sleep(80 * time.Millisecond)

	// Make progress - should reset timer
	m.RecordState("RESEARCH", 25, 1)

	// Wait a bit more - should NOT warn because timer reset
	time.Sleep(50 * time.Millisecond)
	level := m.RecordState("RESEARCH", 30, 1)
	if level != StagnationNone {
		t.Errorf("after progress reset: level = %v, want StagnationNone", level)
	}
}

func TestStagnationMonitor_ComputeStateHash(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	m := NewStagnationMonitor(nil, logger)

	// Same inputs should produce same hash
	hash1 := m.computeStateHash("IMPL", 50, 3)
	hash2 := m.computeStateHash("IMPL", 50, 3)
	if hash1 != hash2 {
		t.Error("identical inputs should produce identical hash")
	}

	// Different phase should produce different hash
	hash3 := m.computeStateHash("VERIFY", 50, 3)
	if hash1 == hash3 {
		t.Error("different phase should produce different hash")
	}

	// Different progress should produce different hash
	hash4 := m.computeStateHash("IMPL", 51, 3)
	if hash1 == hash4 {
		t.Error("different progress should produce different hash")
	}

	// Different iteration should produce different hash
	hash5 := m.computeStateHash("IMPL", 50, 4)
	if hash1 == hash5 {
		t.Error("different iteration should produce different hash")
	}
}

func TestStagnationMonitor_HistorySizeLimit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := &StagnationConfig{
		WarnAfterIdentical:   100,
		PauseAfterIdentical:  100,
		WarnAfterNoProgress:  1 * time.Hour,
		PauseAfterNoProgress: 2 * time.Hour,
		AbortAfterNoProgress: 3 * time.Hour,
		MaxIterations:        100,
		HistorySize:          5, // Small for testing
	}
	m := NewStagnationMonitor(cfg, logger)

	// Record more states than history size
	for i := 0; i < 10; i++ {
		m.RecordState("IMPL", i*10, i+1)
	}

	// History should be limited to HistorySize
	m.mu.Lock()
	historyLen := len(m.stateHashes)
	m.mu.Unlock()

	if historyLen != 5 {
		t.Errorf("history length = %d, want %d", historyLen, 5)
	}
}

func TestStagnationMonitor_TimeSinceProgress(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	m := NewStagnationMonitor(nil, logger)

	m.RecordState("INIT", 0, 1)

	time.Sleep(50 * time.Millisecond)

	duration := m.TimeSinceProgress()
	if duration < 50*time.Millisecond {
		t.Errorf("TimeSinceProgress = %v, want >= 50ms", duration)
	}
}

func TestStagnationMonitor_IdenticalHashCount(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	m := NewStagnationMonitor(nil, logger)

	// No hashes yet
	if count := m.IdenticalHashCount(); count != 0 {
		t.Errorf("empty: count = %d, want 0", count)
	}

	// One hash
	m.RecordState("INIT", 0, 1)
	if count := m.IdenticalHashCount(); count != 1 {
		t.Errorf("one hash: count = %d, want 1", count)
	}

	// Different hash
	m.RecordState("RESEARCH", 25, 1)
	if count := m.IdenticalHashCount(); count != 1 {
		t.Errorf("different hash: count = %d, want 1", count)
	}

	// Same as last
	m.RecordState("RESEARCH", 25, 1)
	if count := m.IdenticalHashCount(); count != 2 {
		t.Errorf("two identical: count = %d, want 2", count)
	}

	// Another same
	m.RecordState("RESEARCH", 25, 1)
	if count := m.IdenticalHashCount(); count != 3 {
		t.Errorf("three identical: count = %d, want 3", count)
	}

	// Break the chain
	m.RecordState("IMPL", 50, 2)
	if count := m.IdenticalHashCount(); count != 1 {
		t.Errorf("chain broken: count = %d, want 1", count)
	}
}

func TestStagnationMonitor_GetCurrentLevel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := &StagnationConfig{
		WarnAfterIdentical:   3,
		PauseAfterIdentical:  5,
		WarnAfterNoProgress:  1 * time.Hour,
		PauseAfterNoProgress: 2 * time.Hour,
		AbortAfterNoProgress: 3 * time.Hour,
		MaxIterations:        100,
		HistorySize:          20,
	}
	m := NewStagnationMonitor(cfg, logger)

	if m.GetCurrentLevel() != StagnationNone {
		t.Errorf("initial: level = %v, want StagnationNone", m.GetCurrentLevel())
	}

	// Build up to warn
	for i := 0; i < 3; i++ {
		m.RecordState("IMPL", 50, 3)
	}

	if m.GetCurrentLevel() != StagnationWarn {
		t.Errorf("after 3 identical: level = %v, want StagnationWarn", m.GetCurrentLevel())
	}
}
