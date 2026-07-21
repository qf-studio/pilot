package autopilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// --- unsourcedLabeledIssueNumbers (pure set logic, GH-4488 acceptance:
// "table-driven tests for the unsourced-detection set logic") ---

func TestUnsourcedLabeledIssueNumbers(t *testing.T) {
	tests := []struct {
		name           string
		labeled        []*github.Issue
		sourcedNumbers map[int]bool
		want           []int
	}{
		{
			name:           "no labeled issues",
			labeled:        nil,
			sourcedNumbers: map[int]bool{},
			want:           nil,
		},
		{
			name:           "all labeled issues sourced",
			labeled:        []*github.Issue{{Number: 1}, {Number: 2}},
			sourcedNumbers: map[int]bool{1: true, 2: true},
			want:           nil,
		},
		{
			name:           "all labeled issues unsourced (empty board)",
			labeled:        []*github.Issue{{Number: 1}, {Number: 2}},
			sourcedNumbers: map[int]bool{},
			want:           []int{1, 2},
		},
		{
			name:           "mixed: some sourced, some not",
			labeled:        []*github.Issue{{Number: 1}, {Number: 2}, {Number: 3}},
			sourcedNumbers: map[int]bool{2: true},
			want:           []int{1, 3},
		},
		{
			name:           "board has extra items not in the labeled set (ignored)",
			labeled:        []*github.Issue{{Number: 1}},
			sourcedNumbers: map[int]bool{1: true, 99: true},
			want:           nil,
		},
		{
			name:           "nil entries in labeled are skipped defensively",
			labeled:        []*github.Issue{nil, {Number: 1}},
			sourcedNumbers: map[int]bool{},
			want:           []int{1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unsourcedLabeledIssueNumbers(tt.labeled, tt.sourcedNumbers)
			if len(got) != len(tt.want) {
				t.Fatalf("unsourcedLabeledIssueNumbers() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("unsourcedLabeledIssueNumbers()[%d] = %d, want %d", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// --- reconcileUnsourcedBoardIssues integration (REST issues + GraphQL board) ---

// boardSourceFakeIssue describes one open issue served by the fake REST
// endpoint used across TestReconcileUnsourcedBoardIssues subtests.
type boardSourceFakeIssue struct {
	number int
	labels []string
}

// boardSourceServer serves GET /repos/owner/repo/issues (ListIssues) from
// issues, and POST /graphql with the given sequence of raw JSON response
// bodies (org-project-ID resolution first, then one items-page response per
// reconcile call — ProjectBoardSource caches the resolved project ID after
// the first call, so only the first reconcile pays the resolution query).
func boardSourceServer(t *testing.T, issues []boardSourceFakeIssue, graphqlResponses []string) *httptest.Server {
	t.Helper()
	idx := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			if idx >= len(graphqlResponses) {
				t.Fatalf("unexpected GraphQL request #%d (only %d responses configured)", idx+1, len(graphqlResponses))
			}
			resp := graphqlResponses[idx]
			idx++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
			return
		}

		if r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Issue{})
			return
		}
		out := make([]github.Issue, 0, len(issues))
		for _, iss := range issues {
			labels := make([]github.Label, 0, len(iss.labels))
			for _, name := range iss.labels {
				labels = append(labels, github.Label{Name: name})
			}
			out = append(out, github.Issue{Number: iss.number, State: github.StateOpen, Labels: labels})
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(server.Close)
	return server
}

// boardSourceOrgProjectResp is the org-project-ID-resolution GraphQL response
// ProjectBoardSource.ensureProjectID issues once per instance and caches.
func boardSourceOrgProjectResp(projectID string) string {
	return fmt.Sprintf(`{"data":{"organization":{"projectV2":{"id":%q}}}}`, projectID)
}

// boardSourceItemsResp is a single-page (no pagination) project-items
// GraphQL response containing nodes.
func boardSourceItemsResp(nodes ...map[string]interface{}) string {
	data := map[string]interface{}{
		"node": map[string]interface{}{
			"items": map[string]interface{}{
				"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
				"nodes":    nodes,
			},
		},
	}
	b, err := json.Marshal(map[string]interface{}{"data": data})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// boardSourceIssueNode builds one project-board item node representing an
// open issue in owner/repo, in the given status column.
func boardSourceIssueNode(number int, nodeID, status string) map[string]interface{} {
	return map[string]interface{}{
		"content": map[string]interface{}{
			"number":     number,
			"id":         nodeID,
			"title":      "t",
			"body":       "",
			"state":      "OPEN",
			"labels":     map[string]interface{}{"nodes": []map[string]interface{}{}},
			"repository": map[string]interface{}{"nameWithOwner": "owner/repo"},
		},
		"fieldValueByName": map[string]interface{}{"name": status},
	}
}

func newBoardSourceController(t *testing.T, issues []boardSourceFakeIssue, graphqlResponses []string) *Controller {
	t.Helper()
	server := boardSourceServer(t, issues, graphqlResponses)
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	src := github.NewProjectBoardSource(ghClient, &github.ProjectBoardConfig{ProjectNumber: 1, StatusField: "Status"}, "owner", "repo")
	return NewController(cfg, ghClient, nil, "owner", "repo", WithProjectBoardSource(src, "Todo"))
}

// TestReconcileUnsourcedBoardIssues_NoBoardSource verifies the sweep is a
// silent no-op when WithProjectBoardSource was never applied (GH-4488:
// project_board.source_enabled is off for this repo, or board sync isn't
// configured at all) — no HTTP calls of any kind.
func TestReconcileUnsourcedBoardIssues_NoBoardSource(t *testing.T) {
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.reconcileUnsourcedBoardIssues(context.Background())

	if got := c.metrics.Snapshot().UnsourcedLabeledIssues["owner/repo"]; got != 0 {
		t.Errorf("expected gauge to stay unset with no board source wired, got %d", got)
	}
}

// TestReconcileUnsourcedBoardIssues_AllSourced verifies every open labeled
// issue that has a matching Todo-column board card produces no warning and
// a zero gauge.
func TestReconcileUnsourcedBoardIssues_AllSourced(t *testing.T) {
	c := newBoardSourceController(t,
		[]boardSourceFakeIssue{{number: 1, labels: []string{github.LabelPilot}}},
		[]string{
			boardSourceOrgProjectResp("PVT_1"),
			boardSourceItemsResp(boardSourceIssueNode(1, "I_1", "Todo")),
		},
	)

	c.reconcileUnsourcedBoardIssues(context.Background())

	if got := c.metrics.Snapshot().UnsourcedLabeledIssues["owner/repo"]; got != 0 {
		t.Errorf("expected gauge 0 when every labeled issue is sourced, got %d", got)
	}
	if len(c.warnedUnsourcedIssues) != 0 {
		t.Errorf("expected no warned issues, got %v", c.warnedUnsourcedIssues)
	}
}

// TestReconcileUnsourcedBoardIssues_Unsourced verifies a labeled issue
// absent from the board (empty Todo column) is flagged: gauge goes nonzero
// and the issue is recorded as warned, deduplicated across a second
// consecutive tick (GH-4488: WARN once per poll-session, not per tick).
func TestReconcileUnsourcedBoardIssues_Unsourced(t *testing.T) {
	c := newBoardSourceController(t,
		[]boardSourceFakeIssue{{number: 136, labels: []string{github.LabelPilot}}},
		[]string{
			boardSourceOrgProjectResp("PVT_1"),
			boardSourceItemsResp(), // empty Todo column — board doesn't cover #136
			boardSourceItemsResp(), // second tick, project ID now cached
		},
	)

	c.reconcileUnsourcedBoardIssues(context.Background())

	if got := c.metrics.Snapshot().UnsourcedLabeledIssues["owner/repo"]; got != 1 {
		t.Fatalf("expected gauge 1 after first tick, got %d", got)
	}
	if !c.warnedUnsourcedIssues[136] {
		t.Errorf("expected issue 136 to be recorded as warned")
	}

	// Second consecutive tick: still unsourced, dedup keeps it recorded
	// (the WARN log itself isn't observable here, but the dedup state
	// driving it must not have been cleared).
	c.reconcileUnsourcedBoardIssues(context.Background())
	if got := c.metrics.Snapshot().UnsourcedLabeledIssues["owner/repo"]; got != 1 {
		t.Errorf("expected gauge to stay 1 on second tick, got %d", got)
	}
	if !c.warnedUnsourcedIssues[136] {
		t.Errorf("expected issue 136 to still be recorded as warned after second tick")
	}
}

// TestReconcileUnsourcedBoardIssues_RecoverySourcedClearsWarn verifies that
// once an issue's card lands in the Todo column, the WARN dedup entry is
// dropped (so a later recurrence — e.g. the card gets moved out again —
// warns again instead of staying silent forever) and the gauge drops to 0.
func TestReconcileUnsourcedBoardIssues_RecoverySourcedClearsWarn(t *testing.T) {
	c := newBoardSourceController(t,
		[]boardSourceFakeIssue{{number: 136, labels: []string{github.LabelPilot}}},
		[]string{
			boardSourceOrgProjectResp("PVT_1"),
			boardSourceItemsResp(),                                           // tick 1: not on board
			boardSourceItemsResp(boardSourceIssueNode(136, "I_136", "Todo")), // tick 2: now sourced
		},
	)

	c.reconcileUnsourcedBoardIssues(context.Background())
	if !c.warnedUnsourcedIssues[136] {
		t.Fatalf("expected issue 136 warned after tick 1")
	}

	c.reconcileUnsourcedBoardIssues(context.Background())
	if got := c.metrics.Snapshot().UnsourcedLabeledIssues["owner/repo"]; got != 0 {
		t.Errorf("expected gauge 0 once the issue is sourced, got %d", got)
	}
	if c.warnedUnsourcedIssues[136] {
		t.Errorf("expected warned entry for issue 136 to clear once sourced")
	}
}

// TestReconcileUnsourcedBoardIssues_WrongStatusStillUnsourced verifies an
// issue whose card exists but sits in a column other than source_status
// (e.g. "In Progress" instead of "Todo") counts as unsourced, same as one
// with no card at all — FindIssuesFromProject itself filters by column.
func TestReconcileUnsourcedBoardIssues_WrongStatusStillUnsourced(t *testing.T) {
	c := newBoardSourceController(t,
		[]boardSourceFakeIssue{{number: 5, labels: []string{github.LabelPilot}}},
		[]string{
			boardSourceOrgProjectResp("PVT_1"),
			boardSourceItemsResp(boardSourceIssueNode(5, "I_5", "In Progress")),
		},
	)

	c.reconcileUnsourcedBoardIssues(context.Background())

	if got := c.metrics.Snapshot().UnsourcedLabeledIssues["owner/repo"]; got != 1 {
		t.Errorf("expected gauge 1 for a card in the wrong column, got %d", got)
	}
}

// --- Part B: board-sync scope-failure alert (isInsufficientScopeError, alertBoardSyncScopeFailureOnce) ---

func TestIsInsufficientScopeError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "unrelated error", err: errors.New("network timeout"), want: false},
		{
			name: "insufficient scopes error",
			err:  errors.New("board sync: failed to update project item status: graphql error: INSUFFICIENT_SCOPES: 'projectV2' requires read:project, token has [gist, read:org, repo, workflow]"),
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInsufficientScopeError(tt.err); got != tt.want {
				t.Errorf("isInsufficientScopeError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func newBoardSyncScopeController(t *testing.T) (*Controller, *fakeAlertSink) {
	t.Helper()
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)
	return c, sink
}

// TestAlertBoardSyncScopeFailureOnce_NonScopeErrorIgnored verifies a
// transient board-sync error (rate limit, network blip, item not yet on the
// board) never fires the alert — only INSUFFICIENT_SCOPES does.
func TestAlertBoardSyncScopeFailureOnce_NonScopeErrorIgnored(t *testing.T) {
	c, sink := newBoardSyncScopeController(t)

	c.alertBoardSyncScopeFailureOnce(errors.New("temporary network error"))

	if len(sink.events) != 0 {
		t.Errorf("expected no alert for a non-scope error, got %d", len(sink.events))
	}
	if c.alertedBoardSyncScope {
		t.Errorf("expected alertedBoardSyncScope to stay false for a non-scope error")
	}
}

// TestAlertBoardSyncScopeFailureOnce_FiresOncePerBoot verifies an
// INSUFFICIENT_SCOPES failure fires exactly one config_error alert even
// across repeated calls (GH-4488 acceptance: "one alert-engine event per
// boot"), since every UpdateProjectItemStatus call site retries every poll
// tick for as long as the PR sits in that stage.
func TestAlertBoardSyncScopeFailureOnce_FiresOncePerBoot(t *testing.T) {
	c, sink := newBoardSyncScopeController(t)
	scopeErr := errors.New("INSUFFICIENT_SCOPES: 'projectV2' requires read:project, token has [gist, read:org, repo, workflow]")

	c.alertBoardSyncScopeFailureOnce(scopeErr)
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 alert after first scope failure, got %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Type != alerts.EventTypeConfigError {
		t.Errorf("expected config_error event type, got %s", ev.Type)
	}
	if ev.Metadata["repo"] != "owner/repo" {
		t.Errorf("expected repo=owner/repo, got %s", ev.Metadata["repo"])
	}

	// Repeated failures (e.g. across every board-sync call site on the same
	// tick, or the next tick) must not fire a second alert.
	c.alertBoardSyncScopeFailureOnce(scopeErr)
	c.alertBoardSyncScopeFailureOnce(errors.New("INSUFFICIENT_SCOPES again"))
	if len(sink.events) != 1 {
		t.Errorf("expected still 1 alert after repeated scope failures, got %d", len(sink.events))
	}
}
