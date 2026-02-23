package upgrade

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// mockTaskChecker is a TaskChecker that returns configurable running tasks
type mockTaskChecker struct {
	tasks   []string
	waitErr error
}

func (m *mockTaskChecker) GetRunningTaskIDs() []string {
	return m.tasks
}

func (m *mockTaskChecker) WaitForTasks(ctx context.Context, timeout time.Duration) error {
	if m.waitErr != nil {
		return m.waitErr
	}
	return nil
}

// newTestGracefulUpgrader creates a GracefulUpgrader with a temp binary, bypassing NewUpgrader
func newTestGracefulUpgrader(t *testing.T, tc TaskChecker) (*GracefulUpgrader, string) {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	if err := os.WriteFile(binPath, []byte("test-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(dir, "upgrade-state.json")

	u := &Upgrader{
		currentVersion: "1.0.0",
		httpClient:     &http.Client{Timeout: 5 * time.Second},
		binaryPath:     binPath,
		backupPath:     binPath + BackupSuffix,
	}

	return &GracefulUpgrader{
		upgrader:    u,
		statePath:   statePath,
		taskChecker: tc,
	}, dir
}

func TestGracefulUpgrader_PerformUpgrade_NoTasks(t *testing.T) {
	tc := &mockTaskChecker{}
	g, dir := newTestGracefulUpgrader(t, tc)

	newBinary := []byte("new-binary-v2")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newBinary)
	}))
	defer server.Close()

	g.upgrader.httpClient = server.Client()

	release := &Release{
		TagName: "v2.0.0",
		Assets: []Asset{
			{
				Name:               fmt.Sprintf("pilot-%s-%s", runtime.GOOS, runtime.GOARCH),
				BrowserDownloadURL: server.URL + "/pilot",
				Size:               int64(len(newBinary)),
			},
		},
	}

	var progressMsgs []string
	opts := &UpgradeOptions{
		WaitForTasks: false,
		Force:        true,
		OnProgress: func(pct int, msg string) {
			progressMsgs = append(progressMsgs, msg)
		},
	}

	err := g.PerformUpgrade(context.Background(), release, opts)
	if err != nil {
		t.Fatalf("PerformUpgrade() error = %v", err)
	}

	// Verify binary was installed
	got, _ := os.ReadFile(filepath.Join(dir, "pilot"))
	if string(got) != string(newBinary) {
		t.Errorf("binary content = %q, want %q", got, newBinary)
	}

	// Verify state was saved as completed
	state, err := LoadState(filepath.Join(dir, "upgrade-state.json"))
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	if state == nil {
		t.Fatal("state should exist after upgrade")
	}
	if state.Status != StatusCompleted {
		t.Errorf("state.Status = %q, want %q", state.Status, StatusCompleted)
	}
}

func TestGracefulUpgrader_PerformUpgrade_DefaultOpts(t *testing.T) {
	tc := &mockTaskChecker{}
	g, _ := newTestGracefulUpgrader(t, tc)

	newBinary := []byte("new-binary")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newBinary)
	}))
	defer server.Close()

	g.upgrader.httpClient = server.Client()

	release := &Release{
		TagName: "v2.0.0",
		Assets: []Asset{
			{
				Name:               fmt.Sprintf("pilot-%s-%s", runtime.GOOS, runtime.GOARCH),
				BrowserDownloadURL: server.URL + "/pilot",
				Size:               int64(len(newBinary)),
			},
		},
	}

	// nil opts should use defaults
	err := g.PerformUpgrade(context.Background(), release, nil)
	if err != nil {
		t.Fatalf("PerformUpgrade(nil opts) error = %v", err)
	}
}

func TestGracefulUpgrader_PerformUpgrade_WaitsForTasks(t *testing.T) {
	tc := &mockTaskChecker{tasks: []string{"task-1", "task-2"}}
	g, _ := newTestGracefulUpgrader(t, tc)

	newBinary := []byte("new-binary")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newBinary)
	}))
	defer server.Close()

	g.upgrader.httpClient = server.Client()

	release := &Release{
		TagName: "v2.0.0",
		Assets: []Asset{
			{
				Name:               fmt.Sprintf("pilot-%s-%s", runtime.GOOS, runtime.GOARCH),
				BrowserDownloadURL: server.URL + "/pilot",
				Size:               int64(len(newBinary)),
			},
		},
	}

	opts := &UpgradeOptions{
		WaitForTasks: true,
		TaskTimeout:  5 * time.Second,
	}

	err := g.PerformUpgrade(context.Background(), release, opts)
	if err != nil {
		t.Fatalf("PerformUpgrade() error = %v", err)
	}
}

func TestGracefulUpgrader_PerformUpgrade_TaskWaitTimeout(t *testing.T) {
	tc := &mockTaskChecker{
		tasks:   []string{"task-1"},
		waitErr: context.DeadlineExceeded,
	}
	g, dir := newTestGracefulUpgrader(t, tc)

	release := &Release{TagName: "v2.0.0"}

	opts := &UpgradeOptions{
		WaitForTasks: true,
		TaskTimeout:  1 * time.Second,
	}

	err := g.PerformUpgrade(context.Background(), release, opts)
	if err == nil {
		t.Fatal("PerformUpgrade() expected timeout error, got nil")
	}

	// Verify state was saved as failed
	state, _ := LoadState(filepath.Join(dir, "upgrade-state.json"))
	if state != nil && state.Status != StatusFailed {
		t.Errorf("state.Status = %q, want %q", state.Status, StatusFailed)
	}
}

