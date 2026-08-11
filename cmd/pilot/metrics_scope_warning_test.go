package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
)

// TestWarnIfMetricsScopeEmpty covers the GH-4832 startup warning: a
// configured dashboard.metrics_scope_path that matches zero rows in the
// store (e.g. a trailing-slash or symlink variant of the recorded
// project_path) is otherwise silent — GetLifetimeTaskCounts, like the other
// scoped queries, just returns all-zero.
func TestWarnIfMetricsScopeEmpty(t *testing.T) {
	tempDir := t.TempDir()
	store, err := memory.NewStore(tempDir)
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SaveExecution(&memory.Execution{
		ID: "e1", TaskID: "GH-4832", ProjectPath: "/repos/pilot", Status: "completed",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	logPath := filepath.Join(tempDir, "warn.log")
	if err := logging.Init(&logging.Config{Level: "warn", Format: "text", Output: logPath}); err != nil {
		t.Fatalf("logging.Init: %v", err)
	}
	t.Cleanup(func() { _ = logging.Init(logging.DefaultConfig()) })

	readLog := func() string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return ""
			}
			t.Fatalf("ReadFile: %v", err)
		}
		return string(data)
	}

	warnIfMetricsScopeEmpty(nil, "/repos/pilot")
	if got := readLog(); strings.Contains(got, "metrics_scope_path") {
		t.Errorf("nil store must not warn, got log:\n%s", got)
	}

	warnIfMetricsScopeEmpty(store, "")
	if got := readLog(); strings.Contains(got, "metrics_scope_path") {
		t.Errorf("empty scope (fleet-wide) must not warn, got log:\n%s", got)
	}

	warnIfMetricsScopeEmpty(store, "/repos/pilot")
	if got := readLog(); strings.Contains(got, "metrics_scope_path") {
		t.Errorf("scope matching rows must not warn, got log:\n%s", got)
	}

	warnIfMetricsScopeEmpty(store, "/repos/pilot/")
	got := readLog()
	if !strings.Contains(got, "zero executions") || !strings.Contains(got, "/repos/pilot/") {
		t.Errorf("trailing-slash scope with zero exact-match rows should warn, got log:\n%s", got)
	}
}
