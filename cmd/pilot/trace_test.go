package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// newTraceTestStore creates a real, file-backed memory.Store (matching
// production schema/migrations) for trace tests, and returns both the store
// and its underlying pilot.db path so tests can seed deterministic
// occurred_at timestamps directly (InsertExecutionEvent always stamps
// time.Now().UTC(), which golden-file assertions can't depend on).
func newTraceTestStore(t *testing.T) (*memory.Store, string) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "pilot-test-trace-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	return store, filepath.Join(tmpDir, "pilot.db")
}

// seedEventAt inserts an execution_events row with an explicit occurred_at,
// bypassing store.InsertExecutionEvent (which always uses time.Now().UTC())
// so the golden fixture has stable, reproducible timestamps.
func seedEventAt(t *testing.T, dbPath string, executionID string, stage memory.Stage, detail string, occurredAt time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open failed: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(
		`INSERT INTO execution_events (execution_id, stage, occurred_at, detail) VALUES (?, ?, ?, ?)`,
		executionID, string(stage), occurredAt, detail,
	); err != nil {
		t.Fatalf("seed event failed: %v", err)
	}
}

func TestRunTrace_UnknownTaskID(t *testing.T) {
	store, _ := newTraceTestStore(t)

	var buf bytes.Buffer
	err := runTrace(&buf, store, "GH-does-not-exist", "")
	if err == nil {
		t.Fatal("expected error for unknown task id, got nil")
	}
	want := "no executions found for task GH-does-not-exist"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output on error, got %q", buf.String())
	}
}

// TestRunTrace_ProjectCollision covers GH-4378: the same task_id ran in two
// unrelated projects. Without --project and with a cwd that matches neither
// project, trace must list both as candidates and exit non-zero instead of
// interleaving their executions into one timeline.
func TestRunTrace_ProjectCollision(t *testing.T) {
	store, _ := newTraceTestStore(t)

	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-pointer", TaskID: "GH-1", ProjectPath: "/repos/pointer", Status: "completed",
	}); err != nil {
		t.Fatalf("SaveExecution(exec-pointer) failed: %v", err)
	}
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-navigator", TaskID: "GH-1", ProjectPath: "/repos/navigator", Status: "completed",
	}); err != nil {
		t.Fatalf("SaveExecution(exec-navigator) failed: %v", err)
	}

	var buf bytes.Buffer
	err := runTrace(&buf, store, "GH-1", "")
	if err == nil {
		t.Fatal("expected ambiguous-project error, got nil")
	}
	if !strings.Contains(err.Error(), "/repos/pointer") || !strings.Contains(err.Error(), "/repos/navigator") {
		t.Errorf("error = %q, want it to list both candidate projects", err.Error())
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output on ambiguous error, got %q", buf.String())
	}

	// --project resolves the collision directly.
	buf.Reset()
	if err := runTrace(&buf, store, "GH-1", "/repos/pointer"); err != nil {
		t.Fatalf("runTrace with --project failed: %v", err)
	}
	if !strings.Contains(buf.String(), "exec-pointer") {
		t.Errorf("output = %q, want it to include exec-pointer", buf.String())
	}
	if strings.Contains(buf.String(), "exec-navigator") {
		t.Errorf("output = %q, want it to exclude exec-navigator", buf.String())
	}
}

// TestRunTrace_Golden seeds two executions (a failed attempt followed by a
// successful retry) for the same task and checks the rendered timeline
// against a golden fixture: newest execution first, each in its own block,
// with UTC timestamps and inter-stage durations.
func TestRunTrace_Golden(t *testing.T) {
	store, dbPath := newTraceTestStore(t)

	base := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)

	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-1",
		TaskID:      "GH-42",
		ProjectPath: "/project",
		Status:      "failed",
	}); err != nil {
		t.Fatalf("SaveExecution(exec-1) failed: %v", err)
	}
	seedEventAt(t, dbPath, "exec-1", memory.StageQueued, "picked up from queue", base)
	seedEventAt(t, dbPath, "exec-1", memory.StageRunning, "", base.Add(5*time.Second))
	seedEventAt(t, dbPath, "exec-1", memory.StageFailed, "build failed", base.Add(2*time.Minute))

	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-2",
		TaskID:      "GH-42",
		ProjectPath: "/project",
		Status:      "completed",
	}); err != nil {
		t.Fatalf("SaveExecution(exec-2) failed: %v", err)
	}
	retryBase := base.Add(10 * time.Minute)
	seedEventAt(t, dbPath, "exec-2", memory.StageQueued, "retry", retryBase)
	seedEventAt(t, dbPath, "exec-2", memory.StageRunning, "", retryBase.Add(5*time.Second))
	seedEventAt(t, dbPath, "exec-2", memory.StageCommit, "abc123", retryBase.Add(2*time.Minute))
	seedEventAt(t, dbPath, "exec-2", memory.StagePRCreated, "PR #99", retryBase.Add(2*time.Minute+30*time.Second))
	seedEventAt(t, dbPath, "exec-2", memory.StageMerged, "", retryBase.Add(5*time.Minute))

	var buf bytes.Buffer
	if err := runTrace(&buf, store, "GH-42", ""); err != nil {
		t.Fatalf("runTrace failed: %v", err)
	}

	goldenPath := filepath.Join("testdata", "trace_GH-42.golden")
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("failed to read golden file %s: %v", goldenPath, err)
	}

	if buf.String() != string(want) {
		t.Errorf("output mismatch\n--- got ---\n%s\n--- want ---\n%s", buf.String(), string(want))
	}
}

func TestResolveTraceProject(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd failed: %v", err)
	}

	single := []memory.TaskProjectSummary{{ProjectPath: "/repos/only"}}
	collision := []memory.TaskProjectSummary{{ProjectPath: "/repos/newer"}, {ProjectPath: "/repos/older"}}
	collisionWithCwd := []memory.TaskProjectSummary{{ProjectPath: cwd}, {ProjectPath: "/repos/other"}}

	tests := []struct {
		name        string
		projectFlag string
		projects    []memory.TaskProjectSummary
		want        string
	}{
		{"explicit --project always wins", "/repos/explicit", collision, "/repos/explicit"},
		{"single project auto-resolves regardless of cwd", "", single, "/repos/only"},
		{"collision resolved by cwd match", "", collisionWithCwd, cwd},
		{"collision with no cwd match is ambiguous", "", collision, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveTraceProject(tt.projectFlag, tt.projects)
			if got != tt.want {
				t.Errorf("resolveTraceProject(%q, %v) = %q, want %q", tt.projectFlag, tt.projects, got, tt.want)
			}
		})
	}
}
