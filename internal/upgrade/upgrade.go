// Package upgrade provides self-update functionality for Pilot.
// It supports checking for new versions, downloading updates,
// graceful task completion before upgrade, and automatic rollback.
package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// GitHubRepo is the GitHub repository for releases
	GitHubRepo = "alekspetrov/pilot"

	// DefaultTimeout for HTTP requests
	DefaultTimeout = 30 * time.Second

	// BackupSuffix for previous version
	BackupSuffix = ".backup"
)

// Release represents a GitHub release
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
	HTMLURL     string    `json:"html_url"`
}

// Asset represents a release asset
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	ContentType        string `json:"content_type"`
}

// VersionInfo contains current version information
type VersionInfo struct {
	Current       string
	Latest        string
	LatestRelease *Release
	UpdateAvail   bool
	ReleaseNotes  string
}

// Upgrader handles version checking and self-update
type Upgrader struct {
	currentVersion string
	httpClient     *http.Client
	binaryPath     string
	backupPath     string
}

// NewUpgrader creates a new Upgrader instance
func NewUpgrader(currentVersion string) (*Upgrader, error) {
	binaryPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("failed to get executable path: %w", err)
	}

	// Resolve symlinks
	resolvedPath, err := filepath.EvalSymlinks(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve symlinks: %w", err)
	}

	// Detect Homebrew installation
	if isHomebrewPath(resolvedPath) {
		return nil, fmt.Errorf("Homebrew installation detected at %s\n\n"+
			"Self-upgrade is not supported for Homebrew installations.\n"+
			"Please use Homebrew to upgrade:\n\n"+
			"  brew upgrade pilot\n\n"+
			"Or reinstall without Homebrew:\n\n"+
			"  brew uninstall pilot\n"+
			"  curl -fsSL https://raw.githubusercontent.com/alekspetrov/pilot/main/install.sh | bash",
			resolvedPath)
	}

	return &Upgrader{
		currentVersion: currentVersion,
		httpClient:     &http.Client{Timeout: DefaultTimeout},
		binaryPath:     resolvedPath,
		backupPath:     resolvedPath + BackupSuffix,
	}, nil
}

