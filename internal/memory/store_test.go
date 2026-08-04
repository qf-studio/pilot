package memory

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewStore(t *testing.T) {
	// Use temp directory for test
	tmpDir, err := os.MkdirTemp("", "pilot-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Verify database file was created
	dbPath := filepath.Join(tmpDir, "pilot.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file not created")
	}
}

func TestExecutionCRUD(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create
	exec := &Execution{
		ID:          "exec-1",
		TaskID:      "TASK-123",
		ProjectPath: "/path/to/project",
		Status:      "completed",
		Output:      "Success!",
		DurationMs:  5000,
		PRUrl:       "https://github.com/org/repo/pull/1",
		CommitSHA:   "abc123",
	}

	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	// Read
	retrieved, err := store.GetExecution("exec-1")
	if err != nil {
		t.Fatalf("GetExecution failed: %v", err)
	}

	if retrieved.TaskID != "TASK-123" {
		t.Errorf("Expected TaskID 'TASK-123', got '%s'", retrieved.TaskID)
	}
	if retrieved.Status != "completed" {
		t.Errorf("Expected Status 'completed', got '%s'", retrieved.Status)
	}
	if retrieved.PRUrl != "https://github.com/org/repo/pull/1" {
		t.Errorf("Expected PR URL, got '%s'", retrieved.PRUrl)
	}
}

func TestGetRecentExecutions(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Add multiple executions
	for i := 1; i <= 5; i++ {
		exec := &Execution{
			ID:          "exec-" + string(rune('0'+i)),
			TaskID:      "TASK-" + string(rune('0'+i)),
			ProjectPath: "/path",
			Status:      "completed",
		}
		_ = store.SaveExecution(exec)
	}

	recent, err := store.GetRecentExecutions(3, "")
	if err != nil {
		t.Fatalf("GetRecentExecutions failed: %v", err)
	}

	if len(recent) != 3 {
		t.Errorf("Expected 3 executions, got %d", len(recent))
	}
}

