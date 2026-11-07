package autopilot

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// fakeBranchGitHub is a minimal GitHub for the stale-branch sweep: prefix
// listing (unpaginated, like the real endpoint), PR lists (paginated), branch
// tip dates and ref deletion.
type fakeBranchGitHub struct {
	mu           sync.Mutex
	branches     []string
	open, closed []*github.PullRequest
	tipDate      map[string]time.Time
	deleteStatus map[string]int // branch -> forced status for DELETE
	deleted      []string
	requests     int
	branchGets   map[string]int  // branch -> GET /branches/{name} count
	deleteCalls  map[string]int  // branch -> DELETE attempts
	matchingReqs int             // GET git/matching-refs requests
	issues       []*github.Issue // open issues served by GET /issues and /issues/{n}
	issuesStatus int             // forced status for GET /issues (0 = serve)
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
		f.matchingReqs++
		prefix := strings.TrimPrefix(p, base+"/git/matching-refs/heads/")
		var matched []map[string]any
		for _, b := range f.branches {
			if strings.HasPrefix(b, prefix) {
				matched = append(matched, map[string]any{"ref": "refs/heads/" + b, "object": map[string]string{"sha": "sha-" + b}})
			}
		}
		// The real matching-refs endpoint ignores per_page/page and always
		// returns the full list (verified live 2026-10-20), so no paging here.
		_ = writeJSON(w, matched)
	case r.Method == http.MethodGet && p == base+"/pulls":
		prs := f.open
		if r.URL.Query().Get("state") == "closed" {
			prs = f.closed
		}
		s, e := f.page(r, len(prs))
		_ = writeJSON(w, prs[s:e])
	case r.Method == http.MethodGet && p == base+"/issues":
		if f.issuesStatus != 0 {
			w.WriteHeader(f.issuesStatus)
			return
		}
		s, e := f.page(r, len(f.issues))
		_ = writeJSON(w, f.issues[s:e])
	case r.Method == http.MethodGet && strings.HasPrefix(p, base+"/issues/"):
		n, _ := strconv.Atoi(strings.TrimPrefix(p, base+"/issues/"))
		for _, is := range f.issues {
			if is.Number == n {
				_ = writeJSON(w, is)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	case r.Method == http.MethodGet && strings.HasPrefix(p, base+"/branches/"):
		name := strings.TrimPrefix(p, base+"/branches/")
		if f.branchGets == nil {
			f.branchGets = map[string]int{}
		}
		f.branchGets[name]++
		date, ok := f.tipDate[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprintf(w, `{"name":%q,"commit":{"sha":"x","commit":{"committer":{"date":%q}}}}`, name, date.UTC().Format(time.RFC3339))
	case r.Method == http.MethodDelete && strings.HasPrefix(p, base+"/git/refs/heads/"):
		name := strings.TrimPrefix(p, base+"/git/refs/heads/")
		if f.deleteCalls == nil {
			f.deleteCalls = map[string]int{}
		}
		f.deleteCalls[name]++
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

// closedPR is a PR closed 72h ago, comfortably past DefaultStaleBranchMinAge.
func closedPR(n int, head string) *github.PullRequest {
	return closedPRAgo(n, head, 72*time.Hour)
}

func closedPRAgo(n int, head string, ago time.Duration) *github.PullRequest {
	return &github.PullRequest{Number: n, State: "closed", Head: github.PRRef{Ref: head}, Base: github.PRRef{Ref: "main"},
		UpdatedAt: time.Now().Add(-ago).UTC().Format(time.RFC3339)}
}

// fixIssue is an open autopilot-fix issue whose footer names branch, in the
// shape FeedbackLoop writes (branch/pr/iteration/source/sha).
func fixIssue(n int, branch string, pr int) *github.Issue {
	return &github.Issue{Number: n, State: "open", Labels: []github.Label{{Name: "autopilot-fix"}, {Name: "pilot"}},
		Body: fmt.Sprintf("CI failed.\n\n<!-- autopilot-meta branch:%s pr:%d iteration:1 source:%d -->\n", branch, pr, n-10)}
}

// queueLiveness is a DispatcherLiveness that also exposes the queue view.
type queueLiveness struct {
	mockDispatcherLiveness
	queued  []string
	project string // when set, only this project path sees the queue
}

func (q *queueLiveness) GetQueuedTaskIDs(projectPath string) ([]string, error) {
	if q.project != "" && q.project != projectPath {
		return nil, nil
	}
	return q.queued, nil
}

// borrowedLister is a BorrowedBranchLookup whose registry lists branches.
type borrowedLister struct {
	BorrowedBranchLookup
	branches []string
}

func (b *borrowedLister) BorrowedBranches() []string { return b.branches }

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
			name: "closed PR long ago, young tip still deleted (tip age is for no-PR branches only)",
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

func TestSweepStaleBranches_ClosedPRListAcrossPages(t *testing.T) {
	f := &fakeBranchGitHub{}
	for i := 0; i < 150; i++ {
		name := fmt.Sprintf("pilot/GH-%d", 1000+i)
		f.branches = append(f.branches, name)
		f.closed = append(f.closed, closedPR(2000+i, name)) // 150 closed PRs also span two pages
	}
	c := newSweepController(t, f, true)
	c.sweepStaleBranches(context.Background())
	if got := len(f.deletedSorted()); got != 150 {
		t.Errorf("deleted %d of 150 branches (closed-PR list spans two pages)", got)
	}
}

// TestSweepStaleBranches_LogsCarryRepo pins GH-5781: with one sweep per project
// the summary and per-branch lines must be attributable to a repo.
func TestSweepStaleBranches_LogsCarryRepo(t *testing.T) {
	f := &fakeBranchGitHub{branches: []string{"pilot/GH-1"}, closed: []*github.PullRequest{closedPR(1, "pilot/GH-1")}}
	c := newSweepController(t, f, true)
	var buf bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c.sweepStaleBranches(context.Background())

	var sawSummary, sawBranch bool
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if !strings.Contains(line, "repo=owner/repo") {
			t.Errorf("sweep log line missing repo=owner/repo: %s", line)
		}
		sawSummary = sawSummary || strings.Contains(line, "stale branch sweep: scanned=")
		sawBranch = sawBranch || strings.Contains(line, "deleted orphaned branch")
	}
	if !sawSummary || !sawBranch {
		t.Errorf("expected summary and per-branch lines, got:\n%s", buf.String())
	}
}

// TestSweepStaleBranches_UnpaginatedRefList_EvaluatesEachBranchOnce pins GH-5777:
// matching-refs ignores per_page/page, so with 100+ refs the sweeper must list
// once and process every branch exactly once. 120 refs: 100 old no-PR (deletable),
// 20 young no-PR (kept).
func TestSweepStaleBranches_UnpaginatedRefList_EvaluatesEachBranchOnce(t *testing.T) {
	old := time.Now().Add(-5 * 24 * time.Hour)
	young := time.Now().Add(-30 * time.Minute)
	f := &fakeBranchGitHub{tipDate: map[string]time.Time{}}
	for i := 0; i < 120; i++ {
		name := fmt.Sprintf("pilot/GH-%d", 3000+i)
		f.branches = append(f.branches, name)
		if i < 100 {
			f.tipDate[name] = old
		} else {
			f.tipDate[name] = young
		}
	}
	c := newSweepController(t, f, true)
	c.sweepStaleBranches(context.Background())

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.matchingReqs != 1 {
		t.Errorf("matching-refs requests = %d, want 1", f.matchingReqs)
	}
	for _, name := range f.branches {
		if n := f.branchGets[name]; n != 1 {
			t.Errorf("%s evaluated %d times, want 1", name, n)
		}
	}
	for name, n := range f.deleteCalls {
		if n != 1 {
			t.Errorf("%s delete attempted %d times, want 1", name, n)
		}
	}
	if len(f.deleted) != 100 {
		t.Errorf("deleted = %d, want 100 unique deletable branches", len(f.deleted))
	}
	if got := c.metrics.Snapshot().StaleBranchesDeleted; got != 100 {
		t.Errorf("StaleBranchesDeleted metric = %d, want 100", got)
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

// sweepLog runs one sweep with a debug logger and returns the captured log.
func sweepLog(t *testing.T, c *Controller) string {
	t.Helper()
	var buf bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c.sweepStaleBranches(context.Background())
	return buf.String()
}

func keptLine(log, branch, reason string) bool {
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "stale branch sweep: kept branch") &&
			strings.Contains(line, "branch="+branch) && strings.Contains(line, "reason="+reason) {
			return true
		}
	}
	return false
}

// TestSweepStaleBranches_GH6181Holds pins the three GH-6181 guards: an open
// autopilot-fix issue or a queued task that references a closed-PR branch
// keeps it, and a closed-PR branch is age-gated like a no-PR one.
func TestSweepStaleBranches_GH6181Holds(t *testing.T) {
	tests := []struct {
		name        string
		fake        *fakeBranchGitHub
		setup       func(c *Controller)
		wantDeleted []string
		wantKept    string // reason= of the kept line for pilot/GH-100, "" = none
		wantSummary string
	}{
		{
			name: "open autopilot-fix issue footer keeps a long-closed PR branch",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-100"}, closed: []*github.PullRequest{closedPRAgo(7, "pilot/GH-100", 25*time.Hour)},
				issues: []*github.Issue{fixIssue(110, "pilot/GH-100", 7)}},
			wantKept:    "fix_issue",
			wantSummary: "skipped_fix_issue=1",
		},
		{
			name: "fix issue footer for another branch does not hold this one",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-100"}, closed: []*github.PullRequest{closedPR(7, "pilot/GH-100")},
				issues: []*github.Issue{fixIssue(110, "pilot/GH-999", 8)}},
			wantDeleted: []string{"pilot/GH-100"},
		},
		{
			name: "borrowed-branch registry entry keeps the branch",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-100"}, closed: []*github.PullRequest{closedPR(7, "pilot/GH-100")}},
			setup: func(c *Controller) {
				c.SetBorrowedBranchLookup(&borrowedLister{branches: []string{"pilot/GH-100"}})
			},
			wantKept:    "fix_issue",
			wantSummary: "skipped_fix_issue=1",
		},
		{
			name: "queued task whose issue footer names the branch keeps it",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-100"}, closed: []*github.PullRequest{closedPR(7, "pilot/GH-100")},
				issues: []*github.Issue{{Number: 120, State: "open", Labels: []github.Label{{Name: "pilot"}},
					Body: "revise\n\n<!-- autopilot-meta branch:pilot/GH-100 pr:7 iteration:1 -->"}}},
			setup: func(c *Controller) {
				c.SetDispatcherLiveness(&queueLiveness{queued: []string{"GH-120"}})
			},
			wantKept:    "pending_task",
			wantSummary: "skipped_pending_task=1",
		},
		{
			name: "queued task keeps its own pilot/<id> branch",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-100"}, closed: []*github.PullRequest{closedPR(7, "pilot/GH-100")},
				issues: []*github.Issue{{Number: 100, State: "open", Body: "plain"}}},
			setup: func(c *Controller) {
				c.SetDispatcherLiveness(&queueLiveness{queued: []string{"GH-100"}})
			},
			wantKept:    "pending_task",
			wantSummary: "skipped_pending_task=1",
		},
		{
			name:        "closed 25h ago, no references: deleted",
			fake:        &fakeBranchGitHub{branches: []string{"pilot/GH-100"}, closed: []*github.PullRequest{closedPRAgo(7, "pilot/GH-100", 25*time.Hour)}},
			wantDeleted: []string{"pilot/GH-100"},
		},
		{
			name:        "closed 1h ago, no references: skipped_young",
			fake:        &fakeBranchGitHub{branches: []string{"pilot/GH-100"}, closed: []*github.PullRequest{closedPRAgo(7, "pilot/GH-100", time.Hour)}},
			wantKept:    "young",
			wantSummary: "skipped_young=1",
		},
		{
			name:        "closed PR with no timestamps is treated as young",
			fake:        &fakeBranchGitHub{branches: []string{"pilot/GH-100"}, closed: []*github.PullRequest{{Number: 7, State: "closed", Head: github.PRRef{Ref: "pilot/GH-100"}}}},
			wantKept:    "young",
			wantSummary: "skipped_young=1",
		},
		{
			name: "merged-at later than updated-at is the close time",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-100"}, closed: []*github.PullRequest{{Number: 7, State: "closed", Head: github.PRRef{Ref: "pilot/GH-100"},
				UpdatedAt: time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339), MergedAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}}},
			wantKept:    "young",
			wantSummary: "skipped_young=1",
		},
		{
			name: "no-PR young branch referenced by a fix issue logs fix_issue",
			fake: &fakeBranchGitHub{branches: []string{"pilot/GH-100"}, tipDate: map[string]time.Time{"pilot/GH-100": time.Now().Add(-72 * time.Hour)},
				issues: []*github.Issue{fixIssue(110, "pilot/GH-100", 7)}},
			wantKept:    "fix_issue",
			wantSummary: "skipped_fix_issue=1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newSweepController(t, tt.fake, true)
			if tt.setup != nil {
				tt.setup(c)
			}
			log := sweepLog(t, c)

			if got := strings.Join(tt.fake.deletedSorted(), ","); got != strings.Join(tt.wantDeleted, ",") {
				t.Errorf("deleted = %q, want %q\n%s", got, tt.wantDeleted, log)
			}
			if tt.wantKept != "" {
				if !keptLine(log, "pilot/GH-100", tt.wantKept) {
					t.Errorf("missing 'kept branch' line with reason=%s:\n%s", tt.wantKept, log)
				}
				if !strings.Contains(log, tt.wantSummary) {
					t.Errorf("summary missing %q:\n%s", tt.wantSummary, log)
				}
			} else if strings.Contains(log, "kept branch") {
				t.Errorf("unexpected 'kept branch' line:\n%s", log)
			}
		})
	}
}

