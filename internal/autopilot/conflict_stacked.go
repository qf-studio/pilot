package autopilot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
//
// GH-5538: detection is two-stage. Stage 1 nominates candidates — exactly, via
// the trailing "(GH-N)" the executor appends to every commit subject combined
// with a merged PR for issue N (mergedIssueFunc), and secondarily via
// normalized subject / Closes-trailer matching. Stage 2 is a content guard:
// a candidate is only dropped when its change is provably already on the base,
// so a subject collision can never delete an own commit from the branch.

// stackedMainScanLimit bounds how many recent base-branch commits are scanned
// for squash-merged predecessors. A stacked predecessor merges within the
// lifetime of an epic, so a few hundred commits is ample.
const stackedMainScanLimit = 500

var (
	// squashSuffixRe strips the " (#123)" suffix GitHub appends to squash-merge subjects.
	squashSuffixRe = regexp.MustCompile(`\s*\(#\d+\)\s*$`)
	// closesRefRe matches "Closes #N" / "Fixes #N" / "Resolves #N" trailers.
	closesRefRe = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+#(\d+)`)
	// ghSuffixRe matches the trailing " (GH-N)" the executor appends to commit subjects.
	ghSuffixRe = regexp.MustCompile(`\(GH-(\d+)\)\s*$`)
)

// mergedIssueFunc reports whether issue N has a merged PR on the base branch.
type mergedIssueFunc func(ctx context.Context, issue int) (bool, error)

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

// subjectIssueRef returns N from a trailing "(GH-N)" on the commit subject, or 0.
func subjectIssueRef(subject string) int {
	m := ghSuffixRe.FindStringSubmatch(strings.TrimSpace(subject))
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// stackedIssueRefs returns the distinct non-merge-commit "(GH-N)" issue numbers
// on the branch other than ownIssue — the issues whose merge status must be
// looked up to nominate inherited commits exactly.
func stackedIssueRefs(branch []stackedCommit, ownIssue int) []int {
	seen := map[int]bool{}
	var refs []int
	for _, c := range branch {
		if c.Parents > 1 {
			continue
		}
		if n := subjectIssueRef(c.Subject); n > 0 && n != ownIssue && !seen[n] {
			seen[n] = true
			refs = append(refs, n)
		}
	}
	return refs
}

// stackedCandidates nominates branch commits (oldest first) that look already
// merged to the base. The primary signal is exact: the commit's trailing
// "(GH-N)" names an issue (not ownIssue) with a merged PR (mergedIssues).
// Normalized-subject and Closes/Fixes/Resolves #N trailer matches are
// secondary signals. Merge commits are never candidates. Candidates are only
// nominations — the caller must still pass each through the content guard.
func stackedCandidates(branch []stackedCommit, mainSubjects map[string]bool, mainCloses map[int]bool, mergedIssues map[int]bool, ownIssue int) map[string]bool {
	cands := map[string]bool{}
	for _, c := range branch {
		if c.Parents > 1 {
			continue
		}
		isCandidate := false
		if n := subjectIssueRef(c.Subject); n > 0 && n != ownIssue && mergedIssues[n] {
			isCandidate = true
		}
		if !isCandidate && mainSubjects[normalizeSubject(c.Subject)] {
			isCandidate = true
		}
		if !isCandidate {
			for _, n := range closingRefs(c.Subject + "\n" + c.Body) {
				if n != ownIssue && mainCloses[n] {
					isCandidate = true
					break
				}
			}
		}
		if isCandidate {
			cands[c.SHA] = true
		}
	}
	return cands
}

// gitPatchIDs returns the set of stable patch-ids for the commits selected by
// the given `git log` arguments (merge commits excluded).
func gitPatchIDs(ctx context.Context, dir string, logArgs ...string) (map[string]bool, error) {
	args := append([]string{"log", "-p", "--no-merges", "--format=%H"}, logArgs...)
	patch, err := gitOutput(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", "patch-id", "--stable")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(patch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git patch-id: %w: %s", err, strings.TrimSpace(string(out)))
	}
	ids := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			ids[f[0]] = true
		}
	}
	return ids, nil
}

// stackedContentGuard decides whether a nominated commit's change is already
// on the base. It never relies on commit messages.
type stackedContentGuard struct {
	ctx          context.Context
	repoPath     string
	worktreePath string // scratch worktree checked out at baseRef
	baseRef      string
	mergeBase    string
	mainPatchIDs map[string]bool // lazily computed
}