// TestGetRecentExecutions_ExcludesCanary covers GH-4240: canary sandbox
// executions must not appear in dashboard queue/history results, whether or
// not a project filter is applied — the ledger row itself is untouched.
func TestGetRecentExecutions_ExcludesCanary(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	if err := store.SaveExecution(&Execution{ID: "re-real", TaskID: "TASK-REAL", ProjectPath: "/path", Status: "completed"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.SaveExecution(&Execution{ID: "re-canary", TaskID: "TASK-CANARY", ProjectPath: "/canary-sandbox", Status: "completed", IsCanary: true}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	recent, err := store.GetRecentExecutions(10, "")
	if err != nil {
		t.Fatalf("GetRecentExecutions failed: %v", err)
	}
	if len(recent) != 1 || recent[0].ID != "re-real" {
		t.Errorf("GetRecentExecutions() = %v, want only [re-real]", recent)
	}

	// The canary row itself is untouched — GetExecution still returns it.
	got, err := store.GetExecution("re-canary")
	if err != nil {
		t.Fatalf("GetExecution(re-canary): %v", err)
	}
	if !got.IsCanary {
		t.Error("GetExecution(re-canary).IsCanary = false, want true")
	}
}

func TestGetLatestExecutionByTaskID(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Two executions of the same task: an older completed run and a newer running one,
	// mirroring a retried/re-dispatched task (GH-3724: "pilot logs GH-<n>" must surface
	// the latest attempt, not miss it).
	older := &Execution{ID: "exec-old", TaskID: "GH-3714", ProjectPath: "/path", Status: "failed"}
	if err := store.SaveExecution(older); err != nil {
		t.Fatalf("SaveExecution(older) failed: %v", err)
	}
	newer := &Execution{ID: "exec-new", TaskID: "GH-3714", ProjectPath: "/path", Status: "running"}
	if err := store.SaveExecution(newer); err != nil {
		t.Fatalf("SaveExecution(newer) failed: %v", err)
	}

	got, err := store.GetLatestExecutionByTaskID("GH-3714", "")
	if err != nil {
		t.Fatalf("GetLatestExecutionByTaskID failed: %v", err)
	}
	if got.ID != "exec-new" {
		t.Errorf("Expected latest execution 'exec-new', got '%s'", got.ID)
	}

	// Partial match, mirroring the previous recordings-based lookup behavior.
	gotPartial, err := store.GetLatestExecutionByTaskID("3714", "")
	if err != nil {
		t.Fatalf("GetLatestExecutionByTaskID (partial) failed: %v", err)
	}
	if gotPartial.ID != "exec-new" {
		t.Errorf("Expected partial match to resolve to 'exec-new', got '%s'", gotPartial.ID)
	}

	if _, err := store.GetLatestExecutionByTaskID("GH-9999", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Expected sql.ErrNoRows for unknown task, got %v", err)
	}

	// GH-4352: project-scoped lookup must not cross projects on a task_id collision.
	if _, err := store.GetLatestExecutionByTaskID("GH-3714", "/other-project"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Expected sql.ErrNoRows for wrong project, got %v", err)
	}
	gotScoped, err := store.GetLatestExecutionByTaskID("GH-3714", "/path")
	if err != nil {
		t.Fatalf("GetLatestExecutionByTaskID (scoped) failed: %v", err)
	}
	if gotScoped.ID != "exec-new" {
		t.Errorf("Expected scoped latest execution 'exec-new', got '%s'", gotScoped.ID)
	}
}

// TestGetLatestExecutionByTaskIDExcluding_ScopedToProject is the GH-4352
// regression test: a task_id collision across two projects (e.g. a sandbox
// canary reusing a low GH-N that's also live in another repo) must not let
// reconcileChildOutcome adopt the wrong project's PR/commit as its child's
// terminal-outcome evidence. Each project's exclude-lookup must resolve only
// its own latest row.
func TestGetLatestExecutionByTaskIDExcluding_ScopedToProject(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const taskID = "GH-4352"

	// Project A: self-row (excluded) + a genuinely separate, concurrently
	// tracked row that is the real reconciliation evidence.
	if err := store.SaveExecution(&Execution{
		ID: "a-self", TaskID: taskID, ProjectPath: "/proj/a", Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution(a-self): %v", err)
	}
	if err := store.SaveExecution(&Execution{
		ID: "a-other", TaskID: taskID, ProjectPath: "/proj/a", Status: "completed",
		PRUrl: "https://github.com/org/proj-a/pull/1", CommitSHA: "aaa111",
	}); err != nil {
		t.Fatalf("SaveExecution(a-other): %v", err)
	}

	// Project B: same task_id, self-row (excluded) + its own, different
	// terminal evidence row.
	if err := store.SaveExecution(&Execution{
		ID: "b-self", TaskID: taskID, ProjectPath: "/proj/b", Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution(b-self): %v", err)
	}
	if err := store.SaveExecution(&Execution{
		ID: "b-other", TaskID: taskID, ProjectPath: "/proj/b", Status: "completed",
		PRUrl: "https://github.com/org/proj-b/pull/2", CommitSHA: "bbb222",
	}); err != nil {
		t.Fatalf("SaveExecution(b-other): %v", err)
	}

	tests := []struct {
		name        string
		projectPath string
		excludeID   string
		wantID      string
		wantPRUrl   string
	}{
		{"project A resolves only its own row", "/proj/a", "a-self", "a-other", "https://github.com/org/proj-a/pull/1"},
		{"project B resolves only its own row", "/proj/b", "b-self", "b-other", "https://github.com/org/proj-b/pull/2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.GetLatestExecutionByTaskIDExcluding(taskID, tt.projectPath, tt.excludeID)
			if err != nil {
				t.Fatalf("GetLatestExecutionByTaskIDExcluding: %v", err)
			}
			if got.ID != tt.wantID {
				t.Errorf("ID = %q, want %q (adopted wrong project's row)", got.ID, tt.wantID)
			}
			if got.PRUrl != tt.wantPRUrl {
				t.Errorf("PRUrl = %q, want %q (adopted wrong project's evidence)", got.PRUrl, tt.wantPRUrl)
			}
		})
	}

	// Empty projectPath preserves pre-GH-4352 behavior: unscoped, falls back
	// to created_at ordering across both projects' rows.
	if _, err := store.GetLatestExecutionByTaskIDExcluding(taskID, "", "a-self"); err != nil {
		t.Fatalf("GetLatestExecutionByTaskIDExcluding (empty projectPath): %v", err)
	}
}

func TestGetLogsByExecutionID(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	base := time.Now()
	messages := []string{"starting", "planning", "implementing", "done"}
	for i, msg := range messages {
		entry := &LogEntry{
			ExecutionID: "GH-3714",
			Timestamp:   base.Add(time.Duration(i) * time.Second),
			Level:       "info",
			Message:     msg,
			Component:   "executor",
		}
		if err := store.SaveLogEntry(entry); err != nil {
			t.Fatalf("SaveLogEntry failed: %v", err)
		}
	}
	// A log line for a different task must not leak into the result.
	if err := store.SaveLogEntry(&LogEntry{ExecutionID: "GH-9999", Timestamp: base, Level: "info", Message: "other task", Component: "executor"}); err != nil {
		t.Fatalf("SaveLogEntry (other task) failed: %v", err)
	}

	logs, err := store.GetLogsByExecutionID("GH-3714", 100)
	if err != nil {
		t.Fatalf("GetLogsByExecutionID failed: %v", err)
	}

	if len(logs) != len(messages) {
		t.Fatalf("Expected %d log entries, got %d", len(messages), len(logs))
	}

	// Entries must come back in chronological order.
	for i, entry := range logs {
		if entry.Message != messages[i] {
			t.Errorf("Log entry %d: expected message %q, got %q", i, messages[i], entry.Message)
		}
	}

	// limit keeps the most recent entries, in chronological order.
	limited, err := store.GetLogsByExecutionID("GH-3714", 2)
	if err != nil {
		t.Fatalf("GetLogsByExecutionID (limited) failed: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("Expected 2 log entries, got %d", len(limited))
	}
	if limited[0].Message != "implementing" || limited[1].Message != "done" {
		t.Errorf("Expected the 2 most recent entries in order, got %q, %q", limited[0].Message, limited[1].Message)
	}
}

func TestPatternCRUD(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	pattern := &Pattern{
		ProjectPath: "/path/to/project",
		Type:        "code",
		Content:     "Always use error wrapping",
		Confidence:  0.9,
	}

	if err := store.SavePattern(pattern); err != nil {
		t.Fatalf("SavePattern failed: %v", err)
	}

	if pattern.ID == 0 {
		t.Error("Pattern ID not set after save")
	}

	patterns, err := store.GetPatterns("/path/to/project")
	if err != nil {
		t.Fatalf("GetPatterns failed: %v", err)
	}

	if len(patterns) != 1 {
		t.Errorf("Expected 1 pattern, got %d", len(patterns))
	}
}

func TestProjectCRUD(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	project := &Project{
		Path:             "/path/to/project",
		Name:             "my-project",
		NavigatorEnabled: true,
		LastActive:       time.Now(),
		Settings:         map[string]interface{}{"theme": "dark"},
	}

	if err := store.SaveProject(project); err != nil {
		t.Fatalf("SaveProject failed: %v", err)
	}

	retrieved, err := store.GetProject("/path/to/project")
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}

	if retrieved.Name != "my-project" {
		t.Errorf("Expected name 'my-project', got '%s'", retrieved.Name)
	}
	if !retrieved.NavigatorEnabled {
		t.Error("Expected NavigatorEnabled to be true")
	}
}

func TestGetAllProjects(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	_ = store.SaveProject(&Project{Path: "/path/1", Name: "project-1"})
	_ = store.SaveProject(&Project{Path: "/path/2", Name: "project-2"})

	projects, err := store.GetAllProjects()
	if err != nil {
		t.Fatalf("GetAllProjects failed: %v", err)
	}

	if len(projects) != 2 {
		t.Errorf("Expected 2 projects, got %d", len(projects))
	}
}

func TestExecution_FullLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer func() { _ = store.Close() }()

	completedAt := time.Now()
	exec := &Execution{
		ID:               "exec-full-1",
		TaskID:           "TASK-456",
		ProjectPath:      "/path/to/project",
		Status:           "completed",
		Output:           "Build succeeded. All tests passed.",
		Error:            "",
		DurationMs:       15000,
		PRUrl:            "https://github.com/org/repo/pull/42",
		CommitSHA:        "abc123def456",
		CompletedAt:      &completedAt,
		TokensInput:      10000,
		TokensOutput:     5000,
		TokensTotal:      15000,
		EstimatedCostUSD: 0.15,
		FilesChanged:     5,
		LinesAdded:       100,
		LinesRemoved:     20,
		ModelName:        "claude-sonnet-4-6",
	}

	// Save
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	// Retrieve
	retrieved, err := store.GetExecution("exec-full-1")
	if err != nil {
		t.Fatalf("GetExecution failed: %v", err)
	}

	// Verify all fields
	tests := []struct {
		name     string
		got      interface{}
		expected interface{}
	}{
		{"ID", retrieved.ID, exec.ID},
		{"TaskID", retrieved.TaskID, exec.TaskID},
		{"ProjectPath", retrieved.ProjectPath, exec.ProjectPath},
		{"Status", retrieved.Status, exec.Status},
		{"Output", retrieved.Output, exec.Output},
		{"DurationMs", retrieved.DurationMs, exec.DurationMs},
		{"PRUrl", retrieved.PRUrl, exec.PRUrl},
		{"CommitSHA", retrieved.CommitSHA, exec.CommitSHA},
		{"TokensInput", retrieved.TokensInput, exec.TokensInput},
		{"TokensOutput", retrieved.TokensOutput, exec.TokensOutput},
		{"TokensTotal", retrieved.TokensTotal, exec.TokensTotal},
		{"FilesChanged", retrieved.FilesChanged, exec.FilesChanged},
		{"LinesAdded", retrieved.LinesAdded, exec.LinesAdded},
		{"LinesRemoved", retrieved.LinesRemoved, exec.LinesRemoved},
		{"ModelName", retrieved.ModelName, exec.ModelName},
	}

	for _, tt := range tests {
		if tt.got != tt.expected {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.expected)
		}
	}

	if retrieved.CompletedAt == nil {
		t.Error("CompletedAt should not be nil")
	}
}

func TestGetExecution_NotFound(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	_, err := store.GetExecution("nonexistent")
	if err == nil {
		t.Error("GetExecution should return error for nonexistent execution")
	}
}

func TestHasCompletedExecution(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// No executions yet — should return false
	completed, err := store.HasCompletedExecution("GH-42", "/project")
	if err != nil {
		t.Fatalf("HasCompletedExecution failed: %v", err)
	}
	if completed {
		t.Error("expected false for non-existent task")
	}

	// Save a non-completed execution
	_ = store.SaveExecution(&Execution{
		ID:          "exec-pending",
		TaskID:      "GH-42",
		ProjectPath: "/project",
		Status:      "running",
	})
	completed, err = store.HasCompletedExecution("GH-42", "/project")
	if err != nil {
		t.Fatalf("HasCompletedExecution failed: %v", err)
	}
	if completed {
		t.Error("expected false for running task")
	}

	// Save a completed execution with a deliverable (commit_sha set).
	_ = store.SaveExecution(&Execution{
		ID:          "exec-done",
		TaskID:      "GH-42",
		ProjectPath: "/project",
		Status:      "completed",
		CommitSHA:   "abc123",
	})
	completed, err = store.HasCompletedExecution("GH-42", "/project")
	if err != nil {
		t.Fatalf("HasCompletedExecution failed: %v", err)
	}
	if !completed {
		t.Error("expected true for completed task with deliverable")
	}

	// Completed but no deliverables (epic-parent false-positive pattern, TASK-296).
	_ = store.SaveExecution(&Execution{
		ID:          "exec-epic",
		TaskID:      "GH-43",
		ProjectPath: "/project",
		Status:      "completed",
	})
	completed, err = store.HasCompletedExecution("GH-43", "/project")
	if err != nil {
		t.Fatalf("HasCompletedExecution failed: %v", err)
	}
	if completed {
		t.Error("expected false for completed task with no deliverable (epic-parent false-positive)")
	}

	// Different project path — should return false
	completed, _ = store.HasCompletedExecution("GH-42", "/other-project")
	if completed {
		t.Error("expected false for different project path")
	}
}

// TestIsTaskQueued_ProjectScoping is the GH-4276 regression: task_id is not
// unique across projects (every freshly onboarded repo starts issue
// numbering at #1), so IsTaskQueued must not treat a task_id queued/running
// in one project as active in a different project sharing the same id.
func TestIsTaskQueued_ProjectScoping(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// project-a has GH-10 running; project-b has never seen GH-10 before.
	_ = store.SaveExecution(&Execution{
		ID:          "exec-running-a",
		TaskID:      "GH-10",
		ProjectPath: "/project-a",
		Status:      "running",
	})

	queued, err := store.IsTaskQueued("GH-10", "/project-a")
	if err != nil {
		t.Fatalf("IsTaskQueued failed: %v", err)
	}
	if !queued {
		t.Error("expected GH-10 to be queued in /project-a")
	}

	queued, err = store.IsTaskQueued("GH-10", "/project-b")
	if err != nil {
		t.Fatalf("IsTaskQueued failed: %v", err)
	}
	if queued {
		t.Error("expected GH-10 to NOT be queued in /project-b (cross-project collision)")
	}

	// project-b later gets its own GH-10 queued — must be visible only there.
	_ = store.SaveExecution(&Execution{
		ID:          "exec-queued-b",
		TaskID:      "GH-10",
		ProjectPath: "/project-b",
		Status:      "queued",
	})
	queued, err = store.IsTaskQueued("GH-10", "/project-b")
	if err != nil {
		t.Fatalf("IsTaskQueued failed: %v", err)
	}
	if !queued {
		t.Error("expected GH-10 to be queued in /project-b once its own row exists")
	}
}

// TestIsTaskQueued_CanonicalizesProjectPath is the mem-019/GH-4297
// discriminator-mismatch regression for the read side: IsTaskQueued must
// reuse canonicalizeProjectPath (as ClaimExecution already does at write
// time) so a caller querying with a trailing separator or "./.." segments
// still finds a row saved under the equivalent clean path, instead of
// silently missing it and dispatching a duplicate.
func TestIsTaskQueued_CanonicalizesProjectPath(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{
		ID:          "exec-canonical",
		TaskID:      "GH-11",
		ProjectPath: "/tmp/project-x",
		Status:      "running",
	})

	queued, err := store.IsTaskQueued("GH-11", "/tmp/project-x/")
	if err != nil {
		t.Fatalf("IsTaskQueued failed: %v", err)
	}
	if !queued {
		t.Error("expected trailing-slash query to match the row saved under the clean path")
	}

	queued, err = store.IsTaskQueued("GH-11", "/tmp/./project-x")
	if err != nil {
		t.Fatalf("IsTaskQueued failed: %v", err)
	}
	if !queued {
		t.Error("expected a query with a '.' segment to match the row saved under the clean path")
	}
}

// TestClaimExecution_SecondCallerLoses is the TASK-407/GH-4349 atomic-claim
// idiom test (the INSERT OR IGNORE + RowsAffected()==1 pattern from
// ClaimSpawnedFix, internal/autopilot/state_store.go:1062): a second
// ClaimExecution for the same (task_id, project_path, generation) must not
// win, regardless of which execution_id it names.
func TestClaimExecution_SecondCallerLoses(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	claimed, err := store.ClaimExecution("GH-20", "/project", 0, "exec-a")
	if err != nil {
		t.Fatalf("ClaimExecution failed: %v", err)
	}
	if !claimed {
		t.Fatal("expected first ClaimExecution to win")
	}

	claimed, err = store.ClaimExecution("GH-20", "/project", 0, "exec-b")
	if err != nil {
		t.Fatalf("ClaimExecution failed: %v", err)
	}
	if claimed {
		t.Error("expected second ClaimExecution for the same key to lose")
	}
}

// TestClaimExecution_DifferentGenerationClaimsAfresh verifies generation is
// part of the claim key: a retry claiming generation+1 wins its own row
// instead of losing to the prior generation's claim.
func TestClaimExecution_DifferentGenerationClaimsAfresh(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	claimed, err := store.ClaimExecution("GH-21", "/project", 0, "exec-gen0")
	if err != nil || !claimed {
		t.Fatalf("expected generation 0 claim to win, claimed=%v err=%v", claimed, err)
	}

	claimed, err = store.ClaimExecution("GH-21", "/project", 1, "exec-gen1")
	if err != nil {
		t.Fatalf("ClaimExecution failed: %v", err)
	}
	if !claimed {
		t.Error("expected generation 1 claim to win despite generation 0 already claimed")
	}
}

// TestClaimExecution_ProjectScoping mirrors TestIsTaskQueued_ProjectScoping:
// the same task_id in two different projects must claim independently
// (mem-019/GH-4297's discriminator-mismatch class, applied to the claim key).
func TestClaimExecution_ProjectScoping(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	claimed, err := store.ClaimExecution("GH-22", "/project-a", 0, "exec-a")
	if err != nil || !claimed {
		t.Fatalf("expected /project-a claim to win, claimed=%v err=%v", claimed, err)
	}

	claimed, err = store.ClaimExecution("GH-22", "/project-b", 0, "exec-b")
	if err != nil {
		t.Fatalf("ClaimExecution failed: %v", err)
	}
	if !claimed {
		t.Error("expected /project-b to claim its own row independent of /project-a")
	}
}

// TestRepickBackoff_PersistsAcrossStoreReopen is the GH-4394 regression test:
// the whole point of persisting repick-backoff state (rather than keeping it
// purely in-process, as it was under #4385) is that a fresh Store handle —
// standing in for a daemon restart or a second process opening the same DB
// file — sees the SAME cooldown a prior handle recorded, instead of it
// resetting to "not found" / zero drops.
func TestRepickBackoff_PersistsAcrossStoreReopen(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	key := "/project|GH-85"

	store1, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	nextAllowedAt := time.Now().Add(2 * time.Minute).Truncate(time.Second)
	if err := store1.SetRepickBackoff(key, 3, nextAllowedAt); err != nil {
		t.Fatalf("SetRepickBackoff failed: %v", err)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Simulate a restart: a brand new Store handle over the same data dir.
	store2, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore (reopen) failed: %v", err)
	}
	defer func() { _ = store2.Close() }()

	drops, gotNextAllowedAt, found, err := store2.GetRepickBackoff(key)
	if err != nil {
		t.Fatalf("GetRepickBackoff failed: %v", err)
	}
	if !found {
		t.Fatal("expected repick backoff state to survive a store reopen (restart)")
	}
	if drops != 3 {
		t.Errorf("expected consecutive_drops=3 to survive reopen, got %d", drops)
	}
	if !gotNextAllowedAt.Equal(nextAllowedAt) {
		t.Errorf("expected next_allowed_at=%v to survive reopen, got %v", nextAllowedAt, gotNextAllowedAt)
	}
}

// TestRepickBackoff_GetMissingKeyNotFound verifies a key with no recorded
// drop reports found=false rather than a zero-value row — the normal
// "not throttled" case for a task that has never dropped.
func TestRepickBackoff_GetMissingKeyNotFound(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	_, _, found, err := store.GetRepickBackoff("/project|GH-does-not-exist")
	if err != nil {
		t.Fatalf("GetRepickBackoff failed: %v", err)
	}
	if found {
		t.Error("expected a never-recorded key to report found=false")
	}
}

// TestRepickBackoff_SetOverwritesExistingRow verifies a second SetRepickBackoff
// call for the same key replaces the prior state (the growing-cooldown case:
// each consecutive drop overwrites the previous count/deadline) rather than
// erroring on the existing primary key or leaving stale data behind.
func TestRepickBackoff_SetOverwritesExistingRow(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	key := "/project|GH-4394"
	if err := store.SetRepickBackoff(key, 1, time.Now().Add(30*time.Second)); err != nil {
		t.Fatalf("SetRepickBackoff (1st) failed: %v", err)
	}
	secondDeadline := time.Now().Add(60 * time.Second).Truncate(time.Second)
	if err := store.SetRepickBackoff(key, 2, secondDeadline); err != nil {
		t.Fatalf("SetRepickBackoff (2nd) failed: %v", err)
	}

	drops, nextAllowedAt, found, err := store.GetRepickBackoff(key)
	if err != nil {
		t.Fatalf("GetRepickBackoff failed: %v", err)
	}
	if !found {
		t.Fatal("expected key to be found after two SetRepickBackoff calls")
	}
	if drops != 2 {
		t.Errorf("expected the second call's consecutive_drops=2 to win, got %d", drops)
	}
	if !nextAllowedAt.Equal(secondDeadline) {
		t.Errorf("expected the second call's deadline %v to win, got %v", secondDeadline, nextAllowedAt)
	}
}

// TestRepickBackoff_ClearRemovesRow verifies ClearRepickBackoff (the
// recordSuccess path) removes the row entirely, so a subsequent Get reports
// found=false rather than a lingering stale entry.
func TestRepickBackoff_ClearRemovesRow(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	key := "/project|GH-4370"
	if err := store.SetRepickBackoff(key, 2, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("SetRepickBackoff failed: %v", err)
	}
	if err := store.ClearRepickBackoff(key); err != nil {
		t.Fatalf("ClearRepickBackoff failed: %v", err)
	}

	_, _, found, err := store.GetRepickBackoff(key)
	if err != nil {
		t.Fatalf("GetRepickBackoff failed: %v", err)
	}
	if found {
		t.Error("expected ClearRepickBackoff to remove the row")
	}

	// Clearing a key that was never set must be a no-op, not an error.
	if err := store.ClearRepickBackoff("/project|GH-never-set"); err != nil {
		t.Errorf("expected clearing a nonexistent key to be a no-op, got error: %v", err)
	}
}

// TestGetDecomposedChildTaskIDs covers the GH-4216 (Defect A, fix 3)
// cross-task-id dispatch guard's read helper: no decomposed event, a
// decomposed event with children, duplicate refs collapsed, and project-path
// scoping.
func TestGetDecomposedChildTaskIDs(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// No execution at all for this task_id yet.
	childIDs, found, err := store.GetDecomposedChildTaskIDs("GH-4211", "/project")
	if err != nil {
		t.Fatalf("GetDecomposedChildTaskIDs failed: %v", err)
	}
	if found {
		t.Error("expected found=false for task with no execution rows")
	}
	if len(childIDs) != 0 {
		t.Errorf("expected no child IDs, got %v", childIDs)
	}

	// Execution exists but never decomposed (plain direct task).
	_ = store.SaveExecution(&Execution{
		ID:          "exec-direct",
		TaskID:      "GH-4300",
		ProjectPath: "/project",
		Status:      "completed",
		CommitSHA:   "abc123",
	})
	childIDs, found, err = store.GetDecomposedChildTaskIDs("GH-4300", "/project")
	if err != nil {
		t.Fatalf("GetDecomposedChildTaskIDs failed: %v", err)
	}
	if found {
		t.Error("expected found=false for a task that never decomposed")
	}
	if len(childIDs) != 0 {
		t.Errorf("expected no child IDs, got %v", childIDs)
	}

	// Epic parent that decomposed into two children (GH-4211 repro shape).
	_ = store.SaveExecution(&Execution{
		ID:          "exec-4211",
		TaskID:      "GH-4211",
		ProjectPath: "/project",
		Status:      "failed",
		Error:       "epic PR creation failed: title is not a conventional commit",
	})
	if err := store.InsertExecutionEvent("exec-4211", StageDecomposed, "decomposed into 2 children: #4212, #4213"); err != nil {
		t.Fatalf("InsertExecutionEvent failed: %v", err)
	}

	childIDs, found, err = store.GetDecomposedChildTaskIDs("GH-4211", "/project")
	if err != nil {
		t.Fatalf("GetDecomposedChildTaskIDs failed: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for a decomposed task")
	}
	wantChildren := []string{"GH-4212", "GH-4213"}
	if !equalStringSlices(childIDs, wantChildren) {
		t.Errorf("childIDs = %v, want %v", childIDs, wantChildren)
	}

	// Different project path — must not see the other project's decomposed event.
	_, found, err = store.GetDecomposedChildTaskIDs("GH-4211", "/other-project")
	if err != nil {
		t.Fatalf("GetDecomposedChildTaskIDs failed: %v", err)
	}
	if found {
		t.Error("expected found=false for a different project path")
	}

	// Duplicate refs across repeated decomposed events collapse to one entry each.
	_ = store.SaveExecution(&Execution{
		ID:          "exec-dup",
		TaskID:      "GH-4400",
		ProjectPath: "/project",
		Status:      "failed",
	})
	if err := store.InsertExecutionEvent("exec-dup", StageDecomposed, "decomposed into 1 children: #4401"); err != nil {
		t.Fatalf("InsertExecutionEvent failed: %v", err)
	}
	if err := store.InsertExecutionEvent("exec-dup", StageDecomposed, "decomposed into 1 children: #4401"); err != nil {
		t.Fatalf("InsertExecutionEvent failed: %v", err)
	}
	childIDs, found, err = store.GetDecomposedChildTaskIDs("GH-4400", "/project")
	if err != nil {
		t.Fatalf("GetDecomposedChildTaskIDs failed: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if !equalStringSlices(childIDs, []string{"GH-4401"}) {
		t.Errorf("childIDs = %v, want [GH-4401]", childIDs)
	}
}

// TestGetDecomposedChildren covers GH-4226: the taskID-only (not
// project-path-scoped) decomposed-children reader used by callers that only
// have a task_id in hand. Table-driven over well-formed, absent, malformed
// (missing colon / non-numeric child / empty list), and latest-of-multiple.
func TestGetDecomposedChildren(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	tests := []struct {
		name      string
		taskID    string
		execID    string
		details   []string // inserted in order; empty slice means no event at all
		wantChild []string
		wantFound bool
		skipSetup bool // task ID has no execution row at all
	}{
		{
			name:      "well-formed detail",
			taskID:    "GH-5001",
			execID:    "exec-5001",
			details:   []string{"decomposed into 3 children: #5002, #5003, #5004"},
			wantChild: []string{"5002", "5003", "5004"},
			wantFound: true,
		},
		{
			name:      "missing event",
			taskID:    "GH-5100",
			skipSetup: true,
			wantChild: nil,
			wantFound: false,
		},
		{
			name:      "malformed - missing colon",
			taskID:    "GH-5200",
			execID:    "exec-5200",
			details:   []string{"decomposed into 2 children #5201, #5202"},
			wantChild: nil,
			wantFound: false,
		},
		{
			name:      "malformed - non-numeric child",
			taskID:    "GH-5300",
			execID:    "exec-5300",
			details:   []string{"decomposed into 2 children: #abc, #5302"},
			wantChild: nil,
			wantFound: false,
		},
		{
			name:      "malformed - empty list",
			taskID:    "GH-5400",
			execID:    "exec-5400",
			details:   []string{"decomposed into 0 children: "},
			wantChild: nil,
			wantFound: false,
		},
		{
			name:   "multiple decomposed events - use latest",
			taskID: "GH-5500",
			execID: "exec-5500",
			details: []string{
				"decomposed into 1 children: #5501",
				"decomposed into 2 children: #5502, #5503",
			},
			wantChild: []string{"5502", "5503"},
			wantFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.skipSetup {
				if err := store.SaveExecution(&Execution{
					ID:          tt.execID,
					TaskID:      tt.taskID,
					ProjectPath: "/project",
					Status:      "failed",
				}); err != nil {
					t.Fatalf("SaveExecution failed: %v", err)
				}
				for _, d := range tt.details {
					if err := store.InsertExecutionEvent(tt.execID, StageDecomposed, d); err != nil {
						t.Fatalf("InsertExecutionEvent failed: %v", err)
					}
				}
			}

			gotChild, gotFound := store.GetDecomposedChildren(tt.taskID)
			if gotFound != tt.wantFound {
				t.Errorf("found = %v, want %v", gotFound, tt.wantFound)
			}
			if !equalStringSlices(gotChild, tt.wantChild) {
				t.Errorf("children = %v, want %v", gotChild, tt.wantChild)
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestHasCompletedExecution_OrphanRecovery verifies that a completed execution
// with a non-empty error field (e.g., from orphan recovery) does NOT count as
// completed. This prevents orphan-recovered executions from blocking re-dispatch.
// GH-2315: Defense-in-depth against orphan recovery blocking re-dispatch.
func TestHasCompletedExecution_OrphanRecovery(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	taskID := "GH-2305"
	projectPath := "/project"

	// Simulate 5 failed executions (original scenario from GH-2314)
	for i := 0; i < 5; i++ {
		execID := fmt.Sprintf("exec-failed-%d", i)
		_ = store.SaveExecution(&Execution{
			ID:          execID,
			TaskID:      taskID,
			ProjectPath: projectPath,
			Status:      "failed",
		})
	}

	// Simulate orphan recovery: marks stale running task as "completed" with error
	_ = store.SaveExecution(&Execution{
		ID:          "exec-orphan",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "running",
	})
	// Orphan recovery calls UpdateExecutionStatus with error message
	_ = store.UpdateExecutionStatus("exec-orphan", "completed", "stale running task recovered (orphaned worker)")

	// The orphan-recovered "completed" execution should NOT count as completed
	completed, err := store.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasCompletedExecution failed: %v", err)
	}
	if completed {
		t.Error("expected false — orphan-recovered execution with error should not block re-dispatch")
	}

	// Now add a genuine completed execution (no error, has deliverable).
	_ = store.SaveExecution(&Execution{
		ID:          "exec-genuine",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "completed",
		CommitSHA:   "deadbeef",
	})
	completed, err = store.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasCompletedExecution failed: %v", err)
	}
	if !completed {
		t.Error("expected true — genuine completed execution with deliverable should be found")
	}
}

func TestPattern_Update(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create pattern
	pattern := &Pattern{
		ProjectPath: "/path/to/project",
		Type:        "code",
		Content:     "Original content",
		Confidence:  0.7,
	}

	if err := store.SavePattern(pattern); err != nil {
		t.Fatalf("SavePattern (create) failed: %v", err)
	}

	originalID := pattern.ID
	if originalID == 0 {
		t.Fatal("Pattern ID should be set after create")
	}

	// Update pattern
	pattern.Content = "Updated content"
	pattern.Confidence = 0.9

	if err := store.SavePattern(pattern); err != nil {
		t.Fatalf("SavePattern (update) failed: %v", err)
	}

	// Verify update
	patterns, err := store.GetPatterns("/path/to/project")
	if err != nil {
		t.Fatalf("GetPatterns failed: %v", err)
	}

	if len(patterns) != 1 {
		t.Fatalf("Expected 1 pattern, got %d", len(patterns))
	}

	if patterns[0].Content != "Updated content" {
		t.Errorf("Content = %q, want 'Updated content'", patterns[0].Content)
	}
	if patterns[0].Confidence != 0.9 {
		t.Errorf("Confidence = %f, want 0.9", patterns[0].Confidence)
	}
}

func TestGetActiveExecutions(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Add executions with different statuses
	executions := []*Execution{
		{ID: "1", TaskID: "T1", ProjectPath: "/p", Status: "running"},
		{ID: "2", TaskID: "T2", ProjectPath: "/p", Status: "completed"},
		{ID: "3", TaskID: "T3", ProjectPath: "/p", Status: "running"},
		{ID: "4", TaskID: "T4", ProjectPath: "/p", Status: "failed"},
	}

	for _, e := range executions {
		_ = store.SaveExecution(e)
	}

	active, err := store.GetActiveExecutions()
	if err != nil {
		t.Fatalf("GetActiveExecutions failed: %v", err)
	}

	if len(active) != 2 {
		t.Errorf("Expected 2 active executions, got %d", len(active))
	}

	for _, e := range active {
		if e.Status != "running" {
			t.Errorf("Active execution has status %q, want 'running'", e.Status)
		}
	}
}

func TestGetProject_InvalidSettingsJSON(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Insert project with invalid JSON settings directly into DB
	_, err := store.db.Exec(`
		INSERT INTO projects (path, name, navigator_enabled, settings)
		VALUES (?, ?, ?, ?)
	`, "/test/project", "test", true, "invalid-json{{{")
	if err != nil {
		t.Fatalf("failed to insert test data: %v", err)
	}

	// Capture slog output
	var buf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(oldLogger)

	// Should not return error, but should log warning
	project, err := store.GetProject("/test/project")
	if err != nil {
		t.Errorf("GetProject should not error on invalid settings JSON: %v", err)
	}
	if project == nil {
		t.Fatal("project should not be nil")
	}
	if project.Settings != nil {
		t.Errorf("Settings should be nil after unmarshal failure, got %v", project.Settings)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "failed to unmarshal project settings") {
		t.Errorf("expected warning log about unmarshal failure, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "/test/project") {
		t.Errorf("expected project path in log, got: %s", logOutput)
	}
}

func TestGetAllProjects_InvalidSettingsJSON(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Insert valid and invalid projects
	_, _ = store.db.Exec(`INSERT INTO projects (path, name, navigator_enabled, settings) VALUES (?, ?, ?, ?)`,
		"/valid/project", "valid", true, `{"theme":"dark"}`)
	_, _ = store.db.Exec(`INSERT INTO projects (path, name, navigator_enabled, settings) VALUES (?, ?, ?, ?)`,
		"/invalid/project", "invalid", true, "not-valid-json")

	var buf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(oldLogger)

	projects, err := store.GetAllProjects()
	if err != nil {
		t.Errorf("GetAllProjects should not error: %v", err)
	}
	if len(projects) != 2 {
		t.Errorf("expected 2 projects, got %d", len(projects))
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "failed to unmarshal project settings") {
		t.Errorf("expected warning log, got: %s", logOutput)
	}
}

func TestGetCrossPattern_InvalidExamplesJSON(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Insert pattern with invalid examples JSON
	_, err := store.db.Exec(`
		INSERT INTO cross_patterns (id, pattern_type, title, description, context, examples, confidence, occurrences, is_anti_pattern, scope)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "pat-1", "testing", "Test Pattern", "desc", "ctx", "invalid[json", 0.9, 5, false, "global")
	if err != nil {
		t.Fatalf("failed to insert test data: %v", err)
	}

	var buf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(oldLogger)

	pattern, err := store.GetCrossPattern("pat-1")
	if err != nil {
		t.Errorf("GetCrossPattern should not error: %v", err)
	}
	if pattern == nil {
		t.Fatal("pattern should not be nil")
	}
	if pattern.Examples != nil {
		t.Errorf("Examples should be nil after unmarshal failure")
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "failed to unmarshal cross pattern examples") {
		t.Errorf("expected warning log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "pat-1") {
		t.Errorf("expected pattern ID in log, got: %s", logOutput)
	}
}

func TestScanCrossPatterns_InvalidExamplesJSON(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Insert patterns with valid and invalid examples
	_, _ = store.db.Exec(`
		INSERT INTO cross_patterns (id, pattern_type, title, description, context, examples, confidence, occurrences, is_anti_pattern, scope)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "pat-valid", "testing", "Valid", "desc", "ctx", `["example1","example2"]`, 0.9, 3, false, "global")
	_, _ = store.db.Exec(`
		INSERT INTO cross_patterns (id, pattern_type, title, description, context, examples, confidence, occurrences, is_anti_pattern, scope)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "pat-invalid", "testing", "Invalid", "desc", "ctx", "{broken", 0.8, 2, false, "global")

	var buf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(oldLogger)

	patterns, err := store.GetCrossPatternsByType("testing")
	if err != nil {
		t.Errorf("GetCrossPatternsByType should not error: %v", err)
	}
	if len(patterns) != 2 {
		t.Errorf("expected 2 patterns, got %d", len(patterns))
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "failed to unmarshal cross pattern examples") {
		t.Errorf("expected warning log, got: %s", logOutput)
	}
}

func TestGetQueuedTasks(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Add executions with different statuses
	executions := []*Execution{
		{ID: "1", TaskID: "T1", ProjectPath: "/p", Status: "queued"},
		{ID: "2", TaskID: "T2", ProjectPath: "/p", Status: "pending"},
		{ID: "3", TaskID: "T3", ProjectPath: "/p", Status: "running"},
		{ID: "4", TaskID: "T4", ProjectPath: "/p", Status: "queued"},
	}

	for _, e := range executions {
		_ = store.SaveExecution(e)
	}

	queued, err := store.GetQueuedTasks(10)
	if err != nil {
		t.Fatalf("GetQueuedTasks failed: %v", err)
	}

	if len(queued) != 3 {
		t.Errorf("Expected 3 queued/pending tasks, got %d", len(queued))
	}
}

// TestGetTasksForMonitorHydration verifies the executor.Monitor restart-hydration
// source (GH-4246): queued/pending/running rows come back with title/issue-id
// metadata, running rows carry StartedAt, and terminal rows are excluded.
func TestGetTasksForMonitorHydration(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	executions := []*Execution{
		{ID: "1", TaskID: "GH-1", ProjectPath: "/p", Status: "queued", TaskTitle: "Queued task", TaskSourceIssueID: "1"},
		{ID: "2", TaskID: "GH-2", ProjectPath: "/p", Status: "pending", TaskTitle: "Pending task", TaskSourceIssueID: "2"},
		{ID: "3", TaskID: "GH-3", ProjectPath: "/p", Status: "running", TaskTitle: "Running task", TaskSourceIssueID: "3"},
		{ID: "4", TaskID: "GH-4", ProjectPath: "/p", Status: "completed", TaskTitle: "Done task"},
	}
	for _, e := range executions {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution(%s): %v", e.ID, err)
		}
	}
	// Stamp started_at on the running row, mirroring the live UpdateExecutionStatus path.
	if err := store.UpdateExecutionStatus("3", "running"); err != nil {
		t.Fatalf("UpdateExecutionStatus: %v", err)
	}

	tasks, err := store.GetTasksForMonitorHydration()
	if err != nil {
		t.Fatalf("GetTasksForMonitorHydration failed: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("Expected 3 non-terminal tasks, got %d", len(tasks))
	}

	byID := make(map[string]*HydrationTask)
	for _, task := range tasks {
		byID[task.TaskID] = task
	}
	if byID["GH-4"] != nil {
		t.Error("Completed task should not be included in hydration set")
	}
	running, ok := byID["GH-3"]
	if !ok {
		t.Fatal("Running task GH-3 missing from hydration set")
	}
	if running.Title != "Running task" || running.IssueURL != "3" {
		t.Errorf("Unexpected metadata for GH-3: title=%q issueURL=%q", running.Title, running.IssueURL)
	}
	if running.StartedAt == nil {
		t.Error("Running task should carry a non-nil StartedAt")
	}
	queued, ok := byID["GH-1"]
	if !ok {
		t.Fatal("Queued task GH-1 missing from hydration set")
	}
	if queued.Status != "queued" {
		t.Errorf("Expected status 'queued', got %q", queued.Status)
	}
}

func TestCountQueuedTasks(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	n, err := store.CountQueuedTasks()
	if err != nil {
		t.Fatalf("CountQueuedTasks failed: %v", err)
	}
	if n != 0 {
		t.Errorf("Expected 0 with empty store, got %d", n)
	}

	executions := []*Execution{
		{ID: "1", TaskID: "T1", ProjectPath: "/p", Status: "queued"},
		{ID: "2", TaskID: "T2", ProjectPath: "/p", Status: "pending"},
		{ID: "3", TaskID: "T3", ProjectPath: "/p", Status: "running"},
		{ID: "4", TaskID: "T4", ProjectPath: "/p", Status: "completed"},
	}
	for _, e := range executions {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution(%s): %v", e.ID, err)
		}
	}

	n, err = store.CountQueuedTasks()
	if err != nil {
		t.Fatalf("CountQueuedTasks failed: %v", err)
	}
	if n != 2 {
		t.Errorf("Expected 2 queued/pending, got %d", n)
	}

	// Drain the queue and confirm the count returns to 0.
	if err := store.UpdateExecutionStatus("1", "completed"); err != nil {
		t.Fatalf("UpdateExecutionStatus: %v", err)
	}
	if err := store.UpdateExecutionStatus("2", "completed"); err != nil {
		t.Fatalf("UpdateExecutionStatus: %v", err)
	}
	n, err = store.CountQueuedTasks()
	if err != nil {
		t.Fatalf("CountQueuedTasks failed: %v", err)
	}
	if n != 0 {
		t.Errorf("Expected 0 after draining queue, got %d", n)
	}
}

// TestTaskLabelsRoundTrip verifies that Task.Labels survive the queue round-trip
// (SaveExecution → GetExecution and GetQueuedTasksForProject). Without this, labels
// like "no-decompose" are silently dropped and runner-side gates bypassed (GH-2326).
func TestTaskLabelsRoundTrip(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	cases := []struct {
		name   string
		labels []string
	}{
		{"nil labels", nil},
		{"empty slice", []string{}},
		{"single label", []string{"no-decompose"}},
		{"multiple labels", []string{"pilot", "no-decompose", "priority:high"}},
		{"special chars", []string{"kind/bug", "area/executor", "v1.0+"}},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			execID := fmt.Sprintf("exec-labels-%d", i)
			input := &Execution{
				ID:          execID,
				TaskID:      fmt.Sprintf("T-%d", i),
				ProjectPath: "/project/a",
				Status:      "queued",
				TaskTitle:   "test",
				TaskLabels:  tc.labels,
			}
			if err := store.SaveExecution(input); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			got, err := store.GetExecution(execID)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			// nil and empty slice both normalize to nil on read
			wantLen := len(tc.labels)
			if len(got.TaskLabels) != wantLen {
				t.Fatalf("labels length: got %d (%v), want %d (%v)", len(got.TaskLabels), got.TaskLabels, wantLen, tc.labels)
			}
			for j, l := range tc.labels {
				if got.TaskLabels[j] != l {
					t.Errorf("labels[%d]: got %q, want %q", j, got.TaskLabels[j], l)
				}
			}

			// Also verify the worker-facing read path returns labels.
			queued, err := store.GetQueuedTasksForProject("/project/a", 100)
			if err != nil {
				t.Fatalf("GetQueuedTasksForProject: %v", err)
			}
			var found *Execution
			for _, e := range queued {
				if e.ID == execID {
					found = e
					break
				}
			}
			if found == nil {
				t.Fatalf("execution %s not in queued list", execID)
			}
			if len(found.TaskLabels) != wantLen {
				t.Errorf("queued read labels length: got %d (%v), want %d", len(found.TaskLabels), found.TaskLabels, wantLen)
			}
		})
	}
}

func TestGetExecutionsInPeriod(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Add some executions
	for i := 0; i < 5; i++ {
		exec := &Execution{
			ID:          "exec-period-" + string(rune('a'+i)),
			TaskID:      "TASK-" + string(rune('1'+i)),
			ProjectPath: "/project/a",
			Status:      "completed",
		}
		_ = store.SaveExecution(exec)
	}

	// Add execution for different project
	_ = store.SaveExecution(&Execution{
		ID:          "exec-other",
		TaskID:      "TASK-99",
		ProjectPath: "/project/b",
		Status:      "completed",
	})

	// Verify the executions were created
	allExecs, _ := store.GetRecentExecutions(100, "")
	t.Logf("Total executions in DB: %d", len(allExecs))

	tests := []struct {
		name    string
		query   BriefQuery
		wantMin int
	}{
		{
			name: "all projects",
			query: BriefQuery{
				Start: time.Now().Add(-24 * time.Hour),
				End:   time.Now().Add(24 * time.Hour),
			},
			wantMin: 6,
		},
		{
			name: "specific project",
			query: BriefQuery{
				Start:    time.Now().Add(-24 * time.Hour),
				End:      time.Now().Add(24 * time.Hour),
				Projects: []string{"/project/a"},
			},
			wantMin: 5,
		},
		{
			name: "multiple projects",
			query: BriefQuery{
				Start:    time.Now().Add(-24 * time.Hour),
				End:      time.Now().Add(24 * time.Hour),
				Projects: []string{"/project/a", "/project/b"},
			},
			wantMin: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := store.GetExecutionsInPeriod(tt.query)
			if err != nil {
				t.Fatalf("GetExecutionsInPeriod failed: %v", err)
			}

			if len(results) < tt.wantMin {
				t.Errorf("got %d executions, want at least %d", len(results), tt.wantMin)
			}
		})
	}
}

func TestGetBriefMetrics(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Add executions with various statuses
	executions := []*Execution{
		{ID: "metrics-1", TaskID: "T1", ProjectPath: "/p", Status: "completed", DurationMs: 1000, PRUrl: "https://github.com/a/b/pull/1"},
		{ID: "metrics-2", TaskID: "T2", ProjectPath: "/p", Status: "completed", DurationMs: 2000, PRUrl: ""},
		{ID: "metrics-3", TaskID: "T3", ProjectPath: "/p", Status: "failed", DurationMs: 500},
		{ID: "metrics-4", TaskID: "T4", ProjectPath: "/p", Status: "completed", DurationMs: 3000, PRUrl: "https://github.com/a/b/pull/2"},
	}

	for _, e := range executions {
		_ = store.SaveExecution(e)
	}

	query := BriefQuery{
		Start: time.Now().Add(-24 * time.Hour),
		End:   time.Now().Add(24 * time.Hour),
	}

	metrics, err := store.GetBriefMetrics(query)
	if err != nil {
		t.Fatalf("GetBriefMetrics failed: %v", err)
	}

	if metrics.TotalTasks < 4 {
		t.Errorf("TotalTasks = %d, want at least 4", metrics.TotalTasks)
	}
	if metrics.CompletedCount < 3 {
		t.Errorf("CompletedCount = %d, want at least 3", metrics.CompletedCount)
	}
	if metrics.FailedCount < 1 {
		t.Errorf("FailedCount = %d, want at least 1", metrics.FailedCount)
	}
	if metrics.PRsCreated < 2 {
		t.Errorf("PRsCreated = %d, want at least 2", metrics.PRsCreated)
	}
}

func TestProjectSettings(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create project with complex settings
	settings := map[string]interface{}{
		"theme":        "dark",
		"autoCommit":   true,
		"maxTokens":    100000,
		"excludePaths": []interface{}{"/vendor", "/node_modules"},
	}

	project := &Project{
		Path:             "/path/to/project",
		Name:             "test-project",
		NavigatorEnabled: true,
		LastActive:       time.Now(),
		Settings:         settings,
	}

	if err := store.SaveProject(project); err != nil {
		t.Fatalf("SaveProject failed: %v", err)
	}

	retrieved, err := store.GetProject("/path/to/project")
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}

	if retrieved.Settings["theme"] != "dark" {
		t.Errorf("Settings[theme] = %v, want 'dark'", retrieved.Settings["theme"])
	}
	if retrieved.Settings["autoCommit"] != true {
		t.Errorf("Settings[autoCommit] = %v, want true", retrieved.Settings["autoCommit"])
	}
}

func TestGetProject_NotFound(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	_, err := store.GetProject("/nonexistent/path")
	if err == nil {
		t.Error("GetProject should return error for nonexistent project")
	}
}

func TestGetTopCrossPatterns(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create patterns with varying confidence
	patterns := []*CrossPattern{
		{ID: "high", Type: "code", Title: "High Confidence", Confidence: 0.95, Occurrences: 10, Scope: "org"},
		{ID: "medium", Type: "code", Title: "Medium Confidence", Confidence: 0.7, Occurrences: 5, Scope: "org"},
		{ID: "low", Type: "code", Title: "Low Confidence", Confidence: 0.4, Occurrences: 2, Scope: "org"},
	}

	for _, p := range patterns {
		_ = store.SaveCrossPattern(p)
	}

	tests := []struct {
		name          string
		limit         int
		minConfidence float64
		wantCount     int
	}{
		{name: "all patterns", limit: 10, minConfidence: 0, wantCount: 3},
		{name: "high confidence only", limit: 10, minConfidence: 0.9, wantCount: 1},
		{name: "medium and above", limit: 10, minConfidence: 0.6, wantCount: 2},
		{name: "limited results", limit: 2, minConfidence: 0, wantCount: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := store.GetTopCrossPatterns(tt.limit, tt.minConfidence)
			if err != nil {
				t.Fatalf("GetTopCrossPatterns failed: %v", err)
			}

			if len(results) != tt.wantCount {
				t.Errorf("got %d patterns, want %d", len(results), tt.wantCount)
			}
		})
	}
}

func TestGetCrossPatternsForProject(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create patterns with different scopes
	_ = store.SaveCrossPattern(&CrossPattern{ID: "org-1", Type: "code", Title: "Org Pattern", Scope: "org"})
	_ = store.SaveCrossPattern(&CrossPattern{ID: "global-1", Type: "code", Title: "Global Pattern", Scope: "global"})
	_ = store.SaveCrossPattern(&CrossPattern{ID: "project-1", Type: "code", Title: "Project Pattern", Scope: "project"})

	// Link project pattern
	_ = store.LinkPatternToProject("project-1", "/project/a")

	tests := []struct {
		name          string
		projectPath   string
		includeGlobal bool
		wantMin       int
	}{
		{name: "with global", projectPath: "/project/a", includeGlobal: true, wantMin: 2},
		{name: "without global", projectPath: "/project/a", includeGlobal: false, wantMin: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := store.GetCrossPatternsForProject(tt.projectPath, tt.includeGlobal)
			if err != nil {
				t.Fatalf("GetCrossPatternsForProject failed: %v", err)
			}

			if len(results) < tt.wantMin {
				t.Errorf("got %d patterns, want at least %d", len(results), tt.wantMin)
			}
		})
	}
}

func TestGetLifetimeTokens(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Empty table should return zeros
	lt, err := store.GetLifetimeTokens("")
	if err != nil {
		t.Fatalf("GetLifetimeTokens (empty): %v", err)
	}
	if lt.TotalTokens != 0 || lt.TotalCostUSD != 0 {
		t.Errorf("empty: want zeros, got tokens=%d cost=%.4f", lt.TotalTokens, lt.TotalCostUSD)
	}

	// Insert executions with token data
	execs := []struct {
		id     string
		input  int64
		output int64
		cost   float64
	}{
		{"exec-lt-1", 1000, 500, 0.05},
		{"exec-lt-2", 2000, 1000, 0.10},
		{"exec-lt-3", 3000, 1500, 0.15},
	}
	for _, e := range execs {
		if err := store.SaveExecution(&Execution{
			ID:          e.id,
			TaskID:      "TASK-" + e.id,
			ProjectPath: "/test",
			Status:      "completed",
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.id, err)
		}
		if err := store.SaveExecutionMetrics(&ExecutionMetrics{
			ExecutionID:      e.id,
			TokensInput:      e.input,
			TokensOutput:     e.output,
			TokensTotal:      e.input + e.output,
			EstimatedCostUSD: e.cost,
		}); err != nil {
			t.Fatalf("SaveExecutionMetrics %s: %v", e.id, err)
		}
	}

	lt, err = store.GetLifetimeTokens("")
	if err != nil {
		t.Fatalf("GetLifetimeTokens: %v", err)
	}

	wantInput := int64(6000)
	wantOutput := int64(3000)
	wantTotal := int64(9000)
	wantCost := 0.30

	if lt.InputTokens != wantInput {
		t.Errorf("InputTokens = %d, want %d", lt.InputTokens, wantInput)
	}
	if lt.OutputTokens != wantOutput {
		t.Errorf("OutputTokens = %d, want %d", lt.OutputTokens, wantOutput)
	}
	if lt.TotalTokens != wantTotal {
		t.Errorf("TotalTokens = %d, want %d", lt.TotalTokens, wantTotal)
	}
	if lt.TotalCostUSD != wantCost {
		t.Errorf("TotalCostUSD = %.4f, want %.4f", lt.TotalCostUSD, wantCost)
	}
}

func TestGetLifetimeTokens_CacheFields(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Insert two executions with cache token data via SaveExecution.
	execs := []struct {
		id         string
		input      int64
		output     int64
		cacheRead  int64
		cacheWrite int64
	}{
		{"lt-cache-1", 1000, 500, 80000, 3000},
		{"lt-cache-2", 2000, 1000, 40000, 2000},
	}
	for _, e := range execs {
		total := e.input + e.output
		if err := store.SaveExecution(&Execution{
			ID:               e.id,
			TaskID:           "TASK-" + e.id,
			ProjectPath:      "/p",
			Status:           "completed",
			TokensInput:      e.input,
			TokensOutput:     e.output,
			TokensTotal:      total,
			TokensCacheRead:  e.cacheRead,
			TokensCacheWrite: e.cacheWrite,
			EstimatedCostUSD: 0.01,
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.id, err)
		}
	}

	lt, err := store.GetLifetimeTokens("")
	if err != nil {
		t.Fatalf("GetLifetimeTokens: %v", err)
	}

	if lt.CacheReadTokens != 120000 {
		t.Errorf("CacheReadTokens = %d, want 120000", lt.CacheReadTokens)
	}
	if lt.CacheWriteTokens != 5000 {
		t.Errorf("CacheWriteTokens = %d, want 5000", lt.CacheWriteTokens)
	}
	// Regular token fields unaffected
	if lt.TotalTokens != 4500 {
		t.Errorf("TotalTokens = %d, want 4500", lt.TotalTokens)
	}
}

func TestGetLifetimeTaskCounts(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Empty table should return zeros
	tc, err := store.GetLifetimeTaskCounts("")
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts (empty): %v", err)
	}
	if tc.Total != 0 || tc.Succeeded != 0 || tc.Failed != 0 {
		t.Errorf("empty: want zeros, got total=%d succeeded=%d failed=%d", tc.Total, tc.Succeeded, tc.Failed)
	}

	// Insert mix of completed and failed executions
	statuses := []struct {
		id     string
		status string
	}{
		{"exec-tc-1", "completed"},
		{"exec-tc-2", "completed"},
		{"exec-tc-3", "failed"},
		{"exec-tc-4", "completed"},
		{"exec-tc-5", "failed"},
	}
	for _, s := range statuses {
		if err := store.SaveExecution(&Execution{
			ID:          s.id,
			TaskID:      "TASK-" + s.id,
			ProjectPath: "/test",
			Status:      s.status,
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", s.id, err)
		}
	}

	// GH-4240: a canary sandbox execution must not move any of these
	// lifetime counters, project-scoped or not.
	if err := store.SaveExecution(&Execution{
		ID:          "exec-tc-canary",
		TaskID:      "TASK-CANARY",
		ProjectPath: "/canary-sandbox",
		Status:      "completed",
		IsCanary:    true,
	}); err != nil {
		t.Fatalf("SaveExecution canary: %v", err)
	}

	tc, err = store.GetLifetimeTaskCounts("")
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts: %v", err)
	}

	if tc.Total != 5 {
		t.Errorf("Total = %d, want 5", tc.Total)
	}
	if tc.Succeeded != 3 {
		t.Errorf("Succeeded = %d, want 3", tc.Succeeded)
	}
	if tc.Failed != 2 {
		t.Errorf("Failed = %d, want 2", tc.Failed)
	}
}

// TestGetLifetimeTaskCounts_ExcludesCanarySameProject is GH-4650: the
// `COALESCE(is_canary, 0) = 0` filter in GetLifetimeTaskCounts is exercised
// in TestGetLifetimeTaskCounts only via a canary row on a *different*
// project path, which the ProjectPath filter alone would already exclude.
// This test seeds one is_canary=0 row and one is_canary=1 row on the SAME
// project and asserts the canary row is dropped by the is_canary predicate
// itself, not incidentally by project scoping — converting the filter from
// dormant/dead code into asserted behavior (GH-4648 AC #3).
func TestGetLifetimeTaskCounts_ExcludesCanarySameProject(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const project = "/project/same-canary"

	if err := store.SaveExecution(&Execution{
		ID:          "exec-sc-real",
		TaskID:      "TASK-SC-REAL",
		ProjectPath: project,
		Status:      "completed",
		IsCanary:    false,
	}); err != nil {
		t.Fatalf("SaveExecution real: %v", err)
	}
	if err := store.SaveExecution(&Execution{
		ID:          "exec-sc-canary",
		TaskID:      "TASK-SC-CANARY",
		ProjectPath: project,
		Status:      "completed",
		IsCanary:    true,
	}); err != nil {
		t.Fatalf("SaveExecution canary: %v", err)
	}

	tc, err := store.GetLifetimeTaskCounts(project)
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts: %v", err)
	}
	if tc.Total != 1 {
		t.Errorf("Total = %d, want 1 (is_canary=1 row must be excluded)", tc.Total)
	}
	if tc.Succeeded != 1 {
		t.Errorf("Succeeded = %d, want 1", tc.Succeeded)
	}

	// Unfiltered (no project scoping) must also exclude the canary row.
	tcAll, err := store.GetLifetimeTaskCounts("")
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts(all): %v", err)
	}
	if tcAll.Total != 1 {
		t.Errorf("unfiltered Total = %d, want 1 (is_canary=1 row must be excluded)", tcAll.Total)
	}
}

// TestGetIssueLevelCounts table-drives the TASK-392 dedupe-by-task_id query:
// unique-issue attempt/ship counts must collapse multiple retry rows for the
// same task_id into a single attempt, and only count it shipped if at least
// one of those rows reached "completed".
func TestGetIssueLevelCounts(t *testing.T) {
	tests := []struct {
		name          string
		execs         []*Execution
		projectPath   string
		wantAttempted int
		wantShipped   int
	}{
		{
			name:          "empty table",
			execs:         nil,
			wantAttempted: 0,
			wantShipped:   0,
		},
		{
			name: "retried then shipped task dedupes to one attempt one ship",
			execs: []*Execution{
				{ID: "ilc-1", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "failed"},
				{ID: "ilc-2", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "failed"},
				{ID: "ilc-3", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "completed"},
			},
			wantAttempted: 1,
			wantShipped:   1,
		},
		{
			name: "mixed statuses across distinct issues",
			execs: []*Execution{
				{ID: "ilc-4", TaskID: "TASK-A", ProjectPath: "/p", Status: "completed"},
				{ID: "ilc-5", TaskID: "TASK-B", ProjectPath: "/p", Status: "failed"},
				{ID: "ilc-6", TaskID: "TASK-C", ProjectPath: "/p", Status: "declined"},
				{ID: "ilc-7", TaskID: "TASK-D", ProjectPath: "/p", Status: "rate_limited"},
			},
			wantAttempted: 4,
			wantShipped:   1,
		},
		{
			name: "never-shipped retried task counts as attempted, not shipped",
			execs: []*Execution{
				{ID: "ilc-8", TaskID: "TASK-STUCK", ProjectPath: "/p", Status: "failed"},
				{ID: "ilc-9", TaskID: "TASK-STUCK", ProjectPath: "/p", Status: "rate_limited"},
			},
			wantAttempted: 1,
			wantShipped:   0,
		},
		{
			name: "project filter scopes dedupe to matching path",
			execs: []*Execution{
				{ID: "ilc-10", TaskID: "TASK-X", ProjectPath: "/alpha", Status: "completed"},
				{ID: "ilc-11", TaskID: "TASK-Y", ProjectPath: "/beta", Status: "completed"},
				{ID: "ilc-12", TaskID: "TASK-Y", ProjectPath: "/beta", Status: "failed"},
			},
			projectPath:   "/beta",
			wantAttempted: 1,
			wantShipped:   1,
		},
		{
			// GH-4240: a canary sandbox execution must not count toward
			// issue-level attempted/shipped, even though it's a normal
			// 'completed' row with no project filter applied.
			name: "canary execution excluded regardless of status",
			execs: []*Execution{
				{ID: "ilc-13", TaskID: "TASK-REAL", ProjectPath: "/p", Status: "completed"},
				{ID: "ilc-14", TaskID: "TASK-CANARY", ProjectPath: "/canary-sandbox", Status: "completed", IsCanary: true},
			},
			wantAttempted: 1,
			wantShipped:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			store, err := NewStore(tmpDir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			for _, e := range tt.execs {
				if err := store.SaveExecution(e); err != nil {
					t.Fatalf("SaveExecution %s: %v", e.ID, err)
				}
			}

			counts, err := store.GetIssueLevelCounts(tt.projectPath)
			if err != nil {
				t.Fatalf("GetIssueLevelCounts: %v", err)
			}
			if counts.Attempted != tt.wantAttempted {
				t.Errorf("Attempted = %d, want %d", counts.Attempted, tt.wantAttempted)
			}
			if counts.Shipped != tt.wantShipped {
				t.Errorf("Shipped = %d, want %d", counts.Shipped, tt.wantShipped)
			}
		})
	}
}

// TestGetIssueLevelCountsByModel table-drives the GH-4483 per-model
// counterpart to GetIssueLevelCounts: pins the acceptance scenario of a task
// retried twice on the same model before shipping — issue-level per-model
// success must read 100% even though the attempt-level signal for that model
// would read 33%. Also pins that rows with an empty model_name are excluded
// entirely rather than bucketed under "unknown".
func TestGetIssueLevelCountsByModel(t *testing.T) {
	tests := []struct {
		name        string
		execs       []*Execution
		projectPath string
		want        map[string]IssueLevelModelCounts
	}{
		{
			name:  "empty table",
			execs: nil,
			want:  map[string]IssueLevelModelCounts{},
		},
		{
			name: "2 failed attempts + 1 completed, same task_id same model: 100% issue-level success",
			execs: []*Execution{
				{ID: "ilcm-1", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "failed", ModelName: "claude-sonnet-5"},
				{ID: "ilcm-2", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "failed", ModelName: "claude-sonnet-5"},
				{ID: "ilcm-3", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "completed", ModelName: "claude-sonnet-5"},
			},
			want: map[string]IssueLevelModelCounts{
				"claude-sonnet-5": {Model: "claude-sonnet-5", Attempted: 1, Shipped: 1},
			},
		},
		{
			name: "distinct models tracked separately",
			execs: []*Execution{
				{ID: "ilcm-4", TaskID: "TASK-A", ProjectPath: "/p", Status: "completed", ModelName: "claude-sonnet-5"},
				{ID: "ilcm-5", TaskID: "TASK-B", ProjectPath: "/p", Status: "failed", ModelName: "claude-opus-4-6"},
				{ID: "ilcm-6", TaskID: "TASK-C", ProjectPath: "/p", Status: "declined", ModelName: "claude-opus-4-6"},
			},
			want: map[string]IssueLevelModelCounts{
				"claude-sonnet-5": {Model: "claude-sonnet-5", Attempted: 1, Shipped: 1},
				"claude-opus-4-6": {Model: "claude-opus-4-6", Attempted: 2, Shipped: 0},
			},
		},
		{
			name: "empty model_name rows excluded, not bucketed under unknown",
			execs: []*Execution{
				{ID: "ilcm-7", TaskID: "TASK-D", ProjectPath: "/p", Status: "completed", ModelName: "claude-sonnet-5"},
				{ID: "ilcm-8", TaskID: "TASK-E", ProjectPath: "/p", Status: "failed", ModelName: ""},
			},
			want: map[string]IssueLevelModelCounts{
				"claude-sonnet-5": {Model: "claude-sonnet-5", Attempted: 1, Shipped: 1},
			},
		},
		{
			name: "project filter scopes dedupe to matching path",
			execs: []*Execution{
				{ID: "ilcm-9", TaskID: "TASK-X", ProjectPath: "/alpha", Status: "completed", ModelName: "claude-sonnet-5"},
				{ID: "ilcm-10", TaskID: "TASK-Y", ProjectPath: "/beta", Status: "completed", ModelName: "claude-sonnet-5"},
			},
			projectPath: "/beta",
			want: map[string]IssueLevelModelCounts{
				"claude-sonnet-5": {Model: "claude-sonnet-5", Attempted: 1, Shipped: 1},
			},
		},
		{
			// GH-4240: a canary sandbox execution must not count, even though
			// it's a normal 'completed' row with a real model name.
			name: "canary execution excluded regardless of status",
			execs: []*Execution{
				{ID: "ilcm-11", TaskID: "TASK-REAL", ProjectPath: "/p", Status: "completed", ModelName: "claude-sonnet-5"},
				{ID: "ilcm-12", TaskID: "TASK-CANARY", ProjectPath: "/canary-sandbox", Status: "completed", ModelName: "claude-sonnet-5", IsCanary: true},
			},
			want: map[string]IssueLevelModelCounts{
				"claude-sonnet-5": {Model: "claude-sonnet-5", Attempted: 1, Shipped: 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			store, err := NewStore(tmpDir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			for _, e := range tt.execs {
				if err := store.SaveExecution(e); err != nil {
					t.Fatalf("SaveExecution %s: %v", e.ID, err)
				}
			}

			counts, err := store.GetIssueLevelCountsByModel(tt.projectPath)
			if err != nil {
				t.Fatalf("GetIssueLevelCountsByModel: %v", err)
			}

			got := make(map[string]IssueLevelModelCounts, len(counts))
			for _, c := range counts {
				got[c.Model] = c
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d model buckets, want %d: got=%+v want=%+v", len(got), len(tt.want), got, tt.want)
			}
			for model, want := range tt.want {
				if got[model] != want {
					t.Errorf("model %q counts = %+v, want %+v", model, got[model], want)
				}
			}
		})
	}
}

// TestGetLifetimePRCountersFromExecutions covers GH-4121: PR-outcome counters
// hydrated all-time from the executions table (not the execution_events
// ledger, which only goes back to its TASK-379/GH-3844 introduction). Pins:
// pre-ledger executions (rows in executions, no execution_events rows at all)
// still contribute; a retried task counts once regardless of how many
// attempts it took; coding-stage failures with no PR are excluded from the
// failed bucket.
func TestGetLifetimePRCountersFromExecutions(t *testing.T) {
	tests := []struct {
		name        string
		execs       []*Execution
		projectPath string
		wantMerged  int64
		wantFailed  int64
	}{
		{
			name:       "empty table",
			execs:      nil,
			wantMerged: 0,
			wantFailed: 0,
		},
		{
			name: "pre-ledger merged execution with no execution_events rows still counts",
			execs: []*Execution{
				{ID: "plc-1", TaskID: "TASK-A", ProjectPath: "/p", Status: "completed", PRUrl: "https://github.com/o/r/pull/1"},
			},
			wantMerged: 1,
			wantFailed: 0,
		},
		{
			name: "genuine PR-family failure counts once",
			execs: []*Execution{
				{ID: "plc-2", TaskID: "TASK-B", ProjectPath: "/p", Status: "failed", PRUrl: "https://github.com/o/r/pull/2"},
			},
			wantMerged: 0,
			wantFailed: 1,
		},
		{
			name: "coding-stage failure with no PR is excluded from failed",
			execs: []*Execution{
				{ID: "plc-3", TaskID: "TASK-C", ProjectPath: "/p", Status: "failed"},
			},
			wantMerged: 0,
			wantFailed: 0,
		},
		{
			name: "completed with no PR url is excluded from merged",
			execs: []*Execution{
				{ID: "plc-4", TaskID: "TASK-D", ProjectPath: "/p", Status: "completed"},
			},
			wantMerged: 0,
			wantFailed: 0,
		},
		{
			name: "retried task failed once with a PR then shipped: counts once as merged, not also failed",
			execs: []*Execution{
				{ID: "plc-5a", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "failed", PRUrl: "https://github.com/o/r/pull/5a"},
				{ID: "plc-5b", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "completed", PRUrl: "https://github.com/o/r/pull/5b"},
			},
			wantMerged: 1,
			wantFailed: 0,
		},
		{
			name: "project filter scopes counts to matching path",
			execs: []*Execution{
				{ID: "plc-6", TaskID: "TASK-X", ProjectPath: "/alpha", Status: "completed", PRUrl: "https://github.com/o/r/pull/6"},
				{ID: "plc-7", TaskID: "TASK-Y", ProjectPath: "/beta", Status: "completed", PRUrl: "https://github.com/o/r/pull/7"},
				{ID: "plc-8", TaskID: "TASK-Z", ProjectPath: "/beta", Status: "failed", PRUrl: "https://github.com/o/r/pull/8"},
			},
			projectPath: "/beta",
			wantMerged:  1,
			wantFailed:  1,
		},
		{
			// GH-4240: canary merged/failed PR rows must not leak into either
			// bucket, including via the failed-bucket's "already merged
			// elsewhere" exclusion subquery.
			name: "canary merged and failed rows both excluded",
			execs: []*Execution{
				{ID: "plc-9", TaskID: "TASK-REAL", ProjectPath: "/p", Status: "completed", PRUrl: "https://github.com/o/r/pull/9"},
				{ID: "plc-10", TaskID: "TASK-CANARY-M", ProjectPath: "/canary-sandbox", Status: "completed", PRUrl: "https://github.com/o/r/pull/10", IsCanary: true},
				{ID: "plc-11", TaskID: "TASK-CANARY-F", ProjectPath: "/canary-sandbox", Status: "failed", PRUrl: "https://github.com/o/r/pull/11", IsCanary: true},
			},
			wantMerged: 1,
			wantFailed: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			store, err := NewStore(tmpDir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			for _, e := range tt.execs {
				if err := store.SaveExecution(e); err != nil {
					t.Fatalf("SaveExecution %s: %v", e.ID, err)
				}
			}

			counters, err := store.GetLifetimePRCountersFromExecutions(tt.projectPath)
			if err != nil {
				t.Fatalf("GetLifetimePRCountersFromExecutions: %v", err)
			}
			if counters.Merged != tt.wantMerged {
				t.Errorf("Merged = %d, want %d", counters.Merged, tt.wantMerged)
			}
			if counters.Failed != tt.wantFailed {
				t.Errorf("Failed = %d, want %d", counters.Failed, tt.wantFailed)
			}
		})
	}
}

func TestBriefHistory(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(*Store)
		channel       string
		wantNil       bool
		wantBriefType string
		wantRecipient string
	}{
		{
			name:    "empty table returns nil",
			setup:   func(s *Store) {},
			channel: "telegram",
			wantNil: true,
		},
		{
			name: "single insert returns that record",
			setup: func(s *Store) {
				_ = s.RecordBriefSent(&BriefRecord{
					SentAt:    time.Now(),
					Channel:   "telegram",
					BriefType: "daily",
					Recipient: "user123",
				})
			},
			channel:       "telegram",
			wantNil:       false,
			wantBriefType: "daily",
			wantRecipient: "user123",
		},
		{
			name: "multiple inserts returns most recent",
			setup: func(s *Store) {
				// Insert older record first
				_ = s.RecordBriefSent(&BriefRecord{
					SentAt:    time.Now().Add(-2 * time.Hour),
					Channel:   "slack",
					BriefType: "daily",
					Recipient: "old-user",
				})
				// Insert newer record
				_ = s.RecordBriefSent(&BriefRecord{
					SentAt:    time.Now().Add(-1 * time.Hour),
					Channel:   "slack",
					BriefType: "weekly",
					Recipient: "new-user",
				})
				// Insert most recent
				_ = s.RecordBriefSent(&BriefRecord{
					SentAt:    time.Now(),
					Channel:   "slack",
					BriefType: "daily",
					Recipient: "latest-user",
				})
			},
			channel:       "slack",
			wantNil:       false,
			wantBriefType: "daily",
			wantRecipient: "latest-user",
		},
		{
			name: "filters by channel",
			setup: func(s *Store) {
				_ = s.RecordBriefSent(&BriefRecord{
					SentAt:    time.Now(),
					Channel:   "telegram",
					BriefType: "daily",
					Recipient: "tg-user",
				})
				_ = s.RecordBriefSent(&BriefRecord{
					SentAt:    time.Now(),
					Channel:   "slack",
					BriefType: "weekly",
					Recipient: "slack-user",
				})
			},
			channel:       "telegram",
			wantNil:       false,
			wantBriefType: "daily",
			wantRecipient: "tg-user",
		},
		{
			name: "non-existent channel returns nil",
			setup: func(s *Store) {
				_ = s.RecordBriefSent(&BriefRecord{
					SentAt:    time.Now(),
					Channel:   "telegram",
					BriefType: "daily",
				})
			},
			channel: "email",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			store, err := NewStore(tmpDir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			tt.setup(store)

			record, err := store.GetLastBriefSent(tt.channel)
			if err != nil {
				t.Fatalf("GetLastBriefSent: %v", err)
			}

			if tt.wantNil {
				if record != nil {
					t.Errorf("expected nil, got %+v", record)
				}
				return
			}

			if record == nil {
				t.Fatal("expected non-nil record, got nil")
			}

			if record.Channel != tt.channel {
				t.Errorf("Channel = %q, want %q", record.Channel, tt.channel)
			}
			if record.BriefType != tt.wantBriefType {
				t.Errorf("BriefType = %q, want %q", record.BriefType, tt.wantBriefType)
			}
			if record.Recipient != tt.wantRecipient {
				t.Errorf("Recipient = %q, want %q", record.Recipient, tt.wantRecipient)
			}
			if record.ID == 0 {
				t.Error("ID should be set after insert")
			}
		})
	}
}

func TestRecordBriefSent_SetsID(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	record := &BriefRecord{
		SentAt:    time.Now(),
		Channel:   "telegram",
		BriefType: "daily",
	}

	if record.ID != 0 {
		t.Error("ID should be 0 before insert")
	}

	if err := store.RecordBriefSent(record); err != nil {
		t.Fatalf("RecordBriefSent: %v", err)
	}

	if record.ID == 0 {
		t.Error("ID should be set after insert")
	}
}

func TestStore_WithRetry(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	t.Run("succeeds on first attempt", func(t *testing.T) {
		attempts := 0
		err := store.withRetry("test", func() error {
			attempts++
			return nil
		})
		if err != nil {
			t.Errorf("withRetry should succeed: %v", err)
		}
		if attempts != 1 {
			t.Errorf("should only attempt once, got %d", attempts)
		}
	})

	t.Run("retries on database locked error", func(t *testing.T) {
		var buf bytes.Buffer
		oldLogger := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		defer slog.SetDefault(oldLogger)

		attempts := 0
		err := store.withRetry("test", func() error {
			attempts++
			if attempts < 3 {
				return fmt.Errorf("database is locked (SQLITE_BUSY)")
			}
			return nil
		})
		if err != nil {
			t.Errorf("withRetry should succeed after retries: %v", err)
		}
		if attempts != 3 {
			t.Errorf("should retry until success, got %d attempts", attempts)
		}

		logOutput := buf.String()
		if !strings.Contains(logOutput, "Database locked, retrying") {
			t.Errorf("expected retry warning in logs, got: %s", logOutput)
		}
	})

	t.Run("does not retry non-retryable errors", func(t *testing.T) {
		attempts := 0
		err := store.withRetry("test", func() error {
			attempts++
			return fmt.Errorf("syntax error: invalid SQL")
		})
		if err == nil {
			t.Error("withRetry should return error")
		}
		if attempts != 1 {
			t.Errorf("should not retry non-retryable error, got %d attempts", attempts)
		}
		if !strings.Contains(err.Error(), "syntax error") {
			t.Errorf("should return original error, got: %v", err)
		}
	})

	t.Run("fails after max retries", func(t *testing.T) {
		attempts := 0
		err := store.withRetry("TestOp", func() error {
			attempts++
			return fmt.Errorf("database is locked (SQLITE_BUSY)")
		})
		if err == nil {
			t.Error("withRetry should return error after max retries")
		}
		if attempts != 5 {
			t.Errorf("should attempt 5 times, got %d", attempts)
		}
		if !strings.Contains(err.Error(), "TestOp failed after 5 retries") {
			t.Errorf("error should mention operation and retry count, got: %v", err)
		}
	})

	t.Run("retries on sqlite_locked", func(t *testing.T) {
		attempts := 0
		err := store.withRetry("test", func() error {
			attempts++
			if attempts < 2 {
				return fmt.Errorf("table is locked (SQLITE_LOCKED)")
			}
			return nil
		})
		if err != nil {
			t.Errorf("withRetry should succeed: %v", err)
		}
		if attempts != 2 {
			t.Errorf("should retry, got %d attempts", attempts)
		}
	})
}

func TestStore_ConnectionPoolSettings(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Verify connection pool settings by checking stats
	stats := store.db.Stats()

	// MaxOpenConns should be 1
	if stats.MaxOpenConnections != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
	}
}

func TestStore_SetApprovalRequestID_HappyPath(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	exec := &Execution{
		ID:          "exec-approval-1",
		TaskID:      "GH-999",
		ProjectPath: "/proj",
		Status:      "completed",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	if err := store.SetApprovalRequestID(ctx, "GH-999", "req-abc"); err != nil {
		t.Fatalf("SetApprovalRequestID: %v", err)
	}

	// Verify via SetApprovalDecision — it matches on approval_request_id.
	if err := store.SetApprovalDecision(ctx, "req-abc", "approved", "tester"); err != nil {
		t.Fatalf("SetApprovalDecision after SetApprovalRequestID: %v", err)
	}

	got, err := store.GetExecution("exec-approval-1")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.ApprovalRequestID != "req-abc" {
		t.Errorf("ApprovalRequestID = %q, want %q", got.ApprovalRequestID, "req-abc")
	}
	if got.ApprovalDecision != "approved" {
		t.Errorf("ApprovalDecision = %q, want %q", got.ApprovalDecision, "approved")
	}
	if got.ApprovalDecisionBy != "tester" {
		t.Errorf("ApprovalDecisionBy = %q, want %q", got.ApprovalDecisionBy, "tester")
	}
}

func TestStore_SetApprovalRequestID_ZeroRowCase(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	// No execution row exists for this task.
	err = store.SetApprovalRequestID(ctx, "GH-000", "req-xyz")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows for missing task, got %v", err)
	}
}

func TestLogEntryCRUD(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Save entries
	entries := []*LogEntry{
		{ExecutionID: "exec-1", Timestamp: time.Now().Add(-2 * time.Second), Level: "info", Message: "Task started", Component: "executor"},
		{ExecutionID: "exec-1", Timestamp: time.Now().Add(-1 * time.Second), Level: "warn", Message: "Slow build", Component: "executor"},
		{ExecutionID: "exec-1", Timestamp: time.Now(), Level: "error", Message: "Build failed", Component: "executor"},
	}

	for _, e := range entries {
		if err := store.SaveLogEntry(e); err != nil {
			t.Fatalf("SaveLogEntry failed: %v", err)
		}
		if e.ID == 0 {
			t.Error("Expected non-zero ID after save")
		}
	}

	// Get recent logs
	recent, err := store.GetRecentLogs(10)
	if err != nil {
		t.Fatalf("GetRecentLogs failed: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("Expected 3 entries, got %d", len(recent))
	}

	// Should be ordered DESC by timestamp — most recent first
	if recent[0].Message != "Build failed" {
		t.Errorf("Expected most recent entry first, got %q", recent[0].Message)
	}
	if recent[0].Level != "error" {
		t.Errorf("Expected level 'error', got %q", recent[0].Level)
	}

	// Test limit
	limited, err := store.GetRecentLogs(2)
	if err != nil {
		t.Fatalf("GetRecentLogs with limit failed: %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("Expected 2 entries with limit, got %d", len(limited))
	}

	// Empty result
	tmpDir2, _ := os.MkdirTemp("", "pilot-test-empty-*")
	defer func() { _ = os.RemoveAll(tmpDir2) }()
	store2, _ := NewStore(tmpDir2)
	defer func() { _ = store2.Close() }()

	empty, err := store2.GetRecentLogs(10)
	if err != nil {
		t.Fatalf("GetRecentLogs on empty store failed: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("Expected 0 entries on empty store, got %d", len(empty))
	}
}

func TestLogSubscribeLogs(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Subscribe before saving
	ch := store.SubscribeLogs()

	entry := &LogEntry{
		ExecutionID: "exec-sub",
		Timestamp:   time.Now(),
		Level:       "info",
		Message:     "hello subscriber",
		Component:   "test",
	}

	if err := store.SaveLogEntry(entry); err != nil {
		t.Fatalf("SaveLogEntry failed: %v", err)
	}

	select {
	case got := <-ch:
		if got.Message != "hello subscriber" {
			t.Errorf("Expected 'hello subscriber', got %q", got.Message)
		}
		if got.ID == 0 {
			t.Error("Expected non-zero ID on received entry")
		}
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for subscriber notification")
	}

	// Unsubscribe and verify channel is closed
	store.UnsubscribeLogs(ch)

	// Save another entry — should not panic or block
	entry2 := &LogEntry{
		ExecutionID: "exec-sub",
		Timestamp:   time.Now(),
		Level:       "info",
		Message:     "after unsubscribe",
		Component:   "test",
	}
	if err := store.SaveLogEntry(entry2); err != nil {
		t.Fatalf("SaveLogEntry after unsubscribe failed: %v", err)
	}
}

func TestLogSubscribeMultipleSubscribers(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	ch1 := store.SubscribeLogs()
	ch2 := store.SubscribeLogs()

	entry := &LogEntry{
		ExecutionID: "exec-multi",
		Timestamp:   time.Now(),
		Level:       "warn",
		Message:     "broadcast test",
		Component:   "test",
	}

	if err := store.SaveLogEntry(entry); err != nil {
		t.Fatalf("SaveLogEntry failed: %v", err)
	}

	// Both subscribers should receive the entry
	for i, ch := range []<-chan *LogEntry{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Message != "broadcast test" {
				t.Errorf("subscriber %d: expected 'broadcast test', got %q", i, got.Message)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out", i)
		}
	}

	store.UnsubscribeLogs(ch1)
	store.UnsubscribeLogs(ch2)
}

func TestCrossPatternsIndexes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Insert test data so the planner has something to work with
	pattern := &CrossPattern{
		ID:    "test-idx-1",
		Type:  "naming",
		Title: "Use camelCase",
		Scope: "org",
	}
	if err := store.SaveCrossPattern(pattern); err != nil {
		t.Fatalf("SaveCrossPattern failed: %v", err)
	}

	tests := []struct {
		name  string
		query string
		index string
	}{
		{
			name:  "scope filter uses index",
			query: `EXPLAIN QUERY PLAN SELECT * FROM cross_patterns WHERE scope = 'org'`,
			index: "idx_cross_patterns_scope",
		},
		{
			name:  "updated_at filter uses index",
			query: `EXPLAIN QUERY PLAN SELECT * FROM cross_patterns WHERE updated_at > '2025-01-01'`,
			index: "idx_cross_patterns_updated",
		},
		{
			name:  "title filter uses index",
			query: `EXPLAIN QUERY PLAN SELECT * FROM cross_patterns WHERE title = 'Use camelCase'`,
			index: "idx_cross_patterns_title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := store.db.Query(tt.query)
			if err != nil {
				t.Fatalf("EXPLAIN QUERY PLAN failed: %v", err)
			}
			defer func() { _ = rows.Close() }()

			var plan strings.Builder
			for rows.Next() {
				var id, parent, notused int
				var detail string
				if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
					t.Fatalf("scan failed: %v", err)
				}
				_, _ = fmt.Fprintf(&plan, "%s\n", detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("rows iteration failed: %v", err)
			}

			if !strings.Contains(plan.String(), tt.index) {
				t.Errorf("expected query plan to use %s, got:\n%s", tt.index, plan.String())
			}
		})
	}
}

func TestGetStaleQueuedExecutions(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	staleDuration := time.Hour

	now := time.Now()

	insertAt := func(exec *Execution, createdAt time.Time) {
		t.Helper()
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("SaveExecution %s: %v", exec.ID, err)
		}
		if _, err := store.db.Exec(
			`UPDATE executions SET created_at = ? WHERE id = ?`, createdAt, exec.ID,
		); err != nil {
			t.Fatalf("set created_at for %s: %v", exec.ID, err)
		}
	}

	// Fresh queued execution (created now — should NOT be returned).
	insertAt(&Execution{ID: "queued-fresh", TaskID: "TASK-fresh", ProjectPath: "/proj", Status: "queued"}, now)

	// Stale queued execution (created 2 hours ago — should be returned).
	insertAt(&Execution{ID: "queued-stale", TaskID: "TASK-stale", ProjectPath: "/proj", Status: "queued"}, now.Add(-2*time.Hour))

	// Stale running execution — must NOT appear in queued results.
	insertAt(&Execution{ID: "running-stale", TaskID: "TASK-running", ProjectPath: "/proj", Status: "running"}, now.Add(-2*time.Hour))

	results, err := store.GetStaleQueuedExecutions(staleDuration)
	if err != nil {
		t.Fatalf("GetStaleQueuedExecutions: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 stale queued execution, got %d", len(results))
	}
	if results[0].ID != "queued-stale" {
		t.Errorf("expected ID %q, got %q", "queued-stale", results[0].ID)
	}
	if results[0].Status != "queued" {
		t.Errorf("expected status 'queued', got %q", results[0].Status)
	}
}

// TestGetStaleQueuedExecutions_LegacyTimestampFormat is the GH-4392
// timestamp-format-hardening regression test. Rows written before the DSN's
// `_time_format=sqlite` fix (GH-4332/#4345) carry created_at in Go's raw
// time.Time.String() layout ("2006-01-02 15:04:05.999999999 -0700 MST"),
// distinct from the layout `_time_format=sqlite` now writes
// ("2006-01-02 15:04:05.999999999-07:00"). A `WHERE created_at < ?` SQL
// predicate compares these lexicographically and is not guaranteed to match
// chronological order across the two layouts — this is how the stale
// recovery sweep silently logged "reset 0 tasks" straight through the
// GH-4392 incident. GetStaleQueuedExecutions must correctly classify
// legacy-format rows by parsing created_at into a time.Time (which the
// modernc.org/sqlite driver's read path already supports for both layouts)
// and comparing in Go, never by raw SQL string range.
func TestGetStaleQueuedExecutions_LegacyTimestampFormat(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	staleDuration := time.Hour
	now := time.Now()

	// Pre-#4345 rows were written via Go's default time.Time.String()
	// formatting (what modernc.org/sqlite falls back to without
	// _time_format=sqlite) — reproduce that exact on-disk text layout
	// directly, bypassing the store's own (now-fixed) write path.
	const legacyLayout = "2006-01-02 15:04:05.999999999 -0700 MST"

	insertLegacy := func(id, taskID string, createdAt time.Time) {
		t.Helper()
		if err := store.SaveExecution(&Execution{ID: id, TaskID: taskID, ProjectPath: "/proj-legacy", Status: "queued"}); err != nil {
			t.Fatalf("SaveExecution %s: %v", id, err)
		}
		legacyText := createdAt.UTC().Format(legacyLayout)
		if _, err := store.db.Exec(`UPDATE executions SET created_at = ? WHERE id = ?`, legacyText, id); err != nil {
			t.Fatalf("set legacy-format created_at for %s: %v", id, err)
		}
	}

	insertLegacy("legacy-stale", "TASK-LEGACY-STALE", now.Add(-2*time.Hour))
	insertLegacy("legacy-fresh", "TASK-LEGACY-FRESH", now)

	results, err := store.GetStaleQueuedExecutions(staleDuration)
	if err != nil {
		t.Fatalf("GetStaleQueuedExecutions: %v", err)
	}

	got := make(map[string]bool, len(results))
	for _, r := range results {
		got[r.ID] = true
	}

	if !got["legacy-stale"] {
		t.Errorf("expected legacy-format stale row to be detected as stale, results: %v", results)
	}
	if got["legacy-fresh"] {
		t.Errorf("expected legacy-format fresh row NOT to be detected as stale, results: %v", results)
	}
}

// TestGetClaimedNonTerminalExecutions is the GH-4392 regression test for the
// store method backing Dispatcher.Start's boot-time orphan reconciliation:
// only rows that are BOTH non-terminal ('queued'/'running') AND hold an
// execution_claims row must come back — a non-terminal row with no claim was
// never inserted through ExecutionLifecycle.Begin and cannot be blocking a
// future dispatch attempt via the claim mechanism (GH-3732's restart
// adoption owns that case instead).
func TestGetClaimedNonTerminalExecutions(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Claimed + queued: must be returned.
	if err := store.SaveExecution(&Execution{ID: "exec-claimed-q", TaskID: "TASK-CLAIMED-Q", ProjectPath: "/proj", Status: "queued"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if _, err := store.ClaimExecution("TASK-CLAIMED-Q", "/proj", 0, "exec-claimed-q"); err != nil {
		t.Fatalf("ClaimExecution: %v", err)
	}

	// Claimed + running: must be returned.
	if err := store.SaveExecution(&Execution{ID: "exec-claimed-r", TaskID: "TASK-CLAIMED-R", ProjectPath: "/proj", Status: "running"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if _, err := store.ClaimExecution("TASK-CLAIMED-R", "/proj", 0, "exec-claimed-r"); err != nil {
		t.Fatalf("ClaimExecution: %v", err)
	}

	// Claimed + completed (terminal): must NOT be returned.
	if err := store.SaveExecution(&Execution{ID: "exec-claimed-done", TaskID: "TASK-CLAIMED-DONE", ProjectPath: "/proj", Status: "completed"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if _, err := store.ClaimExecution("TASK-CLAIMED-DONE", "/proj", 0, "exec-claimed-done"); err != nil {
		t.Fatalf("ClaimExecution: %v", err)
	}

	// Unclaimed + queued (bare SaveExecution, e.g. GH-3732 restart-adoption
	// fixtures): must NOT be returned.
	if err := store.SaveExecution(&Execution{ID: "exec-unclaimed-q", TaskID: "TASK-UNCLAIMED-Q", ProjectPath: "/proj", Status: "queued"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	results, err := store.GetClaimedNonTerminalExecutions()
	if err != nil {
		t.Fatalf("GetClaimedNonTerminalExecutions: %v", err)
	}

	got := make(map[string]bool, len(results))
	for _, r := range results {
		got[r.ID] = true
	}

	for _, wantID := range []string{"exec-claimed-q", "exec-claimed-r"} {
		if !got[wantID] {
			t.Errorf("expected %s in claimed non-terminal results, got %v", wantID, results)
		}
	}
	for _, notWantID := range []string{"exec-claimed-done", "exec-unclaimed-q"} {
		if got[notWantID] {
			t.Errorf("did not expect %s in claimed non-terminal results, got %v", notWantID, results)
		}
	}
	if len(results) != 2 {
		t.Errorf("expected 2 claimed non-terminal executions, got %d: %v", len(results), results)
	}
}

// TestGetStaleRunningExecutions_UsesStartedAtNotCreatedAt is the GH-4033
// regression test: a decomposed subtask's execution row is created (queued) at
// decomposition time but can legitimately sit behind a FIFO sibling for a while
// before the worker actually starts it. Staleness must be measured from
// started_at (execution start), not created_at (queue time), or a subtask still
// actively running gets evicted as "stuck" once its queue age alone crosses the
// threshold — exactly what happened to GH-4021 (queued 18:09, started 18:34,
// evicted 18:50 with a reported stuck_for of 41m computed off the 18:09 queue time).
func TestGetStaleRunningExecutions_UsesStartedAtNotCreatedAt(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	staleDuration := time.Hour
	now := time.Now()

	tests := []struct {
		name      string
		createdAt time.Time
		startedAt *time.Time // nil leaves started_at NULL
		wantStale bool
	}{
		{
			// GH-4021 shape: queued well past the threshold (subtask sat behind
			// a sibling), but execution only began recently — must NOT be
			// evicted while it's still legitimately running.
			name:      "decomposed subtask queued past threshold but started recently",
			createdAt: now.Add(-2 * time.Hour),
			startedAt: timePtr(now.Add(-2 * time.Minute)),
			wantStale: false,
		},
		{
			// Genuinely stuck: execution itself started past the threshold.
			name:      "execution started past threshold",
			createdAt: now.Add(-3 * time.Hour),
			startedAt: timePtr(now.Add(-2 * time.Hour)),
			wantStale: true,
		},
		{
			// Never transitioned through UpdateExecutionStatus (started_at NULL,
			// e.g. a row inserted directly with status='running') — falls back
			// to created_at.
			name:      "no started_at falls back to created_at",
			createdAt: now.Add(-2 * time.Hour),
			startedAt: nil,
			wantStale: true,
		},
		{
			name:      "fresh execution not stale",
			createdAt: now,
			startedAt: timePtr(now),
			wantStale: false,
		},
	}

	var wantStaleIDs []string
	for i, tt := range tests {
		id := fmt.Sprintf("exec-gh4033-%d", i)
		if err := store.SaveExecution(&Execution{
			ID:          id,
			TaskID:      fmt.Sprintf("GH-4021-%d", i+1),
			ProjectPath: "/proj",
			Status:      "running",
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", id, err)
		}
		if _, err := store.db.Exec(
			`UPDATE executions SET created_at = ? WHERE id = ?`, tt.createdAt, id,
		); err != nil {
			t.Fatalf("set created_at for %s: %v", id, err)
		}
		if tt.startedAt != nil {
			if _, err := store.db.Exec(
				`UPDATE executions SET started_at = ? WHERE id = ?`, *tt.startedAt, id,
			); err != nil {
				t.Fatalf("set started_at for %s: %v", id, err)
			}
		}
		if tt.wantStale {
			wantStaleIDs = append(wantStaleIDs, id)
		}
	}

	results, err := store.GetStaleRunningExecutions(staleDuration)
	if err != nil {
		t.Fatalf("GetStaleRunningExecutions: %v", err)
	}

	gotIDs := make(map[string]bool, len(results))
	for _, r := range results {
		gotIDs[r.ID] = true
	}

	for i, tt := range tests {
		id := fmt.Sprintf("exec-gh4033-%d", i)
		if gotIDs[id] != tt.wantStale {
			t.Errorf("%s: id=%s wantStale=%v gotStale=%v", tt.name, id, tt.wantStale, gotIDs[id])
		}
	}

	if len(results) != len(wantStaleIDs) {
		t.Errorf("expected %d stale executions, got %d (%v)", len(wantStaleIDs), len(results), gotIDs)
	}
}

func timePtr(t time.Time) *time.Time { return &t }

func TestUpdateExecutionStatusByTaskID_UpdatesFailedToCompleted(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{
		ID:          "exec-fail-1",
		TaskID:      "GH-100",
		ProjectPath: "/tmp/proj",
		Status:      "failed",
		Error:       "quality gate failed",
	})

	if err := store.UpdateExecutionStatusByTaskID("GH-100", "/tmp/proj", "completed"); err != nil {
		t.Fatalf("UpdateExecutionStatusByTaskID failed: %v", err)
	}

	exec, err := store.GetExecution("exec-fail-1")
	if err != nil {
		t.Fatalf("GetExecution failed: %v", err)
	}
	if exec.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", exec.Status)
	}
	if exec.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

func TestUpdateExecutionStatusByTaskID_SkipsNonFailed(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{
		ID:          "exec-ok-1",
		TaskID:      "GH-200",
		ProjectPath: "/tmp/proj",
		Status:      "completed",
	})

	if err := store.UpdateExecutionStatusByTaskID("GH-200", "/tmp/proj", "completed"); err != nil {
		t.Fatalf("UpdateExecutionStatusByTaskID failed: %v", err)
	}

	exec, _ := store.GetExecution("exec-ok-1")
	// Status should remain "completed" — the WHERE clause only targets "failed"
	if exec.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", exec.Status)
	}
}

func TestUpdateExecutionStatusByTaskID_NoMatchingTask(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Should not error even with no matching rows
	if err := store.UpdateExecutionStatusByTaskID("GH-999", "/tmp/proj", "completed"); err != nil {
		t.Fatalf("expected no error for non-existent task, got: %v", err)
	}
}

// TestUpdateExecutionStatusByTaskID_ScopedToProject verifies that updating by task ID
// only affects rows matching the given project path (D3 regression).
func TestUpdateExecutionStatusByTaskID_ScopedToProject(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Same task ID, different projects
	_ = store.SaveExecution(&Execution{ID: "exec-a", TaskID: "GH-300", ProjectPath: "/proj/a", Status: "failed"})
	_ = store.SaveExecution(&Execution{ID: "exec-b", TaskID: "GH-300", ProjectPath: "/proj/b", Status: "failed"})

	// Only heal project a
	if err := store.UpdateExecutionStatusByTaskID("GH-300", "/proj/a", "completed"); err != nil {
		t.Fatalf("UpdateExecutionStatusByTaskID: %v", err)
	}

	execA, _ := store.GetExecution("exec-a")
	if execA.Status != "completed" {
		t.Errorf("exec-a: expected 'completed', got %q", execA.Status)
	}
	execB, _ := store.GetExecution("exec-b")
	if execB.Status != "failed" {
		t.Errorf("exec-b: expected 'failed' (unaffected), got %q", execB.Status)
	}
}

// TestSelfHealExecutionAfterMerge_ScopedToProject verifies that self-heal only
// promotes rows matching the given project path (D3 regression).
func TestSelfHealExecutionAfterMerge_ScopedToProject(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "heal-a", TaskID: "GH-400", ProjectPath: "/proj/a", Status: "failed"})
	_ = store.SaveExecution(&Execution{ID: "heal-b", TaskID: "GH-400", ProjectPath: "/proj/b", Status: "failed"})

	if err := store.SelfHealExecutionAfterMerge("GH-400", "/proj/a", "https://github.com/org/repo/pull/1"); err != nil {
		t.Fatalf("SelfHealExecutionAfterMerge: %v", err)
	}

	execA, _ := store.GetExecution("heal-a")
	if execA.Status != "completed" {
		t.Errorf("heal-a: expected 'completed', got %q", execA.Status)
	}
	if execA.PRUrl != "https://github.com/org/repo/pull/1" {
		t.Errorf("heal-a: expected pr_url to be stamped, got %q", execA.PRUrl)
	}
	execB, _ := store.GetExecution("heal-b")
	if execB.Status != "failed" {
		t.Errorf("heal-b: expected 'failed' (unaffected), got %q", execB.Status)
	}
}

// TestSelfHealExecutionAfterMerge_EmptyProjectPath verifies that an empty
// projectPath falls back to task_id-only matching (legacy single-repo behavior),
// so a caller that cannot supply the discriminator still heals every matching row
// rather than silently matching nothing. TASK-352.
func TestSelfHealExecutionAfterMerge_EmptyProjectPath(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "e1", TaskID: "GH-500", ProjectPath: "/proj/a", Status: "failed"})
	_ = store.SaveExecution(&Execution{ID: "e2", TaskID: "GH-500", ProjectPath: "/proj/b", Status: "failed"})

	if err := store.SelfHealExecutionAfterMerge("GH-500", "", "https://github.com/org/repo/pull/9"); err != nil {
		t.Fatalf("SelfHealExecutionAfterMerge: %v", err)
	}

	for _, id := range []string{"e1", "e2"} {
		ex, _ := store.GetExecution(id)
		if ex.Status != "completed" {
			t.Errorf("%s: expected 'completed' with empty projectPath fallback, got %q", id, ex.Status)
		}
	}
}

// TestGetExecutionStatusByTaskID_ExactMatchScopedToProject verifies the lookup
// matches task_id exactly (no substring fallback) and stays scoped to project_path,
// so a no_op verdict for one repo's "GH-6" can't be borrowed by another repo's
// same-numbered issue. GH-3780.
func TestGetExecutionStatusByTaskID_ExactMatchScopedToProject(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "e1", TaskID: "GH-6", ProjectPath: "/proj/a", Status: "no_op"})
	_ = store.SaveExecution(&Execution{ID: "e2", TaskID: "GH-6", ProjectPath: "/proj/b", Status: "failed"})
	// A task ID that would falsely substring-match "GH-6" (e.g. GetLatestExecutionByTaskID's
	// "%GH-6%" fallback) must not be returned instead of the exact match.
	_ = store.SaveExecution(&Execution{ID: "e3", TaskID: "GH-60", ProjectPath: "/proj/a", Status: "completed"})

	status, err := store.GetExecutionStatusByTaskID("GH-6", "/proj/a")
	if err != nil {
		t.Fatalf("GetExecutionStatusByTaskID: %v", err)
	}
	if status != "no_op" {
		t.Errorf("status = %q, want %q", status, "no_op")
	}

	status, err = store.GetExecutionStatusByTaskID("GH-6", "/proj/b")
	if err != nil {
		t.Fatalf("GetExecutionStatusByTaskID: %v", err)
	}
	if status != "failed" {
		t.Errorf("status = %q, want %q", status, "failed")
	}

	if _, err := store.GetExecutionStatusByTaskID("GH-6", "/proj/c"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows for unmatched project, got %v", err)
	}
}

// TestGetExecutionStatusByTaskID_EmptyProjectPath verifies that an empty
// projectPath falls back to task_id-only matching, mirroring
// SelfHealExecutionAfterMerge's legacy single-repo behavior. GH-3780.
func TestGetExecutionStatusByTaskID_EmptyProjectPath(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "e1", TaskID: "GH-7", ProjectPath: "/proj/a", Status: "no_op"})

	status, err := store.GetExecutionStatusByTaskID("GH-7", "")
	if err != nil {
		t.Fatalf("GetExecutionStatusByTaskID: %v", err)
	}
	if status != "no_op" {
		t.Errorf("status = %q, want %q", status, "no_op")
	}
}

// TestRecentCompletedTelemetryStats verifies the zero-token telemetry gap
// query: rows are filtered to completed runs with a real commit, and rows
// without commit_sha (e.g. epic orchestrators) are excluded so they don't
// inflate the gap ratio. GH-2428.
func TestRecentCompletedTelemetryStats(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	type rec struct {
		id     string
		status string
		commit string
		tokens int64
	}
	rows := []rec{
		{"a", "completed", "deadbeef", 100}, // counts, not zero
		{"b", "completed", "cafe1234", 0},   // counts, zero
		{"c", "completed", "ba5eba11", 0},   // counts, zero
		{"d", "completed", "", 0},           // SKIPPED (no commit — epic orchestrator)
		{"e", "failed", "feedface", 0},      // SKIPPED (not completed)
	}
	for _, r := range rows {
		if err := store.SaveExecution(&Execution{
			ID:          r.id,
			TaskID:      "T-" + r.id,
			ProjectPath: "/x",
			Status:      r.status,
			CommitSHA:   r.commit,
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", r.id, err)
		}
		if err := store.SaveExecutionMetrics(&ExecutionMetrics{
			ExecutionID: r.id,
			TokensTotal: r.tokens,
		}); err != nil {
			t.Fatalf("SaveExecutionMetrics %s: %v", r.id, err)
		}
	}

	stats, err := store.RecentCompletedTelemetryStats(50)
	if err != nil {
		t.Fatalf("RecentCompletedTelemetryStats: %v", err)
	}
	if stats.CompletedRuns != 3 {
		t.Errorf("CompletedRuns = %d, want 3 (only completed+commit rows)", stats.CompletedRuns)
	}
	if stats.ZeroTokenRuns != 2 {
		t.Errorf("ZeroTokenRuns = %d, want 2", stats.ZeroTokenRuns)
	}
}

func TestInvalidateCompletion(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	taskID := "GH-500"
	projectPath := "/project"

	// Insert a genuine completed execution (no error, with deliverable).
	_ = store.SaveExecution(&Execution{
		ID:          "exec-genuine",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "completed",
		CommitSHA:   "sha-genuine",
	})

	// Insert an orphan-recovered execution (status=completed, error set).
	_ = store.SaveExecution(&Execution{
		ID:          "exec-orphan",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "running",
	})
	_ = store.UpdateExecutionStatus("exec-orphan", "completed", "stale running task recovered (orphaned worker)")

	// Confirm HasCompletedExecution sees the genuine one.
	completed, err := store.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasCompletedExecution: %v", err)
	}
	if !completed {
		t.Fatal("expected true before invalidation")
	}

	// Invalidate: should remove the genuine row only.
	if err := store.InvalidateCompletion(taskID, projectPath); err != nil {
		t.Fatalf("InvalidateCompletion: %v", err)
	}

	// HasCompletedExecution should now return false.
	completed, err = store.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasCompletedExecution after invalidation: %v", err)
	}
	if completed {
		t.Error("expected false after invalidation")
	}

	// Calling again on already-empty set should be a no-op (no error).
	if err := store.InvalidateCompletion(taskID, projectPath); err != nil {
		t.Errorf("InvalidateCompletion on empty set: %v", err)
	}

	// Different project path should be unaffected — add a new genuine row and check.
	otherPath := "/other-project"
	_ = store.SaveExecution(&Execution{
		ID:          "exec-other",
		TaskID:      taskID,
		ProjectPath: otherPath,
		Status:      "completed",
		CommitSHA:   "sha-other",
	})
	if err := store.InvalidateCompletion(taskID, projectPath); err != nil {
		t.Fatalf("InvalidateCompletion: %v", err)
	}
	completed, _ = store.HasCompletedExecution(taskID, otherPath)
	if !completed {
		t.Error("InvalidateCompletion should not affect different project path")
	}
}

// TestReclassifyCompletionAsFailed covers GH-3818/D10: a PR closed without
// merging must demote the "completed" execution row so HasCompletedExecution
// stops trusting it (re-pick), while a genuinely merged PR's row is left
// untouched so idempotency still holds.
func TestReclassifyCompletionAsFailed(t *testing.T) {
	tests := []struct {
		name            string
		row             Execution
		taskID          string
		projectPath     string
		reason          string
		wantStillDone   bool // HasCompletedExecution result after reclassify
		wantStatusAfter string
	}{
		{
			name: "completed with PR closed unmerged - reclassified to failed (re-pick allowed)",
			row: Execution{
				ID: "exec-closed-unmerged", TaskID: "GH-3789", ProjectPath: "/project",
				Status: "completed", PRUrl: "https://github.com/o/r/pull/3802",
			},
			taskID:          "GH-3789",
			projectPath:     "/project",
			reason:          "CI checks failed; PR closed without merge",
			wantStillDone:   false,
			wantStatusAfter: "failed",
		},
		{
			name: "different task ID - untouched",
			row: Execution{
				ID: "exec-other-task", TaskID: "GH-999", ProjectPath: "/project",
				Status: "completed", PRUrl: "https://github.com/o/r/pull/1",
			},
			taskID:          "GH-3789", // reclassify call targets a different task
			projectPath:     "/project",
			reason:          "closed without merge",
			wantStillDone:   true, // GH-999's own row is unaffected; check with its own ID below
			wantStatusAfter: "completed",
		},
		{
			name: "different project path - untouched (cross-repo isolation)",
			row: Execution{
				ID: "exec-other-project", TaskID: "GH-3789", ProjectPath: "/other-project",
				Status: "completed", PRUrl: "https://github.com/o/r/pull/2",
			},
			taskID:          "GH-3789",
			projectPath:     "/project", // reclassify call scoped to a different project
			reason:          "closed without merge",
			wantStillDone:   true,
			wantStatusAfter: "completed",
		},
		{
			name: "orphan-recovered row (error set) - not a genuine completion, untouched",
			row: Execution{
				ID: "exec-orphan", TaskID: "GH-3789", ProjectPath: "/project",
				Status: "completed", Error: "stale running task recovered", PRUrl: "https://github.com/o/r/pull/3",
			},
			taskID:          "GH-3789",
			projectPath:     "/project",
			reason:          "closed without merge",
			wantStillDone:   false, // HasCompletedExecution already excludes error!='' rows
			wantStatusAfter: "completed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			store, err := NewStore(tmpDir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			if err := store.SaveExecution(&tt.row); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			if err := store.ReclassifyCompletionAsFailed(tt.taskID, tt.projectPath, tt.reason); err != nil {
				t.Fatalf("ReclassifyCompletionAsFailed: %v", err)
			}

			completed, err := store.HasCompletedExecution(tt.row.TaskID, tt.row.ProjectPath)
			if err != nil {
				t.Fatalf("HasCompletedExecution: %v", err)
			}
			if completed != tt.wantStillDone {
				t.Errorf("HasCompletedExecution(%s, %s) = %v, want %v", tt.row.TaskID, tt.row.ProjectPath, completed, tt.wantStillDone)
			}

			got, err := store.GetExecution(tt.row.ID)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if got.Status != tt.wantStatusAfter {
				t.Errorf("status after reclassify = %q, want %q", got.Status, tt.wantStatusAfter)
			}
		})
	}
}

// TestReclassifyCompletionAsFailed_HealsBackOnMerge verifies the round trip: a
// row demoted by ReclassifyCompletionAsFailed (PR closed unmerged) is later
// healed back to "completed" by SelfHealExecutionAfterMerge when a follow-up
// PR for the same issue actually merges — this is the "not a one-way trip"
// guarantee the reclassify doc comment promises. Acceptance: completed
// execution + merged PR must still be skipped as idempotent.
func TestReclassifyCompletionAsFailed_HealsBackOnMerge(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	taskID := "GH-3789"
	projectPath := "/project"

	if err := store.SaveExecution(&Execution{
		ID: "exec-1", TaskID: taskID, ProjectPath: projectPath,
		Status: "completed", PRUrl: "https://github.com/o/r/pull/3802",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	// PR #3802 closes unmerged (CI failure) — poller must re-pick the issue.
	if err := store.ReclassifyCompletionAsFailed(taskID, projectPath, "CI checks failed"); err != nil {
		t.Fatalf("ReclassifyCompletionAsFailed: %v", err)
	}
	if completed, _ := store.HasCompletedExecution(taskID, projectPath); completed {
		t.Fatal("expected re-pick (HasCompletedExecution=false) after unmerged close")
	}

	// A follow-up PR (#3810) for the same issue merges — self-heal must restore
	// "completed" so idempotency resumes.
	if err := store.SelfHealExecutionAfterMerge(taskID, projectPath, "https://github.com/o/r/pull/3810"); err != nil {
		t.Fatalf("SelfHealExecutionAfterMerge: %v", err)
	}
	completed, err := store.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasCompletedExecution: %v", err)
	}
	if !completed {
		t.Error("expected idempotency restored (HasCompletedExecution=true) after merge")
	}
}

// TestTerminateNonTerminalExecution covers GH-4499: a PR closed externally
// while its execution row was still queued/pending/running (never reached
// "completed") must have that row terminated too, mirroring the sibling
// TestReclassifyCompletionAsFailed coverage for the already-completed case.
// ReclassifyCompletionAsFailed's completed-only guard is intentionally left
// unchanged — these two methods cover disjoint status sets.
func TestTerminateNonTerminalExecution(t *testing.T) {
	tests := []struct {
		name            string
		row             Execution
		taskID          string
		projectPath     string
		reason          string
		wantStatusAfter string
		wantTerminated  bool // expect error=reason and completed_at set
	}{
		{
			name: "running row - terminated to failed",
			row: Execution{
				ID: "exec-running", TaskID: "GH-4499", ProjectPath: "/project",
				Status: "running",
			},
			taskID:          "GH-4499",
			projectPath:     "/project",
			reason:          "closed without merging (no reason recorded)",
			wantStatusAfter: "failed",
			wantTerminated:  true,
		},
		{
			name: "queued row - terminated to failed",
			row: Execution{
				ID: "exec-queued", TaskID: "GH-4499", ProjectPath: "/project",
				Status: "queued",
			},
			taskID:          "GH-4499",
			projectPath:     "/project",
			reason:          "PR closed without merging",
			wantStatusAfter: "failed",
			wantTerminated:  true,
		},
		{
			name: "pending row - terminated to failed",
			row: Execution{
				ID: "exec-pending", TaskID: "GH-4499", ProjectPath: "/project",
				Status: "pending",
			},
			taskID:          "GH-4499",
			projectPath:     "/project",
			reason:          "PR closed without merging",
			wantStatusAfter: "failed",
			wantTerminated:  true,
		},
		{
			name: "completed row - untouched (ReclassifyCompletionAsFailed's job, not this method's)",
			row: Execution{
				ID: "exec-completed", TaskID: "GH-4499", ProjectPath: "/project",
				Status: "completed", PRUrl: "https://github.com/o/r/pull/1",
			},
			taskID:          "GH-4499",
			projectPath:     "/project",
			reason:          "PR closed without merging",
			wantStatusAfter: "completed",
			wantTerminated:  false,
		},
		{
			name: "already failed row - untouched (idempotent)",
			row: Execution{
				ID: "exec-failed", TaskID: "GH-4499", ProjectPath: "/project",
				Status: "failed", Error: "prior failure",
			},
			taskID:          "GH-4499",
			projectPath:     "/project",
			reason:          "PR closed without merging",
			wantStatusAfter: "failed",
			wantTerminated:  false,
		},
		{
			name: "different task ID - untouched",
			row: Execution{
				ID: "exec-other-task", TaskID: "GH-999", ProjectPath: "/project",
				Status: "running",
			},
			taskID:          "GH-4499", // terminate call targets a different task
			projectPath:     "/project",
			reason:          "PR closed without merging",
			wantStatusAfter: "running",
			wantTerminated:  false,
		},
		{
			name: "different project path - untouched (cross-repo isolation)",
			row: Execution{
				ID: "exec-other-project", TaskID: "GH-4499", ProjectPath: "/other-project",
				Status: "running",
			},
			taskID:          "GH-4499",
			projectPath:     "/project", // terminate call scoped to a different project
			reason:          "PR closed without merging",
			wantStatusAfter: "running",
			wantTerminated:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			store, err := NewStore(tmpDir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			if err := store.SaveExecution(&tt.row); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			if err := store.TerminateNonTerminalExecution(tt.taskID, tt.projectPath, tt.reason); err != nil {
				t.Fatalf("TerminateNonTerminalExecution: %v", err)
			}

			got, err := store.GetExecution(tt.row.ID)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if got.Status != tt.wantStatusAfter {
				t.Errorf("status after terminate = %q, want %q", got.Status, tt.wantStatusAfter)
			}
			if tt.wantTerminated {
				if got.CompletedAt == nil {
					t.Error("expected completed_at to be set after terminate")
				}
				if got.Error != tt.reason {
					t.Errorf("error = %q, want %q", got.Error, tt.reason)
				}
			}
		})
	}
}

// TestTerminateNonTerminalExecution_NewerRowShields verifies that only the
// latest execution row (same created_at DESC, rowid DESC selection as
// GetExecutionStatusByTaskID) is eligible for termination — a newer row from
// a live retry must shield an older non-terminal row from being touched.
// GH-4499.
func TestTerminateNonTerminalExecution_NewerRowShields(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	taskID := "GH-4499"
	projectPath := "/project"
	base := time.Now().Add(-time.Hour)

	// Older row is stuck running (e.g. a stale execution that never reached
	// a terminal status before this PR close was observed).
	if err := store.SaveExecution(&Execution{
		ID: "exec-old", TaskID: taskID, ProjectPath: projectPath,
		Status: "running", CreatedAt: base,
	}); err != nil {
		t.Fatalf("SaveExecution (old): %v", err)
	}

	// A newer row for the same task is a live retry that already completed
	// — this is the row the close actually pertains to.
	if err := store.SaveExecution(&Execution{
		ID: "exec-new", TaskID: taskID, ProjectPath: projectPath,
		Status: "completed", PRUrl: "https://github.com/o/r/pull/2", CreatedAt: base.Add(time.Minute),
	}); err != nil {
		t.Fatalf("SaveExecution (new): %v", err)
	}

	if err := store.TerminateNonTerminalExecution(taskID, projectPath, "PR closed without merging"); err != nil {
		t.Fatalf("TerminateNonTerminalExecution: %v", err)
	}

	oldRow, err := store.GetExecution("exec-old")
	if err != nil {
		t.Fatalf("GetExecution(exec-old): %v", err)
	}
	if oldRow.Status != "running" {
		t.Errorf("old row status = %q, want %q (shielded by newer row)", oldRow.Status, "running")
	}

	newRow, err := store.GetExecution("exec-new")
	if err != nil {
		t.Fatalf("GetExecution(exec-new): %v", err)
	}
	if newRow.Status != "completed" {
		t.Errorf("new row status = %q, want %q (untouched by TerminateNonTerminalExecution)", newRow.Status, "completed")
	}
}

// TestReclassifyCompletionAsSuperseded covers GH-4701: ReclassifyCompletionAsFailed's
// sibling for a close notifyExternalClose can prove was deliberate operator
// cleanup (issue closed not-planned, or already tagged pilot-superseded)
// rather than a genuine failure — the row must land on "superseded", not
// "failed", so HISTORY renders it muted instead of as a pipeline ✗.
func TestReclassifyCompletionAsSuperseded(t *testing.T) {
	tests := []struct {
		name            string
		row             Execution
		taskID          string
		projectPath     string
		wantStatusAfter string
	}{
		{
			name: "completed with PR closed as superseded - reclassified to superseded, not failed",
			row: Execution{
				ID: "exec-superseded", TaskID: "GH-4701", ProjectPath: "/project",
				Status: "completed", PRUrl: "https://github.com/o/r/pull/4701",
			},
			taskID:          "GH-4701",
			projectPath:     "/project",
			wantStatusAfter: "superseded",
		},
		{
			name: "different task ID - untouched",
			row: Execution{
				ID: "exec-other-task", TaskID: "GH-999", ProjectPath: "/project",
				Status: "completed", PRUrl: "https://github.com/o/r/pull/1",
			},
			taskID:          "GH-4701", // reclassify call targets a different task
			projectPath:     "/project",
			wantStatusAfter: "completed",
		},
		{
			name: "different project path - untouched (cross-repo isolation)",
			row: Execution{
				ID: "exec-other-project", TaskID: "GH-4701", ProjectPath: "/other-project",
				Status: "completed", PRUrl: "https://github.com/o/r/pull/2",
			},
			taskID:          "GH-4701",
			projectPath:     "/project", // reclassify call scoped to a different project
			wantStatusAfter: "completed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			store, err := NewStore(tmpDir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			if err := store.SaveExecution(&tt.row); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			if err := store.ReclassifyCompletionAsSuperseded(tt.taskID, tt.projectPath, "source issue closed as not-planned"); err != nil {
				t.Fatalf("ReclassifyCompletionAsSuperseded: %v", err)
			}

			got, err := store.GetExecution(tt.row.ID)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if got.Status != tt.wantStatusAfter {
				t.Errorf("status after reclassify = %q, want %q", got.Status, tt.wantStatusAfter)
			}
		})
	}
}

// TestTerminateNonTerminalExecutionAsSuperseded covers GH-4701:
// TerminateNonTerminalExecution's sibling for the same deliberate-close case
// — a still-running/queued/pending row must land on "superseded", not
// "failed".
func TestTerminateNonTerminalExecutionAsSuperseded(t *testing.T) {
	tests := []struct {
		name            string
		row             Execution
		taskID          string
		projectPath     string
		wantStatusAfter string
	}{
		{
			name: "running row - terminated to superseded",
			row: Execution{
				ID: "exec-running", TaskID: "GH-4701", ProjectPath: "/project",
				Status: "running",
			},
			taskID:          "GH-4701",
			projectPath:     "/project",
			wantStatusAfter: "superseded",
		},
		{
			name: "queued row - terminated to superseded",
			row: Execution{
				ID: "exec-queued", TaskID: "GH-4701", ProjectPath: "/project",
				Status: "queued",
			},
			taskID:          "GH-4701",
			projectPath:     "/project",
			wantStatusAfter: "superseded",
		},
		{
			name: "completed row - untouched (ReclassifyCompletionAsSuperseded's job, not this method's)",
			row: Execution{
				ID: "exec-completed", TaskID: "GH-4701", ProjectPath: "/project",
				Status: "completed", PRUrl: "https://github.com/o/r/pull/1",
			},
			taskID:          "GH-4701",
			projectPath:     "/project",
			wantStatusAfter: "completed",
		},
		{
			name: "different task ID - untouched",
			row: Execution{
				ID: "exec-other-task", TaskID: "GH-999", ProjectPath: "/project",
				Status: "running",
			},
			taskID:          "GH-4701", // terminate call targets a different task
			projectPath:     "/project",
			wantStatusAfter: "running",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			store, err := NewStore(tmpDir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			if err := store.SaveExecution(&tt.row); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			if err := store.TerminateNonTerminalExecutionAsSuperseded(tt.taskID, tt.projectPath, "source issue closed as not-planned"); err != nil {
				t.Fatalf("TerminateNonTerminalExecutionAsSuperseded: %v", err)
			}

			got, err := store.GetExecution(tt.row.ID)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if got.Status != tt.wantStatusAfter {
				t.Errorf("status after terminate = %q, want %q", got.Status, tt.wantStatusAfter)
			}
		})
	}
}

func TestGetLifetimeTokens_ExcludesZeroTokenRows(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Row with real tokens
	if err := store.SaveExecution(&Execution{ID: "exec-real", TaskID: "T-1", ProjectPath: "/p", Status: "completed"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.SaveExecutionMetrics(&ExecutionMetrics{ExecutionID: "exec-real", TokensInput: 5000, TokensOutput: 2000, TokensTotal: 7000, EstimatedCostUSD: 0.50}); err != nil {
		t.Fatalf("SaveExecutionMetrics: %v", err)
	}

	// Dispatcher queue / early-failure row with zero tokens
	if err := store.SaveExecution(&Execution{ID: "exec-zero", TaskID: "T-2", ProjectPath: "/p", Status: "failed"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	lt, err := store.GetLifetimeTokens("")
	if err != nil {
		t.Fatalf("GetLifetimeTokens: %v", err)
	}
	if lt.TotalTokens != 7000 {
		t.Errorf("TotalTokens = %d, want 7000 (zero-token row must be excluded)", lt.TotalTokens)
	}
	if lt.TotalCostUSD != 0.50 {
		t.Errorf("TotalCostUSD = %.4f, want 0.5000", lt.TotalCostUSD)
	}

	// Project-scoped filter must also exclude zero-token rows.
	ltScoped, err := store.GetLifetimeTokens("/p")
	if err != nil {
		t.Fatalf("GetLifetimeTokens(/p): %v", err)
	}
	if ltScoped.TotalTokens != 7000 {
		t.Errorf("scoped TotalTokens = %d, want 7000 (zero-token row must be excluded under filter)", ltScoped.TotalTokens)
	}
	if ltScoped.TotalCostUSD != 0.50 {
		t.Errorf("scoped TotalCostUSD = %.4f, want 0.5000", ltScoped.TotalCostUSD)
	}
}

func TestGetDailyMetrics_ExcludesZeroTokenRows(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	yesterday := now.Add(-24 * time.Hour)

	// Real execution
	if err := store.SaveExecution(&Execution{ID: "dm-real", TaskID: "T-1", ProjectPath: "/p", Status: "completed", CreatedAt: yesterday}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.SaveExecutionMetrics(&ExecutionMetrics{ExecutionID: "dm-real", TokensInput: 3000, TokensOutput: 1000, TokensTotal: 4000, TokensCacheRead: 90000, TokensCacheWrite: 5000, EstimatedCostUSD: 0.30}); err != nil {
		t.Fatalf("SaveExecutionMetrics: %v", err)
	}

	// Zero-token row (same day) — should not appear in daily metrics
	if err := store.SaveExecution(&Execution{ID: "dm-zero", TaskID: "T-2", ProjectPath: "/p", Status: "failed", CreatedAt: yesterday}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	q := MetricsQuery{Start: yesterday.Add(-time.Hour), End: now.Add(time.Hour)}
	days, err := store.GetDailyMetrics(q)
	if err != nil {
		t.Fatalf("GetDailyMetrics: %v", err)
	}
	if len(days) == 0 {
		t.Fatal("GetDailyMetrics: want at least 1 day row")
	}
	// Only the real execution should be counted
	if days[0].ExecutionCount != 1 {
		t.Errorf("ExecutionCount = %d, want 1 (zero-token row must be excluded)", days[0].ExecutionCount)
	}
	// TASK-390: daily cache sums feed the stacked tokens sparkline.
	if days[0].CacheReadTokens != 90000 {
		t.Errorf("CacheReadTokens = %d, want 90000", days[0].CacheReadTokens)
	}
	if days[0].CacheWriteTokens != 5000 {
		t.Errorf("CacheWriteTokens = %d, want 5000", days[0].CacheWriteTokens)
	}
}

// TestGetDailyMetrics_ExcludesCanaryRows covers GH-4240: a canary sandbox
// execution with real token/cost data must still not appear in daily metrics.
func TestGetDailyMetrics_ExcludesCanaryRows(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	yesterday := now.Add(-24 * time.Hour)

	if err := store.SaveExecution(&Execution{ID: "dm-real2", TaskID: "T-3", ProjectPath: "/p", Status: "completed", CreatedAt: yesterday}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.SaveExecutionMetrics(&ExecutionMetrics{ExecutionID: "dm-real2", TokensTotal: 4000, EstimatedCostUSD: 0.30}); err != nil {
		t.Fatalf("SaveExecutionMetrics: %v", err)
	}

	if err := store.SaveExecution(&Execution{ID: "dm-canary", TaskID: "T-4", ProjectPath: "/canary-sandbox", Status: "completed", CreatedAt: yesterday, IsCanary: true}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.SaveExecutionMetrics(&ExecutionMetrics{ExecutionID: "dm-canary", TokensTotal: 9000, EstimatedCostUSD: 5.00}); err != nil {
		t.Fatalf("SaveExecutionMetrics: %v", err)
	}

	q := MetricsQuery{Start: yesterday.Add(-time.Hour), End: now.Add(time.Hour)}
	days, err := store.GetDailyMetrics(q)
	if err != nil {
		t.Fatalf("GetDailyMetrics: %v", err)
	}
	if len(days) == 0 {
		t.Fatal("GetDailyMetrics: want at least 1 day row")
	}
	if days[0].ExecutionCount != 1 {
		t.Errorf("ExecutionCount = %d, want 1 (canary row must be excluded)", days[0].ExecutionCount)
	}
	if days[0].TotalTokens != 4000 {
		t.Errorf("TotalTokens = %d, want 4000 (canary tokens must be excluded)", days[0].TotalTokens)
	}
}

// TestEffortLevelColumns_MigrationAddsColumns verifies that a fresh DB created by NewStore
// has the effort_level and complexity_level columns in the executions table.
func TestEffortLevelColumns_MigrationAddsColumns(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Query sqlite_master to verify columns exist
	var effortCount, complexityCount int
	row := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('executions') WHERE name='effort_level'`)
	if err := row.Scan(&effortCount); err != nil {
		t.Fatalf("pragma_table_info effort_level: %v", err)
	}
	row = store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('executions') WHERE name='complexity_level'`)
	if err := row.Scan(&complexityCount); err != nil {
		t.Fatalf("pragma_table_info complexity_level: %v", err)
	}

	if effortCount != 1 {
		t.Errorf("effort_level column missing from executions table")
	}
	if complexityCount != 1 {
		t.Errorf("complexity_level column missing from executions table")
	}
}

// TestEffortLevelColumns_BackwardsCompat verifies that opening a DB that already has an
// executions table (but lacks effort_level/complexity_level) runs the migration cleanly
// via the idempotent ALTER TABLE pattern.
func TestEffortLevelColumns_BackwardsCompat(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a DB without the new columns
	store1, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore (first open): %v", err)
	}
	// Insert a row before the migration adds the columns; simulate a pre-existing row.
	_ = store1.SaveExecution(&Execution{
		ID: "pre-migration", TaskID: "T-pre", ProjectPath: "/p", Status: "completed",
	})
	_ = store1.Close()

	// Re-open: migration must run the ALTER TABLE statements idempotently.
	store2, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore (second open, post-migration): %v", err)
	}
	defer func() { _ = store2.Close() }()

	// Pre-existing row must still be readable with null columns coerced to "".
	exec, err := store2.GetExecution("pre-migration")
	if err != nil {
		t.Fatalf("GetExecution after migration: %v", err)
	}
	if exec.EffortLevel != "" {
		t.Errorf("pre-migration row: EffortLevel = %q, want empty", exec.EffortLevel)
	}
	if exec.ComplexityLevel != "" {
		t.Errorf("pre-migration row: ComplexityLevel = %q, want empty", exec.ComplexityLevel)
	}
}

// TestEffortLevelColumns_RoundTrip verifies that SaveExecution persists EffortLevel and
// ComplexityLevel, and GetExecution returns the same values.
func TestEffortLevelColumns_RoundTrip(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	exec := &Execution{
		ID:              "exec-effort-rt",
		TaskID:          "T-effort",
		ProjectPath:     "/project",
		Status:          "completed",
		EffortLevel:     "medium",
		ComplexityLevel: "simple",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	got, err := store.GetExecution("exec-effort-rt")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.EffortLevel != "medium" {
		t.Errorf("EffortLevel = %q, want %q", got.EffortLevel, "medium")
	}
	if got.ComplexityLevel != "simple" {
		t.Errorf("ComplexityLevel = %q, want %q", got.ComplexityLevel, "simple")
	}

	// Also verify UpdateExecutionEffort writes new values.
	if err := store.UpdateExecutionEffort("exec-effort-rt", "high", "complex"); err != nil {
		t.Fatalf("UpdateExecutionEffort: %v", err)
	}
	got2, err := store.GetExecution("exec-effort-rt")
	if err != nil {
		t.Fatalf("GetExecution after update: %v", err)
	}
	if got2.EffortLevel != "high" {
		t.Errorf("after update: EffortLevel = %q, want %q", got2.EffortLevel, "high")
	}
	if got2.ComplexityLevel != "complex" {
		t.Errorf("after update: ComplexityLevel = %q, want %q", got2.ComplexityLevel, "complex")
	}
}

// TestModelNameColumn_NoHardcodedDefault verifies GH-3764(c): a fresh executions
// table's model_name column carries no literal SQL default (dflt_value IS NULL
// in pragma_table_info), so a future NULL row is never silently backfilled with
// a guessed model name.
func TestModelNameColumn_NoHardcodedDefault(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	var dfltValue sql.NullString
	row := store.db.QueryRow(`SELECT dflt_value FROM pragma_table_info('executions') WHERE name = 'model_name'`)
	if err := row.Scan(&dfltValue); err != nil {
		t.Fatalf("pragma_table_info model_name: %v", err)
	}
	if dfltValue.Valid {
		t.Errorf("model_name dflt_value = %q, want no default (NULL)", dfltValue.String)
	}
}

// TestModelNameColumn_NullRendersUnknown verifies GH-3764(c): a row whose
// model_name is NULL (e.g. a pre-migration legacy row, simulated here via a raw
// UPDATE since SaveExecution always writes an explicit value) reads back as
// "unknown" through GetExecution and ExportMetrics rather than a blank string.
func TestModelNameColumn_NullRendersUnknown(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	exec := &Execution{ID: "exec-null-model", TaskID: "T-null-model", ProjectPath: "/p", Status: "completed", CreatedAt: now}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE executions SET model_name = NULL WHERE id = ?`, exec.ID); err != nil {
		t.Fatalf("UPDATE model_name = NULL: %v", err)
	}

	got, err := store.GetExecution("exec-null-model")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.ModelName != "unknown" {
		t.Errorf("GetExecution ModelName = %q, want %q", got.ModelName, "unknown")
	}

	exports, err := store.ExportMetrics(MetricsQuery{Start: now.Add(-time.Hour), End: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("ExportMetrics: %v", err)
	}
	if len(exports) != 1 {
		t.Fatalf("len(exports) = %d, want 1", len(exports))
	}
	if exports[0].ModelName != "unknown" {
		t.Errorf("ExportMetrics ModelName = %q, want %q", exports[0].ModelName, "unknown")
	}
}

// TestPruneExecutionLogs verifies D5: deletes logs older than the cutoff and
// leaves newer ones untouched.
func TestPruneExecutionLogs(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now().Add(-10 * time.Minute)

	// Insert two old entries and one recent entry directly.
	_, err = store.db.Exec(`INSERT INTO execution_logs (timestamp, level, message, component) VALUES (?, 'info', 'old1', 'test')`, old)
	if err != nil {
		t.Fatalf("insert old1: %v", err)
	}
	_, err = store.db.Exec(`INSERT INTO execution_logs (timestamp, level, message, component) VALUES (?, 'info', 'old2', 'test')`, old)
	if err != nil {
		t.Fatalf("insert old2: %v", err)
	}
	_, err = store.db.Exec(`INSERT INTO execution_logs (timestamp, level, message, component) VALUES (?, 'info', 'recent', 'test')`, recent)
	if err != nil {
		t.Fatalf("insert recent: %v", err)
	}

	deleted, err := store.PruneExecutionLogs(time.Hour)
	if err != nil {
		t.Fatalf("PruneExecutionLogs: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_logs`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("remaining rows = %d, want 1", count)
	}
}

// TestRecordPatternFeedback_TransactionAtomicity verifies D6: all three writes
// inside RecordPatternFeedback succeed together — the feedback row, the
// confidence update, and the project-link update.
func TestRecordPatternFeedback_TransactionAtomicity(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Prerequisites: a cross pattern and a project link must exist for the
	// confidence/link UPDATE statements to match rows.
	pattern := &CrossPattern{
		ID:          "pat-tx-1",
		Type:        "code",
		Title:       "test pattern",
		Description: "desc",
		Confidence:  0.5,
		Occurrences: 1,
		Scope:       "org",
	}
	if err := store.SaveCrossPattern(pattern); err != nil {
		t.Fatalf("SaveCrossPattern: %v", err)
	}
	if err := store.LinkPatternToProject("pat-tx-1", "/proj/tx"); err != nil {
		t.Fatalf("LinkPatternToProject: %v", err)
	}
	// Seed an execution row so the FK constraint on pattern_feedback is satisfied.
	if err := store.SaveExecution(&Execution{ID: "exec-tx-1", TaskID: "GH-TX", ProjectPath: "/proj/tx", Status: "completed"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	fb := &PatternFeedback{
		PatternID:       "pat-tx-1",
		ExecutionID:     "exec-tx-1",
		ProjectPath:     "/proj/tx",
		Outcome:         "success",
		ConfidenceDelta: 0.1,
	}
	if err := store.RecordPatternFeedback(fb); err != nil {
		t.Fatalf("RecordPatternFeedback: %v", err)
	}
	if fb.ID == 0 {
		t.Error("expected feedback ID to be set after insert")
	}

	// Confidence should have increased.
	updated, err := store.GetCrossPattern("pat-tx-1")
	if err != nil {
		t.Fatalf("GetCrossPattern: %v", err)
	}
	if updated.Confidence <= 0.5 {
		t.Errorf("confidence = %.3f, want > 0.5", updated.Confidence)
	}

	// Project link success_count should be 1.
	links, err := store.GetProjectsForPattern("pat-tx-1")
	if err != nil {
		t.Fatalf("GetProjectsForPattern: %v", err)
	}
	if len(links) == 0 || links[0].SuccessCount != 1 {
		t.Errorf("success_count = %d, want 1", func() int {
			if len(links) > 0 {
				return links[0].SuccessCount
			}
			return -1
		}())
	}
}

func TestGetRecentExecutions_ProjectFilter(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const pathA = "/project/alpha"
	const pathB = "/project/beta"

	fixture := []struct {
		id   string
		path string
	}{
		{"pf-exec-a1", pathA},
		{"pf-exec-a2", pathA},
		{"pf-exec-a3", pathA},
		{"pf-exec-b1", pathB},
		{"pf-exec-b2", pathB},
	}
	for _, f := range fixture {
		if err := store.SaveExecution(&Execution{
			ID:          f.id,
			TaskID:      "TASK-" + f.id,
			ProjectPath: f.path,
			Status:      "completed",
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", f.id, err)
		}
	}

	all, err := store.GetRecentExecutions(100, "")
	if err != nil {
		t.Fatalf("GetRecentExecutions (all): %v", err)
	}
	if len(all) != 5 {
		t.Errorf("unfiltered: got %d, want 5", len(all))
	}

	forA, err := store.GetRecentExecutions(100, pathA)
	if err != nil {
		t.Fatalf("GetRecentExecutions (pathA): %v", err)
	}
	if len(forA) != 3 {
		t.Errorf("filter=%s: got %d, want 3", pathA, len(forA))
	}
	for _, e := range forA {
		if e.ProjectPath != pathA {
			t.Errorf("unexpected ProjectPath %q in filtered results", e.ProjectPath)
		}
	}

	forB, err := store.GetRecentExecutions(100, pathB)
	if err != nil {
		t.Fatalf("GetRecentExecutions (pathB): %v", err)
	}
	if len(forB) != 2 {
		t.Errorf("filter=%s: got %d, want 2", pathB, len(forB))
	}
}

// TestListProjectsForTask covers GH-4378: task_id is not unique across
// projects, so `pilot trace` needs to know which distinct projects recorded
// executions for a given task_id (and each project's most recent execution)
// in order to disambiguate instead of merging.
func TestListProjectsForTask(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	t.Run("unknown task id returns empty result", func(t *testing.T) {
		got, err := store.ListProjectsForTask("GH-does-not-exist")
		if err != nil {
			t.Fatalf("ListProjectsForTask: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d projects, want 0", len(got))
		}
	})

	t.Run("collision across projects returns each distinct project, newest first", func(t *testing.T) {
		older := time.Now().Add(-time.Hour)
		newer := time.Now()

		if err := store.SaveExecution(&Execution{
			ID: "exec-navigator", TaskID: "GH-1", ProjectPath: "/repos/navigator", Status: "completed", CreatedAt: older,
		}); err != nil {
			t.Fatalf("SaveExecution(exec-navigator): %v", err)
		}
		if err := store.SaveExecution(&Execution{
			ID: "exec-pointer", TaskID: "GH-1", ProjectPath: "/repos/pointer", Status: "completed", CreatedAt: newer,
		}); err != nil {
			t.Fatalf("SaveExecution(exec-pointer): %v", err)
		}

		got, err := store.ListProjectsForTask("GH-1")
		if err != nil {
			t.Fatalf("ListProjectsForTask: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d projects, want 2: %+v", len(got), got)
		}
		if got[0].ProjectPath != "/repos/pointer" {
			t.Errorf("got[0].ProjectPath = %q, want /repos/pointer (most recent)", got[0].ProjectPath)
		}
		if got[1].ProjectPath != "/repos/navigator" {
			t.Errorf("got[1].ProjectPath = %q, want /repos/navigator", got[1].ProjectPath)
		}
	})
}

func TestGetLifetimeTokens_ProjectFilter(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const pathA = "/project/alpha"
	const pathB = "/project/beta"

	saveWithTokens := func(id, path string, input, output int64, cost float64) {
		t.Helper()
		if err := store.SaveExecution(&Execution{
			ID: id, TaskID: "TASK-" + id, ProjectPath: path, Status: "completed",
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", id, err)
		}
		if err := store.SaveExecutionMetrics(&ExecutionMetrics{
			ExecutionID:      id,
			TokensInput:      input,
			TokensOutput:     output,
			TokensTotal:      input + output,
			EstimatedCostUSD: cost,
		}); err != nil {
			t.Fatalf("SaveExecutionMetrics %s: %v", id, err)
		}
	}

	saveWithTokens("lt-pf-a1", pathA, 1000, 500, 0.10)
	saveWithTokens("lt-pf-a2", pathA, 2000, 1000, 0.20)
	saveWithTokens("lt-pf-b1", pathB, 500, 250, 0.05)

	approxEqual := func(a, b float64) bool {
		diff := a - b
		if diff < 0 {
			diff = -diff
		}
		return diff < 1e-9
	}

	ltA, err := store.GetLifetimeTokens(pathA)
	if err != nil {
		t.Fatalf("GetLifetimeTokens(pathA): %v", err)
	}
	if ltA.TotalTokens != 4500 {
		t.Errorf("pathA TotalTokens = %d, want 4500", ltA.TotalTokens)
	}
	if !approxEqual(ltA.TotalCostUSD, 0.30) {
		t.Errorf("pathA TotalCostUSD = %.10f, want ~0.30", ltA.TotalCostUSD)
	}

	ltB, err := store.GetLifetimeTokens(pathB)
	if err != nil {
		t.Fatalf("GetLifetimeTokens(pathB): %v", err)
	}
	if ltB.TotalTokens != 750 {
		t.Errorf("pathB TotalTokens = %d, want 750", ltB.TotalTokens)
	}

	ltAll, err := store.GetLifetimeTokens("")
	if err != nil {
		t.Fatalf("GetLifetimeTokens(all): %v", err)
	}
	if ltAll.TotalTokens != 5250 {
		t.Errorf("unfiltered TotalTokens = %d, want 5250", ltAll.TotalTokens)
	}
}

func TestGetLifetimeTaskCounts_ProjectFilter(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const pathA = "/project/alpha"
	const pathB = "/project/beta"

	fixture := []struct {
		id     string
		path   string
		status string
	}{
		{"tc-pf-a1", pathA, "completed"},
		{"tc-pf-a2", pathA, "completed"},
		{"tc-pf-a3", pathA, "failed"},
		{"tc-pf-b1", pathB, "completed"},
		{"tc-pf-b2", pathB, "no_op"},
	}
	for _, f := range fixture {
		if err := store.SaveExecution(&Execution{
			ID:          f.id,
			TaskID:      "TASK-" + f.id,
			ProjectPath: f.path,
			Status:      f.status,
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", f.id, err)
		}
	}

	tcA, err := store.GetLifetimeTaskCounts(pathA)
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts(pathA): %v", err)
	}
	if tcA.Total != 3 {
		t.Errorf("pathA Total = %d, want 3", tcA.Total)
	}
	if tcA.Succeeded != 2 {
		t.Errorf("pathA Succeeded = %d, want 2", tcA.Succeeded)
	}
	if tcA.Failed != 1 {
		t.Errorf("pathA Failed = %d, want 1", tcA.Failed)
	}

	tcB, err := store.GetLifetimeTaskCounts(pathB)
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts(pathB): %v", err)
	}
	if tcB.Total != 2 {
		t.Errorf("pathB Total = %d, want 2", tcB.Total)
	}
	if tcB.Succeeded != 1 {
		t.Errorf("pathB Succeeded = %d, want 1", tcB.Succeeded)
	}
	if tcB.NoOp != 1 {
		t.Errorf("pathB NoOp = %d, want 1", tcB.NoOp)
	}
	if tcB.Failed != 0 {
		t.Errorf("pathB Failed = %d, want 0", tcB.Failed)
	}

	tcAll, err := store.GetLifetimeTaskCounts("")
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts(all): %v", err)
	}
	if tcAll.Total != 5 {
		t.Errorf("unfiltered Total = %d, want 5", tcAll.Total)
	}
}

// TestFindOrphanedRunningExecutions_ExcludesInFlightTaskIDs verifies the
// task_id NOT IN(...) exclusion — a status='running' row whose task_id is in
// the caller-supplied live set (e.g. executor.Monitor's running/queued IDs)
// must never be returned as an orphan-sweep candidate. TASK-399/GH-4209.
func TestFindOrphanedRunningExecutions_ExcludesInFlightTaskIDs(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "run-live", TaskID: "GH-4206", ProjectPath: "/proj", Status: "running"})
	_ = store.SaveExecution(&Execution{ID: "run-orphan", TaskID: "GH-4189", ProjectPath: "/proj", Status: "running"})
	_ = store.SaveExecution(&Execution{ID: "run-done", TaskID: "GH-1", ProjectPath: "/proj", Status: "completed"})

	results, err := store.FindOrphanedRunningExecutions([]string{"GH-4206"})
	if err != nil {
		t.Fatalf("FindOrphanedRunningExecutions: %v", err)
	}

	gotIDs := make(map[string]bool, len(results))
	for _, r := range results {
		gotIDs[r.ID] = true
	}
	if gotIDs["run-live"] {
		t.Error("run-live: excluded task_id must not be returned as a candidate")
	}
	if !gotIDs["run-orphan"] {
		t.Error("run-orphan: non-excluded running row must be returned as a candidate")
	}
	if gotIDs["run-done"] {
		t.Error("run-done: non-running row must never be returned")
	}
}

// TestFindOrphanedRunningExecutions_NoExclusions verifies an empty exclude set
// returns every status='running' row (e.g. a cold-start sweep where the live
// Monitor is empty). TASK-399/GH-4209.
func TestFindOrphanedRunningExecutions_NoExclusions(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "run-1", TaskID: "GH-1", ProjectPath: "/proj", Status: "running"})
	_ = store.SaveExecution(&Execution{ID: "run-2", TaskID: "GH-2", ProjectPath: "/proj", Status: "running"})

	results, err := store.FindOrphanedRunningExecutions(nil)
	if err != nil {
		t.Fatalf("FindOrphanedRunningExecutions: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(results))
	}
}

// TestResolveOrphanedRunningExecution_MergedHealsToCompleted verifies that
// passing a non-empty prURL (merge evidence found) flips the row to
// 'completed' and stamps the URL. TASK-399/GH-4209.
func TestResolveOrphanedRunningExecution_MergedHealsToCompleted(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "orphan-merged", TaskID: "GH-4189", ProjectPath: "/proj", Status: "running"})

	if err := store.ResolveOrphanedRunningExecution("orphan-merged", "https://github.com/org/repo/pull/42"); err != nil {
		t.Fatalf("ResolveOrphanedRunningExecution: %v", err)
	}

	exec, err := store.GetExecution("orphan-merged")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", exec.Status)
	}
	if exec.PRUrl != "https://github.com/org/repo/pull/42" {
		t.Errorf("expected pr_url stamped, got %q", exec.PRUrl)
	}
	if exec.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

// TestResolveOrphanedRunningExecution_NoEvidenceHealsToFailed verifies that an
// empty prURL (no merge evidence) flips the row to 'failed' — which keeps it
// eligible for the ordinary SelfHealExecutionAfterMerge path (already
// includes 'failed' in its IN(...) set) if a merge surfaces later.
// TASK-399/GH-4209.
func TestResolveOrphanedRunningExecution_NoEvidenceHealsToFailed(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "orphan-no-evidence", TaskID: "GH-99", ProjectPath: "/proj", Status: "running"})

	if err := store.ResolveOrphanedRunningExecution("orphan-no-evidence", ""); err != nil {
		t.Fatalf("ResolveOrphanedRunningExecution: %v", err)
	}

	exec, err := store.GetExecution("orphan-no-evidence")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected status 'failed', got %q", exec.Status)
	}
	if exec.Error == "" {
		t.Error("expected a descriptive error message to be stamped")
	}
}

