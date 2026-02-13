package executor

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestStagnationMonitor_IdenticalStates(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	config := &StagnationConfig{
		Enabled:                  true,
		WarnAtIteration:          5,
		PauseAtIteration:         8,
		AbortAtIteration:         12,
		IdenticalStatesThreshold: 3,
		StateHistorySize:         5,
		WarnTimeout:              1 * time.Hour, // High to not trigger
		PauseTimeout:             2 * time.Hour,
		AbortTimeout:             3 * time.Hour,
		GracePeriod:              30 * time.Second,
		CommitPartialWork:        true,
	}

	m := NewStagnationMonitor(config, log)

	// First 2 identical states - no warning yet
	m.RecordState("IMPL", 50, 1)
	level := m.RecordState("IMPL", 50, 1)
	if level != StagnationNone {
		t.Errorf("Expected StagnationNone after 2 repeats, got %v", level)
	}

	// 3rd identical - should abort due to identical states threshold
	level = m.RecordState("IMPL", 50, 1)
	if level != StagnationAbort {
		t.Errorf("Expected StagnationAbort after 3 identical states, got %v", level)
	}
}

func TestStagnationMonitor_IterationLimits(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	config := &StagnationConfig{
		Enabled:                  true,
		WarnAtIteration:          3,
		PauseAtIteration:         5,
		AbortAtIteration:         7,
		IdenticalStatesThreshold: 10, // High to not trigger
		StateHistorySize:         5,
		WarnTimeout:              1 * time.Hour,
		PauseTimeout:             2 * time.Hour,
		AbortTimeout:             3 * time.Hour,
		GracePeriod:              30 * time.Second,
		CommitPartialWork:        true,
	}

	m := NewStagnationMonitor(config, log)

	// Different states each time to avoid identical state detection
	level := m.RecordState("IMPL", 10, 1)
	if level != StagnationNone {
		t.Errorf("Expected StagnationNone at iteration 1, got %v", level)
	}

	level = m.RecordState("IMPL", 20, 2)
	if level != StagnationNone {
		t.Errorf("Expected StagnationNone at iteration 2, got %v", level)
	}

	level = m.RecordState("IMPL", 30, 3)
	if level != StagnationWarn {
		t.Errorf("Expected StagnationWarn at iteration 3, got %v", level)
	}

	level = m.RecordState("IMPL", 40, 5)
	if level != StagnationPause {
		t.Errorf("Expected StagnationPause at iteration 5, got %v", level)
	}

	level = m.RecordState("IMPL", 50, 7)
	if level != StagnationAbort {
		t.Errorf("Expected StagnationAbort at iteration 7, got %v", level)
	}
}

func TestStagnationMonitor_Reset(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	config := DefaultStagnationConfig()
	config.Enabled = true
	config.WarnAtIteration = 2

	m := NewStagnationMonitor(config, log)

	// Build up state
	m.RecordState("IMPL", 50, 1)
	m.RecordState("IMPL", 50, 2)
	if m.Level() != StagnationWarn {
		t.Errorf("Expected StagnationWarn before reset")
	}

	// Reset
	m.Reset()

	if m.Level() != StagnationNone {
		t.Errorf("Expected StagnationNone after reset, got %v", m.Level())
	}
}

func TestStagnationLevel_String(t *testing.T) {
	tests := []struct {
		level StagnationLevel
		want  string
	}{
		{StagnationNone, "none"},
		{StagnationWarn, "warn"},
		{StagnationPause, "pause"},
		{StagnationAbort, "abort"},
	}

	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("StagnationLevel(%d).String() = %v, want %v", tt.level, got, tt.want)
		}
	}
}
