package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-6327: a PR failed on a CI timeout with no check reported is held with a
// purpose-built flag and re-enters the pipeline on a new head or green checks.

const (
	ciTimeoutOldSHA = "aaaa111"
	ciTimeoutNewSHA = "bbbb222"
)

// ciTimeoutServer is a fake GitHub whose check runs are set per head SHA and
// whose PR head, state and merged flag can be flipped.
type ciTimeoutServer struct {
	mu       sync.Mutex
	checks   map[string][]github.CheckRun // sha -> check runs (none = no check ever reported)
	head     string
	state    string
	merged   bool
	comments []string
	srv      *httptest.Server
}

func newCITimeoutServer(t *testing.T) *ciTimeoutServer {
	t.Helper()
	s := &ciTimeoutServer{
		checks: map[string][]github.CheckRun{},
		head:   ciTimeoutOldSHA,
		state:  "open",
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls/42":
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number: 42, State: s.state, Merged: s.merged,
				HTMLURL: "https://github.com/owner/repo/pull/42",
				Head:    github.PRRef{SHA: s.head, Ref: "pilot/GH-10"},
				Base:    github.PRRef{Ref: "main"},
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/check-runs"):
			parts := strings.Split(r.URL.Path, "/")
			runs := s.checks[parts[len(parts)-2]]
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{TotalCount: len(runs), CheckRuns: runs})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/42/comments":
			var in struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			s.comments = append(s.comments, in.Body)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *ciTimeoutServer) setChecks(sha string, runs ...github.CheckRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks[sha] = runs
}

func (s *ciTimeoutServer) setHead(sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.head = sha
}

func (s *ciTimeoutServer) setPR(state string, merged bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state, s.merged = state, merged
}

func (s *ciTimeoutServer) commentList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.comments...)
}

func ciTimeoutPending() github.CheckRun {
	return github.CheckRun{Name: "ci", Status: github.CheckRunInProgress}
}

func ciTimeoutGreen() github.CheckRun {
	return github.CheckRun{Name: "ci", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess}
}

func ciTimeoutRed() github.CheckRun {
	return github.CheckRun{Name: "ci", Status: github.CheckRunCompleted, Conclusion: github.ConclusionFailure}
}

// newCITimeoutController returns a controller over srv with a real state store
// and PR #42 registered at waiting_ci with an already expired CI wait.
func newCITimeoutController(t *testing.T, srv *ciTimeoutServer) (*Controller, *PRState) {
	t.Helper()
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
	c.SetStateStore(newTestStateStore(t))
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, ciTimeoutOldSHA, "pilot/GH-10", "")
	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR 42 not tracked")
	}
	pr.Stage = StageWaitingCI
	pr.CIWaitStartedAt = time.Now().Add(-3 * time.Hour)
	return c, pr
}

// failOnTimeout runs one ProcessPR so the expired wait is confirmed.
func failOnTimeout(t *testing.T, c *Controller, pr *PRState) {
	t.Helper()
	if err := c.ProcessPR(context.Background(), 42, nil); err != nil {
		t.Fatalf("ProcessPR: %v", err)
	}
	if pr.Stage != StageFailed {
		t.Fatalf("Stage = %s after the timeout, want %s", pr.Stage, StageFailed)
	}
}

// tickPR runs one polling-loop pass (redrives, then ProcessPR) over PR #42.
func tickPR(c *Controller, pr *PRState) {
	c.processOnePR(context.Background(), pr.snapshot())
}

func TestCITimeoutHold_NoCheckReported_SetsFlagWithoutTerminalLabel(t *testing.T) {
	srv := newCITimeoutServer(t)
	c, pr := newCITimeoutController(t, srv)

	failOnTimeout(t, c, pr)

	if !pr.CITimeoutHoldActive || pr.CITimeoutHoldHeadSHA != ciTimeoutOldSHA {
		t.Errorf("hold = %v at %q, want true at %q", pr.CITimeoutHoldActive, pr.CITimeoutHoldHeadSHA, ciTimeoutOldSHA)
	}
	if pr.TerminalLabel != "" {
		t.Errorf("TerminalLabel = %q, want empty on the held path", pr.TerminalLabel)
	}
	if !strings.Contains(pr.Error, "CI timeout after") {
		t.Errorf("Error = %q, want the CI timeout text kept", pr.Error)
	}
	comments := srv.commentList()
	if len(comments) != 1 || !strings.Contains(comments[0], "no check") || !strings.Contains(comments[0], "Update branch") {
		t.Errorf("comments = %q, want exactly one hold comment", comments)
	}
}

