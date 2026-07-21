package ghbudget

import (
	"bytes"
	"fmt"
	"log/slog"
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
	client := &http.Client{Transport: tr}

	for i := 0; i < 2; i++ {
		resp, err := client.Get(server.URL + "/repos/owner/repo/pulls?state=closed")
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

	client := &http.Client{Transport: &RoundTripper{Tracker: NewTracker(DefaultFloorPct, nil)}}

	for round := 0; round < 2; round++ {
		for _, page := range []string{"1", "2"} {
			resp, err := client.Get(server.URL + "?page=" + page)
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
