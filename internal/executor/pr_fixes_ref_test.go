package executor

import "testing"

// GH-5165: extraFixesKeyword must propagate an explicit closing-keyword
// reference to another issue (e.g. a decomposed sub-issue's body pointing
// back at the external report the whole epic ultimately resolves) into the
// generated PR body as a real "Fixes #N" line — the actual repro that
// motivated this: GH-5165's own body reads "Ensure the PR body includes
// 'Fixes #5149'."
//
// GH-5191: it must NOT promote prose occurrences of the same keywords —
// quoted-but-embedded, negated, or merely descriptive mentions of "closes
// #N"/"fixes #N"/"resolves #N" mid-sentence must stay plain text, since
// promoting them would silently attach an unrelated auto-close keyword to
// the generated PR.
func TestExtraFixesKeyword(t *testing.T) {
	tests := []struct {
		name        string
		description string
		ownIssueNum string
		want        string
	}{
		{
			name:        "explicit Fixes reference distinct from own issue",
			description: "Ensure the PR body includes 'Fixes #5149'. Note that the GitHub approval handler is explicitly out of scope for this change.",
			ownIssueNum: "5165",
			want:        "\nFixes #5149",
		},
		{
			name:        "no reference present",
			description: "Just a plain description with no closing keywords.",
			ownIssueNum: "5165",
			want:        "",
		},
		{
			name:        "reference matches own issue number, deduped away",
			description: "This closes #5165, nothing else.",
			ownIssueNum: "5165",
			want:        "",
		},
		{
			name:        "case-insensitive and multiple keyword forms",
			description: "fixes #10\nCLOSES #20\nResolves #30",
			ownIssueNum: "99",
			want:        "\nFixes #10\nFixes #20\nFixes #30",
		},
		{
			name:        "duplicate references collapse to one line",
			description: "Fixes #5149. Also fixes #5149 again.",
			ownIssueNum: "5165",
			want:        "\nFixes #5149",
		},
		{
			name:        "empty description",
			description: "",
			ownIssueNum: "5165",
			want:        "",
		},
		{
			name:        "descriptive mid-sentence usage is not promoted",
			description: "this bug closes #123 prematurely, which is itself the defect",
			ownIssueNum: "5165",
			want:        "",
		},
		{
			name:        "descriptive mid-sentence usage with resolves is not promoted",
			description: "the old fix resolves #99 only partially and needs revisiting",
			ownIssueNum: "5165",
			want:        "",
		},
		{
			name:        "negated narrative usage is not promoted",
			description: "It never really fixes #42, that ticket needs separate follow-up work.",
			ownIssueNum: "5165",
			want:        "",
		},
		{
			name:        "quoted marker with trailing words is not promoted",
			description: `The changelog says "this closes #123 in theory" but actually doesn't.`,
			ownIssueNum: "5165",
			want:        "",
		},
		{
			name:        "structured marker still promoted alongside descriptive noise",
			description: "This description mentions that the bug closes #123 prematurely, but the real fix is:\nFixes #456",
			ownIssueNum: "5165",
			want:        "\nFixes #456",
		},
		{
			name:        "bullet-list Refs-style marker is promoted",
			description: "## Refs\n\n- Closes #789\n- Related to the epic, not a closer: fixes #999 in another package though",
			ownIssueNum: "5165",
			want:        "\nFixes #789",
		},
		{
			// GH-5198: a quoted marker with no line-anchor and no
			// imperative cue ("include"/"add") preceding it must not be
			// promoted, even when the surrounding sentence is itself a
			// negated instruction not to write the marker.
			name:        "negated instruction wrapping a quoted marker is not promoted",
			description: `Do not write "closes #123" in the PR body.`,
			ownIssueNum: "5165",
			want:        "",
		},
		{
			// GH-5198: a line-anchored marker followed by a comma and
			// trailing prose must not be promoted — the terminator after
			// the ref must be end-of-line or a bare period, not any
			// punctuation character.
			name:        "line-anchored marker with trailing prose after comma is not promoted",
			description: "Closes #123, but only partially — needs follow-up",
			ownIssueNum: "5165",
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extraFixesKeyword(tt.description, tt.ownIssueNum)
			if got != tt.want {
				t.Errorf("extraFixesKeyword(%q, %q) = %q, want %q", tt.description, tt.ownIssueNum, got, tt.want)
			}
		})
	}
}
