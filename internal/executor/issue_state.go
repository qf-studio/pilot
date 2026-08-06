// Package executor — GH-4656
//
// Live GitHub issue-state revalidation used by two boundaries: the
// dispatcher's pickup-time guard (dispatcher.go, before Execute()) and the
// runner's PR-creation preflight (runner.go, immediately before opening a
// PR). Both close the same class of incident (2026-07-31, GH-4648/GH-4649):
// a queued or in-flight run whose scope was already delivered by a
// sibling/parent run on a different branch has no way to notice its own
// issue was closed out from under it — every existing pickup-time re-check
// is task_id-local (this task's own ledger rows) and mergedPRPreflightCheck
// is keyed to this task's own branch. This file adds the one check neither
// covers: ask GitHub itself.
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

// labelPilotSuperseded mirrors github.LabelSuperseded's value
// (internal/adapters/github/types.go) without importing that package —
// adapters/github imports internal/comms, which imports internal/executor,
// so importing adapters/github here would be a cycle (same constraint
// documented on GithubSideEffectSearcher, sideeffect_audit.go).
const labelPilotSuperseded = "pilot-superseded"

// IssueState is the live GitHub issue state fetched by IssueStateChecker —
// just enough to decide whether a queued or in-flight execution's issue has
// been closed/superseded out from under it.
type IssueState struct {
	Closed bool
	Labels []string
}

// HasLabel reports whether name is present in Labels (case-insensitive).
func (s IssueState) HasLabel(name string) bool {
	for _, l := range s.Labels {
		if strings.EqualFold(l, name) {
			return true
		}
	}
	return false
}

// IssueStateChecker is the narrow read surface the pickup-time and
// PR-creation preflight guards need: one GET for one issue's live
// state+labels. Mirrors the GithubSideEffectSearcher/RepoAllowlist pattern
// (sideeffect_audit.go, repo_guardrail.go) — a small consumer-side
// interface, faked directly in tests. Two implementations exist:
//
//   - sdkshim.GitHubIssueStateChecker (internal/adapters/sdkshim): wraps the
//     studio-sdk client already registered per-repo for PR creation
//     (RegisterPRCreator) — in-process, so its traffic is visible to
//     ghbudget's shared-user-pool accounting (GH-4391). Registered per-repo
//     via RegisterIssueStateChecker at daemon startup.
//   - ghCLIIssueStateChecker (this file): gh-CLI fallback used when no SDK
//     checker is registered for a repo, mirroring queryParentDoneViaGitHub's
//     shellout idiom (epic.go) — keeps the guard functional for projects
//     that aren't SDK-managed.
type IssueStateChecker interface {
	// GetIssueState fetches the live state of issue `number` in owner/repo.
	// Implementations must make at most one GitHub API call.
	GetIssueState(ctx context.Context, owner, repo string, number int) (IssueState, error)
}

// ghCLIIssueStateChecker is the default fallback IssueStateChecker: shells
// out to `gh issue view`, the same idiom already used throughout this
// package (epic.go's queryParentDoneViaGitHub, sideeffect_audit.go) to read
// GitHub state without importing adapters/github.
type ghCLIIssueStateChecker struct{}

