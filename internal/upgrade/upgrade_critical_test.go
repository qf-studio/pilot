package upgrade

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
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

// ---------------------------------------------------------------------------
// downloadAsset
// ---------------------------------------------------------------------------

func TestDownloadAsset_Success(t *testing.T) {
	payload := []byte("binary-content-here")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	u := &Upgrader{httpClient: server.Client()}
	asset := &Asset{
		BrowserDownloadURL: server.URL + "/pilot.tar.gz",
		Size:               int64(len(payload)),
	}

	var progressCalled bool
	tmpPath, err := u.downloadAsset(context.Background(), asset, func(pct int, msg string) {
		progressCalled = true
	})
	if err != nil {
		t.Fatalf("downloadAsset() error = %v", err)
	}
	defer func() { _ = os.Remove(tmpPath) }()

	got, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("downloaded content = %q, want %q", got, payload)
	}
	if !progressCalled {
		t.Error("progress callback was not invoked")
	}
}

func TestDownloadAsset_404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	u := &Upgrader{httpClient: server.Client()}
	asset := &Asset{BrowserDownloadURL: server.URL + "/missing", Size: 100}

	_, err := u.downloadAsset(context.Background(), asset, nil)
	if err == nil {
		t.Fatal("downloadAsset() expected error for 404, got nil")
	}
}

func TestDownloadAsset_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := server.Client()
	client.Timeout = 50 * time.Millisecond

	u := &Upgrader{httpClient: client}
	asset := &Asset{BrowserDownloadURL: server.URL + "/slow", Size: 100}

	_, err := u.downloadAsset(context.Background(), asset, nil)
	if err == nil {
		t.Fatal("downloadAsset() expected timeout error, got nil")
	}
}

func TestDownloadAsset_ContextCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	u := &Upgrader{httpClient: server.Client()}
	asset := &Asset{BrowserDownloadURL: server.URL + "/slow", Size: 100}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := u.downloadAsset(ctx, asset, nil)
	if err == nil {
		t.Fatal("downloadAsset() expected context canceled error, got nil")
	}
}

func TestDownloadAsset_NilProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data"))
	}))
	defer server.Close()

	u := &Upgrader{httpClient: server.Client()}
	asset := &Asset{BrowserDownloadURL: server.URL, Size: 0}

	tmpPath, err := u.downloadAsset(context.Background(), asset, nil)
	if err != nil {
		t.Fatalf("downloadAsset() error = %v", err)
	}
	defer func() { _ = os.Remove(tmpPath) }()
}

// ---------------------------------------------------------------------------
// createBackup
// ---------------------------------------------------------------------------

