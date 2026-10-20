package autopilot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// fakeBranchGitHub is a minimal GitHub for the stale-branch sweep: prefix
// listing, PR lists, branch tip dates and ref deletion, with pagination.
type fakeBranchGitHub struct {
	mu           sync.Mutex
	branches     []string
	open, closed []*github.PullRequest
	tipDate      map[string]time.Time
	deleteStatus map[string]int // branch -> forced status for DELETE
	deleted      []string
	requests     int
}

func (f *fakeBranchGitHub) page(r *http.Request, n int) (int, int) {
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if perPage <= 0 {
		perPage = 30
	}
	if page <= 0 {
		page = 1
	}
	start := (page - 1) * perPage
	if start > n {
		start = n
	}
	end := start + perPage
	if end > n {
		end = n
	}
	return start, end
}

func (f *fakeBranchGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	const base = "/repos/owner/repo"
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(p, base+"/git/matching-refs/heads/"):
		prefix := strings.TrimPrefix(p, base+"/git/matching-refs/heads/")
		var matched []map[string]any
		for _, b := range f.branches {
			if strings.HasPrefix(b, prefix) {
				matched = append(matched, map[string]any{"ref": "refs/heads/" + b, "object": map[string]string{"sha": "sha-" + b}})
			}
		}
		s, e := f.page(r, len(matched))
		_ = writeJSON(w, matched[s:e])
	case r.Method == http.MethodGet && p == base+"/pulls":
		prs := f.open
		if r.URL.Query().Get("state") == "closed" {
			prs = f.closed
		}
		s, e := f.page(r, len(prs))
		_ = writeJSON(w, prs[s:e])
	case r.Method == http.MethodGet && strings.HasPrefix(p, base+"/branches/"):
		name := strings.TrimPrefix(p, base+"/branches/")
		date, ok := f.tipDate[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprintf(w, `{"name":%q,"commit":{"sha":"x","commit":{"committer":{"date":%q}}}}`, name, date.UTC().Format(time.RFC3339))
	case r.Method == http.MethodDelete && strings.HasPrefix(p, base+"/git/refs/heads/"):
		name := strings.TrimPrefix(p, base+"/git/refs/heads/")
		if st := f.deleteStatus[name]; st != 0 {
			w.WriteHeader(st)
			return
		}
		f.deleted = append(f.deleted, name)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeBranchGitHub) deletedSorted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.deleted...)
	sort.Strings(out)
	return out
}

func newSweepController(t *testing.T, f *fakeBranchGitHub, enabled bool) *Controller {
	t.Helper()
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	cfg := DefaultConfig()
	cfg.StaleBranchSweep = enabled
	sweepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(cfg, github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL), nil, "owner", "repo",
		WithBranchSweepClient(sweepClient))
	c.SetAlertsEngine(&fakeAlertSink{})
	return c
}

func closedPR(n int, head string) *github.PullRequest {
	return &github.PullRequest{Number: n, State: "closed", Head: github.PRRef{Ref: head}, Base: github.PRRef{Ref: "main"}}
}

func openPR(n int, head, base string) *github.PullRequest {
	return &github.PullRequest{Number: n, State: "open", Head: github.PRRef{Ref: head}, Base: github.PRRef{Ref: base}}
}

func TestSweepStaleBranches(t *testing.T) {
	old := time.Now().Add(-72 * time.Hour)
	young := time.Now().Add(-1 * time.Hour)

	tests := []struct {
		name        string
		fake        *fakeBranchGitHub
		setup       func(c *Controller)
		wantDeleted []string
		goneCount   int // branches whose DELETE 404'd: counted as deleted, but not recorded by the fake
	}{
		{
			name:        "merged PR, old",
			fake:        &fakeBranchGitHub{branches: []string{"pilot/GH-1"}, closed: []*github.PullRequest{closedPR(10, "pilot/GH-1")}},
			wantDeleted: []string{"pilot/GH-1"},
		},
		{
			name: "closed PR, young tip still deleted (PR state alone decides)",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-2"}, closed: []*github.PullRequest{closedPR(11, "pilot/GH-2")},
				tipDate: map[string]time.Time{"pilot/GH-2": young}},
			wantDeleted: []string{"pilot/GH-2"},
		},
		{
			name: "open PR is skipped",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-3"}, open: []*github.PullRequest{openPR(12, "pilot/GH-3", "main")},
				closed: []*github.PullRequest{closedPR(5, "pilot/GH-3")}},
		},
		{
			name: "no PR, tip 1h old, min age 24h",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-4"}, tipDate: map[string]time.Time{"pilot/GH-4": young}},
		},
		{
			name:        "no PR, tip 3d old",
			fake:        &fakeBranchGitHub{branches: []string{"pilot/GH-5"}, tipDate: map[string]time.Time{"pilot/GH-5": old}},
			wantDeleted: []string{"pilot/GH-5"},
		},
		{
			name: "no PR, tip date unknown is treated as young",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-6"}, tipDate: map[string]time.Time{"pilot/GH-6": {}}},
		},
		{
			name: "base of an open stacked PR is held by safeDeleteBranch",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-7"}, closed: []*github.PullRequest{closedPR(13, "pilot/GH-7")},
				open: []*github.PullRequest{openPR(14, "pilot/GH-8", "pilot/GH-7")}},
		},
		{
			name: "live execution for the task ID",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-9", "pilot/GH-10"},
				closed: []*github.PullRequest{closedPR(15, "pilot/GH-9"), closedPR(16, "pilot/GH-10")}},
			setup: func(c *Controller) {
				c.SetDispatcherLiveness(&mockDispatcherLiveness{runningTaskIDs: []string{"GH-9"}})
			},
			wantDeleted: []string{"pilot/GH-10"},
		},
		{
			name: "branch tracked by autopilot",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-11"}, closed: []*github.PullRequest{closedPR(17, "pilot/GH-11")}},
			setup: func(c *Controller) {
				c.activePRs[17] = &PRState{PRNumber: 17, BranchName: "pilot/GH-11"}
			},
		},
		{
			name: "DELETE 404 counts as gone without error",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-12"}, closed: []*github.PullRequest{closedPR(18, "pilot/GH-12")},
				deleteStatus: map[string]int{"pilot/GH-12": http.StatusNotFound}},
			goneCount: 1,
		},
		{
			name: "DELETE 500 on one branch does not stop the sweep",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-13", "pilot/GH-14"},
				closed:       []*github.PullRequest{closedPR(19, "pilot/GH-13"), closedPR(20, "pilot/GH-14")},
				deleteStatus: map[string]int{"pilot/GH-13": http.StatusInternalServerError}},
			wantDeleted: []string{"pilot/GH-14"},
		},
		{
			name:        "non-pilot branches are never touched",
			fake:        &fakeBranchGitHub{branches: []string{"feature/x", "pilot/GH-15"}, closed: []*github.PullRequest{closedPR(21, "feature/x"), closedPR(22, "pilot/GH-15")}},
			wantDeleted: []string{"pilot/GH-15"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newSweepController(t, tt.fake, true)
			if tt.setup != nil {
				tt.setup(c)
			}
			c.sweepStaleBranches(context.Background())

			got := tt.fake.deletedSorted()
			if strings.Join(got, ",") != strings.Join(tt.wantDeleted, ",") {
				t.Errorf("deleted = %v, want %v", got, tt.wantDeleted)
			}
			if gotM := c.metrics.Snapshot().StaleBranchesDeleted; gotM != int64(len(tt.wantDeleted)+tt.goneCount) {
				t.Errorf("StaleBranchesDeleted metric = %d, want %d", gotM, len(tt.wantDeleted)+tt.goneCount)
			}
		})
	}
}

