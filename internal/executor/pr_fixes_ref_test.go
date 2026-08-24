package executor

import "testing"

// GH-5165: extraFixesKeyword must propagate an explicit closing-keyword
// reference to another issue (e.g. a decomposed sub-issue's body pointing
// back at the external report the whole epic ultimately resolves) into the
// generated PR body as a real "Fixes #N" line — the actual repro that
// motivated this: GH-5165's own body reads "Ensure the PR body includes
// 'Fixes #5149'."
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
