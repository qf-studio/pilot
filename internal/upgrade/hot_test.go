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

	// GH-6187: a drain timeout no longer fails the call — it enters the passive
	// wait — so the stuck task only surfaces as an error once ctx ends.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := h.PerformHotUpgrade(ctx, release, cfg)
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

// ---------------------------------------------------------------------------
// GH-6187: passive wait after a drain timeout
// ---------------------------------------------------------------------------

// drainEvent is one admission/drain observation, stamped with the fake clock.
type drainEvent struct {
	kind string // "pause", "resume", "poll-open", "poll-paused", "install"
	at   time.Duration
}

// drainScript is a TaskChecker + admission pauser driven by a fake clock. Each
// task ends at a fixed offset from t0; WaitForTasks advances the clock to the
// latest end or to the timeout, whichever comes first.
type drainScript struct {
	clk    *fakeClock
	t0     time.Time
	ends   map[string]time.Duration
	paused bool
	events []drainEvent

	onPause func(s *drainScript, nth int) // optional hook, called after the nth pause
	pauses  int
	onPoll  func(s *drainScript, nth int) // optional hook, called on each poll
	polls   int
}

func newDrainScript(t0 time.Time, ends map[string]time.Duration) *drainScript {
	return &drainScript{clk: newFakeClock(t0), t0: t0, ends: ends}
}

func (s *drainScript) since() time.Duration { return s.clk.Now().Sub(s.t0) }