func TestCreateBackup_Success(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	content := []byte("original-binary")
	if err := os.WriteFile(binPath, content, 0755); err != nil {
		t.Fatal(err)
	}

	u := &Upgrader{binaryPath: binPath, backupPath: binPath + BackupSuffix}
	if err := u.createBackup(); err != nil {
		t.Fatalf("createBackup() error = %v", err)
	}

	got, err := os.ReadFile(u.backupPath)
	if err != nil {
		t.Fatalf("failed to read backup: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("backup content = %q, want %q", got, content)
	}
}

func TestCreateBackup_ReplacesExistingBackup(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	backupPath := binPath + BackupSuffix

	if err := os.WriteFile(binPath, []byte("v2"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, []byte("old-backup"), 0755); err != nil {
		t.Fatal(err)
	}

	u := &Upgrader{binaryPath: binPath, backupPath: backupPath}
	if err := u.createBackup(); err != nil {
		t.Fatalf("createBackup() error = %v", err)
	}

	got, _ := os.ReadFile(backupPath)
	if string(got) != "v2" {
		t.Errorf("backup content = %q, want %q", got, "v2")
	}
}

func TestCreateBackup_SourceMissing(t *testing.T) {
	dir := t.TempDir()
	u := &Upgrader{
		binaryPath: filepath.Join(dir, "nonexistent"),
		backupPath: filepath.Join(dir, "nonexistent.backup"),
	}
	if err := u.createBackup(); err == nil {
		t.Fatal("createBackup() expected error for missing source, got nil")
	}
}

func TestCreateBackup_ReadOnlyDir(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	if err := os.WriteFile(binPath, []byte("bin"), 0755); err != nil {
		t.Fatal(err)
	}

	readOnlyDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(readOnlyDir, 0755) }()

	u := &Upgrader{
		binaryPath: binPath,
		backupPath: filepath.Join(readOnlyDir, "pilot.backup"),
	}
	err := u.createBackup()
	if err == nil {
		t.Fatal("createBackup() expected permission error, got nil")
	}
}

// ---------------------------------------------------------------------------
// installBinary dispatch
// ---------------------------------------------------------------------------

func TestInstallBinary_DirectBinary(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	srcPath := filepath.Join(dir, "downloaded")
	if err := os.WriteFile(srcPath, []byte("new-binary"), 0644); err != nil {
		t.Fatal(err)
	}

	u := &Upgrader{binaryPath: binPath}
	if err := u.installBinary(srcPath); err != nil {
		t.Fatalf("installBinary() error = %v", err)
	}

	got, _ := os.ReadFile(binPath)
	if string(got) != "new-binary" {
		t.Errorf("installed content = %q, want %q", got, "new-binary")
	}

	info, _ := os.Stat(binPath)
	if info.Mode()&0755 != 0755 {
		t.Errorf("installed permissions = %o, want 0755", info.Mode()&os.ModePerm)
	}
}

func TestInstallBinary_TarGz(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	tarPath := filepath.Join(dir, "update.tar.gz")

	createTestTarGz(t, tarPath, "pilot", []byte("tar-binary-content"))

	u := &Upgrader{binaryPath: binPath}
	if err := u.installBinary(tarPath); err != nil {
		t.Fatalf("installBinary() error = %v", err)
	}

	got, _ := os.ReadFile(binPath)
	if string(got) != "tar-binary-content" {
		t.Errorf("installed content = %q, want %q", got, "tar-binary-content")
	}
}

// ---------------------------------------------------------------------------
// isTarGz
// ---------------------------------------------------------------------------

func TestIsTarGz_ValidGzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.tar.gz")
	createTestTarGz(t, path, "pilot", []byte("content"))

	u := &Upgrader{}
	if !u.isTarGz(path) {
		t.Error("isTarGz() = false for valid gzip file")
	}
}

func TestIsTarGz_NotGzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notgz")
	if err := os.WriteFile(path, []byte("plain text file"), 0644); err != nil {
		t.Fatal(err)
	}

	u := &Upgrader{}
	if u.isTarGz(path) {
		t.Error("isTarGz() = true for non-gzip file")
	}
}

func TestIsTarGz_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}

	u := &Upgrader{}
	if u.isTarGz(path) {
		t.Error("isTarGz() = true for empty file")
	}
}

func TestIsTarGz_NonexistentFile(t *testing.T) {
	u := &Upgrader{}
	if u.isTarGz("/nonexistent/path/file.tar.gz") {
		t.Error("isTarGz() = true for nonexistent file")
	}
}

// ---------------------------------------------------------------------------
// installFromTarGz
// ---------------------------------------------------------------------------

