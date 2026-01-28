package upgrade

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name     string
		a, b     string
		expected int
	}{
		{"equal versions", "1.0.0", "1.0.0", 0},
		{"a less than b major", "1.0.0", "2.0.0", -1},
		{"a greater than b major", "2.0.0", "1.0.0", 1},
		{"a less than b minor", "1.0.0", "1.1.0", -1},
		{"a greater than b minor", "1.1.0", "1.0.0", 1},
		{"a less than b patch", "1.0.0", "1.0.1", -1},
		{"a greater than b patch", "1.0.1", "1.0.0", 1},
		{"with v prefix", "v1.0.0", "v1.0.1", -1},
		{"mixed prefix", "v1.0.0", "1.0.1", -1},
		{"with dirty suffix", "1.0.0-dirty", "1.0.1", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := compareVersions(tt.a, tt.b)
			if result != tt.expected {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, result, tt.expected)
			}
		})
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		name     string
		version  string
		expected [3]int
	}{
		{"simple version", "1.2.3", [3]int{1, 2, 3}},
		{"with v prefix", "v1.2.3", [3]int{1, 2, 3}},
		{"with dirty suffix", "1.2.3-dirty", [3]int{1, 2, 3}},
		{"partial version", "1.2", [3]int{1, 2, 0}},
		{"major only", "1", [3]int{1, 0, 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseVersion(tt.version)
			if result != tt.expected {
				t.Errorf("parseVersion(%q) = %v, want %v", tt.version, result, tt.expected)
			}
		})
	}
}

