package executor

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// StagnationLevel represents the severity of detected stagnation.
// Higher levels indicate more serious stagnation requiring escalation.
type StagnationLevel int

const (
	// StagnationNone indicates no stagnation detected
	StagnationNone StagnationLevel = iota

	// StagnationWarn indicates early warning (3+ identical states OR 10m no progress)
	// Action: Log warning, emit alert, continue execution
	StagnationWarn

	// StagnationPause indicates significant stagnation (5+ identical states OR 20m no progress)
	// Action: Alert, request human decision
	StagnationPause

	// StagnationAbort indicates critical stagnation (30m timeout OR iteration limit)
	// Action: Graceful shutdown, commit partial work
	StagnationAbort
)

// String returns the string representation of a StagnationLevel
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
		return fmt.Sprintf("unknown(%d)", l)
	}
}

// StagnationConfig holds configuration for stagnation detection thresholds.
// All durations are specified as time.Duration values.
type StagnationConfig struct {
	// WarnAfterIdentical is the number of identical state hashes before warning (default: 3)
	WarnAfterIdentical int `yaml:"warn_after_identical"`

	// PauseAfterIdentical is the number of identical state hashes before pause (default: 5)
	PauseAfterIdentical int `yaml:"pause_after_identical"`

	// WarnAfterNoProgress is the duration without progress before warning (default: 10m)
	WarnAfterNoProgress time.Duration `yaml:"warn_after_no_progress"`

	// PauseAfterNoProgress is the duration without progress before pause (default: 20m)
	PauseAfterNoProgress time.Duration `yaml:"pause_after_no_progress"`

	// AbortAfterNoProgress is the duration without progress before abort (default: 30m)
	AbortAfterNoProgress time.Duration `yaml:"abort_after_no_progress"`

	// MaxIterations is the absolute iteration limit before abort (default: 10)
	MaxIterations int `yaml:"max_iterations"`

	// HistorySize is the number of state hashes to track (default: 20)
	HistorySize int `yaml:"history_size"`
}

// DefaultStagnationConfig returns sensible default configuration values.
func DefaultStagnationConfig() *StagnationConfig {
	return &StagnationConfig{
		WarnAfterIdentical:   3,
		PauseAfterIdentical:  5,
		WarnAfterNoProgress:  10 * time.Minute,
		PauseAfterNoProgress: 20 * time.Minute,
		AbortAfterNoProgress: 30 * time.Minute,
		MaxIterations:        10,
		HistorySize:          20,
	}
}

// StagnationMonitor tracks execution state and detects stagnation patterns.
// It uses state hash tracking and timeout-based detection to identify when
// execution is stuck in a loop or making no meaningful progress.
type StagnationMonitor struct {
	config *StagnationConfig
	log    *slog.Logger

	// State tracking
	stateHashes    []uint64
	lastProgressAt time.Time
	lastPhase      string
	lastProgress   int
	lastIteration  int
	currentLevel   StagnationLevel
	startedAt      time.Time

	mu sync.Mutex
}

// NewStagnationMonitor creates a new StagnationMonitor with the given configuration.
// If config is nil, DefaultStagnationConfig() is used.
// If log is nil, slog.Default() is used.
func NewStagnationMonitor(config *StagnationConfig, log *slog.Logger) *StagnationMonitor {
	if config == nil {
		config = DefaultStagnationConfig()
	}
	if log == nil {
		log = slog.Default()
	}

	now := time.Now()
	return &StagnationMonitor{
		config:         config,
		log:            log,
		stateHashes:    make([]uint64, 0, config.HistorySize),
		lastProgressAt: now,
		startedAt:      now,
		lastProgress:   -1, // -1 indicates no progress recorded yet
		lastIteration:  -1,
		currentLevel:   StagnationNone,
	}
}

// RecordState records a Navigator status update and checks for stagnation.
// Returns the current stagnation level after evaluating all detection mechanisms.
//
// Detection mechanisms:
// 1. State hash tracking - hash(phase + progress + iteration), detect consecutive identical
// 2. Progress timeout - track time since last meaningful change
// 3. Iteration limit - escalate at configurable thresholds
func (m *StagnationMonitor) RecordState(phase string, progress, iteration int) StagnationLevel {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	// Check for meaningful progress (phase change, progress increase, or iteration advance)
	hasProgress := m.detectProgress(phase, progress, iteration)
	if hasProgress {
		m.lastProgressAt = now
		m.log.Debug("stagnation monitor: progress detected",
			"phase", phase,
			"progress", progress,
			"iteration", iteration,
		)
	}

	// Update last seen values
	m.lastPhase = phase
	m.lastProgress = progress
	m.lastIteration = iteration

	// Compute and record state hash
	hash := m.computeStateHash(phase, progress, iteration)
	m.stateHashes = append(m.stateHashes, hash)

	// Trim history to configured size
	if len(m.stateHashes) > m.config.HistorySize {
		m.stateHashes = m.stateHashes[len(m.stateHashes)-m.config.HistorySize:]
	}

	// Evaluate stagnation level
	level := m.evaluateStagnation(now, iteration)

	// Log level changes
	if level != m.currentLevel {
		m.log.Info("stagnation level changed",
			"from", m.currentLevel.String(),
			"to", level.String(),
			"phase", phase,
			"progress", progress,
			"iteration", iteration,
			"identical_hashes", m.countIdenticalHashes(),
			"time_since_progress", now.Sub(m.lastProgressAt).String(),
		)
		m.currentLevel = level
	}

	return level
}