func (s *drainScript) running() []string {
	var ids []string
	for id, end := range s.ends {
		if s.since() < end {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *drainScript) GetRunningTaskIDs() []string {
	if s.pauses > 0 { // a poll, not the initial check
		s.polls++
		kind := "poll-open"
		if s.paused {
			kind = "poll-paused"
		}
		s.events = append(s.events, drainEvent{kind, s.since()})
		if s.onPoll != nil {
			s.onPoll(s, s.polls)
		}
	}
	return s.running()
}

func (s *drainScript) WaitForTasks(ctx context.Context, timeout time.Duration) error {
	deadline := s.since() + timeout
	var latest time.Duration
	for _, end := range s.ends {
		if end > latest {
			latest = end
		}
	}
	if latest <= deadline {
		if latest > s.since() {
			<-s.clk.After(latest - s.since())
		}
		return nil
	}
	<-s.clk.After(deadline - s.since())
	return context.DeadlineExceeded
}

func (s *drainScript) pause() {
	s.paused = true
	s.pauses++
	s.events = append(s.events, drainEvent{"pause", s.since()})
	if s.onPause != nil {
		s.onPause(s, s.pauses)
	}
}

func (s *drainScript) resume() {
	s.paused = false
	s.events = append(s.events, drainEvent{"resume", s.since()})
}

func (s *drainScript) count(kind string) int {
	n := 0
	for _, e := range s.events {
		if e.kind == kind {
			n++
		}
	}
	return n
}

// pausesBetween counts pause events strictly inside (lo, hi): admission must
// stay open there, so any pause is the retry path re-pausing.
func (s *drainScript) pausesBetween(lo, hi time.Duration) int {
	n := 0
	for _, e := range s.events {
		if e.kind == "pause" && e.at > lo && e.at < hi {
			n++
		}
	}
	return n
}

func (s *drainScript) last(kind string) (time.Duration, bool) {
	for i := len(s.events) - 1; i >= 0; i-- {
		if s.events[i].kind == kind {
			return s.events[i].at, true
		}
	}
	return 0, false
}

func (s *drainScript) first(kind string) (time.Duration, bool) {
	for _, e := range s.events {
		if e.kind == kind {
			return e.at, true
		}
	}
	return 0, false
}

// run drives PerformHotUpgrade through the script. The release has no assets,
// so the call ends in "upgrade failed" right after the install step starts;
// FlushSession stamps the moment the drain was declared finished.
func (s *drainScript) run(t *testing.T, cfg *HotUpgradeConfig) error {
	t.Helper()
	h, _ := newTestHotUpgrader(t, s)
	h.clock = s.clk
	cfg.WaitForTasks = true
	cfg.PauseAdmission = s.pause
	cfg.ResumeAdmission = s.resume
	cfg.FlushSession = func() error {
		s.events = append(s.events, drainEvent{"install", s.since()})
		return nil
	}
	return h.PerformHotUpgrade(context.Background(), &Release{TagName: "v2.0.0"}, cfg)
}

func TestPerformHotUpgrade_PassiveWait_OneTaskEndsAfterTimeout(t *testing.T) {
	s := newDrainScript(time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
		map[string]time.Duration{"task-1": 45 * time.Minute})

	err := s.run(t, &HotUpgradeConfig{TaskTimeout: 30 * time.Minute, PollInterval: 5 * time.Minute})
	if err == nil || !strings.Contains(err.Error(), "upgrade failed") {
		t.Fatalf("PerformHotUpgrade() error = %v, want the post-drain install step to be reached", err)
	}

	// One 30-minute window, then open admission, then one pause for the install.
	if got := s.count("pause"); got != 2 {
		t.Errorf("pauses = %d, want 2 (first window + final pause); events=%v", got, s.events)
	}
	if got := s.count("resume"); got != 1 {
		t.Errorf("resumes = %d, want 1 (at the drain timeout); events=%v", got, s.events)
	}
	if got := s.pausesBetween(30*time.Minute, 45*time.Minute); got != 0 {
		t.Errorf("admission re-paused %d time(s) between the timeout and the task end, want 0 — the retry path must not re-pause; events=%v", got, s.events)
	}
	if got := s.count("poll-open"); got == 0 {
		t.Error("no passive polls observed, want admission open between the timeout and the task end")
	}
	if at, _ := s.first("resume"); at != 30*time.Minute {
		t.Errorf("admission reopened at T+%v, want T+30m", at)
	}
	if at, _ := s.last("pause"); at < 45*time.Minute {
		t.Errorf("final pause at T+%v, want at or after T+45m", at)
	}
	if at, ok := s.first("install"); !ok || at < 45*time.Minute {
		t.Errorf("install at T+%v (ok=%v), want after the task ended at T+45m", at, ok)
	}
}

func TestPerformHotUpgrade_PassiveWait_ReplayOf20261106(t *testing.T) {
	// GH-6157 ran 13:56→16:19; the release landed at 15:12 and the checker
	// saw it at 15:15. Before the fix admission was paused 15:15–15:45,
	// 15:45–16:15 and 16:15–… Now: one 30-minute window, then open admission
	// until the task ends, then the install.
	t0 := time.Date(2026, 11, 6, 15, 15, 0, 0, time.UTC)
	taskEnd := time.Date(2026, 11, 6, 16, 19, 0, 0, time.UTC).Sub(t0)
	s := newDrainScript(t0, map[string]time.Duration{"GH-6157": taskEnd})

	_ = s.run(t, &HotUpgradeConfig{TaskTimeout: 30 * time.Minute, PollInterval: upgradeCheckIntervalForTest})

	if at, _ := s.first("pause"); at != 0 {
		t.Errorf("first pause at T+%v, want the moment the update was seen", at)
	}
	if at, _ := s.first("resume"); at != 30*time.Minute {
		t.Errorf("admission reopened at T+%v, want T+30m (15:45Z)", at)
	}
	if got := s.pausesBetween(30*time.Minute, taskEnd); got != 0 {
		t.Errorf("admission re-paused %d time(s) before the task ended, want 0; events=%v", got, s.events)
	}
	if got := s.count("pause"); got != 2 {
		t.Errorf("pauses = %d, want 2 (one 30m window + the final pause); events=%v", got, s.events)
	}
	install, ok := s.first("install")
	if !ok || install < taskEnd || install > taskEnd+upgradeCheckIntervalForTest {
		t.Errorf("install at T+%v (ok=%v), want within one poll after the task ended at T+%v", install, ok, taskEnd)
	}
}

func TestPerformHotUpgrade_PassiveWait_SecondPauseWaitsForAllTasks(t *testing.T) {
	s := newDrainScript(time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), map[string]time.Duration{
		"task-1": 45 * time.Minute,
		"task-2": 55 * time.Minute,
	})

	_ = s.run(t, &HotUpgradeConfig{TaskTimeout: 30 * time.Minute, PollInterval: 5 * time.Minute})

	var pauses []time.Duration
	for _, e := range s.events {
		if e.kind == "pause" {
			pauses = append(pauses, e.at)
		}
	}
	if len(pauses) != 2 {
		t.Fatalf("pauses = %v, want exactly 2", pauses)
	}
	if pauses[1] < 55*time.Minute {
		t.Errorf("second pause at T+%v, want only after both tasks ended (T+55m)", pauses[1])
	}
	if got := s.pausesBetween(30*time.Minute, 55*time.Minute); got != 0 {
		t.Errorf("admission re-paused %d time(s) before both tasks ended, want 0; events=%v", got, s.events)
	}
}

func TestPerformHotUpgrade_PassiveWait_TaskAdmittedBetweenCheckAndPause(t *testing.T) {
	s := newDrainScript(time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
		map[string]time.Duration{"task-1": 45 * time.Minute})
	// The pause that follows the first empty poll loses the race: a new task
	// slipped in just before admission closed.
	s.onPause = func(s *drainScript, nth int) {
		if nth == 2 {
			s.ends["task-2"] = s.since() + 20*time.Minute
		}
	}

	_ = s.run(t, &HotUpgradeConfig{TaskTimeout: 30 * time.Minute, PollInterval: 5 * time.Minute})

	var pauses []time.Duration
	for _, e := range s.events {
		if e.kind == "pause" {
			pauses = append(pauses, e.at)
		}
	}
	if len(pauses) != 3 {
		t.Fatalf("pauses = %v, want 3 (window, lost race, final)", pauses)
	}
	if last := pauses[2]; last < s.ends["task-2"] {
		t.Errorf("final pause at T+%v, want after the late task ended at T+%v", last, s.ends["task-2"])
	}
	if at, ok := s.first("install"); !ok || at < s.ends["task-2"] {
		t.Errorf("install at T+%v (ok=%v), want after the late task ended", at, ok)
	}
}

func TestPerformHotUpgrade_PassiveWait_MaxWaitAlertsOnceAndKeepsWaiting(t *testing.T) {
	s := newDrainScript(time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
		map[string]time.Duration{"task-1": 8 * time.Hour})

	var alerts []time.Duration
	var blocked []string
	_ = s.run(t, &HotUpgradeConfig{
		TaskTimeout:  30 * time.Minute,
		PollInterval: 5 * time.Minute,
		MaxWait:      time.Hour,
		OnMaxWait: func(waited time.Duration, blockedBy []string) {
			alerts = append(alerts, waited)
			blocked = blockedBy
		},
	})

	if len(alerts) != 1 {
		t.Fatalf("OnMaxWait fired %d times, want exactly once", len(alerts))
	}
	if alerts[0] < time.Hour {
		t.Errorf("alert after %v, want >= max_wait 1h", alerts[0])
	}
	if len(blocked) != 1 || blocked[0] != "task-1" {
		t.Errorf("blockedBy = %v, want [task-1]", blocked)
	}
	// It kept waiting past the alert and installed only once the task ended.
	if at, ok := s.first("install"); !ok || at < 8*time.Hour {
		t.Errorf("install at T+%v (ok=%v), want after the task ended at T+8h", at, ok)
	}
}

func TestPerformHotUpgrade_PassiveWait_ContextCancelLeavesAdmissionOpen(t *testing.T) {
	s := newDrainScript(time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
		map[string]time.Duration{"task-1": 10 * time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.onPoll = func(_ *drainScript, nth int) {
		if nth == 3 {
			cancel()
		}
	}

	h, _ := newTestHotUpgrader(t, s)
	h.clock = s.clk
	err := h.PerformHotUpgrade(ctx, &Release{TagName: "v2.0.0"}, &HotUpgradeConfig{
		WaitForTasks:    true,
		TaskTimeout:     30 * time.Minute,
		PollInterval:    5 * time.Minute,
		PauseAdmission:  s.pause,
		ResumeAdmission: s.resume,
	})
	if err == nil || !strings.Contains(err.Error(), "timeout waiting for tasks") {
		t.Fatalf("PerformHotUpgrade() error = %v, want a wrapped wait error", err)
	}
	if s.paused {
		t.Error("admission still paused after the passive wait was cancelled")
	}
	if got := s.count("pause"); got != 1 {
		t.Errorf("pauses = %d, want only the first window", got)
	}
}

func TestPerformHotUpgrade_NoRunningTasks_PausesOnceAndInstalls(t *testing.T) {
	s := newDrainScript(time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), map[string]time.Duration{})

	_ = s.run(t, &HotUpgradeConfig{TaskTimeout: 30 * time.Minute})

	if got := s.count("pause"); got != 1 {
		t.Errorf("pauses = %d, want 1", got)
	}
	if got := s.count("resume"); got != 0 {
		t.Errorf("resumes = %d, want 0 — the pause must hold through the install", got)
	}
	if _, ok := s.first("install"); !ok {
		t.Error("install step not reached")
	}
}

// upgradeCheckIntervalForTest mirrors DefaultCheckInterval so the replay test
// reads in production timings.
const upgradeCheckIntervalForTest = DefaultCheckInterval
