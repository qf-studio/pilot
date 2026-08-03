package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_HandleMergeConflict_SourceIssueClosed covers GH-4657/TASK-437:
// a conflicting PR whose source issue is already closed — because a
// sibling/parent run delivered the same scope first (PR#4653 was born
// conflicting against already-closed issue #4649, whose scope had already
// merged via PR#4652) — must be closed with an honest terminal state
// instead of escalated for a rebase nobody should perform.
//
// Table-driven across the three branches of the new issue-state check added
// to handleMergeConflict: a closed source issue (new short-circuit), an open
// source issue (today's rebase/escalate ladder, unchanged), and a GetIssue
// API error (fail-open to today's ladder, since escalation is the safe
// default when issue state can't be confirmed).
//
// Every case uses the same source-file (non-go.mod/go.sum) conflict fixture
// as TestController_HandleMergeConflict_SourceFileConflictEscalatesInsteadOfClosing
// so that, absent the GH-4657 short-circuit, the path always falls through
// to escalateAndHold — making "open issue" and "GetIssue error" genuine
// regression checks that today's escalation behavior is untouched.
func TestController_HandleMergeConflict_SourceIssueClosed(t *testing.T) {
	tests := []struct {
		name           string
		issueHandler   func(w http.ResponseWriter, r *http.Request)
		wantPRClosed   bool
		wantEscalation bool
	}{
		{
			name: "closed issue: PR closed honestly, no escalation",
			issueHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(github.Issue{
					Number: 4649,
					State:  "closed",
					Labels: []github.Label{{Name: github.LabelSuperseded}},
				})
			},
			wantPRClosed:   true,
			wantEscalation: false,
		},
		{
			name: "open issue: today's escalation unchanged",
			issueHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(github.Issue{Number: 4649, State: "open"})
			},
			wantPRClosed:   false,
			wantEscalation: true,
		},
		{
			name: "GetIssue error: fail-open to today's escalation",
			issueHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantPRClosed:   false,
			wantEscalation: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := newFixtureRepo(t)
			ctx := context.Background()

			// Source-file conflict (not go.mod/go.sum-only): absent the
			// GH-4657 short-circuit, auto-rebase fails and mechanical
			// resolution determines the conflict surface isn't
			// go.mod/go.sum-only, so the ladder escalates instead of
			// closing (GH-4459).
			runFixtureGit(t, local, "checkout", "-b", "feature/x")
			writeFixtureFile(t, local, "main.go", "package fixture\n\nfunc X() int { return 1 }\n")
			runFixtureGit(t, local, "add", ".")
			runFixtureGit(t, local, "commit", "-m", "add X returning 1")
			runFixtureGit(t, local, "push", "origin", "feature/x")

			runFixtureGit(t, local, "checkout", "main")
			writeFixtureFile(t, local, "main.go", "package fixture\n\nfunc X() int { return 2 }\n")
			runFixtureGit(t, local, "add", ".")
			runFixtureGit(t, local, "commit", "-m", "change X to return 2")
			runFixtureGit(t, local, "push", "origin", "main")

			var (
				prClosed    bool
				prComment   string
				labelsAdded []string
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/issues/4649" && r.Method == http.MethodGet:
					tt.issueHandler(w, r)
				case r.URL.Path == "/repos/owner/repo/pulls/60/update-branch" && r.Method == http.MethodPut:
					w.WriteHeader(http.StatusUnprocessableEntity)
					_, _ = w.Write([]byte(`{"message":"merge conflict between base and head"}`))
				case r.URL.Path == "/repos/owner/repo/pulls/60" && r.Method == http.MethodPatch:
					prClosed = true
					w.WriteHeader(http.StatusOK)
				case r.URL.Path == "/repos/owner/repo/issues/60/comments" && r.Method == http.MethodPost:
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					prComment = body["body"]
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(github.PRComment{ID: 1})
				case r.URL.Path == "/repos/owner/repo/issues/4649/labels" && r.Method == http.MethodPost:
					var body map[string][]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					labelsAdded = append(labelsAdded, body["labels"]...)
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode([]github.Label{})
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvDev

			c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
			prState := &PRState{
				PRNumber:    60,
				PRURL:       "https://github.com/owner/repo/pull/60",
				IssueNumber: 4649,
				BranchName:  "feature/x",
				HeadSHA:     "deadbeef",
				Stage:       StageMerging,
				CreatedAt:   time.Now(),
			}
			c.mu.Lock()
			c.activePRs[60] = prState
			c.mu.Unlock()

			if err := c.handleMergeConflict(ctx, prState); err != nil {
				t.Fatalf("handleMergeConflict: %v", err)
			}

			if prClosed != tt.wantPRClosed {
				t.Errorf("prClosed = %v, want %v", prClosed, tt.wantPRClosed)
			}
			if prState.Stage != StageFailed {
				t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
			}

			gotEscalationLabel := false
			for _, l := range labelsAdded {
				if l == "needs-manual-rebase" || l == labelNeedsHuman {
					gotEscalationLabel = true
				}
			}
			if gotEscalationLabel != tt.wantEscalation {
				t.Errorf("escalation labels present = %v (labels=%v), want %v", gotEscalationLabel, labelsAdded, tt.wantEscalation)
			}

			if tt.wantPRClosed {
				if prComment == "" || !strings.Contains(prComment, "closed") {
					t.Errorf("expected an honest close comment naming the closed source issue, got %q", prComment)
				}
				if prState.TerminalLabel != github.LabelSuperseded {
					t.Errorf("TerminalLabel = %q, want %q", prState.TerminalLabel, github.LabelSuperseded)
				}
			} else {
				if prState.TerminalLabel == github.LabelSuperseded {
					t.Errorf("TerminalLabel must not be set to %q when the PR wasn't closed via the GH-4657 path", github.LabelSuperseded)
				}
			}
		})
	}
}
