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

// TestClassifyOwnerHealth covers the pure classifier GH-4842 relies on to
// tell "still might ship" (ownerAlive), "did its job" (ownerShipped), and
// "died without shipping" (ownerDead) apart.
func TestClassifyOwnerHealth(t *testing.T) {
	tests := []struct {
		name  string
		issue *github.Issue
		want  ownerHealth
	}{
		{
			name:  "nil issue is alive",
			issue: nil,
			want:  ownerAlive,
		},
		{
			name:  "open issue is alive",
			issue: &github.Issue{Number: 1, State: github.StateOpen},
			want:  ownerAlive,
		},
		{
			name:  "closed with pilot-done is shipped, not death",
			issue: &github.Issue{Number: 2, State: github.StateClosed, Labels: []github.Label{{Name: github.LabelDone}}},
			want:  ownerShipped,
		},
		{
			name:  "closed without pilot-done is dead",
			issue: &github.Issue{Number: 3, State: github.StateClosed, Labels: []github.Label{{Name: github.LabelRetryExhausted}}},
			want:  ownerDead,
		},
		{
			name:  "closed with no labels at all is dead",
			issue: &github.Issue{Number: 4, State: github.StateClosed},
			want:  ownerDead,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyOwnerHealth(tt.issue); got != tt.want {
				t.Errorf("classifyOwnerHealth() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestParseFixIssueSource covers recovering the source issue number from a
// spawned fix issue's body — the mechanism GH-4842 uses instead of adding
// new persistence for the designation itself.
func TestParseFixIssueSource(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantNum int
		wantOK  bool
	}{
		{
			name:    "well-formed autopilot-spawned body",
			body:    "Some intro text.\n\nDepends on: #123\n\n<!-- autopilot-meta branch:pilot/GH-42 pr:42 iteration:1 -->",
			wantNum: 123,
			wantOK:  true,
		},
		{
			name:    "no autopilot-meta marker at all — not a Pilot-spawned fix issue",
			body:    "Depends on: #123\n\nJust a regular issue mentioning a dependency.",
			wantNum: 0,
			wantOK:  false,
		},
		{
			name:    "autopilot-meta present but no Depends-on line",
			body:    "Some fix issue body.\n\n<!-- autopilot-meta branch:pilot/GH-42 pr:42 iteration:1 -->",
			wantNum: 0,
			wantOK:  false,
		},
		{
			name:    "Depends-on inline (not line-anchored) must not match — avoids epic-decomposition false positive",
			body:    "See also Depends on: #123 mentioned inline.\n\n<!-- autopilot-meta branch:pilot/GH-42 pr:42 iteration:1 -->",
			wantNum: 0,
			wantOK:  false,
		},
		{
			name:    "empty body",
			body:    "",
			wantNum: 0,
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotNum, gotOK := parseFixIssueSource(tt.body)
			if gotOK != tt.wantOK || gotNum != tt.wantNum {
				t.Errorf("parseFixIssueSource() = (%d, %v), want (%d, %v)", gotNum, gotOK, tt.wantNum, tt.wantOK)
			}
		})
	}
}

// ownerDeathGHServer is a small router for the owner-death test matrix: it
// serves canned Issue bodies by number, and records label/comment writes so
// tests can assert on the reaction that fired.
type ownerDeathGHServer struct {
	mu sync.Mutex

	issues map[int]*github.Issue // GET /issues/{n}

	addLabelCalls    []string // "issue:label"
	removeLabelCalls []string // "issue:label"
	comments         []string // "issue:body"
}

func newOwnerDeathGHServer() *ownerDeathGHServer {
	return &ownerDeathGHServer{issues: map[int]*github.Issue{}}
}

func (s *ownerDeathGHServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		switch {
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

func (s *ownerDeathGHServer) hasAddLabel(issue int, label string) bool {
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

// TestController_ReactToDeadFixIssue covers item 2 of GH-4842: react to a
// dead designated fix issue by either re-arming its source for retry, or
// escalating to needs-human when the source's retries are already
// exhausted — in every case exactly one live owner (or an escalation) must
// result, and an alert must fire.
func TestController_ReactToDeadFixIssue(t *testing.T) {
	fixIssueBody := "Fixes CI failure.\n\nDepends on: #100\n\n<!-- autopilot-meta branch:pilot/GH-100 pr:7 iteration:1 -->"

	tests := []struct {
		name           string
		sourceIssue    *github.Issue
		wantRearm      bool
		wantEscalate   bool
		wantNoReaction bool
	}{
		{
			name: "source open with pilot-failed and no retry-exhausted — rearmed",
			sourceIssue: &github.Issue{
				Number: 100,
				State:  github.StateOpen,
				Labels: []github.Label{{Name: github.LabelFailed}},
			},
			wantRearm: true,
		},
		{
			name: "source open with pilot-failed and retry-exhausted — escalated",
			sourceIssue: &github.Issue{
				Number: 100,
				State:  github.StateOpen,
				Labels: []github.Label{{Name: github.LabelFailed}, {Name: github.LabelRetryExhausted}},
			},
			wantEscalate: true,
		},
		{
			name: "source already closed — no reaction (avoid double-processing)",
			sourceIssue: &github.Issue{
				Number: 100,
				State:  github.StateClosed,
				Labels: []github.Label{{Name: github.LabelDone}},
			},
			wantNoReaction: true,
		},
		{
			name: "source not currently designated to this fix issue — no reaction",
			sourceIssue: &github.Issue{
				Number: 100,
				State:  github.StateOpen,
				Labels: []github.Label{{Name: github.LabelRetryReady}},
			},
			wantNoReaction: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newOwnerDeathGHServer()
			srv.issues[100] = tt.sourceIssue
			ts := httptest.NewServer(srv.handler())
			defer ts.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, ts.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")
			sink := &fakeAlertSink{}
			c.SetAlertsEngine(sink)

			deadIssue := &github.Issue{Number: 200, State: github.StateClosed, Body: fixIssueBody}
			c.reactToDeadFixIssue(context.Background(), deadIssue, "closed without shipping")

			switch {
			case tt.wantRearm:
				if !srv.hasAddLabel(100, github.LabelRetryReady) {
					t.Errorf("expected pilot-retry-ready to be added to source #100, calls=%v", srv.addLabelCalls)
				}
				if len(sink.events) != 1 {
					t.Errorf("expected exactly 1 alert, got %d", len(sink.events))
				}
			case tt.wantEscalate:
				if !srv.hasAddLabel(100, labelNeedsHuman) {
					t.Errorf("expected %s to be added to source #100, calls=%v", labelNeedsHuman, srv.addLabelCalls)
				}
				if srv.hasAddLabel(100, github.LabelRetryReady) {
					t.Error("retry-exhausted source must not be re-armed with retry-ready")
				}
				if len(sink.events) != 1 {
					t.Errorf("expected exactly 1 alert, got %d", len(sink.events))
				}
			case tt.wantNoReaction:
				if len(srv.addLabelCalls) != 0 {
					t.Errorf("expected no label writes, got %v", srv.addLabelCalls)
				}
				if len(sink.events) != 0 {
					t.Errorf("expected no alerts, got %d", len(sink.events))
				}
			}
		})
	}
}

// TestFeedbackLoop_CreateFailureIssue_DedupOwnerHealth covers item 3 of
// GH-4842 end-to-end through FeedbackLoop.CreateFailureIssue's dedup path:
// a closed-unmerged (dead) previously-designated fix issue must never be
// handed back out as the live owner — it must fall through and mint a
// replacement. A closed-and-shipped (pilot-done) or still-open issue is NOT
// death and must still be returned as-is (existing dedup behavior).
func TestFeedbackLoop_CreateFailureIssue_DedupOwnerHealth(t *testing.T) {
	tests := []struct {
		name           string
		existingIssue  *github.Issue
		wantCreateCall bool
		wantReturned   int
	}{
		{
			name:           "existing fix issue open — alive, dedup returns it unchanged",
			existingIssue:  &github.Issue{Number: 999, State: github.StateOpen},
			wantCreateCall: false,
			wantReturned:   999,
		},
		{
			name:           "existing fix issue closed+pilot-done — shipped, not death, dedup returns it unchanged",
			existingIssue:  &github.Issue{Number: 999, State: github.StateClosed, Labels: []github.Label{{Name: github.LabelDone}}},
			wantCreateCall: false,
			wantReturned:   999,
		},
		{
			name:           "existing fix issue closed without shipping — dead, dedup falls through to create a replacement",
			existingIssue:  &github.Issue{Number: 999, State: github.StateClosed, Labels: []github.Label{{Name: github.LabelRetryExhausted}}},
			wantCreateCall: true,
			wantReturned:   555,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			createCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/issues/999" && r.Method == http.MethodGet:
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(tt.existingIssue)
				case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodGet:
					// GH-4309 belt-and-suspenders search — no match, let dedup
					// logic (not this fallback) drive the outcome.
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode([]*github.Issue{})
				case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
					createCalls++
					resp := github.Issue{Number: 555}
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(resp)
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()

			fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)
			store := newTestStateStore(t)
			fl.SetStateStore(store)
			sink := &fakeAlertSink{}
			fl.SetAlertsEngine(sink)

			dedupRepo := "owner/repo"
			dedupKey := spawnedFixDedupKey(42, FailureCIPostMerge, []string{"epic-lifecycle / run"})
			if _, err := store.ClaimSpawnedFix(dedupRepo, dedupKey); err != nil {
				t.Fatalf("seed ClaimSpawnedFix failed: %v", err)
			}
			if err := store.RecordSpawnedFixIssue(dedupRepo, dedupKey, 999); err != nil {
				t.Fatalf("seed RecordSpawnedFixIssue failed: %v", err)
			}

			prState := &PRState{PRNumber: 42, HeadSHA: "abc1234"}
			got, err := fl.CreateFailureIssue(context.Background(), prState, FailureCIPostMerge, []string{"epic-lifecycle / run"}, "", 1)
			if err != nil {
				t.Fatalf("CreateFailureIssue() error = %v", err)
			}
			if got != tt.wantReturned {
				t.Errorf("CreateFailureIssue() = %d, want %d", got, tt.wantReturned)
			}
			if (createCalls > 0) != tt.wantCreateCall {
				t.Errorf("createCalls = %d, wantCreateCall = %v", createCalls, tt.wantCreateCall)
			}

			if tt.wantCreateCall {
				// The dead issue's dedup row must be overwritten with the
				// replacement so future ticks resolve to the live owner.
				stored, err := store.GetSpawnedFixIssue(dedupRepo, dedupKey)
				if err != nil {
					t.Fatalf("GetSpawnedFixIssue error = %v", err)
				}
				if stored != 555 {
					t.Errorf("dedup row after replacement = %d, want 555 (overwritten, not stuck on the dead issue)", stored)
				}
				if len(sink.events) != 1 {
					t.Errorf("expected exactly 1 owner-death alert, got %d", len(sink.events))
				}
			} else if len(sink.events) != 0 {
				t.Errorf("expected no owner-death alert for a live/shipped owner, got %d", len(sink.events))
			}
		})
	}
}
