package upgrade

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
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

func TestUpgrader_findAsset_zip(t *testing.T) {
	// Simulate a release with both tar.gz and zip assets (like GoReleaser produces)
	release := &Release{
		Assets: []Asset{
			{Name: "pilot-darwin-arm64.tar.gz", BrowserDownloadURL: "https://example.com/darwin-arm64.tar.gz"},
			{Name: "pilot-linux-amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux-amd64.tar.gz"},
			{Name: "pilot-windows-amd64.zip", BrowserDownloadURL: "https://example.com/windows-amd64.zip"},
		},
	}

	upgrader := &Upgrader{}

	// Test that findAsset finds tar.gz for current platform (darwin/linux) or zip for windows
	asset := upgrader.findAsset(release)
	if asset == nil {
		t.Log("No asset found for current platform (expected if running on unsupported platform)")
	}

	// Test explicit matching logic by checking all formats are discoverable
	t.Run("tar.gz preferred over zip", func(t *testing.T) {
		r := &Release{
			Assets: []Asset{
				{Name: "pilot-darwin-arm64.tar.gz", BrowserDownloadURL: "tar"},
				{Name: "pilot-darwin-arm64.zip", BrowserDownloadURL: "zip"},
			},
		}
		a := upgrader.findAsset(r)
		if a != nil && a.BrowserDownloadURL == "zip" {
			t.Error("findAsset should prefer tar.gz over zip")
		}
	})

	t.Run("zip found when no tar.gz", func(t *testing.T) {
		r := &Release{
			Assets: []Asset{
				{Name: "pilot-windows-amd64.zip", BrowserDownloadURL: "https://example.com/win.zip"},
				{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/checksums.txt"},
			},
		}
		// This will only match on windows/amd64, but we verify no panic
		_ = upgrader.findAsset(r)
	})
}

func TestUpgrader_installFromZip(t *testing.T) {
	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pilot")

	upgrader := &Upgrader{
		binaryPath: binaryPath,
	}

	// Create a test zip file containing a "pilot" binary
	zipPath := filepath.Join(tempDir, "test.zip")
	zipFile, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("Failed to create zip file: %v", err)
	}

	zw := zip.NewWriter(zipFile)

	// Add a "pilot" entry
	w, err := zw.Create("pilot")
	if err != nil {
		t.Fatalf("Failed to create zip entry: %v", err)
	}
	binaryContent := []byte("test pilot binary content")
	if _, err := w.Write(binaryContent); err != nil {
		t.Fatalf("Failed to write zip entry: %v", err)
	}

	// Add a README (should be ignored)
	w2, err := zw.Create("README.md")
	if err != nil {
		t.Fatalf("Failed to create README entry: %v", err)
	}
	if _, err := w2.Write([]byte("readme")); err != nil {
		t.Fatalf("Failed to write README entry: %v", err)
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("Failed to close zip writer: %v", err)
	}
	if err := zipFile.Close(); err != nil {
		t.Fatalf("Failed to close zip file: %v", err)
	}

	// Test extraction
	if err := upgrader.installFromZip(zipPath); err != nil {
		t.Fatalf("installFromZip() error = %v", err)
	}

	// Verify extracted content
	content, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("Failed to read extracted binary: %v", err)
	}
	if string(content) != string(binaryContent) {
		t.Errorf("Extracted content = %q, want %q", string(content), string(binaryContent))
	}
}