func TestCITimeoutHold_DiscoveredChecksNeverHeldNorRevived(t *testing.T) {
	tests := []struct {
		name      string
		run       github.CheckRun
		wantStage PRStage
	}{
		{"checks stay pending", ciTimeoutPending(), StageFailed},
		{"checks fail", ciTimeoutRed(), StageCIFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newCITimeoutServer(t)
			srv.setChecks(ciTimeoutOldSHA, tt.run)
			c, pr := newCITimeoutController(t, srv)

			if err := c.ProcessPR(context.Background(), 42, nil); err != nil {
				t.Fatalf("ProcessPR: %v", err)
			}
			if pr.Stage != tt.wantStage {
				t.Fatalf("Stage = %s, want %s", pr.Stage, tt.wantStage)
			}
			if pr.CITimeoutHoldActive {
				t.Fatal("CITimeoutHoldActive set for a PR whose checks were discovered")
			}
			if tt.wantStage == StageFailed && pr.TerminalLabel != github.LabelFailed {
				t.Errorf("TerminalLabel = %q, want %q", pr.TerminalLabel, github.LabelFailed)
			}

			// A new head with green checks must not revive it.
			srv.setHead(ciTimeoutNewSHA)
			srv.setChecks(ciTimeoutNewSHA, ciTimeoutGreen())
			tickPR(c, pr)
			if pr.CITimeoutHoldActive || pr.CITimeoutRedriveCount != 0 {
				t.Errorf("hold = %v redrives = %d after a new head, want false and 0", pr.CITimeoutHoldActive, pr.CITimeoutRedriveCount)
			}
			if tt.wantStage == StageFailed && pr.Stage != StageFailed {
				t.Errorf("Stage = %s after a new head, want %s", pr.Stage, StageFailed)
			}
			// The failing-check path runs its own CI-fix cascade, so only the
			// timeout path is held to "no comment at all".
			if got := len(srv.commentList()); got != 0 && tt.wantStage == StageFailed {
				t.Errorf("comments = %d, want 0", got)
			}
		})
	}
}

func TestRedriveCITimeoutHeldPR_NewHeadReEntersPipeline(t *testing.T) {
	srv := newCITimeoutServer(t)
	c, pr := newCITimeoutController(t, srv)
	failOnTimeout(t, c, pr)

	srv.setHead(ciTimeoutNewSHA)
	srv.setChecks(ciTimeoutNewSHA, ciTimeoutPending())
	before := time.Now()
	tickPR(c, pr)

	if pr.Stage != StageWaitingCI {
		t.Fatalf("Stage = %s, want %s", pr.Stage, StageWaitingCI)
	}
	if pr.CITimeoutHoldActive || pr.CITimeoutHoldHeadSHA != "" || pr.Error != "" {
		t.Errorf("hold = %v at %q, error = %q, want all cleared", pr.CITimeoutHoldActive, pr.CITimeoutHoldHeadSHA, pr.Error)
	}
	if pr.HeadSHA != ciTimeoutNewSHA || pr.CITimeoutRedriveCount != 1 {
		t.Errorf("HeadSHA = %q redrives = %d, want %q and 1", pr.HeadSHA, pr.CITimeoutRedriveCount, ciTimeoutNewSHA)
	}
	if pr.CIWaitStartedAt.Before(before) {
		t.Errorf("CIWaitStartedAt = %v, want a fresh clock at or after %v", pr.CIWaitStartedAt, before)
	}
	comments := srv.commentList()
	if len(comments) != 2 || !strings.Contains(comments[1], "Pilot continues") {
		t.Errorf("comments = %q, want the hold comment then one re-entry comment", comments)
	}

	// The re-entry is persisted.
	saved, err := c.stateStore.GetPRState("owner/repo", 42)
	if err != nil || saved == nil || saved.CITimeoutHoldActive || saved.CITimeoutRedriveCount != 1 {
		t.Errorf("persisted = %+v, %v, want the hold cleared and one redrive", saved, err)
	}
}

