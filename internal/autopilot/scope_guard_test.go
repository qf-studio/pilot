package autopilot

import (
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
)

func TestExtractTypeScope(t *testing.T) {
	cases := []struct {
		title           string
		wantType, wantS string
	}{
		{"feat(auth): add OAuth", "feat", "auth"},
		{"fix(upgrade): cherry-pick atomic binary replacement", "fix", "upgrade"},
		{"chore: bump deps", "", ""},                        // missing scope
		{"refactor(internal/executor): clean", "refactor", "internal/executor"},
		{"feat(auth)!: breaking change", "feat", "auth"},
		{"random title", "", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		gotType, gotS := extractTypeScope(tc.title)
		if gotType != tc.wantType || gotS != tc.wantS {
			t.Errorf("extractTypeScope(%q) = (%q,%q), want (%q,%q)",
				tc.title, gotType, gotS, tc.wantType, tc.wantS)
		}
	}
}

func TestScopeDriftReason(t *testing.T) {
	cases := []struct {
		name, prTitle, issueTitle string
		wantContains              string // empty = expect no drift
	}{
		{
			name:         "cascade-2 entry: type+scope mismatch",
			prTitle:      "feat(auth): add OAuth provider integration",
			issueTitle:   "fix(upgrade): cherry-pick atomic binary replacement",
			wantContains: "type",
		},
		{
			name:         "scope mismatch only",
			prTitle:      "feat(memory): X",
			issueTitle:   "feat(executor): Y",
			wantContains: "scope",
		},
		{
			name:       "matching prefix — no drift",
			prTitle:    "fix(executor): tweak",
			issueTitle: "fix(executor): broader description",
		},
		{
			name:       "matching type, scope unset on issue side — abstain",
			prTitle:    "fix(executor): X",
			issueTitle: "fix: X",
		},
		{
			name:       "PR has no conventional prefix — abstain (other validators handle title)",
			prTitle:    "raw title",
			issueTitle: "feat(auth): X",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScopeDriftReason(tc.prTitle, tc.issueTitle)
			if tc.wantContains == "" && got != "" {
				t.Errorf("expected no drift, got %q", got)
			}
			if tc.wantContains != "" && !strings.Contains(got, tc.wantContains) {
				t.Errorf("want reason containing %q, got %q", tc.wantContains, got)
			}
		})
	}
}

func TestSizeFloorReason(t *testing.T) {
	cases := []struct {
		name  string
		files []*github.PRFile
		want  bool // true if a reason is expected
	}{
		{
			name: "small PR — no floor",
			files: []*github.PRFile{
				{Filename: "a.go", Status: "modified", Additions: 50, Deletions: 10},
			},
		},
		{
			name: "200 LoC exactly — no floor (strict >)",
			files: []*github.PRFile{
				{Filename: "a.go", Status: "modified", Additions: 200},
			},
		},
		{
			name: "201 LoC — floor fires",
			files: []*github.PRFile{
				{Filename: "a.go", Status: "modified", Additions: 201},
			},
			want: true,
		},
		{
			name: "cascade-2 contaminating PR (~512 LoC)",
			files: []*github.PRFile{
				{Filename: "internal/gateway/oauth.go", Status: "added", Additions: 244},
				{Filename: "internal/gateway/oauth_test.go", Status: "added", Additions: 240},
				{Filename: "internal/gateway/server.go", Status: "modified", Additions: 24},
				{Filename: "cmd/pilot/main.go", Status: "modified", Additions: 3},
				{Filename: "internal/config/config.go", Status: "modified", Additions: 1},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SizeFloorReason(tc.files)
			if tc.want && got == "" {
				t.Errorf("expected size-floor to fire, got empty reason")
			}
			if !tc.want && got != "" {
				t.Errorf("expected no size-floor fire, got %q", got)
			}
		})
	}
}
