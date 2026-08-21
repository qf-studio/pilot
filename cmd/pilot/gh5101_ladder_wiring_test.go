// GH-5101: stampPilotFailedWithLadder wires the `pilot github run` one-shot
// execute failure sites (newGitHubRunCmd, runner.Execute error and the
// no-commit/no-PR outcome) into the shared retryladder.Advance helper, so
// stamping pilot-failed also advances the pilot-failed-retry-N rung in the
// same mutation — mirroring postTitleRejectionEscalation's GH-5077/GH-5098
// pattern. Before this, both sites called client.AddLabels with a bare
// "pilot-failed" and never touched the ladder, so issues driven through
// this CLI path never advanced past pilot-failed-retry-1 no matter how many
// times they failed here.
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

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
)

// gh5101FakeIssueServer stands in for the GitHub REST API, using the
// argv-log idiom this package already uses for recording ordered call
// sequences (writeFakeGh's recorded argv in ghguard_test.go; the recorded
// DELETE sequence in TestUnwindGithubStartedLabel_GH5028) — here the "log"
// is the ordered sequence of label mutations the server observes, since
// stampPilotFailedWithLadder talks to the GitHub REST client rather than a
// gh subprocess.
func gh5101FakeIssueServer(t *testing.T, issueNumber int, initialLabels []string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var log []string

	labels := make([]github.Label, len(initialLabels))
	for i, l := range initialLabels {
		labels[i] = github.Label{Name: l}
	}
	issue := &github.Issue{Number: issueNumber, Title: "test issue", Labels: labels}

	issuePath := fmt.Sprintf("/repos/owner/repo/issues/%d", issueNumber)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == issuePath:
			_ = json.NewEncoder(w).Encode(issue)
		case r.Method == http.MethodPost && r.URL.Path == issuePath+"/labels":
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, l := range body.Labels {
				log = append(log, "add:"+l)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, issuePath+"/labels/"):
			removed := strings.TrimPrefix(r.URL.Path, issuePath+"/labels/")
			log = append(log, "remove:"+removed)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return server, &log
}

// TestStampPilotFailedWithLadder_RungAdvanceAndExhaustedTerminal covers the
// GH-5101 wiring end to end: the rung-advance path (fresh failure at each
// ladder position) and the exhausted-terminal case (once at the top rung, a
// further failure must stamp pilot-failed alone, without regressing off
// pilot-failed-retry-exhausted or re-advancing past it).
func TestStampPilotFailedWithLadder_RungAdvanceAndExhaustedTerminal(t *testing.T) {
	tests := []struct {
		name          string
		initialLabels []string
		wantLog       []string
	}{
		{
			name:          "no ladder label yet -> stamps pilot-failed and retry-1",
			initialLabels: nil,
			wantLog:       []string{"add:pilot-failed", "add:pilot-failed-retry-1"},
		},
		{
			name:          "retry-1 present -> advances to retry-2",
			initialLabels: []string{"pilot-failed-retry-1"},
			wantLog:       []string{"add:pilot-failed", "add:pilot-failed-retry-2", "remove:pilot-failed-retry-1"},
		},
		{
			name:          "retry-2 present -> exhausts the ladder",
			initialLabels: []string{"pilot-failed-retry-2"},
			wantLog:       []string{"add:pilot-failed", "add:pilot-failed-retry-exhausted", "remove:pilot-failed-retry-2"},
		},
		{
			name:          "exhausted-terminal: already at top rung -> no further advancement, no regression",
			initialLabels: []string{"pilot-failed-retry-exhausted"},
			wantLog:       []string{"add:pilot-failed"},
		},
		{
			name:          "duplicate fail event: issue already carries pilot-failed -> ladder does not re-advance",
			initialLabels: []string{"pilot-failed", "pilot-failed-retry-1"},
			wantLog:       []string{"add:pilot-failed"},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueNumber := 5101000 + i
			server, log := gh5101FakeIssueServer(t, issueNumber, tt.initialLabels)
			defer server.Close()

			client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			stampPilotFailedWithLadder(context.Background(), client, "owner", "repo", issueNumber)

			if len(*log) != len(tt.wantLog) {
				t.Fatalf("call log = %v, want %v", *log, tt.wantLog)
			}
			for i, want := range tt.wantLog {
				if (*log)[i] != want {
					t.Errorf("call log[%d] = %q, want %q (full log: %v)", i, (*log)[i], want, *log)
				}
			}
		})
	}
}
