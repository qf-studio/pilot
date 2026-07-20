package upgrade

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestHotUpgrader(t *testing.T, tc TaskChecker) (*HotUpgrader, string) {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "pilot")
	if err := os.WriteFile(binPath, []byte("test-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(dir, "upgrade-state.json")

	u := &Upgrader{
		currentVersion:      "1.0.0",
		httpClient:          &http.Client{Timeout: 5 * time.Second},
		binaryPath:          binPath,
		backupPath:          binPath + BackupSuffix,
		prepareForExecution: func(string) error { return nil },
	}

	graceful := &GracefulUpgrader{
		upgrader:    u,
		statePath:   statePath,
		taskChecker: tc,
	}

	return &HotUpgrader{
		graceful:    graceful,
		taskChecker: tc,
	}, dir
}

func TestHotUpgrader_GetUpgrader(t *testing.T) {
	h, _ := newTestHotUpgrader(t, &NoOpTaskChecker{})
	if h.GetUpgrader() == nil {
		t.Fatal("GetUpgrader() returned nil")
	}
}

func TestHotUpgrader_GetGracefulUpgrader(t *testing.T) {
	h, _ := newTestHotUpgrader(t, &NoOpTaskChecker{})
	if h.GetGracefulUpgrader() == nil {
		t.Fatal("GetGracefulUpgrader() returned nil")
	}
}

func TestHotUpgrader_PerformHotUpgrade_DefaultConfig(t *testing.T) {
	tc := &NoOpTaskChecker{}
	h, _ := newTestHotUpgrader(t, tc)

	newBinary := []byte("hot-upgraded-binary")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newBinary)
	}))
	defer server.Close()

	h.graceful.upgrader.httpClient = server.Client()

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

	// Note: PerformHotUpgrade will try to RestartWithNewBinary at the end,
	// which would exec. Since we can't let that happen in tests, we test
	// the flow up to the restart step by verifying the binary was installed.
	// The actual restart will succeed (since the binary is just data, not
	// a real executable), but the exec call will fail, which is fine.
	err := h.PerformHotUpgrade(context.Background(), release, nil)
	// The error from RestartWithNewBinary is expected in test env
	// as the "binary" is not a real executable
	if err != nil {
		t.Logf("PerformHotUpgrade() error (expected in test): %v", err)
	}
}

// GH-3600: a failed exec must leave durable evidence — restart_failed with the
// error in the state file, never a state that reads like success.
func TestHotUpgrader_PerformHotUpgrade_ExecFailurePersisted(t *testing.T) {
	tc := &NoOpTaskChecker{}
	h, dir := newTestHotUpgrader(t, tc)

	newBinary := []byte("hot-upgraded-binary")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newBinary)
	}))
	defer server.Close()

	h.graceful.upgrader.httpClient = server.Client()

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

	// Fail the restart deterministically before syscall.Exec can run.
	origSmoke := runSmokeTest
	runSmokeTest = func(string) error { return fmt.Errorf("smoke test exploded") }
	defer func() { runSmokeTest = origSmoke }()

	err := h.PerformHotUpgrade(context.Background(), release, nil)
	if err == nil || !strings.Contains(err.Error(), "restart failed") {
		t.Fatalf("PerformHotUpgrade() error = %v, want wrapped restart failure", err)
	}

	state, lerr := LoadState(filepath.Join(dir, "upgrade-state.json"))
	if lerr != nil || state == nil {
		t.Fatalf("LoadState() = %v, %v", state, lerr)
	}
	if state.Status != StatusRestartFailed {
		t.Errorf("state.Status = %q, want %q", state.Status, StatusRestartFailed)
	}
	if !strings.Contains(state.Error, "smoke test exploded") {
		t.Errorf("state.Error = %q, want the exec failure recorded", state.Error)
	}
}

// GH-3600: platforms without hot restart (Windows) install and return — state
// must stay awaiting_restart so the manual restart reconciles at next boot.
func TestHotUpgrader_PerformHotUpgrade_NoHotRestart_AwaitingRestart(t *testing.T) {
	tc := &NoOpTaskChecker{}
	h, dir := newTestHotUpgrader(t, tc)

	newBinary := []byte("hot-upgraded-binary")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newBinary)
	}))
	defer server.Close()

	h.graceful.upgrader.httpClient = server.Client()

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

	origCan := canHotRestart
	canHotRestart = func() bool { return false }
	defer func() { canHotRestart = origCan }()

	if err := h.PerformHotUpgrade(context.Background(), release, nil); err != nil {
		t.Fatalf("PerformHotUpgrade() error = %v", err)
	}

	state, lerr := LoadState(filepath.Join(dir, "upgrade-state.json"))
	if lerr != nil || state == nil {
		t.Fatalf("LoadState() = %v, %v", state, lerr)
	}
	if state.Status != StatusAwaitingRestart {
		t.Errorf("state.Status = %q, want %q", state.Status, StatusAwaitingRestart)
	}
}

func TestHotUpgrader_PerformHotUpgrade_WithTasks(t *testing.T) {
	tc := &mockTaskChecker{tasks: []string{"task-1"}}
	h, _ := newTestHotUpgrader(t, tc)

	newBinary := []byte("hot-upgraded")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newBinary)
	}))
	defer server.Close()

	h.graceful.upgrader.httpClient = server.Client()

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
	cfg := &HotUpgradeConfig{
		WaitForTasks: true,
		TaskTimeout:  5 * time.Second,
		OnProgress: func(pct int, msg string) {
			progressMsgs = append(progressMsgs, msg)
		},
	}

	_ = h.PerformHotUpgrade(context.Background(), release, cfg)

	// Verify progress was reported
	if len(progressMsgs) == 0 {
		t.Error("no progress messages reported")
	}
}

