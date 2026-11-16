package main

import (
	"testing"

	"github.com/qf-studio/pilot/internal/ghissue"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestPollerAdmit_GateAndCountAgree pins GH-6391: every label the host
// admission gate refuses (githubLiveAdmissionSkipReason) comes from
// ghissue.AdmissionGateLabels, and the lane-starvation count
// (ghissue.PollerAdmits) refuses the same issue, so the two cannot differ.
func TestPollerAdmit_GateAndCountAgree(t *testing.T) {
	for _, label := range ghissue.AdmissionGateLabels {
		t.Run(label, func(t *testing.T) {
			want, ok := admissionGateSkipReasons[label]
			if !ok {
				t.Fatalf("admissionGateSkipReasons has no skip reason for gate label %q", label)
			}
			if got := githubLiveAdmissionSkipReason(githubSDK.StateOpen, []string{"pilot", label}, "pilot"); got != want {
				t.Errorf("githubLiveAdmissionSkipReason = %q, want %q", got, want)
			}
			issue := &githubSDK.Issue{
				Number: 1,
				State:  githubSDK.StateOpen,
				Labels: []githubSDK.Label{{Name: "pilot"}, {Name: label}},
			}
			if ghissue.PollerAdmits(issue, "pilot") {
				t.Errorf("PollerAdmits = true for an issue the admission gate refuses (%q)", label)
			}
		})
	}

	t.Run("admissible issue passes both", func(t *testing.T) {
		if got := githubLiveAdmissionSkipReason(githubSDK.StateOpen, []string{"pilot"}, "pilot"); got != "" {
			t.Errorf("githubLiveAdmissionSkipReason = %q, want admitted", got)
		}
		issue := &githubSDK.Issue{Number: 1, State: githubSDK.StateOpen, Labels: []githubSDK.Label{{Name: "pilot"}}}
		if !ghissue.PollerAdmits(issue, "pilot") {
			t.Error("PollerAdmits = false for an issue the admission gate admits")
		}
	})
}