func TestInstallFromTarGz_Success(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	tarPath := filepath.Join(dir, "release.tar.gz")
	content := []byte("pilot-binary-from-tar")

	createTestTarGz(t, tarPath, "pilot", content)

	u := &Upgrader{binaryPath: binPath}
	if err := u.installFromTarGz(tarPath); err != nil {
		t.Fatalf("installFromTarGz() error = %v", err)
	}

	got, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("failed to read installed binary: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestInstallFromTarGz_NestedPath(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	tarPath := filepath.Join(dir, "release.tar.gz")

	createTestTarGz(t, tarPath, "dist/pilot", []byte("nested-binary"))

	u := &Upgrader{binaryPath: binPath}
	if err := u.installFromTarGz(tarPath); err != nil {
		t.Fatalf("installFromTarGz() error = %v", err)
	}

	got, _ := os.ReadFile(binPath)
	if string(got) != "nested-binary" {
		t.Errorf("content = %q, want %q", got, "nested-binary")
	}
}

func TestInstallFromTarGz_NoBinary(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	tarPath := filepath.Join(dir, "release.tar.gz")

	createTestTarGz(t, tarPath, "README.md", []byte("readme"))

	u := &Upgrader{binaryPath: binPath}
	err := u.installFromTarGz(tarPath)
	if err == nil {
		t.Fatal("installFromTarGz() expected error for missing binary, got nil")
	}
}

func TestInstallFromTarGz_CorruptedArchive(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "corrupt.tar.gz")
	// Write gzip magic bytes followed by garbage
	if err := os.WriteFile(tarPath, []byte{0x1f, 0x8b, 0x08, 0x00, 0xff, 0xff}, 0644); err != nil {
		t.Fatal(err)
	}

	u := &Upgrader{binaryPath: filepath.Join(dir, "pilot")}
	err := u.installFromTarGz(tarPath)
	if err == nil {
		t.Fatal("installFromTarGz() expected error for corrupted archive, got nil")
	}
}

func TestInstallFromTarGz_NonexistentFile(t *testing.T) {
	u := &Upgrader{binaryPath: "/tmp/pilot"}
	err := u.installFromTarGz("/nonexistent/file.tar.gz")
	if err == nil {
		t.Fatal("installFromTarGz() expected error for nonexistent file, got nil")
	}
}

func TestInstallFromTarGz_PilotExe(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	tarPath := filepath.Join(dir, "release.tar.gz")

	createTestTarGz(t, tarPath, "pilot.exe", []byte("windows-binary"))

	u := &Upgrader{binaryPath: binPath}
	if err := u.installFromTarGz(tarPath); err != nil {
		t.Fatalf("installFromTarGz() error = %v", err)
	}

	got, _ := os.ReadFile(binPath)
	if string(got) != "windows-binary" {
		t.Errorf("content = %q, want %q", got, "windows-binary")
	}
}

// ---------------------------------------------------------------------------
// installDirectBinary
// ---------------------------------------------------------------------------

func TestInstallDirectBinary_Success(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "downloaded")
	binPath := filepath.Join(dir, "pilot")

	content := []byte("direct-binary-content")
	if err := os.WriteFile(srcPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	u := &Upgrader{binaryPath: binPath}
	if err := u.installDirectBinary(srcPath); err != nil {
		t.Fatalf("installDirectBinary() error = %v", err)
	}

	got, _ := os.ReadFile(binPath)
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}

	info, _ := os.Stat(binPath)
	if info.Mode()&0755 != 0755 {
		t.Errorf("permissions = %o, want 0755", info.Mode()&os.ModePerm)
	}
}

func TestInstallDirectBinary_SourceMissing(t *testing.T) {
	dir := t.TempDir()
	u := &Upgrader{binaryPath: filepath.Join(dir, "pilot")}
	err := u.installDirectBinary(filepath.Join(dir, "nonexistent"))
	if err == nil {
		t.Fatal("installDirectBinary() expected error for missing source, got nil")
	}
}

