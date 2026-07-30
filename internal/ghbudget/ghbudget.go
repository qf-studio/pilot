// Package ghbudget tracks the shared GitHub primary rate-limit budget and
// gates low-priority background GitHub API consumers when headroom runs low.
//
// GH-4391: on 2026-07-16 the founder box's daemon burned its entire GitHub
// rate budget on startup rescans (11 repos x wide merged-PR/orphan-PR scans,
// fired back-to-back with no delay) and then 403'd every issue poller for
// 67+ minutes despite the daemon being otherwise healthy — 5 queued tasks
// sat frozen the whole time. `gh api rate_limit` showed 5000/5000 remaining
// at the same moment because GitHub's primary rate limit is pooled per
// authenticated user across every token/session that user holds — the
// gh-CLI token's view of the pool is not the daemon's config-token view of
// the SAME pool (see .agent/knowledge/memories/pitfalls/
// github-user-aggregate-rate-pool.md). The daemon had no visibility into its
// own remaining budget and no reservation for its highest-value consumer
// (the issue pollers).
//
// This package closes that gap with two independent pieces, wired together
// only by the Tracker:
//
//   - Tracker: process-wide budget state (remaining/limit/reset) fed by
//     every observed GitHub API response, plus a floor check
//     (PriorityBackground callers are told to stand down once headroom
//     drops below floorPct of the limit; PriorityCritical callers —
//     pollers, active-PR CI watches — are never gated).
//   - RoundTripper: an http.RoundTripper wrapper that (a) feeds every
//     response's rate-limit headers to a Tracker and (b) transparently
//     turns repeat GET requests into conditional requests (If-None-Match),
//     synthesizing a cache-backed 200 from a 304 so callers never see the
//     304 — GitHub does not decrement the rate-limit budget for a 304
//     response, so a repeat scan of an unchanged resource is free.
//
// Neither of Pilot's two GitHub HTTP clients (internal/adapters/github and
// the vendored studio-sdk client) exposes a way to inject a custom
// transport, and both leave http.Client.Transport nil — which means both
// fall back to http.DefaultTransport. Installing a RoundTripper over
// http.DefaultTransport at daemon startup, before any client is
// constructed, is therefore sufficient to observe every outbound GitHub API
// call from both clients (pollers, autopilot scans, CI watcher) without
// touching either client's source.
package ghbudget

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Priority classifies a GitHub API caller for budget-floor gating.
type Priority int

const (
	// PriorityCritical marks callers that must always be allowed to proceed
	// even when the shared rate budget is low: issue pollers and
	// active-PR CI watches. Allow always returns true for this priority.
	PriorityCritical Priority = iota
	// PriorityBackground marks callers that can be paused when the budget
	// floor is engaged: merged-PR scans, orphan-PR sweeps, reconciler
	// evidence fetches.
	PriorityBackground
)

// DefaultFloorPct is the fraction of the GitHub rate limit below which
// PriorityBackground callers are paused. 15% of a 5000/hr token budget is
// 750 requests of headroom reserved for pollers and active-PR CI watches.
const DefaultFloorPct = 0.15

// State is a point-in-time snapshot of the tracked GitHub rate-limit budget.
type State struct {
	// HaveData is false until the first response with rate-limit headers has
	// been observed.
	HaveData  bool
	Remaining int
	Limit     int
	ResetAt   time.Time
	// FloorEngaged is true when Remaining/Limit < the tracker's floorPct.
	FloorEngaged bool
}

// Tracker tracks the shared GitHub primary rate-limit budget. All daemon
// consumers draw from one per-authenticated-user pool regardless of which
// client (or which repo's controller) issued the request, so a single
// process-wide Tracker — not one per controller — is the correct scope. Safe
// for concurrent use.
type Tracker struct {
	floorPct float64
	log      *slog.Logger

	mu           sync.Mutex
	state        State
	floorEngaged bool
}

// NewTracker creates a Tracker gating PriorityBackground callers once
// remaining/limit drops below floorPct. floorPct <= 0 uses DefaultFloorPct.
// A nil log uses slog.Default().
func NewTracker(floorPct float64, log *slog.Logger) *Tracker {
	if floorPct <= 0 {
		floorPct = DefaultFloorPct
	}
	if log == nil {
		log = slog.Default()
	}
	return &Tracker{floorPct: floorPct, log: log.With("component", "ghbudget")}
}

