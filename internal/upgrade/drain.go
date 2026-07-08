// Package upgrade: cross-process drain handshake (GH-4106).
//
// A caller (e.g. an external supervisor doing a blue/green restart) needs to
// tell a *different* Pilot process — identified only by PID — to stop
// accepting new work, then wait until it reports zero in-flight executions
// or a bounded timeout elapses. GracefulUpgrader/TaskChecker only cover the
// in-process case (a process checking its own tasks before upgrading
// itself); this file adds the missing IPC leg for the cross-process case.
//
// The handshake is deliberately the lightest option that fits the existing
// patterns in this package: a UNIX signal (see drain_unix.go/drain_windows.go)
// tells the target to enter drain mode, and a JSON status file — the same
// disk-based approach state.go already uses for upgrade state — lets the
// target report its in-flight count back without a socket or RPC layer.
package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DrainOutcome is the typed result of waiting for a target process to drain.
type DrainOutcome int

const (
	// DrainUnknown means the wait did not complete normally (e.g. the
	// caller's context was cancelled, or the status file could not be read).
	DrainUnknown DrainOutcome = iota

	// Drained means the target process reported zero in-flight executions
	// after entering drain mode.
	Drained

	// TimedOut means the bounded wait elapsed before the target reported
	// zero in-flight executions.
	TimedOut
)

// String implements fmt.Stringer for logging.
func (o DrainOutcome) String() string {
	switch o {
	case Drained:
		return "drained"
	case TimedOut:
		return "timed_out"
	default:
		return "unknown"
	}
}

// DrainStatus is the cross-process handshake payload. The target process is
// expected to write this file (via Save, or ReportDrainStatus) after it
// receives the drain signal, updating InFlightCount as its running tasks
// finish. The requester polls the same file with WaitForDrain.
type DrainStatus struct {
	// PID is the process that wrote this status.
	PID int `json:"pid"`

	// Draining is true once the target has acknowledged the drain signal
	// and stopped accepting new work.
	Draining bool `json:"draining"`

	// InFlightCount is the number of executions still running.
	InFlightCount int `json:"in_flight_count"`

	// UpdatedAt is when this status was last written.
	UpdatedAt time.Time `json:"updated_at"`
}

// DrainStatusFile is the default status file name, mirroring StateFile.
const DrainStatusFile = "drain-status.json"

// DefaultDrainStatusPath returns the default path for the drain status
// handshake file, mirroring DefaultStatePath.
func DefaultDrainStatusPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pilot", DrainStatusFile)
}

// LoadDrainStatus reads the drain status file. A missing file is not an
// error — it returns (nil, nil), mirroring LoadState's "nothing signaled
// yet" semantics.
func LoadDrainStatus(path string) (*DrainStatus, error) {
	if path == "" {
		path = DefaultDrainStatusPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read drain status: %w", err)
	}

	var status DrainStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("failed to parse drain status: %w", err)
	}

	return &status, nil
}

// Save writes the drain status to disk, creating the parent directory if
// needed. The write is atomic (temp file + rename) so a concurrent
// WaitForDrain-style reader — polling this same path while the target
// process updates it — never observes a partially-written file.
func (d *DrainStatus) Save(path string) error {
	if path == "" {
		path = DefaultDrainStatusPath()
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create drain status directory: %w", err)
	}

	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal drain status: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".drain-status-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp drain status file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write drain status: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to write drain status: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to write drain status: %w", err)
	}

	return nil
}

// ReportDrainStatus is the receiving side of the handshake: the target
// process calls this (typically from its drain-signal handler and again as
// running tasks finish) to publish its current in-flight count.
func ReportDrainStatus(path string, pid int, draining bool, inFlightCount int) error {
	status := &DrainStatus{
		PID:           pid,
		Draining:      draining,
		InFlightCount: inFlightCount,
		UpdatedAt:     time.Now(),
	}
	return status.Save(path)
}

// clock abstracts time so the poll loop below can be driven by fake clocks
// in tests instead of real sleeps.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// realClock is the production clock implementation.
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// DrainConfig configures RequestDrain's signal+poll handshake.
type DrainConfig struct {
	// StatusPath is where the target process reports its drain status.
	// Defaults to DefaultDrainStatusPath().
	StatusPath string

	// Timeout bounds how long to wait for the target to report drained.
	// Defaults to 30s.
	Timeout time.Duration

	// PollInterval is how often the status file is re-read. Defaults to
	// 500ms.
	PollInterval time.Duration
}

func (c *DrainConfig) withDefaults() DrainConfig {
	cfg := DrainConfig{}
	if c != nil {
		cfg = *c
	}
	if cfg.StatusPath == "" {
		cfg.StatusPath = DefaultDrainStatusPath()
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	return cfg
}

// RequestDrain signals pid to enter drain mode, then polls its status file
// until it reports zero in-flight executions or cfg.Timeout elapses.
// Returns Drained or TimedOut; DrainUnknown plus a non-nil error indicates
// the signal failed to send, the status file could not be read, or ctx was
// cancelled.
func (g *GracefulUpgrader) RequestDrain(ctx context.Context, pid int, cfg *DrainConfig) (DrainOutcome, error) {
	resolved := cfg.withDefaults()

	if err := SignalDrain(pid); err != nil {
		return DrainUnknown, fmt.Errorf("failed to signal drain: %w", err)
	}

	return waitForDrain(ctx, resolved.StatusPath, resolved.Timeout, resolved.PollInterval, g.clock)
}

// waitForDrain polls statusPath until the reported status shows the target
// has drained (Draining && InFlightCount == 0), timeout elapses, or ctx is
// cancelled.
func waitForDrain(ctx context.Context, statusPath string, timeout, pollInterval time.Duration, clk clock) (DrainOutcome, error) {
	if clk == nil {
		clk = realClock{}
	}

	deadline := clk.Now().Add(timeout)

	for {
		status, err := LoadDrainStatus(statusPath)
		if err != nil {
			return DrainUnknown, err
		}
		if status != nil && status.Draining && status.InFlightCount == 0 {
			return Drained, nil
		}

		if !clk.Now().Before(deadline) {
			return TimedOut, nil
		}

		select {
		case <-ctx.Done():
			return DrainUnknown, ctx.Err()
		case <-clk.After(pollInterval):
		}
	}
}
