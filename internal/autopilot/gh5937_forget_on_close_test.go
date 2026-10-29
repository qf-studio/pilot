package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

func fixIssueWith(state string, labels ...string) *github.Issue {
	iss := &github.Issue{Number: 5922, State: state}
	for _, l := range labels {
		iss.Labels = append(iss.Labels, github.Label{Name: l})
	}
	return iss
}

// A fix issue that is closed by hand, or loses its autopilot-fix label, stops
// owning its borrowed branch as soon as autopilot observes it — not only when
// a later adoption happens to check (GH-5937). Each case runs against the real
// executor.Runner registry through the real observation point.
func TestForgetBorrowedBranchOnIssueClose(t *testing.T) {
	tests := []struct {
		name     string
		issue    *github.Issue
		wantKept bool
	}{
		{"closed by hand (pilot-done, so not owner death) drops the entry", fixIssueWith(github.StateClosed, "pilot", "autopilot-fix", github.LabelDone), false},
		{"closed by hand without labels drops the entry", fixIssueWith(github.StateClosed), false},
		{"open but unlabeled drops the entry", fixIssueWith(github.StateOpen, "pilot"), false},
		{"open and labeled keeps the entry", fixIssueWith(github.StateOpen, "pilot", "autopilot-fix"), true},
	}

	observers := []struct {
		name    string
		observe func(t *testing.T, c *Controller, issue *github.Issue)
	}{
		{"feedback-loop dedup path", func(t *testing.T, c *Controller, _ *github.Issue) {
			store := newTestStateStore(t)
			c.SetStateStore(store)
			checks := []string{"Test"}
			key := spawnedFixDedupKey(5921, FailureCIPostMerge, checks)
			if _, err := store.ClaimSpawnedFix("owner/repo", key); err != nil {
				t.Fatalf("seed claim: %v", err)
			}
			if err := store.RecordSpawnedFixIssue("owner/repo", key, 5922); err != nil {
				t.Fatalf("seed fix issue: %v", err)
			}
			if _, err := c.feedbackLoop.CreateFailureIssue(context.Background(), &PRState{PRNumber: 5921, HeadSHA: "abc1234"}, FailureCIPostMerge, checks, "", 1); err != nil {
				t.Fatalf("CreateFailureIssue: %v", err)
			}
		}},
		{"shared issue-observation hook (controller call sites)", func(t *testing.T, c *Controller, issue *github.Issue) {
			c.forgetBorrowedBranchIfDeadIssue(issue)
		}},
	}

	for _, obs := range observers {
		for _, tt := range tests {
			t.Run(obs.name+"/"+tt.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.URL.Path == "/repos/owner/repo/issues/5922" && r.Method == http.MethodGet:
						_ = json.NewEncoder(w).Encode(tt.issue)
					case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodGet:
						_ = json.NewEncoder(w).Encode([]*github.Issue{})
					case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
						w.WriteHeader(http.StatusCreated)
						_ = json.NewEncoder(w).Encode(github.Issue{Number: 555})
					default:
						_, _ = w.Write([]byte("{}"))
					}
				}))
				defer server.Close()

				registry := newRunnerRegistry()
				c := NewController(DefaultConfig(), github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL), nil, "owner", "repo")
				c.SetBorrowedBranchLookup(registry)

				obs.observe(t, c, tt.issue)

				if _, _, kept := registry.BorrowedBranch("GH-5922"); kept != tt.wantKept {
					t.Fatalf("registry entry kept = %v, want %v", kept, tt.wantKept)
				}
				if tt.wantKept {
					return
				}
				// A later adoption of the branch binds to the issue the PR closes.
				adopt, _, _ := newAdoptController(t, map[int]adoptFakeIssue{5917: {state: "open"}}, nil)
				adopt.SetBorrowedBranchLookup(registry)
				got := adopt.resolveAdoptedIssueNumber(context.Background(), adoptPR("pilot/GH-5917", "Closes #5917"))
				if got.Number != 5917 || got.ViaFixIssue {
					t.Errorf("adoption = {issue:%d viaFix:%v}, want the closing-reference issue 5917", got.Number, got.ViaFixIssue)
				}
			})
		}
	}
}

// Restoring a consumed entry must keep the origin PR number it was registered
// with: githubOnPRCreatedHandler's BorrowedBranch gate needs fromPR > 0.
func TestRestoreBorrowedBranchKeepsPRNumber(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		issues map[int]adoptFakeIssue
	}{
		{
			name:   "live fix issue that lost to a closing reference",
			body:   "Closes #5917",
			issues: map[int]adoptFakeIssue{5917: {state: "open"}, 5922: {state: "open", labels: fixLabels}},
		},
		{
			name:   "unverifiable fix issue",
			body:   "",
			issues: map[int]adoptFakeIssue{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := newRunnerRegistry() // registered with fromPR 5921
			c, _, _ := newAdoptController(t, tt.issues, nil)
			c.SetBorrowedBranchLookup(registry)

			c.resolveAdoptedIssueNumber(context.Background(), adoptPR("pilot/GH-5917", tt.body))

			branch, fromPR, ok := registry.BorrowedBranch("GH-5922")
			if !ok || branch != "pilot/GH-5917" {
				t.Fatalf("entry not restored: (%q, %d, %v)", branch, fromPR, ok)
			}
			if fromPR != 5921 {
				t.Errorf("restored fromPR = %d, want 5921", fromPR)
			}
		})
	}
}
