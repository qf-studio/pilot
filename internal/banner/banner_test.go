package banner

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = w

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("failed to close pipe writer: %v", err)
	}
	os.Stdout = orig

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read captured stdout: %v", err)
	}
	return string(out)
}

// GH-4393: the startup banner must surface the fully resolved DB path so a
// config path silently bypassing a storage shim is visible immediately
// instead of surfacing hours later as a split-brain ledger.
func TestStartupBannerLogsResolvedDBPath(t *testing.T) {
	const dbPath = "/var/lib/pilot/pilot-home/data/pilot.db"

	out := captureStdout(t, func() {
		StartupBanner("v9.9.9", "http://127.0.0.1:9090", dbPath)
	})

	if !strings.Contains(out, dbPath) {
		t.Errorf("StartupBanner output missing resolved DB path %q\noutput:\n%s", dbPath, out)
	}
}

func TestStartupTelegramLogsResolvedDBPath(t *testing.T) {
	const dbPath = "/var/lib/pilot/pilot-home/data/pilot.db"
	cfg := config.DefaultConfig()

	out := captureStdout(t, func() {
		StartupTelegram("v9.9.9", "/repo/path", "", dbPath, cfg)
	})

	if !strings.Contains(out, dbPath) {
		t.Errorf("StartupTelegram output missing resolved DB path %q\noutput:\n%s", dbPath, out)
	}
}
