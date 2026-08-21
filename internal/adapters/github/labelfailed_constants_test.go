package github_test

// GH-5114 (GH-5100 addendum item 3): internal/retryladder intentionally
// duplicates the pilot-failed-retry-N label constants from
// internal/adapters/github/types.go:140,159-161 (see retryladder.go's
// package doc for why — it's a leaf package importable from
// internal/executor without an import cycle). This test guards that
// duplication: if either copy drifts, retry-ladder label mutations and the
// GitHub adapter's own label set would silently diverge.

import (
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/retryladder"
)

func TestRetryLadderLabelConstants_MatchGitHubAdapter(t *testing.T) {
	cases := []struct {
		name        string
		ladderValue string
		githubValue string
	}{
		{"LabelFailed", retryladder.LabelFailed, github.LabelFailed},
		{"LabelFailedRetry1", retryladder.LabelFailedRetry1, github.LabelFailedRetry1},
		{"LabelFailedRetry2", retryladder.LabelFailedRetry2, github.LabelFailedRetry2},
		{"LabelFailedRetryExhausted", retryladder.LabelFailedRetryExhausted, github.LabelFailedRetryExhausted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.ladderValue != tc.githubValue {
				t.Errorf("retryladder.%s = %q, github.%s = %q; duplicated constants have drifted",
					tc.name, tc.ladderValue, tc.name, tc.githubValue)
			}
		})
	}
}
