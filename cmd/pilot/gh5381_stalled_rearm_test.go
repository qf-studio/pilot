package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
)

// resetBotLoginCacheForTest clears resolveBotLogin's package-level cache —
// without this, whichever test in this package resolves a bot login first
// would leak that login into every later test's resolveBotLogin call
// (cachedBotLogin/botLoginResolved are cached for the process lifetime by
// design, since terminalCompletionChecker has no field to hold it — see
// resolveBotLogin's doc comment). Each test that exercises Timeline
// edited-event evidence must start from a clean cache so it only ever talks
// to its own mock server.
func resetBotLoginCacheForTest(t *testing.T) {
	t.Helper()
	botLoginMu.Lock()
	cachedBotLogin = ""
	botLoginResolved = false
	botLoginMu.Unlock()
	t.Cleanup(func() {
		botLoginMu.Lock()
		cachedBotLogin = ""
		botLoginResolved = false
		botLoginMu.Unlock()
	})
}

// TestTryRearmStalled_BotCommentAndLabelOnly_NotRearmed is GH-5381's core
// regression test for the reverted PR #5380: the ONLY post-stall activity on
// the issue is exactly what the dispatcher's own stall surfacing produces
// (surfaceStalledIssue, internal/executor/dispatcher.go ~2540-2551) — a
// bot comment and a pilot-blocked label add, both landing seconds after the
// stall timestamp and bumping issue.UpdatedAt right along with them. #5380
// treated issue.UpdatedAt moving past the stall as re-arm evidence, so this
// exact fixture re-armed itself every single sweep pass (GH-5381's incident).
// Neither event qualifies as real evidence: the labeled event names
// pilot-blocked, not the trigger label or a retry-ready label, and nothing
// in the (empty) Timeline shows an "edited" event.
func TestTryRearmStalled_BotCommentAndLabelOnly_NotRearmed(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)
	botActivityTime := stallTime.Add(2 * time.Second)

	srv := newRearmTestServer(t,
		&github.Issue{
			Number: 5139, State: "open",
			Labels:    []github.Label{{Name: "pilot"}, {Name: github.LabelBlocked}},
			UpdatedAt: botActivityTime,
		},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
			{Event: "labeled", CreatedAt: botActivityTime, Label: &github.Label{Name: github.LabelBlocked}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5139", "/project-gh5381-bot-comment-only"
	seedStalledRow(t, store, "exec-stalled-bot-comment-only", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })
	t.Cleanup(func() { stalledRearmNoEvidenceStreaks.reset(key) })

	rearmed, err := checker.tryRearmStalled(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rearmed {
		t.Fatal("expected rearmed=false — the only post-stall activity is the bot's own comment+pilot-blocked label, not deliberate operator evidence")
	}

	exec, err := store.GetExecution("exec-stalled-bot-comment-only")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "stalled" {
		t.Errorf("expected the row to remain status=stalled, got %q", exec.Status)
	}
}

// TestTryRearmStalled_TimelineEditByOperator_Rearms is GH-5381's real
// body-edit evidence acceptance test: a Timeline "edited" event authored by
// someone other than the bot, timestamped after the stall, counts as
// evidence even with zero label/reopen events — the classic Events API
// (ListIssueEvents) never emits "edited" at all, so this is only observable
// via ListIssueTimeline.
func TestTryRearmStalled_TimelineEditByOperator_Rearms(t *testing.T) {
	resetBotLoginCacheForTest(t)
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)
	editTime := stallTime.Add(30 * time.Minute)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/user" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(&github.User{Login: "pilot-bot"})
		case r.URL.Path == "/repos/owner/repo/issues/5139" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(&github.Issue{
				Number: 5139, State: "open",
				Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelRetry1}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/5139/events" && r.Method == http.MethodGet:
			// No labeled/reopened event after the stall — a body edit alone
			// is not surfaced as a labeled/reopened classic event.
			_ = json.NewEncoder(w).Encode([]*github.IssueEvent{
				{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
				{Event: "labeled", CreatedAt: stallTime.Add(-23 * time.Hour), Label: &github.Label{Name: github.LabelRetry1}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/5139/timeline" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]*github.TimelineEvent{
				{Event: "edited", CreatedAt: editTime, Actor: &github.User{Login: "operator-jane"}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/5139/labels/"+github.LabelBlocked && r.Method == http.MethodDelete:
			// A successful re-arm always tries to clear pilot-blocked,
			// regardless of which evidence type triggered it (this fixture
			// carries pilot-retry-1, not pilot-blocked, but the removal call
			// still fires unconditionally — see tryRearmStalled).
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5139", "/project-gh5381-timeline-edit"
	seedStalledRow(t, store, "exec-stalled-timeline-edit", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })
	t.Cleanup(func() { stalledRearmNoEvidenceStreaks.reset(key) })

	rearmed, err := checker.tryRearmStalled(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rearmed {
		t.Fatal("expected rearmed=true — a non-bot Timeline 'edited' event postdates the stall")
	}

	exec, err := store.GetExecution("exec-stalled-timeline-edit")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected the stalled row to be reclassified to status=failed, got %q", exec.Status)
	}
}

// TestTryRearmStalled_TimelineEditByBot_NotRearmed proves the actor filter:
// an "edited" event authored by the bot account itself (e.g. an
// autopilot-meta footer rewrite) must NOT count as re-arm evidence — this is
// exactly the class of self-inflicted signal GH-5381 fixes forward from
// (PR #5380 used issue.UpdatedAt, which can't distinguish the bot's own
// edits from an operator's).
func TestTryRearmStalled_TimelineEditByBot_NotRearmed(t *testing.T) {
	resetBotLoginCacheForTest(t)
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)
	editTime := stallTime.Add(30 * time.Minute)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/user" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(&github.User{Login: "pilot-bot"})
		case r.URL.Path == "/repos/owner/repo/issues/5139" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(&github.Issue{
				Number: 5139, State: "open",
				Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelBlocked}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/5139/events" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]*github.IssueEvent{
				{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/5139/timeline" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]*github.TimelineEvent{
				{Event: "edited", CreatedAt: editTime, Actor: &github.User{Login: "pilot-bot"}},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5139", "/project-gh5381-timeline-bot-edit"
	seedStalledRow(t, store, "exec-stalled-timeline-bot-edit", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })
	t.Cleanup(func() { stalledRearmNoEvidenceStreaks.reset(key) })

	rearmed, err := checker.tryRearmStalled(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rearmed {
		t.Fatal("expected rearmed=false — the 'edited' event was authored by the bot account itself, not an operator")
	}
}

// forceRepickBackoffReady clears key's backoff window so the very next
// gateStatus/allow check reports not-gated, regardless of how recently a
// drop was recorded. Test-only: reaches into repickBackoffTracker's
// unexported fields (same package) to simulate several sweep intervals
// elapsing without an actual test sleep.
func forceRepickBackoffReady(key string) {
	repickBackoff.mu.Lock()
	defer repickBackoff.mu.Unlock()
	if e, ok := repickBackoff.entries[key]; ok {
		e.nextAllowedAt = time.Time{}
		e.gateLogged = false
	}
}

// gh5381EscalationServer is a stateful mock GitHub server shared by
// TestSweepStalledRearm_NoEvidenceThreeTimes_EscalatesAndStops and
// TestSweepStalledRearm_ProbeErrorThreeTimes_EscalatesLikeNoEvidence: the
// served issue's labels grow across calls (AddLabels mutates the same state
// ListIssues/GetIssue read back), mirroring how a real repo would reflect
// escalateStalledRearmNoEvidence's own pilot-needs-human application on the
// very next sweep pass. getIssueFails, when true, makes the per-issue GET
// error instead of succeeding — simulating a persistent GitHub API outage
// on the probe call specifically (ListIssues itself still succeeds, exactly
// like a real partial outage would look from the sweep's perspective).
type gh5381EscalationServer struct {
	mu            sync.Mutex
	labels        []string
	updatedAt     time.Time
	events        []*github.IssueEvent
	getIssueFails bool
	listCalls     int
	getCalls      int
	commentCalls  int
	addLabelCall  int
}

func newGH5381EscalationServer(t *testing.T, issueNum int, initialLabels []string, updatedAt time.Time, events []*github.IssueEvent, getIssueFails bool) (*httptest.Server, *gh5381EscalationServer) {
	t.Helper()
	state := &gh5381EscalationServer{labels: append([]string{}, initialLabels...), updatedAt: updatedAt, events: events, getIssueFails: getIssueFails}

	issuePath := "/repos/owner/repo/issues/" + strconv.Itoa(issueNum)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		state.mu.Lock()
		defer state.mu.Unlock()

		labelObjs := make([]github.Label, len(state.labels))
		for i, l := range state.labels {
			labelObjs[i] = github.Label{Name: l}
		}

		switch {
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodGet:
			state.listCalls++
			if p := r.URL.Query().Get("page"); p != "1" && p != "" {
				_ = json.NewEncoder(w).Encode([]*github.Issue{})
				return
			}
			_ = json.NewEncoder(w).Encode([]*github.Issue{
				{Number: issueNum, State: "open", Labels: labelObjs, UpdatedAt: state.updatedAt},
			})
		case r.URL.Path == issuePath && r.Method == http.MethodGet:
			state.getCalls++
			if state.getIssueFails {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"simulated outage"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(&github.Issue{
				Number: issueNum, State: "open", Labels: labelObjs, UpdatedAt: state.updatedAt,
			})
		case r.URL.Path == issuePath+"/events" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(state.events)
		case r.URL.Path == issuePath+"/timeline" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]*github.TimelineEvent{})
		case r.URL.Path == issuePath+"/comments" && r.Method == http.MethodPost:
			state.commentCalls++
			_ = json.NewEncoder(w).Encode(&github.Comment{ID: 1})
		case r.URL.Path == issuePath+"/labels" && r.Method == http.MethodPost:
			state.addLabelCall++
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			state.labels = append(state.labels, body.Labels...)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, state
}

// TestSweepStalledRearm_NoEvidenceThreeTimes_EscalatesAndStops is GH-5381's
// (carried forward from the reverted #5380/GH-5376) backstop acceptance
// test: a stalled task_id with zero re-arm evidence across repeated sweep
// passes must stop accumulating claim-lost drops after
// terminalDropPilotStripThreshold (3) consecutive no-evidence findings,
// escalate to pilot-needs-human with an explanatory comment instead, and
// never repeat the escalation or resume probing on subsequent passes.
func TestSweepStalledRearm_NoEvidenceThreeTimes_EscalatesAndStops(t *testing.T) {
	resetBotLoginCacheForTest(t)
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)

	// UpdatedAt/events both predate the stall — no evidence of any kind,
	// ever, across every sweep pass in this test.
	srv, state := newGH5381EscalationServer(t, 5346,
		[]string{"pilot", github.LabelBlocked},
		stallTime.Add(-2*time.Hour),
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
		},
		false,
	)

	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5346", "/project-gh5381-escalate"
	seedStalledRow(t, store, "exec-gh5346-stalled", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })
	t.Cleanup(func() { stalledRearmNoEvidenceStreaks.reset(key) })

	ctx := context.Background()

	// Sweep passes 1 and 2: ordinary no-evidence drops, no escalation yet.
	for i := 1; i <= 2; i++ {
		checker.sweepStalledRearm(ctx, projectPath)
		if _, _, claimLostDrops, _ := repickBackoff.gateDetail(key); claimLostDrops != i {
			t.Errorf("after sweep pass %d: expected claim_lost_drops=%d, got %d", i, i, claimLostDrops)
		}
		state.mu.Lock()
		comments, addLabels := state.commentCalls, state.addLabelCall
		state.mu.Unlock()
		if comments != 0 || addLabels != 0 {
			t.Fatalf("after sweep pass %d: expected no escalation yet, got comments=%d addLabelCalls=%d", i, comments, addLabels)
		}
		forceRepickBackoffReady(key)
	}

	// Sweep pass 3: the third consecutive no-evidence finding must escalate
	// instead of recording a third claim-lost drop.
	checker.sweepStalledRearm(ctx, projectPath)
	if _, _, claimLostDrops, _ := repickBackoff.gateDetail(key); claimLostDrops != 2 {
		t.Errorf("expected claim_lost_drops to stay at 2 (no further recordClaimLostDrop call on the escalating pass), got %d", claimLostDrops)
	}
	state.mu.Lock()
	comments, addLabels := state.commentCalls, state.addLabelCall
	labels := append([]string{}, state.labels...)
	state.mu.Unlock()
	if comments != 1 {
		t.Errorf("expected exactly 1 escalation comment posted, got %d", comments)
	}
	if addLabels != 1 {
		t.Errorf("expected exactly 1 AddLabels call for pilot-needs-human, got %d", addLabels)
	}
	if !containsString(labels, labelPilotNeedsHumanSDK) {
		t.Errorf("expected pilot-needs-human to have been applied, labels=%v", labels)
	}

	forceRepickBackoffReady(key)

	// Sweep pass 4: the issue now carries pilot-needs-human (reflected by the
	// stateful mock, exactly like a real repo would show on the next poll),
	// so the sweep's candidate filter must exclude it entirely — no further
	// GetIssue probe, no further drop, no repeat escalation.
	getCallsBefore := state.getCalls
	checker.sweepStalledRearm(ctx, projectPath)
	state.mu.Lock()
	getCallsAfter, commentsAfter, addLabelsAfter := state.getCalls, state.commentCalls, state.addLabelCall
	state.mu.Unlock()
	if getCallsAfter != getCallsBefore {
		t.Errorf("expected no further GetIssue probe once pilot-needs-human is applied, got %d new calls", getCallsAfter-getCallsBefore)
	}
	if commentsAfter != 1 || addLabelsAfter != 1 {
		t.Errorf("expected no repeat escalation, got comments=%d addLabelCalls=%d", commentsAfter, addLabelsAfter)
	}
	if _, _, claimLostDrops, _ := repickBackoff.gateDetail(key); claimLostDrops != 2 {
		t.Errorf("expected claim_lost_drops to remain 2 after the filtered-out pass, got %d", claimLostDrops)
	}

	exec, err := store.GetExecution("exec-gh5346-stalled")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "stalled" {
		t.Errorf("expected the row to remain status=stalled throughout (never re-armed), got %q", exec.Status)
	}
}

// TestSweepStalledRearm_ProbeErrorThreeTimes_EscalatesLikeNoEvidence is
// GH-5381's probe-error backstop test (item 3 of the task): a persistent
// GitHub API failure on the per-issue probe (GetIssue erroring every call,
// simulating an outage) must feed the SAME no-evidence streak an ordinary
// "checked, found nothing" result does — three consecutive probe errors
// escalate to pilot-needs-human exactly like three consecutive no-evidence
// sweeps, rather than calling recordClaimLostDrop forever with no backstop.
func TestSweepStalledRearm_ProbeErrorThreeTimes_EscalatesLikeNoEvidence(t *testing.T) {
	resetBotLoginCacheForTest(t)
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)

	srv, state := newGH5381EscalationServer(t, 5347,
		[]string{"pilot", github.LabelBlocked},
		stallTime.Add(-2*time.Hour),
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
		},
		true, // every GetIssue call fails
	)

	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5347", "/project-gh5381-probe-error"
	seedStalledRow(t, store, "exec-gh5347-stalled", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })
	t.Cleanup(func() { stalledRearmNoEvidenceStreaks.reset(key) })

	ctx := context.Background()

	for i := 1; i <= 2; i++ {
		checker.sweepStalledRearm(ctx, projectPath)
		state.mu.Lock()
		comments, addLabels := state.commentCalls, state.addLabelCall
		state.mu.Unlock()
		if comments != 0 || addLabels != 0 {
			t.Fatalf("after sweep pass %d: expected no escalation yet, got comments=%d addLabelCalls=%d", i, comments, addLabels)
		}
		forceRepickBackoffReady(key)
	}

	// Sweep pass 3: the third consecutive probe error must escalate exactly
	// like the third consecutive no-evidence result would.
	checker.sweepStalledRearm(ctx, projectPath)
	state.mu.Lock()
	comments, addLabels := state.commentCalls, state.addLabelCall
	labels := append([]string{}, state.labels...)
	getCalls := state.getCalls
	state.mu.Unlock()
	if getCalls != 3 {
		t.Errorf("expected exactly 3 GetIssue probe attempts, got %d", getCalls)
	}
	if comments != 1 {
		t.Errorf("expected exactly 1 escalation comment posted after 3 consecutive probe errors, got %d", comments)
	}
	if addLabels != 1 {
		t.Errorf("expected exactly 1 AddLabels call for pilot-needs-human, got %d", addLabels)
	}
	if !containsString(labels, labelPilotNeedsHumanSDK) {
		t.Errorf("expected pilot-needs-human to have been applied, labels=%v", labels)
	}

	exec, err := store.GetExecution("exec-gh5347-stalled")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "stalled" {
		t.Errorf("expected the row to remain status=stalled throughout (never re-armed), got %q", exec.Status)
	}
}

