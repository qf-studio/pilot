package autopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// fakeJiraDoneCall records a single NotifyTaskCompleted invocation.
type fakeJiraDoneCall struct {
	IssueKey string
	PRURL    string
}

// fakeJiraDoneNotifier is a test double for JiraDoneNotifier.
type fakeJiraDoneNotifier struct {
	calls []fakeJiraDoneCall
	err   error
}

func (f *fakeJiraDoneNotifier) NotifyTaskCompleted(_ context.Context, issueKey, prURL, _ string) error {
	f.calls = append(f.calls, fakeJiraDoneCall{IssueKey: issueKey, PRURL: prURL})
	return f.err
}

// TestController_HandleMerging_NotifiesJiraDone covers GH-4987's merge-side
// Jira done leg: (a) a JIRA-* task's PR merge fires the notifier exactly once
// with the merged PR URL, (b) a non-JIRA (GH-*) task never invokes the
// notifier — no behavior change for GH-/Linear-originated tasks, and (c) a
// notifier error is WARN-logged and non-fatal — the merge path still
// succeeds and the PR still reaches StageMerged.
func TestController_HandleMerging_NotifiesJiraDone(t *testing.T) {
	tests := []struct {
		name          string
		branchName    string
		issueNumber   int
		notifierErr   error
		wantCalls     []fakeJiraDoneCall
		wantWarnInLog string
	}{
		{
			name:        "JIRA-prefixed task id triggers the done-leg call exactly once",
			branchName:  "pilot/JIRA-KAN-6",
			issueNumber: 0,
			wantCalls: []fakeJiraDoneCall{
				{IssueKey: "KAN-6", PRURL: "https://github.com/owner/repo/pull/201"},
			},
		},
		{
			name:        "non-JIRA task id does not invoke the notifier",
			branchName:  "pilot/GH-30",
			issueNumber: 30,
			wantCalls:   nil,
		},
		{
			name:          "notifier error surfaces as a WARN log and the merge path returns success",
			branchName:    "pilot/JIRA-KAN-7",
			issueNumber:   0,
			notifierErr:   context.DeadlineExceeded,
			wantWarnInLog: "failed to notify Jira task completed",
			wantCalls: []fakeJiraDoneCall{
				{IssueKey: "KAN-7", PRURL: "https://github.com/owner/repo/pull/201"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/pulls/201/merge" && r.Method == http.MethodPost:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
				case r.URL.Path == "/repos/owner/repo/issues/30/labels" && r.Method == http.MethodPost:
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode([]github.Label{})
				case r.URL.Path == "/repos/owner/repo/issues/30" && r.Method == http.MethodPatch:
					w.WriteHeader(http.StatusOK)
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvDev
			cfg.AutoMerge = true

			var logBuf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuf, nil))

			c := NewController(cfg, ghClient, nil, "owner", "repo", WithLogger(logger))
			notifier := &fakeJiraDoneNotifier{err: tt.notifierErr}
			c.SetJiraDoneNotifier(notifier)

			c.mu.Lock()
			c.activePRs[201] = &PRState{
				PRNumber:     201,
				PRURL:        "https://github.com/owner/repo/pull/201",
				IssueNumber:  tt.issueNumber,
				BranchName:   tt.branchName,
				HeadSHA:      "sha201",
				Stage:        StageMerging,
				TargetBranch: "main",
				CreatedAt:    time.Now(),
			}
			c.mu.Unlock()

			if err := c.ProcessPR(context.Background(), 201, nil); err != nil {
				t.Fatalf("ProcessPR returned error: %v", err)
			}

			pr, _ := c.GetPRState(201)
			if pr.Stage != StageMerged {
				t.Errorf("Stage = %s, want %s — merge path must succeed even if Jira notify fails", pr.Stage, StageMerged)
			}

			if len(notifier.calls) != len(tt.wantCalls) {
				t.Fatalf("notifier calls = %+v, want %+v", notifier.calls, tt.wantCalls)
			}
			for i, want := range tt.wantCalls {
				if notifier.calls[i] != want {
					t.Errorf("call[%d] = %+v, want %+v", i, notifier.calls[i], want)
				}
			}

			if tt.wantWarnInLog != "" && !strings.Contains(logBuf.String(), tt.wantWarnInLog) {
				t.Errorf("log output missing WARN %q; got: %s", tt.wantWarnInLog, logBuf.String())
			}
		})
	}
}

// TestJiraIssueKeyFromBranch covers the branch-name -> Jira issue key
// extraction in isolation (GH-4987).
func TestJiraIssueKeyFromBranch(t *testing.T) {
	tests := []struct {
		branch string
		want   string
	}{
		{"pilot/JIRA-KAN-6", "KAN-6"},
		{"pilot/JIRA-PROJ-42", "PROJ-42"},
		{"pilot/GH-30", ""},
		{"pilot/APP-123", ""}, // Linear-style task id, not Jira
		{"", ""},
		{"not-a-pilot-branch", ""},
	}
	for _, tt := range tests {
		if got := jiraIssueKeyFromBranch(tt.branch); got != tt.want {
			t.Errorf("jiraIssueKeyFromBranch(%q) = %q, want %q", tt.branch, got, tt.want)
		}
	}
}
