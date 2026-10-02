package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// stageDeadlineFixture builds a controller tracking PRs 1 and 2, both served
// as open by a fake GitHub, with a short StageTimeout.
func stageDeadlineFixture(t *testing.T, timeout time.Duration) *Controller {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		const prefix = "/repos/owner/repo/pulls/"
		if rest, ok := strings.CutPrefix(r.URL.Path, prefix); ok && !strings.Contains(rest, "/") {
			n, _ := strconv.Atoi(rest)
			_ = json.NewEncoder(w).Encode(map[string]any{"number": n, "state": "open"})
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)

	cfg := DefaultConfig()
	cfg.StageTimeout = timeout
	cfg.ReviewFeedback = nil
	c := NewController(cfg, github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL), nil, "owner", "repo")

	c.mu.Lock()
	for _, n := range []int{1, 2} {
		c.activePRs[n] = &PRState{PRNumber: n, IssueNumber: 100 + n, BranchName: "pilot/GH-" + strconv.Itoa(100+n), HeadSHA: "abc1234", Stage: StageReleasing}
	}
	c.mu.Unlock()
	return c
}

// runTick runs one processAllPRs tick and fails the test if it does not
// return within limit — the failure mode GH-5541 removes: one blocked stage
// handler holding the whole tick.
func runTick(t *testing.T, c *Controller, limit time.Duration, what string) {
	t.Helper()
	tickDone := make(chan struct{})
	go func() {
		defer close(tickDone)
		c.processAllPRs(context.Background())
	}()
	select {
	case <-tickDone:
	case <-time.After(limit):
		t.Fatalf("%s: processAllPRs still blocked after %v — a stage handler held the whole tick (no per-stage deadline)", what, limit)
	}
}

// TestProcessAllPRs_StageDeadline_BlockedHandlerDoesNotStallLoop is the GH-5541
// acceptance test: PR 1's stage handler blocks (ignoring ctx, and holding
// pr.mu exactly like ProcessPR does) past the deadline; the same tick must
// still process PR 2, record the timeout metric, skip PR 1 on the next tick
// without re-entering it, and resume it once the handler returns.
//
// Without the per-stage deadline the first runTick blocks on PR 1's handler and
// fails at its own limit.
func TestProcessAllPRs_StageDeadline_BlockedHandlerDoesNotStallLoop(t *testing.T) {
	c := stageDeadlineFixture(t, 150*time.Millisecond)

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseHandler)

	var mu sync.Mutex
	calls := map[int]int{}
	c.processPRFn = func(_ context.Context, n int, _ *github.PullRequest) error {
		mu.Lock()
		calls[n]++
		mu.Unlock()
		if n == 1 {
			live, _ := c.GetPRState(n)
			live.mu.Lock() // ProcessPR holds pr.mu for its whole body
			defer live.mu.Unlock()
			<-release // deliberately ignores ctx: an uncooperative handler
		}
		return nil
	}
	count := func(n int) int {
		mu.Lock()
		defer mu.Unlock()
		return calls[n]
	}

	start := time.Now()
	runTick(t, c, 10*time.Second, "tick 1")
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("tick 1 returned in %v, before the stage deadline — PR 1's handler did not actually block", elapsed)
	}
	if count(2) != 1 {
		t.Fatalf("PR 2 processed %d times in the same tick as the blocked PR 1, want 1", count(2))
	}
	if got := c.Metrics().Snapshot().StageTimeouts[string(StageReleasing)]; got != 1 {
		t.Fatalf("StageTimeouts[releasing] = %d, want 1", got)
	}
	if c.Metrics().Snapshot().LastTickAt.IsZero() {
		t.Fatal("LastTickAt not stamped by the tick")
	}

	// Tick 2: PR 1's handler is still running (holding pr.mu). The loop must
	// neither block on it (GetActivePRs, pr.mu) nor start a second pass.
	runTick(t, c, 10*time.Second, "tick 2")
	if count(1) != 1 {
		t.Fatalf("PR 1 handler entered %d times while the first was still running, want 1", count(1))
	}
	if count(2) != 2 {
		t.Fatalf("PR 2 processed %d times across two ticks, want 2", count(2))
	}
	if got := c.Metrics().Snapshot().StageTimeouts[string(StageReleasing)]; got != 1 {
		t.Fatalf("StageTimeouts[releasing] = %d after skipped tick, want still 1", got)
	}

	// Handler returns: PR 1 keeps its stage and is retried.
	releaseHandler()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.RLock()
		_, busy := c.stageInflight[1]
		c.mu.RUnlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("abandoned handler never cleared from stageInflight")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if pr, ok := c.GetPRState(1); !ok || pr.Stage != StageReleasing {
		t.Fatalf("PR 1 lost its stage after a timeout: %+v", pr)
	}
	runTick(t, c, 10*time.Second, "tick 3")
	if count(1) != 2 {
		t.Fatalf("PR 1 handler entered %d times after release, want 2 (retried next tick)", count(1))
	}
}

