// Package retryladder computes the pilot-failed-retry-N label ladder
// mutation shared by every Pilot call site that stamps pilot-failed onto a
// GitHub issue: internal/executor (postTitleRejectionEscalation, GH-5077),
// internal/autopilot, internal/adapters/github, and cmd/pilot.
//
// GH-5098: extracted from internal/executor/title_rejection.go's
// nextFailedRetryLabel so the rung-computation isn't re-implemented at each
// call site. This package intentionally has zero internal/... dependencies
// of its own — a leaf package — so every caller can import it directly
// without risking an import cycle. In particular internal/executor cannot
// import internal/adapters/github (adapters/github imports internal/comms,
// which imports internal/executor — see title_rejection.go's
// labelPilotFailed comment), so the canonical label values from
// internal/adapters/github/types.go are duplicated here as plain string
// constants rather than imported, per Pilot's documented
// label-constant-duplication idiom.
package retryladder

import "strings"

// Label name constants for the pilot-failed-retry-N ladder. Mirror
// adapters/github.LabelFailed / LabelFailedRetry1/2/Exhausted
// (internal/adapters/github/types.go:140,159-161).
const (
	LabelFailed               = "pilot-failed"
	LabelFailedRetry1         = "pilot-failed-retry-1"
	LabelFailedRetry2         = "pilot-failed-retry-2"
	LabelFailedRetryExhausted = "pilot-failed-retry-exhausted"
)

// NextRung computes the pilot-failed-retry-N ladder mutation (none ->
// pilot-failed-retry-1 -> pilot-failed-retry-2 ->
// pilot-failed-retry-exhausted) for a single, fresh pilot-failed
// application. currentLabels is the issue's live label set read immediately
// before this event's mutation. Returns the label to add and (if any) the
// label to remove — the ladder holds exactly one rung label at a time, so
// advancing always removes the previous rung.
//
// Both return values are empty once the issue has already reached the
// terminal pilot-failed-retry-exhausted rung: GH-5077 requires no further
// advancement past exhaustion.
func NextRung(currentLabels []string) (add, remove string) {
	has := func(name string) bool {
		for _, l := range currentLabels {
			if strings.EqualFold(l, name) {
				return true
			}
		}
		return false
	}
	switch {
	case has(LabelFailedRetryExhausted):
		return "", ""
	case has(LabelFailedRetry2):
		return LabelFailedRetryExhausted, LabelFailedRetry2
	case has(LabelFailedRetry1):
		return LabelFailedRetry2, LabelFailedRetry1
	default:
		return LabelFailedRetry1, ""
	}
}

// Advance is the reusable "stamp pilot-failed + advance the retry rung in a
// single label mutation" helper (GH-5098). Given an issue's current label
// set (read immediately before the mutation) and whether the issue already
// carries pilot-failed, it returns the ladder label to add and (if any)
// remove for that one mutation, plus whether the ladder just reached its
// terminal rung — all inputs a caller needs to fold into a single
// add/remove label-edit call alongside its own pilot-failed application.
//
// Ladder advancement only happens for a fresh pilot-failed application
// (hasFailed == false): a repeat pilot-failed application on an issue that
// already carries the label is a duplicate fail-label event and must not
// advance the ladder again — see
// internal/executor/title_rejection.go's postTitleRejectionEscalation,
// the original call site this was extracted from.
func Advance(currentLabels []string, hasFailed bool) (add, remove string, exhausted bool) {
	if hasFailed {
		return "", "", false
	}
	add, remove = NextRung(currentLabels)
	return add, remove, add == LabelFailedRetryExhausted
}