// TestResolveOrphanedRunningExecution_IdempotentOnAlreadyTerminalRow verifies
// the `AND status = 'running'` guard: re-running the resolve against a row
// that has since transitioned through the normal completion path must be a
// no-op, not a clobber. TASK-399/GH-4209.
func TestResolveOrphanedRunningExecution_IdempotentOnAlreadyTerminalRow(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "already-done", TaskID: "GH-7", ProjectPath: "/proj", Status: "completed", PRUrl: "https://github.com/org/repo/pull/7"})

	// Simulate a second sweep tick racing against the row's own normal
	// completion — must not overwrite the real outcome.
	if err := store.ResolveOrphanedRunningExecution("already-done", ""); err != nil {
		t.Fatalf("ResolveOrphanedRunningExecution: %v", err)
	}

	exec, err := store.GetExecution("already-done")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "completed" {
		t.Errorf("expected status to remain 'completed', got %q", exec.Status)
	}
	if exec.PRUrl != "https://github.com/org/repo/pull/7" {
		t.Errorf("expected pr_url to remain stamped, got %q", exec.PRUrl)
	}
}

// TestUpdateExecutionStatusIfNotTerminal_AppliesFromNonTerminal verifies the
// CAS guard's normal case: a write against a still-running row applies
// exactly like plain UpdateExecutionStatus would. GH-4423.
func TestUpdateExecutionStatusIfNotTerminal_AppliesFromNonTerminal(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "cas-running", TaskID: "GH-4423-A", ProjectPath: "/proj", Status: "running"})

	applied, err := store.UpdateExecutionStatusIfNotTerminal("cas-running", "failed", "orphaned worker")
	if err != nil {
		t.Fatalf("UpdateExecutionStatusIfNotTerminal: %v", err)
	}
	if !applied {
		t.Error("expected applied=true for a write against a non-terminal row")
	}

	exec, err := store.GetExecution("cas-running")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected status 'failed', got %q", exec.Status)
	}
	if exec.CompletedAt == nil {
		t.Error("expected completed_at to be set for the terminal write")
	}
}