func TestGracefulUpgrader_CheckAndRollback_NoState(t *testing.T) {
	tc := &NoOpTaskChecker{}
	g, _ := newTestGracefulUpgrader(t, tc)

	rolledBack, err := g.CheckAndRollback()
	if err != nil {
		t.Fatalf("CheckAndRollback() error = %v", err)
	}
	if rolledBack {
		t.Error("CheckAndRollback() = true, want false when no state exists")
	}
}

func TestGracefulUpgrader_CheckAndRollback_FailedState(t *testing.T) {
	tc := &NoOpTaskChecker{}
	g, dir := newTestGracefulUpgrader(t, tc)
	binPath := filepath.Join(dir, "pilot")

	// Create backup
	if err := g.upgrader.createBackup(); err != nil {
		t.Fatal(err)
	}

	// Corrupt binary
	if err := os.WriteFile(binPath, []byte("corrupted"), 0755); err != nil {
		t.Fatal(err)
	}

	// Save failed state
	state := &State{
		Status:     StatusFailed,
		BackupPath: g.upgrader.backupPath,
	}
	statePath := filepath.Join(dir, "upgrade-state.json")
	if err := state.Save(statePath); err != nil {
		t.Fatal(err)
	}

	rolledBack, err := g.CheckAndRollback()
	if err != nil {
		t.Fatalf("CheckAndRollback() error = %v", err)
	}
	if !rolledBack {
		t.Error("CheckAndRollback() = false, want true for failed state")
	}

	// Verify original content restored
	got, _ := os.ReadFile(binPath)
	if string(got) != "test-binary" {
		t.Errorf("binary content = %q, want %q", got, "test-binary")
	}
}

func TestGracefulUpgrader_CheckAndRollback_CompletedState(t *testing.T) {
	tc := &NoOpTaskChecker{}
	g, dir := newTestGracefulUpgrader(t, tc)

	// Save completed state (no rollback needed)
	state := &State{Status: StatusCompleted}
	statePath := filepath.Join(dir, "upgrade-state.json")
	if err := state.Save(statePath); err != nil {
		t.Fatal(err)
	}

	rolledBack, err := g.CheckAndRollback()
	if err != nil {
		t.Fatalf("CheckAndRollback() error = %v", err)
	}
	if rolledBack {
		t.Error("CheckAndRollback() = true, want false for completed state")
	}
}

func TestGracefulUpgrader_CleanupState_CompletedUpgrade(t *testing.T) {
	tc := &NoOpTaskChecker{}
	g, dir := newTestGracefulUpgrader(t, tc)

	// Create backup file
	backupPath := g.upgrader.backupPath
	if err := os.WriteFile(backupPath, []byte("backup"), 0755); err != nil {
		t.Fatal(err)
	}

	// Save completed state
	statePath := filepath.Join(dir, "upgrade-state.json")
	state := &State{Status: StatusCompleted}
	if err := state.Save(statePath); err != nil {
		t.Fatal(err)
	}

	err := g.CleanupState()
	if err != nil {
		t.Fatalf("CleanupState() error = %v", err)
	}

	// Verify backup removed
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Error("backup should be removed after CleanupState")
	}

	// Verify state cleared
	loaded, _ := LoadState(statePath)
	if loaded != nil {
		t.Error("state should be nil after CleanupState")
	}
}

func TestGracefulUpgrader_CleanupState_NoState(t *testing.T) {
	tc := &NoOpTaskChecker{}
	g, _ := newTestGracefulUpgrader(t, tc)

	// Should be no-op
	if err := g.CleanupState(); err != nil {
		t.Fatalf("CleanupState() error = %v", err)
	}
}

func TestGracefulUpgrader_CleanupState_NotCompleted(t *testing.T) {
	tc := &NoOpTaskChecker{}
	g, dir := newTestGracefulUpgrader(t, tc)

	// Save failed state (should NOT cleanup)
	statePath := filepath.Join(dir, "upgrade-state.json")
	state := &State{Status: StatusFailed}
	if err := state.Save(statePath); err != nil {
		t.Fatal(err)
	}

	if err := g.CleanupState(); err != nil {
		t.Fatalf("CleanupState() error = %v", err)
	}

	// State should still exist
	loaded, _ := LoadState(statePath)
	if loaded == nil {
		t.Error("state should still exist for non-completed upgrade")
	}
}

func TestGracefulUpgrader_GetUpgrader(t *testing.T) {
	tc := &NoOpTaskChecker{}
	g, _ := newTestGracefulUpgrader(t, tc)

	u := g.GetUpgrader()
	if u == nil {
		t.Fatal("GetUpgrader() returned nil")
	}
	if u.currentVersion != "1.0.0" {
		t.Errorf("version = %q, want %q", u.currentVersion, "1.0.0")
	}
}
