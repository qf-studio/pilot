package autopilot

import (
	"strings"
	"testing"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

func TestBuildScopeReleaseNotes(t *testing.T) {
	tests := []struct {
		name           string
		in             ScopeNotesInput
		wantContains   []string
		wantNotContain []string
	}{
		{
			name: "features fixes and other grouped with attribution",
			in: ScopeNotesInput{
				Owner:      "owner",
				Repo:       "repo",
				ScopeTitle: "Epic: Ship Widgets",
				Members: []ScopeMember{
					{PR: 10, Issue: 100, Commits: []*github.Commit{makeCommit("feat(widgets): add resize handle")}},
					{PR: 11, Issue: 101, Commits: []*github.Commit{makeCommit("fix(widgets): correct off-by-one")}},
					{PR: 12, Issue: 102, Commits: []*github.Commit{makeCommit("chore(deps): bump lodash")}},
				},
				LastTag: "v1.0.0",
				NewTag:  "v1.1.0",
			},
			wantContains: []string{
				"# Epic: Ship Widgets",
				"## Features",
				"- add resize handle (#10, GH-100)",
				"## Bug Fixes",
				"- correct off-by-one (#11, GH-101)",
				"## Other Changes",
				"- bump lodash (#12, GH-102)",
				"**Full Changelog**: https://github.com/owner/repo/compare/v1.0.0...v1.1.0",
				"_3 PRs, 3 commits_",
			},
			wantNotContain: []string{"⚠ Breaking Changes"},
		},
		{
			name: "breaking change via bang suffix",
			in: ScopeNotesInput{
				ScopeTitle: "Scope A",
				Members: []ScopeMember{
					{PR: 20, Issue: 200, Commits: []*github.Commit{makeCommit("feat(api)!: remove legacy endpoint")}},
				},
				LastTag: "v1.0.0",
				NewTag:  "v2.0.0",
			},
			wantContains: []string{
				"## ⚠ Breaking Changes",
				"- remove legacy endpoint (#20, GH-200)",
				"## Features",
			},
		},
		{
			name: "breaking change via BREAKING prefix",
			in: ScopeNotesInput{
				ScopeTitle: "Scope B",
				Members: []ScopeMember{
					{PR: 21, Issue: 201, Commits: []*github.Commit{makeCommit("BREAKING: drop support for v1 tokens")}},
				},
				LastTag: "v1.0.0",
				NewTag:  "v2.0.0",
			},
			wantContains: []string{
				"## ⚠ Breaking Changes",
				"- drop support for v1 tokens (#21, GH-201)",
			},
		},
		{
			name: "non-conventional commit lands in Other Changes verbatim",
			in: ScopeNotesInput{
				ScopeTitle: "Scope C",
				Members: []ScopeMember{
					{PR: 30, Issue: 300, Commits: []*github.Commit{makeCommit("quick typo fix")}},
				},
				LastTag: "v1.0.0",
				NewTag:  "v1.0.1",
			},
			wantContains: []string{
				"## Other Changes",
				"- quick typo fix (#30, GH-300)",
			},
		},
		{
			name: "member with no linked issue omits GH- attribution",
			in: ScopeNotesInput{
				ScopeTitle: "Scope D",
				Members: []ScopeMember{
					{PR: 40, Issue: 0, Commits: []*github.Commit{makeCommit("fix: patch race condition")}},
				},
				LastTag: "v1.0.0",
				NewTag:  "v1.0.1",
			},
			wantContains:   []string{"- patch race condition (#40)"},
			wantNotContain: []string{"GH-0"},
		},
		{
			name: "empty members produce headline, footer, and zero counts",
			in: ScopeNotesInput{
				ScopeTitle: "Scope E",
				LastTag:    "v1.0.0",
				NewTag:     "v1.0.0",
			},
			wantContains: []string{
				"# Scope E",
				"**Full Changelog**: https://github.com///compare/v1.0.0...v1.0.0",
				"_0 PRs, 0 commits_",
			},
			wantNotContain: []string{"## Features", "## Bug Fixes", "## Other Changes", "⚠ Breaking Changes"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildScopeReleaseNotes(tt.in)
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("BuildScopeReleaseNotes() = %q\nmissing substring %q", got, want)
				}
			}
			for _, notWant := range tt.wantNotContain {
				if strings.Contains(got, notWant) {
					t.Errorf("BuildScopeReleaseNotes() = %q\nunexpectedly contains %q", got, notWant)
				}
			}
		})
	}
}
