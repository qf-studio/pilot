package ghbudget

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func headerWithRateLimit(remaining, limit int) http.Header {
	h := make(http.Header)
	h.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	h.Set("X-RateLimit-Limit", strconv.Itoa(limit))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
	return h
}

func headerWithRateLimitAndReset(remaining, limit int, resetAt time.Time) http.Header {
	h := headerWithRateLimit(remaining, limit)
	h.Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))
	return h
}

// githubHostClient returns an http.Client whose requests to
// "http://api.github.com/..." are actually dialed to server's real listener
// address, regardless of the requested host. RoundTripper's cache is scoped
// to req.URL.Host == "api.github.com" (GH-4498), so tests exercising that
// path need a request that legitimately carries that host without a real
// DNS entry for it.
func githubHostClient(server *httptest.Server, rt *RoundTripper) *http.Client {
	serverAddr := server.Listener.Addr().String()
	rt.Next = &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, serverAddr)
		},
	}
	return &http.Client{Transport: rt}
}

// TestTracker_Allow_PriorityGating is table-driven per the GH-4391
// acceptance criteria: "Fake GitHub client returning rate-limit headers:
// when remaining < floor, background scans pause but a poller fetch still
// executes."
func TestTracker_Allow_PriorityGating(t *testing.T) {
	tests := []struct {
		name           string
		remaining      int
		limit          int
		wantCritical   bool
		wantBackground bool
	}{
		{"plenty of headroom", 4000, 5000, true, true},
		{"exactly at floor (15%)", 750, 5000, true, true},
		{"just below floor", 749, 5000, true, false},
		{"nearly exhausted", 10, 5000, true, false},
		{"fully exhausted", 0, 5000, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewTracker(DefaultFloorPct, nil)
			tr.Observe(headerWithRateLimit(tt.remaining, tt.limit))

			if got := tr.Allow(PriorityCritical); got != tt.wantCritical {
				t.Errorf("Allow(PriorityCritical) = %v, want %v", got, tt.wantCritical)
			}
			if got := tr.Allow(PriorityBackground); got != tt.wantBackground {
				t.Errorf("Allow(PriorityBackground) = %v, want %v", got, tt.wantBackground)
			}
		})
	}
}

// TestTracker_Allow_NilTrackerAlwaysAllows verifies a nil *Tracker (budget
// tracking not wired) never gates any priority, so call sites can invoke
// Allow unconditionally without a separate nil check.
func TestTracker_Allow_NilTrackerAlwaysAllows(t *testing.T) {
	var tr *Tracker
	if !tr.Allow(PriorityCritical) {
		t.Error("nil tracker should allow PriorityCritical")
	}
	if !tr.Allow(PriorityBackground) {
		t.Error("nil tracker should allow PriorityBackground")
	}
}

// TestTracker_Observe_WarnsOncePerEngagementEpisode verifies the floor-engaged
// WARN fires exactly once per transition into the engaged state, not once
// per observation while it stays engaged, and fires again after a recovery.
func TestTracker_Observe_WarnsOncePerEngagementEpisode(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	tr := NewTracker(DefaultFloorPct, log)

	// Two consecutive low-budget observations: only the first should warn.
	tr.Observe(headerWithRateLimit(100, 5000))
	tr.Observe(headerWithRateLimit(90, 5000))

	warnCount := bytes.Count(buf.Bytes(), []byte("budget floor engaged"))
	if warnCount != 1 {
		t.Fatalf("expected exactly 1 WARN after two low observations, got %d: %s", warnCount, buf.String())
	}

	// Recover above the floor, then dip again — a fresh episode warns again.
	tr.Observe(headerWithRateLimit(4000, 5000))
	tr.Observe(headerWithRateLimit(50, 5000))

	warnCount = bytes.Count(buf.Bytes(), []byte("budget floor engaged"))
	if warnCount != 2 {
		t.Fatalf("expected exactly 2 WARNs after a recovery + re-engagement, got %d: %s", warnCount, buf.String())
	}
}

// TestTracker_Observe_IgnoresMissingHeaders verifies a response with no
// rate-limit headers (e.g. a non-GitHub host sharing the transport) doesn't
// reset previously-tracked state or panic.
func TestTracker_Observe_IgnoresMissingHeaders(t *testing.T) {
	tr := NewTracker(DefaultFloorPct, nil)
	tr.Observe(headerWithRateLimit(10, 5000)) // engages the floor
	if !tr.Snapshot().FloorEngaged {
		t.Fatal("expected floor engaged after low observation")
	}

	tr.Observe(make(http.Header)) // no rate-limit headers at all

	snap := tr.Snapshot()
	if !snap.FloorEngaged || snap.Remaining != 10 {
		t.Fatalf("state should be retained across a header-less observation, got %+v", snap)
	}
}

