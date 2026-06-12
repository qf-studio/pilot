package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateMarkHelpers(t *testing.T) {
	tests := []struct {
		name               string
		mutate             func(s *State)
		wantStatus         UpgradeStatus
		wantError          bool
		wantBootReconcile  bool
		wantCompletedZero  bool
	}{
		{
			name:              "MarkAwaitingRestart",
			mutate:            func(s *State) { s.MarkAwaitingRestart() },
			wantStatus:        StatusAwaitingRestart,
			wantBootReconcile: true,
			wantCompletedZero: true,
		},
		{
			name:              "MarkRestartFailed",
			mutate:            func(s *State) { s.MarkRestartFailed(errors.New("exec exploded")) },
			wantStatus:        StatusRestartFailed,
			wantError:         true,
			wantBootReconcile: true,
			wantCompletedZero: true,
		},
		{
			name:       "legacy completed needs no reconcile",
			mutate:     func(s *State) { s.MarkCompleted() },
			wantStatus: StatusCompleted,
		},
		{
			name:       "failed stays in rollback domain",
			mutate:     func(s *State) { s.MarkFailed(errors.New("download died")) },
			wantStatus: StatusFailed,
			wantError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &State{PreviousVersion: "1.0.0", NewVersion: "v2.0.0"}
			tt.mutate(s)

			if s.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", s.Status, tt.wantStatus)
			}
			if (s.Error != "") != tt.wantError {
				t.Errorf("Error = %q, wantError %v", s.Error, tt.wantError)
			}
			if got := s.NeedsBootReconcile(); got != tt.wantBootReconcile {
				t.Errorf("NeedsBootReconcile() = %v, want %v", got, tt.wantBootReconcile)
			}
			if tt.wantCompletedZero && !s.UpgradeCompleted.IsZero() {
				t.Error("UpgradeCompleted should stay zero until promotion")
			}
			// New statuses must never look in-progress or trigger auto-rollback:
			// the installed binary is good — rollback would resurrect the old one.
			if s.Status == StatusAwaitingRestart || s.Status == StatusRestartFailed {
				if s.IsPending() {
					t.Error("IsPending() must be false for restart-phase statuses")
				}
				s.BackupPath = "/tmp/backup"
				if s.NeedsRollback() {
					t.Error("NeedsRollback() must be false for restart-phase statuses")
				}
			}
		})
	}
}

func TestState_NewStatuses_JSONRoundtrip(t *testing.T) {
	for _, status := range []UpgradeStatus{StatusAwaitingRestart, StatusRestartFailed} {
		t.Run(string(status), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "upgrade-state.json")
			s := &State{NewVersion: "v2.0.0", Status: status, Error: "boom"}
			if err := s.Save(path); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			got, err := LoadState(path)
			if err != nil {
				t.Fatalf("LoadState() error = %v", err)
			}
			if got.Status != status || got.Error != "boom" {
				t.Errorf("roundtrip = %q/%q, want %q/boom", got.Status, got.Error, status)
			}
		})
	}
}