// isHomebrewPath checks if a path is inside a Homebrew installation
func isHomebrewPath(path string) bool {
	homebrewPrefixes := []string{
		"/opt/homebrew/Cellar/",
		"/usr/local/Cellar/",
		"/home/linuxbrew/.linuxbrew/Cellar/",
	}
	for _, prefix := range homebrewPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// CheckVersion checks if a newer version is available
func (u *Upgrader) CheckVersion(ctx context.Context) (*VersionInfo, error) {
	release, err := u.fetchLatestRelease(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch latest release: %w", err)
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(u.currentVersion, "v")

	info := &VersionInfo{
		Current:       u.currentVersion,
		Latest:        release.TagName,
		LatestRelease: release,
		UpdateAvail:   compareVersions(current, latest) < 0,
		ReleaseNotes:  release.Body,
	}

	return info, nil
}

// Upgrade downloads and installs the latest version
func (u *Upgrader) Upgrade(ctx context.Context, release *Release, onProgress func(pct int, msg string)) error {
	// Find appropriate asset for current platform
	asset := u.findAsset(release)
	if asset == nil {
		return fmt.Errorf("no release asset found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	if onProgress != nil {
		onProgress(0, "Downloading update...")
	}

	// Download to temp file
	tempFile, err := u.downloadAsset(ctx, asset, onProgress)
	if err != nil {
		return fmt.Errorf("failed to download update: %w", err)
	}
	defer func() { _ = os.Remove(tempFile) }()

	if onProgress != nil {
		onProgress(70, "Creating backup...")
	}

	// Backup current binary
	if err := u.createBackup(); err != nil {
		return fmt.Errorf("failed to create backup: %w", err)
	}

	if onProgress != nil {
		onProgress(80, "Installing update...")
	}

	// Install new binary
	if err := u.installBinary(tempFile); err != nil {
		// Attempt rollback
		if rollbackErr := u.Rollback(); rollbackErr != nil {
			return fmt.Errorf("install failed: %w; rollback also failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("install failed (rolled back): %w", err)
	}

	if onProgress != nil {
		onProgress(100, "Update complete!")
	}

	return nil
}

// Rollback restores the previous version
func (u *Upgrader) Rollback() error {
	if _, err := os.Stat(u.backupPath); os.IsNotExist(err) {
		return fmt.Errorf("no backup found at %s", u.backupPath)
	}

	// Remove current binary
	if err := os.Remove(u.binaryPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove current binary: %w", err)
	}

	// Restore backup
	if err := os.Rename(u.backupPath, u.binaryPath); err != nil {
		return fmt.Errorf("failed to restore backup: %w", err)
	}

	return nil
}

// CleanupBackup removes the backup file
func (u *Upgrader) CleanupBackup() error {
	if err := os.Remove(u.backupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove backup: %w", err)
	}
	return nil
}

// HasBackup checks if a backup exists
func (u *Upgrader) HasBackup() bool {
	_, err := os.Stat(u.backupPath)
	return err == nil
}

// BinaryPath returns the path to the current binary
func (u *Upgrader) BinaryPath() string {
	return u.binaryPath
}

// fetchLatestRelease fetches the latest release from GitHub
func (u *Upgrader) fetchLatestRelease(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", GitHubRepo)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no releases found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to parse release: %w", err)
	}

	return &release, nil
}

// findAsset finds the appropriate release asset for the current platform
func (u *Upgrader) findAsset(release *Release) *Asset {
	// Expected asset name format: pilot-{os}-{arch}.tar.gz
	expectedName := fmt.Sprintf("pilot-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)

	for i := range release.Assets {
		if release.Assets[i].Name == expectedName {
			return &release.Assets[i]
		}
	}

	// Try without extension
	expectedBinary := fmt.Sprintf("pilot-%s-%s", runtime.GOOS, runtime.GOARCH)
	for i := range release.Assets {
		if release.Assets[i].Name == expectedBinary {
			return &release.Assets[i]
		}
	}

	return nil
}

// downloadAsset downloads a release asset to a temp file
func (u *Upgrader) downloadAsset(ctx context.Context, asset *Asset, onProgress func(pct int, msg string)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", asset.BrowserDownloadURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Create temp file
	tempFile, err := os.CreateTemp("", "pilot-update-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}

	tempFileName := tempFile.Name()
	closeFile := func() {
		_ = tempFile.Close()
	}

	// Download with progress tracking
	written := int64(0)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := tempFile.Write(buf[:n]); writeErr != nil {
				closeFile()
				_ = os.Remove(tempFileName)
				return "", fmt.Errorf("failed to write: %w", writeErr)
			}
			written += int64(n)
			if onProgress != nil && asset.Size > 0 {
				pct := int(float64(written) / float64(asset.Size) * 60) // Scale to 60% of progress
				onProgress(pct, fmt.Sprintf("Downloading... %d%%", pct))
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			closeFile()
			_ = os.Remove(tempFileName)
			return "", fmt.Errorf("failed to read: %w", readErr)
		}
	}

	closeFile()
	return tempFileName, nil
}

// createBackup creates a backup of the current binary
func (u *Upgrader) createBackup() error {
	// Remove existing backup if any
	if err := os.Remove(u.backupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove old backup: %w", err)
	}

	// Copy current binary to backup
	src, err := os.Open(u.binaryPath)
	if err != nil {
		return fmt.Errorf("failed to open current binary: %w", err)
	}
	defer func() { _ = src.Close() }()

	dst, err := os.OpenFile(u.backupPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("failed to create backup file: %w", err)
	}
	defer func() { _ = dst.Close() }()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("failed to copy binary: %w", err)
	}

	return nil
}

// installBinary installs the new binary from a downloaded file
func (u *Upgrader) installBinary(downloadPath string) error {
	// Check if it's a tarball
	if strings.HasSuffix(downloadPath, ".tar.gz") || u.isTarGz(downloadPath) {
		return u.installFromTarGz(downloadPath)
	}

	// Direct binary
	return u.installDirectBinary(downloadPath)
}

// isTarGz checks if a file is a gzipped tarball
func (u *Upgrader) isTarGz(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	// Check magic bytes for gzip
	buf := make([]byte, 2)
	if _, err := f.Read(buf); err != nil {
		return false
	}
	return buf[0] == 0x1f && buf[1] == 0x8b
}

// installFromTarGz extracts and installs from a tarball
func (u *Upgrader) installFromTarGz(tarPath string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer func() { _ = gzr.Close() }()

	tr := tar.NewReader(gzr)

	// Find the binary in the tarball
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("binary not found in archive")
		}
		if err != nil {
			return fmt.Errorf("failed to read tarball: %w", err)
		}

		// Look for the pilot binary
		if header.Typeflag == tar.TypeReg &&
			(header.Name == "pilot" || filepath.Base(header.Name) == "pilot") {

			// Extract to binary path
			out, err := os.OpenFile(u.binaryPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
			if err != nil {
				return fmt.Errorf("failed to create binary: %w", err)
			}

			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()

			if copyErr != nil {
				return fmt.Errorf("failed to extract binary: %w", copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("failed to close binary: %w", closeErr)
			}

			return nil
		}
	}
}

// installDirectBinary installs a direct binary file
func (u *Upgrader) installDirectBinary(srcPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	dst, err := os.OpenFile(u.binaryPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("failed to create binary: %w", err)
	}
	defer func() { _ = dst.Close() }()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("failed to copy binary: %w", err)
	}

	return nil
}

// compareVersions compares two semantic versions
// Returns -1 if a < b, 0 if a == b, 1 if a > b
func compareVersions(a, b string) int {
	aParts := parseVersion(a)
	bParts := parseVersion(b)

	for i := 0; i < 3; i++ {
		if aParts[i] < bParts[i] {
			return -1
		}
		if aParts[i] > bParts[i] {
			return 1
		}
	}
	return 0
}

// parseVersion parses a version string into [major, minor, patch]
func parseVersion(v string) [3]int {
	var parts [3]int
	v = strings.TrimPrefix(v, "v")

	// Handle dirty suffix
	v = strings.Split(v, "-")[0]

	_, _ = fmt.Sscanf(v, "%d.%d.%d", &parts[0], &parts[1], &parts[2])
	return parts
}