// TestUpdateExecutionStatusIfNotTerminal_RejectsWhenAlreadyTerminal is the
// GH-4423 regression test for the TOCTOU class this issue targets: a stale
// reaper's blind failure-write must not clobber a row that already reached a
// terminal status (e.g. completed) between the reaper's evidence-gathering
// and its final write.
func TestUpdateExecutionStatusIfNotTerminal_RejectsWhenAlreadyTerminal(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{
		ID: "cas-completed", TaskID: "GH-4423-B", ProjectPath: "/proj",
		Status: "completed", PRUrl: "https://github.com/org/repo/pull/99",
	})

	applied, err := store.UpdateExecutionStatusIfNotTerminal("cas-completed", "failed", "stale running task recovered (orphaned worker)")
	if err != nil {
		t.Fatalf("UpdateExecutionStatusIfNotTerminal: %v", err)
	}
	if applied {
		t.Error("expected applied=false — row already terminal, write must be rejected")
	}

	exec, err := store.GetExecution("cas-completed")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "completed" {
		t.Errorf("expected status to remain 'completed', got %q", exec.Status)
	}
	if exec.PRUrl != "https://github.com/org/repo/pull/99" {
		t.Errorf("expected pr_url to remain stamped, got %q", exec.PRUrl)
	}
}

