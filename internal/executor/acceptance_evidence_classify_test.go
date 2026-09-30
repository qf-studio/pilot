package executor

import (
	"strings"
	"testing"
)

// GH-5438: PR #5436's classifier gated AcceptanceItemMutation on an edit-cue
// regex ("change|delete|remove") over the description half of a "<desc> ->
// <outcome>" acceptance item. Every one of the six phrasings below
// ("drop …", "replace …", "make … skip …", "swap …", "disable …", "move …")
// is a real mutation item that failed that gate and fell through to
// AcceptanceItemOther — neither run nor listed under Not-verified, the
// exact silence GH-5435 was filed about. This table proves all nine
// phrasings (the three that already worked plus the six that didn't) now
// classify as AcceptanceItemMutation, and that a freeform (non
// "delete/remove line N in <file>") mutation description is correctly
// listed under Not-verified with the "freeform mutation, run manually"
// reason and its full original text once run.
func TestClassifyAcceptanceItem_MutationPhrasings(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		wantDesc   string
		wantTarget string
		// wantDeterministic is true for the two phrasings that match the
		// deterministic "delete/remove line N in <file>" shape this gate
		// can apply automatically; every other phrasing here is freeform
		// and must be reported under Not-verified instead of guessed at.
		wantDeterministic bool
	}{
		// Already worked under the old edit-cue gate.
		{
			name:              "change",
			text:              "change the retry backoff to exponential -> TestBackoff fails",
			wantDesc:          "change the retry backoff to exponential",
			wantTarget:        "TestBackoff",
			wantDeterministic: false,
		},
		{
			name:              "delete line N in file",
			text:              "delete line 4 in main.go -> TestMain fails",
			wantDesc:          "delete line 4 in main.go",
			wantTarget:        "TestMain",
			wantDeterministic: true,
		},
		{
			name:              "remove line N in file",
			text:              "remove line 10 in foo.go -> TestFoo fails",
			wantDesc:          "remove line 10 in foo.go",
			wantTarget:        "TestFoo",
			wantDeterministic: true,
		},
		// GH-5513: an unrelated negation cue earlier in the outcome half must
		// not downgrade a positive pin to other.
		{
			name:       "unrelated not before fail word",
			text:       "delete the guard -> TestFoo, not TestBar, fails",
			wantDesc:   "delete the guard",
			wantTarget: "TestFoo",
		},
		{
			name:       "does not panic and fails",
			text:       "delete the guard -> does not panic and TestFoo fails",
			wantDesc:   "delete the guard",
			wantTarget: "TestFoo",
		},
		{
			name:       "no config clause then fails",
			text:       "delete the guard -> when no config is set, TestFoo fails",
			wantDesc:   "delete the guard",
			wantTarget: "TestFoo",
		},
		// The six phrasings GH-5438 was filed about — real issue-authored
		// mutation items that PR #5436's edit-cue gate silently dropped to
		// AcceptanceItemOther.
		{
			name:              "drop",
			text:              "drop `transactionId` from the `open` call -> TestOpenNoTransactionID fails",
			wantDesc:          "drop `transactionId` from the `open` call",
			wantTarget:        "TestOpenNoTransactionID",
			wantDeterministic: false,
		},
		{
			name:              "replace",
			text:              "replace `hmac.Equal` with `==` -> TestConstantTimeCompare fails",
			wantDesc:          "replace `hmac.Equal` with `==`",
			wantTarget:        "TestConstantTimeCompare",
			wantDeterministic: false,
		},
		{
			name:              "make ... skip",
			text:              "make `main()` skip `registerBilling` -> TestMainRegistersBilling fails",
			wantDesc:          "make `main()` skip `registerBilling`",
			wantTarget:        "TestMainRegistersBilling",
			wantDeterministic: false,
		},
		{
			name:              "swap",
			text:              "swap the order of the two middleware calls -> TestMiddlewareOrder fails",
			wantDesc:          "swap the order of the two middleware calls",
			wantTarget:        "TestMiddlewareOrder",
			wantDeterministic: false,
		},
		{
			name:              "disable",
			text:              "disable the rate limiter -> TestRateLimitEnforced fails",
			wantDesc:          "disable the rate limiter",
			wantTarget:        "TestRateLimitEnforced",
			wantDeterministic: false,
		},
		{
			name:              "move ... after the await",
			text:              "move the `defer cancel()` call after the await -> TestContextCancelled fails",
			wantDesc:          "move the `defer cancel()` call after the await",
			wantTarget:        "TestContextCancelled",
			wantDeterministic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := ClassifyAcceptanceItem(tt.text)
			if item.Kind != AcceptanceItemMutation {
				t.Fatalf("Kind = %q, want %q", item.Kind, AcceptanceItemMutation)
			}
			if !item.RequiresEvidence() {
				t.Fatal("RequiresEvidence() = false, want true for a mutation item")
			}
			if item.MutationDescription != tt.wantDesc {
				t.Errorf("MutationDescription = %q, want %q", item.MutationDescription, tt.wantDesc)
			}
			if item.MutationTarget != tt.wantTarget {
				t.Errorf("MutationTarget = %q, want %q", item.MutationTarget, tt.wantTarget)
			}

			_, _, ok := parseLineMutation(item.MutationDescription)
			if ok != tt.wantDeterministic {
				t.Errorf("parseLineMutation deterministic-shape match = %v, want %v", ok, tt.wantDeterministic)
			}

			if tt.wantDeterministic {
				return
			}

			// Freeform mutations must be listed under "## Not verified" with
			// their full original text and the exact reason
			// "freeform mutation, run manually" when actually run (GH-5438
			// acceptance: "freeform ones [listed] under Not verified").
			result := runMutationItem(t.Context(), &fakeAcceptanceCommandRunner{}, t.TempDir(), item, defaultTestAllowedCommands)
			if result.NotVerifiedReason != "freeform mutation, run manually" {
				t.Errorf("NotVerifiedReason = %q, want %q", result.NotVerifiedReason, "freeform mutation, run manually")
			}
			rendered := RenderAcceptanceEvidenceSections([]AcceptanceEvidenceResult{result})
			for _, want := range []string{"## Not verified", tt.text, "freeform mutation, run manually"} {
				if !strings.Contains(rendered, want) {
					t.Errorf("rendered Not-verified section = %q, want it to contain %q", rendered, want)
				}
			}
		})
	}
}