// TestRoundTripper_ConditionalGET_SecondScanCosts304Only verifies the
// GH-4391 acceptance criterion: "Conditional-request path: second scan of
// an unchanged repo costs 304s only." A fake GitHub-like server returns an
// ETag on the first GET and a 304 on a second GET carrying a matching
// If-None-Match. The RoundTripper must synthesize a 200 with the original
// body from the 304 so the caller never has to understand conditional
// requests, and the origin server must only be asked to do real work
// (serialize + send the full body) on the first call.
func TestRoundTripper_ConditionalGET_SecondScanCosts304Only(t *testing.T) {
	const etag = `"abc123"`
	const wantBody = `[{"number":1}]`

	var fullResponses int32
	var notModifiedResponses int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			atomic.AddInt32(&notModifiedResponses, 1)
			w.Header().Set("X-RateLimit-Remaining", "4999")
			w.Header().Set("X-RateLimit-Limit", "5000")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		atomic.AddInt32(&fullResponses, 1)
		w.Header().Set("ETag", etag)
		w.Header().Set("X-RateLimit-Remaining", "4998")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(wantBody))
	}))
	defer server.Close()

	tr := &RoundTripper{Tracker: NewTracker(DefaultFloorPct, nil)}
	client := githubHostClient(server, tr)

	for i := 0; i < 2; i++ {
		resp, err := client.Get("http://api.github.com/repos/owner/repo/pulls?state=closed")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		body := make([]byte, len(wantBody))
		n, _ := resp.Body.Read(body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("call %d: caller must never see a raw 304, got status %d", i, resp.StatusCode)
		}
		if string(body[:n]) != wantBody {
			t.Fatalf("call %d: body = %q, want %q", i, body[:n], wantBody)
		}
	}

	if got := atomic.LoadInt32(&fullResponses); got != 1 {
		t.Errorf("expected exactly 1 full (200) response from the origin, got %d", got)
	}
	if got := atomic.LoadInt32(&notModifiedResponses); got != 1 {
		t.Errorf("expected exactly 1 304 from the origin on the second call, got %d", got)
	}
}

// TestRoundTripper_ObservesRateLimitHeaders verifies the RoundTripper feeds
// every real response's rate-limit headers to the wired Tracker.
func TestRoundTripper_ObservesRateLimitHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "42")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tracker := NewTracker(DefaultFloorPct, nil)
	client := &http.Client{Transport: &RoundTripper{Tracker: tracker}}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	snap := tracker.Snapshot()
	if !snap.HaveData || snap.Remaining != 42 || snap.Limit != 5000 {
		t.Fatalf("tracker did not observe response headers: %+v", snap)
	}
}