func TestRedriveCITimeoutHeldPR_SameSHAGreenReEntersPipeline(t *testing.T) {
	srv := newCITimeoutServer(t)
	c, pr := newCITimeoutController(t, srv)
	failOnTimeout(t, c, pr)

	// Still no check: nothing happens.
	tickPR(c, pr)
	if pr.Stage != StageFailed || !pr.CITimeoutHoldActive {
		t.Fatalf("Stage = %s hold = %v with no check, want %s held", pr.Stage, pr.CITimeoutHoldActive, StageFailed)
	}
	// Checks appear but are red: nothing happens.
	srv.setChecks(ciTimeoutOldSHA, ciTimeoutRed())
	tickPR(c, pr)
	if pr.Stage != StageFailed || !pr.CITimeoutHoldActive {
		t.Fatalf("Stage = %s hold = %v with red checks, want %s held", pr.Stage, pr.CITimeoutHoldActive, StageFailed)
	}

	srv.setChecks(ciTimeoutOldSHA, ciTimeoutGreen())
	tickPR(c, pr)
	if pr.Stage == StageFailed || pr.CITimeoutHoldActive || pr.CITimeoutRedriveCount != 1 {
		t.Errorf("Stage = %s hold = %v redrives = %d, want re-entered once", pr.Stage, pr.CITimeoutHoldActive, pr.CITimeoutRedriveCount)
	}
}

func TestRedriveCITimeoutHeldPR_CapReached_StaysFailedWithTerminalLabel(t *testing.T) {
	srv := newCITimeoutServer(t)
	c, pr := newCITimeoutController(t, srv)
	pr.CITimeoutRedriveCount = maxCITimeoutRedrives

	failOnTimeout(t, c, pr)

	if pr.CITimeoutHoldActive {
		t.Error("CITimeoutHoldActive set at the cap")
	}
	if pr.TerminalLabel != github.LabelFailed {
		t.Errorf("TerminalLabel = %q, want %q at the cap", pr.TerminalLabel, github.LabelFailed)
	}
	srv.setHead(ciTimeoutNewSHA)
	srv.setChecks(ciTimeoutNewSHA, ciTimeoutGreen())
	tickPR(c, pr)
	if pr.Stage != StageFailed || pr.CITimeoutRedriveCount != maxCITimeoutRedrives {
		t.Errorf("Stage = %s redrives = %d, want %s and %d", pr.Stage, pr.CITimeoutRedriveCount, StageFailed, maxCITimeoutRedrives)
	}
	if got := len(srv.commentList()); got != 0 {
		t.Errorf("comments = %d at the cap, want 0 (no hold comment after the first)", got)
	}
}

func TestRedriveCITimeoutHeldPR_HeldRowAtCap_GetsTerminalLabel(t *testing.T) {
	srv := newCITimeoutServer(t)
	c, _ := newCITimeoutController(t, srv)
	pr := &PRState{
		PRNumber: 42, Stage: StageFailed, HeadSHA: ciTimeoutOldSHA, Error: "CI timeout after 30m0s",
		CITimeoutHoldActive: true, CITimeoutHoldHeadSHA: ciTimeoutOldSHA, CITimeoutRedriveCount: maxCITimeoutRedrives,
	}
	ghPR := &github.PullRequest{Number: 42, State: "open", Head: github.PRRef{SHA: ciTimeoutNewSHA}}

	c.redriveCITimeoutHeldPR(context.Background(), pr, ghPR)

	if pr.Stage != StageFailed || pr.CITimeoutHoldActive || pr.TerminalLabel != github.LabelFailed {
		t.Errorf("Stage = %s hold = %v label = %q, want failed, released and %q", pr.Stage, pr.CITimeoutHoldActive, pr.TerminalLabel, github.LabelFailed)
	}
}

func TestRedriveCITimeoutHeldPR_MergedOrClosedPRIgnored(t *testing.T) {
	tests := []struct {
		name string
		ghPR *github.PullRequest
	}{
		{"closed", &github.PullRequest{Number: 42, State: "closed", Head: github.PRRef{SHA: ciTimeoutNewSHA}}},
		{"merged", &github.PullRequest{Number: 42, State: "open", Merged: true, Head: github.PRRef{SHA: ciTimeoutNewSHA}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newCITimeoutServer(t)
			c, _ := newCITimeoutController(t, srv)
			pr := &PRState{
				PRNumber: 42, Stage: StageFailed, HeadSHA: ciTimeoutOldSHA,
				CITimeoutHoldActive: true, CITimeoutHoldHeadSHA: ciTimeoutOldSHA,
			}

			c.redriveCITimeoutHeldPR(context.Background(), pr, tt.ghPR)

			if pr.Stage != StageFailed || !pr.CITimeoutHoldActive || pr.CITimeoutRedriveCount != 0 {
				t.Errorf("Stage = %s hold = %v redrives = %d, want untouched", pr.Stage, pr.CITimeoutHoldActive, pr.CITimeoutRedriveCount)
			}
			if got := len(srv.commentList()); got != 0 {
				t.Errorf("comments = %d, want 0", got)
			}
		})
	}
}

