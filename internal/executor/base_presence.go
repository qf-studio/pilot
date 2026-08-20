// Package executor — GH-5045/GH-5052
//
// Dispatch-time "base presence" guard: before a queued task's claim commits
// to executing, check whether the issue body's own stated prerequisites
// (an explicit "Depends on: #N" reference, or a backtick-quoted file path)
// have actually landed on the target repo's default branch yet. A sub-issue
// authored against a still-open sibling PR — directly, or indirectly via an
// issue whose attached PR hasn't merged yet — or against a path a prior step
// hasn't merged yet, has nothing to build on — executing it anyway wastes a
// run reproducing work that's already in flight or racing a base that
// doesn't exist.
//
// Mirrors issue_state.go's IssueStateChecker/fetchIssueState shape: a
// narrow probe interface (BasePresenceProbe), a gh-CLI fallback
// implementation, per-repo registration on Runner
// (RegisterBasePresenceProbe/basePresenceProbeFor), and a swappable
// package-level var (checkBasePresence) so dispatcher.go's claim-path check
// and tests both call the exact same entry point.
//
// The two REQUIRED probe methods — IssueOrPRState and
// FileExistsOnDefaultBranch — match internal/adapters/github/repo_state.go's
// signatures exactly (including its merged-vs-closed distinction: a merged
// PR reports state "merged", not "closed") so a *github.Client can satisfy
// BasePresenceProbe directly once a composition-root registers one (a
// separate subtask — this package only needs the interface shape to match).
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// resolveOwnerRepoForBasePresence resolves (owner, repo) for the claim-path
// hold check, mirroring fetchIssueState's resolution order (issue_state.go):
// task.SourceRepo when populated, else the `origin` remote at projectPath.
// buildTaskFromExecution never populates SourceRepo, so the dispatcher
// claim-path call site always falls through to the git-remote path.
func resolveOwnerRepoForBasePresence(ctx context.Context, task *Task, projectPath string) (string, string, error) {
	owner, repo := "", ""
	if task.SourceRepo != "" {
		owner, repo, _ = strings.Cut(task.SourceRepo, "/")
	}
	if owner != "" && repo != "" {
		return owner, repo, nil
	}
	return resolveGitRemote(ctx, projectPath)
}

// labelPilotNeedsHuman is the escalation label applied once a held task has
// exhausted DispatcherConfig.BasePresenceHoldMaxCycles held cycles without
// its prerequisites landing — mirrors labelPilotSuperseded's
// cycle-avoidance pattern (issue_state.go): defined once here rather than
// imported from adapters/github to avoid the same import-cycle constraint
// documented there.
const labelPilotNeedsHuman = "pilot-needs-human"

// BasePresenceProbe is the narrow read surface basePresenceChecker needs:
// one lookup for a referenced issue/PR's live state, one lookup for the PR
// number(s) attached to a referenced issue (the "Depends on: #N" where #N is
// an issue rather than a PR shape), and one existence check for a
// referenced file path on the repo's default branch. Kept as an interface
// (rather than calling gh/adapters directly) so tests inject fakes —
// mirrors IssueStateChecker (issue_state.go) and ContractContentFetcher
// (contract_evidence.go).
//
// IssueOrPRState and FileExistsOnDefaultBranch intentionally match
// internal/adapters/github.Client's methods of the same name byte-for-byte
// (repo_state.go) so that Client already satisfies everything but
// LinkedPRNumbers — a composition-root adapter only needs to add the one
// new method, not reimplement the other two.
type BasePresenceProbe interface {
	// IssueOrPRState fetches the live state of issue-or-PR `number` in
	// owner/repo. kind is "issue" or "pr". state mirrors GitHub's PR
	// lifecycle: "open", "closed", or "merged" — a merged PR reports state
	// "merged", never "closed" (see repo_state.go's Client.IssueOrPRState
	// doc comment for why that distinction requires an extra round trip).
	// For kind "issue", state is the issue's own "open"/"closed".
	// Implementations must make at most one GitHub API call for a plain
	// issue, two for a PR.
	IssueOrPRState(ctx context.Context, owner, repo string, number int) (kind, state string, err error)
	// LinkedPRNumbers returns the numbers of every pull request that
	// references issue `issueNumber` in owner/repo (e.g. via a "Fixes #N" /
	// "Depends on: #N" body reference) — the canonical incident shape
	// (GH-5021/GH-5028): a sub-issue's "Depends on: #N" names an issue, not
	// a PR, and the actual unmerged work lives on the PR attached to that
	// issue. Order is unspecified; callers probe every returned number via
	// IssueOrPRState. An issue with no attached PR returns (nil, nil), not
	// an error.
	LinkedPRNumbers(ctx context.Context, owner, repo string, issueNumber int) ([]int, error)
	// FileExistsOnDefaultBranch reports whether path exists at HEAD of
	// owner/repo's default branch.
	FileExistsOnDefaultBranch(ctx context.Context, owner, repo, path string) (bool, error)
}

