package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// syncAlertSink is a mutex-protected alertSink recorder, used instead of the
// plain fakeAlertSink (controller_test.go) wherever a background goroutine
// (scheduleReleaseTickWithRetry's retry loop) may call ProcessEvent
// concurrently with a test goroutine reading the recorded events.
type syncAlertSink struct {
	mu     sync.Mutex
	events []alerts.Event
}

func (s *syncAlertSink) ProcessEvent(e alerts.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *syncAlertSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func (s *syncAlertSink) first() alerts.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events[0]
}

// withShortRetryTiming shrinks the package-level retry backoff bounds for
// the duration of a test, restoring the production defaults on cleanup.
// GH-4476: the real bounds are 15-30 minutes over a 6h window, which a unit
// test can't wait out — these vars exist specifically to be overridden here.
func withShortRetryTiming(t *testing.T, minInterval, maxInterval, window time.Duration) {
	t.Helper()
	origMin, origMax, origWindow := releaseTickRetryMinInterval, releaseTickRetryMaxInterval, releaseTickRetryWindow
	releaseTickRetryMinInterval = minInterval
	releaseTickRetryMaxInterval = maxInterval
	releaseTickRetryWindow = window
	t.Cleanup(func() {
		releaseTickRetryMinInterval = origMin
		releaseTickRetryMaxInterval = origMax
		releaseTickRetryWindow = origWindow
	})
}

// rateLimitResponse writes a 403 response shaped like a GitHub primary
// rate-limit hit (X-RateLimit-Remaining: 0), matching the studio-sdk client's
// RateLimitError classification (GH-4476 repro: the 07-18 16:00 tick hit
// exactly this response and the train skipped the day with no retry).
func rateLimitResponse(w http.ResponseWriter) {
	w.Header().Set("X-RateLimit-Remaining", "0")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for user ID 123."}`))
}

// TestScheduleReleaseTickWithRetry_SucceedsOnLaterRetry covers the GH-4476
// acceptance criterion: a simulated 403 on the first tick attempt must not
// forfeit the day — a later retry within the window succeeds and enqueues
// the train, and no exhausted-retries alert fires.
func TestScheduleReleaseTickWithRetry_SucceedsOnLaterRetry(t *testing.T) {
	withShortRetryTiming(t, 5*time.Millisecond, 10*time.Millisecond, 2*time.Second)

	c1 := makeCommit("feat: add thing (#101)")
	c1.SHA = "sha1"

	var releaseLatestCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			if atomic.AddInt32(&releaseLatestCalls, 1) == 1 {
				rateLimitResponse(w)
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.0.0"})
		case strings.HasSuffix(r.URL.Path, "/tags"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/compare/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"commits": []*github.Commit{c1}})
		case strings.Contains(r.URL.Path, "/pulls/"):
			var num int
			_, _ = fmtSscanIssueNum(strings.Replace(r.URL.Path, "/pulls/", "/issues/", 1), &num)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.PullRequest{Number: num, Merged: true})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	c, stateStore := newScheduleController(t, server.URL, "0 21 * * FRI")
	sink := &syncAlertSink{}
	c.SetAlertsEngine(sink)

	scheduledAt := time.Now()
	c.scheduleReleaseTickWithRetry(context.Background(), scheduledAt)

	scopeKey := trainScopeKey(scheduledAt)
	deadline := time.Now().Add(2 * time.Second)
	var row *ScopeRelease
	for time.Now().Before(deadline) {
		var err error
		row, err = stateStore.GetScopeRelease("owner/repo", scopeKey)
		if err != nil {
			t.Fatalf("GetScopeRelease failed: %v", err)
		}
		if row != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if row == nil {
		t.Fatal("expected the train to be enqueued once the retry succeeded")
	}
	if calls := atomic.LoadInt32(&releaseLatestCalls); calls < 2 {
		t.Errorf("expected at least 2 calls to /releases/latest (initial failure + retry), got %d", calls)
	}
	if n := sink.count(); n != 0 {
		t.Errorf("expected no release_tick_failed alert when the retry eventually succeeds, got %d alert(s)", n)
	}
}

// TestScheduleReleaseTickWithRetry_ExhaustsRetriesFiresAlert covers the
// second GH-4476 acceptance criterion: a tick that fails every attempt for
// the whole retry window must give up loudly — ERROR log plus an alert via
// the alerts engine — instead of silently vanishing.
func TestScheduleReleaseTickWithRetry_ExhaustsRetriesFiresAlert(t *testing.T) {
	withShortRetryTiming(t, 5*time.Millisecond, 5*time.Millisecond, 40*time.Millisecond)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rateLimitResponse(w)
	}))
	defer server.Close()

	c, stateStore := newScheduleController(t, server.URL, "0 21 * * FRI")
	sink := &syncAlertSink{}
	c.SetAlertsEngine(sink)

	scheduledAt := time.Now()
	c.scheduleReleaseTickWithRetry(context.Background(), scheduledAt)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sink.count() == 0 {
		time.Sleep(5 * time.Millisecond)
	}

	if n := sink.count(); n != 1 {
		t.Fatalf("expected exactly 1 release_tick_failed alert, got %d", n)
	}
	event := sink.first()
	if event.Type != alerts.EventType("release_tick_failed") {
		t.Errorf("alert type = %q, want release_tick_failed", event.Type)
	}
	if event.Metadata["repo"] != "owner/repo" {
		t.Errorf("alert repo metadata = %q, want owner/repo", event.Metadata["repo"])
	}

	rows, err := stateStore.ListScopeReleases("owner/repo", "pending", "releasing", "done", "failed")
	if err != nil {
		t.Fatalf("ListScopeReleases failed: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected no train row to be enqueued for a tick that never succeeded, got %d", len(rows))
	}
}

// TestScheduleReleaseTickWithRetry_NonRetryableSkipDoesNotSpawnRetry covers
// the negative case: a legitimate no-op (nothing merged yet) must not spawn
// a retry loop or fire any alert — retrying can't produce merged PRs that
// don't exist, and treating a quiet day as a failure would be alert noise.
func TestScheduleReleaseTickWithRetry_NonRetryableSkipDoesNotSpawnRetry(t *testing.T) {
	withShortRetryTiming(t, 5*time.Millisecond, 5*time.Millisecond, 40*time.Millisecond)

	server := noTagScheduleTickServer(t, nil, nil)
	defer server.Close()

	c, _ := newScheduleController(t, server.URL, "0 21 * * FRI")
	sink := &syncAlertSink{}
	c.SetAlertsEngine(sink)

	scheduledAt := time.Now()
	c.scheduleReleaseTickWithRetry(context.Background(), scheduledAt)

	// Give any (incorrectly) spawned retry goroutine a chance to misbehave.
	time.Sleep(80 * time.Millisecond)

	if n := sink.count(); n != 0 {
		t.Errorf("expected no alert for a legitimate skip (no merged PRs yet), got %d", n)
	}
}
