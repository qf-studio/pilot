package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// githubAPIURL is the production GitHub REST API base. Tests use
// NewGitHubBudgetClientWithBaseURL against an httptest server instead.
const githubAPIURL = "https://api.github.com"

// budgetCacheTTL bounds how often GitHubBudgetClient re-fetches /rate_limit.
// GET /rate_limit is documented as free against the primary rate limit, but
// caching still avoids every concurrent background scan issuing its own
// request on every tick.
const budgetCacheTTL = 10 * time.Second

// DefaultRateLimitFloorPercent is the fallback floor (as a percentage of the
// token's total primary-rate-limit budget) below which background GitHub
// consumers pause — see Config.RateLimitFloorPercent (GH-4391).
const DefaultRateLimitFloorPercent = 15

// RateLimitBudget is a point-in-time snapshot of the GitHub primary rate
// limit for the token backing a GitHubBudgetClient.
type RateLimitBudget struct {
	Limit     int
	Remaining int
	Reset     time.Time
}

// PercentRemaining returns Remaining/Limit as a percentage (0-100). A Limit
// of zero (budget never successfully fetched) is treated as 100% remaining
// so callers fail open rather than wedging every background scan on a
// transient /rate_limit error.
func (b RateLimitBudget) PercentRemaining() float64 {
	if b.Limit <= 0 {
		return 100
	}
	return float64(b.Remaining) / float64(b.Limit) * 100
}

// GitHubBudgetClient tracks the shared GitHub primary-rate-limit budget for
// the daemon's configured token, and provides a cheap "has anything changed"
// probe for merged-PR rescans — both GH-4391.
//
// It is deliberately independent of the studio-sdk github.Client that
// Controller uses for everything else: that SDK is an external, versioned
// dependency (no vendor/replace directive in go.mod) with no exposed
// rate-limit-header tracking or conditional-request support. Rather than
// forking it, GitHubBudgetClient makes its own raw HTTP calls against two
// endpoints GitHub documents as NOT counting against the primary rate limit:
//
//   - GET /rate_limit (always free) — backs BelowFloor.
//   - A conditional GET with If-None-Match (a 304 response is free) against
//     the closed-PR list — backs ProbeRepoChanged.
type GitHubBudgetClient struct {
	token      string
	baseURL    string
	httpClient *http.Client

	mu           sync.Mutex
	cached       RateLimitBudget
	cachedAt     time.Time
	floorEngaged bool
	etags        map[string]string // "owner/repo" -> ETag from the last conditional probe
}

// NewGitHubBudgetClient creates a GitHubBudgetClient against the production
// GitHub API.
func NewGitHubBudgetClient(token string) *GitHubBudgetClient {
	return NewGitHubBudgetClientWithBaseURL(token, githubAPIURL)
}

// NewGitHubBudgetClientWithBaseURL creates a GitHubBudgetClient against a
// custom base URL (for testing).
func NewGitHubBudgetClientWithBaseURL(token, baseURL string) *GitHubBudgetClient {
	return &GitHubBudgetClient{
		token:      token,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		etags:      make(map[string]string),
	}
}

// Status returns the current rate-limit budget, using a cached value when
// available (see budgetCacheTTL) rather than re-fetching /rate_limit on
// every call.
func (b *GitHubBudgetClient) Status(ctx context.Context) (RateLimitBudget, error) {
	b.mu.Lock()
	if time.Since(b.cachedAt) < budgetCacheTTL {
		cached := b.cached
		b.mu.Unlock()
		return cached, nil
	}
	b.mu.Unlock()

	budget, err := b.fetch(ctx)
	if err != nil {
		return RateLimitBudget{}, err
	}

	b.mu.Lock()
	b.cached = budget
	b.cachedAt = time.Now()
	b.mu.Unlock()

	return budget, nil
}

func (b *GitHubBudgetClient) fetch(ctx context.Context) (RateLimitBudget, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/rate_limit", nil)
	if err != nil {
		return RateLimitBudget{}, err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return RateLimitBudget{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return RateLimitBudget{}, fmt.Errorf("rate_limit check: unexpected status %d", resp.StatusCode)
	}

	var payload struct {
		Resources struct {
			Core struct {
				Limit     int   `json:"limit"`
				Remaining int   `json:"remaining"`
				Reset     int64 `json:"reset"`
			} `json:"core"`
		} `json:"resources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return RateLimitBudget{}, fmt.Errorf("rate_limit check: decode response: %w", err)
	}

	return RateLimitBudget{
		Limit:     payload.Resources.Core.Limit,
		Remaining: payload.Resources.Core.Remaining,
		Reset:     time.Unix(payload.Resources.Core.Reset, 0),
	}, nil
}

// BelowFloor reports whether the current remaining budget is below
// floorPercent (e.g. 15 for 15%). justCrossed is true only on the
// below-floor edge (the previous call was above-floor or this is the first
// call), so callers can log a single WARN per engagement instead of one per
// call. A Status() fetch error fails open (below=false, justCrossed=false):
// an unreachable /rate_limit endpoint must not itself starve background
// scans.
func (b *GitHubBudgetClient) BelowFloor(ctx context.Context, floorPercent int) (below, justCrossed bool) {
	budget, err := b.Status(ctx)
	if err != nil {
		return false, false
	}

	below = budget.PercentRemaining() < float64(floorPercent)

	b.mu.Lock()
	was := b.floorEngaged
	b.floorEngaged = below
	b.mu.Unlock()

	return below, below && !was
}

// ProbeRepoChanged performs a conditional GET against a repo's closed-PR
// list (the cheapest page — per_page=1, sorted by most-recently-updated)
// using the ETag from the previous probe for that repo, if any.
//
// It reports changed=true (and remembers the new ETag) on a 200 response —
// either the first probe for this owner/repo, or genuine new activity — and
// changed=false on 304 Not Modified, which GitHub does not count against the
// primary rate limit (unlike the full paginated ListPullRequests call this
// is meant to gate). A transport or unexpected-status error also reports
// changed=true so callers fail open to the full scan rather than silently
// going stale.
func (b *GitHubBudgetClient) ProbeRepoChanged(ctx context.Context, owner, repo string) (changed bool, err error) {
	key := owner + "/" + repo

	b.mu.Lock()
	etag := b.etags[key]
	b.mu.Unlock()

	url := fmt.Sprintf("%s/repos/%s/%s/pulls?state=closed&per_page=1&sort=updated&direction=desc", b.baseURL, owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return true, err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()

	if newETag := resp.Header.Get("ETag"); newETag != "" {
		b.mu.Lock()
		b.etags[key] = newETag
		b.mu.Unlock()
	}

	switch resp.StatusCode {
	case http.StatusNotModified:
		return false, nil
	case http.StatusOK:
		return true, nil
	default:
		return true, fmt.Errorf("repo activity probe: unexpected status %d", resp.StatusCode)
	}
}