// TestMarkExecutionCompletedIfNotTerminal_AppliesFromNonTerminal verifies the
// CAS-guarded completion write applies normally against a running row. GH-4423.
func TestMarkExecutionCompletedIfNotTerminal_AppliesFromNonTerminal(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "cas-mec-running", TaskID: "GH-4423-C", ProjectPath: "/proj", Status: "running"})

	applied, err := store.MarkExecutionCompletedIfNotTerminal("cas-mec-running", "https://github.com/org/repo/pull/100", "abc123", 500)
	if err != nil {
		t.Fatalf("MarkExecutionCompletedIfNotTerminal: %v", err)
	}
	if !applied {
		t.Error("expected applied=true for a write against a non-terminal row")
	}

	exec, err := store.GetExecution("cas-mec-running")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "completed" || exec.PRUrl != "https://github.com/org/repo/pull/100" {
		t.Errorf("expected completed with pr_url stamped, got status=%q pr_url=%q", exec.Status, exec.PRUrl)
	}
}

// TestMarkExecutionCompletedIfNotTerminal_RejectsWhenAlreadyTerminal verifies
// a duplicate Finish call (ExecutionLifecycle.Persist's success branch) can't
// resurrect and overwrite a row that already reached a different terminal
// status (e.g. "failed" from a racing writer). GH-4423.
func TestMarkExecutionCompletedIfNotTerminal_RejectsWhenAlreadyTerminal(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "cas-mec-failed", TaskID: "GH-4423-D", ProjectPath: "/proj", Status: "failed", Error: "boom"})

	applied, err := store.MarkExecutionCompletedIfNotTerminal("cas-mec-failed", "https://github.com/org/repo/pull/101", "def456", 500)
	if err != nil {
		t.Fatalf("MarkExecutionCompletedIfNotTerminal: %v", err)
	}
	if applied {
		t.Error("expected applied=false — row already terminal, write must be rejected")
	}

	exec, err := store.GetExecution("cas-mec-failed")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected status to remain 'failed', got %q", exec.Status)
	}
	if exec.PRUrl != "" {
		t.Errorf("expected pr_url to remain empty, got %q", exec.PRUrl)
	}
}

