package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newChecksumServer serves the asset at /asset and the given checksums body at
// /checksums.txt.
func newChecksumServer(t *testing.T, assetBody []byte, checksums string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(assetBody)
	})
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestUpgrader_verifyAssetChecksum(t *testing.T) {
	assetBody := []byte("release-archive-bytes")
	assetName := "pilot-linux-amd64.tar.gz"
	good := sha256Hex(assetBody)
	bad := sha256Hex([]byte("something else"))

	tests := []struct {
		name         string
		checksums    string
		withChecksum bool
		wantErr      string
	}{
		{
			name:         "Match",
			checksums:    fmt.Sprintf("%s  other.tar.gz\n%s  %s\n", bad, strings.ToUpper(good), assetName),
			withChecksum: true,
		},
		{
			name:         "Mismatch",
			checksums:    fmt.Sprintf("%s  %s\n", bad, assetName),
			withChecksum: true,
			wantErr:      "mismatch",
		},
		{
			name:         "MissingChecksumsAssetSkips",
			withChecksum: false,
		},
		{
			name:         "MalformedLineErrors",
			checksums:    fmt.Sprintf("not-a-checksum-line\n%s  %s\n", good, assetName),
			withChecksum: true,
			wantErr:      "malformed",
		},
		{
			name:         "MissingEntryErrors",
			checksums:    fmt.Sprintf("%s  other.tar.gz\n", good),
			withChecksum: true,
			wantErr:      "no checksum entry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newChecksumServer(t, assetBody, tt.checksums)
			u := &Upgrader{httpClient: srv.Client()}

			asset := Asset{Name: assetName, BrowserDownloadURL: srv.URL + "/asset"}
			release := &Release{TagName: "v2.0.0", Assets: []Asset{asset}}
			if tt.withChecksum {
				release.Assets = append(release.Assets, Asset{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"})
			}

			path, err := u.downloadAsset(context.Background(), &release.Assets[0], nil)
			if err != nil {
				t.Fatalf("downloadAsset: %v", err)
			}
			defer func() { _ = os.Remove(path) }()

			err = u.verifyAssetChecksum(context.Background(), release, &release.Assets[0], path)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("verifyAssetChecksum() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("verifyAssetChecksum() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestFindChecksumsAsset(t *testing.T) {
	if findChecksumsAsset(&Release{Assets: []Asset{{Name: "a"}}}) != nil {
		t.Error("expected nil when no checksums.txt asset")
	}
	r := &Release{Assets: []Asset{{Name: "a"}, {Name: "checksums.txt"}}}
	if got := findChecksumsAsset(r); got == nil || got.Name != "checksums.txt" {
		t.Errorf("findChecksumsAsset() = %v, want checksums.txt", got)
	}
}

func TestUpgrader_Upgrade_ChecksumMismatchAbortsBeforeBackup(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	original := []byte("original-binary")
	if err := os.WriteFile(binPath, original, 0755); err != nil {
		t.Fatal(err)
	}

	assetName := fmt.Sprintf("pilot-%s-%s", runtime.GOOS, runtime.GOARCH)
	srv := newChecksumServer(t, []byte("tampered-binary"),
		fmt.Sprintf("%s  %s\n", sha256Hex([]byte("genuine-binary")), assetName))

	u := &Upgrader{
		currentVersion:      "1.0.0",
		httpClient:          srv.Client(),
		binaryPath:          binPath,
		backupPath:          binPath + BackupSuffix,
		prepareForExecution: func(string) error { return nil },
	}
	release := &Release{
		TagName: "v2.0.0",
		Assets: []Asset{
			{Name: assetName, BrowserDownloadURL: srv.URL + "/asset"},
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
		},
	}

	err := u.Upgrade(context.Background(), release, nil)
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("Upgrade() error = %v, want checksum mismatch", err)
	}
	if _, statErr := os.Stat(binPath + BackupSuffix); !os.IsNotExist(statErr) {
		t.Errorf("backup file exists after checksum failure (stat err = %v)", statErr)
	}
	got, readErr := os.ReadFile(binPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("binary modified after checksum failure: %q", got)
	}
}