// BasePresenceHold is the result of basePresenceChecker.Check: whether the
// task should be held back from claiming, and if so, the human-readable
// reason (used in the log line and execution-event detail).
type BasePresenceHold struct {
	Held   bool
	Reason string
}

// basePresenceChecker runs BasePresenceProbe lookups over a task's
// extracted refs/paths and decides whether any of them is an unmet
// prerequisite. Fails open per-lookup: a probe error is logged (via log,
// falling back to slog.Default() when unset) and treated as "not a hold"
// for that one ref/path, consistent with every other GH-4656-era
// pickup-time guard's "pipeline availability outranks the guard" stance
// (issue_state.go).
type basePresenceChecker struct {
	probe BasePresenceProbe
	log   *slog.Logger
}

func (c basePresenceChecker) logger() *slog.Logger {
	if c.log != nil {
		return c.log
	}
	return slog.Default()
}

// logProbeError logs a fail-open probe error (GH-5052 gap: the original
// GH-5045 draft swallowed per-lookup probe errors silently, with no way for
// an operator to notice a probe was mis-registered or a repo was
// unreachable other than the task never claiming). ref identifies what was
// being looked up (an issue/PR number or a file path) purely for the log
// line.
func (c basePresenceChecker) logProbeError(op, ref string, err error) {
	c.logger().Warn("base-presence probe error; failing open for this ref/path",
		slog.String("op", op),
		slog.String("ref", ref),
		slog.Any("error", err),
	)
}

// Check returns the first unmet prerequisite found among refs (checked
// before paths, in caller-supplied order) — an open-PR ref (directly, or
// via an issue whose attached PR is still open-unmerged), or a path missing
// from the default branch. Returns a zero-value (not held) result when
// nothing blocks, including when probe is nil (no adapter wired for this
// repo) or every lookup errors (fail-open).
func (c basePresenceChecker) Check(ctx context.Context, owner, repo string, refs []int, paths []string) BasePresenceHold {
	if c.probe == nil {
		return BasePresenceHold{}
	}

	for _, n := range refs {
		if hold, held := c.checkRef(ctx, owner, repo, n); held {
			return hold
		}
	}

	for _, p := range paths {
		exists, err := c.probe.FileExistsOnDefaultBranch(ctx, owner, repo, p)
		if err != nil {
			c.logProbeError("FileExistsOnDefaultBranch", p, err)
			continue
		}
		if !exists {
			return BasePresenceHold{
				Held:   true,
				Reason: fmt.Sprintf("referenced path %q not found on default branch", p),
			}
		}
	}

	return BasePresenceHold{}
}

