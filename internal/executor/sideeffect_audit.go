// Package executor — GH-4670
//
// Post-run GitHub side-effect audit: the detective half of the GH-4649
// containment pair (the advisory half is githubScopeDirective in
// prompt_builder.go). GH-4649 incident: an executor session improvised
// `gh issue close` plus a `pilot-superseded` label on a SIBLING issue mid-run.
// Every existing post-run gate (getPostExecutionSummary, the prompt's own
// self-report) only inspects git state — a GitHub-side mutation on an issue
// other than the one dispatched was, and remains, invisible to all of them.
//
// This file adds one check to close that blind spot: after a run completes,
// search the task's own repo for issues closed or reopened since the run
// started, excluding the dispatched issue itself. Any hit is journaled as an
// execution_events row (memory.StageGithubSideEffect) and raised as an
// alert-engine warning — detection only, no auto-revert (reopening a closed
// issue could itself be wrong; the evidence is for a human operator to judge).
//
// Rate discipline: exactly one GitHub call per audited run (the search
// itself). Fails open on any error — one WARN log, no event, no alert, and
// the run's outcome is unaffected either way.
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
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// GithubSideEffectIssue is one closed-or-reopened issue found by the post-run
// audit search, in the task's own repo, within the run's time window.
type GithubSideEffectIssue struct {
	Number int
	Title  string
	// State is the issue's state at search time ("open" or "closed"). A
	// currently-open hit in this list means it was reopened during the
	// window; a "closed" hit means it was closed during the window.
	State string
	URL   string
}

// GithubSideEffectSearcher is the narrow read surface the post-run audit
// needs: one search across a repo for issues closed or reopened at/after
// since. Mirrors the RepoAllowlist pattern (repo_guardrail.go, GH-3027/
// TASK-286) — a small consumer-side interface, faked directly in tests.
// Concrete implementations live in this file (gh-CLI-backed) rather than in
// internal/adapters/github, which already imports this package (see
// epic.go's SubIssueLinker doc comment) — importing it back here would be a
// cycle.
type GithubSideEffectSearcher interface {
	// SearchClosedOrReopenedSince returns issues in owner/repo that were
	// closed or reopened at or after since. Implementations should make
	// exactly one GitHub API call.
	SearchClosedOrReopenedSince(ctx context.Context, owner, repo string, since time.Time) ([]GithubSideEffectIssue, error)
}

// ghCLISideEffectSearcher is the default GithubSideEffectSearcher. It shells
// out to `gh search issues`, the same gh-CLI idiom already used throughout
// this package (epic.go, git.go, title_rejection.go) to read/write GitHub
// state without importing adapters/github.
type ghCLISideEffectSearcher struct{}

// NewGithubSideEffectSearcher returns the default gh-CLI-backed
// GithubSideEffectSearcher, wired onto a Runner via SetGithubSideEffectSearcher.
func NewGithubSideEffectSearcher() GithubSideEffectSearcher {
	return ghCLISideEffectSearcher{}
}

// SearchClosedOrReopenedSince implements GithubSideEffectSearcher via
// `gh search issues --state closed --updated >=<since>`, one process
// invocation (one GitHub API call). Detects issues that are still closed at
// search time; a reopen that leaves an issue open again is not distinguishable
// from any other "open" issue via the Search API and is out of scope for this
// single-call, fail-open check.
func (ghCLISideEffectSearcher) SearchClosedOrReopenedSince(ctx context.Context, owner, repo string, since time.Time) ([]GithubSideEffectIssue, error) {
	args := []string{
		"search", "issues",
		"--repo", owner + "/" + repo,
		"--state", "closed",
		"--updated", ">=" + since.UTC().Format(time.RFC3339),
		"--json", "number,title,state,url",
	}
	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", args...))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh search issues: %w (stderr: %s)", err, stderr.String())
	}

	var raw []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse gh search issues output: %w", err)
	}

	hits := make([]GithubSideEffectIssue, 0, len(raw))
	for _, r := range raw {
		hits = append(hits, GithubSideEffectIssue{
			Number: r.Number,
			Title:  r.Title,
			State:  strings.ToLower(r.State),
			URL:    r.URL,
		})
	}
	return hits, nil
}

