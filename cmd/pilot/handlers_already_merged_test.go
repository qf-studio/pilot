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

// TASK-321: issueAlreadyMerged distinguishes a re-dispatch of shipped work
// (merged PR exists) from a genuine no-op (none), so the handler can close the
// issue as done instead of mislabeling completed work pilot-blocked.
func TestIssueAlreadyMerged(t *testing.T) {
	tests := []struct {
		name        string
		searchCount string // total_count returned by /search/issues
		branchPRs   string // JSON array returned by /repos/.../pulls
		want        bool
	}{
		{
			name:        "search finds merged PR",
			searchCount: `{"total_count":1}`,
			branchPRs:   `[]`,
			want:        true,
		},
		{
			name:        "search misses but branch lookup finds merged PR (Search API lag)",
			searchCount: `{"total_count":0}`,
			branchPRs:   `[{"merged_at":"2026-05-29T18:00:00Z"}]`,
			want:        true,
		},
		{
			name:        "genuine no-op — no merged PR anywhere",
			searchCount: `{"total_count":0}`,
			branchPRs:   `[]`,
			want:        false,
		},
		{
			name:        "branch PR exists but is not merged",
			searchCount: `{"total_count":0}`,
			branchPRs:   `[{"merged_at":"","merged":false}]`,
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasPrefix(r.URL.Path, "/search/issues"):
					_, _ = w.Write([]byte(tt.searchCount))
				case strings.Contains(r.URL.Path, "/pulls"):
					_, _ = w.Write([]byte(tt.branchPRs))
				default:
					http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
				}
			}))
			defer server.Close()

			client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			got := issueAlreadyMerged(context.Background(), client, "owner", "repo", 3260)
			if got != tt.want {
				t.Errorf("issueAlreadyMerged() = %v, want %v", got, tt.want)
			}
		})
	}
}