func TestReconcileBootState(t *testing.T) {
	tests := []struct {
		name            string
		state           *State // nil = no state file
		corrupt         bool
		runningVersion  string
		wantOutcome     BootOutcome
		wantFileGone    bool
		wantStatusAfter UpgradeStatus // checked when file expected to remain
	}{
		{
			name:           "no state file",
			runningVersion: "2.0.0",
			wantOutcome:    BootNoAction,
			wantFileGone:   true,
		},
		{
			name:           "awaiting_restart with matching version is promoted",
			state:          &State{PreviousVersion: "1.0.0", NewVersion: "v2.0.0", Status: StatusAwaitingRestart},
			runningVersion: "2.0.0",
			wantOutcome:    BootUpgradeVerified,
			wantFileGone:   true,
		},
		{
			name:           "v-prefix on running side still matches",
			state:          &State{PreviousVersion: "1.0.0", NewVersion: "2.0.0", Status: StatusAwaitingRestart},
			runningVersion: "v2.0.0",
			wantOutcome:    BootUpgradeVerified,
			wantFileGone:   true,
		},
		{
			name:            "awaiting_restart with version mismatch becomes restart_failed",
			state:           &State{PreviousVersion: "1.0.0", NewVersion: "v2.0.0", Status: StatusAwaitingRestart},
			runningVersion:  "1.0.0",
			wantOutcome:     BootRestartFailed,
			wantStatusAfter: StatusRestartFailed,
		},
		{
			name:           "restart_failed recovers after manual restart onto new version",
			state:          &State{PreviousVersion: "1.0.0", NewVersion: "v2.0.0", Status: StatusRestartFailed, Error: "exec exploded"},
			runningVersion: "2.0.0",
			wantOutcome:    BootUpgradeVerified,
			wantFileGone:   true,
		},
		{
			name:            "restart_failed mismatch is idempotent across boots",
			state:           &State{PreviousVersion: "1.0.0", NewVersion: "v2.0.0", Status: StatusRestartFailed, Error: "exec exploded"},
			runningVersion:  "1.0.0",
			wantOutcome:     BootRestartFailed,
			wantStatusAfter: StatusRestartFailed,
		},
		{
			name:            "legacy completed state is ignored",
			state:           &State{PreviousVersion: "1.0.0", NewVersion: "v2.0.0", Status: StatusCompleted},
			runningVersion:  "1.0.0",
			wantOutcome:     BootNoAction,
			wantStatusAfter: StatusCompleted,
		},
		{
			name:            "failed state stays in the rollback domain",
			state:           &State{PreviousVersion: "1.0.0", NewVersion: "v2.0.0", Status: StatusFailed, Error: "download died"},
			runningVersion:  "1.0.0",
			wantOutcome:     BootNoAction,
			wantStatusAfter: StatusFailed,
		},
		{
			name:           "corrupt state file degrades to no action",
			corrupt:        true,
			runningVersion: "2.0.0",
			wantOutcome:    BootNoAction,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			statePath := filepath.Join(dir, "upgrade-state.json")

			if tt.corrupt {
				if err := os.WriteFile(statePath, []byte("{not json"), 0644); err != nil {
					t.Fatal(err)
				}
			} else if tt.state != nil {
				if err := tt.state.Save(statePath); err != nil {
					t.Fatal(err)
				}
			}

			result, err := ReconcileBootState(tt.runningVersion, statePath)
			if err != nil {
				t.Fatalf("ReconcileBootState() error = %v — must never fail startup", err)
			}
			if result.Outcome != tt.wantOutcome {
				t.Fatalf("Outcome = %v, want %v", result.Outcome, tt.wantOutcome)
			}

			_, statErr := os.Stat(statePath)
			fileGone := os.IsNotExist(statErr)
			if tt.wantFileGone != fileGone && !tt.corrupt {
				t.Errorf("state file gone = %v, want %v", fileGone, tt.wantFileGone)
			}
			if tt.wantStatusAfter != "" {
				after, lerr := LoadState(statePath)
				if lerr != nil || after == nil {
					t.Fatalf("LoadState() after reconcile = %v, %v", after, lerr)
				}
				if after.Status != tt.wantStatusAfter {
					t.Errorf("Status after = %q, want %q", after.Status, tt.wantStatusAfter)
				}
			}
			if tt.wantOutcome == BootRestartFailed && !strings.Contains(result.RestartError, "expected") && !strings.Contains(result.RestartError, "exec exploded") {
				t.Errorf("RestartError = %q, want persisted or derived failure reason", result.RestartError)
			}
		})
	}
}

func TestReconcileBootState_RemovesBackupOnPromotion(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "upgrade-state.json")
	backupPath := filepath.Join(dir, "pilot.backup")
	if err := os.WriteFile(backupPath, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	s := &State{PreviousVersion: "1.0.0", NewVersion: "v2.0.0", Status: StatusAwaitingRestart, BackupPath: backupPath}
	if err := s.Save(statePath); err != nil {
		t.Fatal(err)
	}

	result, err := ReconcileBootState("2.0.0", statePath)
	if err != nil || result.Outcome != BootUpgradeVerified {
		t.Fatalf("ReconcileBootState() = %v, %v; want verified", result.Outcome, err)
	}
	if _, statErr := os.Stat(backupPath); !os.IsNotExist(statErr) {
		t.Error("backup binary should be removed after verified upgrade")
	}
}

func TestReconcileBootState_HotExecMarker(t *testing.T) {
	t.Setenv("PILOT_RESTARTED", "1")
	result, err := ReconcileBootState("2.0.0", filepath.Join(t.TempDir(), "upgrade-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.HotExec {
		t.Error("HotExec should reflect PILOT_RESTARTED=1")
	}
}