// SetGithubSideEffectSearcher injects the searcher used by the GH-4670
// post-run audit. Passing nil disables the audit (auditGithubSideEffects
// becomes a no-op) — the safe default for call paths that haven't wired one.
func (r *Runner) SetGithubSideEffectSearcher(searcher GithubSideEffectSearcher) {
	r.githubSideEffectSearcher = searcher
}

// HasGithubSideEffectSearcher reports whether a searcher is configured.
func (r *Runner) HasGithubSideEffectSearcher() bool { return r.githubSideEffectSearcher != nil }

// auditGithubSideEffects (GH-4670) is the detective half of the GH-4649
// containment pair: it queries the task's own repo for issues closed or
// reopened since runStart, excludes the dispatched issue itself, and for
// every remaining hit journals an executor.github_sideeffect execution event
// plus fires an alert-engine warning (same channel as task_failed). No-op
// (and no GitHub call) when no searcher is wired or the task isn't a
// GitHub-sourced issue. Fails open on search errors: one WARN log, nothing
// else — a missing token or transient API hiccup must never affect whether
// the run is reported as succeeded.
func (r *Runner) auditGithubSideEffects(ctx context.Context, task *Task, runStart time.Time) {
	if r.githubSideEffectSearcher == nil {
		return
	}
	if task.SourceRepo == "" || (task.SourceAdapter != "" && task.SourceAdapter != "github") {
		return
	}
	owner, repo, ok := strings.Cut(task.SourceRepo, "/")
	if !ok || owner == "" || repo == "" {
		return
	}
	dispatchedNum, dispatchedErr := strconv.Atoi(task.SourceIssueID)

	hits, err := r.githubSideEffectSearcher.SearchClosedOrReopenedSince(ctx, owner, repo, runStart)
	if err != nil {
		slog.Warn("github_sideeffect_audit_failed",
			slog.String("component", "executor.sideeffect_audit"),
			slog.String("task_id", task.ID),
			slog.String("repo", task.SourceRepo),
			slog.Any("error", err),
		)
		return
	}

	for _, hit := range hits {
		if dispatchedErr == nil && hit.Number == dispatchedNum {
			continue // the session's own dispatched issue — expected lifecycle activity
		}

		detail, marshalErr := json.Marshal(struct {
			Repo       string `json:"repo"`
			Number     int    `json:"number"`
			Title      string `json:"title"`
			State      string `json:"state"`
			URL        string `json:"url"`
			TaskIssue  string `json:"task_issue"`
			RunStartAt string `json:"run_start_at"`
		}{
			Repo:       task.SourceRepo,
			Number:     hit.Number,
			Title:      hit.Title,
			State:      hit.State,
			URL:        hit.URL,
			TaskIssue:  task.SourceIssueID,
			RunStartAt: runStart.UTC().Format(time.RFC3339),
		})
		if marshalErr == nil {
			r.recordExecutionEvent(task.LogExecutionID(), memory.StageGithubSideEffect, string(detail))
		}

		slog.Warn("github_sideeffect_detected",
			slog.String("component", "executor.sideeffect_audit"),
			slog.String("task_id", task.ID),
			slog.String("repo", task.SourceRepo),
			slog.Int("issue", hit.Number),
			slog.String("state", hit.State),
		)

		r.emitAlertEvent(AlertEvent{
			Type:      AlertEventTypeGithubSideEffect,
			TaskID:    task.ID,
			TaskTitle: task.Title,
			Project:   task.ProjectPath,
			Error: fmt.Sprintf("session dispatched for %s#%s mutated sibling issue #%d (%s): %s",
				task.SourceRepo, task.SourceIssueID, hit.Number, hit.State, hit.Title),
			Metadata: map[string]string{
				"repo":       task.SourceRepo,
				"issue":      strconv.Itoa(hit.Number),
				"state":      hit.State,
				"task_issue": task.SourceIssueID,
			},
			Timestamp: time.Now(),
		})
	}
}
