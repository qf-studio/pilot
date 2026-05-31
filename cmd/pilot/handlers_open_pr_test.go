package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
)

// TASK-341: issueHasOpenPR detects the awaiting-merge window (PR created but not
// yet merged) so a "no new commit produced" no-op re-dispatch is classified as
// awaiting-merge instead of being mislabeled pilot-blocked. It is the open-PR
// counterpart to issueAlreadyMerged (TASK-321).
func TestIssueHasOpenPR(t *testing.T) {
	tests := []struct {
		name        string
		branchPRs   string // JSON array returned by /repos/.../pulls?state=open (FindOpenPRByBranch)
		searchItems string // JSON returned by /search/issues (SearchPRsForIssue)
		want        bool
	}{
		{
			name:        "branch lookup finds open PR (strongly consistent)",
			branchPRs:   `[{"number":10,"state":"open","head":{"ref":"pilot/GH-3260"}}]`,
			searchItems: `{"items":[]}`,
			want:        true,
		},
		{
			name:        "branch misses but search finds open PR (head off-convention)",
			branchPRs:   `[]`,
			searchItems: `{"items":[{"number":10,"state":"open"}]}`,
			want:        true,
		},
		{
			name:        "search finds only a merged/closed PR — not awaiting merge",
			branchPRs:   `[]`,
			searchItems: `{"items":[{"number":10,"state":"closed","pull_request":{"merged_at":"2026-05-29T18:00:00Z"}}]}`,
			want:        false,
		},
		{
			name:        "genuine no-op — no open PR anywhere",
			branchPRs:   `[]`,
			searchItems: `{"items":[]}`,
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasPrefix(r.URL.Path, "/search/issues"):
					_, _ = w.Write([]byte(tt.searchItems))
				case strings.Contains(r.URL.Path, "/pulls"):
					// issueHasOpenPR only queries open PRs by branch here.
					_, _ = w.Write([]byte(tt.branchPRs))
				default:
					http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
				}
			}))
			defer server.Close()

			client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			got := issueHasOpenPR(context.Background(), client, "owner", "repo", 3260)
			if got != tt.want {
				t.Errorf("issueHasOpenPR() = %v, want %v", got, tt.want)
			}
		})
	}
}