// TestRecordStalledRearmMiss_LabelFailure_StreakNotReset is GH-5381's
// label-failure-handling test: when the escalation's AddLabels call fails,
// the no-evidence streak must NOT be reset — otherwise pilot-needs-human
// never lands (the issue stays a sweep candidate) and the streak silently
// drops back below threshold, so the very next miss just resumes ordinary
// claim-lost-drop backoff instead of retrying the label application.
func TestRecordStalledRearmMiss_LabelFailure_StreakNotReset(t *testing.T) {
	var commentCalls, addLabelCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodPost:
			commentCalls++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&github.Comment{ID: 1})
		case strings.HasSuffix(r.URL.Path, "/labels") && r.Method == http.MethodPost:
			addLabelCalls++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"simulated label failure"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	checker := terminalCompletionChecker{
		ghClient:  github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-9001", "/project-gh5381-label-failure"
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })
	t.Cleanup(func() { stalledRearmNoEvidenceStreaks.reset(key) })

	ctx := context.Background()
	// Drive the streak to exactly the escalation threshold.
	for i := 1; i < terminalDropPilotStripThreshold; i++ {
		checker.recordStalledRearmMiss(ctx, taskID, 9001, key)
	}
	// This miss crosses the threshold and attempts escalation; AddLabels
	// fails.
	checker.recordStalledRearmMiss(ctx, taskID, 9001, key)
	if addLabelCalls != 1 {
		t.Fatalf("expected 1 AddLabels attempt, got %d", addLabelCalls)
	}
	if commentCalls != 1 {
		t.Fatalf("expected 1 escalation comment attempt, got %d", commentCalls)
	}

	// The streak must still be at/above threshold, so the very next miss
	// retries escalation (comment + label) rather than quietly falling back
	// to recordClaimLostDrop.
	checker.recordStalledRearmMiss(ctx, taskID, 9001, key)
	if addLabelCalls != 2 {
		t.Errorf("expected the label application to be retried on the next miss (streak not reset), got %d AddLabels calls", addLabelCalls)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}
