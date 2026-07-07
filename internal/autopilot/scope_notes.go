package autopilot

import (
	"fmt"
	"strings"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// ScopeMember is one merged PR contributing to a scope release, carrying its
// own commits (fetched per-PR rather than folded into the deduped union used
// for bump detection) so every changelog entry can be attributed to its exact
// PR and linked issue (GH-3992).
type ScopeMember struct {
	PR      int
	Issue   int
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

// BuildScopeReleaseNotes renders the aggregated release notes for a scope
// carrier: a headline, grouped Features/Bug Fixes/Other Changes sections with
// exact per-PR/per-issue attribution, a Breaking Changes section, and a
// compare-link + stats footer. Locked format per TASK-389/GH-3992.
func BuildScopeReleaseNotes(in ScopeNotesInput) string {
	headline := in.ScopeTitle
	if headline == "" {
		headline = in.ScopeKey
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

			// Breaking-change detection reuses parseBumpFromMessage's
			// first-line-only predicate (releaser.go) so scope notes agree
			// with the bump that was actually cut — body "BREAKING CHANGE:"
			// footers are out of scope, matching DetectBumpType.
			isBreaking := parseBumpFromMessage(msg) == BumpMajor

			matches := conventionalCommitRegex.FindStringSubmatch(msg)
			if matches == nil {
				entry := formatScopeEntry(msg, member)
				others = append(others, entry)
				if isBreaking {
					breaking = append(breaking, entry)
				}
				continue
			}

			commitType := strings.ToLower(matches[1])
			description := matches[4]
			entry := formatScopeEntry(description, member)

			switch commitType {
			case "feat", "feature":
				features = append(features, entry)
			case "fix", "bugfix":
				fixes = append(fixes, entry)
			default:
				others = append(others, entry)
			}
			if isBreaking {
				breaking = append(breaking, entry)
			}
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n", headline))

	if len(features) > 0 {
		sb.WriteString("\n## Features\n" + strings.Join(features, "\n") + "\n")
	}
	if len(fixes) > 0 {
		sb.WriteString("\n## Bug Fixes\n" + strings.Join(fixes, "\n") + "\n")
	}
	if len(others) > 0 {
		sb.WriteString("\n## Other Changes\n" + strings.Join(others, "\n") + "\n")
	}
	if len(breaking) > 0 {
		sb.WriteString("\n## ⚠ Breaking Changes\n" + strings.Join(breaking, "\n") + "\n")
	}

	sb.WriteString(fmt.Sprintf("\n**Full Changelog**: https://github.com/%s/%s/compare/%s...%s\n",
		in.Owner, in.Repo, in.LastTag, in.NewTag))
	sb.WriteString(fmt.Sprintf("_%d PRs, %d commits_\n", len(in.Members), commitCount))

	return sb.String()
}

// formatScopeEntry renders one changelog line, omitting the GH-<issue> suffix
// when the member PR has no linked issue (its body carried no recognizable
// "Closes #N" reference).
func formatScopeEntry(description string, member ScopeMember) string {
	if member.Issue > 0 {
		return fmt.Sprintf("- %s (#%d, GH-%d)", description, member.PR, member.Issue)
	}
	return fmt.Sprintf("- %s (#%d)", description, member.PR)
}
