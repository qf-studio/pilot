package orchestrator

import (
	"os"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// setupReconcileTestStore creates a temporary store for reconcile tests.
func setupReconcileTestStore(t *testing.T) (*memory.Store, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "pilot-orchestrator-reconcile-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	store, err := memory.NewStore(tempDir)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		t.Fatalf("failed to create store: %v", err)
	}

	return store, func() {
		_ = store.Close()
		_ = os.RemoveAll(tempDir)
	}
}

// TestOrchestratorReconcileWithStore_DelegatesToMonitor verifies
// Orchestrator.ReconcileWithStore corrects a task the orchestrator's monitor
// still shows as running once the executions row (source of truth) has
// reached a terminal status — the same backstop Monitor.ReconcileWithStore
// provides, exposed at the Orchestrator layer so GetTaskStates() (which
// drives the dashboard header running-count) reflects it too (GH-4490
// subtask 4).
func TestOrchestratorReconcileWithStore_DelegatesToMonitor(t *testing.T) {
	store, cleanup := setupReconcileTestStore(t)
	defer cleanup()

	if err := store.SaveExecution(&memory.Execution{
		ID: "e1", TaskID: "GH-4490", ProjectPath: "/p", Status: "completed", PRUrl: "https://example.com/pr/1",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	orch, err := NewOrchestrator(&Config{MaxConcurrent: 1}, nil)
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}
	defer orch.Stop()

	orch.monitor.Register("GH-4490", "Task", "")
	orch.monitor.SetProjectInfo("GH-4490", "/p", "proj")
	orch.monitor.Start("GH-4490")
	orch.monitor.UpdateProgress("GH-4490", "Implementing", 60, "working")

	if err := orch.ReconcileWithStore(store); err != nil {
		t.Fatalf("ReconcileWithStore: %v", err)
	}

	states := orch.GetTaskStates()
	var found bool
	for _, s := range states {
		if s.ID != "GH-4490" {
			continue
		}
		found = true
		if s.Status != "completed" {
			t.Errorf("Status = %s, want completed", s.Status)
		}
		if s.Progress != 100 {
			t.Errorf("Progress = %d, want 100", s.Progress)
		}
	}
	if !found {
		t.Fatal("GH-4490 not found in GetTaskStates() after reconcile")
	}
}

// TestOrchestratorReconcileWithStore_NilStore verifies the nil-store no-op
// behavior propagates through the Orchestrator wrapper.
func TestOrchestratorReconcileWithStore_NilStore(t *testing.T) {
	orch, err := NewOrchestrator(&Config{MaxConcurrent: 1}, nil)
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}
	defer orch.Stop()

	if err := orch.ReconcileWithStore(nil); err != nil {
		t.Errorf("ReconcileWithStore(nil) should be a no-op, got error: %v", err)
	}
}