// GetCurrentLevel returns the current stagnation level without recording new state.
func (m *StagnationMonitor) GetCurrentLevel() StagnationLevel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.currentLevel
}

// Reset resets the monitor state for a new execution.
func (m *StagnationMonitor) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	m.stateHashes = make([]uint64, 0, m.config.HistorySize)
	m.lastProgressAt = now
	m.startedAt = now
	m.lastPhase = ""
	m.lastProgress = -1
	m.lastIteration = -1
	m.currentLevel = StagnationNone
}

// TimeSinceProgress returns the duration since the last meaningful progress.
func (m *StagnationMonitor) TimeSinceProgress() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Since(m.lastProgressAt)
}

// IdenticalHashCount returns the count of consecutive identical hashes at the end of history.
func (m *StagnationMonitor) IdenticalHashCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.countIdenticalHashes()
}

// detectProgress checks if there's meaningful progress compared to the last recorded state.
func (m *StagnationMonitor) detectProgress(phase string, progress, iteration int) bool {
	// First state is always progress
	if m.lastProgress == -1 {
		return true
	}

	// Phase change is progress
	if phase != m.lastPhase {
		return true
	}

	// Progress increase is progress
	if progress > m.lastProgress {
		return true
	}

	// Iteration advance is progress
	if iteration > m.lastIteration {
		return true
	}

	return false
}

// computeStateHash generates a hash from phase, progress, and iteration.
// Uses SHA-256 for consistent hashing across different states.
func (m *StagnationMonitor) computeStateHash(phase string, progress, iteration int) uint64 {
	h := sha256.New()
	h.Write([]byte(phase))
	h.Write([]byte{0}) // separator

	// Write progress as bytes
	progBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(progBytes, uint32(progress))
	h.Write(progBytes)

	// Write iteration as bytes
	iterBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(iterBytes, uint32(iteration))
	h.Write(iterBytes)

	// Take first 8 bytes of hash as uint64
	sum := h.Sum(nil)
	return binary.BigEndian.Uint64(sum[:8])
}

// countIdenticalHashes counts consecutive identical hashes at the end of history.
func (m *StagnationMonitor) countIdenticalHashes() int {
	if len(m.stateHashes) < 2 {
		return len(m.stateHashes)
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

// evaluateStagnation determines the stagnation level based on all detection mechanisms.
func (m *StagnationMonitor) evaluateStagnation(now time.Time, iteration int) StagnationLevel {
	identicalCount := m.countIdenticalHashes()
	timeSinceProgress := now.Sub(m.lastProgressAt)

	// Check for abort conditions first (highest priority)
	if timeSinceProgress >= m.config.AbortAfterNoProgress {
		m.log.Warn("stagnation: abort threshold reached (timeout)",
			"time_since_progress", timeSinceProgress.String(),
			"threshold", m.config.AbortAfterNoProgress.String(),
		)
		return StagnationAbort
	}

	if iteration >= m.config.MaxIterations {
		m.log.Warn("stagnation: abort threshold reached (max iterations)",
			"iteration", iteration,
			"max_iterations", m.config.MaxIterations,
		)
		return StagnationAbort
	}

	// Check for pause conditions
	if identicalCount >= m.config.PauseAfterIdentical {
		m.log.Warn("stagnation: pause threshold reached (identical states)",
			"identical_count", identicalCount,
			"threshold", m.config.PauseAfterIdentical,
		)
		return StagnationPause
	}

	if timeSinceProgress >= m.config.PauseAfterNoProgress {
		m.log.Warn("stagnation: pause threshold reached (no progress)",
			"time_since_progress", timeSinceProgress.String(),
			"threshold", m.config.PauseAfterNoProgress.String(),
		)
		return StagnationPause
	}

	// Check for warn conditions
	if identicalCount >= m.config.WarnAfterIdentical {
		m.log.Debug("stagnation: warn threshold reached (identical states)",
			"identical_count", identicalCount,
			"threshold", m.config.WarnAfterIdentical,
		)
		return StagnationWarn
	}

	if timeSinceProgress >= m.config.WarnAfterNoProgress {
		m.log.Debug("stagnation: warn threshold reached (no progress)",
			"time_since_progress", timeSinceProgress.String(),
			"threshold", m.config.WarnAfterNoProgress.String(),
		)
		return StagnationWarn
	}

	return StagnationNone
}
