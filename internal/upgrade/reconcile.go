// Package upgrade: boot-time reconciliation of the two-phase upgrade state
// (GH-3600). A hot upgrade writes awaiting_restart before exec'ing; only the
// restarted process can prove the upgrade took effect, by comparing its own
// compiled version against the state's NewVersion. The running version is the
// ground truth — it covers hot exec, manual restarts, and Windows alike; the
// PILOT_RESTARTED env marker only distinguishes how the restart happened.
package upgrade

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// BootOutcome classifies what boot-time reconciliation found.
type BootOutcome int

const (
	// BootNoAction — no state file, no reconcilable status, or load error.
	BootNoAction BootOutcome = iota

	// BootUpgradeVerified — the running version matches the installed one;
	// state promoted to completed and cleaned up.
	BootUpgradeVerified

	// BootRestartFailed — state expected a new version but this process runs
	// a different one: the previous restart never took effect.
	BootRestartFailed
)

// BootReconcileResult reports the reconciliation outcome for logging/UI.
type BootReconcileResult struct {
	Outcome         BootOutcome
	PreviousVersion string
	NewVersion      string

	// RestartError carries the persisted exec failure, if one was recorded.
	RestartError string

	// HotExec is true when this process was started via the hot-upgrade exec
	// (PILOT_RESTARTED=1) rather than a manual restart. Phrasing only.
	HotExec bool
}

// ReconcileBootState verifies a pending upgrade against the running version.
// It never fails startup: any load/parse problem degrades to BootNoAction.
func ReconcileBootState(runningVersion, statePath string) (*BootReconcileResult, error) {
	result := &BootReconcileResult{
		Outcome: BootNoAction,
		HotExec: os.Getenv("PILOT_RESTARTED") == "1",
	}

	state, err := LoadState(statePath)
	if err != nil {
		slog.Warn("upgrade state unreadable at boot, skipping reconciliation", slog.Any("error", err))
		return result, nil
	}
	if state == nil || !state.NeedsBootReconcile() {
		// Covers legacy "completed" files (CLI upgrades, pre-GH-3600 hot
		// upgrades) and failed/rolled_back, which belong to the rollback domain.
		return result, nil
	}

	result.PreviousVersion = state.PreviousVersion
	result.NewVersion = state.NewVersion
	result.RestartError = state.Error

	// Exact equality after trimming the v prefix (same normalization as
	// CheckVersion). Equality is the question here — semver ordering would
	// mangle dev builds.
	if strings.TrimPrefix(runningVersion, "v") == strings.TrimPrefix(state.NewVersion, "v") {
		// Promote and clean up. Small intentional duplication of
		// GracefulUpgrader.CleanupState — constructing an upgrader at boot
		// (os.Executable lookups) is not worth it here.
		state.MarkCompleted()
		_ = state.Save(statePath)
		if state.BackupPath != "" {
			_ = os.Remove(state.BackupPath)
		}
		_ = ClearState(statePath)

		result.Outcome = BootUpgradeVerified
		return result, nil
	}

	// Mismatch: the previous restart did not take effect.
	if state.Status == StatusAwaitingRestart {
		state.MarkRestartFailed(fmt.Errorf(
			"process restarted with version %s, expected %s", runningVersion, state.NewVersion))
		_ = state.Save(statePath)
		result.RestartError = state.Error
	}

	result.Outcome = BootRestartFailed
	return result, nil
}