// GH-5479: pilot#5477's "Evidence in the PR body: one `go test -run` output
// line per new test." matched none of the original paste-output phrasings, so
// it classified as other and the gate said nothing. Each new phrase must now
// classify as paste_output; a bare "PR body" or "evidence" must not.
func TestClassifyAcceptanceItem_PasteOutputEvidencePhrasings(t *testing.T) {
	const issue5477 = "Evidence in the PR body: one `go test -run` output line per new test."
	tests := []struct {
		name         string
		text         string
		want         AcceptanceItemKind
		wantCommands int
	}{
		{"#5477 sentence", issue5477, AcceptanceItemPasteOutput, 0},
		{"#5477 sentence with checkbox", "- [ ] " + issue5477, AcceptanceItemPasteOutput, 0},
		{"evidence in the pr body", "Evidence in the PR body for the new tests.", AcceptanceItemPasteOutput, 0},
		{"into the pr body", "Include the test results into the PR body.", AcceptanceItemPasteOutput, 0},
		{"output line", "One output line per new test.", AcceptanceItemPasteOutput, 0},
		{"output line with runnable command", "One output line of `go test ./pkg/` per package.", AcceptanceItemPasteOutput, 1},
		{"described in the pr body stays other", "The change is described in the PR body.", AcceptanceItemOther, 0},
		{"evidence screenshot stays other", "evidence: screenshot attached", AcceptanceItemOther, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := ClassifyAcceptanceItem(tt.text)
			if item.Kind != tt.want {
				t.Fatalf("Kind = %q, want %q", item.Kind, tt.want)
			}
			if len(item.Commands) != tt.wantCommands {
				t.Errorf("Commands = %v, want length %d", item.Commands, tt.wantCommands)
			}
		})
	}
}

// GH-5486: a mutation item that also mentions a paste-output phrase ("output
// line") must classify as mutation, not paste_output.
func TestClassifyAcceptanceItem_MutationBeatsPasteOutput(t *testing.T) {
	item := ClassifyAcceptanceItem("delete the output line in `foo.go` line 42 -> TestFoo fails")
	if item.Kind != AcceptanceItemMutation {
		t.Fatalf("Kind = %q, want %q", item.Kind, AcceptanceItemMutation)
	}
	if item.MutationTarget != "TestFoo" {
		t.Errorf("MutationTarget = %q, want TestFoo", item.MutationTarget)
	}
	if len(item.Commands) != 0 {
		t.Errorf("Commands = %v, want none", item.Commands)
	}

	paste := ClassifyAcceptanceItem("Paste the output of `go test ./...` into the PR body")
	if paste.Kind != AcceptanceItemPasteOutput {
		t.Errorf("paste item Kind = %q, want %q", paste.Kind, AcceptanceItemPasteOutput)
	}
}

// GH-5506: a bullet whose outcome half is negated ("-> TestFoo must not
// fail") asserts the opposite polarity of a mutation pin and must not be
// classified as one.
func TestClassifyAcceptanceItem_NegatedOutcomeIsNotMutation(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"must not fail", "add the guard for X -> TestFoo must not fail"},
		{"should not fail", "add the guard for X -> TestFoo should not fail"},
		{"does not fail", "add the guard for X -> TestFoo does not fail"},
		{"doesn't fail", "add the guard for X -> TestFoo doesn't fail"},
		{"never fails", "add the guard for X -> TestFoo never fails"},
		{"no test fails", "add the guard for X -> no test fails"},
		{"without failing", "add the guard for X -> TestFoo passes without failing"},
		{"no longer fails", "add the guard for X -> TestFoo no longer fails"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := ClassifyAcceptanceItem(tt.text)
			if item.Kind == AcceptanceItemMutation {
				t.Errorf("Kind = %q for %q, want anything but %q", item.Kind, tt.text, AcceptanceItemMutation)
			}
			if item.MutationTarget != "" {
				t.Errorf("MutationTarget = %q, want empty", item.MutationTarget)
			}
		})
	}

	positives := []string{
		"change X -> TestFoo fails",
		"change X -> TestFoo must fail",
		"change X -> TestFoo should fail",
		"change X -> TestFoo now fails",
		"change X -> fails",
		"change X -> TestFoo fails with 'not found'",
	}
	for _, text := range positives {
		if got := ClassifyAcceptanceItem(text).Kind; got != AcceptanceItemMutation {
			t.Errorf("Kind for %q = %q, want %q", text, got, AcceptanceItemMutation)
		}
	}
}

func TestIsDanglingFlagFragment(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"go test -run", true},
		{"go test ./pkg -bench", true},
		{"go test ./internal/executor -run TestFoo", false},
		{"go test ./...", false},
		{"go test", false},
		{"make test", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			if got := isDanglingFlagFragment(tt.command); got != tt.want {
				t.Errorf("isDanglingFlagFragment(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}
