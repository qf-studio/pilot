package upgrade

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// State stores upgrade state for graceful handoff and rollback
type State struct {
	// PreviousVersion is the version before upgrade
	PreviousVersion string `json:"previous_version"`

	// NewVersion is the version being upgraded to
	NewVersion string `json:"new_version"`

	// UpgradeStarted is when the upgrade was initiated
	UpgradeStarted time.Time `json:"upgrade_started"`

	// PendingTasks lists task IDs that were in progress
	PendingTasks []string `json:"pending_tasks,omitempty"`

	// BackupPath is the path to the backup binary
	BackupPath string `json:"backup_path"`

	// Status is the current upgrade status
	Status UpgradeStatus `json:"status"`

	// Error message if upgrade failed
	Error string `json:"error,omitempty"`

	// UpgradeCompleted is when the upgrade finished
	UpgradeCompleted time.Time `json:"upgrade_completed,omitempty"`
}

// UpgradeStatus represents the upgrade state
type UpgradeStatus string

const (
	StatusPending     UpgradeStatus = "pending"
	StatusDownloading UpgradeStatus = "downloading"
	StatusWaiting     UpgradeStatus = "waiting_tasks"
	StatusInstalling  UpgradeStatus = "installing"
	StatusCompleted   UpgradeStatus = "completed"
	StatusFailed      UpgradeStatus = "failed"
	StatusRolledBack  UpgradeStatus = "rolled_back"

	// GH-3600: two-phase state for the hot-upgrade path. "completed" is only
	// correct for the CLI graceful path where no restart is expected; the hot
	// path must not claim success until the restarted process verifies it.

	// StatusAwaitingRestart means the binary is installed but the restart has
	// not been verified yet (hot path). Promoted to completed at next boot when
	// the running version matches NewVersion.
	StatusAwaitingRestart UpgradeStatus = "awaiting_restart"

	// StatusRestartFailed means exec/smoke-test failed — the old binary is
	// still running while the new one sits installed on disk.
	StatusRestartFailed UpgradeStatus = "restart_failed"
)

// StateFile is the default state file name
const StateFile = "upgrade-state.json"

// DefaultStatePath returns the default path for upgrade state
func DefaultStatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pilot", StateFile)
}

// LoadState loads the upgrade state from disk
func LoadState(path string) (*State, error) {
	if path == "" {
		path = DefaultStatePath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No state file
		}
		return nil, fmt.Errorf("failed to read state: %w", err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state: %w", err)
	}

	return &state, nil
}

// Save saves the upgrade state to disk
func (s *State) Save(path string) error {
	if path == "" {
		path = DefaultStatePath()
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write state: %w", err)
	}

	return nil
}

// Clear removes the upgrade state file
func ClearState(path string) error {
	if path == "" {
		path = DefaultStatePath()
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove state: %w", err)
	}

	return nil
}

// IsPending returns true if an upgrade is in progress
func (s *State) IsPending() bool {
	return s.Status == StatusPending ||
		s.Status == StatusDownloading ||
		s.Status == StatusWaiting ||
		s.Status == StatusInstalling
}

// NeedsRollback returns true if the upgrade failed and should be rolled back
func (s *State) NeedsRollback() bool {
	return s.Status == StatusFailed && s.BackupPath != ""
}

// MarkFailed marks the upgrade as failed
func (s *State) MarkFailed(err error) {
	s.Status = StatusFailed
	if err != nil {
		s.Error = err.Error()
	}
}

// MarkCompleted marks the upgrade as completed
func (s *State) MarkCompleted() {
	s.Status = StatusCompleted
	s.UpgradeCompleted = time.Now()
}

// MarkRolledBack marks the upgrade as rolled back
func (s *State) MarkRolledBack() {
	s.Status = StatusRolledBack
	s.UpgradeCompleted = time.Now()
}

// MarkAwaitingRestart marks the install as done with the restart still pending
// (hot path). UpgradeCompleted stays zero until boot reconciliation promotes
// the state to completed.
func (s *State) MarkAwaitingRestart() {
	s.Status = StatusAwaitingRestart
}

// MarkRestartFailed records that the post-install restart (exec or smoke test)
// failed while the previous binary kept running.
func (s *State) MarkRestartFailed(err error) {
	s.Status = StatusRestartFailed
	if err != nil {
		s.Error = err.Error()
	}
}

// NeedsBootReconcile returns true when the next process start must verify
// whether the installed version actually took effect. Deliberately excludes
// failed/rolled_back (the rollback domain) and legacy completed states.
func (s *State) NeedsBootReconcile() bool {
	return s.Status == StatusAwaitingRestart || s.Status == StatusRestartFailed
}