// checkRef resolves one dependency-ref number and decides whether it's an
// unmet prerequisite, handling both ref shapes named in GH-5052:
//
//   - (a) #N is itself an open PR — blocks directly.
//   - (b) #N is an issue whose attached PR (found via LinkedPRNumbers) is
//     open-unmerged — the canonical incident shape (GH-5021, ui#120/124/139
//     lineage): a sub-issue's "Depends on: #N" almost always names the
//     tracking issue, not the PR that actually carries the unmerged work.
//
// A merged or closed PR (found directly or via the issue's linked-PR list)
// never blocks; neither does a plain issue with no attached PR at all.
func (c basePresenceChecker) checkRef(ctx context.Context, owner, repo string, n int) (BasePresenceHold, bool) {
	kind, state, err := c.probe.IssueOrPRState(ctx, owner, repo, n)
	if err != nil {
		c.logProbeError("IssueOrPRState", strconv.Itoa(n), err)
		return BasePresenceHold{}, false
	}

	switch kind {
	case "pr":
		if state == "open" {
			return BasePresenceHold{
				Held:   true,
				Reason: fmt.Sprintf("referenced PR #%d is still open (not merged)", n),
			}, true
		}
		return BasePresenceHold{}, false

	case "issue":
		prNumbers, lerr := c.probe.LinkedPRNumbers(ctx, owner, repo, n)
		if lerr != nil {
			c.logProbeError("LinkedPRNumbers", strconv.Itoa(n), lerr)
			return BasePresenceHold{}, false
		}
		for _, pn := range prNumbers {
			prKind, prState, perr := c.probe.IssueOrPRState(ctx, owner, repo, pn)
			if perr != nil {
				c.logProbeError("IssueOrPRState", strconv.Itoa(pn), perr)
				continue
			}
			if prKind == "pr" && prState == "open" {
				return BasePresenceHold{
					Held:   true,
					Reason: fmt.Sprintf("referenced issue #%d's attached PR #%d is still open (not merged)", n, pn),
				}, true
			}
		}
		return BasePresenceHold{}, false

	default:
		return BasePresenceHold{}, false
	}
}

// ghCLIBasePresenceProbe is the default fallback BasePresenceProbe: shells
// out to `gh`, the same idiom used by ghCLIIssueStateChecker (issue_state.go)
// for repos with no registered SDK-backed probe.
type ghCLIBasePresenceProbe struct{}

// ghCLIViewState runs `gh <kind> view <number> --repo owner/repo --json
// state` and reports the parsed state, lower-cased to match
// BasePresenceProbe's "open"/"closed"/"merged" convention. found is false
// when the command failed outright (most commonly: number doesn't resolve
// as this kind — `gh` reports "no <kind>s found" with the same exit code a
// genuine transport error would use, so the two aren't distinguishable from
// the exit code alone); err is only non-nil for a genuine parse failure of
// output the command DID produce.
func ghCLIViewState(ctx context.Context, kind, owner, repo string, number int) (state string, found bool, err error) {
	args := []string{kind, "view", strconv.Itoa(number), "--repo", owner + "/" + repo, "--json", "state"}
	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", args...))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if runErr := cmd.Run(); runErr != nil {
		return "", false, nil
	}

	var resp struct {
		State string `json:"state"`
	}
	if jsonErr := json.Unmarshal(stdout.Bytes(), &resp); jsonErr != nil {
		return "", false, fmt.Errorf("parse gh %s view output: %w", kind, jsonErr)
	}
	return strings.ToLower(strings.TrimSpace(resp.State)), true, nil
}

// IssueOrPRState implements BasePresenceProbe by trying `gh pr view` first
// (a dependency ref is far more often a PR than a bare issue for a
// "prerequisite not landed" check), falling back to `gh issue view` when
// the number isn't a PR. Never shells out during `go test`.
func (ghCLIBasePresenceProbe) IssueOrPRState(ctx context.Context, owner, repo string, number int) (kind, state string, err error) {
	if testing.Testing() {
		return "", "", fmt.Errorf("ghCLIBasePresenceProbe: shellout disabled under go test")
	}

	if s, ok, verr := ghCLIViewState(ctx, "pr", owner, repo, number); verr != nil {
		return "", "", fmt.Errorf("gh pr view %d in %s/%s: %w", number, owner, repo, verr)
	} else if ok {
		return "pr", s, nil
	}

	s, ok, verr := ghCLIViewState(ctx, "issue", owner, repo, number)
	if verr != nil {
		return "", "", fmt.Errorf("gh issue view %d in %s/%s: %w", number, owner, repo, verr)
	}
	if !ok {
		return "", "", fmt.Errorf("number %d not found as PR or issue in %s/%s", number, owner, repo)
	}
	return "issue", s, nil
}