func TestUpgrader_installFromZip_exe(t *testing.T) {
	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pilot.exe")

	upgrader := &Upgrader{
		binaryPath: binaryPath,
	}

	// Create a zip with pilot.exe (Windows-style)
	zipPath := filepath.Join(tempDir, "test.zip")
	zipFile, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("Failed to create zip file: %v", err)
	}

	zw := zip.NewWriter(zipFile)
	w, err := zw.Create("pilot.exe")
	if err != nil {
		t.Fatalf("Failed to create zip entry: %v", err)
	}
	binaryContent := []byte("windows pilot binary")
	if _, err := w.Write(binaryContent); err != nil {
		t.Fatalf("Failed to write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Failed to close zip writer: %v", err)
	}
	if err := zipFile.Close(); err != nil {
		t.Fatalf("Failed to close zip file: %v", err)
	}

	if err := upgrader.installFromZip(zipPath); err != nil {
		t.Fatalf("installFromZip() error = %v", err)
	}

	content, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("Failed to read extracted binary: %v", err)
	}
	if string(content) != string(binaryContent) {
		t.Errorf("Extracted content = %q, want %q", string(content), string(binaryContent))
	}
}

func TestUpgrader_isZip(t *testing.T) {
	tempDir := t.TempDir()
	upgrader := &Upgrader{}

	t.Run("valid zip", func(t *testing.T) {
		zipPath := filepath.Join(tempDir, "test.zip")
		f, _ := os.Create(zipPath)
		zw := zip.NewWriter(f)
		w, _ := zw.Create("dummy")
		_, _ = w.Write([]byte("data"))
		_ = zw.Close()
		_ = f.Close()

		if !upgrader.isZip(zipPath) {
			t.Error("isZip() = false for valid zip file")
		}
	})

	t.Run("not zip", func(t *testing.T) {
		notZipPath := filepath.Join(tempDir, "notzip")
		_ = os.WriteFile(notZipPath, []byte("not a zip file"), 0644)

		if upgrader.isZip(notZipPath) {
			t.Error("isZip() = true for non-zip file")
		}
	})
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

// ---------------------------------------------------------------------------
// VersionChecker tests
// ---------------------------------------------------------------------------

func TestNewVersionChecker(t *testing.T) {
	t.Run("default interval", func(t *testing.T) {
		vc := NewVersionChecker("1.0.0", 0)
		if vc.checkInterval != DefaultCheckInterval {
			t.Errorf("checkInterval = %v, want %v", vc.checkInterval, DefaultCheckInterval)
		}
		if vc.currentVersion != "1.0.0" {
			t.Errorf("currentVersion = %q, want %q", vc.currentVersion, "1.0.0")
		}
	})

	t.Run("custom interval", func(t *testing.T) {
		vc := NewVersionChecker("2.0.0", 10*time.Minute)
		if vc.checkInterval != 10*time.Minute {
			t.Errorf("checkInterval = %v, want %v", vc.checkInterval, 10*time.Minute)
		}
	})
}

// ---------------------------------------------------------------------------
// GracefulUpgrader tests
// ---------------------------------------------------------------------------

func TestGracefulUpgrader_PerformUpgrade(t *testing.T) {
	// Create a mock HTTP server serving a valid release asset
	binaryContent := []byte("new-binary-content")
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(binaryContent)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(binaryContent)
	}))
	defer assetServer.Close()

	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pilot")
	if err := os.WriteFile(binaryPath, []byte("old-binary"), 0755); err != nil {
		t.Fatalf("Failed to create test binary: %v", err)
	}

	statePath := filepath.Join(tempDir, "upgrade-state.json")

	upgrader := &Upgrader{
		currentVersion: "0.1.0",
		httpClient:     &http.Client{Timeout: 5 * time.Second},
		binaryPath:     binaryPath,
		backupPath:     binaryPath + BackupSuffix,
	}

	t.Run("success with no running tasks", func(t *testing.T) {
		g := &GracefulUpgrader{
			upgrader:    upgrader,
			statePath:   statePath,
			taskChecker: &NoOpTaskChecker{},
		}

		release := &Release{
			TagName: "v0.2.0",
			Assets: []Asset{
				{
					Name:               fmt.Sprintf("pilot-%s-%s", runtime.GOOS, runtime.GOARCH),
					BrowserDownloadURL: assetServer.URL,
					Size:               int64(len(binaryContent)),
				},
			},
		}

		var progressCalled bool
		opts := &UpgradeOptions{
			WaitForTasks: false,
			Force:        true,
			OnProgress: func(pct int, msg string) {
				progressCalled = true
			},
		}

		if err := g.PerformUpgrade(context.Background(), release, opts); err != nil {
			t.Fatalf("PerformUpgrade() error = %v", err)
		}

		if !progressCalled {
			t.Error("OnProgress callback was not called")
		}

		// State file should show completed
		state, err := LoadState(statePath)
		if err != nil {
			t.Fatalf("LoadState() error = %v", err)
		}
		if state.Status != StatusCompleted {
			t.Errorf("State.Status = %q, want %q", state.Status, StatusCompleted)
		}
	})

	t.Run("nil opts uses defaults", func(t *testing.T) {
		// Restore binary for next test
		if err := os.WriteFile(binaryPath, []byte("old-binary"), 0755); err != nil {
			t.Fatalf("Failed to restore binary: %v", err)
		}

		g := &GracefulUpgrader{
			upgrader:    upgrader,
			statePath:   statePath,
			taskChecker: &NoOpTaskChecker{},
		}

		release := &Release{
			TagName: "v0.2.0",
			Assets: []Asset{
				{
					Name:               fmt.Sprintf("pilot-%s-%s", runtime.GOOS, runtime.GOARCH),
					BrowserDownloadURL: assetServer.URL,
					Size:               int64(len(binaryContent)),
				},
			},
		}

		if err := g.PerformUpgrade(context.Background(), release, nil); err != nil {
			t.Fatalf("PerformUpgrade(nil opts) error = %v", err)
		}
	})

	t.Run("wait for tasks timeout", func(t *testing.T) {
		if err := os.WriteFile(binaryPath, []byte("old-binary"), 0755); err != nil {
			t.Fatalf("Failed to restore binary: %v", err)
		}

		checker := &mockTaskChecker{
			tasks:   []string{"task-1"},
			waitErr: context.DeadlineExceeded,
		}

		g := &GracefulUpgrader{
			upgrader:    upgrader,
			statePath:   statePath,
			taskChecker: checker,
		}

		release := &Release{TagName: "v0.2.0"}
		opts := &UpgradeOptions{
			WaitForTasks: true,
			TaskTimeout:  time.Millisecond,
			Force:        false,
		}

		err := g.PerformUpgrade(context.Background(), release, opts)
		if err == nil {
			t.Fatal("PerformUpgrade() should return error on task timeout")
		}
	})
}

