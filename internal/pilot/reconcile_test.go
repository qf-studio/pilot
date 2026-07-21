package pilot

import (
	"os"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
)

// TestPilotReconcileTaskStatesWithStore_NilStore verifies the nil-store
// no-op contract without needing a fully constructed Pilot — mirrors
// executor.Monitor.ReconcileWithStore(nil) and Orchestrator.ReconcileWithStore(nil).
func TestPilotReconcileTaskStatesWithStore_NilStore(t *testing.T) {
	p := &Pilot{}
	if got := p.Store(); got != nil {
		t.Errorf("Store() = %v, want nil", got)
	}
	if err := p.ReconcileTaskStatesWithStore(); err != nil {
		t.Errorf("ReconcileTaskStatesWithStore() with nil store should be a no-op, got error: %v", err)
	}
}

// TestPilotReconcileTaskStatesWithStore_Wiring verifies Pilot.Store() exposes
// the store New() creates, and ReconcileTaskStatesWithStore() delegates to
// the orchestrator using that same store without error — the wiring
// dashboard mode's collectTasks() (cmd/pilot/commands.go) relies on so the
// header running-count reflects the executions table, not just the ticker
// in runPollingMode (GH-4490 subtask 4).
func TestPilotReconcileTaskStatesWithStore_Wiring(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pilot-reconcile-wiring-test")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	cfg := config.DefaultConfig()
	cfg.Memory.Path = tempDir

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	if p.Store() == nil {
		t.Fatal("Store() = nil, want the store New() created")
	}

	// No tasks tracked yet — must be a clean no-op, not an error.
	if err := p.ReconcileTaskStatesWithStore(); err != nil {
		t.Errorf("ReconcileTaskStatesWithStore() = %v, want nil", err)
	}
}