// TestSweepStaleBranches_GH6181ReplayFixIssueOutlivesClosedPR replays
// 2026-11-06: autopilot closed PR#6165 and filed CI-fix #6167 (footer
// branch:pilot/GH-6157 pr:6165); the startup sweep ran 14 minutes later and
// deleted pilot/GH-6157. The age gate is set to 10 minutes so the PR (closed 14
// minutes ago) is past it — only the fix-issue guard can hold the branch, so
// dropping that guard fails this test.
func TestSweepStaleBranches_GH6181ReplayFixIssueOutlivesClosedPR(t *testing.T) {
	f := &fakeBranchGitHub{
		branches: []string{"pilot/GH-6157"},
		closed:   []*github.PullRequest{closedPRAgo(6165, "pilot/GH-6157", 14*time.Minute)},
		issues:   []*github.Issue{fixIssue(6167, "pilot/GH-6157", 6165)},
	}
	c := newSweepController(t, f, true)
	c.config.StaleBranchMinAge = 10 * time.Minute
	// #6167 is queued under its own id, not running: no live-task guard matches.
	c.SetDispatcherLiveness(&queueLiveness{queued: []string{"GH-6167"}})

	log := sweepLog(t, c)
	if got := f.deletedSorted(); len(got) != 0 {
		t.Fatalf("pilot/GH-6157 was deleted: %v\n%s", got, log)
	}
	if !keptLine(log, "pilot/GH-6157", "fix_issue") {
		t.Errorf("expected kept line with reason=fix_issue:\n%s", log)
	}
}