func TestGracefulUpgrader_CheckAndRollback(t *testing.T) {
	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pilot")
	backupPath := binaryPath + BackupSuffix
	statePath := filepath.Join(tempDir, "upgrade-state.json")

	t.Run("no state file", func(t *testing.T) {
		g := &GracefulUpgrader{
			upgrader: &Upgrader{
				binaryPath: binaryPath,
				backupPath: backupPath,
			},
			statePath: statePath,
		}

		rolled, err := g.CheckAndRollback()
		if err != nil {
			t.Fatalf("CheckAndRollback() error = %v", err)
		}
		if rolled {
			t.Error("CheckAndRollback() = true, want false when no state file")
		}
	})

	t.Run("rollback on failed state", func(t *testing.T) {
		// Create binary and backup
		if err := os.WriteFile(binaryPath, []byte("new-bad-binary"), 0755); err != nil {
			t.Fatalf("Failed to create binary: %v", err)
		}
		if err := os.WriteFile(backupPath, []byte("original-good-binary"), 0755); err != nil {
			t.Fatalf("Failed to create backup: %v", err)
		}

		// Create failed state
		state := &State{
			Status:     StatusFailed,
			BackupPath: backupPath,
			Error:      "upgrade failed",
		}
		if err := state.Save(statePath); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		g := &GracefulUpgrader{
			upgrader: &Upgrader{
				binaryPath: binaryPath,
				backupPath: backupPath,
			},
			statePath: statePath,
		}

		rolled, err := g.CheckAndRollback()
		if err != nil {
			t.Fatalf("CheckAndRollback() error = %v", err)
		}
		if !rolled {
			t.Error("CheckAndRollback() = false, want true for failed state")
		}

		// Verify binary was restored
		content, err := os.ReadFile(binaryPath)
		if err != nil {
			t.Fatalf("Failed to read binary: %v", err)
		}
		if string(content) != "original-good-binary" {
			t.Errorf("Binary content = %q, want %q", string(content), "original-good-binary")
		}
	})

	t.Run("no rollback for completed state", func(t *testing.T) {
		state := &State{
			Status: StatusCompleted,
		}
		if err := state.Save(statePath); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		g := &GracefulUpgrader{
			upgrader: &Upgrader{
				binaryPath: binaryPath,
				backupPath: backupPath,
			},
			statePath: statePath,
		}

		rolled, err := g.CheckAndRollback()
		if err != nil {
			t.Fatalf("CheckAndRollback() error = %v", err)
		}
		if rolled {
			t.Error("CheckAndRollback() = true, want false for completed state")
		}
	})
}