// GetIssueState implements IssueStateChecker via `gh issue view --repo
// owner/repo <number> --json state,labels`, one process invocation (one
// GitHub API call). Never shells out during `go test` — tests override
// fetchIssueState (or inject a fake IssueStateChecker via
// RegisterIssueStateChecker) instead of exercising this path.
func (ghCLIIssueStateChecker) GetIssueState(ctx context.Context, owner, repo string, number int) (IssueState, error) {
	if testing.Testing() {
		return IssueState{}, fmt.Errorf("ghCLIIssueStateChecker: shellout disabled under go test")
	}
	args := []string{
		"issue", "view", strconv.Itoa(number),
		"--repo", owner + "/" + repo,
		"--json", "state,labels",
	}
	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", args...))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return IssueState{}, fmt.Errorf("gh issue view: %w (stderr: %s)", err, stderr.String())
	}

	var resp struct {
		State  string `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return IssueState{}, fmt.Errorf("parse gh issue view output: %w", err)
	}

	labels := make([]string, 0, len(resp.Labels))
	for _, l := range resp.Labels {
		labels = append(labels, l.Name)
	}
	return IssueState{
		Closed: strings.EqualFold(strings.TrimSpace(resp.State), "closed"),
		Labels: labels,
	}, nil
}

// RegisterIssueStateChecker registers an IssueStateChecker under an explicit
// key ("adapter:owner/repo"), mirroring RegisterPRCreator (runner.go) —
// registrations happen once at startup (cmd/pilot's SDK poller wiring,
// alongside RegisterPRCreator for the same repo/client), so concurrent tasks
// from different repos can never observe another repo's checker.
func (r *Runner) RegisterIssueStateChecker(key string, checker IssueStateChecker) {
	r.issueStateCheckersMu.Lock()
	defer r.issueStateCheckersMu.Unlock()
	if r.issueStateCheckers == nil {
		r.issueStateCheckers = make(map[string]IssueStateChecker)
	}
	r.issueStateCheckers[key] = checker
}

// issueStateCheckerFor returns the registered checker for key, or nil.
func (r *Runner) issueStateCheckerFor(key string) IssueStateChecker {
	r.issueStateCheckersMu.RLock()
	defer r.issueStateCheckersMu.RUnlock()
	return r.issueStateCheckers[key]
}

// fetchIssueState resolves task's GitHub issue (owner/repo/number) and
// fetches its live state. Both GH-4656 call sites (dispatcher.go's
// pickup-time guard, runner.go's PR-creation preflight) call this exact var
// so tests can override it wholesale — mirrors mergedPRPreflightCheck's
// swappable-var idiom (dispatcher.go) — instead of wiring a real git remote
// plus a registered checker per test case.
//
// Owner/repo resolution: task.SourceRepo when populated (set on tasks built
// from a fresh GitHub event), else the `origin` remote at projectPath —
// buildTaskFromExecution never populates SourceRepo (the executions table
// has no matching column), so the dispatcher pickup site always falls
// through to the git-remote path.
//
// Checker resolution: the per-repo SDK-backed checker registered via
// RegisterIssueStateChecker when one exists for "github:owner/repo", else
// the gh-CLI fallback (ghCLIIssueStateChecker) so the guard still functions
// for repos with no registered SDK client.
//
// Returns an error (never IssueState.Closed=true) for anything that isn't a
// clean live read — callers must fail open and proceed rather than block
// (GH-4656 acceptance #4: pipeline availability outranks the guard).
var fetchIssueState = func(ctx context.Context, runner *Runner, task *Task, projectPath string) (IssueState, error) {
	if task == nil {
		return IssueState{}, fmt.Errorf("fetchIssueState: nil task")
	}

	numStr := strings.TrimSpace(task.GHIssueRef())
	num, err := strconv.Atoi(numStr)
	if err != nil {
		return IssueState{}, fmt.Errorf("fetchIssueState: parse issue number %q: %w", numStr, err)
	}

	owner, repo := "", ""
	if task.SourceRepo != "" {
		owner, repo, _ = strings.Cut(task.SourceRepo, "/")
	}
	if owner == "" || repo == "" {
		owner, repo, err = resolveGitRemote(ctx, projectPath)
		if err != nil {
			return IssueState{}, fmt.Errorf("fetchIssueState: resolve repo: %w", err)
		}
	}

	var checker IssueStateChecker
	if runner != nil {
		checker = runner.issueStateCheckerFor("github:" + owner + "/" + repo)
	}
	if checker == nil {
		checker = ghCLIIssueStateChecker{}
	}
	return checker.GetIssueState(ctx, owner, repo, num)
}