func TestConfig_EffectiveStageTimeout(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want time.Duration
	}{
		{"default config", DefaultConfig(), 5 * time.Minute},
		{"unset", &Config{}, 5 * time.Minute},
		{"negative", &Config{StageTimeout: -time.Second}, 5 * time.Minute},
		{"explicit", &Config{StageTimeout: 90 * time.Second}, 90 * time.Second},
		{"nil", nil, 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectiveStageTimeout(); got != tt.want {
				t.Errorf("EffectiveStageTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTickStaleness(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name      string
		lastTick  time.Time
		poll      time.Duration
		stage     time.Duration
		wantStale bool
		wantLimit float64
	}{
		{"never ticked is not stale", time.Time{}, 30 * time.Second, 0, false, 180},
		{"fresh", now.Add(-10 * time.Second), 30 * time.Second, 0, false, 180},
		{"idle backoff floor: 30s config, 2m old", now.Add(-2 * time.Minute), 30 * time.Second, 0, false, 180},
		{"older than 3x floor", now.Add(-4 * time.Minute), 30 * time.Second, 0, true, 180},
		{"long poll interval scales", now.Add(-4 * time.Minute), 2 * time.Minute, 0, false, 360},
		{"57 minute freeze", now.Add(-57 * time.Minute), 30 * time.Second, 0, true, 180},
		// GH-5547: a full default stage_timeout (5m) must not read as a stale tick.
		{"default stage timeout: 5m old is not stale", now.Add(-5 * time.Minute), 30 * time.Second, 5 * time.Minute, false, 360},
		{"default stage timeout: past stage+interval is stale", now.Add(-7 * time.Minute), 30 * time.Second, 5 * time.Minute, true, 360},
		{"short stage timeout keeps 3x floor", now.Add(-2 * time.Minute), 30 * time.Second, 30 * time.Second, false, 180},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, limit, stale := tickStaleness(tt.lastTick, tt.poll, tt.stage, now)
			if stale != tt.wantStale || limit != tt.wantLimit {
				t.Errorf("tickStaleness = (limit %v, stale %v), want (limit %v, stale %v)", limit, stale, tt.wantLimit, tt.wantStale)
			}
		})
	}
}

// TestMetricsAlerter_StaleTickFiresWarning drives the real alert engine: a
// controller whose loop last ticked 10 minutes ago raises the warning; a fresh
// tick does not.
func TestMetricsAlerter_StaleTickFiresWarning(t *testing.T) {
	config := &alerts.AlertConfig{
		Enabled:  true,
		Channels: []alerts.ChannelConfig{{Name: "capture", Type: "webhook", Enabled: true}},
		Rules: []alerts.AlertRule{
			{Name: "autopilot_tick_stale", Type: alerts.AlertTypeTickStale, Enabled: true, Severity: alerts.SeverityWarning},
		},
	}
	capture := alertCapture{alerts: make(chan *alerts.Alert, 4)}
	dispatcher := alerts.NewDispatcher(config)
	dispatcher.RegisterChannel(capture)
	engine := alerts.NewEngine(config, alerts.WithDispatcher(dispatcher))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}

	c := NewController(DefaultConfig(), github.NewClient(testutil.FakeGitHubToken), nil, "owner", "repo")
	ma := NewMetricsAlerter(c, engine)

	c.Metrics().RecordTick(time.Now())
	ma.evaluate()
	select {
	case a := <-capture.alerts:
		t.Fatalf("fresh tick raised %q", a.Message)
	case <-time.After(300 * time.Millisecond):
	}

	c.Metrics().RecordTick(time.Now().Add(-10 * time.Minute))
	ma.evaluate()
	select {
	case a := <-capture.alerts:
		if a.Severity != alerts.SeverityWarning {
			t.Errorf("severity = %v, want warning", a.Severity)
		}
		if !strings.Contains(a.Message, "has not ticked") {
			t.Errorf("unexpected message %q", a.Message)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stale tick raised no alert")
	}
}

// TestProcessPRWithDeadline_CancelAfterSendKeepsStageTimeoutZero pins the
// GH-5559 ordering in the stage handler goroutine: guard-delete → send on done
// → cancel(). If cancel() ran before the send, the waiting loop could wake on
// stageCtx.Done() while done was still empty and record a false stage timeout
// for a handler that had in fact finished. Many instant handlers across
// parallel workers make that window overwhelmingly likely to be hit, so the
// metric must stay at exactly zero.
func TestProcessPRWithDeadline_CancelAfterSendKeepsStageTimeoutZero(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StageTimeout = time.Minute // never legitimately expires
	cfg.ReviewFeedback = nil
	c := NewController(cfg, github.NewClient(testutil.FakeGitHubToken), nil, "owner", "repo")

	const workers, perWorker = 8, 1000
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(prNumber int) {
			defer wg.Done()
			// Not in activePRs: processOnePR returns immediately, so the
			// handler completes as fast as possible.
			for i := 0; i < perWorker; i++ {
				c.processPRWithDeadline(context.Background(), &PRState{PRNumber: prNumber, Stage: StageReleasing})
			}
		}(1000 + w)
	}
	wg.Wait()

	if got := c.Metrics().Snapshot().StageTimeouts[string(StageReleasing)]; got != 0 {
		t.Fatalf("StageTimeouts[releasing] = %d after %d instant handlers, want 0 — cancel() ran before the done send",
			got, workers*perWorker)
	}
}