// TestRoundTripper_NonGETNotCached verifies POST/PATCH/etc requests are
// never cached or conditionalized — only idempotent GET scans are.
func TestRoundTripper_NonGETNotCached(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("ETag", `"same-etag"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{Transport: &RoundTripper{Tracker: NewTracker(DefaultFloorPct, nil)}}

	for i := 0; i < 2; i++ {
		resp, err := client.Post(server.URL, "application/json", bytes.NewReader([]byte(`{}`)))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 real POST calls (no caching for non-GET), got %d", got)
	}
}

// TestRoundTripper_DistinctURLsCachedIndependently verifies pagination pages
// (which differ only by ?page=N) get independent cache entries rather than
// colliding.
func TestRoundTripper_DistinctURLsCachedIndependently(t *testing.T) {
	var page1Calls, page2Calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		var etag string
		switch page {
		case "1":
			etag = `"page1"`
		case "2":
			etag = `"page2"`
		}
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		// Only requests that fall through to a full 200 count as real work —
		// this is what the assertions below bound.
		switch page {
		case "1":
			atomic.AddInt32(&page1Calls, 1)
		case "2":
			atomic.AddInt32(&page2Calls, 1)
		}
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `page-%s-body`, page)
	}))
	defer server.Close()

	client := githubHostClient(server, &RoundTripper{Tracker: NewTracker(DefaultFloorPct, nil)})

	for round := 0; round < 2; round++ {
		for _, page := range []string{"1", "2"} {
			resp, err := client.Get("http://api.github.com/?page=" + page)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
		}
	}

	if got := atomic.LoadInt32(&page1Calls); got != 1 {
		t.Errorf("page 1: expected 1 full response across 2 rounds, got %d", got)
	}
	if got := atomic.LoadInt32(&page2Calls); got != 1 {
		t.Errorf("page 2: expected 1 full response across 2 rounds, got %d", got)
	}
}

// TestRoundTripper_HostScoped is the GH-4498 acceptance test: the
// conditional-GET cache must engage only for req.URL.Host ==
// "api.github.com". A non-GitHub ETag'd GET sharing the same transport (only
// latent today — in-process non-GitHub traffic is POST/websocket, but
// fragile since http.DefaultTransport is shared process-wide) must never be
// conditionalized or have a cache-backed response synthesized for it.
func TestRoundTripper_HostScoped(t *testing.T) {
	const etag = `"same-etag"`

	tests := []struct {
		name          string
		useGitHubHost bool
		wantFullCalls int32 // real (non-304) responses the origin sees across 2 identical GETs
	}{
		{"api.github.com: second call is a free conditional hit", true, 1},
		{"non-GitHub host: cache never engages, every call is full", false, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fullCalls int32
			var sawConditionalHeader bool

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-None-Match") == etag {
					sawConditionalHeader = true
					w.WriteHeader(http.StatusNotModified)
					return
				}
				atomic.AddInt32(&fullCalls, 1)
				w.Header().Set("ETag", etag)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("body"))
			}))
			defer server.Close()

			rt := &RoundTripper{Tracker: NewTracker(DefaultFloorPct, nil)}
			var client *http.Client
			url := server.URL
			if tt.useGitHubHost {
				client = githubHostClient(server, rt)
				url = "http://api.github.com/repos/owner/repo"
			} else {
				client = &http.Client{Transport: rt}
			}

			for i := 0; i < 2; i++ {
				resp, err := client.Get(url)
				if err != nil {
					t.Fatalf("call %d: %v", i, err)
				}
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("call %d: caller must never see a raw 304, got status %d", i, resp.StatusCode)
				}
				_ = resp.Body.Close()
			}

			if got := atomic.LoadInt32(&fullCalls); got != tt.wantFullCalls {
				t.Errorf("origin saw %d full (non-304) calls, want %d", got, tt.wantFullCalls)
			}
			if tt.useGitHubHost && !sawConditionalHeader {
				t.Error("expected the second api.github.com call to carry If-None-Match")
			}
			if !tt.useGitHubHost && sawConditionalHeader {
				t.Error("non-GitHub host must never send If-None-Match — caching must not engage for it")
			}
		})
	}
}

// TestRoundTripper_CacheBoundedOnResetRollover is the GH-4498 acceptance
// test for eviction: per-SHA check-run URLs are unique per commit, so
// without eviction the cache map grows forever. This proves that once the
// wall clock passes the Tracker's tracked ResetAt (the GitHub rate-limit
// window rolling over), the entire cache is dropped regardless of how many
// entries had accumulated — the map cannot grow past one window's worth.
func TestRoundTripper_CacheBoundedOnResetRollover(t *testing.T) {
	fixedNow := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	tr := NewTracker(DefaultFloorPct, nil)
	rt := &RoundTripper{Tracker: tr, now: func() time.Time { return fixedNow }}

	// Seed a tracked rate-limit window resetting one hour out, and seed the
	// cache as if many distinct per-SHA check-run URLs had accumulated.
	tr.Observe(headerWithRateLimitAndReset(4000, 5000, fixedNow.Add(time.Hour)))
	rt.mu.Lock()
	rt.cache = map[string]cacheEntry{
		"https://api.github.com/repos/o/r/commits/sha1/check-runs": {etag: `"a"`},
		"https://api.github.com/repos/o/r/commits/sha2/check-runs": {etag: `"b"`},
		"https://api.github.com/repos/o/r/commits/sha3/check-runs": {etag: `"c"`},
	}
	rt.mu.Unlock()

	// Still inside the same window: the cache must survive untouched.
	rt.evictIfWindowRolledOver()
	rt.mu.Lock()
	gotBefore := len(rt.cache)
	rt.mu.Unlock()
	if gotBefore != 3 {
		t.Fatalf("cache evicted before ResetAt passed: len=%d, want 3", gotBefore)
	}

	// Advance past the tracked ResetAt (a window rollover) and check again.
	fixedNow = fixedNow.Add(2 * time.Hour)
	rt.evictIfWindowRolledOver()
	rt.mu.Lock()
	gotAfter := len(rt.cache)
	rt.mu.Unlock()
	if gotAfter != 0 {
		t.Fatalf("cache should be fully evicted after ResetAt rollover, got %d entries, want 0", gotAfter)
	}
}