// Observe updates the tracked budget from a real (non-synthesized) GitHub
// API response's rate-limit headers. Headers that are missing or
// unparseable (some GitHub endpoints, and any non-GitHub host if the
// transport is shared, don't set them) are a no-op — the previous state is
// retained rather than reset. Safe to call from multiple goroutines.
//
// Logs exactly one WARN per floor-engagement episode (the transition from
// disengaged to engaged), not once per observation — GH-4391 acceptance:
// a rate-starved daemon must be visible without spamming the log on every
// subsequent call while it stays starved.
func (t *Tracker) Observe(h http.Header) {
	remaining, ok1 := parseIntHeader(h, "X-RateLimit-Remaining")
	limit, ok2 := parseIntHeader(h, "X-RateLimit-Limit")
	if !ok1 || !ok2 || limit <= 0 {
		return
	}
	resetAt := parseResetHeader(h)
	engaged := float64(remaining)/float64(limit) < t.floorPct

	t.mu.Lock()
	t.state = State{HaveData: true, Remaining: remaining, Limit: limit, ResetAt: resetAt, FloorEngaged: engaged}
	transitioned := engaged && !t.floorEngaged
	t.floorEngaged = engaged
	t.mu.Unlock()

	if transitioned {
		t.log.Warn("github rate-limit budget floor engaged — pausing background scans (merged-PR scans, orphan-PR sweeps, reconciler evidence fetches) until headroom recovers; pollers and active-PR CI watches are not affected",
			"remaining", remaining,
			"limit", limit,
			"floor_pct", t.floorPct,
			"reset_at", resetAt,
		)
	}
}

