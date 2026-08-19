package autopilot

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// TestRecoverMissedTrainTick_LogsOneLinePerVerdict is the log-hygiene
// companion to TestRecoverMissedTrainTick (GH-4982) and
// TestRecoverMissedTrainTick_LastReleaseGate: post-merge review of PR #4984
// plus box-log verification (2026-08-19) found that recoverMissedTrainTick's
// skip paths were either silent (row-exists) or logged at Debug (lookback,
// last-release) — invisible at the daemon's Info level — while the
// "recovering missed train" WARN was mistakenly believed to fire before any
// gate ran. That combination is exactly what produced the #4982
// misdiagnosis: a genuinely-correct row-exists skip on 08-18 15:11Z read as
// "recovery never ran" because nothing logged the reason.
//
// GH-4989 requires: (1) the WARN never appears unless a recovery release is
// actually attempted, and (2) every skip path (lookback, row-exists,
// last-release) emits one Info-or-louder line naming the gate and its
// reason. This test drives all four verdicts end to end and asserts the log
// side of each.
func TestRecoverMissedTrainTick_LogsOneLinePerVerdict(t *testing.T) {
	loc := time.UTC
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse("0 21 * * FRI")
	if err != nil {
		t.Fatalf("parse schedule: %v", err)
	}

	t.Run("lookback gate skip logs at Info with the gate name, never the WARN", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}))
		defer server.Close()

		c, logs := newScheduleControllerWithLogCapture(t, server.URL, "0 21 * * FRI")
		rel := c.resolvedRelease()
		rel.ScopeLookback = 1 * time.Millisecond // any past scheduled slot is older than this

		c.recoverMissedTrainTick(context.Background(), rel, schedule, loc)

		got := logs.String()
		if !strings.Contains(got, "level=INFO") || !strings.Contains(got, "gate=lookback") {
			t.Errorf("expected an Info-level log naming the lookback gate, got:\n%s", got)
		}
		if strings.Contains(got, "recovering missed train") {
			t.Errorf("WARN \"recovering missed train\" must not fire on a lookback skip, got:\n%s", got)
		}
	})

	t.Run("row-exists gate skip logs at Info with row state and tag, never the WARN", func(t *testing.T) {
		var compareHits int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/compare/") {
				compareHits++
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}))
		defer server.Close()

		c, logs := newScheduleControllerWithLogCapture(t, server.URL, "0 21 * * FRI")
		rel := c.resolvedRelease()
		rel.ScopeLookback = 7 * 24 * time.Hour

		prevScheduled := previousScheduledTime(schedule, time.Now().In(loc))
		if err := c.stateStore.EnqueueScopeRelease("owner/repo", trainScopeKey(prevScheduled), "Release train existing", []int{1}); err != nil {
			t.Fatalf("EnqueueScopeRelease: %v", err)
		}

		c.recoverMissedTrainTick(context.Background(), rel, schedule, loc)

		got := logs.String()
		if !strings.Contains(got, "level=INFO") || !strings.Contains(got, "gate=row-exists") {
			t.Errorf("expected an Info-level log naming the row-exists gate, got:\n%s", got)
		}
		if !strings.Contains(got, "row_state=pending") {
			t.Errorf("expected the row-exists skip to log the row's state, got:\n%s", got)
		}
		if strings.Contains(got, "recovering missed train") {
			t.Errorf("WARN \"recovering missed train\" must not fire on a row-exists skip, got:\n%s", got)
		}
		if compareHits != 0 {
			t.Errorf("expected no downstream compare-commits call, got %d", compareHits)
		}
	})

	t.Run("last-release gate skip logs at Info with the gate name, never the WARN", func(t *testing.T) {
		prevScheduled := previousScheduledTime(schedule, time.Now().In(loc))

		var downstreamHits int64
		server := trainRecoveryServer(t, "v1.0.0", prevScheduled.Add(1*time.Hour), true, &downstreamHits)
		defer server.Close()

		c, logs := newScheduleControllerWithLogCapture(t, server.URL, "0 21 * * FRI")
		rel := c.resolvedRelease()
		rel.ScopeLookback = 7 * 24 * time.Hour

		c.recoverMissedTrainTick(context.Background(), rel, schedule, loc)

		got := logs.String()
		if !strings.Contains(got, "level=INFO") || !strings.Contains(got, "gate=last-release") {
			t.Errorf("expected an Info-level log naming the last-release gate, got:\n%s", got)
		}
		if strings.Contains(got, "recovering missed train") {
			t.Errorf("WARN \"recovering missed train\" must not fire on a last-release skip, got:\n%s", got)
		}
	})

	t.Run("recovery actually fires: the WARN appears exactly once", func(t *testing.T) {
		server := scheduleTickServer(t, "v1.0.0", nil, nil)
		defer server.Close()

		c, logs := newScheduleControllerWithLogCapture(t, server.URL, "0 21 * * FRI")
		rel := c.resolvedRelease()
		rel.ScopeLookback = 7 * 24 * time.Hour

		c.recoverMissedTrainTick(context.Background(), rel, schedule, loc)

		got := logs.String()
		if strings.Count(got, "recovering missed train") != 1 {
			t.Errorf("expected exactly one \"recovering missed train\" WARN when recovery actually fires, got:\n%s", got)
		}
	})
}

// newScheduleControllerWithLogCapture is newScheduleController plus a
// buffered slog.Logger so tests can assert on recoverMissedTrainTick's
// per-verdict log lines (GH-4989).
func newScheduleControllerWithLogCapture(t *testing.T, serverURL, schedule string) (*Controller, *bytes.Buffer) {
	t.Helper()
	c, _ := newScheduleController(t, serverURL, schedule)
	var buf bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return c, &buf
}
