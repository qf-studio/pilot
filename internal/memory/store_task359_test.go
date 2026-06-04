package memory

import (
	"os"
	"testing"
)

// TestMarkExecutionCompleted verifies the TASK-359 Layer 1 atomic completion
// write: status, pr_url, commit_sha, and duration_ms are all set in a single
// UPDATE, and the row is then accepted by HasCompletedExecution.
func TestMarkExecutionCompleted(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Seed a running execution (no deliverable yet).
	if err := store.SaveExecution(&Execution{
		ID:          "exec-1",
		TaskID:      "GH-100",
		ProjectPath: "/project",
		Status:      "running",
	}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	const (
		prURL = "https://github.com/o/r/pull/7"
		sha   = "deadbeef"
		dur   = int64(4321)
	)
	if err := store.MarkExecutionCompleted("exec-1", prURL, sha, dur); err != nil {
		t.Fatalf("MarkExecutionCompleted failed: %v", err)
	}

	// All four columns must be set in the single write.
	var (
		gotStatus, gotPR, gotSHA string
		gotDur                   int64
	)
	row := store.db.QueryRow(
		`SELECT status, pr_url, commit_sha, duration_ms FROM executions WHERE id = ?`, "exec-1")
	if err := row.Scan(&gotStatus, &gotPR, &gotSHA, &gotDur); err != nil {
		t.Fatalf("scan row: %v", err)
	}
	if gotStatus != "completed" {
		t.Errorf("status = %q, want completed", gotStatus)
	}
	if gotPR != prURL {
		t.Errorf("pr_url = %q, want %q", gotPR, prURL)
	}
	if gotSHA != sha {
		t.Errorf("commit_sha = %q, want %q", gotSHA, sha)
	}
	if gotDur != dur {
		t.Errorf("duration_ms = %d, want %d", gotDur, dur)
	}

	// And it must be recognized as a genuine completion.
	completed, err := store.HasCompletedExecution("GH-100", "/project")
	if err != nil {
		t.Fatalf("HasCompletedExecution failed: %v", err)
	}
	if !completed {
		t.Error("expected HasCompletedExecution=true after MarkExecutionCompleted")
	}
}

// TestMarkExecutionCompleted_EmptyPRUrl documents that the atomic write itself
// does not enforce the PR-URL invariant — that guard lives in the executor's
// finalize path (TASK-359 Layer 1). A direct-commit completion (commit_sha set,
// pr_url empty) is still a valid completed row.
func TestMarkExecutionCompleted_EmptyPRUrl(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{
		ID:          "exec-direct",
		TaskID:      "GH-101",
		ProjectPath: "/project",
		Status:      "running",
	})
	if err := store.MarkExecutionCompleted("exec-direct", "", "abc123", 10); err != nil {
		t.Fatalf("MarkExecutionCompleted failed: %v", err)
	}

	completed, _ := store.HasCompletedExecution("GH-101", "/project")
	if !completed {
		t.Error("direct-commit completion (commit_sha set, no pr_url) should still count as completed")
	}
}