// LinkedPRNumbers implements BasePresenceProbe via the GitHub Search API
// (`gh api search/issues`, the same REST surface
// adapters/github.Client.SearchPRsForIssue queries), searching for open PRs
// that reference issueNumber anywhere in their title/body/comments. Never
// shells out during `go test`.
func (ghCLIBasePresenceProbe) LinkedPRNumbers(ctx context.Context, owner, repo string, issueNumber int) ([]int, error) {
	if testing.Testing() {
		return nil, fmt.Errorf("ghCLIBasePresenceProbe: shellout disabled under go test")
	}

	query := fmt.Sprintf("repo:%s/%s is:pr #%d", owner, repo, issueNumber)
	args := []string{"api", "search/issues", "-f", "q=" + query, "--jq", ".items[].number"}
	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", args...))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh api search/issues (linked PRs for #%d in %s/%s): %w (stderr: %s)", issueNumber, owner, repo, err, stderr.String())
	}

	var numbers []int
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		n, perr := strconv.Atoi(line)
		if perr != nil {
			continue
		}
		numbers = append(numbers, n)
	}
	return numbers, nil
}

// FileExistsOnDefaultBranch implements BasePresenceProbe via `gh api
// repos/<owner>/<repo>/contents/<path>` (no ref query param, so this reads
// the repo's default branch). A 404 means the path is genuinely absent
// (returns false, nil error); any other failure is a real lookup error.
func (ghCLIBasePresenceProbe) FileExistsOnDefaultBranch(ctx context.Context, owner, repo, path string) (bool, error) {
	if testing.Testing() {
		return false, fmt.Errorf("ghCLIBasePresenceProbe: shellout disabled under go test")
	}

	args := []string{"api", fmt.Sprintf("repos/%s/%s/contents/%s", owner, repo, path)}
	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", args...))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "404") {
			return false, nil
		}
		return false, fmt.Errorf("gh api repos/%s/%s/contents/%s: %w (stderr: %s)", owner, repo, path, err, stderr.String())
	}
	return true, nil
}

// RegisterBasePresenceProbe registers a BasePresenceProbe under an explicit
// key ("adapter:owner/repo"), mirroring RegisterIssueStateChecker
// (issue_state.go). Registrations happen once at startup, so concurrent
// tasks from different repos can never observe another repo's probe.
func (r *Runner) RegisterBasePresenceProbe(key string, probe BasePresenceProbe) {
	r.basePresenceProbesMu.Lock()
	defer r.basePresenceProbesMu.Unlock()
	if r.basePresenceProbes == nil {
		r.basePresenceProbes = make(map[string]BasePresenceProbe)
	}
	r.basePresenceProbes[key] = probe
}

// basePresenceProbeFor returns the registered probe for key, or nil.
func (r *Runner) basePresenceProbeFor(key string) BasePresenceProbe {
	r.basePresenceProbesMu.RLock()
	defer r.basePresenceProbesMu.RUnlock()
	return r.basePresenceProbes[key]
}

// checkBasePresence resolves (owner, repo) for task/projectPath (mirroring
// fetchIssueState's resolution order, issue_state.go), then resolves the
// BasePresenceProbe registered for that repo (falling back to the gh-CLI
// probe when none is registered) and runs basePresenceChecker.Check. A
// package-level swappable var — mirroring fetchIssueState and
// mergedPRPreflightCheck (dispatcher.go) — so dispatcher.go's claim-path
// check and tests both call the exact same entry point instead of tests
// wiring a real git remote plus a registered probe per case.
//
// Returns a non-nil error only when (owner, repo) could not be resolved at
// all — the caller (ProjectWorker.processQueue) logs that as a fail-open
// warning and proceeds, exactly like the GH-4656 fetchIssueState guard. A
// per-ref/per-path probe error is logged and swallowed internally by
// basePresenceChecker.Check (fail-open there too), since no single
// "the check failed" signal is worth surfacing for that case.
var checkBasePresence = func(ctx context.Context, runner *Runner, task *Task, projectPath string, refs []int, paths []string) (BasePresenceHold, error) {
	if len(refs) == 0 && len(paths) == 0 {
		return BasePresenceHold{}, nil
	}

	owner, repo, err := resolveOwnerRepoForBasePresence(ctx, task, projectPath)
	if err != nil {
		return BasePresenceHold{}, err
	}

	var probe BasePresenceProbe
	var log *slog.Logger
	if runner != nil {
		probe = runner.basePresenceProbeFor("github:" + owner + "/" + repo)
		log = runner.log
	}
	if probe == nil {
		probe = ghCLIBasePresenceProbe{}
	}

	return basePresenceChecker{probe: probe, log: log}.Check(ctx, owner, repo, refs, paths), nil
}
