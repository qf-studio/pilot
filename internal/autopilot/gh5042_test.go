package autopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// gh5042LabelServer builds an httptest server around issue #10 that tracks
// its live label set (seeded from initial) through the same
// GET/POST-labels/DELETE-labels/POST-comments surface notifyExternalClose
// and escalateAndHold actually exercise, so tests can assert on the
// converged label set rather than individual call flags.
func gh5042LabelServer(t *testing.T, issueNumber int, initial []string, issueState string) (*httptest.Server, func() map[string]bool) {
	t.Helper()

	labels := map[string]bool{}
	for _, l := range initial {
		labels[l] = true
	}

	issuePath := fmt.Sprintf("/repos/owner/repo/issues/%d", issueNumber)
	labelsPath := issuePath + "/labels"
	commentsPath := issuePath + "/comments"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == issuePath && r.Method == http.MethodGet:
			var ghLabels []github.Label
			for l := range labels {
				ghLabels = append(ghLabels, github.Label{Name: l})
			}
			issue := github.Issue{Number: issueNumber, State: issueState, Labels: ghLabels}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(issue)

		case r.URL.Path == labelsPath && r.Method == http.MethodPost:
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, l := range body.Labels {
				labels[l] = true
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))

		case strings.HasPrefix(r.URL.Path, labelsPath+"/") && r.Method == http.MethodDelete:
			name := strings.TrimPrefix(r.URL.Path, labelsPath+"/")
			delete(labels, name)
			w.WriteHeader(http.StatusOK)

		case (r.URL.Path == commentsPath || strings.HasSuffix(r.URL.Path, "/comments")) && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]int{"id": 1})

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))

	return server, func() map[string]bool {
		snapshot := make(map[string]bool, len(labels))
		for k, v := range labels {
			snapshot[k] = v
		}
		return snapshot
	}
}

func assertLabelSet(t *testing.T, got map[string]bool, want []string) {
	t.Helper()
	wantSet := map[string]bool{}
	for _, l := range want {
		wantSet[l] = true
	}
	for l := range got {
		if !wantSet[l] {
			t.Errorf("unexpected label %q present, got=%v want=%v", l, got, want)
		}
	}
	for l := range wantSet {
		if !got[l] {
			t.Errorf("expected label %q missing, got=%v want=%v", l, got, want)
		}
	}
}

// TestGH5042_RetryArmingConvergesToPollableExclusiveState is the GH-5032
// acceptance test: an issue escalated with pilot-needs-human (+ optionally
// needs-manual-rebase), whose failed-stage PR is then externally closed,
// must converge to exactly {pilot, pilot-retry-ready} — pollable (pilot
// present) and mutually exclusive with the escalation hold (needs-human and
// needs-manual-rebase both gone). Table-driven over the prior label
// combinations the live incident could have left behind.
func TestGH5042_RetryArmingConvergesToPollableExclusiveState(t *testing.T) {
	tests := []struct {
		name    string
		initial []string
	}{
		{
			name:    "escalation hold with rebase label, no pilot label (GH-5032 live shape)",
			initial: []string{labelNeedsHuman, labelNeedsManualRebase},
		},
		{
			name:    "escalation hold without rebase label",
			initial: []string{labelNeedsHuman},
		},
		{
			name:    "escalation hold plus stale in-progress label",
			initial: []string{labelNeedsHuman, labelNeedsManualRebase, github.LabelInProgress},
		},
		{
			name:    "no prior labels at all",
			initial: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, snapshot := gh5042LabelServer(t, 10, tt.initial, "open")
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{PRNumber: 42, IssueNumber: 10, Stage: StageFailed, Error: "CI checks failed"}
			c.notifyExternalClose(context.Background(), prState)

			assertLabelSet(t, snapshot(), []string{github.LabelPilot, github.LabelRetryReady})
		})
	}
}

// TestGH5042_EscalateAndHoldRemovesRetryReady is the reverse-direction
// mutual-exclusion test: escalateAndHold must strip pilot-retry-ready from
// an issue when it applies pilot-needs-human, so a later escalation can
// never sit alongside a stale retry-ready arm.
func TestGH5042_EscalateAndHoldRemovesRetryReady(t *testing.T) {
	server, snapshot := gh5042LabelServer(t, 10, []string{github.LabelPilot, github.LabelRetryReady}, "open")
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{PRNumber: 42, IssueNumber: 10}
	c.escalateAndHold(context.Background(), prState, "rebase conflict requires manual resolution", []string{labelNeedsManualRebase}, "")

	got := snapshot()
	if got[github.LabelRetryReady] {
		t.Errorf("expected pilot-retry-ready removed by escalateAndHold, got=%v", got)
	}
	if !got[labelNeedsHuman] {
		t.Errorf("expected pilot-needs-human applied by escalateAndHold, got=%v", got)
	}
	if !got[labelNeedsManualRebase] {
		t.Errorf("expected needs-manual-rebase applied by escalateAndHold, got=%v", got)
	}
}

// TestGH5042_TerminalFinalizationShedsStaleLabels covers requirement 3:
// once an issue reaches a terminal state via notifyExternalClose (here, a
// TerminalLabel-driven pilot-superseded close — the same code path
// handleMerging/checkExternalMergeOrClose route through for pilot-done),
// stale in-progress/needs-human/needs-manual-rebase labels left over from
// an earlier escalation must not survive.
func TestGH5042_TerminalFinalizationShedsStaleLabels(t *testing.T) {
	initial := []string{
		github.LabelInProgress,
		labelNeedsHuman,
		labelNeedsManualRebase,
	}
	server, snapshot := gh5042LabelServer(t, 10, initial, "open")
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:      42,
		IssueNumber:   10,
		TerminalLabel: github.LabelSuperseded,
	}
	c.notifyExternalClose(context.Background(), prState)

	got := snapshot()
	for _, stale := range []string{github.LabelInProgress, labelNeedsHuman, labelNeedsManualRebase} {
		if got[stale] {
			t.Errorf("expected stale label %q shed on terminal finalization, got=%v", stale, got)
		}
	}
	if !got[github.LabelSuperseded] {
		t.Errorf("expected terminal label %q applied, got=%v", github.LabelSuperseded, got)
	}
}

// TestGH5042_MutateIssueLabelsLogsIssueAndDelta is the log-capture
// acceptance test (requirement 5): every label mutation routed through
// mutateIssueLabels must log the issue number and the exact
// added/removed label sets, so an operator can reconstruct label history
// from logs alone.
func TestGH5042_MutateIssueLabelsLogsIssueAndDelta(t *testing.T) {
	server, _ := gh5042LabelServer(t, 10, []string{labelNeedsHuman}, "open")
	defer server.Close()

	var logBuf bytes.Buffer
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.log = slog.New(slog.NewTextHandler(&logBuf, nil))

	c.mutateIssueLabels(context.Background(), 10, []string{github.LabelRetryReady}, []string{labelNeedsHuman})

	logs := logBuf.String()
	if !strings.Contains(logs, "issue=10") {
		t.Errorf("expected log to name the issue number, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, github.LabelRetryReady) {
		t.Errorf("expected log to record the added label %q, got logs:\n%s", github.LabelRetryReady, logs)
	}
	if !strings.Contains(logs, labelNeedsHuman) {
		t.Errorf("expected log to record the removed label %q, got logs:\n%s", labelNeedsHuman, logs)
	}
}
