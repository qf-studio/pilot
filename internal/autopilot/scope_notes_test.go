package autopilot

import (
	"strings"
	"testing"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

func TestBuildScopeReleaseNotes(t *testing.T) {
	tests := []struct {
		name    string
		in      ScopeNotesInput
		wantAll []string // substrings that must all appear
		wantNot []string // substrings that must NOT appear
	}{
		{
			name: "features fixes and other grouped with exact per-entry attribution",
			in: ScopeNotesInput{
				Owner:      "qf-studio",
				Repo:       "pilot",
				ScopeTitle: "Checkout epic",
				Members: []ScopeMember{
					{PR: 101, Issue: 201, Commits: []*github.Commit{makeCommit("feat(checkout): add coupon field")}},
					{PR: 102, Issue: 202, Commits: []*github.Commit{makeCommit("fix(checkout): handle nil cart")}},
					{PR: 103, Issue: 0, Commits: []*github.Commit{makeCommit("chore(deps): bump lodash")}},
				},
				LastTag: "v1.0.0",
				NewTag:  "v1.1.0",
			},
			wantAll: []string{
				"# Checkout epic",
				"## Features",
				"- add coupon field (#101, GH-201)",
				"## Bug Fixes",
				"- handle nil cart (#102, GH-202)",
				"## Other Changes",
				"- bump lodash (#103)",
				"**Full Changelog**: https://github.com/qf-studio/pilot/compare/v1.0.0...v1.1.0",
				"_3 PRs, 3 commits_",
			},
			wantNot: []string{"GH-0", "## ⚠ Breaking Changes"},
		},
		{
			name: "breaking via ! suffix",
			in: ScopeNotesInput{
				ScopeTitle: "Auth rework",
				Members: []ScopeMember{
					{PR: 201, Issue: 301, Commits: []*github.Commit{makeCommit("feat(auth)!: drop legacy tokens")}},
				},
				LastTag: "v1.0.0",
				NewTag:  "v2.0.0",
			},
			wantAll: []string{
				"## ⚠ Breaking Changes",
				"- drop legacy tokens (#201, GH-301)",
				"## Features",
			},
		},
		{
			name: "breaking via BREAKING commit type prefix",
			in: ScopeNotesInput{
				ScopeTitle: "Auth rework",
				Members: []ScopeMember{
					{PR: 202, Commits: []*github.Commit{makeCommit("BREAKING: remove v1 endpoints")}},
				},
				LastTag: "v1.0.0",
				NewTag:  "v2.0.0",
			},
			wantAll: []string{
				"## ⚠ Breaking Changes",
				"- remove v1 endpoints (#202)",
			},
		},
		{
			name: "BREAKING CHANGE footer form is out of scope, matching bump detection",
			in: ScopeNotesInput{
				ScopeTitle: "Auth rework",
				Members: []ScopeMember{
					{PR: 203, Commits: []*github.Commit{makeCommit("feat(auth): rotate keys\n\nBREAKING CHANGE: old keys invalid")}},
				},
				LastTag: "v1.0.0",
				NewTag:  "v1.1.0",
			},
			wantAll: []string{"## Features", "- rotate keys (#203)"},
			wantNot: []string{"## ⚠ Breaking Changes"},
		},
		{
			name: "non-conventional commit message falls back to Other Changes",
			in: ScopeNotesInput{
				ScopeTitle: "Misc",
				Members: []ScopeMember{
					{PR: 301, Commits: []*github.Commit{makeCommit("update the README typo")}},
				},
				LastTag: "v1.0.0",
				NewTag:  "v1.0.1",
			},
			wantAll: []string{"## Other Changes", "- update the README typo (#301)"},
			wantNot: []string{"## Features", "## Bug Fixes"},
		},
		{
			name: "empty scope title falls back to scope key",
			in: ScopeNotesInput{
				ScopeKey: "label:checkout",
				Members:  nil,
				LastTag:  "v1.0.0",
				NewTag:   "v1.0.0",
			},
			wantAll: []string{"# label:checkout", "_0 PRs, 0 commits_"},
			wantNot: []string{"## Features", "## Bug Fixes", "## Other Changes", "## ⚠ Breaking Changes"},
		},
		{
			name: "empty members produce headline and footer only",
			in: ScopeNotesInput{
				ScopeTitle: "Empty scope",
				Members:    []ScopeMember{{PR: 401, Commits: nil}},
				LastTag:    "v1.0.0",
				NewTag:     "v1.0.0",
			},
			wantAll: []string{"# Empty scope", "_1 PRs, 0 commits_"},
			wantNot: []string{"## Features", "## Bug Fixes", "## Other Changes"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildScopeReleaseNotes(tt.in)
			for _, want := range tt.wantAll {
				if !strings.Contains(got, want) {
					t.Errorf("BuildScopeReleaseNotes() missing %q\n--- got ---\n%s", want, got)
				}
			}
			for _, notWant := range tt.wantNot {
				if strings.Contains(got, notWant) {
					t.Errorf("BuildScopeReleaseNotes() unexpectedly contains %q\n--- got ---\n%s", notWant, got)
				}
			}
		})
	}
}