func TestRedriveCITimeoutHeldPR_RejectedApprovalPRUntouched(t *testing.T) {
	srv := newCITimeoutServer(t)
	c, pr := newCITimeoutController(t, srv)
	pr.Stage = StageFailed
	pr.Error = "merge approval rejected"
	pr.ApprovalRejectNotified = true
	srv.setHead(ciTimeoutNewSHA)
	srv.setChecks(ciTimeoutNewSHA, ciTimeoutGreen())

	tickPR(c, pr)

	if pr.Stage != StageFailed || pr.Error != "merge approval rejected" || pr.CITimeoutRedriveCount != 0 {
		t.Errorf("Stage = %s error = %q redrives = %d, want untouched", pr.Stage, pr.Error, pr.CITimeoutRedriveCount)
	}
	if got := len(srv.commentList()); got != 0 {
		t.Errorf("comments = %d, want 0", got)
	}
}

func TestRestoreState_CITimeoutHeldPRStaysTrackedAndNewHeadReEnters(t *testing.T) {
	srv := newCITimeoutServer(t)
	c, pr := newCITimeoutController(t, srv)
	failOnTimeout(t, c, pr)

	// A restart: a new controller over the same store.
	restarted := NewController(DefaultConfig(), github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.srv.URL), nil, "owner", "repo")
	restarted.SetStateStore(c.stateStore)
	if _, err := restarted.RestoreState(); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	got, ok := restarted.GetPRState(42)
	if !ok {
		t.Fatal("CI-timeout-held PR dropped by RestoreState")
	}
	if !got.CITimeoutHoldActive || got.CITimeoutHoldHeadSHA != ciTimeoutOldSHA || got.TerminalLabel != "" {
		t.Fatalf("restored hold = %v at %q label %q, want true at %q with no label", got.CITimeoutHoldActive, got.CITimeoutHoldHeadSHA, got.TerminalLabel, ciTimeoutOldSHA)
	}

	srv.setHead(ciTimeoutNewSHA)
	srv.setChecks(ciTimeoutNewSHA, ciTimeoutPending())
	live := restarted.activePRs[42]
	restarted.processOnePR(context.Background(), live.snapshot())
	if live.Stage != StageWaitingCI || live.CITimeoutRedriveCount != 1 {
		t.Errorf("Stage = %s redrives = %d after a new head, want %s and 1", live.Stage, live.CITimeoutRedriveCount, StageWaitingCI)
	}
}

func TestRestoreState_CITimeoutRedriveCountPersists(t *testing.T) {
	store := newTestStateStore(t)
	in := &PRState{
		PRNumber: 7, Stage: StageFailed, CreatedAt: time.Now(),
		CITimeoutHoldActive: true, CITimeoutHoldHeadSHA: ciTimeoutOldSHA, CITimeoutRedriveCount: 2,
	}
	if err := store.SavePRState("owner/repo", in); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}
	one, err := store.GetPRState("owner/repo", 7)
	if err != nil || one == nil {
		t.Fatalf("GetPRState = %v, %v", one, err)
	}
	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil || len(all) != 1 {
		t.Fatalf("LoadAllPRStates = %v, %v", all, err)
	}
	for name, got := range map[string]*PRState{"GetPRState": one, "LoadAllPRStates": all[0], "snapshot": one.snapshot()} {
		if !got.CITimeoutHoldActive || got.CITimeoutHoldHeadSHA != ciTimeoutOldSHA || got.CITimeoutRedriveCount != 2 {
			t.Errorf("%s: hold = %v at %q redrives = %d, want true at %q and 2", name, got.CITimeoutHoldActive, got.CITimeoutHoldHeadSHA, got.CITimeoutRedriveCount, ciTimeoutOldSHA)
		}
	}
}

func TestCITimeoutHold_CommentGoesThroughSecretMask(t *testing.T) {
	srv := newCITimeoutServer(t)
	c, pr := newCITimeoutController(t, srv)
	const secret = "fake-configured-secret-value"
	secretpatterns.RegisterSecretValues(secret)
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)

	c.postCITimeoutComment(context.Background(), pr, "debug "+secret+" end")

	comments := srv.commentList()
	if len(comments) != 1 || strings.Contains(comments[0], secret) {
		t.Errorf("comments = %q, want one comment with the secret masked", comments)
	}
}