// TestSelfHealExecutionByPRURL_HealsMatchingRow verifies the pr_url-keyed
// fallback heal used when a merged PR's issue number can't be resolved at
// all. TASK-399/GH-4209.
func TestSelfHealExecutionByPRURL_HealsMatchingRow(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	prURL := "https://github.com/org/repo/pull/55"
	_ = store.SaveExecution(&Execution{ID: "pr-url-heal", TaskID: "GH-55", ProjectPath: "/proj", Status: "failed", Error: "boom", PRUrl: prURL})
	_ = store.SaveExecution(&Execution{ID: "unrelated", TaskID: "GH-56", ProjectPath: "/proj", Status: "failed", PRUrl: "https://github.com/org/repo/pull/56"})

	if err := store.SelfHealExecutionByPRURL(prURL); err != nil {
		t.Fatalf("SelfHealExecutionByPRURL: %v", err)
	}

	healed, err := store.GetExecution("pr-url-heal")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if healed.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", healed.Status)
	}
	if healed.Error != "" {
		t.Errorf("expected error cleared, got %q", healed.Error)
	}

	unrelated, err := store.GetExecution("unrelated")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if unrelated.Status != "failed" {
		t.Errorf("unrelated row must be unaffected, got %q", unrelated.Status)
	}
}

