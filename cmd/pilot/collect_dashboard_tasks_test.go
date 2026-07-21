package main

import (
	"os"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/pilot"
)

// TestCollectDashboardTasks_ReconcilesGatewayMonitorBeforeMerging verifies
// that collectDashboardTasks — the merge function backing runDashboardMode's
// (gateway/webhook) dashboard ticker — reconciles gwMonitor against gwStore
// before reading task states. Without this, a card whose Complete/Fail
// callback never fired (no-commit failure, externally closed PR) would show
// "running" forever in this dashboard mode even though runPollingMode's
// ticker (subtask 1) was already fixed, because the two modes have separate
// merge/ticker paths (GH-4490 subtask 4).
func TestCollectDashboardTasks_ReconcilesGatewayMonitorBeforeMerging(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pilot-collect-dashboard-tasks-test")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	gwStore, err := memory.NewStore(tempDir)
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = gwStore.Close() }()

	if err := gwStore.SaveExecution(&memory.Execution{
		ID: "e1", TaskID: "GH-4490", ProjectPath: "/p", Status: "completed", PRUrl: "https://example.com/pr/1",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	gwMonitor := executor.NewMonitor()
	gwMonitor.Register("GH-4490", "Task", "")
	gwMonitor.SetProjectInfo("GH-4490", "/p", "proj")
	gwMonitor.Start("GH-4490")
	gwMonitor.UpdateProgress("GH-4490", "Implementing", 60, "working")

	pilotTempDir, err := os.MkdirTemp("", "pilot-collect-dashboard-tasks-pilot-store")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(pilotTempDir) }()

	cfg := config.DefaultConfig()
	cfg.Memory.Path = pilotTempDir
	p, err := pilot.New(cfg)
	if err != nil {
		t.Fatalf("pilot.New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	tasks := collectDashboardTasks(p, gwMonitor, gwStore, "")

	var found bool
	for _, task := range tasks {
		if task.ID != "GH-4490" {
			continue
		}
		found = true
		if task.Status == "running" {
			t.Errorf("Status = %q, want a terminal status (executions row is completed) — header running-count would over-count this stale card", task.Status)
		}
		if task.Progress != 100 {
			t.Errorf("Progress = %d, want 100", task.Progress)
		}
	}
	if !found {
		t.Fatal("GH-4490 not found in collectDashboardTasks() output")
	}
}