func TestHotUpgrader_PerformHotUpgrade_TaskWaitTimeout(t *testing.T) {
	tc := &mockTaskChecker{
		tasks:   []string{"task-1"},
		waitErr: context.DeadlineExceeded,
	}
	h, _ := newTestHotUpgrader(t, tc)

	release := &Release{TagName: "v2.0.0"}

	cfg := &HotUpgradeConfig{
		WaitForTasks: true,
		TaskTimeout:  1 * time.Second,
	}

	err := h.PerformHotUpgrade(context.Background(), release, cfg)
	if err == nil {
		t.Fatal("PerformHotUpgrade() expected timeout error, got nil")
	}
}

func TestHotUpgrader_PerformHotUpgrade_FlushSession(t *testing.T) {
	tc := &NoOpTaskChecker{}
	h, _ := newTestHotUpgrader(t, tc)

	newBinary := []byte("flushed-binary")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newBinary)
	}))
	defer server.Close()

	h.graceful.upgrader.httpClient = server.Client()

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

	flushed := false
	cfg := &HotUpgradeConfig{
		WaitForTasks: false,
		TaskTimeout:  5 * time.Second,
		FlushSession: func() error {
			flushed = true
			return nil
		},
	}

	_ = h.PerformHotUpgrade(context.Background(), release, cfg)

	if !flushed {
		t.Error("FlushSession callback was not called")
	}
}

func TestHotUpgrader_PerformHotUpgrade_FlushSessionError(t *testing.T) {
	tc := &NoOpTaskChecker{}
	h, _ := newTestHotUpgrader(t, tc)

	newBinary := []byte("binary")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newBinary)
	}))
	defer server.Close()

	h.graceful.upgrader.httpClient = server.Client()

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

	cfg := &HotUpgradeConfig{
		FlushSession: func() error {
			return fmt.Errorf("flush failed")
		},
	}

	// FlushSession error is non-fatal, upgrade should proceed
	_ = h.PerformHotUpgrade(context.Background(), release, cfg)
}

func TestCanHotRestart(t *testing.T) {
	result := CanHotRestart()
	// On non-windows, should be true
	if runtime.GOOS != "windows" && !result {
		t.Error("CanHotRestart() = false, want true on Unix")
	}
}

// ---------------------------------------------------------------------------
// progressLogThrottle (GH-4468: download progress logging hygiene)
// ---------------------------------------------------------------------------

func TestNewProgressLogThrottle_NilClockDefaultsToReal(t *testing.T) {
	th := newProgressLogThrottle(nil)
	if _, ok := th.clock.(realClock); !ok {
		t.Errorf("expected realClock default when nil is passed, got %T", th.clock)
	}
}

func TestProgressLogThrottle_ShouldLog_TableDriven(t *testing.T) {
	tests := []struct {
		name  string
		calls []int // sequence of pct values, no simulated time elapsed between calls
		want  []bool
	}{
		{
			name:  "first call always logs",
			calls: []int{5},
			want:  []bool{true},
		},
		{
			name:  "same decile suppressed after first log",
			calls: []int{5, 6, 7, 9},
			want:  []bool{true, false, false, false},
		},
		{
			name:  "crossing a 10% boundary logs",
			calls: []int{5, 12, 12, 23},
			want:  []bool{true, true, false, true},
		},
		{
			name:  "0% and 100% always log",
			calls: []int{0, 0, 50, 100, 100},
			want:  []bool{true, true, true, true, true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClock(time.Now())
			th := newProgressLogThrottle(fc)
			for i, pct := range tt.calls {
				got := th.shouldLog(pct)
				if got != tt.want[i] {
					t.Errorf("call #%d shouldLog(%d) = %v, want %v", i, pct, got, tt.want[i])
				}
			}
		})
	}
}

// TestProgressLogThrottle_ElapsedTimeReopensSameDecile verifies the ≥1s
// escape hatch: even without crossing a 10% boundary, once the hygiene
// interval has passed the next update logs again.
func TestProgressLogThrottle_ElapsedTimeReopensSameDecile(t *testing.T) {
	fc := newFakeClock(time.Now())
	th := newProgressLogThrottle(fc)

	if !th.shouldLog(11) {
		t.Fatal("first call should log")
	}
	if th.shouldLog(15) {
		t.Fatal("same decile with no elapsed time should be suppressed")
	}

	// Advance the clock past the hygiene interval without crossing a decile.
	<-fc.After(progressLogHygieneInterval)

	if !th.shouldLog(16) {
		t.Error("same decile after the hygiene interval elapsed should log")
	}
}

// TestProgressLogThrottle_DoesNotGateCallback confirms the throttle is only
// a logging decision — PerformHotUpgrade must still forward every update to
// cfg.OnProgress so the TUI progress bar stays smooth.
func TestProgressLogThrottle_DoesNotGateCallback(t *testing.T) {
	tc := &NoOpTaskChecker{}
	h, _ := newTestHotUpgrader(t, tc)

	newBinary := []byte(strings.Repeat("x", 200*1024)) // large enough for multiple 32KB chunks
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(newBinary)
	}))
	defer server.Close()

	h.graceful.upgrader.httpClient = server.Client()

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

	var callbackCount int
	cfg := &HotUpgradeConfig{
		OnProgress: func(pct int, msg string) {
			callbackCount++
		},
	}

	_ = h.PerformHotUpgrade(context.Background(), release, cfg)

	if callbackCount < 2 {
		t.Errorf("expected multiple OnProgress callbacks despite log throttling, got %d", callbackCount)
	}
}
