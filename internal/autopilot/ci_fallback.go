package autopilot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// ActionsRunsClient is the optional capability of the in-tree GitHub client
// (the StepLogClient value) that the CI verdict fallback needs (GH-6283).
type ActionsRunsClient interface {
	ListWorkflowRuns(ctx context.Context, owner, repo, sha string) ([]ghadapter.WorkflowRun, error)
	ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int64) ([]ghadapter.WorkflowJob, error)
}

// ErrCIUnreadable is returned when a token can read neither check runs nor
// one of the sources the fallback reads instead (Actions runs, commit
// statuses): CI is unknowable, not red. Callers treat it like any CI poll
// error — hold, then fail after ci_checks.api_outage_budget.
type ErrCIUnreadable struct {
	// Sources names the unreadable sources, e.g. "actions runs".
	Sources []string
	// Err is the first underlying API error.
	Err error
}

func (e *ErrCIUnreadable) Error() string {
	return fmt.Sprintf("CI unreadable: check runs are forbidden and %s could not be read: %v",
		strings.Join(e.Sources, ", "), e.Err)
}

func (e *ErrCIUnreadable) Unwrap() error { return e.Err }

// Is makes errors.Is(err, &ErrCIUnreadable{}) match any ErrCIUnreadable.
func (e *ErrCIUnreadable) Is(target error) bool {
	_, ok := target.(*ErrCIUnreadable)
	return ok
}

// actionsClient returns the client that lists workflow runs, or nil when the
// monitor has none wired.
func (m *CIMonitor) actionsClient() ActionsRunsClient {
	ac, _ := m.stepLogClient.(ActionsRunsClient)
	return ac
}

// checkRunsDenied is the failure path of listLatestCheckRuns: when the
// check-runs read is refused with a 403 (a fine-grained token, GH-6283) it
// remembers that for the monitor's lifetime and serves the verdict inputs from
// Actions runs and commit statuses instead. Any other error is returned as is.
func (m *CIMonitor) checkRunsDenied(ctx context.Context, sha string, err error) (*github.CheckRunsResponse, error) {
	if !ghadapter.IsCheckRunsForbidden(err) || m.actionsClient() == nil {
		return nil, err
	}
	if m.checkRunsForbidden.CompareAndSwap(false, true) {
		m.log.Warn("check runs are forbidden for this token (fine-grained tokens cannot read them); reading CI from Actions runs and commit statuses instead",
			"owner", m.owner,
			"repo", m.repo,
			"error", err,
		)
	}
	return m.fallbackCheckRuns(ctx, sha)
}

// fallbackCheckRuns builds the check-run list a token without the Checks API
// would have seen, from the two sources it can read:
//
//   - Actions workflow runs of the sha, the latest attempt per workflow, as
//     one entry per workflow named after the workflow;
//   - the jobs of those runs, one entry per job named after the job, only when
//     a required-checks allowlist needs to resolve check names;
//   - commit statuses, one entry per context. A combined status with zero
//     statuses contributes nothing: GitHub reports it as "pending", and
//     turning "no CI" into a verdict is the grace-period logic's job.
//
// Every consumer of listLatestCheckRuns then applies its usual rules
// (allowlist, exclusions, grace period, aggregation) unchanged. Entries carry
// no job ID except real jobs, so log collection for fix issues finds nothing
// to fetch for a workflow-level entry and stays degraded.
func (m *CIMonitor) fallbackCheckRuns(ctx context.Context, sha string) (*github.CheckRunsResponse, error) {
	ac := m.actionsClient()
	if ac == nil {
		return nil, errors.New("check runs are forbidden and no Actions client is wired")
	}

	var unreadable []string
	var firstErr error
	note := func(source string, err error) error {
		if !ghadapter.IsUnreadable(err) {
			return fmt.Errorf("read %s for CI fallback: %w", source, err)
		}
		unreadable = append(unreadable, source)
		if firstErr == nil {
			firstErr = err
		}
		return nil
	}

	runs, err := ac.ListWorkflowRuns(ctx, m.owner, m.repo, sha)
	if err != nil {
		if terr := note("actions runs", err); terr != nil {
			return nil, terr
		}
	}
	combined, err := m.ghClient.GetCombinedStatus(ctx, m.owner, m.repo, sha)
	if err != nil {
		if terr := note("commit statuses", err); terr != nil {
			return nil, terr
		}
	}

	var entries []github.CheckRun
	kept := latestWorkflowRuns(runs)
	for _, run := range kept {
		entries = append(entries, github.CheckRun{
			HeadSHA:    sha,
			Name:       workflowRunName(run),
			Status:     run.Status,
			Conclusion: run.Conclusion,
			DetailsURL: run.HTMLURL,
		})
	}
	if len(m.requiredChecks) > 0 && len(unreadable) == 0 {
		for _, run := range kept {
			jobs, jobsErr := ac.ListWorkflowRunJobs(ctx, m.owner, m.repo, run.ID)
			if jobsErr != nil {
				if terr := note("actions jobs", jobsErr); terr != nil {
					return nil, terr
				}
				break
			}
			for _, job := range jobs {
				entries = append(entries, github.CheckRun{
					ID:         job.ID,
					HeadSHA:    sha,
					Name:       job.Name,
					Status:     job.Status,
					Conclusion: job.Conclusion,
					DetailsURL: job.HTMLURL,
				})
			}
		}
	}
	if len(unreadable) > 0 {
		return nil, &ErrCIUnreadable{Sources: unreadable, Err: firstErr}
	}
	entries = append(entries, commitStatusEntries(sha, combined)...)

	merged := m.mergeEntriesByName(entries)
	return &github.CheckRunsResponse{TotalCount: len(merged), CheckRuns: merged}, nil
}

