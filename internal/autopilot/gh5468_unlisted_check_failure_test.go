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

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestCIMonitor_UnlistedCheckFailure_GH5468 is the regression test for
// GH-5468: pilot-console PR#337 (2026-09-28) squash-merged with "Check
// Wire-Contract Tests" red because the project has no ci_checks override and
// inherited the shared global required_checks allowlist [test, lint] (tuned
// for the pilot repo's own CI job names). checkRequiredChecks only ever
// populated requiredStatus for those two names, so the third, discovered
// check-run's failure was invisible to aggregateStatus no matter how red it
// was — "CI checks discovered" logged all five names, but "CI passed" fired
// two seconds after test and lint alone went green.
//
// checkRequiredChecks now inspects every discovered check-run outside the
// allowlist (and outside ci_checks.exclude) too: a completed failure there
// still fails the gate regardless of what the allowlisted checks report; a
// still-pending one does not, since the allowlist alone decides what to wait
// for.
func TestCIMonitor_UnlistedCheckFailure_GH5468(t *testing.T) {
	tests := []struct {
		name               string
		unlistedStatus     string
		unlistedConclusion string
		exclude            []string
		want               CIStatus
	}{
		{
			name:               "unlisted check completed and failed blocks the merge",
			unlistedStatus:     github.CheckRunCompleted,
			unlistedConclusion: github.ConclusionFailure,
			want:               CIFailure,
		},
		{
			name:               "unlisted check still pending does not block (allowlist decides what to wait for)",
			unlistedStatus:     github.CheckRunInProgress,
			unlistedConclusion: "",
			want:               CISuccess,
		},
		{
			name:               "unlisted check named in ci_checks.exclude does not block",
			unlistedStatus:     github.CheckRunCompleted,
			unlistedConclusion: github.ConclusionFailure,
			exclude:            []string{"Check Wire-Contract Tests"},
			want:               CISuccess,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/qf-studio/pilot-console/commits/deadbeef/check-runs" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				resp := github.CheckRunsResponse{
					TotalCount: 3,
					CheckRuns: []github.CheckRun{
						{Name: "test", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
						{Name: "lint", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
						{Name: "Check Wire-Contract Tests", Status: tt.unlistedStatus, Conclusion: tt.unlistedConclusion},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			// Mirrors the live pilot-console config shape: no ci_checks block
			// of its own, just the inherited legacy required_checks allowlist.
			cfg := DefaultConfig()
			cfg.RequiredChecks = []string{"test", "lint"}
			if tt.exclude != nil {
				cfg.CIChecks = &CIChecksConfig{Exclude: tt.exclude}
			}

			monitor := NewCIMonitor(ghClient, "qf-studio", "pilot-console", cfg)
			var logBuf bytes.Buffer
			monitor.log = slog.New(slog.NewTextHandler(&logBuf, nil))

			status, err := monitor.CheckCI(context.Background(), "deadbeef")
			if err != nil {
				t.Fatalf("CheckCI() error = %v", err)
			}
			if status != tt.want {
				t.Fatalf("CheckCI() = %s, want %s", status, tt.want)
			}

			if tt.want == CIFailure && !strings.Contains(logBuf.String(), "Check Wire-Contract Tests") {
				t.Errorf("expected the Warn log naming the failing unlisted check, got:\n%s", logBuf.String())
			}
		})
	}
}