// Allow reports whether a caller of the given priority may proceed.
// PriorityCritical always returns true. A nil *Tracker (budget tracking not
// wired) also always returns true, so callers can gate unconditionally
// without a separate nil check.
func (t *Tracker) Allow(p Priority) bool {
	if t == nil || p == PriorityCritical {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.floorEngaged
}

// Snapshot returns the current tracked budget state.
func (t *Tracker) Snapshot() State {
	if t == nil {
		return State{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

func parseIntHeader(h http.Header, key string) (int, bool) {
	v := h.Get(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

func parseResetHeader(h http.Header) time.Time {
	v := h.Get("X-RateLimit-Reset")
	if v == "" {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// cacheEntry is a cached conditional-GET response body, keyed by request URL.
type cacheEntry struct {
	etag       string
	statusCode int
	header     http.Header
	body       []byte
}

// githubAPIHost is the only host the conditional-GET cache engages for.
// http.DefaultTransport (which RoundTripper wraps) is shared process-wide, so
// without this scope any other ETag'd GET routed through the default
// transport would be cached and synthesized too — latent today since
// in-process non-GitHub traffic is POST/websocket, but fragile. Observe is
// unaffected: it already no-ops for responses without rate-limit headers, so
// it stays host-agnostic.
const githubAPIHost = "api.github.com"

// RoundTripper wraps an http.RoundTripper, feeding every response's
// rate-limit headers to a Tracker and transparently caching GET responses by
// ETag so a repeat request for an unchanged resource costs a 304 — which
// GitHub does not deduct from the rate-limit budget — instead of a full 200.
// The 304 is never surfaced to the caller: on a cache hit, RoundTrip
// synthesizes a 200 response from the cached body so callers that don't
// understand conditional requests (both of Pilot's GitHub clients included)
// need no changes.
//
// Only GET requests to githubAPIHost are cached. Non-GET requests, requests
// to any other host, and GET responses without an ETag pass straight through
// (still observed for rate-limit headers if present).
//
// The cache is bounded by discarding the whole map whenever the Tracker's
// tracked ResetAt rolls over (GH-4498): per-SHA check-run URLs are unique per
// commit, so an unbounded map would grow forever without this. Piggybacking
// on the Tracker's already-hourly rate-limit window reset avoids needing a
// second eviction policy (e.g. LRU) alongside it.
type RoundTripper struct {
	// Next is the underlying transport. A nil Next uses http.DefaultTransport.
	Next http.RoundTripper
	// Tracker receives rate-limit headers from every observed response. Must
	// be non-nil.
	Tracker *Tracker

	// now returns the current time; overridable in tests. A nil now uses
	// time.Now.
	now func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// RoundTrip implements http.RoundTripper.
func (rt *RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	next := rt.Next
	if next == nil {
		next = http.DefaultTransport
	}

	rt.evictIfWindowRolledOver()

	cacheable := req.Method == http.MethodGet && req.URL.Host == githubAPIHost
	var cacheKey string
	var cached cacheEntry
	var haveCached bool
	if cacheable {
		cacheKey = req.URL.String()
		rt.mu.Lock()
		cached, haveCached = rt.cache[cacheKey]
		rt.mu.Unlock()
		if haveCached && cached.etag != "" {
			req = req.Clone(req.Context())
			req.Header.Set("If-None-Match", cached.etag)
		}
	}

	resp, err := next.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	if rt.Tracker != nil {
		rt.Tracker.Observe(resp.Header)
	}

	if cacheable && haveCached && resp.StatusCode == http.StatusNotModified {
		// Free hit against the rate-limit budget (304s don't decrement it).
		// Drain and close the real body before discarding it — required by
		// net/http even though a 304 body is normally empty.
		liveHeader := resp.Header
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return synthesizeResponse(req, cached, liveHeader), nil
	}

	if cacheable && resp.StatusCode == http.StatusOK {
		if etag := resp.Header.Get("ETag"); etag != "" {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr == nil {
				entry := cacheEntry{etag: etag, statusCode: resp.StatusCode, header: resp.Header.Clone(), body: body}
				rt.mu.Lock()
				if rt.cache == nil {
					rt.cache = make(map[string]cacheEntry)
				}
				rt.cache[cacheKey] = entry
				rt.mu.Unlock()
				resp.Body = io.NopCloser(bytes.NewReader(body))
				return resp, nil
			}
			// Failed to read/cache the body — fall through and return the
			// (already partially consumed) response as-is; the caller's own
			// read will simply see less/no data, matching the read error it
			// would have hit anyway.
			resp.Body = io.NopCloser(bytes.NewReader(body))
		}
	}

	return resp, nil
}

// evictIfWindowRolledOver drops the whole cache once the wall clock passes
// the Tracker's tracked ResetAt, bounding the cache to at most one GitHub
// rate-limit window's worth of entries instead of growing forever (GH-4498).
// A zero ResetAt (no observation yet) or a nil Tracker is a no-op — there's
// nothing to bound against yet.
//
// This is checked on every RoundTrip rather than on a timer: it's a cheap
// mutex + snapshot check, and the very next real response's Observe() call
// refreshes ResetAt to the new window's value, so the map only clears once
// per rollover rather than on every call after expiry.
func (rt *RoundTripper) evictIfWindowRolledOver() {
	if rt.Tracker == nil {
		return
	}
	resetAt := rt.Tracker.Snapshot().ResetAt
	if resetAt.IsZero() {
		return
	}
	now := time.Now
	if rt.now != nil {
		now = rt.now
	}
	if !now().Before(resetAt) {
		rt.mu.Lock()
		rt.cache = nil
		rt.mu.Unlock()
	}
}

// synthesizeResponse builds a 200 response from a cached entry, refreshing
// the rate-limit headers from the live 304 (liveHeader) so callers
// inspecting them see current values even on a cache hit.
func synthesizeResponse(req *http.Request, cached cacheEntry, liveHeader http.Header) *http.Response {
	header := cached.header.Clone()
	for _, key := range []string{"X-Ratelimit-Limit", "X-Ratelimit-Remaining", "X-Ratelimit-Reset", "X-Ratelimit-Used", "X-Ratelimit-Resource"} {
		if v := liveHeader.Get(key); v != "" {
			header.Set(key, v)
		}
	}
	return &http.Response{
		Status:        strconv.Itoa(http.StatusOK) + " OK",
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(cached.body)),
		ContentLength: int64(len(cached.body)),
		Request:       req,
	}
}
