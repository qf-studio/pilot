package autopilot

import (
	"fmt"
	"strings"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// ScopeMember is one merged member PR contributing to a scope release: its
// own commits (fetched per-PR so each changelog entry attributes to the
// exact PR/issue it shipped in, rather than the deduped cross-member union
// used for bump detection) and the issue number it closed, when known.
type ScopeMember struct {
	PR      int
	Issue   int
	Commits []*github.Commit
}

// ScopeNotesInput is the input to BuildScopeReleaseNotes.
type ScopeNotesInput struct {
	Owner      string
	Repo       string
	ScopeTitle string
	Members    []ScopeMember
	LastTag    string
	NewTag     string
}

// BuildScopeReleaseNotes renders the aggregated release notes for a scope
// carrier: a headline, grouped Features/Bug Fixes/Other Changes sections
// (each entry attributed to its exact source PR and issue), a
// "## ⚠ Breaking Changes" section when any first-line commit message signals
// a breaking change (reusing parseBumpFromMessage's breaking predicate — a
// `!` suffix or BREAKING type prefix; body `BREAKING CHANGE:` footers are out
// of scope, matching bump detection), and a compare-link + stats footer
// (GH-3992).
func BuildScopeReleaseNotes(in ScopeNotesInput) string {
	headline := in.ScopeTitle
	if headline == "" {
		headline = "Release"
	}

	var features, fixes, others, breaking []string
	commitCount := 0

	for _, member := range in.Members {
		for _, commit := range member.Commits {
			commitCount++
			msg := commit.Commit.Message
			if idx := strings.Index(msg, "\n"); idx > 0 {
				msg = msg[:idx]
			}
			attribution := formatScopeAttribution(member.PR, member.Issue)

			matches := conventionalCommitRegex.FindStringSubmatch(msg)
			if matches == nil {
				others = append(others, fmt.Sprintf("- %s %s", msg, attribution))
				continue
			}

			commitType := strings.ToLower(matches[1])
			description := matches[4]
			line := fmt.Sprintf("- %s %s", description, attribution)

			if matches[3] == "!" || strings.HasPrefix(strings.ToUpper(commitType), "BREAKING") {
				breaking = append(breaking, line)
			}

			switch commitType {
			case "feat", "feature":
				features = append(features, line)
			case "fix", "bugfix":
				fixes = append(fixes, line)
			default:
				others = append(others, line)
			}
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n", headline))

	if len(features) > 0 {
		sb.WriteString("\n## Features\n")
		sb.WriteString(strings.Join(features, "\n"))
		sb.WriteString("\n")
	}
	if len(fixes) > 0 {
		sb.WriteString("\n## Bug Fixes\n")
		sb.WriteString(strings.Join(fixes, "\n"))
		sb.WriteString("\n")
	}
	if len(others) > 0 {
		sb.WriteString("\n## Other Changes\n")
		sb.WriteString(strings.Join(others, "\n"))
		sb.WriteString("\n")
	}
	if len(breaking) > 0 {
		sb.WriteString("\n## ⚠ Breaking Changes\n")
		sb.WriteString(strings.Join(breaking, "\n"))
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("\n**Full Changelog**: https://github.com/%s/%s/compare/%s...%s\n",
		in.Owner, in.Repo, in.LastTag, in.NewTag))
	sb.WriteString(fmt.Sprintf("\n_%d PRs, %d commits_\n", len(in.Members), commitCount))

	return sb.String()
}

// formatScopeAttribution renders a changelog entry's source suffix: the PR
// always, plus the linked issue when known (a member PR whose issue link
// couldn't be resolved — e.g. a fetch failure — still attributes to its PR).
func formatScopeAttribution(pr, issue int) string {
	if issue > 0 {
		return fmt.Sprintf("(#%d, GH-%d)", pr, issue)
	}
	return fmt.Sprintf("(#%d)", pr)
}
