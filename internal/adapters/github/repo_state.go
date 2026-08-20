package github

import "context"

// FileExistsOnDefaultBranch reports whether path exists in owner/repo on the
// repository's default branch. It wraps GetFileContent with an empty ref
// (which GetFileContent already treats as "use the default branch") and
// collapses a 404 into (false, nil) rather than an error, since "the file
// isn't there" is an expected, non-exceptional outcome for this check.
// Any other failure (auth, rate limit, network) is still returned as an
// error so callers don't silently treat a broken request as "file missing".
func (c *Client) FileExistsOnDefaultBranch(ctx context.Context, owner, repo, path string) (bool, error) {
	_, err := c.GetFileContent(ctx, owner, repo, path, "")
	if err != nil {
		if isNotFoundError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// IssueOrPRState fetches #number via the Issues API and reports what it is.
// GitHub numbers issues and pull requests from the same sequence and the
// Issues API (GET /repos/{owner}/{repo}/issues/{number}) returns both; a
// non-nil pull_request field on the response is GitHub's own signal that
// the numbered item is a PR rather than a plain issue (see Issue.PullRequest).
//
// kind is "issue" or "pr". state mirrors GitHub's PR lifecycle: "open",
// "closed", or "merged" (a merged PR still reports state "closed" from the
// Issues/Pulls API, so callers wanting to distinguish merged-vs-abandoned
// need the extra GetPullRequest round trip this function makes to check
// Merged). For kind "issue", state is the issue's own "open"/"closed".
func (c *Client) IssueOrPRState(ctx context.Context, owner, repo string, number int) (kind, state string, err error) {
	issue, err := c.GetIssue(ctx, owner, repo, number)
	if err != nil {
		return "", "", err
	}

	if issue.PullRequest == nil {
		return "issue", issue.State, nil
	}

	// #number is a PR. Re-fetch via the Pulls API to get the Merged flag —
	// the Issues API alone can't distinguish a merged PR from one closed
	// without merging (both report state "closed").
	pr, err := c.GetPullRequest(ctx, owner, repo, number)
	if err != nil {
		return "", "", err
	}
	if pr.Merged {
		return "pr", "merged", nil
	}
	return "pr", pr.State, nil
}
