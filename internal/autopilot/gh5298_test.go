package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-5298 (GH-5297 subtask): notifyExternalClose's supersede-close label
// mutation (controller.go, the `else` branch inside the label-write block)
// applied pilot-superseded without ever stripping the `pilot` label in the
// same call. GH-4826 already proved a successful spawnFailureIssue hand-off
// marks the source issue pilot-superseded instead of re-arming
// pilot-retry-ready, but it never checked what happened to `pilot` itself —
// if the issue still carried it going into the close (the common case: it
// was never removed by the failed run), the issue came out of this mutation
// wearing both labels, letting the poller pick it back up and double-dispatch
// work the newly spawned fix issue already owns.
//
// This test drives the real CI-fail supersede path end to end
// (handleCIFailed -> spawnFailureIssue -> notifyExternalClose) for the
// success case, and contrasts it with the sibling non-superseded resolution
// (zero-evidence decline -> notifyExternalClose's default retry-ready path)
// to prove the new pilot-stripping logic is scoped to the superseded branch
// only — retry-ready must keep re-adding `pilot` (GH-5042), never remove it.
func TestNotifyExternalClose_CIFailSupersede_StripsPilotLabel(t *testing.T) {
	const codeLog = `Run golangci-lint run ./...
internal/autopilot/controller.go:1234:6: Error return value of c.ghClient.ClosePullRequest is not checked (errcheck)
##[error]Process completed with exit code 1.`

	tests := []struct {
		name              string
		prNumber          int
		issueNumber       int
		headSHA           string
		hasEvidence       bool // controls whether check-runs reports a failed check to classify
		wantIssueCreated  bool
		wantPRClosed      bool
		wantTerminalLabel string
		wantLabelAdded    string // label that must appear in the issue's add-labels call
		wantPilotRemoved  bool   // whether `pilot` must appear in the issue's remove-labels call
	}{
		{
			name:              "spawn succeeds: pilot-superseded applied and pilot stripped in the same mutation",
			prNumber:          52980,
			issueNumber:       52981,
			headSHA:           "gh5298sha1",
			hasEvidence:       true,
			wantIssueCreated:  true,
			wantPRClosed:      true,
			wantTerminalLabel: github.LabelSuperseded,
			wantLabelAdded:    github.LabelSuperseded,
			wantPilotRemoved:  true,
		},
		{
			name:              "spawn declines (zero evidence): retry-ready resolution leaves pilot untouched",
			prNumber:          52982,
			issueNumber:       52983,
			headSHA:           "gh5298sha2",
			hasEvidence:       false,
			wantIssueCreated:  false,
			wantPRClosed:      false,
			wantTerminalLabel: "",
			wantLabelAdded:    github.LabelRetryReady,
			wantPilotRemoved:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueCreated := false
			prClosed := false
			var labelsAdded []string
			var labelsRemoved []string

			issuePath := fmt.Sprintf("/repos/owner/repo/issues/%d", tt.issueNumber)
			labelsPath := issuePath + "/labels"
			pullPath := fmt.Sprintf("/repos/owner/repo/pulls/%d", tt.prNumber)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == fmt.Sprintf("/repos/owner/repo/commits/%s/check-runs", tt.headSHA):
					var resp github.CheckRunsResponse
					if tt.hasEvidence {
						resp = github.CheckRunsResponse{
							TotalCount: 1,
							CheckRuns: []github.CheckRun{
								{ID: 501, Name: "lint", Status: "completed", Conclusion: "failure"},
							},
						}
					} else {
						resp = github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}}
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(mustJSON(t, resp))

				case r.URL.Path == "/repos/owner/repo/actions/jobs/501/logs":
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(codeLog))

				case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
					issueCreated = true
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write(mustJSON(t, github.Issue{Number: tt.issueNumber + 100000}))

				case r.URL.Path == pullPath && r.Method == http.MethodPatch:
					prClosed = true
					w.WriteHeader(http.StatusOK)

				case r.URL.Path == issuePath && r.Method == http.MethodGet:
					// Source issue is open and still carries `pilot` going into
					// the close — the common shape this bug left stranded.
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(mustJSON(t, github.Issue{
						Number: tt.issueNumber,
						State:  github.StateOpen,
						Labels: []github.Label{{Name: github.LabelPilot}},
					}))

				case r.URL.Path == labelsPath && r.Method == http.MethodPost:
					var body map[string][]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					labelsAdded = append(labelsAdded, body["labels"]...)
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode([]github.Label{})

				case strings.HasPrefix(r.URL.Path, labelsPath+"/") && r.Method == http.MethodDelete:
					removed := strings.TrimPrefix(r.URL.Path, labelsPath+"/")
					labelsRemoved = append(labelsRemoved, removed)
					w.WriteHeader(http.StatusOK)

				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvDev

			c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))

			prState := &PRState{
				PRNumber:    tt.prNumber,
				IssueNumber: tt.issueNumber,
				HeadSHA:     tt.headSHA,
				Stage:       StageCIFailed,
			}

			if err := c.handleCIFailed(context.Background(), prState); err != nil {
				t.Fatalf("handleCIFailed returned unexpected error: %v", err)
			}

			if issueCreated != tt.wantIssueCreated {
				t.Errorf("issueCreated = %v, want %v", issueCreated, tt.wantIssueCreated)
			}
			if prClosed != tt.wantPRClosed {
				t.Errorf("prClosed = %v, want %v", prClosed, tt.wantPRClosed)
			}
			if prState.TerminalLabel != tt.wantTerminalLabel {
				t.Fatalf("prState.TerminalLabel = %q, want %q", prState.TerminalLabel, tt.wantTerminalLabel)
			}

			// Drive the close notification — the seam that actually writes the
			// issue's labels — the same way the poller does once it observes
			// the PR closed (or, for the decline case, exercises notifyExternalClose's
			// default resolution the same way a stale-PR close would).
			c.notifyExternalClose(context.Background(), prState)

			foundWantAdded := false
			for _, l := range labelsAdded {
				if l == tt.wantLabelAdded {
					foundWantAdded = true
				}
			}
			if !foundWantAdded {
				t.Errorf("expected %q applied to the issue, labels added: %v", tt.wantLabelAdded, labelsAdded)
			}

			foundPilotRemoved := false
			for _, l := range labelsRemoved {
				if l == github.LabelPilot {
					foundPilotRemoved = true
				}
			}
			if foundPilotRemoved != tt.wantPilotRemoved {
				t.Errorf("pilot label removed = %v, want %v (labels removed: %v)", foundPilotRemoved, tt.wantPilotRemoved, labelsRemoved)
			}
		})
	}
}