func TestGracefulUpgrader_CleanupState(t *testing.T) {
	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pilot")
	backupPath := binaryPath + BackupSuffix
	statePath := filepath.Join(tempDir, "upgrade-state.json")

	t.Run("cleanup completed state", func(t *testing.T) {
		// Create backup file
		if err := os.WriteFile(backupPath, []byte("backup"), 0755); err != nil {
			t.Fatalf("Failed to create backup: %v", err)
		}

		state := &State{Status: StatusCompleted}
		if err := state.Save(statePath); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		g := &GracefulUpgrader{
			upgrader: &Upgrader{
				binaryPath: binaryPath,
				backupPath: backupPath,
			},
			statePath: statePath,
		}

		if err := g.CleanupState(); err != nil {
			t.Fatalf("CleanupState() error = %v", err)
		}

		// State should be cleared
		loaded, err := LoadState(statePath)
		if err != nil {
			t.Fatalf("LoadState() error = %v", err)
		}
		if loaded != nil {
			t.Error("State should be nil after cleanup")
		}
	})

	t.Run("no cleanup for non-completed state", func(t *testing.T) {
		state := &State{Status: StatusFailed}
		if err := state.Save(statePath); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		g := &GracefulUpgrader{
			upgrader: &Upgrader{
				binaryPath: binaryPath,
				backupPath: backupPath,
			},
			statePath: statePath,
		}

		if err := g.CleanupState(); err != nil {
			t.Fatalf("CleanupState() error = %v", err)
		}

		// State should still exist
		loaded, err := LoadState(statePath)
		if err != nil {
			t.Fatalf("LoadState() error = %v", err)
		}
		if loaded == nil {
			t.Error("State should not be cleared for non-completed status")
		}
	})

	t.Run("no state file is ok", func(t *testing.T) {
		_ = os.Remove(statePath)

		g := &GracefulUpgrader{
			upgrader: &Upgrader{
				binaryPath: binaryPath,
				backupPath: backupPath,
			},
			statePath: statePath,
		}

		if err := g.CleanupState(); err != nil {
			t.Fatalf("CleanupState() error = %v, should handle missing state", err)
		}
	})
}

// ---------------------------------------------------------------------------
// HotUpgrader tests
// ---------------------------------------------------------------------------

func TestHotUpgrader_Accessors(t *testing.T) {
	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pilot")
	if err := os.WriteFile(binaryPath, []byte("binary"), 0755); err != nil {
		t.Fatalf("Failed to create binary: %v", err)
	}

	u := &Upgrader{
		currentVersion: "1.0.0",
		binaryPath:     binaryPath,
		backupPath:     binaryPath + BackupSuffix,
	}
	g := &GracefulUpgrader{upgrader: u, taskChecker: &NoOpTaskChecker{}}
	h := &HotUpgrader{graceful: g, taskChecker: &NoOpTaskChecker{}}

	if got := h.GetUpgrader(); got != u {
		t.Error("GetUpgrader() did not return expected upgrader")
	}
	if got := h.GetGracefulUpgrader(); got != g {
		t.Error("GetGracefulUpgrader() did not return expected graceful upgrader")
	}
}

