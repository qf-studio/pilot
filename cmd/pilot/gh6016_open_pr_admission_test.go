package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
)

// GH-6020: Controller.redriveHeldPR deliberately strips pilot-needs-human
// when a new commit lands on a held PR, a failed execution row is not
// terminal, and the SDK poller's candidate filter never looks at PRs — so the
// issue was re-admitted and its PR's work re-implemented (issue 6016 / PR
// 6017, 2026-11-03). These tests cover the open-PR admission gate in
// handleGithubIssueEventSDK.

// openPRAdmissionServer is a fake GitHub API: GET issue returns an open issue
// labelled "pilot" only; GET pulls is served by pullsHandler; any other
// method is recorded as a write.
type openPRAdmissionServer struct {
	mu        sync.Mutex
	writes    []string
	pullsHits int
	allHits   int
}

func (s *openPRAdmissionServer) snapshot() (writes []string, pullsHits, allHits int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...), s.pullsHits, s.allHits
}

func newOpenPRAdmissionServer(t *testing.T, issueNum int, extraLabels []string, pullsHandler func(w http.ResponseWriter, r *http.Request)) *openPRAdmissionServer {
	t.Helper()
	s := &openPRAdmissionServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.allHits++
		if r.Method != http.MethodGet {
			s.writes = append(s.writes, r.Method+" "+r.URL.Path)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
			return
		}
		isPulls := strings.HasSuffix(r.URL.Path, "/pulls")
		if isPulls {
			s.pullsHits++
		}
		s.mu.Unlock()
		if isPulls {
			pullsHandler(w, r)
			return
		}
		labels := []map[string]string{{"name": "pilot"}}
		for _, l := range extraLabels {
			labels = append(labels, map[string]string{"name": l})
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"number": issueNum,
			"title":  "open pr gate",
			"body":   "body",
			"state":  "open",
			"labels": labels,
			"user":   map[string]string{"login": "someone"},
		}); err != nil {
			t.Errorf("encode issue: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	prev := githubTaskClientOpts
	githubTaskClientOpts = []githubSDK.ClientOption{githubSDK.WithClientBaseURL(srv.URL)}
	t.Cleanup(func() { githubTaskClientOpts = prev })
	return s
}

func newOpenPRTestDispatcher(t *testing.T) (*executor.Dispatcher, *memory.Store) {
	t.Helper()
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	dispatcher := executor.NewDispatcher(store, executor.NewRunner(), nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("dispatcher.Start: %v", err)
	}
	t.Cleanup(dispatcher.Stop)
	return dispatcher, store
}

func openPRTestConfig() *config.Config {
	return &config.Config{Adapters: &config.AdaptersConfig{GitHub: &github.Config{
		Token:      testutil.FakeGitHubToken,
		PilotLabel: "pilot",
	}}}
}

func writeOpenPRList(t *testing.T, w http.ResponseWriter, branch string) {
	t.Helper()
	if err := json.NewEncoder(w).Encode([]map[string]any{{
		"number": 6017,
		"state":  "open",
		"head":   map[string]string{"ref": branch},
	}}); err != nil {
		t.Errorf("encode pulls: %v", err)
	}
}

func TestHandleGithubIssueEventSDK_OpenPRBlocksAdmission(t *testing.T) {
	const (
		owner = "acme"
		repo  = "widgets"
	)
	tests := []struct {
		name    string
		issueNo int
	}{
		{name: "open PR on pilot branch is skipped", issueNo: 60201},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskID := fmt.Sprintf("GH-%d", tt.issueNo)
			srv := newOpenPRAdmissionServer(t, tt.issueNo, nil, func(w http.ResponseWriter, r *http.Request) {
				if got, want := r.URL.Query().Get("head"), owner+":pilot/"+taskID; got != want {
					t.Errorf("pulls head = %q, want %q", got, want)
				}
				writeOpenPRList(t, w, "pilot/"+taskID)
			})
			dispatcher, store := newOpenPRTestDispatcher(t)

			projectPath := "/nonexistent-gh6020-block-project"
			backoffKey := repickBackoffKey(projectPath, taskID)
			t.Cleanup(func() { repickBackoff.recordSuccess(backoffKey) })

			ev := sdkcore.IssueEvent{
				SequenceID: taskID,
				IssueID:    fmt.Sprint(tt.issueNo),
				Title:      "open pr gate",
				Labels:     []string{"pilot"},
			}
			result, err := handleGithubIssueEventSDK(context.Background(), openPRTestConfig(), ev, projectPath, owner+"/"+repo, dispatcher, nil, nil, nil, nil, nil, nil)
			if err != nil {
				t.Fatalf("handleGithubIssueEventSDK() error = %v, want nil", err)
			}
			if result == nil || result.Success || !result.Skipped || result.SkipReason != skipReasonOpenPR {
				t.Fatalf("result = %+v, want Success=false Skipped=true SkipReason=%q", result, skipReasonOpenPR)
			}
			if skipReasonOpenPR != "open_pr" {
				t.Errorf("skipReasonOpenPR = %q, want open_pr", skipReasonOpenPR)
			}

			writes, pullsHits, _ := srv.snapshot()
			if len(writes) != 0 {
				t.Errorf("skip must add no pilot-in-progress label and post no comment, got writes %v", writes)
			}
			if pullsHits != 1 {
				t.Errorf("pulls endpoint hits = %d, want 1", pullsHits)
			}
			if dispatcher.IsActive(taskID, projectPath) {
				t.Error("open-PR issue must never become an active dispatch")
			}
			if exec, _ := store.GetLatestExecutionByTaskID(taskID, projectPath); exec != nil {
				t.Errorf("expected no execution row, got %+v", exec)
			}
			if repickBackoff.allow(backoffKey) {
				t.Error("expected the repick backoff to be armed by the open-PR skip")
			}
		})
	}
}

