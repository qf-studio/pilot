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

// GH-5348 subtask 3 originally built this as a belt-and-braces detector that
// ran once a fix PR merged and, on zero file overlap with the origin PR,
// stripped an already-applied pilot-superseded label. GH-5351 turns it into
// the gate itself: spawnFailureIssue (controller.go) no longer applies
// pilot-superseded the instant a fix ISSUE is created — the PR it replaces
// is now closed via the self-close marker (GH-5351), so
// checkExternalMergeOrClose skips notifyExternalClose for that close
// entirely and pilot-superseded is never written eagerly. This test now
// covers verifyFixPRDeliversSourceScope as the sole place pilot-superseded
// ever gets applied: only once the fix PR actually merges, and only when its
// changed files overlap with the origin PR it claims to continue.

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

func TestController_VerifyFixPRDeliversSourceScope(t *testing.T) {
	fixIssueBody := "Fixes CI failure.\n\n<!-- autopilot-meta branch:pilot/GH-100 pr:7 iteration:1 source:100 -->"

	tests := []struct {
		name                    string
		fixIssue                *github.Issue
		sourceIssue             *github.Issue
		fixPRFiles              []*github.PRFile
		originPRFiles           []*github.PRFile
		wantSupersede           bool // shared scope: source gets pilot-superseded applied
		wantMismatchNoSupersede bool // zero overlap: comment + alert, no label write
		wantNoReaction          bool // neither of the above: no label writes, no comment, no alert
	}{
		{
			name:     "shared file — source gets marked superseded",
			fixIssue: &github.Issue{Number: 200, State: github.StateClosed, Body: fixIssueBody, Labels: []github.Label{{Name: github.LabelDone}}},
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateOpen,
				Labels: []github.Label{{Name: github.LabelPilot}, {Name: github.LabelInProgress}},
			},
			fixPRFiles:    []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			originPRFiles: []*github.PRFile{{Filename: "internal/foo/bar.go"}, {Filename: "internal/foo/baz.go"}},
			wantSupersede: true,
		},
		{
			name:     "zero file overlap — source stays open, no supersede, flagged for review",
			fixIssue: &github.Issue{Number: 200, State: github.StateClosed, Body: fixIssueBody, Labels: []github.Label{{Name: github.LabelDone}}},
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateOpen,
				Labels: []github.Label{{Name: github.LabelPilot}, {Name: github.LabelInProgress}},
			},
			fixPRFiles:              []*github.PRFile{{Filename: "internal/unrelated/thing.go"}},
			originPRFiles:           []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			wantMismatchNoSupersede: true,
		},
		{
			name:     "zero overlap but source already closed — no reaction",
			fixIssue: &github.Issue{Number: 200, State: github.StateClosed, Body: fixIssueBody, Labels: []github.Label{{Name: github.LabelDone}}},
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateClosed,
				Labels: []github.Label{{Name: github.LabelDone}},
			},
			fixPRFiles:     []*github.PRFile{{Filename: "internal/unrelated/thing.go"}},
			originPRFiles:  []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			wantNoReaction: true,
		},
		{
			name:     "source already carries pilot-superseded — idempotent, no reaction",
			fixIssue: &github.Issue{Number: 200, State: github.StateClosed, Body: fixIssueBody, Labels: []github.Label{{Name: github.LabelDone}}},
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateOpen,
				Labels: []github.Label{{Name: github.LabelSuperseded}},
			},
			// Overlap is present, but the gate must still no-op — a previous
			// run (or a daemon restart mid-gate) already applied the label.
			fixPRFiles:     []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			originPRFiles:  []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			wantNoReaction: true,
		},
		{
			name:     "not a fix issue (no source: marker) — no reaction",
			fixIssue: &github.Issue{Number: 200, State: github.StateClosed, Body: "Just a regular merged PR's issue.", Labels: []github.Label{{Name: github.LabelDone}}},
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateOpen,
				Labels: []github.Label{{Name: github.LabelPilot}},
			},
			fixPRFiles:     []*github.PRFile{{Filename: "internal/unrelated/thing.go"}},
			originPRFiles:  []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			wantNoReaction: true,
		},
		{
			name:     "origin PR has no recorded files — skip rather than false-positive react",
			fixIssue: &github.Issue{Number: 200, State: github.StateClosed, Body: fixIssueBody, Labels: []github.Label{{Name: github.LabelDone}}},
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateOpen,
				Labels: []github.Label{{Name: github.LabelPilot}},
			},
			fixPRFiles:     []*github.PRFile{{Filename: "internal/unrelated/thing.go"}},
			originPRFiles:  nil,
			wantNoReaction: true,
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

			// No StateStore wired: exercises the live-fetch fallback path in
			// originScope (the "pr:7" marker + a live ListPullRequestFiles
			// call), same as a fix issue spawned before GH-5351's
			// RecordOriginScope migration.
			prState := &PRState{PRNumber: 55, IssueNumber: 200}
			c.verifyFixPRDeliversSourceScope(context.Background(), prState)

			switch {
			case tt.wantSupersede:
				if !srv.hasAddLabel(100, github.LabelSuperseded) {
					t.Errorf("expected %s to be added to source #100, calls=%v", github.LabelSuperseded, srv.addLabelCalls)
				}
				if len(sink.events) != 0 {
					t.Errorf("expected no alerts on a confirmed-delivery supersede, got %d", len(sink.events))
				}
				for _, c := range srv.comments {
					if strings.HasPrefix(c, "100:") {
						t.Errorf("expected no comment on a confirmed-delivery supersede, got %v", srv.comments)
					}
				}
			case tt.wantMismatchNoSupersede:
				if srv.hasAddLabel(100, github.LabelSuperseded) {
					t.Errorf("expected %s NOT to be added to source #100 on zero overlap, calls=%v", github.LabelSuperseded, srv.addLabelCalls)
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
			case tt.wantNoReaction:
				if len(srv.addLabelCalls) != 0 {
					t.Errorf("expected no label writes, got %v", srv.addLabelCalls)
				}
				if len(srv.removeLabelCalls) != 0 {
					t.Errorf("expected no label removals, got %v", srv.removeLabelCalls)
				}
				if len(sink.events) != 0 {
					t.Errorf("expected no alerts, got %d", len(sink.events))
				}
				if len(srv.comments) != 0 {
					t.Errorf("expected no comments, got %v", srv.comments)
				}
			}
		})
	}
}