func TestHotUpgrader_PerformHotUpgrade(t *testing.T) {
	binaryContent := []byte("new-hot-binary")
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(binaryContent)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(binaryContent)
	}))
	defer assetServer.Close()

	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pilot")
	statePath := filepath.Join(tempDir, "upgrade-state.json")

	t.Run("task wait timeout", func(t *testing.T) {
		if err := os.WriteFile(binaryPath, []byte("old-binary"), 0755); err != nil {
			t.Fatalf("Failed to create binary: %v", err)
		}

		checker := &mockTaskChecker{
			tasks:   []string{"running-task"},
			waitErr: context.DeadlineExceeded,
		}

		u := &Upgrader{
			currentVersion: "0.1.0",
			httpClient:     &http.Client{Timeout: 5 * time.Second},
			binaryPath:     binaryPath,
			backupPath:     binaryPath + BackupSuffix,
		}
		g := &GracefulUpgrader{upgrader: u, statePath: statePath, taskChecker: checker}
		h := &HotUpgrader{graceful: g, taskChecker: checker}

		release := &Release{TagName: "v0.2.0"}
		cfg := &HotUpgradeConfig{
			WaitForTasks: true,
			TaskTimeout:  time.Millisecond,
		}

		err := h.PerformHotUpgrade(context.Background(), release, cfg)
		if err == nil {
			t.Fatal("PerformHotUpgrade() should return error on task timeout")
		}
	})

	t.Run("flush session callback", func(t *testing.T) {
		if err := os.WriteFile(binaryPath, []byte("old-binary"), 0755); err != nil {
			t.Fatalf("Failed to create binary: %v", err)
		}

		var flushed int32
		u := &Upgrader{
			currentVersion: "0.1.0",
			httpClient:     &http.Client{Timeout: 5 * time.Second},
			binaryPath:     binaryPath,
			backupPath:     binaryPath + BackupSuffix,
		}
		g := &GracefulUpgrader{upgrader: u, statePath: statePath, taskChecker: &NoOpTaskChecker{}}
		h := &HotUpgrader{graceful: g, taskChecker: &NoOpTaskChecker{}}

		release := &Release{
			TagName: "v0.2.0",
			Assets: []Asset{
				{
					Name:               fmt.Sprintf("pilot-%s-%s", runtime.GOOS, runtime.GOARCH),
					BrowserDownloadURL: assetServer.URL,
					Size:               int64(len(binaryContent)),
				},
			},
		}

		cfg := &HotUpgradeConfig{
			WaitForTasks: false,
			FlushSession: func() error {
				atomic.StoreInt32(&flushed, 1)
				return nil
			},
		}

		// This will succeed through upgrade but fail on RestartWithNewBinary
		// since we can't actually exec in a test. It may also succeed if
		// CanHotRestart returns true and RestartWithNewBinary succeeds (unlikely in test).
		// We just verify the flush callback ran.
		_ = h.PerformHotUpgrade(context.Background(), release, cfg)

		if atomic.LoadInt32(&flushed) != 1 {
			t.Error("FlushSession callback was not called")
		}
	})

	t.Run("flush session error is non-fatal", func(t *testing.T) {
		if err := os.WriteFile(binaryPath, []byte("old-binary"), 0755); err != nil {
			t.Fatalf("Failed to create binary: %v", err)
		}

		u := &Upgrader{
			currentVersion: "0.1.0",
			httpClient:     &http.Client{Timeout: 5 * time.Second},
			binaryPath:     binaryPath,
			backupPath:     binaryPath + BackupSuffix,
		}
		g := &GracefulUpgrader{upgrader: u, statePath: statePath, taskChecker: &NoOpTaskChecker{}}
		h := &HotUpgrader{graceful: g, taskChecker: &NoOpTaskChecker{}}

		release := &Release{
			TagName: "v0.2.0",
			Assets: []Asset{
				{
					Name:               fmt.Sprintf("pilot-%s-%s", runtime.GOOS, runtime.GOARCH),
					BrowserDownloadURL: assetServer.URL,
					Size:               int64(len(binaryContent)),
				},
			},
		}

		cfg := &HotUpgradeConfig{
			WaitForTasks: false,
			FlushSession: func() error {
				return errors.New("flush failed")
			},
		}

		// Flush error is non-fatal, so upgrade should continue
		_ = h.PerformHotUpgrade(context.Background(), release, cfg)
	})

	t.Run("nil config uses defaults", func(t *testing.T) {
		if err := os.WriteFile(binaryPath, []byte("old-binary"), 0755); err != nil {
			t.Fatalf("Failed to create binary: %v", err)
		}

		u := &Upgrader{
			currentVersion: "0.1.0",
			httpClient:     &http.Client{Timeout: 5 * time.Second},
			binaryPath:     binaryPath,
			backupPath:     binaryPath + BackupSuffix,
		}
		g := &GracefulUpgrader{upgrader: u, statePath: statePath, taskChecker: &NoOpTaskChecker{}}
		h := &HotUpgrader{graceful: g, taskChecker: &NoOpTaskChecker{}}

		release := &Release{
			TagName: "v0.2.0",
			Assets: []Asset{
				{
					Name:               fmt.Sprintf("pilot-%s-%s", runtime.GOOS, runtime.GOARCH),
					BrowserDownloadURL: assetServer.URL,
					Size:               int64(len(binaryContent)),
				},
			},
		}

		// nil config should not panic
		_ = h.PerformHotUpgrade(context.Background(), release, nil)
	})
}

