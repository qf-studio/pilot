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

// TestController_HandleMergeConflict_SourceIssueClosed covers GH-4657/TASK-437
// and its GH-4696 reachability follow-up: a conflicting PR whose source issue
// is already closed — because a sibling/parent run delivered the same scope
// first (PR#4653 was born conflicting against already-closed issue #4649,
// whose scope had already merged via PR#4652) — must be closed with an
// honest terminal state instead of escalated for a rebase nobody should
// perform. GH-4696 tightened this: "source issue closed" alone is not proof
// this PR's own changes are on main, so closeConflictSourceIssueClosed now
// verifies reachability (compare base=HeadSHA, head=mainSHA is "ahead" or
// "identical") before closing, and fails safe (holds instead of closing)
// when that check says the work isn't there yet, or when the check itself
// errors.
//
// Table-driven across five branches of handleMergeConflict's issue-state +
// reachability checks: a closed source issue whose work IS on main (new
// short-circuit, AC2), an open source issue (today's rebase/escalate ladder,
// unchanged), a GetIssue API error (fail-open to today's ladder), a closed
// source issue whose work is NOT on main (GH-4696: hold instead of close),
// and a closed source issue where the reachability check itself errors
// (GH-4696: fail-safe, hold instead of close).
//
// Every case uses the same source-file (non-go.mod/go.sum) conflict fixture
// as TestController_HandleMergeConflict_SourceFileConflictEscalatesInsteadOfClosing
// so that, absent the GH-4657/GH-4696 short-circuit closing the PR, the path
// always falls through to escalateAndHold — making "open issue" and
// "GetIssue error" genuine regression checks that today's escalation
// behavior is untouched.
func TestController_HandleMergeConflict_SourceIssueClosed(t *testing.T) {
	tests := []struct {
		name              string
		issueHandler      func(w http.ResponseWriter, r *http.Request)
		compareStatus     string // GitHub compare "status" for base=HeadSHA...head=mainSHA; "" defaults to "ahead" (work already on main)
		reachabilityFails bool   // simulate the GetBranch/CompareStatus reachability check itself erroring
		wantPRClosed      bool
		wantEscalation    bool
		wantCommentSubstr string // optional: substring the posted comment must contain
	}{
		{
			name: "closed issue + work on main: PR closed honestly, no escalation",
			issueHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(github.Issue{
					Number: 4649,
					State:  "closed",
					Labels: []github.Label{{Name: github.LabelSuperseded}},
				})
			},
			compareStatus:  "ahead",
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
		{
			name: "closed issue + work NOT on main (GH-4696): held open, escalated, comment names the situation",
			issueHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(github.Issue{
					Number: 4649,
					State:  "closed",
					Labels: []github.Label{{Name: github.LabelSuperseded}},
				})
			},
			compareStatus:     "diverged",
			wantPRClosed:      false,
			wantEscalation:    true,
			wantCommentSubstr: "not confirmed on the base branch",
		},
		{
			name: "closed issue + reachability check errors (GH-4696): fail-safe, held open instead of closed",
			issueHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(github.Issue{
					Number: 4649,
					State:  "closed",
					Labels: []github.Label{{Name: github.LabelSuperseded}},
				})
			},
			reachabilityFails: true,
			wantPRClosed:      false,
			wantEscalation:    true,
			wantCommentSubstr: "could not be verified",
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
				case r.URL.Path == "/repos/owner/repo/branches/main" && r.Method == http.MethodGet:
					// GH-4696: closeConflictSourceIssueClosed's reachability check
					// fetches the base branch's SHA before comparing it to the PR's
					// HeadSHA.
					if tt.reachabilityFails {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(github.Branch{Name: "main", Commit: github.BranchCommit{SHA: "mainsha123"}})
				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/compare/deadbeef...mainsha123") && r.Method == http.MethodGet:
					// GH-4696: compare(base=HeadSHA, head=mainSHA) — "ahead"/"identical"
					// means the PR's changes are already reachable from main.
					if tt.reachabilityFails {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					status := tt.compareStatus
					if status == "" {
						status = "ahead"
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
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

			if tt.wantCommentSubstr != "" && !strings.Contains(prComment, tt.wantCommentSubstr) {
				t.Errorf("comment = %q, want substring %q", prComment, tt.wantCommentSubstr)
			}
		})
	}
}
