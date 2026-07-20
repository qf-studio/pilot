package upgrade

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// checkBinaryWritable / ErrBinaryNotWritable (GH-4468)
// ---------------------------------------------------------------------------

func TestCheckBinaryWritable_WritableDir(t *testing.T) {
	dir := t.TempDir()

	if err := checkBinaryWritable(dir); err != nil {
		t.Fatalf("checkBinaryWritable() error = %v, want nil for a writable dir", err)
	}

	// The probe file must not be left behind.
	if _, err := os.Stat(filepath.Join(dir, upgradeProbeFilename)); !os.IsNotExist(err) {
		t.Errorf("probe file was not cleaned up (stat err = %v)", err)
	}
}

func TestCheckBinaryWritable_UnwritableDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root — permission checks are bypassed")
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0755) }() // let t.TempDir() clean up

	err := checkBinaryWritable(dir)
	if err == nil {
		t.Fatal("checkBinaryWritable() error = nil, want error for an unwritable dir")
	}

	var notWritable *ErrBinaryNotWritable
	if !errors.As(err, &notWritable) {
		t.Fatalf("expected *ErrBinaryNotWritable, got %T: %v", err, err)
	}
	if notWritable.Dir != dir {
		t.Errorf("Dir = %q, want %q", notWritable.Dir, dir)
	}
	if notWritable.UID != os.Getuid() {
		t.Errorf("UID = %d, want %d", notWritable.UID, os.Getuid())
	}
	if notWritable.Err == nil {
		t.Error("Err should wrap the underlying OS error")
	}
}

func TestErrBinaryNotWritable_Error(t *testing.T) {
	err := &ErrBinaryNotWritable{Dir: "/usr/local/bin", UID: 1000, Err: os.ErrPermission}
	msg := err.Error()

	for _, want := range []string{"/usr/local/bin", "1000", "relocate"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
}

func TestErrBinaryNotWritable_Unwrap(t *testing.T) {
	underlying := os.ErrPermission
	err := &ErrBinaryNotWritable{Dir: "/usr/local/bin", UID: 1000, Err: underlying}

	if !errors.Is(err, os.ErrPermission) {
		t.Error("errors.Is(err, os.ErrPermission) = false, want true (Unwrap should expose Err)")
	}
}

// ---------------------------------------------------------------------------
// Upgrade() preflight integration: must fail before any network call
// ---------------------------------------------------------------------------

func TestUpgrade_PreflightBlocksBeforeDownload(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root — permission checks are bypassed")
	}

	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	if err := os.WriteFile(binPath, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0755) }()

	downloadHit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloadHit = true
		_, _ = w.Write([]byte("new-binary"))
	}))
	defer server.Close()

	u := &Upgrader{
		currentVersion:      "1.0.0",
		httpClient:          server.Client(),
		binaryPath:          binPath,
		backupPath:          binPath + BackupSuffix,
		prepareForExecution: func(string) error { return nil },
	}

	release := &Release{
		TagName: "v2.0.0",
		Assets: []Asset{
			{
				Name:               fmt.Sprintf("pilot-%s-%s", runtime.GOOS, runtime.GOARCH),
				BrowserDownloadURL: server.URL + "/pilot",
				Size:               10,
			},
		},
	}

	err := u.Upgrade(context.Background(), release, nil)
	if err == nil {
		t.Fatal("Upgrade() error = nil, want *ErrBinaryNotWritable")
	}

	var notWritable *ErrBinaryNotWritable
	if !errors.As(err, &notWritable) {
		t.Fatalf("expected *ErrBinaryNotWritable, got %T: %v", err, err)
	}
	if notWritable.Dir != dir {
		t.Errorf("Dir = %q, want %q", notWritable.Dir, dir)
	}

	if downloadHit {
		t.Error("Upgrade() hit the download server despite an unwritable binary dir — preflight should abort first")
	}
}

// TestUpgrade_PreflightPassesForWritableDir is the control case: with a
// writable dir, Upgrade() proceeds past the preflight and completes normally
// (mirrors TestUpgrade_EndToEnd but asserts specifically that the new
// preflight step doesn't regress the happy path).
func TestUpgrade_PreflightPassesForWritableDir(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	if err := os.WriteFile(binPath, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	newBinary := []byte("new-binary-v2")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newBinary)
	}))
	defer server.Close()

	u := &Upgrader{
		currentVersion:      "1.0.0",
		httpClient:          server.Client(),
		binaryPath:          binPath,
		backupPath:          binPath + BackupSuffix,
		prepareForExecution: func(string) error { return nil },
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

	if err := u.Upgrade(context.Background(), release, nil); err != nil {
		t.Fatalf("Upgrade() error = %v, want nil for a writable dir", err)
	}

	got, _ := os.ReadFile(binPath)
	if string(got) != string(newBinary) {
		t.Errorf("installed content = %q, want %q", got, newBinary)
	}
}