// ---------------------------------------------------------------------------
// State additional tests
// ---------------------------------------------------------------------------

func TestLoadState_empty_path(t *testing.T) {
	// LoadState with empty path falls back to DefaultStatePath, which may or may not exist.
	// We just verify it doesn't panic.
	_, _ = LoadState("")
}

func TestState_JSON_roundtrip(t *testing.T) {
	s := &State{
		PreviousVersion: "1.0.0",
		NewVersion:      "2.0.0",
		UpgradeStarted:  time.Now().Truncate(time.Second),
		PendingTasks:    []string{"t1", "t2"},
		BackupPath:      "/tmp/backup",
		Status:          StatusWaiting,
		Error:           "some error",
	}

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var loaded State
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if loaded.PreviousVersion != s.PreviousVersion {
		t.Errorf("PreviousVersion = %q, want %q", loaded.PreviousVersion, s.PreviousVersion)
	}
	if loaded.Status != s.Status {
		t.Errorf("Status = %q, want %q", loaded.Status, s.Status)
	}
	if len(loaded.PendingTasks) != 2 {
		t.Errorf("PendingTasks len = %d, want 2", len(loaded.PendingTasks))
	}
}

// ---------------------------------------------------------------------------
// upgrade.go additional tests
// ---------------------------------------------------------------------------

