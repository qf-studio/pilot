package autopilot

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// ciTimeoutEnv is a mock GitHub for the GH-6327 hold after a CI timeout with
// no check reported: the head SHA, the PR state and the check runs per SHA are
// switchable, PR comments and merge calls are recorded.
type ciTimeoutEnv struct {
	t   *testing.T
	srv *httptest.Server
	ctx context.Context
	now time.Time

	mu       sync.Mutex
	head     string
	prState  string
	merged   bool
	checks   map[string][]map[string]interface{} // sha -> check runs
	checkErr map[string]bool                     // sha -> HTTP 500 on check-runs
	comments []string
	mergeReq int

	store *StateStore
	c     *Controller
	pr    *PRState
}

func newCITimeoutEnv(t *testing.T) *ciTimeoutEnv {
	t.Helper()
	e := &ciTimeoutEnv{
		t:        t,
		ctx:      context.Background(),
		now:      time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC),
		head:     "sha-A",
		prState:  "open",
		checks:   map[string][]map[string]interface{}{},
		checkErr: map[string]bool{},
	}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/42/comments"):
			var in struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			e.comments = append(e.comments, in.Body)
			_, _ = w.Write([]byte(`{"id":1}`))
		case strings.HasSuffix(r.URL.Path, "/merge"):
			e.mergeReq++
			_, _ = w.Write([]byte(`{"merged":true,"sha":"m"}`))
		case strings.Contains(r.URL.Path, "/check-runs"):
			parts := strings.Split(r.URL.Path, "/")
			sha := parts[len(parts)-2]
			if e.checkErr[sha] {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"Server Error"}`))
				return
			}
			runs := e.checks[sha]
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"total_count": len(runs), "check_runs": runs})
		case strings.HasSuffix(r.URL.Path, "/status"):
			_, _ = w.Write([]byte(`{"state":"pending","total_count":0,"statuses":[]}`))
		case strings.Contains(r.URL.Path, "/pulls/42"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"number": 42, "state": e.prState, "merged": e.merged, "mergeable": true,
				"head": map[string]interface{}{"sha": e.head},
			})
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(e.srv.Close)

	e.store = newTestStateStore(t)
	e.c = e.newController()
	e.c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "sha-A", "pilot/GH-10", "")
	e.pr, _ = e.c.GetPRState(42)
	e.pr.Stage = StageWaitingCI
	e.pr.CIWaitStartedAt = e.now
	// Prove this repo's CI is wired up (a check run on another SHA), as it was
	// on 2026-11-11: a SHA with no check run then stays pending to the timeout.
	e.setChecks("sha-other", "build", "completed", "success")
	if _, err := e.c.ciMonitor.CheckCI(e.ctx, "sha-other"); err != nil {
		t.Fatalf("CheckCI(sha-other): %v", err)
	}
	return e
}

// newController builds a controller on the shared mock and state store, with
// the controllable clock and a short discovery wait.
func (e *ciTimeoutEnv) newController() *Controller {
	cfg := DefaultConfig()
	cfg.CIWaitTimeout = 30 * time.Minute
	cfg.CIChecks.DiscoveryGracePeriod = time.Millisecond
	c := NewController(cfg, github.NewClientWithBaseURL(testutil.FakeGitHubToken, e.srv.URL), nil, "owner", "repo")
	c.SetStateStore(e.store)
	c.ciPollClock = func() time.Time { return e.now }
	return c
}

func (e *ciTimeoutEnv) setChecks(sha, name, status, conclusion string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checks[sha] = []map[string]interface{}{{"name": name, "status": status, "conclusion": conclusion}}
}

func (e *ciTimeoutEnv) setHead(sha string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.head = sha
}

func (e *ciTimeoutEnv) commentCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.comments)
}

func (e *ciTimeoutEnv) mergeCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mergeReq
}

// timeOut runs the wait past the CI timeout with the real ProcessPR, so the
// confirmed-timeout site of handleWaitingCI runs and its result is persisted.
func (e *ciTimeoutEnv) timeOut(c *Controller, pr *PRState) {
	e.t.Helper()
	pr.mu.Lock()
	pr.CIWaitStartedAt = e.now
	pr.mu.Unlock()
	e.now = e.now.Add(2 * time.Hour)
	ghPR := &github.PullRequest{Number: 42, State: "open", Head: github.PRRef{SHA: pr.HeadSHA}}
	if err := c.ProcessPR(e.ctx, 42, ghPR); err != nil {
		e.t.Fatalf("ProcessPR: %v", err)
	}
}

// tick runs one polling pass over PR 42 on controller c.
func (e *ciTimeoutEnv) tick(c *Controller) {
	e.t.Helper()
	c.mu.RLock()
	pr := c.activePRs[42]
	c.mu.RUnlock()
	if pr == nil {
		e.t.Fatal("PR 42 not tracked")
	}
	c.processOnePR(e.ctx, pr.snapshot())
}

func (e *ciTimeoutEnv) openPR(sha string) *github.PullRequest {
	return &github.PullRequest{Number: 42, State: "open", Head: github.PRRef{SHA: sha}}
}

func TestCITimeout_NoCheckSetsHoldWithoutTerminalLabel(t *testing.T) {
	e := newCITimeoutEnv(t)
	e.timeOut(e.c, e.pr)

	if e.pr.Stage != StageFailed {
		t.Fatalf("Stage = %s, want failed", e.pr.Stage)
	}
	if !e.pr.CITimeoutHoldActive || e.pr.CITimeoutHoldHeadSHA != "sha-A" {
		t.Errorf("hold = %v at %q, want true at sha-A", e.pr.CITimeoutHoldActive, e.pr.CITimeoutHoldHeadSHA)
	}
	if !strings.HasPrefix(e.pr.Error, "CI timeout after") {
		t.Errorf("Error = %q, want the CI timeout text", e.pr.Error)
	}
	if e.pr.TerminalLabel != "" {
		t.Errorf("TerminalLabel = %q, want none on the hold", e.pr.TerminalLabel)
	}
	if n := e.commentCount(); n != 1 {
		t.Fatalf("comments = %d, want 1 at the hold", n)
	}
	if !strings.Contains(e.comments[0], "no check within the wait time") {
		t.Errorf("hold comment = %q", e.comments[0])
	}
}

func TestCITimeout_DiscoveredChecksNeverSetHoldNorRevive(t *testing.T) {
	e := newCITimeoutEnv(t)
	e.setChecks("sha-A", "build", "in_progress", "")
	e.timeOut(e.c, e.pr)

	if e.pr.Stage != StageFailed {
		t.Fatalf("Stage = %s, want failed", e.pr.Stage)
	}
	if e.pr.CITimeoutHoldActive || e.pr.CITimeoutHoldHeadSHA != "" {
		t.Errorf("hold set for a timeout with a discovered check: %v %q", e.pr.CITimeoutHoldActive, e.pr.CITimeoutHoldHeadSHA)
	}
	if e.pr.TerminalLabel != github.LabelFailed {
		t.Errorf("TerminalLabel = %q, want %q", e.pr.TerminalLabel, github.LabelFailed)
	}

	// A new head and green checks do not revive a PR that was never held.
	e.setHead("sha-B")
	e.setChecks("sha-B", "build", "completed", "success")
	e.c.redriveCITimeoutHeldPR(e.ctx, e.pr, e.openPR("sha-B"))
	if e.pr.Stage != StageFailed || e.pr.CITimeoutRedriveCount != 0 {
		t.Errorf("never-held PR revived: stage %s, redrives %d", e.pr.Stage, e.pr.CITimeoutRedriveCount)
	}
	if n := e.commentCount(); n != 0 {
		t.Errorf("comments = %d, want 0", n)
	}
}

func TestCITimeoutRedrive_NewHeadReenters(t *testing.T) {
	e := newCITimeoutEnv(t)
	e.timeOut(e.c, e.pr)
	e.c.processPRFn = func(context.Context, int, *github.PullRequest) error { return nil }

	e.setHead("sha-B")
	e.tick(e.c)

	if e.pr.Stage != StageWaitingCI {
		t.Fatalf("Stage = %s, want waiting_ci", e.pr.Stage)
	}
	if e.pr.CITimeoutHoldActive || e.pr.CITimeoutHoldHeadSHA != "" || e.pr.Error != "" || e.pr.TerminalLabel != "" {
		t.Errorf("hold not cleared: %v %q error=%q label=%q", e.pr.CITimeoutHoldActive, e.pr.CITimeoutHoldHeadSHA, e.pr.Error, e.pr.TerminalLabel)
	}
	if e.pr.HeadSHA != "sha-B" || e.pr.CITimeoutRedriveCount != 1 {
		t.Errorf("head %q redrives %d, want sha-B and 1", e.pr.HeadSHA, e.pr.CITimeoutRedriveCount)
	}
	if !e.pr.CIWaitStartedAt.Equal(e.now) {
		t.Errorf("CIWaitStartedAt = %v, want the fresh clock %v", e.pr.CIWaitStartedAt, e.now)
	}
	if n := e.commentCount(); n != 2 {
		t.Errorf("comments = %d, want 2 (hold, re-entry)", n)
	}
}

func TestCITimeoutRedrive_SameSHATurnsGreenReenters(t *testing.T) {
	e := newCITimeoutEnv(t)
	e.timeOut(e.c, e.pr)

	// Still no check run on the same head: stays held.
	e.c.redriveCITimeoutHeldPR(e.ctx, e.pr, e.openPR("sha-A"))
	if e.pr.Stage != StageFailed || !e.pr.CITimeoutHoldActive {
		t.Fatalf("held PR moved with no check: stage %s hold %v", e.pr.Stage, e.pr.CITimeoutHoldActive)
	}

	// A check that is still running does not count either.
	e.setChecks("sha-A", "build", "in_progress", "")
	e.c.redriveCITimeoutHeldPR(e.ctx, e.pr, e.openPR("sha-A"))
	if e.pr.Stage != StageFailed {
		t.Fatalf("held PR moved on a pending check: stage %s", e.pr.Stage)
	}

	e.setChecks("sha-A", "build", "completed", "success")
	e.c.redriveCITimeoutHeldPR(e.ctx, e.pr, e.openPR("sha-A"))
	if e.pr.Stage != StageWaitingCI || e.pr.CITimeoutHoldActive || e.pr.CITimeoutRedriveCount != 1 {
		t.Errorf("not re-entered on green checks: stage %s hold %v redrives %d", e.pr.Stage, e.pr.CITimeoutHoldActive, e.pr.CITimeoutRedriveCount)
	}
}

func TestCITimeoutRedrive_CheckCIErrorIsNotGreen(t *testing.T) {
	e := newCITimeoutEnv(t)
	e.timeOut(e.c, e.pr)

	e.mu.Lock()
	e.checkErr["sha-A"] = true
	e.mu.Unlock()
	e.c.redriveCITimeoutHeldPR(e.ctx, e.pr, e.openPR("sha-A"))

	if e.pr.Stage != StageFailed || !e.pr.CITimeoutHoldActive || e.pr.CITimeoutRedriveCount != 0 {
		t.Errorf("CheckCI error treated as green: stage %s hold %v redrives %d", e.pr.Stage, e.pr.CITimeoutHoldActive, e.pr.CITimeoutRedriveCount)
	}
}

func TestCITimeoutRedrive_CapKeepsPRFailedWithTerminalLabel(t *testing.T) {
	e := newCITimeoutEnv(t)
	e.c.processPRFn = nil

	for round := 1; round <= maxCITimeoutRedrives; round++ {
		e.timeOut(e.c, e.pr)
		if !e.pr.CITimeoutHoldActive {
			t.Fatalf("round %d: not held (stage %s)", round, e.pr.Stage)
		}
		e.setHead("sha-new-" + string(rune('0'+round)))
		e.c.redriveCITimeoutHeldPR(e.ctx, e.pr, e.openPR(e.head))
		if e.pr.Stage != StageWaitingCI || e.pr.CITimeoutRedriveCount != round {
			t.Fatalf("round %d: stage %s redrives %d", round, e.pr.Stage, e.pr.CITimeoutRedriveCount)
		}
	}

	// The next timeout is past the cap: failed with the terminal label, no hold.
	e.timeOut(e.c, e.pr)
	if e.pr.Stage != StageFailed || e.pr.CITimeoutHoldActive || e.pr.TerminalLabel != github.LabelFailed {
		t.Fatalf("at the cap: stage %s hold %v label %q", e.pr.Stage, e.pr.CITimeoutHoldActive, e.pr.TerminalLabel)
	}
	before := e.commentCount()
	e.setHead("sha-late")
	e.c.redriveCITimeoutHeldPR(e.ctx, e.pr, e.openPR("sha-late"))
	if e.pr.Stage != StageFailed || e.pr.CITimeoutRedriveCount != maxCITimeoutRedrives {
		t.Errorf("revived past the cap: stage %s redrives %d", e.pr.Stage, e.pr.CITimeoutRedriveCount)
	}
	if e.commentCount() != before {
		t.Errorf("comment posted past the cap")
	}
}

func TestCITimeoutRedrive_CapReachedOnHeldRowSetsTerminalLabel(t *testing.T) {
	e := newCITimeoutEnv(t)
	pr := &PRState{
		PRNumber: 42, Stage: StageFailed, HeadSHA: "sha-A", Error: "CI timeout after 30m0s (last confirmed status: pending)",
		CITimeoutHoldActive: true, CITimeoutHoldHeadSHA: "sha-A", CITimeoutRedriveCount: maxCITimeoutRedrives,
	}
	e.c.redriveCITimeoutHeldPR(e.ctx, pr, e.openPR("sha-B"))
	if pr.Stage != StageFailed || pr.CITimeoutHoldActive || pr.TerminalLabel != github.LabelFailed {
		t.Errorf("stage %s hold %v label %q, want failed, released, %q", pr.Stage, pr.CITimeoutHoldActive, pr.TerminalLabel, github.LabelFailed)
	}
}

func TestCITimeoutRedrive_MergedOrClosedPRIgnored(t *testing.T) {
	for name, ghPR := range map[string]*github.PullRequest{
		"closed": {Number: 42, State: "closed", Head: github.PRRef{SHA: "sha-B"}},
		"merged": {Number: 42, State: "closed", Merged: true, Head: github.PRRef{SHA: "sha-B"}},
	} {
		t.Run(name, func(t *testing.T) {
			e := newCITimeoutEnv(t)
			e.timeOut(e.c, e.pr)
			before := e.commentCount()
			e.c.redriveCITimeoutHeldPR(e.ctx, e.pr, ghPR)
			if e.pr.Stage != StageFailed || !e.pr.CITimeoutHoldActive || e.pr.CITimeoutRedriveCount != 0 {
				t.Errorf("%s PR redriven: stage %s hold %v redrives %d", name, e.pr.Stage, e.pr.CITimeoutHoldActive, e.pr.CITimeoutRedriveCount)
			}
			if e.commentCount() != before {
				t.Errorf("comment posted for a %s PR", name)
			}
		})
	}
}

func TestCITimeoutRedrive_RejectedApprovalUntouched(t *testing.T) {
	e := newCITimeoutEnv(t)
	pr := &PRState{
		PRNumber: 42, Stage: StageFailed, HeadSHA: "sha-A", Error: "merge approval rejected",
		ApprovalRejectNotified: true, TerminalLabel: "",
	}
	e.setChecks("sha-B", "build", "completed", "success")
	e.c.redriveCITimeoutHeldPR(e.ctx, pr, e.openPR("sha-B"))
	if pr.Stage != StageFailed || pr.HeadSHA != "sha-A" || pr.Error != "merge approval rejected" || pr.CITimeoutRedriveCount != 0 {
		t.Errorf("rejected PR changed: %+v", pr)
	}
	if n := e.commentCount(); n != 0 {
		t.Errorf("comments = %d, want 0", n)
	}
}

func TestRestoreState_CITimeoutHoldSurvivesRestartAndNewHeadReenters(t *testing.T) {
	e := newCITimeoutEnv(t)
	e.timeOut(e.c, e.pr)

	// A restart: a new controller on the same store, a CI monitor that has seen no
	// check run.
	c2 := e.newController()
	if _, err := c2.RestoreState(); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	c2.mu.RLock()
	restored := c2.activePRs[42]
	c2.mu.RUnlock()
	if restored == nil {
		t.Fatal("held PR not kept tracked after RestoreState")
	}
	if !restored.CITimeoutHoldActive || restored.CITimeoutHoldHeadSHA != "sha-A" || restored.Stage != StageFailed {
		t.Fatalf("restored row: stage %s hold %v at %q", restored.Stage, restored.CITimeoutHoldActive, restored.CITimeoutHoldHeadSHA)
	}

	c2.processPRFn = func(context.Context, int, *github.PullRequest) error { return nil }
	e.setHead("sha-B")
	e.tick(c2)
	if restored.Stage != StageWaitingCI || restored.CITimeoutRedriveCount != 1 || restored.HeadSHA != "sha-B" {
		t.Errorf("after a new head: stage %s redrives %d head %q", restored.Stage, restored.CITimeoutRedriveCount, restored.HeadSHA)
	}
}

func TestRestoreState_CITimeoutHoldNotMergedOnEmptyResult(t *testing.T) {
	e := newCITimeoutEnv(t)
	e.timeOut(e.c, e.pr)

	c2 := e.newController()
	if _, err := c2.RestoreState(); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	c2.mu.RLock()
	restored := c2.activePRs[42]
	c2.mu.RUnlock()
	if restored == nil {
		t.Fatal("held PR not kept tracked after RestoreState")
	}

	// Tick past the discovery wait with zero check runs: the fresh monitor
	// falls back to the combined status, whose empty answer maps to success.
	for i := 0; i < 4; i++ {
		e.now = e.now.Add(time.Minute)
		e.tick(c2)
		time.Sleep(5 * time.Millisecond)
	}
	emptyIsSuccess := false
	for i := 0; i < 3 && !emptyIsSuccess; i++ {
		status, err := c2.ciMonitor.CheckCI(e.ctx, "sha-A")
		emptyIsSuccess = err == nil && status == CISuccess
		time.Sleep(5 * time.Millisecond)
	}
	if !emptyIsSuccess {
		t.Fatal("setup: the fresh monitor never read the empty result as success, so this test guards nothing")
	}
	if restored.Stage != StageFailed || !restored.CITimeoutHoldActive || restored.CITimeoutRedriveCount != 0 {
		t.Errorf("held PR moved on an empty result: stage %s hold %v redrives %d", restored.Stage, restored.CITimeoutHoldActive, restored.CITimeoutRedriveCount)
	}
	if n := e.mergeCalls(); n != 0 {
		t.Errorf("merge calls = %d, want 0", n)
	}
}

// countingHandler counts Warn records that mention missing checks.
type countingHandler struct {
	mu    sync.Mutex
	warns int
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level < slog.LevelWarn {
		return nil
	}
	missing := strings.Contains(r.Message, "never reported") || strings.Contains(r.Message, "CI timeout confirmed")
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "missing_checks" {
			missing = true
		}
		return true
	})
	if missing {
		h.mu.Lock()
		h.warns++
		h.mu.Unlock()
	}
	return nil
}
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }

func TestCITimeoutHold_MissingChecksWarnLoggedOncePerPR(t *testing.T) {
	e := newCITimeoutEnv(t)
	h := &countingHandler{}
	e.c.log = slog.New(h)
	e.c.ciMonitor.log = slog.New(h)

	// Wait cycles on a head that never reports a check, then the timeout, then
	// many polls of the held PR.
	for i := 0; i < 3; i++ {
		if err := e.c.ProcessPR(e.ctx, 42, e.openPR("sha-A")); err != nil {
			t.Fatalf("ProcessPR: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	e.timeOut(e.c, e.pr)
	for i := 0; i < 5; i++ {
		e.c.redriveCITimeoutHeldPR(e.ctx, e.pr, e.openPR("sha-A"))
	}
	if !e.pr.CITimeoutHoldActive {
		t.Fatalf("PR not held (stage %s)", e.pr.Stage)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.warns != 2 {
		// One from the monitor (no check for the head) and one from the timeout
		// site; neither repeats per wait cycle or per held poll.
		t.Errorf("missing-check Warn lines = %d, want 2 (monitor once, timeout once)", h.warns)
	}
}

func TestStateStore_CITimeoutHoldRoundTrip(t *testing.T) {
	store := newTestStateStore(t)
	pr := &PRState{
		PRNumber: 7, Stage: StageFailed, HeadSHA: "sha-A",
		CITimeoutHoldActive: true, CITimeoutHoldHeadSHA: "sha-A", CITimeoutRedriveCount: 2,
	}
	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}
	got, err := store.GetPRState("owner/repo", 7)
	if err != nil || got == nil {
		t.Fatalf("GetPRState: %v %v", got, err)
	}
	if !got.CITimeoutHoldActive || got.CITimeoutHoldHeadSHA != "sha-A" || got.CITimeoutRedriveCount != 2 {
		t.Errorf("GetPRState = %v %q %d", got.CITimeoutHoldActive, got.CITimeoutHoldHeadSHA, got.CITimeoutRedriveCount)
	}
	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil || len(all) != 1 {
		t.Fatalf("LoadAllPRStates: %v %v", all, err)
	}
	if !all[0].CITimeoutHoldActive || all[0].CITimeoutHoldHeadSHA != "sha-A" || all[0].CITimeoutRedriveCount != 2 {
		t.Errorf("LoadAllPRStates = %v %q %d", all[0].CITimeoutHoldActive, all[0].CITimeoutHoldHeadSHA, all[0].CITimeoutRedriveCount)
	}
}