func TestInstallDirectBinary_ReadOnlyDest(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src")
	if err := os.WriteFile(srcPath, []byte("bin"), 0644); err != nil {
		t.Fatal(err)
	}

	readOnlyDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(readOnlyDir, 0755) }()

	u := &Upgrader{binaryPath: filepath.Join(readOnlyDir, "pilot")}
	err := u.installDirectBinary(srcPath)
	if err == nil {
		t.Fatal("installDirectBinary() expected permission error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Rollback / Recovery paths
// ---------------------------------------------------------------------------

func TestUpgrade_RollbackOnInstallFailure(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	backupPath := binPath + BackupSuffix

	originalContent := []byte("original-v1")
	if err := os.WriteFile(binPath, originalContent, 0755); err != nil {
		t.Fatal(err)
	}

	u := &Upgrader{
		currentVersion: "1.0.0",
		binaryPath:     binPath,
		backupPath:     backupPath,
	}

	// Create backup
	if err := u.createBackup(); err != nil {
		t.Fatalf("createBackup() error = %v", err)
	}

	// Simulate failed install (binary gets corrupted)
	if err := os.WriteFile(binPath, []byte("corrupted"), 0755); err != nil {
		t.Fatal(err)
	}

	// Rollback should restore original
	if err := u.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	got, _ := os.ReadFile(binPath)
	if string(got) != string(originalContent) {
		t.Errorf("after rollback content = %q, want %q", got, originalContent)
	}

	if u.HasBackup() {
		t.Error("HasBackup() = true after rollback, want false")
	}
}

func TestRollback_NoBackup(t *testing.T) {
	dir := t.TempDir()
	u := &Upgrader{
		binaryPath: filepath.Join(dir, "pilot"),
		backupPath: filepath.Join(dir, "pilot.backup"),
	}
	err := u.Rollback()
	if err == nil {
		t.Fatal("Rollback() expected error when no backup exists, got nil")
	}
}

func TestUpgrade_EndToEnd_RollbackOnFailedInstall(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	backupPath := binPath + BackupSuffix

	originalContent := []byte("original-v1")
	if err := os.WriteFile(binPath, originalContent, 0755); err != nil {
		t.Fatal(err)
	}

	// Serve a valid download but make the install target read-only after backup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("new-binary"))
	}))
	defer server.Close()

	u := &Upgrader{
		currentVersion: "1.0.0",
		httpClient:     server.Client(),
		binaryPath:     binPath,
		backupPath:     backupPath,
	}

	// First create backup manually
	if err := u.createBackup(); err != nil {
		t.Fatal(err)
	}

	// Verify backup exists before rollback
	if !u.HasBackup() {
		t.Fatal("backup should exist")
	}

	// Simulate corrupted install
	if err := os.WriteFile(binPath, []byte("bad"), 0755); err != nil {
		t.Fatal(err)
	}

	// Rollback
	if err := u.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	got, _ := os.ReadFile(binPath)
	if string(got) != string(originalContent) {
		t.Errorf("content = %q, want %q", got, originalContent)
	}
}

// ---------------------------------------------------------------------------
// isHomebrewPath
// ---------------------------------------------------------------------------

func TestIsHomebrewPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/opt/homebrew/Cellar/pilot/1.0/bin/pilot", true},
		{"/usr/local/Cellar/pilot/1.0/bin/pilot", true},
		{"/home/linuxbrew/.linuxbrew/Cellar/pilot/1.0/bin/pilot", true},
		{"/usr/local/bin/pilot", false},
		{"/home/user/pilot", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isHomebrewPath(tt.path)
			if got != tt.want {
				t.Errorf("isHomebrewPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// findAsset — extended tests
// ---------------------------------------------------------------------------

func TestFindAsset_MatchesCurrentPlatform(t *testing.T) {
	tarName := fmt.Sprintf("pilot-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	release := &Release{
		Assets: []Asset{
			{Name: tarName, BrowserDownloadURL: "https://example.com/tar"},
			{Name: "pilot-other-other.tar.gz", BrowserDownloadURL: "https://example.com/other"},
		},
	}

	u := &Upgrader{}
	asset := u.findAsset(release)
	if asset == nil {
		t.Fatal("findAsset() returned nil for current platform")
	}
	if asset.Name != tarName {
		t.Errorf("findAsset() name = %q, want %q", asset.Name, tarName)
	}
}

func TestFindAsset_FallbackToBinary(t *testing.T) {
	binaryName := fmt.Sprintf("pilot-%s-%s", runtime.GOOS, runtime.GOARCH)
	release := &Release{
		Assets: []Asset{
			{Name: binaryName, BrowserDownloadURL: "https://example.com/binary"},
		},
	}

	u := &Upgrader{}
	asset := u.findAsset(release)
	if asset == nil {
		t.Fatal("findAsset() returned nil for direct binary asset")
	}
	if asset.Name != binaryName {
		t.Errorf("findAsset() name = %q, want %q", asset.Name, binaryName)
	}
}

func TestFindAsset_NoMatch(t *testing.T) {
	release := &Release{
		Assets: []Asset{
			{Name: "pilot-fakeos-fakearch.tar.gz"},
		},
	}

	u := &Upgrader{}
	if asset := u.findAsset(release); asset != nil {
		t.Errorf("findAsset() = %v, want nil for unmatched platform", asset)
	}
}

func TestFindAsset_EmptyAssets(t *testing.T) {
	u := &Upgrader{}
	if asset := u.findAsset(&Release{}); asset != nil {
		t.Errorf("findAsset() = %v, want nil for empty assets", asset)
	}
}

// ---------------------------------------------------------------------------
// Upgrade end-to-end with mock server
// ---------------------------------------------------------------------------

func TestUpgrade_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	backupPath := binPath + BackupSuffix

	if err := os.WriteFile(binPath, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	newBinary := []byte("new-binary-v2")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(newBinary)))
		_, _ = w.Write(newBinary)
	}))
	defer server.Close()

	u := &Upgrader{
		currentVersion: "1.0.0",
		httpClient:     server.Client(),
		binaryPath:     binPath,
		backupPath:     backupPath,
	}

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

	var progressMessages []string
	err := u.Upgrade(context.Background(), release, func(pct int, msg string) {
		progressMessages = append(progressMessages, msg)
	})
	if err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}

	got, _ := os.ReadFile(binPath)
	if string(got) != string(newBinary) {
		t.Errorf("installed content = %q, want %q", got, newBinary)
	}

	if !u.HasBackup() {
		t.Error("backup should exist after upgrade")
	}

	if len(progressMessages) == 0 {
		t.Error("no progress messages reported")
	}
}

func TestUpgrade_NoMatchingAsset(t *testing.T) {
	u := &Upgrader{
		currentVersion: "1.0.0",
		httpClient:     &http.Client{},
	}
	release := &Release{
		TagName: "v2.0.0",
		Assets:  []Asset{{Name: "pilot-fakeos-fakearch.tar.gz"}},
	}

	err := u.Upgrade(context.Background(), release, nil)
	if err == nil {
		t.Fatal("Upgrade() expected error for no matching asset, got nil")
	}
}

func TestUpgrade_WithTarGzAsset(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	backupPath := binPath + BackupSuffix

	if err := os.WriteFile(binPath, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create a tar.gz payload
	tarGzPath := filepath.Join(dir, "payload.tar.gz")
	createTestTarGz(t, tarGzPath, "pilot", []byte("tarred-binary"))
	tarGzData, err := os.ReadFile(tarGzPath)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(tarGzData)))
		_, _ = w.Write(tarGzData)
	}))
	defer server.Close()

	u := &Upgrader{
		currentVersion: "1.0.0",
		httpClient:     server.Client(),
		binaryPath:     binPath,
		backupPath:     backupPath,
	}

	tarName := fmt.Sprintf("pilot-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	release := &Release{
		TagName: "v2.0.0",
		Assets: []Asset{
			{
				Name:               tarName,
				BrowserDownloadURL: server.URL + "/pilot.tar.gz",
				Size:               int64(len(tarGzData)),
			},
		},
	}

	if err := u.Upgrade(context.Background(), release, nil); err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}

	got, _ := os.ReadFile(binPath)
	if string(got) != "tarred-binary" {
		t.Errorf("installed content = %q, want %q", got, "tarred-binary")
	}
}

// ---------------------------------------------------------------------------
// BinaryPath / HasBackup / CleanupBackup
// ---------------------------------------------------------------------------

func TestBinaryPath(t *testing.T) {
	u := &Upgrader{binaryPath: "/usr/local/bin/pilot"}
	if got := u.BinaryPath(); got != "/usr/local/bin/pilot" {
		t.Errorf("BinaryPath() = %q, want %q", got, "/usr/local/bin/pilot")
	}
}