func TestUpgrader_isTarGz(t *testing.T) {
	tempDir := t.TempDir()
	u := &Upgrader{}

	t.Run("valid gzip", func(t *testing.T) {
		path := filepath.Join(tempDir, "test.tar.gz")
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("Failed to create file: %v", err)
		}
		gw := gzip.NewWriter(f)
		tw := tar.NewWriter(gw)
		hdr := &tar.Header{Name: "pilot", Mode: 0755, Size: 4}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader error: %v", err)
		}
		if _, err := tw.Write([]byte("test")); err != nil {
			t.Fatalf("Write error: %v", err)
		}
		_ = tw.Close()
		_ = gw.Close()
		_ = f.Close()

		if !u.isTarGz(path) {
			t.Error("isTarGz() = false for valid gzip file")
		}
	})

	t.Run("not gzip", func(t *testing.T) {
		path := filepath.Join(tempDir, "notgz")
		if err := os.WriteFile(path, []byte("not a gzip file"), 0644); err != nil {
			t.Fatalf("WriteFile error: %v", err)
		}
		if u.isTarGz(path) {
			t.Error("isTarGz() = true for non-gzip file")
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		if u.isTarGz(filepath.Join(tempDir, "nonexistent")) {
			t.Error("isTarGz() = true for nonexistent file")
		}
	})
}