// workflowRunName is the entry name of a workflow run.
func workflowRunName(run ghadapter.WorkflowRun) string {
	if run.Name != "" {
		return run.Name
	}
	return fmt.Sprintf("workflow-%d", run.WorkflowID)
}

// latestWorkflowRuns keeps one run per workflow: the highest run_attempt, the
// highest run ID on a tie (a re-run attempt supersedes the attempt it
// re-ran, as latestCheckRunsByName does for check runs).
func latestWorkflowRuns(runs []ghadapter.WorkflowRun) []ghadapter.WorkflowRun {
	key := func(r ghadapter.WorkflowRun) string {
		if r.WorkflowID != 0 {
			return fmt.Sprintf("id:%d", r.WorkflowID)
		}
		return "name:" + r.Name
	}
	latest := make(map[string]ghadapter.WorkflowRun, len(runs))
	order := make([]string, 0, len(runs))
	for _, run := range runs {
		k := key(run)
		existing, ok := latest[k]
		if !ok {
			order = append(order, k)
			latest[k] = run
			continue
		}
		if run.RunAttempt > existing.RunAttempt || (run.RunAttempt == existing.RunAttempt && run.ID > existing.ID) {
			latest[k] = run
		}
	}
	kept := make([]ghadapter.WorkflowRun, 0, len(order))
	for _, k := range order {
		kept = append(kept, latest[k])
	}
	return kept
}

// commitStatusEntries maps the contexts of a combined status to check-run
// shaped entries: failure and error are completed failures, success a
// completed success, anything else (pending) still running. A combined status
// with zero statuses adds nothing. When the response lists fewer contexts than
// its total (GitHub pages them), the combined state stands in for the rest.
func commitStatusEntries(sha string, combined *github.CombinedStatus) []github.CheckRun {
	if combined == nil || combined.TotalCount == 0 {
		return nil
	}
	entries := make([]github.CheckRun, 0, len(combined.Statuses)+1)
	for _, st := range combined.Statuses {
		name := st.Context
		if name == "" {
			name = "default"
		}
		entries = append(entries, statusEntry(sha, name, st.State, st.TargetURL))
	}
	if combined.TotalCount > len(combined.Statuses) {
		entries = append(entries, statusEntry(sha, "commit status", combined.State, ""))
	}
	return entries
}

func statusEntry(sha, name, state, targetURL string) github.CheckRun {
	entry := github.CheckRun{HeadSHA: sha, Name: name, DetailsURL: targetURL}
	switch state {
	case github.StatusSuccess:
		entry.Status, entry.Conclusion = github.CheckRunCompleted, github.ConclusionSuccess
	case github.StatusFailure, github.StatusError:
		entry.Status, entry.Conclusion = github.CheckRunCompleted, github.ConclusionFailure
	default:
		entry.Status = github.CheckRunInProgress
	}
	return entry
}

// mergeEntriesByName collapses entries sharing a name (a workflow and its only
// job often do) into the worst of them, so a failure is never masked by a
// green namesake: failure over pending over success.
func (m *CIMonitor) mergeEntriesByName(entries []github.CheckRun) []github.CheckRun {
	rank := func(run github.CheckRun) int {
		switch m.mapCheckStatus(run.Status, run.Conclusion) {
		case CIFailure:
			return 2
		case CISuccess:
			return 0
		default:
			return 1
		}
	}
	index := make(map[string]int, len(entries))
	merged := make([]github.CheckRun, 0, len(entries))
	for _, entry := range entries {
		i, ok := index[entry.Name]
		if !ok {
			index[entry.Name] = len(merged)
			merged = append(merged, entry)
			continue
		}
		if rank(entry) > rank(merged[i]) {
			merged[i] = entry
		}
	}
	return merged
}
