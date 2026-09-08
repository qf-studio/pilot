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

// TestTryRearmStalled_BodyEditAfterStall_Rearms is GH-5376's evidence-type
// acceptance test: the GH-5346 incident re-armed pilot-retry-1 with a body
// edit alone ("new branch from main") — no label event, no reopen. Before
// this fix, latestRearmEvent only recognized labeled/reopened timeline
// events, so tryRearmStalled kept reporting rearmed=false forever even
// though the operator had performed exactly the deliberate gesture the
// re-arm probe exists to detect.
func TestTryRearmStalled_BodyEditAfterStall_Rearms(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)
	editTime := stallTime.Add(30 * time.Minute)

	srv := newRearmTestServer(t,
		&github.Issue{
			Number: 5139, State: "open",
			Labels:    []github.Label{{Name: "pilot"}, {Name: github.LabelRetry1}},
			UpdatedAt: editTime,
		},
		[]*github.IssueEvent{
			// No labeled/reopened event after the stall — a body edit alone
			// is not surfaced as a labeled/reopened timeline event.
			{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
			{Event: "labeled", CreatedAt: stallTime.Add(-23 * time.Hour), Label: &github.Label{Name: github.LabelRetry1}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5139", "/project-gh5376-body-edit"
	seedStalledRow(t, store, "exec-stalled-body-edit", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })
	t.Cleanup(func() { stalledRearmNoEvidenceStreaks.reset(key) })

	rearmed, err := checker.tryRearmStalled(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rearmed {
		t.Fatal("expected rearmed=true — issue.UpdatedAt moved past the stall timestamp via a body edit")
	}

	exec, err := store.GetExecution("exec-stalled-body-edit")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected the stalled row to be reclassified to status=failed, got %q", exec.Status)
	}
}

// TestTryRearmStalled_NoUpdateAtAll_NotRearmed proves the UpdatedAt fallback
// doesn't over-trigger: an issue whose UpdatedAt predates the stall (nothing
// touched it since) must not be treated as re-armed.
func TestTryRearmStalled_NoUpdateAtAll_NotRearmed(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)

	srv := newRearmTestServer(t,
		&github.Issue{
			Number: 5139, State: "open",
			Labels:    []github.Label{{Name: "pilot"}, {Name: github.LabelBlocked}},
			UpdatedAt: stallTime.Add(-2 * time.Hour),
		},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5139", "/project-gh5376-no-update"
	seedStalledRow(t, store, "exec-stalled-no-update", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })
	t.Cleanup(func() { stalledRearmNoEvidenceStreaks.reset(key) })

	rearmed, err := checker.tryRearmStalled(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rearmed {
		t.Fatal("expected rearmed=false — issue.UpdatedAt predates the stall, no deliberate edit happened after it")
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

// gh5376EscalationServer is a stateful mock GitHub server for
// TestSweepStalledRearm_NoEvidenceThreeTimes_EscalatesAndStops: the served
// issue's labels grow across calls (AddLabels mutates the same state
// ListIssues/GetIssue read back), mirroring how a real repo would reflect
// escalateStalledRearmNoEvidence's own pilot-needs-human application on the
// very next sweep pass.
type gh5376EscalationServer struct {
	mu           sync.Mutex
	labels       []string
	listCalls    int
	getCalls     int
	commentCalls int
	addLabelCall int
}

func newGH5376EscalationServer(t *testing.T, issueNum int, initialLabels []string, updatedAt time.Time, events []*github.IssueEvent) (*httptest.Server, *gh5376EscalationServer) {
	t.Helper()
	state := &gh5376EscalationServer{labels: append([]string{}, initialLabels...)}

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
				{Number: issueNum, State: "open", Labels: labelObjs, UpdatedAt: updatedAt},
			})
		case r.URL.Path == issuePath && r.Method == http.MethodGet:
			state.getCalls++
			_ = json.NewEncoder(w).Encode(&github.Issue{
				Number: issueNum, State: "open", Labels: labelObjs, UpdatedAt: updatedAt,
			})
		case r.URL.Path == issuePath+"/events" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(events)
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

// TestSweepStalledRearm_NoEvidenceThreeTimes_EscalatesAndStops is GH-5376's
// backstop acceptance test: a stalled task_id with zero re-arm evidence
// across repeated sweep passes must stop accumulating claim-lost drops after
// terminalDropPilotStripThreshold (3) consecutive no-evidence findings,
// escalate to pilot-needs-human with an explanatory comment instead, and
// never repeat the escalation or resume probing on subsequent passes
// (reproducing GH-5346's 76-drops-over-33h loop bounded to exactly 3).
func TestSweepStalledRearm_NoEvidenceThreeTimes_EscalatesAndStops(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)

	// UpdatedAt/events both predate the stall — no evidence of any kind,
	// ever, across every sweep pass in this test.
	srv, state := newGH5376EscalationServer(t, 5346,
		[]string{"pilot", github.LabelBlocked},
		stallTime.Add(-2*time.Hour),
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
		},
	)

	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5346", "/project-gh5376-escalate"
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

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}
