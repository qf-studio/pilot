package autopilot

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// GH-5527: epic sub-issues run sequentially and each sub-issue branch is cut
// from its predecessor's branch, so a successor carries the predecessor's
// commits. Once main squash-merges the predecessor's PR, a plain merge/rebase
// of the successor replays those inherited commits against files main already
// has and reports conflicts that are not real. rebaseStackedBranch detects the
// inherited commits and replays only the branch's own commits onto the base —
// the programmatic equivalent of
// `git rebase --onto origin/<base> <last-inherited-commit> HEAD`.

// stackedMainScanLimit bounds how many recent base-branch commits are scanned
// for squash-merged predecessors. A stacked predecessor merges within the
// lifetime of an epic, so a few hundred commits is ample.
const stackedMainScanLimit = 500

var (
	// squashSuffixRe strips the " (#123)" suffix GitHub appends to squash-merge subjects.
	squashSuffixRe = regexp.MustCompile(`\s*\(#\d+\)\s*$`)
	// closesRefRe matches "Closes #N" / "Fixes #N" / "Resolves #N" trailers.
	closesRefRe = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+#(\d+)`)
)

const (
	gitFieldSep  = "\x1f"
	gitRecordSep = "\x1e"
)

// stackedCommit is one commit from the origin/<base>..origin/<branch> range.
type stackedCommit struct {
	SHA     string
	Parents int
	Subject string
	Body    string
}

// stackedRebaseResult describes what rebaseStackedBranch did.
type stackedRebaseResult struct {
	// Inherited is the number of non-merge commits on the branch that were
	// identified as already merged to the base and therefore dropped.
	Inherited int
	// Replayed is the number of the branch's own commits replayed onto the base.
	Replayed int
}

// normalizeSubject strips the squash-merge "(#N)" suffix so a branch commit
// subject compares equal to the squash commit that landed it on main.
func normalizeSubject(s string) string {
	return strings.TrimSpace(squashSuffixRe.ReplaceAllString(strings.TrimSpace(s), ""))
}

func closingRefs(msg string) []int {
	var refs []int
	for _, m := range closesRefRe.FindAllStringSubmatch(msg, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			refs = append(refs, n)
		}
	}
	return refs
}

func parseStackedLog(out string) []stackedCommit {
	var commits []stackedCommit
	for _, rec := range strings.Split(out, gitRecordSep) {
		rec = strings.TrimLeft(rec, "\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.SplitN(rec, gitFieldSep, 4)
		if len(f) < 4 {
			continue
		}
		commits = append(commits, stackedCommit{
			SHA:     f[0],
			Parents: len(strings.Fields(f[1])),
			Subject: f[2],
			Body:    f[3],
		})
	}
	return commits
}

// classifyStackedCommits splits branch commits (oldest first) into inherited
// (already merged to the base, matched by normalized subject or by a
// Closes/Fixes/Resolves #N trailer) and own commits. Merge commits are never
// replayed: they are counted as neither. ownIssue's own closing trailer is
// ignored so a branch is never classified as inherited because of itself.
func classifyStackedCommits(branch []stackedCommit, mainSubjects map[string]bool, mainCloses map[int]bool, ownIssue int) (inherited int, own []stackedCommit) {
	for _, c := range branch {
		if c.Parents > 1 {
			continue
		}
		isInherited := mainSubjects[normalizeSubject(c.Subject)]
		if !isInherited {
			for _, n := range closingRefs(c.Subject + "\n" + c.Body) {
				if n != ownIssue && mainCloses[n] {
					isInherited = true
					break
				}
			}
		}
		if isInherited {
			inherited++
		} else {
			own = append(own, c)
		}
	}
	return inherited, own
}

const stackedLogFormat = "--format=%H" + gitFieldSep + "%P" + gitFieldSep + "%s" + gitFieldSep + "%b" + gitRecordSep

// rebaseStackedBranch fetches branchName and baseBranch, and — only when the
// branch carries commits already merged to the base — replays the branch's own
// commits onto origin/baseBranch in a scratch worktree and force-pushes the
// result to branchName (with a lease on the SHA that was analysed).
//
// Returns (nil, nil) when the branch is not stacked (no inherited commits, or
// nothing of its own left) so the caller continues down the normal ladder. A
// non-nil error means replay conflicted or a git step failed; the remote
// branch is untouched in that case.
func rebaseStackedBranch(ctx context.Context, repoPath, branchName, baseBranch string, ownIssue int) (*stackedRebaseResult, error) {
	if err := runGitCmd(ctx, repoPath, "fetch", "origin", branchName, baseBranch); err != nil {
		return nil, fmt.Errorf("fetch origin %s %s: %w", branchName, baseBranch, err)
	}

	branchRef := "origin/" + branchName
	baseRef := "origin/" + baseBranch

	headSHA, err := gitOutput(ctx, repoPath, "rev-parse", branchRef)
	if err != nil {
		return nil, err
	}
	headSHA = strings.TrimSpace(headSHA)

	branchOut, err := gitOutput(ctx, repoPath, "log", "--reverse", "--topo-order", stackedLogFormat, baseRef+".."+branchRef)
	if err != nil {
		return nil, err
	}
	branchCommits := parseStackedLog(branchOut)
	if len(branchCommits) == 0 {
		return nil, nil
	}

	mainOut, err := gitOutput(ctx, repoPath, "log", "-n", strconv.Itoa(stackedMainScanLimit), stackedLogFormat, baseRef)
	if err != nil {
		return nil, err
	}
	mainSubjects := map[string]bool{}
	mainCloses := map[int]bool{}
	for _, mc := range parseStackedLog(mainOut) {
		mainSubjects[normalizeSubject(mc.Subject)] = true
		for _, n := range closingRefs(mc.Subject + "\n" + mc.Body) {
			mainCloses[n] = true
		}
	}

	inherited, own := classifyStackedCommits(branchCommits, mainSubjects, mainCloses, ownIssue)
	if inherited == 0 || len(own) == 0 {
		return nil, nil
	}

	worktreePath, err := os.MkdirTemp("", "pilot-stacked-rebase-*")
	if err != nil {
		return nil, fmt.Errorf("create scratch worktree dir: %w", err)
	}
	if err := os.Remove(worktreePath); err != nil {
		return nil, fmt.Errorf("remove scratch worktree placeholder: %w", err)
	}
	defer func() {
		_ = runGitCmd(ctx, repoPath, "worktree", "remove", "--force", worktreePath)
		_ = os.RemoveAll(worktreePath)
	}()

	if err := runGitCmd(ctx, repoPath, "worktree", "add", "--detach", worktreePath, baseRef); err != nil {
		return nil, fmt.Errorf("worktree add for %s: %w", baseRef, err)
	}

	for _, c := range own {
		if err := runGitCmd(ctx, worktreePath, "cherry-pick", c.SHA); err != nil {
			_ = runGitCmd(ctx, worktreePath, "cherry-pick", "--abort")
			return nil, fmt.Errorf("replay own commit %s onto %s: %w", c.SHA, baseRef, err)
		}
	}

	lease := fmt.Sprintf("--force-with-lease=refs/heads/%s:%s", branchName, headSHA)
	if err := runGitCmd(ctx, worktreePath, "push", lease, "origin", "HEAD:refs/heads/"+branchName); err != nil {
		return nil, fmt.Errorf("push stacked rebase to %s: %w", branchName, err)
	}

	return &stackedRebaseResult{Inherited: inherited, Replayed: len(own)}, nil
}