// TestSweepStaleBranches_GH6181UnknownProtectionSetSkipsSweep: if the open
// fix-issue list or a queued task's issue cannot be read, nothing is deleted.
func TestSweepStaleBranches_GH6181UnknownProtectionSetSkipsSweep(t *testing.T) {
	t.Run("fix issue list fails", func(t *testing.T) {
		f := &fakeBranchGitHub{branches: []string{"pilot/GH-100"}, closed: []*github.PullRequest{closedPR(7, "pilot/GH-100")}, issuesStatus: http.StatusInternalServerError}
		c := newSweepController(t, f, true)
		c.sweepStaleBranches(context.Background())
		if got := f.deletedSorted(); len(got) != 0 {
			t.Errorf("deleted %v with unknown fix-issue set", got)
		}
	})
	t.Run("queued task issue unreadable", func(t *testing.T) {
		f := &fakeBranchGitHub{branches: []string{"pilot/GH-100"}, closed: []*github.PullRequest{closedPR(7, "pilot/GH-100")}}
		c := newSweepController(t, f, true)
		c.SetDispatcherLiveness(&queueLiveness{queued: []string{"GH-555"}}) // 404 in the fake
		c.sweepStaleBranches(context.Background())
		if got := f.deletedSorted(); len(got) != 0 {
			t.Errorf("deleted %v with unreadable queued issue", got)
		}
	})
}