// alreadyOnBase reports true when any of the following holds for c:
//   - the base's content at every path the branch touched up to c (plus c's own
//     paths) equals c's tree — covers multi-commit predecessors squashed into
//     one main commit, where per-commit cherry-picks would conflict;
//   - c's patch-id matches a commit already on the base;
//   - cherry-picking c onto the base yields an empty change.
func (g *stackedContentGuard) alreadyOnBase(c stackedCommit) (bool, error) {
	same, err := g.treeMatchesBase(c)
	if err != nil {
		return false, err
	}
	if same {
		return true, nil
	}

	if g.mainPatchIDs == nil {
		ids, err := gitPatchIDs(g.ctx, g.repoPath, "-n", strconv.Itoa(stackedMainScanLimit), g.baseRef)
		if err != nil {
			return false, err
		}
		g.mainPatchIDs = ids
	}
	own, err := gitPatchIDs(g.ctx, g.repoPath, "-n", "1", c.SHA)
	if err != nil {
		return false, err
	}
	for id := range own {
		if g.mainPatchIDs[id] {
			return true, nil
		}
	}

	return g.cherryPickEmpty(c), nil
}

func (g *stackedContentGuard) treeMatchesBase(c stackedCommit) (bool, error) {
	paths := map[string]bool{}
	for _, rng := range [][2]string{{g.mergeBase, c.SHA}, {c.SHA + "^", c.SHA}} {
		out, err := gitOutput(g.ctx, g.repoPath, "diff", "--no-renames", "--name-only", "-z", rng[0], rng[1])
		if err != nil {
			return false, err
		}
		for _, p := range strings.Split(out, "\x00") {
			if p != "" {
				paths[p] = true
			}
		}
	}
	if len(paths) == 0 {
		return false, nil
	}
	args := []string{"--literal-pathspecs", "diff", "--no-renames", "--name-only", g.baseRef, c.SHA, "--"}
	for p := range paths {
		args = append(args, p)
	}
	out, err := gitOutput(g.ctx, g.repoPath, args...)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// cherryPickEmpty applies c onto the base without committing and reports
// whether the result is an empty change. Conflicts and errors count as
// "not empty" (the safe direction: the commit is kept).
func (g *stackedContentGuard) cherryPickEmpty(c stackedCommit) bool {
	defer func() {
		_ = runGitCmd(g.ctx, g.worktreePath, "reset", "--hard", "HEAD")
		_ = runGitCmd(g.ctx, g.worktreePath, "cherry-pick", "--quit")
	}()
	if err := runGitCmd(g.ctx, g.worktreePath, "cherry-pick", "--no-commit", c.SHA); err != nil {
		return false
	}
	out, err := gitOutput(g.ctx, g.worktreePath, "diff", "--cached", "--name-only")
	return err == nil && strings.TrimSpace(out) == ""
}

const stackedLogFormat = "--format=%H" + gitFieldSep + "%P" + gitFieldSep + "%s" + gitFieldSep + "%b" + gitRecordSep

// rebaseStackedBranch fetches branchName and baseBranch, and — only when the
// branch carries commits already merged to the base — replays the branch's own
// commits onto origin/baseBranch in a scratch worktree and force-pushes the
// result to branchName (with a lease on the SHA that was analysed).
//
// merged reports whether an issue has a merged PR (exact "(GH-N)" detection).
//
// Returns (nil, nil) when the branch is not stacked (no inherited commits, or
// nothing of its own left) so the caller continues down the normal ladder. A
// non-nil error means replay conflicted or a git step failed; the remote
// branch is untouched in that case.
func rebaseStackedBranch(ctx context.Context, repoPath, branchName, baseBranch string, ownIssue int, merged mergedIssueFunc) (*stackedRebaseResult, error) {
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

	mergedIssues := map[int]bool{}
	if merged != nil {
		for _, n := range stackedIssueRefs(branchCommits, ownIssue) {
			ok, err := merged(ctx, n)
			if err != nil {
				return nil, fmt.Errorf("check merged PR for issue #%d: %w", n, err)
			}
			mergedIssues[n] = ok
		}
	}

	candidates := stackedCandidates(branchCommits, mainSubjects, mainCloses, mergedIssues, ownIssue)
	if len(candidates) == 0 {
		return nil, nil
	}

	mergeBase, err := gitOutput(ctx, repoPath, "merge-base", baseRef, branchRef)
	if err != nil {
		return nil, err
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

	// Content guard: a nominated commit is dropped only when its change is
	// provably already on the base — never on subject/trailer evidence alone.
	guard := &stackedContentGuard{
		ctx:          ctx,
		repoPath:     repoPath,
		worktreePath: worktreePath,
		baseRef:      baseRef,
		mergeBase:    strings.TrimSpace(mergeBase),
	}
	inherited := 0
	var own []stackedCommit
	for _, c := range branchCommits {
		if c.Parents > 1 {
			continue
		}
		if candidates[c.SHA] {
			onBase, err := guard.alreadyOnBase(c)
			if err != nil {
				return nil, fmt.Errorf("content guard for %s: %w", c.SHA, err)
			}
			if onBase {
				inherited++
				continue
			}
		}
		own = append(own, c)
	}
	if inherited == 0 || len(own) == 0 {
		return nil, nil
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
