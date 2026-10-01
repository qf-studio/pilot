package autopilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// waitStageIdle waits until the abandoned handler for prNumber has returned.
func waitStageIdle(t *testing.T, c *Controller, prNumber int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.RLock()
		_, busy := c.stageInflight[prNumber]
		c.mu.RUnlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stage handler for PR %d never returned after its context expired", prNumber)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestProcessAllPRs_StageTimeoutIsNotAPRFailure is the GH-5547 acceptance test.
// PR 1 sits in StageReleasing; its first GitHub call (the tag lookup) blocks on
// the request context, so every tick ends with the real ProcessPR returning
// the stage context's DeadlineExceeded. Three such ticks must not open the
// per-PR circuit breaker (MaxFailures=3), must not spend a releasing attempt,
// and the PR must be processed normally on the following tick.
//
// Without the ctx-error exemption in ProcessPR the third timeout opens the
// breaker, ReleasingAttempts reads 3, and the fourth tick is refused.
func TestProcessAllPRs_StageTimeoutIsNotAPRFailure(t *testing.T) {
	var block atomic.Bool
	block.Store(true)
	var unblockedTagCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		const prefix = "/repos/owner/repo/pulls/"
		if rest, ok := strings.CutPrefix(r.URL.Path, prefix); ok && !strings.Contains(rest, "/") {
			n, _ := strconv.Atoi(rest)
			_ = json.NewEncoder(w).Encode(map[string]any{"number": n, "state": "open"})
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags") {
			if block.Load() {
				select {
				case <-r.Context().Done():
				case <-time.After(10 * time.Second):
				}
				return
			}
			unblockedTagCalls.Add(1)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)

	cfg := DefaultConfig()
	cfg.StageTimeout = 150 * time.Millisecond
	cfg.MaxFailures = 3
	cfg.ReviewFeedback = nil
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", TagPrefix: "v"}
	c := NewController(cfg, github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL), nil, "owner", "repo")
	if c.releaser == nil {
		t.Fatal("releaser not initialized")
	}
	c.mu.Lock()
	c.activePRs[1] = &PRState{PRNumber: 1, IssueNumber: 101, BranchName: "pilot/GH-101", HeadSHA: "abc1234", Stage: StageReleasing}
	c.mu.Unlock()

	for i := 1; i <= 3; i++ {
		runTick(t, c, 10*time.Second, fmt.Sprintf("timeout tick %d", i))
		waitStageIdle(t, c, 1)

		if got := c.Metrics().Snapshot().StageTimeouts[string(StageReleasing)]; got != int64(i) {
			t.Fatalf("after tick %d: StageTimeouts[releasing] = %d, want %d", i, got, i)
		}
		if c.isPRCircuitOpen(1) {
			t.Fatalf("after tick %d: per-PR circuit breaker open — a stage timeout was counted as a PR failure", i)
		}
		pr, _ := c.GetPRState(1)
		if pr.Stage != StageReleasing {
			t.Fatalf("after tick %d: stage = %s, want %s", i, pr.Stage, StageReleasing)
		}
		if pr.ReleasingAttempts != 0 {
			t.Fatalf("after tick %d: ReleasingAttempts = %d, want 0 — a timed-out call is not an attempt", i, pr.ReleasingAttempts)
		}
		if pr.Error != "" {
			t.Fatalf("after tick %d: PR error recorded for a timeout: %q", i, pr.Error)
		}
	}

	c.mu.RLock()
	failures := c.prFailures[1]
	c.mu.RUnlock()
	if failures != nil && failures.FailureCount != 0 {
		t.Fatalf("FailureCount = %d after three stage timeouts, want 0", failures.FailureCount)
	}

	// Following tick: GitHub is healthy again; the PR must actually be processed.
	block.Store(false)
	runTick(t, c, 10*time.Second, "recovery tick")
	waitStageIdle(t, c, 1)
	if unblockedTagCalls.Load() == 0 {
		t.Fatal("recovery tick never reached the releasing handler — PR was refused (circuit open?)")
	}
	if got := c.Metrics().Snapshot().CircuitBreakerTrips; got != 0 {
		t.Fatalf("CircuitBreakerTrips = %d, want 0", got)
	}
}

// TestIsStageCtxError pins the exemption's boundaries: only the stage context's
// own expiry/cancellation counts, not an unrelated inner deadline.
func TestIsStageCtxError(t *testing.T) {
	expired, cancelExpired := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelExpired()
	<-expired.Done()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()

	wrapped := fmt.Errorf("failed to check existing tags: %w", context.DeadlineExceeded)
	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"nil error", expired, nil, false},
		{"deadline on expired ctx", expired, context.DeadlineExceeded, true},
		{"wrapped deadline on expired ctx", expired, wrapped, true},
		{"canceled on cancelled ctx", cancelled, context.Canceled, true},
		{"inner deadline while stage ctx still live", live, wrapped, false},
		{"unrelated error on expired ctx", expired, errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStageCtxError(tt.ctx, tt.err); got != tt.want {
				t.Errorf("isStageCtxError = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCheckReleasingRetryOrEscalate_StageCtxDoesNotEscalate: at the retry cap a
// stage-deadline error must not flip the PR to failed.
func TestCheckReleasingRetryOrEscalate_StageCtxDoesNotEscalate(t *testing.T) {
	c := NewController(DefaultConfig(), github.NewClient(testutil.FakeGitHubToken), nil, "owner", "repo")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pr := &PRState{PRNumber: 1, Stage: StageReleasing, ReleasingAttempts: c.config.MaxReleasingAttempts}

	err := c.checkReleasingRetryOrEscalate(ctx, pr, fmt.Errorf("lookup: %w", context.Canceled))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the ctx error returned for retry", err)
	}
	if pr.Stage != StageReleasing {
		t.Fatalf("stage = %s, want %s — ctx expiry escalated the PR", pr.Stage, StageReleasing)
	}
}

// TestProcessPRWithDeadline_PanicCountedWhenAbandoned: a handler that panics
// after the loop gave up on it is logged and counted, not silently dropped.
func TestProcessPRWithDeadline_PanicCountedWhenAbandoned(t *testing.T) {
	c := stageDeadlineFixture(t, 100*time.Millisecond)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	c.processPRFn = func(_ context.Context, n int, _ *github.PullRequest) error {
		if n == 1 {
			<-release
			panic("handler blew up after abandonment")
		}
		return nil
	}

	runTick(t, c, 10*time.Second, "tick")
	close(release)
	waitStageIdle(t, c, 1)

	if got := c.Metrics().Snapshot().StagePanics[string(StageReleasing)]; got != 1 {
		t.Fatalf("StagePanics[releasing] = %d, want 1", got)
	}
}