func TestAutopilotMetaBranches(t *testing.T) {
	tests := []struct {
		name, body string
		want       string
	}{
		{"footer", "x\n<!-- autopilot-meta branch:pilot/GH-1 pr:2 iteration:1 -->\n", "pilot/GH-1"},
		{"no space after marker variants", "<!--autopilot-meta branch:pilot/GH-2 pr:3-->", "pilot/GH-2"},
		{"branch not last field", "<!-- autopilot-meta pr:3 branch:pilot/GH-3 sha:abc1234 -->", "pilot/GH-3"},
		{"non-pilot branch ignored", "<!-- autopilot-meta branch:feature/x pr:3 -->", ""},
		{"no footer", "branch:pilot/GH-4 in prose", ""},
		{"two footers", "<!-- autopilot-meta branch:pilot/GH-5 -->\n<!-- autopilot-meta branch:pilot/GH-6 -->", "pilot/GH-5,pilot/GH-6"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strings.Join(autopilotMetaBranches(tt.body), ","); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSweepStaleBranches_GH6181QueueScopedToProject: another repo's queued task
// must neither stop this repo's sweep nor hold its branches.
func TestSweepStaleBranches_GH6181QueueScopedToProject(t *testing.T) {
	f := &fakeBranchGitHub{branches: []string{"pilot/GH-555"}, closed: []*github.PullRequest{closedPR(7, "pilot/GH-555")}}
	c := newSweepController(t, f, true)
	c.projectPath = "/repo-a"
	c.SetDispatcherLiveness(&queueLiveness{queued: []string{"GH-555"}, project: "/repo-b"})
	c.sweepStaleBranches(context.Background())
	if got := strings.Join(f.deletedSorted(), ","); got != "pilot/GH-555" {
		t.Errorf("deleted = %q, want pilot/GH-555", got)
	}
}

// TestSweepStaleBranches_GH6185UnreadableQueueSkipsSweep: with the real
// Dispatcher over a store that cannot be read, "queue unknown" must skip the
// sweep — it must not read as "nothing queued" and delete a queued task's
// branch. Control: the same setup over a healthy store deletes the orphan.
func TestSweepStaleBranches_GH6185UnreadableQueueSkipsSweep(t *testing.T) {
	newDispatcher := func(t *testing.T) (*executor.Dispatcher, *memory.Store) {
		t.Helper()
		dir, err := os.MkdirTemp("", "pilot-sweep-queue")
		if err != nil {
			t.Fatalf("temp dir: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		store, err := memory.NewStore(dir)
		if err != nil {
			t.Fatalf("store: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return executor.NewDispatcher(store, executor.NewRunner(), nil), store
	}

	t.Run("store error skips the sweep", func(t *testing.T) {
		d, store := newDispatcher(t)
		if err := store.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		f := &fakeBranchGitHub{branches: []string{"pilot/GH-100"}, closed: []*github.PullRequest{closedPR(7, "pilot/GH-100")}}
		c := newSweepController(t, f, true)
		c.projectPath = "/repo-a"
		c.SetDispatcherLiveness(d)
		log := sweepLog(t, c)
		if got := f.deletedSorted(); len(got) != 0 {
			t.Fatalf("deleted %v with an unreadable queue\n%s", got, log)
		}
		if !strings.Contains(log, "failed to read the dispatcher queue") {
			t.Errorf("expected skip warning in log:\n%s", log)
		}
	})

	t.Run("healthy empty queue still sweeps", func(t *testing.T) {
		d, _ := newDispatcher(t)
		f := &fakeBranchGitHub{branches: []string{"pilot/GH-100"}, closed: []*github.PullRequest{closedPR(7, "pilot/GH-100")}}
		c := newSweepController(t, f, true)
		c.projectPath = "/repo-a"
		c.SetDispatcherLiveness(d)
		c.sweepStaleBranches(context.Background())
		if got := strings.Join(f.deletedSorted(), ","); got != "pilot/GH-100" {
			t.Errorf("deleted = %q, want pilot/GH-100", got)
		}
	})
}
