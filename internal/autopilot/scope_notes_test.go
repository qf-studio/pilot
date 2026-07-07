package autopilot

import (
	"strings"
	"testing"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestBuildScopeReleaseNotes covers the locked notes format: headline,
// grouped Features/Fixes/Other sections with #PR+GH-issue attribution,
// breaking changes (both "!" and BREAKING-prefix forms), non-conventional
// commits, empty members, link formatting, and footer counts (GH-3992).
func TestBuildScopeReleaseNotes(t *testing.T) {
	tests := []struct {
		name       string
		in         ScopeNotesInput
		wantSubstr []string
		wantAbsent []string
	}{
		{
			name: "features fixes other grouped with attribution",
			in: ScopeNotesInput{
				Owner:      "owner",
				Repo:       "repo",
				ScopeTitle: "Checkout Revamp",
				LastTag:    "v1.0.0",
				NewTag:     "v1.1.0",
				Members: []ScopeMember{
					{PR: 101, Issue: 201, Commits: []*github.Commit{makeCommit("feat(checkout): add express pay")}},
					{PR: 102, Issue: 202, Commits: []*github.Commit{makeCommit("fix(checkout): correct tax calc")}},
					{PR: 103, Issue: 0, Commits: []*github.Commit{makeCommit("chore: bump deps")}},
				},
			},
			wantSubstr: []string{
				"# Checkout Revamp",
				"## Features",
				"- add express pay (#101, GH-201)",
				"## Bug Fixes",
				"- correct tax calc (#102, GH-202)",
				"## Other Changes",
				"- bump deps (#103)",
				"**Full Changelog**: https://github.com/owner/repo/compare/v1.0.0...v1.1.0",
				"_3 PRs, 3 commits_",
			},
			wantAbsent: []string{"GH-0", "⚠ Breaking Changes"},
		},
		{
			name: "breaking change via bang suffix",
			in: ScopeNotesInput{
				Owner: "owner", Repo: "repo", ScopeTitle: "Auth", LastTag: "v1.0.0", NewTag: "v2.0.0",
				Members: []ScopeMember{
					{PR: 201, Issue: 301, Commits: []*github.Commit{makeCommit("feat(auth)!: drop legacy tokens")}},
				},
			},
			wantSubstr: []string{
				"## Features",
				"- drop legacy tokens (#201, GH-301)",
				"## ⚠ Breaking Changes",
			},
		},
		{
			name: "breaking change via BREAKING prefix",
			in: ScopeNotesInput{
				Owner: "owner", Repo: "repo", ScopeTitle: "Auth", LastTag: "v1.0.0", NewTag: "v2.0.0",
				Members: []ScopeMember{
					{PR: 202, Issue: 302, Commits: []*github.Commit{makeCommit("BREAKING: remove v1 API")}},
				},
			},
			wantSubstr: []string{
				"## ⚠ Breaking Changes",
				"- remove v1 API (#202, GH-302)",
			},
		},
		{
			name: "non-conventional commit falls into Other Changes",
			in: ScopeNotesInput{
				Owner: "owner", Repo: "repo", ScopeTitle: "Misc", LastTag: "v1.0.0", NewTag: "v1.0.1",
				Members: []ScopeMember{
					{PR: 301, Issue: 401, Commits: []*github.Commit{makeCommit("update readme wording")}},
				},
			},
			wantSubstr: []string{
				"## Other Changes",
				"- update readme wording (#301, GH-401)",
			},
			wantAbsent: []string{"## Features", "## Bug Fixes"},
		},
		{
			name: "empty members still renders headline and footer",
			in: ScopeNotesInput{
				Owner: "owner", Repo: "repo", ScopeTitle: "Nothing", LastTag: "v1.0.0", NewTag: "v1.0.0",
			},
			wantSubstr: []string{
				"# Nothing",
				"**Full Changelog**: https://github.com/owner/repo/compare/v1.0.0...v1.0.0",
				"_0 PRs, 0 commits_",
			},
			wantAbsent: []string{"## Features", "## Bug Fixes", "## Other Changes", "⚠ Breaking Changes"},
		},
		{
			name: "empty scope title falls back to scope key",
			in: ScopeNotesInput{
				Owner: "owner", Repo: "repo", ScopeKey: "epic:42", LastTag: "v1.0.0", NewTag: "v1.0.1",
			},
			wantSubstr: []string{"# epic:42"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildScopeReleaseNotes(tt.in)
			for _, want := range tt.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("BuildScopeReleaseNotes() missing %q\ngot:\n%s", want, got)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("BuildScopeReleaseNotes() unexpectedly contains %q\ngot:\n%s", absent, got)
				}
			}
		})
	}
}

func TestClosingIssueNumber(t *testing.T) {
	tests := []struct {
		body string
		want int
	}{
		{"## Summary\n\nCloses #123\n\n## Changes", 123},
		{"fixes #45", 45},
		{"Resolves #7 and does other stuff", 7},
		{"no reference here", 0},
		{"", 0},
	}
	for _, tt := range tests {
		if got := closingIssueNumber(tt.body); got != tt.want {
			t.Errorf("closingIssueNumber(%q) = %d, want %d", tt.body, got, tt.want)
		}
	}
}