func TestHandleGithubIssueEventSDK_NoOpenPRAdmits(t *testing.T) {
	const (
		owner = "acme"
		repo  = "widgets"
	)
	tests := []struct {
		name    string
		issueNo int
		pulls   string
	}{
		{name: "empty pulls list", issueNo: 60211, pulls: `[]`},
		{name: "pulls list for a different head ref", issueNo: 60212, pulls: `[{"number":1,"state":"open","head":{"ref":"pilot/GH-1"}}]`},
		{name: "closed PR on the branch", issueNo: 60213, pulls: `[{"number":2,"state":"closed","head":{"ref":"pilot/GH-60213"}}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newOpenPRAdmissionServer(t, tt.issueNo, []string{"pilot-skip-spec-check"}, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tt.pulls))
			})
			dispatcher, _ := newOpenPRTestDispatcher(t)

			taskID := fmt.Sprintf("GH-%d", tt.issueNo)
			projectPath := "/nonexistent-gh6020-admit-project"
			backoffKey := repickBackoffKey(projectPath, taskID)
			t.Cleanup(func() { repickBackoff.recordSuccess(backoffKey) })

			ev := sdkcore.IssueEvent{
				SequenceID: taskID,
				IssueID:    fmt.Sprint(tt.issueNo),
				Title:      "open pr gate",
				Labels:     []string{"pilot", "pilot-skip-spec-check"},
			}
			result, _ := handleGithubIssueEventSDK(context.Background(), openPRTestConfig(), ev, projectPath, owner+"/"+repo, dispatcher, nil, nil, nil, nil, nil, nil)
			if result != nil && result.Skipped && result.SkipReason == skipReasonOpenPR {
				t.Fatalf("no open PR must not be skipped as open_pr, got %+v", result)
			}
			if _, pullsHits, _ := srv.snapshot(); pullsHits != 1 {
				t.Errorf("pulls endpoint hits = %d, want 1 (gate must consult the API)", pullsHits)
			}
		})
	}
}

func TestHandleGithubIssueEventSDK_OpenPRLookupErrorAdmits(t *testing.T) {
	const (
		owner = "acme"
		repo  = "widgets"
	)
	tests := []struct {
		name    string
		issueNo int
		status  int
	}{
		{name: "pulls endpoint 500 fails open", issueNo: 60221, status: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newOpenPRAdmissionServer(t, tt.issueNo, []string{"pilot-skip-spec-check"}, func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"message":"boom"}`, tt.status)
			})
			dispatcher, _ := newOpenPRTestDispatcher(t)

			taskID := fmt.Sprintf("GH-%d", tt.issueNo)
			projectPath := "/nonexistent-gh6020-error-project"
			backoffKey := repickBackoffKey(projectPath, taskID)
			t.Cleanup(func() { repickBackoff.recordSuccess(backoffKey) })

			ev := sdkcore.IssueEvent{
				SequenceID: taskID,
				IssueID:    fmt.Sprint(tt.issueNo),
				Title:      "open pr gate",
				Labels:     []string{"pilot", "pilot-skip-spec-check"},
			}
			result, _ := handleGithubIssueEventSDK(context.Background(), openPRTestConfig(), ev, projectPath, owner+"/"+repo, dispatcher, nil, nil, nil, nil, nil, nil)
			if result != nil && result.Skipped {
				t.Fatalf("a lookup error must admit, got skip %+v", result)
			}
			if _, pullsHits, _ := srv.snapshot(); pullsHits == 0 {
				t.Error("expected the gate to have attempted the pulls lookup")
			}
		})
	}
}