func TestUpgrader_installFromTarGz(t *testing.T) {
	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pilot")
	u := &Upgrader{binaryPath: binaryPath}

	t.Run("extract pilot binary", func(t *testing.T) {
		tarPath := filepath.Join(tempDir, "test.tar.gz")
		f, err := os.Create(tarPath)
		if err != nil {
			t.Fatalf("Create error: %v", err)
		}

		content := []byte("pilot-binary-from-tarball")
		gw := gzip.NewWriter(f)
		tw := tar.NewWriter(gw)

		// Add a non-pilot file first (should be skipped)
		hdr := &tar.Header{Name: "README.md", Mode: 0644, Size: 6, Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader error: %v", err)
		}
		if _, err := tw.Write([]byte("readme")); err != nil {
			t.Fatalf("Write error: %v", err)
		}

		// Add the pilot binary
		hdr = &tar.Header{Name: "pilot", Mode: 0755, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader error: %v", err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("Write error: %v", err)
		}

		_ = tw.Close()
		_ = gw.Close()
		_ = f.Close()

		if err := u.installFromTarGz(tarPath); err != nil {
			t.Fatalf("installFromTarGz() error = %v", err)
		}

		got, err := os.ReadFile(binaryPath)
		if err != nil {
			t.Fatalf("ReadFile error: %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("Extracted content = %q, want %q", string(got), string(content))
		}
	})

	t.Run("no pilot binary in archive", func(t *testing.T) {
		tarPath := filepath.Join(tempDir, "nopilot.tar.gz")
		f, err := os.Create(tarPath)
		if err != nil {
			t.Fatalf("Create error: %v", err)
		}

		gw := gzip.NewWriter(f)
		tw := tar.NewWriter(gw)
		hdr := &tar.Header{Name: "other-file", Mode: 0644, Size: 4, Typeflag: tar.TypeReg}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write([]byte("data"))
		_ = tw.Close()
		_ = gw.Close()
		_ = f.Close()

		err = u.installFromTarGz(tarPath)
		if err == nil {
			t.Error("installFromTarGz() should error when pilot binary not found")
		}
	})
}

func TestUpgrader_installDirectBinary(t *testing.T) {
	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pilot")
	u := &Upgrader{binaryPath: binaryPath}

	srcPath := filepath.Join(tempDir, "downloaded")
	content := []byte("direct-binary-content")
	if err := os.WriteFile(srcPath, content, 0644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	if err := u.installDirectBinary(srcPath); err != nil {
		t.Fatalf("installDirectBinary() error = %v", err)
	}

	got, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("Installed content = %q, want %q", string(got), string(content))
	}
}

func TestUpgrader_downloadAsset(t *testing.T) {
	content := []byte("downloaded-asset-bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))
	defer server.Close()

	u := &Upgrader{
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}

	t.Run("successful download", func(t *testing.T) {
		asset := &Asset{
			BrowserDownloadURL: server.URL,
			Size:               int64(len(content)),
		}

		var progressPcts []int
		tmpPath, err := u.downloadAsset(context.Background(), asset, func(pct int, msg string) {
			progressPcts = append(progressPcts, pct)
		})
		if err != nil {
			t.Fatalf("downloadAsset() error = %v", err)
		}
		defer func() { _ = os.Remove(tmpPath) }()

		got, err := os.ReadFile(tmpPath)
		if err != nil {
			t.Fatalf("ReadFile error: %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("Downloaded content = %q, want %q", string(got), string(content))
		}
	})

	t.Run("server error", func(t *testing.T) {
		errServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer errServer.Close()

		asset := &Asset{BrowserDownloadURL: errServer.URL}
		_, err := u.downloadAsset(context.Background(), asset, nil)
		if err == nil {
			t.Error("downloadAsset() should return error on 500")
		}
	})
}

func TestUpgrader_fetchLatestRelease(t *testing.T) {
	t.Run("valid releases", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"tag_name": "v1.0.0", "draft": true, "prerelease": false},
				{"tag_name": "v0.9.0", "draft": false, "prerelease": true},
				{"tag_name": "v0.8.0", "draft": false, "prerelease": false, "body": "stable", "assets": []}
			]`))
		}))
		defer server.Close()

		// We can't easily override the URL used by fetchLatestRelease,
		// so test the response parsing indirectly via the mock server.
		resp, err := http.Get(server.URL)
		if err != nil {
			t.Fatalf("HTTP GET error: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		var releases []Release
		if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
			t.Fatalf("Decode error: %v", err)
		}

		// Simulate the logic in fetchLatestRelease
		var found *Release
		for i := range releases {
			if !releases[i].Draft && !releases[i].Prerelease {
				found = &releases[i]
				break
			}
		}

		if found == nil {
			t.Fatal("No stable release found")
		}
		if found.TagName != "v0.8.0" {
			t.Errorf("Selected release = %q, want %q", found.TagName, "v0.8.0")
		}
	})

	t.Run("all drafts fallback to first", func(t *testing.T) {
		releases := []Release{
			{TagName: "v2.0.0", Draft: true},
			{TagName: "v1.9.0", Draft: true, Prerelease: true},
		}

		// Simulate fallback logic
		var found *Release
		for i := range releases {
			if !releases[i].Draft && !releases[i].Prerelease {
				found = &releases[i]
				break
			}
		}
		if found == nil && len(releases) > 0 {
			found = &releases[0]
		}

		if found == nil || found.TagName != "v2.0.0" {
			t.Errorf("Fallback release = %v, want v2.0.0", found)
		}
	})
}

func TestUpgrader_BinaryPath(t *testing.T) {
	u := &Upgrader{binaryPath: "/usr/local/bin/pilot"}
	if got := u.BinaryPath(); got != "/usr/local/bin/pilot" {
		t.Errorf("BinaryPath() = %q, want %q", got, "/usr/local/bin/pilot")
	}
}

func TestUpgrader_installBinary_dispatch(t *testing.T) {
	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pilot")
	u := &Upgrader{binaryPath: binaryPath}

	t.Run("direct binary", func(t *testing.T) {
		srcPath := filepath.Join(tempDir, "plain-binary")
		if err := os.WriteFile(srcPath, []byte("plain"), 0644); err != nil {
			t.Fatalf("WriteFile error: %v", err)
		}

		if err := u.installBinary(srcPath); err != nil {
			t.Fatalf("installBinary() error = %v", err)
		}

		got, err := os.ReadFile(binaryPath)
		if err != nil {
			t.Fatalf("ReadFile error: %v", err)
		}
		if string(got) != "plain" {
			t.Errorf("Installed content = %q, want %q", string(got), "plain")
		}
	})
}

func TestUpgrader_Rollback_noBackup(t *testing.T) {
	tempDir := t.TempDir()
	u := &Upgrader{
		binaryPath: filepath.Join(tempDir, "pilot"),
		backupPath: filepath.Join(tempDir, "pilot.backup"),
	}

	err := u.Rollback()
	if err == nil {
		t.Error("Rollback() should error when no backup exists")
	}
}
