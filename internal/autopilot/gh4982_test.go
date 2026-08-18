package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
	"github.com/robfig/cron/v3"
)

// TestDecideTrainRecovery covers decideTrainRecovery's four boot/tick/release
// orderings (GH-4982 acceptance criteria), anchored to the live incident's
// own timestamps: a daemon restart at 2026-08-18 13:40:57Z ran boot-time
// recovery before that day's 14:00Z scheduled tick and cut a release anyway
// (the "early fire" defect); a later restart at 15:12:16Z found the tick had
// genuinely passed with nothing released for it but didn't recover (the
// "missed recovery" defect) because a nearby-but-earlier release apparently
// satisfied whatever gated it.
func TestDecideTrainRecovery(t *testing.T) {
	scheduledAt := time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC) // 16:00 Europe/Berlin daily tick

	tests := []struct {
		name          string
		now           time.Time
		lastReleaseAt time.Time
		wantFire      bool
	}{
		{
			name:          "boot before tick: scheduled time still in the future — no recovery",
			now:           time.Date(2026, 8, 18, 13, 40, 57, 0, time.UTC), // restart #1, 15:40 Berlin
			lastReleaseAt: time.Time{},
			wantFire:      false,
		},
		{
			name:          "boot after tick with pre-tick release: last release predates the tick — recovery fires",
			now:           time.Date(2026, 8, 18, 15, 12, 16, 0, time.UTC), // restart #2, 17:12 Berlin
			lastReleaseAt: time.Date(2026, 8, 18, 13, 45, 7, 0, time.UTC),  // tag cut 15min before the tick
			wantFire:      true,
		},
		{
			name:          "boot after tick with post-tick release: last release already covers the tick — no recovery",
			now:           time.Date(2026, 8, 18, 15, 12, 16, 0, time.UTC),
			lastReleaseAt: scheduledAt.Add(15 * time.Minute),
			wantFire:      false,
		},
		{
			name:          "boot after tick, no releases at all: recovery fires",
			now:           time.Date(2026, 8, 18, 15, 12, 16, 0, time.UTC),
			lastReleaseAt: time.Time{},
			wantFire:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideTrainRecovery(tt.now, scheduledAt, tt.lastReleaseAt)
			if got.Fire != tt.wantFire {
				t.Errorf("decideTrainRecovery() fire = %v (reason %q), want fire = %v", got.Fire, got.Reason, tt.wantFire)
			}
			if got.Reason == "" {
				t.Error("decideTrainRecovery() reason is empty — a recovery decision must log its reasoning (GH-4982)")
			}
		})
	}
}

// trainRecoveryServer builds a fake GitHub server for exercising
// recoverMissedTrainTick's last-release-time gate end to end (GH-4982).
// /releases/latest and /releases/tags/<lastTag> both serve a Release
// timestamped at releaseAt, or both 404 when hasRelease is false (simulating
// "no releases at all"). /tags is always empty so the release object — not a
// bare tag — is the GetCurrentVersionWithSource baseline winner, keeping
// GetLastReleaseTime's tag-name lookup deterministic. downstreamHits counts
// any request past the gate itself: CompareCommits (the tagged-repo tick
// path) or ListPullRequests?state=closed (the no-tag first-release path) —
// a proxy for "the gate fired and the tick actually ran".
func trainRecoveryServer(t *testing.T, lastTag string, releaseAt time.Time, hasRelease bool, downstreamHits *int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"), strings.HasSuffix(r.URL.Path, "/releases/tags/"+lastTag):
			if !hasRelease {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: lastTag, CreatedAt: releaseAt, PublishedAt: releaseAt})
		case strings.HasSuffix(r.URL.Path, "/tags"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/compare/"):
			atomic.AddInt64(downstreamHits, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"commits": []}`))
		case strings.HasSuffix(r.URL.Path, "/pulls") && r.URL.Query().Get("state") == "closed":
			atomic.AddInt64(downstreamHits, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
}

// TestRecoverMissedTrainTick_LastReleaseGate is the end-to-end companion to
// TestDecideTrainRecovery: it drives recoverMissedTrainTick itself (real
// schedule, real state store, fake GitHub server) through the three
// release-timing orderings that require an actual past tick — "boot before
// tick" is covered separately since previousScheduledTime never hands
// recoverMissedTrainTick a future scheduledAt to begin with (GH-4982).
func TestRecoverMissedTrainTick_LastReleaseGate(t *testing.T) {
	loc := time.UTC
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse("0 21 * * FRI")
	if err != nil {
		t.Fatalf("parse schedule: %v", err)
	}
	prevScheduled := previousScheduledTime(schedule, time.Now().In(loc))

	tests := []struct {
		name       string
		hasRelease bool
		releaseAt  time.Time
		wantFire   bool
	}{
		{
			name:       "boot after tick with pre-tick release: recovery fires",
			hasRelease: true,
			releaseAt:  prevScheduled.Add(-1 * time.Hour),
			wantFire:   true,
		},
		{
			name:       "boot after tick with post-tick release: recovery does not fire",
			hasRelease: true,
			releaseAt:  prevScheduled.Add(1 * time.Hour),
			wantFire:   false,
		},
		{
			name:       "boot after tick, no releases at all: recovery fires",
			hasRelease: false,
			wantFire:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var downstreamHits int64
			server := trainRecoveryServer(t, "v1.0.0", tt.releaseAt, tt.hasRelease, &downstreamHits)
			defer server.Close()

			c, _ := newScheduleController(t, server.URL, "0 21 * * FRI")
			rel := c.resolvedRelease()
			rel.ScopeLookback = 7 * 24 * time.Hour

			c.recoverMissedTrainTick(context.Background(), rel, schedule, loc)

			gotFire := atomic.LoadInt64(&downstreamHits) > 0
			if gotFire != tt.wantFire {
				t.Errorf("downstream tick fired = %v, want %v (downstream_hits=%d)", gotFire, tt.wantFire, downstreamHits)
			}
		})
	}
}