func TestCleanupBackup_NoBackup(t *testing.T) {
	dir := t.TempDir()
	u := &Upgrader{backupPath: filepath.Join(dir, "nonexistent.backup")}
	if err := u.CleanupBackup(); err != nil {
		t.Fatalf("CleanupBackup() error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// State helpers
// ---------------------------------------------------------------------------

func TestState_MarkFailed(t *testing.T) {
	s := &State{Status: StatusInstalling}
	s.MarkFailed(fmt.Errorf("disk full"))

	if s.Status != StatusFailed {
		t.Errorf("status = %q, want %q", s.Status, StatusFailed)
	}
	if s.Error != "disk full" {
		t.Errorf("error = %q, want %q", s.Error, "disk full")
	}
}

func TestState_MarkFailed_NilError(t *testing.T) {
	s := &State{Status: StatusInstalling}
	s.MarkFailed(nil)

	if s.Status != StatusFailed {
		t.Errorf("status = %q, want %q", s.Status, StatusFailed)
	}
	if s.Error != "" {
		t.Errorf("error = %q, want empty", s.Error)
	}
}

func TestState_MarkCompleted(t *testing.T) {
	s := &State{Status: StatusInstalling}
	s.MarkCompleted()

	if s.Status != StatusCompleted {
		t.Errorf("status = %q, want %q", s.Status, StatusCompleted)
	}
	if s.UpgradeCompleted.IsZero() {
		t.Error("UpgradeCompleted should be set")
	}
}

func TestState_MarkRolledBack(t *testing.T) {
	s := &State{Status: StatusFailed}
	s.MarkRolledBack()

	if s.Status != StatusRolledBack {
		t.Errorf("status = %q, want %q", s.Status, StatusRolledBack)
	}
	if s.UpgradeCompleted.IsZero() {
		t.Error("UpgradeCompleted should be set")
	}
}

func TestDefaultStatePath(t *testing.T) {
	path := DefaultStatePath()
	if path == "" {
		t.Error("DefaultStatePath() returned empty string")
	}
}

// ---------------------------------------------------------------------------
// VersionChecker
// ---------------------------------------------------------------------------

func TestVersionChecker_StartStop(t *testing.T) {
	vc := &VersionChecker{
		currentVersion: "1.0.0",
		checkInterval:  1 * time.Hour,
		stopCh:         make(chan struct{}),
		isHomebrew:     true, // skip real HTTP calls
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	vc.Start(ctx)

	// Double start should be no-op
	vc.Start(ctx)

	// Give goroutine time to start
	time.Sleep(50 * time.Millisecond)

	vc.Stop()

	// Double stop should be no-op
	vc.Stop()
}

func TestVersionChecker_GetLatestInfo(t *testing.T) {
	vc := &VersionChecker{
		currentVersion: "1.0.0",
		checkInterval:  1 * time.Hour,
		stopCh:         make(chan struct{}),
	}

	if info := vc.GetLatestInfo(); info != nil {
		t.Error("GetLatestInfo() should return nil initially")
	}

	vc.mu.Lock()
	vc.latestInfo = &VersionInfo{Current: "1.0.0", Latest: "v2.0.0"}
	vc.mu.Unlock()

	info := vc.GetLatestInfo()
	if info == nil {
		t.Fatal("GetLatestInfo() returned nil after setting")
	}
	if info.Latest != "v2.0.0" {
		t.Errorf("Latest = %q, want %q", info.Latest, "v2.0.0")
	}
}

func TestVersionChecker_LastCheck(t *testing.T) {
	vc := &VersionChecker{
		currentVersion: "1.0.0",
		stopCh:         make(chan struct{}),
	}

	if !vc.LastCheck().IsZero() {
		t.Error("LastCheck() should be zero initially")
	}
}

func TestVersionChecker_IsHomebrew(t *testing.T) {
	vc := &VersionChecker{isHomebrew: true, homebrewErr: fmt.Errorf("homebrew detected")}

	if !vc.IsHomebrew() {
		t.Error("IsHomebrew() = false, want true")
	}
	if vc.GetHomebrewError() == nil {
		t.Error("GetHomebrewError() = nil, want error")
	}
}

func TestVersionChecker_CheckNow_Homebrew(t *testing.T) {
	vc := &VersionChecker{
		isHomebrew:  true,
		homebrewErr: fmt.Errorf("homebrew installation"),
	}

	_, err := vc.CheckNow(context.Background())
	if err == nil {
		t.Fatal("CheckNow() expected error for homebrew, got nil")
	}
}

func TestVersionChecker_OnUpdate(t *testing.T) {
	vc := &VersionChecker{
		currentVersion: "1.0.0",
		stopCh:         make(chan struct{}),
	}

	called := false
	vc.OnUpdate(func(info *VersionInfo) {
		called = true
	})

	vc.mu.RLock()
	cb := vc.onUpdate
	vc.mu.RUnlock()
	if cb == nil {
		t.Fatal("onUpdate callback not set")
	}
	cb(&VersionInfo{})
	if !called {
		t.Error("callback was not called")
	}
}

func TestVersionChecker_ContextCancellation(t *testing.T) {
	vc := &VersionChecker{
		currentVersion: "1.0.0",
		checkInterval:  50 * time.Millisecond,
		stopCh:         make(chan struct{}),
		isHomebrew:     true, // skip real HTTP
	}

	ctx, cancel := context.WithCancel(context.Background())
	vc.Start(ctx)

	// Cancel context should stop the checker
	cancel()
	time.Sleep(100 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// GracefulUpgrader / HotUpgradeConfig defaults
// ---------------------------------------------------------------------------

func TestDefaultUpgradeOptions(t *testing.T) {
	opts := DefaultUpgradeOptions()
	if !opts.WaitForTasks {
		t.Error("WaitForTasks should default to true")
	}
	if opts.TaskTimeout != 5*time.Minute {
		t.Errorf("TaskTimeout = %v, want 5m", opts.TaskTimeout)
	}
	if opts.Force {
		t.Error("Force should default to false")
	}
}

func TestDefaultHotUpgradeConfig(t *testing.T) {
	cfg := DefaultHotUpgradeConfig()
	if !cfg.WaitForTasks {
		t.Error("WaitForTasks should default to true")
	}
	if cfg.TaskTimeout != 2*time.Minute {
		t.Errorf("TaskTimeout = %v, want 2m", cfg.TaskTimeout)
	}
}

// ---------------------------------------------------------------------------
// installFromZip — no binary
// ---------------------------------------------------------------------------

func TestInstallFromZip_NoBinary(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	zipPath := filepath.Join(dir, "test.zip")

	zipFile, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zipFile)
	w, err := zw.Create("README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("readme"))
	_ = zw.Close()
	_ = zipFile.Close()

	u := &Upgrader{binaryPath: binPath}
	err = u.installFromZip(zipPath)
	if err == nil {
		t.Fatal("installFromZip() expected error for missing binary, got nil")
	}
}

func TestInstallFromZip_InvalidFile(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "bad.zip")
	if err := os.WriteFile(zipPath, []byte("not a zip"), 0644); err != nil {
		t.Fatal(err)
	}

	u := &Upgrader{binaryPath: filepath.Join(dir, "pilot")}
	err := u.installFromZip(zipPath)
	if err == nil {
		t.Fatal("installFromZip() expected error for invalid zip, got nil")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func createTestTarGz(t *testing.T, path, entryName string, content []byte) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create tar.gz: %v", err)
	}
	defer func() { _ = f.Close() }()

	gzw := gzip.NewWriter(f)
	defer func() { _ = gzw.Close() }()

	tw := tar.NewWriter(gzw)
	defer func() { _ = tw.Close() }()

	hdr := &tar.Header{
		Name:     entryName,
		Mode:     0755,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("failed to write tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("failed to write tar content: %v", err)
	}
}
