package ghissue

import (
	"slices"
	"testing"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

func TestPollerAdmits(t *testing.T) {
	issue := func(state string, pr bool, labels ...string) *github.Issue {
		is := &github.Issue{Number: 1, State: state}
		for _, l := range labels {
			is.Labels = append(is.Labels, github.Label{Name: l})
		}
		if pr {
			is.PullRequest = &struct{}{}
		}
		return is
	}

	tests := []struct {
		name  string
		issue *github.Issue
		want  bool
	}{
		{name: "nil", issue: nil, want: false},
		{name: "open issue with the trigger label", issue: issue(github.StateOpen, false, "pilot"), want: true},
		{name: "trigger label compared case-insensitively", issue: issue(github.StateOpen, false, "Pilot"), want: true},
		{name: "pull request with the trigger label", issue: issue(github.StateOpen, true, "pilot"), want: false},
		{name: "closed issue", issue: issue(github.StateClosed, false, "pilot"), want: false},
		{name: "no trigger label", issue: issue(github.StateOpen, false, "bug"), want: false},
		{name: "needs human", issue: issue(github.StateOpen, false, "pilot", LabelNeedsHuman), want: false},
		{name: "blocked", issue: issue(github.StateOpen, false, "pilot", github.LabelBlocked), want: false},
		{name: "needs clarification", issue: issue(github.StateOpen, false, "pilot", github.LabelNeedsClarification), want: false},
		{name: "spec incomplete", issue: issue(github.StateOpen, false, "pilot", github.LabelSpecIncomplete), want: false},
		{name: "in progress", issue: issue(github.StateOpen, false, "pilot", github.LabelInProgress), want: false},
		{name: "done", issue: issue(github.StateOpen, false, "pilot", github.LabelDone), want: false},
		{name: "unrelated extra label", issue: issue(github.StateOpen, false, "pilot", "bug"), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PollerAdmits(tt.issue, "pilot"); got != tt.want {
				t.Errorf("PollerAdmits = %v, want %v", got, tt.want)
			}
		})
	}
}

// mutation pin: NotAdmittedLabels lists the SDK candidate skips inline instead of through SDKCandidateSkipLabels -> this test fails
func TestNotAdmittedLabels_Composition(t *testing.T) {
	var want []string
	want = append(want, AdmissionGateLabels...)
	want = append(want, SDKCandidateSkipLabels...)
	want = append(want, github.LabelSpecIncomplete)
	if !slices.Equal(NotAdmittedLabels, want) {
		t.Fatalf("NotAdmittedLabels = %v, want AdmissionGateLabels + SDKCandidateSkipLabels + %s = %v",
			NotAdmittedLabels, github.LabelSpecIncomplete, want)
	}
	if !slices.Equal(SDKCandidateSkipLabels, []string{github.LabelInProgress, github.LabelDone}) {
		t.Errorf("SDKCandidateSkipLabels = %v, want the SDK filter's unconditional skips [%s %s]",
			SDKCandidateSkipLabels, github.LabelInProgress, github.LabelDone)
	}
}
