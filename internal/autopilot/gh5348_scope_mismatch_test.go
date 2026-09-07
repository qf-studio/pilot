package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-5348 subtask 3: spawnFailureIssue (controller.go) marks a source issue
// pilot-superseded the instant its fix ISSUE is created — before that
// issue's own PR exists, let alone before its diff is known. Subtasks 1/2
// fixed the one confirmed way the eventual fix PR's diff could drop the
// origin's real changes (a deleted branch silently rebuilt from main); this
// covers the belt-and-braces detector that runs once the fix PR actually
// merges: if its changed files share nothing with the origin PR it claims
// to continue, the source issue must not stay silently parked under
// pilot-superseded.

type scopeMismatchGHServer struct {
	mu sync.Mutex

	issues  map[int]*github.Issue    // GET /issues/{n}
	prFiles map[int][]*github.PRFile // GET /pulls/{n}/files

	addLabelCalls    []string // "issue:label"
	removeLabelCalls []string // "issue:label"
	comments         []string // "issue:body"
}

func newScopeMismatchGHServer() *scopeMismatchGHServer {
	return &scopeMismatchGHServer{
		issues:  map[int]*github.Issue{},
		prFiles: map[int][]*github.PRFile{},
	}
}

func (s *scopeMismatchGHServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/files"):
			var num int
			_, _ = fmt.Sscanf(r.URL.Path, "/repos/owner/repo/pulls/%d/files", &num)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(s.prFiles[num])
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/") && !strings.HasSuffix(r.URL.Path, "/labels"):
			var num int
			if _, err := fmt.Sscanf(r.URL.Path, "/repos/owner/repo/issues/%d", &num); err == nil {
				if issue, ok := s.issues[num]; ok {
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(issue)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			var num int
			_, _ = fmt.Sscanf(r.URL.Path, "/repos/owner/repo/issues/%d/labels", &num)
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, l := range body.Labels {
				s.addLabelCalls = append(s.addLabelCalls, fmt.Sprintf("%d:%s", num, l))
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/labels/"):
			parts := strings.Split(r.URL.Path, "/")
			label := parts[len(parts)-1]
			var num int
			_, _ = fmt.Sscanf(r.URL.Path, "/repos/owner/repo/issues/%d/labels/", &num)
			s.removeLabelCalls = append(s.removeLabelCalls, fmt.Sprintf("%d:%s", num, label))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			var num int
			_, _ = fmt.Sscanf(r.URL.Path, "/repos/owner/repo/issues/%d/comments", &num)
			var body struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.comments = append(s.comments, fmt.Sprintf("%d:%s", num, body.Body))
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.Comment{ID: 1, Body: body.Body})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}
}

func (s *scopeMismatchGHServer) hasAddLabel(issue int, label string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := fmt.Sprintf("%d:%s", issue, label)
	for _, c := range s.addLabelCalls {
		if c == want {
			return true
		}
	}
	return false
}

func (s *scopeMismatchGHServer) hasRemoveLabel(issue int, label string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := fmt.Sprintf("%d:%s", issue, label)
	for _, c := range s.removeLabelCalls {
		if c == want {
			return true
		}
	}
	return false
}

func TestController_VerifyFixPRDeliversSourceScope(t *testing.T) {
	fixIssueBody := "Fixes CI failure.\n\n<!-- autopilot-meta branch:pilot/GH-100 pr:7 iteration:1 source:100 -->"

	tests := []struct {
		name                string
		fixIssue            *github.Issue
		sourceIssue         *github.Issue
		fixPRFiles          []*github.PRFile
		originPRFiles       []*github.PRFile
		wantEscalate        bool
		wantLabelsUntouched bool
	}{
		{
			name:     "shared file — no escalation, source stays superseded",
			fixIssue: &github.Issue{Number: 200, State: github.StateClosed, Body: fixIssueBody, Labels: []github.Label{{Name: github.LabelDone}}},
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateOpen,
				Labels: []github.Label{{Name: github.LabelSuperseded}},
			},
			fixPRFiles:          []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			originPRFiles:       []*github.PRFile{{Filename: "internal/foo/bar.go"}, {Filename: "internal/foo/baz.go"}},
			wantLabelsUntouched: true,
		},
		{
			name:     "zero file overlap — source escalated to needs-human",
			fixIssue: &github.Issue{Number: 200, State: github.StateClosed, Body: fixIssueBody, Labels: []github.Label{{Name: github.LabelDone}}},
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateOpen,
				Labels: []github.Label{{Name: github.LabelSuperseded}},
			},
			fixPRFiles:    []*github.PRFile{{Filename: "internal/unrelated/thing.go"}},
			originPRFiles: []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			wantEscalate:  true,
		},
		{
			name:     "zero overlap but source already closed — no reaction",
			fixIssue: &github.Issue{Number: 200, State: github.StateClosed, Body: fixIssueBody, Labels: []github.Label{{Name: github.LabelDone}}},
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateClosed,
				Labels: []github.Label{{Name: github.LabelSuperseded}},
			},
			fixPRFiles:          []*github.PRFile{{Filename: "internal/unrelated/thing.go"}},
			originPRFiles:       []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			wantLabelsUntouched: true,
		},
		{
			name:     "zero overlap but source no longer carries pilot-superseded — no reaction",
			fixIssue: &github.Issue{Number: 200, State: github.StateClosed, Body: fixIssueBody, Labels: []github.Label{{Name: github.LabelDone}}},
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateOpen,
				Labels: []github.Label{{Name: github.LabelRetryReady}},
			},
			fixPRFiles:          []*github.PRFile{{Filename: "internal/unrelated/thing.go"}},
			originPRFiles:       []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			wantLabelsUntouched: true,
		},
		{
			name:     "not a fix issue (no source: marker) — no reaction",
			fixIssue: &github.Issue{Number: 200, State: github.StateClosed, Body: "Just a regular merged PR's issue.", Labels: []github.Label{{Name: github.LabelDone}}},
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateOpen,
				Labels: []github.Label{{Name: github.LabelSuperseded}},
			},
			fixPRFiles:          []*github.PRFile{{Filename: "internal/unrelated/thing.go"}},
			originPRFiles:       []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			wantLabelsUntouched: true,
		},
		{
			name:     "origin PR has no recorded files — skip rather than false-positive escalate",
			fixIssue: &github.Issue{Number: 200, State: github.StateClosed, Body: fixIssueBody, Labels: []github.Label{{Name: github.LabelDone}}},
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateOpen,
				Labels: []github.Label{{Name: github.LabelSuperseded}},
			},
			fixPRFiles:          []*github.PRFile{{Filename: "internal/unrelated/thing.go"}},
			originPRFiles:       nil,
			wantLabelsUntouched: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newScopeMismatchGHServer()
			srv.issues[200] = tt.fixIssue
			srv.issues[100] = tt.sourceIssue
			srv.prFiles[55] = tt.fixPRFiles   // fix PR number
			srv.prFiles[7] = tt.originPRFiles // origin PR number, from "pr:7" in fixIssueBody
			ts := httptest.NewServer(srv.handler())
			defer ts.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, ts.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")
			sink := &fakeAlertSink{}
			c.SetAlertsEngine(sink)

			prState := &PRState{PRNumber: 55, IssueNumber: 200}
			c.verifyFixPRDeliversSourceScope(context.Background(), prState)

			if tt.wantEscalate {
				if !srv.hasAddLabel(100, labelNeedsHuman) {
					t.Errorf("expected %s to be added to source #100, calls=%v", labelNeedsHuman, srv.addLabelCalls)
				}
				if !srv.hasRemoveLabel(100, github.LabelSuperseded) {
					t.Errorf("expected %s to be removed from source #100, calls=%v", github.LabelSuperseded, srv.removeLabelCalls)
				}
				if len(sink.events) != 1 {
					t.Errorf("expected exactly 1 alert, got %d", len(sink.events))
				}
				foundComment := false
				for _, c := range srv.comments {
					if strings.HasPrefix(c, "100:") && strings.Contains(c, "Delivery mismatch") {
						foundComment = true
					}
				}
				if !foundComment {
					t.Errorf("expected a delivery-mismatch comment on source #100, got %v", srv.comments)
				}
			}
			if tt.wantLabelsUntouched {
				if len(srv.addLabelCalls) != 0 {
					t.Errorf("expected no label writes, got %v", srv.addLabelCalls)
				}
				if len(srv.removeLabelCalls) != 0 {
					t.Errorf("expected no label removals, got %v", srv.removeLabelCalls)
				}
				if len(sink.events) != 0 {
					t.Errorf("expected no alerts, got %d", len(sink.events))
				}
			}
		})
	}
}
