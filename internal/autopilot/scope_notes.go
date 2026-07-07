package autopilot

import (
	"fmt"
	"strings"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// ScopeMember pairs one member PR of a scope-release carrier with the commits
// fetched specifically for that PR (scopeReleaseCommits/GetPRCommits already
// fetches per-member, GH-3990) and, when resolvable, the GitHub issue it
// closed. Carrying both lets BuildScopeReleaseNotes attribute every changelog
// entry to an exact "(#PR, GH-Issue)" pair instead of falling back to the
// scope's single anchor issue for every entry (GH-3992).
type ScopeMember struct {
	PR      int
	Issue   int // 0 when the member PR carries no resolvable issue reference
	Commits []*github.Commit
}

// ScopeNotesInput is the input to BuildScopeReleaseNotes.
type ScopeNotesInput struct {
	Owner      string
	Repo       string
	ScopeKey   string
	ScopeTitle string
	Members    []ScopeMember
	LastTag    string
	NewTag     string
}

// BuildScopeReleaseNotes renders the deterministic body for a scope-release
// carrier: a headline, commits grouped into Features/Bug Fixes/Other Changes
// (the same classification GenerateChangelog uses, releaser.go ~:208, but
// with a per-entry "(#PR, GH-Issue)" attribution instead of one shared PR
// number for the whole release), a "## ⚠ Breaking Changes" section when any
// commit's first line trips parseBumpFromMessage's breaking predicate (`!`
// suffix or a "BREAKING" commit type — body `BREAKING CHANGE:` footers are
// out of scope, matching bump detection), and a compare-link + PR/commit
// count footer (GH-3992).
func BuildScopeReleaseNotes(in ScopeNotesInput) string {
	headline := in.ScopeTitle
	if headline == "" {
		headline = in.ScopeKey
	}

	var features, fixes, others, breaking []string
	commitCount := 0

	for _, member := range in.Members {
		attribution := fmt.Sprintf("#%d", member.PR)
		if member.Issue > 0 {
			attribution += fmt.Sprintf(", GH-%d", member.Issue)
		}

		for _, commit := range member.Commits {
			commitCount++
			msg := commit.Commit.Message
			if idx := strings.Index(msg, "\n"); idx > 0 {
				msg = msg[:idx]
			}

			description := msg
			commitType := ""
			if matches := conventionalCommitRegex.FindStringSubmatch(msg); matches != nil {
				commitType = strings.ToLower(matches[1])
				description = matches[4]
			}
			entry := fmt.Sprintf("- %s (%s)", description, attribution)

			if parseBumpFromMessage(msg) == BumpMajor {
				breaking = append(breaking, entry)
			}

			switch commitType {
			case "feat", "feature":
				features = append(features, entry)
			case "fix", "bugfix":
				fixes = append(fixes, entry)
			default:
				others = append(others, entry)
			}
		}
	}

	sections := []string{"# " + headline}
	if len(features) > 0 {
		sections = append(sections, "## Features\n"+strings.Join(features, "\n"))
	}
	if len(fixes) > 0 {
		sections = append(sections, "## Bug Fixes\n"+strings.Join(fixes, "\n"))
	}
	if len(others) > 0 {
		sections = append(sections, "## Other Changes\n"+strings.Join(others, "\n"))
	}
	if len(breaking) > 0 {
		sections = append(sections, "## ⚠ Breaking Changes\n"+strings.Join(breaking, "\n"))
	}

	footer := fmt.Sprintf("**Full Changelog**: https://github.com/%s/%s/compare/%s...%s\n\n_%d PRs, %d commits_",
		in.Owner, in.Repo, in.LastTag, in.NewTag, len(in.Members), commitCount)
	sections = append(sections, footer)

	return strings.Join(sections, "\n\n")
}
