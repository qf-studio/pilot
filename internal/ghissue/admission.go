package ghissue

import (
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// LabelNeedsHuman parks an issue for a human after Pilot gave up on it
// (GH-5056). The SDK poller's candidate filter does not know it; the host
// admission gate (cmd/pilot githubLiveAdmissionSkipReason) refuses it.
const LabelNeedsHuman = "pilot-needs-human"

// AdmissionGateLabels are the labels the host admission gate of the GitHub
// poller path (cmd/pilot githubLiveAdmissionSkipReason) refuses, in the order
// it checks them. The gate iterates this list, so a label added here is
// refused there too.
var AdmissionGateLabels = []string{
	LabelNeedsHuman,
	github.LabelBlocked,
	github.LabelNeedsClarification,
}

// NotAdmittedLabels are the labels that keep an open trigger-labelled issue
// out of dispatch on the GitHub poller path: the AdmissionGateLabels, the
// unconditional skips of the SDK poller's candidate filter (pilot-in-progress,
// pilot-done), and pilot-spec-incomplete — the spec guard set it on the first
// strike and the next pick of an unchanged body escalates to pilot-blocked
// instead of dispatching.
var NotAdmittedLabels = append(append([]string{}, AdmissionGateLabels...),
	github.LabelInProgress,
	github.LabelDone,
	github.LabelSpecIncomplete,
)

// PollerAdmits reports whether the GitHub poller path would admit item for
// dispatch on its labels alone: it is an open issue (not a pull request —
// the issues list API returns both), it carries triggerLabel, and it carries
// none of NotAdmittedLabels. Conditional skips that need more than the item
// (pilot-failed / pilot-retry-ready retry windows, superseded parents,
// pending dependencies) are not modelled here.
func PollerAdmits(item *github.Issue, triggerLabel string) bool {
	if item == nil || item.PullRequest != nil {
		return false
	}
	if item.State != "" && item.State != github.StateOpen {
		return false
	}
	if !github.HasLabel(item, triggerLabel) {
		return false
	}
	for _, l := range NotAdmittedLabels {
		if github.HasLabel(item, l) {
			return false
		}
	}
	return true
}