// TestSelfHealExecutionByPRURL_EmptyURLNoOp verifies an empty prURL never
// touches any row (avoids matching rows with an empty pr_url column).
// TASK-399/GH-4209.
func TestSelfHealExecutionByPRURL_EmptyURLNoOp(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "no-pr-url", TaskID: "GH-1", ProjectPath: "/proj", Status: "failed"})

	if err := store.SelfHealExecutionByPRURL(""); err != nil {
		t.Fatalf("SelfHealExecutionByPRURL: %v", err)
	}

	exec, err := store.GetExecution("no-pr-url")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected status to remain 'failed', got %q", exec.Status)
	}
}

// TestSelfHealExecutionAfterMerge_BackfillsEmptyPRUrlOnCompletedRow pins
// GH-4511's fix to healAndBackfillRows: a row already sitting at
// status='completed' (the GH-4277/GH-4292 backfill-only candidate — missing
// only its terminal execution_events entry) whose own pr_url column is still
// empty must have pr_url backfilled from the caller's known prURL. Before
// this fix, the completed-row branch only appended the missing ledger event
// and left pr_url untouched, so such a row stayed permanently invisible to
// GetLifetimePRCountersFromExecutions's non-empty-pr_url filter even though the
// live in-memory PRsMerged counter had already counted the merge —
// desyncing the lifetime baseline from the session counter across a
// restart.
func TestSelfHealExecutionAfterMerge_BackfillsEmptyPRUrlOnCompletedRow(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	prURL := "https://github.com/org/repo/pull/77"
	if err := store.SaveExecution(&Execution{
		ID: "backfill-pr-url", TaskID: "GH-77", ProjectPath: "/proj",
		Status: "completed", PRUrl: "",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	if err := store.SelfHealExecutionAfterMerge("GH-77", "/proj", prURL); err != nil {
		t.Fatalf("SelfHealExecutionAfterMerge: %v", err)
	}

	healed, err := store.GetExecution("backfill-pr-url")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if healed.Status != "completed" {
		t.Errorf("status = %q, want unchanged 'completed'", healed.Status)
	}
	if healed.PRUrl != prURL {
		t.Errorf("PRUrl = %q, want backfilled %q", healed.PRUrl, prURL)
	}

	counters, err := store.GetLifetimePRCountersFromExecutions("")
	if err != nil {
		t.Fatalf("GetLifetimePRCountersFromExecutions: %v", err)
	}
	if counters.Merged != 1 {
		t.Errorf("Merged = %d, want 1 — backfilled pr_url must make the row visible to the lifetime query", counters.Merged)
	}
}

// TestSelfHealExecutionAfterMerge_ExcludesRunningQueuedPending is the
// GH-4209 regression test for the store.go:1446 IN(...) exclusion this task
// must preserve unmodified: running/queued/pending rows are never healed by
// SelfHealExecutionAfterMerge, only the non-success terminal set.
func TestSelfHealExecutionAfterMerge_ExcludesRunningQueuedPending(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	statuses := []string{"running", "queued", "pending"}
	for i, status := range statuses {
		id := fmt.Sprintf("in-flight-%d", i)
		_ = store.SaveExecution(&Execution{ID: id, TaskID: "GH-8", ProjectPath: "/proj", Status: status})
	}

	if err := store.SelfHealExecutionAfterMerge("GH-8", "/proj", "https://github.com/org/repo/pull/8"); err != nil {
		t.Fatalf("SelfHealExecutionAfterMerge: %v", err)
	}

	for i, status := range statuses {
		id := fmt.Sprintf("in-flight-%d", i)
		exec, err := store.GetExecution(id)
		if err != nil {
			t.Fatalf("GetExecution(%s): %v", id, err)
		}
		if exec.Status != status {
			t.Errorf("%s: expected status to remain %q, got %q", id, status, exec.Status)
		}
	}
}

// TestUpdateExecutionTitle covers the GH-4280 backfill/no-op/overwrite matrix.
func TestUpdateExecutionTitle(t *testing.T) {
	tests := []struct {
		name          string
		initialTitle  string
		updateTitle   string
		expectedTitle string
	}{
		{
			name:          "empty to resolved backfill",
			initialTitle:  "",
			updateTitle:   "Fix the flaky test",
			expectedTitle: "Fix the flaky test",
		},
		{
			name:          "empty to empty no-op",
			initialTitle:  "",
			updateTitle:   "",
			expectedTitle: "",
		},
		{
			name:          "resolved to resolved overwrite",
			initialTitle:  "Old title",
			updateTitle:   "New title",
			expectedTitle: "New title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			id := "exec-title-1"
			if err := store.SaveExecution(&Execution{
				ID:          id,
				TaskID:      "GH-4280",
				ProjectPath: "/proj",
				Status:      "running",
				TaskTitle:   tt.initialTitle,
			}); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			if err := store.UpdateExecutionTitle(id, tt.updateTitle); err != nil {
				t.Fatalf("UpdateExecutionTitle: %v", err)
			}

			exec, err := store.GetExecution(id)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if exec.TaskTitle != tt.expectedTitle {
				t.Errorf("expected task_title %q, got %q", tt.expectedTitle, exec.TaskTitle)
			}
		})
	}
}

// TestHasTerminalCompletion_CountsCanceledRow is the GH-4678 regression test
// for HasTerminalCompletion's new "canceled" branch: an operator-cancelled
// execution must count as done — the same "never re-dispatch" signal a
// genuine completion or a no_op already provides — so the SDK poller's
// pre-dispatch gate and the dispatcher's own hasTerminalSuccessLedger guard
// both suppress re-dispatch for it (AC3). Deliberately does NOT require an
// empty error column, unlike the no_op branch: Cancel always records the
// operator's reason there.
func TestHasTerminalCompletion_CountsCanceledRow(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	taskID, projectPath := "GH-4678-HTC", "/project-htc"

	done, err := store.HasTerminalCompletion(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasTerminalCompletion (before any row): %v", err)
	}
	if done {
		t.Fatal("expected done=false before any execution row exists")
	}

	if err := store.SaveExecution(&Execution{
		ID: "exec-canceled", TaskID: taskID, ProjectPath: projectPath,
		Status: "canceled", Error: "operator: duplicate ticket",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	done, err = store.HasTerminalCompletion(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasTerminalCompletion (after cancel): %v", err)
	}
	if !done {
		t.Error("expected done=true once a canceled row exists, even with a non-empty error/reason")
	}
}

// TestUpdateExecutionStatusIfNotTerminal_RejectsWhenAlreadyCanceled is the
// GH-4678 CAS-guard regression test: once Cancel writes status='canceled',
// the CAS guard (terminalExecutionStatuses) must reject any later write
// that would silently resurrect the row to a non-terminal status — the same
// protection every other terminal status already gets (GH-4423).
func TestUpdateExecutionStatusIfNotTerminal_RejectsWhenAlreadyCanceled(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "cas-canceled", TaskID: "GH-4678-CAS", ProjectPath: "/proj", Status: "canceled", Error: "operator cancel"})

	applied, err := store.UpdateExecutionStatusIfNotTerminal("cas-canceled", "queued")
	if err != nil {
		t.Fatalf("UpdateExecutionStatusIfNotTerminal: %v", err)
	}
	if applied {
		t.Error("expected applied=false — a canceled row must never be resurrected to a non-terminal status")
	}

	exec, err := store.GetExecution("cas-canceled")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "canceled" {
		t.Errorf("expected status to remain 'canceled', got %q", exec.Status)
	}
}
