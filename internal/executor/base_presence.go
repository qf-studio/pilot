// Package executor — GH-5045
//
// Dispatch-time "base presence" guard: before a queued task's claim commits
// to executing, check whether the issue body's own stated prerequisites
// (an explicit "Depends on: #N" reference, or a backtick-quoted file path)
// have actually landed on the target repo's default branch yet. A sub-issue
// authored against a still-open sibling PR, or against a path a prior step
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
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// RefState is the live GitHub state of one dependency-ref number, just
// enough for basePresenceChecker to decide whether it still blocks a
// dependent task.
type RefState struct {
	// Found is false when number resolved to neither a PR nor an issue
	// (deleted, cross-repo, or a lookup error the caller chose to
	// swallow). A not-found ref never blocks — there's nothing to wait on.
	Found bool
	// IsPR is true when number resolved to a pull request rather than a
	// plain issue. Only an open PR counts as an unmet prerequisite; an
	// open issue reference doesn't imply anything about main's state.
	IsPR bool
	// Open is true when the resolved PR/issue has not yet been
	// merged/closed.
	Open bool
}

// BasePresenceProbe is the narrow read surface basePresenceChecker needs:
// one lookup for a referenced issue/PR's live state, one existence check
// for a referenced file path on the repo's default branch. Kept as an
// interface (rather than calling gh/adapters directly) so tests inject
// fakes — mirrors IssueStateChecker (issue_state.go) and
// ContractContentFetcher (contract_evidence.go).
type BasePresenceProbe interface {
	// IssueOrPRState fetches the live state of issue-or-PR `number` in
	// owner/repo. Implementations must make at most one GitHub API call.
	IssueOrPRState(ctx context.Context, owner, repo string, number int) (RefState, error)
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
// prerequisite. Fails open per-lookup: a probe error is logged by the
// caller and treated as "not a hold" for that one ref/path, consistent
// with every other GH-4656-era pickup-time guard's "pipeline availability
// outranks the guard" stance (issue_state.go).
type basePresenceChecker struct {
	probe BasePresenceProbe
}

// Check returns the first unmet prerequisite found among refs (checked
// before paths, in caller-supplied order) — an open-PR ref, or a path
// missing from the default branch. Returns a zero-value (not held) result
// when nothing blocks, including when probe is nil (no adapter wired for
// this repo) or every lookup errors.
func (c basePresenceChecker) Check(ctx context.Context, owner, repo string, refs []int, paths []string) BasePresenceHold {
	if c.probe == nil {
		return BasePresenceHold{}
	}

	for _, n := range refs {
		state, err := c.probe.IssueOrPRState(ctx, owner, repo, n)
		if err != nil || !state.Found {
			continue
		}
		if state.IsPR && state.Open {
			return BasePresenceHold{
				Held:   true,
				Reason: fmt.Sprintf("referenced PR #%d is still open (not merged)", n),
			}
		}
	}

	for _, p := range paths {
		exists, err := c.probe.FileExistsOnDefaultBranch(ctx, owner, repo, p)
		if err != nil {
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

// ghCLIBasePresenceProbe is the default fallback BasePresenceProbe: shells
// out to `gh`, the same idiom used by ghCLIIssueStateChecker (issue_state.go)
// for repos with no registered SDK-backed probe.
type ghCLIBasePresenceProbe struct{}

// IssueOrPRState implements BasePresenceProbe by trying `gh pr view` first
// (a dependency ref is far more often a PR than a bare issue for a
// "prerequisite not landed" check), falling back to `gh issue view` when
// the number isn't a PR. Never shells out during `go test`.
func (ghCLIBasePresenceProbe) IssueOrPRState(ctx context.Context, owner, repo string, number int) (RefState, error) {
	if testing.Testing() {
		return RefState{}, fmt.Errorf("ghCLIBasePresenceProbe: shellout disabled under go test")
	}

	if state, ok, err := ghViewState(ctx, "pr", owner, repo, number); ok {
		return RefState{Found: true, IsPR: true, Open: state}, nil
	} else if err != nil {
		// Fall through to the issue lookup — a "pr view" failure just as
		// often means "this number is an issue, not a PR" as it does a
		// real error; the issue lookup below is the tiebreaker.
		_ = err
	}

	state, ok, err := ghViewState(ctx, "issue", owner, repo, number)
	if err != nil {
		return RefState{}, fmt.Errorf("gh pr/issue view %d in %s/%s: %w", number, owner, repo, err)
	}
	if !ok {
		return RefState{}, nil
	}
	return RefState{Found: true, IsPR: false, Open: state}, nil
}

// ghViewState runs `gh <kind> view <number> --repo owner/repo --json
// state` and reports the parsed open/closed-or-merged state. ok is false
// when the command failed outright (number doesn't resolve as this kind);
// err is only non-nil for a genuine transport/parse failure, distinct from
// "not this kind" (which returns ok=false, err=nil).
func ghViewState(ctx context.Context, kind, owner, repo string, number int) (open bool, ok bool, err error) {
	args := []string{kind, "view", strconv.Itoa(number), "--repo", owner + "/" + repo, "--json", "state"}
	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", args...))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if runErr := cmd.Run(); runErr != nil {
		return false, false, nil
	}

	var resp struct {
		State string `json:"state"`
	}
	if jsonErr := json.Unmarshal(stdout.Bytes(), &resp); jsonErr != nil {
		return false, false, fmt.Errorf("parse gh %s view output: %w", kind, jsonErr)
	}
	return strings.EqualFold(strings.TrimSpace(resp.State), "OPEN"), true, nil
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
// per-ref/per-path probe error is swallowed internally by
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
	if runner != nil {
		probe = runner.basePresenceProbeFor("github:" + owner + "/" + repo)
	}
	if probe == nil {
		probe = ghCLIBasePresenceProbe{}
	}

	return basePresenceChecker{probe: probe}.Check(ctx, owner, repo, refs, paths), nil
}
