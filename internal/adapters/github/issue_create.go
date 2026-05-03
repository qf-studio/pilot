package github

import (
	"context"
	"fmt"
	"regexp"
)

// conventionalCommitRE matches conventional commit titles per the spec used by Pilot.
var conventionalCommitRE = regexp.MustCompile(`^(feat|fix|chore|refactor|test|docs|perf|build|ci|style)(\([^)]+\))?: .+$`)

// createPilotIssue validates the title against the conventional-commits format and
// creates a GitHub issue with the given labels. Returns an error if the title does
// not match or if the API call fails.
func createPilotIssue(ctx context.Context, c *Client, owner, repo, title, body string, labels []string) (*Issue, error) {
	if !conventionalCommitRE.MatchString(title) {
		return nil, fmt.Errorf("issue title %q does not match conventional-commits format (type(scope): description)", title)
	}
	return c.CreateIssue(ctx, owner, repo, &IssueInput{
		Title:  title,
		Body:   body,
		Labels: labels,
	})
}