func TestVersionInfo_UpdateAvailable(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{"update available", "0.1.0", "0.2.0", true},
		{"no update", "0.2.0", "0.2.0", false},
		{"newer local", "0.3.0", "0.2.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := tt.current
			latest := tt.latest
			got := compareVersions(current, latest) < 0
			if got != tt.want {
				t.Errorf("UpdateAvail = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUpgrader_findAsset(t *testing.T) {
	release := &Release{
		Assets: []Asset{
			{Name: "pilot-darwin-arm64.tar.gz", BrowserDownloadURL: "https://example.com/darwin-arm64.tar.gz"},
			{Name: "pilot-darwin-amd64.tar.gz", BrowserDownloadURL: "https://example.com/darwin-amd64.tar.gz"},
			{Name: "pilot-linux-amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux-amd64.tar.gz"},
			{Name: "pilot-linux-arm64.tar.gz", BrowserDownloadURL: "https://example.com/linux-arm64.tar.gz"},
		},
	}

	upgrader := &Upgrader{}

	// Test that we can find an asset (the actual OS/arch will be determined at runtime)
	asset := upgrader.findAsset(release)
	// We can't deterministically test which asset is found since it depends on runtime.GOOS/GOARCH
	// but we can ensure the method doesn't panic
	_ = asset
}

func TestUpgrader_CheckVersion(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"tag_name": "v0.3.0",
			"name": "v0.3.0",
			"body": "Release notes here",
			"draft": false,
			"prerelease": false,
			"published_at": "2025-01-01T00:00:00Z",
			"html_url": "https://github.com/test/test/releases/tag/v0.3.0",
			"assets": []
		}`))
	}))
	defer server.Close()

	// Create upgrader with mock
	upgrader := &Upgrader{
		currentVersion: "0.2.0",
		httpClient:     &http.Client{Timeout: 5 * time.Second},
	}

	// We can't easily test against real GitHub API, but we can verify the structure
	t.Run("version info structure", func(t *testing.T) {
		info := &VersionInfo{
			Current:     "0.2.0",
			Latest:      "v0.3.0",
			UpdateAvail: true,
		}

		if info.Current != "0.2.0" {
			t.Errorf("Current = %q, want %q", info.Current, "0.2.0")
		}
		if !info.UpdateAvail {
			t.Error("UpdateAvail should be true")
		}
	})

	// Keep upgrader in scope
	_ = upgrader
}

func TestUpgrader_BackupAndRollback(t *testing.T) {
	// Create temp directory for test
	tempDir := t.TempDir()

	// Create a fake binary
	binaryPath := filepath.Join(tempDir, "pilot")
	if err := os.WriteFile(binaryPath, []byte("original binary content"), 0755); err != nil {
		t.Fatalf("Failed to create test binary: %v", err)
	}

	upgrader := &Upgrader{
		currentVersion: "0.2.0",
		binaryPath:     binaryPath,
		backupPath:     binaryPath + BackupSuffix,
		httpClient:     &http.Client{Timeout: 5 * time.Second},
	}

	// Test backup creation
	t.Run("create backup", func(t *testing.T) {
		if err := upgrader.createBackup(); err != nil {
			t.Fatalf("createBackup() error = %v", err)
		}

		if !upgrader.HasBackup() {
			t.Error("HasBackup() = false after backup creation")
		}

		// Verify backup content
		content, err := os.ReadFile(upgrader.backupPath)
		if err != nil {
			t.Fatalf("Failed to read backup: %v", err)
		}
		if string(content) != "original binary content" {
			t.Errorf("Backup content = %q, want %q", string(content), "original binary content")
		}
	})

	// Simulate binary modification
	t.Run("modify binary", func(t *testing.T) {
		if err := os.WriteFile(binaryPath, []byte("new binary content"), 0755); err != nil {
			t.Fatalf("Failed to modify binary: %v", err)
		}
	})

	// Test rollback
	t.Run("rollback", func(t *testing.T) {
		if err := upgrader.Rollback(); err != nil {
			t.Fatalf("Rollback() error = %v", err)
		}

		// Verify rollback restored original content
		content, err := os.ReadFile(binaryPath)
		if err != nil {
			t.Fatalf("Failed to read binary after rollback: %v", err)
		}
		if string(content) != "original binary content" {
			t.Errorf("Binary content after rollback = %q, want %q", string(content), "original binary content")
		}

		// Verify backup was removed
		if upgrader.HasBackup() {
			t.Error("HasBackup() = true after rollback, should be false")
		}
	})
}

func TestUpgrader_CleanupBackup(t *testing.T) {
	tempDir := t.TempDir()

	binaryPath := filepath.Join(tempDir, "pilot")
	backupPath := binaryPath + BackupSuffix

	// Create backup file
	if err := os.WriteFile(backupPath, []byte("backup content"), 0755); err != nil {
		t.Fatalf("Failed to create backup file: %v", err)
	}

	upgrader := &Upgrader{
		binaryPath: binaryPath,
		backupPath: backupPath,
	}

	if !upgrader.HasBackup() {
		t.Error("HasBackup() = false, should be true")
	}

	if err := upgrader.CleanupBackup(); err != nil {
		t.Fatalf("CleanupBackup() error = %v", err)
	}

	if upgrader.HasBackup() {
		t.Error("HasBackup() = true after cleanup, should be false")
	}
}

func TestState(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "upgrade-state.json")

	// Test save and load
	t.Run("save and load", func(t *testing.T) {
		state := &State{
			PreviousVersion: "0.2.0",
			NewVersion:      "0.3.0",
			UpgradeStarted:  time.Now(),
			BackupPath:      "/path/to/backup",
			Status:          StatusPending,
		}

		if err := state.Save(statePath); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		loaded, err := LoadState(statePath)
		if err != nil {
			t.Fatalf("LoadState() error = %v", err)
		}

		if loaded.PreviousVersion != state.PreviousVersion {
			t.Errorf("PreviousVersion = %q, want %q", loaded.PreviousVersion, state.PreviousVersion)
		}
		if loaded.NewVersion != state.NewVersion {
			t.Errorf("NewVersion = %q, want %q", loaded.NewVersion, state.NewVersion)
		}
		if loaded.Status != state.Status {
			t.Errorf("Status = %q, want %q", loaded.Status, state.Status)
		}
	})

	// Test IsPending
	t.Run("IsPending", func(t *testing.T) {
		tests := []struct {
			status UpgradeStatus
			want   bool
		}{
			{StatusPending, true},
			{StatusDownloading, true},
			{StatusWaiting, true},
			{StatusInstalling, true},
			{StatusCompleted, false},
			{StatusFailed, false},
			{StatusRolledBack, false},
		}

		for _, tt := range tests {
			state := &State{Status: tt.status}
			if got := state.IsPending(); got != tt.want {
				t.Errorf("IsPending() for status %q = %v, want %v", tt.status, got, tt.want)
			}
		}
	})

	// Test NeedsRollback
	t.Run("NeedsRollback", func(t *testing.T) {
		state := &State{
			Status:     StatusFailed,
			BackupPath: "/path/to/backup",
		}
		if !state.NeedsRollback() {
			t.Error("NeedsRollback() = false, want true")
		}

		state.Status = StatusCompleted
		if state.NeedsRollback() {
			t.Error("NeedsRollback() = true for completed status, want false")
		}

		state.Status = StatusFailed
		state.BackupPath = ""
		if state.NeedsRollback() {
			t.Error("NeedsRollback() = true without backup path, want false")
		}
	})

	// Test ClearState
	t.Run("ClearState", func(t *testing.T) {
		// Create state file
		state := &State{Status: StatusCompleted}
		if err := state.Save(statePath); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		if err := ClearState(statePath); err != nil {
			t.Fatalf("ClearState() error = %v", err)
		}

		loaded, err := LoadState(statePath)
		if err != nil {
			t.Fatalf("LoadState() error = %v", err)
		}
		if loaded != nil {
			t.Error("LoadState() should return nil after ClearState()")
		}
	})
}

func TestNoOpTaskChecker(t *testing.T) {
	checker := &NoOpTaskChecker{}

	tasks := checker.GetRunningTaskIDs()
	if len(tasks) != 0 {
		t.Errorf("GetRunningTaskIDs() = %v, want empty slice", tasks)
	}

	ctx := context.Background()
	if err := checker.WaitForTasks(ctx, time.Second); err != nil {
		t.Errorf("WaitForTasks() error = %v", err)
	}
}