func TestHandleGithubIssueEventSDK_OpenPRArmsRepickBackoff(t *testing.T) {
	const (
		owner   = "acme"
		repo    = "widgets"
		issueNo = 60231
	)
	taskID := fmt.Sprintf("GH-%d", issueNo)
	srv := newOpenPRAdmissionServer(t, issueNo, nil, func(w http.ResponseWriter, r *http.Request) {
		writeOpenPRList(t, w, "pilot/"+taskID)
	})
	dispatcher, _ := newOpenPRTestDispatcher(t)

	projectPath := "/nonexistent-gh6020-backoff-project"
	backoffKey := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(backoffKey) })

	ev := sdkcore.IssueEvent{
		SequenceID: taskID,
		IssueID:    fmt.Sprint(issueNo),
		Title:      "open pr gate",
		Labels:     []string{"pilot"},
	}
	if !repickBackoff.allow(backoffKey) {
		t.Fatal("precondition: backoff must be unarmed before the first dispatch")
	}

	result1, err := handleGithubIssueEventSDK(context.Background(), openPRTestConfig(), ev, projectPath, owner+"/"+repo, dispatcher, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("first call error = %v, want nil", err)
	}
	if result1 == nil || !result1.Skipped || result1.SkipReason != skipReasonOpenPR {
		t.Fatalf("first call = %+v, want Skipped open_pr", result1)
	}
	drops, found, err := dispatcher.ClaimLostDropCount(backoffKey)
	if err != nil {
		t.Fatalf("ClaimLostDropCount: %v", err)
	}
	if !found || drops != 1 {
		t.Errorf("after first call: claim-lost drops = %d (found=%v), want 1", drops, found)
	}
	_, _, hitsAfterFirst := srv.snapshot()

	// Second event inside the cooldown: dropped by the allow check before any
	// API call, and the drop counter is not grown again.
	result2, err := handleGithubIssueEventSDK(context.Background(), openPRTestConfig(), ev, projectPath, owner+"/"+repo, dispatcher, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("second call error = %v, want nil", err)
	}
	if result2 == nil || !result2.Skipped {
		t.Fatalf("second call = %+v, want Skipped", result2)
	}
	if _, _, hitsAfterSecond := srv.snapshot(); hitsAfterSecond != hitsAfterFirst {
		t.Errorf("second call made %d API requests, want 0 (backoff must short-circuit)", hitsAfterSecond-hitsAfterFirst)
	}
	drops2, _, err := dispatcher.ClaimLostDropCount(backoffKey)
	if err != nil {
		t.Fatalf("ClaimLostDropCount (second): %v", err)
	}
	if drops2 != 1 {
		t.Errorf("after second call: claim-lost drops = %d, want still 1", drops2)
	}
}
