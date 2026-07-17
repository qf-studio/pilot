package autopilot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// TestGitHubBudgetClient_BelowFloor is table-driven over remaining/limit
// combinations, verifying the floor comparison and the justCrossed edge
// (GH-4391 acceptance: "when remaining < floor, background scans pause").
func TestGitHubBudgetClient_BelowFloor(t *testing.T) {
	tests := []struct {
		name      string
		remaining int
		limit     int
		floor     int
		wantBelow bool
	}{
		{"plenty of budget", 4500, 5000, 15, false},
		{"exactly at floor", 750, 5000, 15, false}, // 15.0% is not < 15
		{"just below floor", 700, 5000, 15, true},  // 14% < 15%
		{"budget exhausted", 0, 5000, 15, true},
		{"custom lower floor", 400, 5000, 5, false}, // 8% not below a 5% floor
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/rate_limit" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"resources":{"core":{"limit":%d,"remaining":%d,"reset":0}}}`, tt.limit, tt.remaining)
			}))
			defer server.Close()

			bc := NewGitHubBudgetClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			below, _ := bc.BelowFloor(context.Background(), tt.floor)
			if below != tt.wantBelow {
				t.Errorf("BelowFloor() = %v, want %v (remaining=%d limit=%d floor=%d)", below, tt.wantBelow, tt.remaining, tt.limit, tt.floor)
			}
		})
	}
}

// TestGitHubBudgetClient_BelowFloor_JustCrossed verifies justCrossed only
// fires on the below-floor transition, not on every call while still below —
// this is what lets callers log a single WARN instead of per-call spam.
func TestGitHubBudgetClient_BelowFloor_JustCrossed(t *testing.T) {
	var remaining int32 = 4500 // 90% of 5000: above a 15% floor

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"resources":{"core":{"limit":5000,"remaining":%d,"reset":0}}}`, atomic.LoadInt32(&remaining))
	}))
	defer server.Close()

	bc := NewGitHubBudgetClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	below, justCrossed := bc.BelowFloor(context.Background(), 15)
	if below || justCrossed {
		t.Fatalf("expected above-floor with no crossing on first call, got below=%v justCrossed=%v", below, justCrossed)
	}

	// Drop below the floor and force a fresh fetch by bypassing the cache:
	// use a client with cachedAt zero-valued (fresh client) to simulate the
	// next tick's re-fetch.
	atomic.StoreInt32(&remaining, 100) // 2%: below a 15% floor
	bc2 := NewGitHubBudgetClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	bc2.mu.Lock()
	bc2.floorEngaged = false
	bc2.mu.Unlock()

	below, justCrossed = bc2.BelowFloor(context.Background(), 15)
	if !below || !justCrossed {
		t.Fatalf("expected below-floor with justCrossed=true on first below-floor call, got below=%v justCrossed=%v", below, justCrossed)
	}

	// A second below-floor call must not re-signal justCrossed.
	below, justCrossed = bc2.BelowFloor(context.Background(), 15)
	if !below || justCrossed {
		t.Fatalf("expected below-floor with justCrossed=false on repeat call, got below=%v justCrossed=%v", below, justCrossed)
	}
}

// TestGitHubBudgetClient_BelowFloor_FailsOpen verifies an unreachable
// /rate_limit endpoint does not itself block background scans.
func TestGitHubBudgetClient_BelowFloor_FailsOpen(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	bc := NewGitHubBudgetClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	below, justCrossed := bc.BelowFloor(context.Background(), 15)
	if below || justCrossed {
		t.Fatalf("expected fail-open (below=false, justCrossed=false) on fetch error, got below=%v justCrossed=%v", below, justCrossed)
	}
}

// TestGitHubBudgetClient_ProbeRepoChanged verifies the conditional-request
// path: the first probe for a repo costs a real 200, and a second probe of
// an unchanged repo costs only a 304 (GH-4391 acceptance).
func TestGitHubBudgetClient_ProbeRepoChanged(t *testing.T) {
	var calls int32
	const etag = `"deadbeef"`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/repos/owner/repo/pulls" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("If-None-Match") == etag {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()

	bc := NewGitHubBudgetClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	changed, err := bc.ProbeRepoChanged(context.Background(), "owner", "repo")
	if err != nil {
		t.Fatalf("first probe: unexpected error %v", err)
	}
	if !changed {
		t.Fatal("first probe: expected changed=true (no prior ETag)")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 call after first probe, got %d", got)
	}

	changed, err = bc.ProbeRepoChanged(context.Background(), "owner", "repo")
	if err != nil {
		t.Fatalf("second probe: unexpected error %v", err)
	}
	if changed {
		t.Fatal("second probe: expected changed=false (304, unchanged)")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 total calls after second probe, got %d", got)
	}
}

// TestGitHubBudgetClient_ProbeRepoChanged_TransportErrorFailsOpen verifies a
// probe request failure reports changed=true so callers fall back to the
// full scan rather than silently skipping it.
func TestGitHubBudgetClient_ProbeRepoChanged_TransportErrorFailsOpen(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	bc := NewGitHubBudgetClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	changed, err := bc.ProbeRepoChanged(context.Background(), "owner", "repo")
	if err == nil {
		t.Fatal("expected an error for unexpected status")
	}
	if !changed {
		t.Fatal("expected changed=true (fail open) on probe error")
	}
}