func TestSweepStaleBranches_FlagOffMakesNoAPICalls(t *testing.T) {
	f := &fakeBranchGitHub{branches: []string{"pilot/GH-1"}, closed: []*github.PullRequest{closedPR(1, "pilot/GH-1")}}
	c := newSweepController(t, f, false)
	c.sweepStaleBranches(context.Background())
	if f.requests != 0 {
		t.Errorf("flag off made %d API requests, want 0", f.requests)
	}
}

func TestSweepStaleBranches_PaginatesAllBranches(t *testing.T) {
	f := &fakeBranchGitHub{}
	for i := 0; i < 150; i++ {
		name := fmt.Sprintf("pilot/GH-%d", 1000+i)
		f.branches = append(f.branches, name)
		f.closed = append(f.closed, closedPR(2000+i, name)) // 150 closed PRs also span two pages
	}
	c := newSweepController(t, f, true)
	c.sweepStaleBranches(context.Background())
	if got := len(f.deletedSorted()); got != 150 {
		t.Errorf("deleted %d of 150 branches across 2 pages", got)
	}
}

// TestSweepStaleBranches_AuditFixture mirrors the 2026-10-19 branch audit:
// 59 no-PR old, 7 closed-PR, 7 merged-PR, 2 open-PR, 1 no-PR young
// => exactly 73 deletions and 3 skips.
func TestSweepStaleBranches_AuditFixture(t *testing.T) {
	old := time.Now().Add(-5 * 24 * time.Hour)
	f := &fakeBranchGitHub{tipDate: map[string]time.Time{}}
	n, prNum := 0, 100
	add := func(count int, kind string) {
		for i := 0; i < count; i++ {
			n++
			name := fmt.Sprintf("pilot/GH-%d", 7000+n)
			f.branches = append(f.branches, name)
			switch kind {
			case "nopr-old":
				f.tipDate[name] = old
			case "nopr-young":
				f.tipDate[name] = time.Now().Add(-30 * time.Minute)
			case "closed", "merged":
				prNum++
				f.closed = append(f.closed, closedPR(prNum, name))
			case "open":
				prNum++
				f.open = append(f.open, openPR(prNum, name, "main"))
			}
		}
	}
	add(59, "nopr-old")
	add(7, "closed")
	add(7, "merged")
	add(2, "open")
	add(1, "nopr-young")

	c := newSweepController(t, f, true)
	c.sweepStaleBranches(context.Background())

	if got := len(f.deletedSorted()); got != 73 {
		t.Errorf("deleted = %d, want 73", got)
	}
	if got := len(f.branches) - len(f.deletedSorted()); got != 3 {
		t.Errorf("skipped = %d, want 3", got)
	}
	for _, d := range f.deletedSorted() {
		for _, pr := range f.open {
			if pr.Head.Ref == d {
				t.Errorf("deleted branch %s that has an open PR", d)
			}
		}
	}
}

func TestStaleBranchSweepInterval(t *testing.T) {
	tests := []struct {
		window, want time.Duration
	}{
		{30 * time.Minute, time.Hour},
		{3 * time.Hour, 3 * time.Hour},
	}
	for _, tt := range tests {
		c := &Controller{config: &Config{MergedPRScanWindow: tt.window}}
		if got := c.staleBranchSweepInterval(); got != tt.want {
			t.Errorf("window %v: interval = %v, want %v", tt.window, got, tt.want)
		}
	}
}
