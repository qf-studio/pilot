package retryladder

import "testing"

// TestNextRung_RungTransitions mirrors
// internal/executor's TestNextFailedRetryLabel_RungTransitions (the
// pre-extraction test) — this is the canonical rung-computation routine
// both that test and this package's callers now delegate to (GH-5098).
func TestNextRung_RungTransitions(t *testing.T) {
	tests := []struct {
		name       string
		labels     []string
		wantAdd    string
		wantRemove string
	}{
		{
			name:       "no ladder label yet -> retry-1",
			labels:     nil,
			wantAdd:    LabelFailedRetry1,
			wantRemove: "",
		},
		{
			name:       "unrelated labels only -> retry-1",
			labels:     []string{"bug", "pilot"},
			wantAdd:    LabelFailedRetry1,
			wantRemove: "",
		},
		{
			name:       "retry-1 present -> retry-2, removes retry-1",
			labels:     []string{LabelFailedRetry1},
			wantAdd:    LabelFailedRetry2,
			wantRemove: LabelFailedRetry1,
		},
		{
			name:       "retry-2 present -> exhausted, removes retry-2",
			labels:     []string{LabelFailedRetry2},
			wantAdd:    LabelFailedRetryExhausted,
			wantRemove: LabelFailedRetry2,
		},
		{
			name:       "exhausted present -> no further advancement",
			labels:     []string{LabelFailedRetryExhausted},
			wantAdd:    "",
			wantRemove: "",
		},
		{
			name:       "case-insensitive match on existing rung",
			labels:     []string{"PILOT-FAILED-RETRY-1"},
			wantAdd:    LabelFailedRetry2,
			wantRemove: LabelFailedRetry1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAdd, gotRemove := NextRung(tt.labels)
			if gotAdd != tt.wantAdd || gotRemove != tt.wantRemove {
				t.Errorf("NextRung(%v) = (%q, %q), want (%q, %q)",
					tt.labels, gotAdd, gotRemove, tt.wantAdd, tt.wantRemove)
			}
		})
	}
}

// TestAdvance_SingleMutationInvariant asserts Advance folds the
// already-failed guard and the rung computation into one decision, so a
// caller gets everything needed for a single label-edit call without a
// separate follow-up mutation. GH-5077's core invariant: a repeat
// pilot-failed application (hasFailed == true) must never advance the
// ladder again, regardless of what rung the current labels show.
func TestAdvance_SingleMutationInvariant(t *testing.T) {
	tests := []struct {
		name          string
		labels        []string
		hasFailed     bool
		wantAdd       string
		wantRemove    string
		wantExhausted bool
	}{
		{
			name:      "fresh failure, no rung yet -> stamps retry-1",
			labels:    nil,
			hasFailed: false,
			wantAdd:   LabelFailedRetry1,
		},
		{
			name:       "fresh failure, retry-1 present -> advances to retry-2",
			labels:     []string{LabelFailedRetry1},
			hasFailed:  false,
			wantAdd:    LabelFailedRetry2,
			wantRemove: LabelFailedRetry1,
		},
		{
			name:          "fresh failure, retry-2 present -> exhausts the ladder",
			labels:        []string{LabelFailedRetry2},
			hasFailed:     false,
			wantAdd:       LabelFailedRetryExhausted,
			wantRemove:    LabelFailedRetry2,
			wantExhausted: true,
		},
		{
			name:      "fresh failure, already exhausted -> no further advancement",
			labels:    []string{LabelFailedRetryExhausted},
			hasFailed: false,
			wantAdd:   "",
		},
		{
			name:      "duplicate fail event (hasFailed=true) -> never advances, even mid-ladder",
			labels:    []string{LabelFailedRetry1},
			hasFailed: true,
			wantAdd:   "",
		},
		{
			name:      "duplicate fail event at exhausted rung -> still no-op",
			labels:    []string{LabelFailedRetryExhausted},
			hasFailed: true,
			wantAdd:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAdd, gotRemove, gotExhausted := Advance(tt.labels, tt.hasFailed)
			if gotAdd != tt.wantAdd || gotRemove != tt.wantRemove || gotExhausted != tt.wantExhausted {
				t.Errorf("Advance(%v, %v) = (%q, %q, %v), want (%q, %q, %v)",
					tt.labels, tt.hasFailed, gotAdd, gotRemove, gotExhausted,
					tt.wantAdd, tt.wantRemove, tt.wantExhausted)
			}
		})
	}
}
