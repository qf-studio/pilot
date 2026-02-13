package executor

import (
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"
)

// StagnationLevel represents escalation severity
type StagnationLevel int

const (
	StagnationNone  StagnationLevel = iota
	StagnationWarn                  // 3+ identical states OR timeout
	StagnationPause                 // 5+ identical states OR timeout
	StagnationAbort                 // 7+ identical states OR timeout
)

func (l StagnationLevel) String() string {
	switch l {
	case StagnationNone:
		return "none"
	case StagnationWarn:
		return "warn"
	case StagnationPause:
		return "pause"
	case StagnationAbort:
		return "abort"
	default:
		return "unknown"
	}
}

// StagnationMonitor tracks execution state for loop detection
type StagnationMonitor struct {
	config         *StagnationConfig
	log            *slog.Logger
	stateHashes    []uint64
	lastProgressAt time.Time
	lastProgress   int
	currentLevel   StagnationLevel
	startedAt      time.Time
}

// NewStagnationMonitor creates a new monitor
func NewStagnationMonitor(config *StagnationConfig, log *slog.Logger) *StagnationMonitor {
	if config == nil {
		config = DefaultStagnationConfig()
	}
	now := time.Now()
	return &StagnationMonitor{
		config:         config,
		log:            log,
		stateHashes:    make([]uint64, 0, 10),
		lastProgressAt: now,
		startedAt:      now,
		currentLevel:   StagnationNone,
	}
}

// RecordState records current state and returns stagnation level
func (m *StagnationMonitor) RecordState(phase string, progress, iteration int) StagnationLevel {
	if !m.config.Enabled {
		return StagnationNone
	}

	now := time.Now()

	// Calculate state hash
	hash := m.hashState(phase, progress, iteration)
	m.stateHashes = append(m.stateHashes, hash)

	// Keep only configured history size
	historySize := m.config.StateHistorySize
	if historySize <= 0 {
		historySize = 10
	}
	if len(m.stateHashes) > historySize {
		m.stateHashes = m.stateHashes[1:]
	}

	// Track progress changes
	if progress != m.lastProgress {
		m.lastProgressAt = now
		m.lastProgress = progress
	}

	// Check for repeated states
	repeats := m.countRepeats()

	// Check timeouts
	timeSinceProgress := now.Sub(m.lastProgressAt)
	timeSinceStart := now.Sub(m.startedAt)

	// Determine level based on both iteration count and timeouts
	newLevel := StagnationNone

	// Check abort conditions first (highest priority)
	if iteration >= m.config.AbortAtIteration ||
		repeats >= m.config.IdenticalStatesThreshold ||
		timeSinceStart >= m.config.AbortTimeout {
		newLevel = StagnationAbort
	} else if iteration >= m.config.PauseAtIteration || timeSinceProgress >= m.config.PauseTimeout {
		newLevel = StagnationPause
	} else if iteration >= m.config.WarnAtIteration || timeSinceProgress >= m.config.WarnTimeout {
		newLevel = StagnationWarn
	}

	// Log level changes
	if newLevel != m.currentLevel {
		m.log.Info("Stagnation level changed",
			slog.String("from", m.currentLevel.String()),
			slog.String("to", newLevel.String()),
			slog.Int("iteration", iteration),
			slog.Int("repeats", repeats),
			slog.Duration("time_since_progress", timeSinceProgress),
			slog.Duration("time_since_start", timeSinceStart),
		)
		m.currentLevel = newLevel
	}

	return m.currentLevel
}

// hashState creates a hash of the current state
func (m *StagnationMonitor) hashState(phase string, progress, iteration int) uint64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s:%d:%d", phase, progress, iteration)
	return h.Sum64()
}

// countRepeats counts consecutive identical hashes at end of slice
func (m *StagnationMonitor) countRepeats() int {
	if len(m.stateHashes) < 2 {
		return 0
	}

	lastHash := m.stateHashes[len(m.stateHashes)-1]
	count := 1

	for i := len(m.stateHashes) - 2; i >= 0; i-- {
		if m.stateHashes[i] == lastHash {
			count++
		} else {
			break
		}
	}

	return count
}

// Level returns current stagnation level
func (m *StagnationMonitor) Level() StagnationLevel {
	return m.currentLevel
}

// ShouldCommitPartial returns whether to commit partial work on abort
func (m *StagnationMonitor) ShouldCommitPartial() bool {
	return m.config.CommitPartialWork
}

// Reset resets the monitor for a new task
func (m *StagnationMonitor) Reset() {
	now := time.Now()
	m.stateHashes = m.stateHashes[:0]
	m.lastProgressAt = now
	m.startedAt = now
	m.lastProgress = 0
	m.currentLevel = StagnationNone
}
