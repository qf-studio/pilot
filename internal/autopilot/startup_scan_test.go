package autopilot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestRunStaggeredStartupScans_StaggersAndBoundsAPISpend is the GH-4391
// acceptance case: startup with 10+ repos performs staggered (not
// concurrent-burst) scans, and total startup API spend stays an order of
// magnitude below the 5000-request primary rate-limit budget — the
// founder-box incident burned the entire budget bursting 11 repos' 30-day
// merged-PR scans at once.
func TestRunStaggeredStartupScans_StaggersAndBoundsAPISpend(t *testing.T) {
	const numRepos = 12

	var totalCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&totalCalls, 1)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()

	controllers := make(map[string]*Controller, numRepos)
	for i := 0; i < numRepos; i++ {
		repo := fmt.Sprintf("repo%d", i)
		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		controllers[repo] = NewController(DefaultConfig(), ghClient, nil, "owner", repo)
	}

	var sleepCalls int32
	var sleptDurations []time.Duration
	fakeSleep := func(d time.Duration) {
		atomic.AddInt32(&sleepCalls, 1)
		sleptDurations = append(sleptDurations, d)
	}

	RunStaggeredStartupScans(context.Background(), controllers, DefaultStartupMergedPRScanWindow, fakeSleep)

	// One stagger delay between each pair of consecutive repo slots — not a
	// single burst with zero delays.
	if got := atomic.LoadInt32(&sleepCalls); got != numRepos-1 {
		t.Errorf("expected %d stagger sleeps for %d repos, got %d", numRepos-1, numRepos, got)
	}
	for _, d := range sleptDurations {
		if d < StartupScanStaggerBase || d >= StartupScanStaggerBase+StartupScanStaggerJitter {
			t.Errorf("stagger delay %v outside expected [%v, %v)", d, StartupScanStaggerBase, StartupScanStaggerBase+StartupScanStaggerJitter)
		}
	}

	// Each repo's startup slot issues a handful of calls (ScanExistingPRs,
	// ScanRecentlyMergedPRsWithWindow, Start's epic-parent recovery probes)
	// — bounded per repo, not a 5 000-request burst across all repos.
	got := atomic.LoadInt32(&totalCalls)
	const wantMax = 10 * numRepos // generous per-repo upper bound
	if got == 0 {
		t.Fatal("expected at least some GitHub calls from the startup scans")
	}
	if got > wantMax {
		t.Errorf("total startup API calls = %d, want <= %d (order of magnitude below the 5000 budget)", got, wantMax)
	}
	if got >= 500 {
		t.Errorf("total startup API calls = %d must be an order of magnitude below the 5000 primary rate-limit budget", got)
	}
}

// TestRunStaggeredStartupScans_RespectsContextCancellation verifies the
// stagger loop stops issuing scans once the context is cancelled, instead of
// draining every remaining repo.
func TestRunStaggeredStartupScans_RespectsContextCancellation(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	controllers := map[string]*Controller{
		"repo-a": NewController(DefaultConfig(), ghClient, nil, "owner", "repo-a"),
		"repo-b": NewController(DefaultConfig(), ghClient, nil, "owner", "repo-b"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Must return promptly without panicking or blocking on a cancelled ctx.
	RunStaggeredStartupScans(ctx, controllers, DefaultStartupMergedPRScanWindow, func(time.Duration) {
		t.Fatal("sleepFn should not be called once ctx is already cancelled")
	})
}
