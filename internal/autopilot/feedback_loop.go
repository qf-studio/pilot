package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/qf-studio/pilot/internal/ghissue"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/text"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// ciFailureExcerptRe matches the start of an actionable CI failure: a Go test
// failure header, a panic, or a GitHub Actions error annotation. These lines
// are typically prefixed by a runner timestamp, so the patterns are not
// anchored to line start.
var ciFailureExcerptRe = regexp.MustCompile(`--- FAIL:|panic:|##\[error\]`)

// maxCIErrorExcerptChars caps the size of the CI Error Logs section embedded
// in fix-request issue bodies to stay well within GitHub's issue body limit.
const maxCIErrorExcerptChars = 4000

// ciFailureExcerptFallbackLines is how many trailing lines to keep when no
// failure marker is found in the log (e.g. plain build/lint output).
const ciFailureExcerptFallbackLines = 150

// extractFailureExcerpt returns the actionable slice of CI logs to embed in a
// fix-request issue body: from the first failure marker onward (this
// naturally includes the trailing `FAIL <pkg>` summary since it always
// follows the failure in a Go test log), or the last N lines when no marker
// is found.
//
// GH-3958: excerpting from the head of the log only captured GitHub Actions
// runner-provisioning preamble ("Current runner version", "Runner Image
// Provisioner"...) and never the actual failure, which sits near the end of
// the log. Every auto-generated fix issue self-rejected as vague as a result.
func extractFailureExcerpt(logs string, maxChars int) string {
	lines := strings.Split(logs, "\n")

	start := -1
	for i, line := range lines {
		if ciFailureExcerptRe.MatchString(line) {
			start = i
			break
		}
	}

	var excerpt []string
	switch {
	case start >= 0:
		if start > 3 {
			start -= 3 // small lead-in context before the failure marker
		} else {
			start = 0
		}
		excerpt = lines[start:]
	case len(lines) > ciFailureExcerptFallbackLines:
		excerpt = lines[len(lines)-ciFailureExcerptFallbackLines:]
	default:
		excerpt = lines
	}

	return truncateKeepingTail(strings.Join(excerpt, "\n"), maxChars)
}

// truncateKeepingTail caps s to maxChars while preserving both ends: the
// head (where the failure marker and its lead-in context live) and the tail
// (where the trailing `FAIL <pkg>` summary lives). A naive head-only cut can
// silently drop the summary line on a large panic/stack-trace dump that
// exceeds maxChars on its own.
func truncateKeepingTail(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	const marker = "\n... (truncated) ...\n"
	budget := maxChars - len(marker)
	if budget <= 0 {
		return s[:maxChars] + "\n... (truncated)"
	}
	headLen := budget * 7 / 10
	tailLen := budget - headLen
	return s[:headLen] + marker + s[len(s)-tailLen:]
}

// FeedbackLoop creates issues when CI fails or bugs are detected.
// It closes the autonomous loop by automatically creating fix issues
// that Pilot can pick up and execute.
type FeedbackLoop struct {
	ghClient     *github.Client
	owner        string
	repo         string
	issueLabels  []string
	learningLoop *memory.LearningLoop // GH-1979: optional, annotates issues with known patterns
	stateStore   *StateStore          // GH-4307: optional, dedups fix-issue creation across retries/daemons
	log          *slog.Logger
}

// NewFeedbackLoop creates a feedback loop for automatic issue creation.
func NewFeedbackLoop(ghClient *github.Client, owner, repo string, cfg *Config) *FeedbackLoop {
	return &FeedbackLoop{
		ghClient:    ghClient,
		owner:       owner,
		repo:        repo,
		issueLabels: cfg.IssueLabels,
		log:         slog.Default().With("component", "feedback-loop"),
	}
}

// SetLearningLoop injects a learning loop for pattern annotation in fix issues.
func (f *FeedbackLoop) SetLearningLoop(ll *memory.LearningLoop) {
	f.learningLoop = ll
}

// SetStateStore injects the persistent state store used to dedup fix-issue
// creation. Optional: without it, CreateFailureIssue never checks for an
// existing claim and always creates a new issue (pre-GH-4307 behavior).
func (f *FeedbackLoop) SetStateStore(store *StateStore) {
	f.stateStore = store
}

// spawnedFixDedupKey identifies one failure signal for idempotent fix-issue
// creation: same PR, same failure type, same set of failed checks. Checks
// are sorted before joining so check-order jitter across API calls can't
// split one real failure into two dedup keys (GH-4307).
func spawnedFixDedupKey(prNumber int, failureType FailureType, failedChecks []string) string {
	sorted := append([]string(nil), failedChecks...)
	sort.Strings(sorted)
	return fmt.Sprintf("fix:pr%d:%s:%s", prNumber, failureType, strings.Join(sorted, ","))
}

// FailureType categorizes the type of failure that occurred.
type FailureType string

const (
	// FailureCIPreMerge indicates CI failed before the PR was merged.
	FailureCIPreMerge FailureType = "ci_pre_merge"
	// FailureCIPostMerge indicates CI failed after the PR was merged to main.
	FailureCIPostMerge FailureType = "ci_post_merge"
	// FailureMerge indicates the PR could not be merged due to conflicts.
	FailureMerge FailureType = "merge_conflict"
	// FailureDeployment indicates deployment failed after merge.
	FailureDeployment FailureType = "deployment"
	// FailureReviewRequested indicates a human reviewer requested changes on the PR.
	FailureReviewRequested FailureType = "review_requested"
)

// CreateFailureIssue creates a GitHub issue for a CI/deployment failure.
// The iteration parameter tracks how many CI fix attempts have been chained
// (0 = original PR, 1 = first fix, etc.). It is embedded in autopilot-meta
// so downstream fix issues can inherit and increment the counter.
// Returns the issue number on success.
func (f *FeedbackLoop) CreateFailureIssue(ctx context.Context, prState *PRState, failureType FailureType, failedChecks []string, logs string, iteration int) (int, error) {
	// CI logs and check names are attacker-controllable (test-failure
	// output, assertion messages, diff content). Strip invisible Unicode
	// format characters before embedding into the fix-issue body so an
	// adversarial test-failure payload cannot re-enter Pilot via the
	// GitHub poller and inject instructions into the next execution.
	var logsStripped, checksStripped int
	logs, logsStripped = text.SanitizeUntrusted(logs)
	for i := range failedChecks {
		var n int
		failedChecks[i], n = text.SanitizeUntrusted(failedChecks[i])
		checksStripped += n
	}
	if logsStripped+checksStripped > 0 {
		f.log.Warn("invisible_unicode_stripped",
			slog.String("source", "feedback_loop"),
			slog.Int("logs_stripped", logsStripped),
			slog.Int("checks_stripped", checksStripped),
		)
	}

	// GH-4307: dedup guard — a re-tick, a release-scan re-discovery, or a
	// second daemon observing the same failure signal must not mint a second
	// fix issue for it. The claim lives in the shared SQLite store so it
	// holds even across process boundaries.
	var dedupRepo, dedupKey string
	if f.stateStore != nil {
		dedupRepo = f.owner + "/" + f.repo
		dedupKey = spawnedFixDedupKey(prState.PRNumber, failureType, failedChecks)
		claimed, claimErr := f.stateStore.ClaimSpawnedFix(dedupRepo, dedupKey)
		if claimErr != nil {
			f.log.Warn("spawned-fix dedup check failed, proceeding without guard",
				"pr", prState.PRNumber, "error", claimErr)
		} else if !claimed {
			existing, lookupErr := f.stateStore.GetSpawnedFixIssue(dedupRepo, dedupKey)
			if lookupErr != nil {
				f.log.Warn("duplicate fix-issue creation suppressed but issue lookup failed",
					"pr", prState.PRNumber, "failure", failureType, "error", lookupErr)
			} else {
				f.log.Info("duplicate fix-issue creation suppressed",
					"pr", prState.PRNumber, "failure", failureType, "existing_issue", existing)
			}
			return existing, nil
		}
	}

	title := f.generateTitle(prState, failureType)

	// GH-4309: belt-and-suspenders — the SQLite claim above cannot see a
	// dedup row lost to a fresh clone, a restored backup, or a wiped store.
	// Before minting a new issue, search GitHub directly for an open issue
	// with this exact title and the pilot label(s); if one already exists,
	// suppress creation and backfill the store claim so future ticks don't
	// repeat this search.
	if existingNum, found, searchErr := f.findExistingFixIssue(ctx, title); searchErr != nil {
		f.log.Warn("existing fix-issue search failed, proceeding with creation",
			"pr", prState.PRNumber, "failure", failureType, "error", searchErr)
	} else if found {
		f.log.Info("existing fix issue found via GitHub search, suppressing duplicate creation",
			"issue", existingNum, "pr", prState.PRNumber, "failure", failureType)
		if dedupKey != "" {
			if recErr := f.stateStore.RecordSpawnedFixIssue(dedupRepo, dedupKey, existingNum); recErr != nil {
				f.log.Warn("failed to backfill spawned fix issue number from GitHub search",
					"issue", existingNum, "error", recErr)
			}
		}
		return existingNum, nil
	}

	// GH-1979: Surface known patterns to annotate the fix issue body.
	var knownPatterns []*memory.CrossPattern
	if f.learningLoop != nil {
		projectPath := f.owner + "/" + f.repo
		patterns, err := f.learningLoop.SurfaceHighValuePatterns(ctx, projectPath)
		if err != nil {
			f.log.Warn("failed to surface patterns for fix issue", "error", err)
		} else {
			knownPatterns = patterns
		}
	}

	body := f.generateBody(prState, failureType, failedChecks, logs, iteration, knownPatterns)

	// AllowAllIssueRepos: FeedbackLoop's owner/repo are set from explicit config at
	// construction, so they're already constrained to a configured project. The
	// explicit sentinel encodes that intent (vs a nil-means-skip default). TASK-286 / GH-3027 / TASK-347.
	issue, err := ghissue.CreatePilotIssue(ctx, f.ghClient, ghissue.AllowAllIssueRepos(), f.owner, f.repo, title, body, f.issueLabels)
	if err != nil {
		return 0, fmt.Errorf("failed to create issue: %w", err)
	}

	if dedupKey != "" {
		if recErr := f.stateStore.RecordSpawnedFixIssue(dedupRepo, dedupKey, issue.Number); recErr != nil {
			f.log.Warn("failed to record spawned fix issue number", "issue", issue.Number, "error", recErr)
		}
	}

	f.log.Info("created fix issue",
		"issue", issue.Number,
		"pr", prState.PRNumber,
		"failure", failureType,
	)

	return issue.Number, nil
}

// findExistingFixIssue searches open issues carrying every configured pilot
// label for an exact title match. It's the GitHub-side complement to the
// SQLite dedup claim in CreateFailureIssue: a store row can be lost (fresh
// clone, restored backup, wiped DB) while the issue it was tracking is still
// open on GitHub.
func (f *FeedbackLoop) findExistingFixIssue(ctx context.Context, title string) (int, bool, error) {
	issues, err := f.ghClient.ListIssues(ctx, f.owner, f.repo, &github.ListIssuesOptions{
		Labels: f.issueLabels,
		State:  github.StateOpen,
	})
	if err != nil {
		return 0, false, err
	}
	for _, issue := range issues {
		if issue.Title == title {
			return issue.Number, true, nil
		}
	}
	return 0, false, nil
}

// generateTitle creates an issue title based on the failure type.
// Titles follow conventional-commits format so Pilot's title validator accepts them.
func (f *FeedbackLoop) generateTitle(prState *PRState, failureType FailureType) string {
	switch failureType {
	case FailureCIPreMerge:
		return fmt.Sprintf("fix(ci): resolve CI failure from PR #%d", prState.PRNumber)
	case FailureCIPostMerge:
		return fmt.Sprintf("fix(ci): resolve post-merge CI failure from PR #%d", prState.PRNumber)
	case FailureMerge:
		return fmt.Sprintf("fix(merge): resolve merge conflict for PR #%d", prState.PRNumber)
	case FailureDeployment:
		return fmt.Sprintf("fix(deploy): resolve deployment failure from PR #%d", prState.PRNumber)
	case FailureReviewRequested:
		return fmt.Sprintf("fix(review): address review feedback on PR #%d", prState.PRNumber)
	default:
		return fmt.Sprintf("fix(autopilot): resolve issue from PR #%d", prState.PRNumber)
	}
}

// generateBody creates a detailed issue body with context for Pilot.
func (f *FeedbackLoop) generateBody(prState *PRState, failureType FailureType, failedChecks []string, logs string, iteration int, knownPatterns []*memory.CrossPattern) string {
	var sb strings.Builder

	sb.WriteString("# Autopilot: Auto-Generated Fix Request\n\n")

	// Context section
	sb.WriteString("## Context\n\n")
	sb.WriteString(fmt.Sprintf("- **Original PR**: #%d\n", prState.PRNumber))
	if prState.IssueNumber > 0 {
		sb.WriteString(fmt.Sprintf("- **Original Issue**: #%d\n", prState.IssueNumber))
	}
	sb.WriteString(fmt.Sprintf("- **Failure Type**: %s\n", failureType))
	if len(prState.HeadSHA) >= 7 {
		sb.WriteString(fmt.Sprintf("- **SHA**: %s\n", prState.HeadSHA[:7]))
	}
	if prState.BranchName != "" {
		sb.WriteString(fmt.Sprintf("- **Branch**: %s\n", prState.BranchName))
	}
	sb.WriteString("\n")

	// Failed checks section
	if len(failedChecks) > 0 {
		sb.WriteString("## Failed Checks\n\n")
		for _, check := range failedChecks {
			sb.WriteString(fmt.Sprintf("- [ ] %s\n", check))
		}
		sb.WriteString("\n")
	}

	// Error logs section in collapsible details block (GH-1567)
	if logs != "" {
		sb.WriteString("<details><summary>CI Error Logs</summary>\n\n")
		sb.WriteString("```\n")
		sb.WriteString(extractFailureExcerpt(logs, maxCIErrorExcerptChars))
		sb.WriteString("\n```\n\n")
		sb.WriteString("</details>\n\n")
	}

	// GH-1979: Known patterns section — helps Pilot avoid past mistakes.
	if len(knownPatterns) > 0 {
		sb.WriteString("## Known Patterns\n\n")
		sb.WriteString("These patterns have been learned from previous failures in this project:\n\n")
		for _, p := range knownPatterns {
			sb.WriteString(fmt.Sprintf("- **%s** (confidence: %.0f%%): %s\n", p.Title, p.Confidence*100, p.Description))
		}
		sb.WriteString("\n")
	}

	// Task instructions for Pilot
	sb.WriteString("## Task\n\n")
	switch failureType {
	case FailureCIPreMerge:
		sb.WriteString("Fix the CI failures listed above. Run tests locally before committing.\n")
	case FailureCIPostMerge:
		sb.WriteString("The PR was merged but CI failed afterward. Investigate and fix.\n")
	case FailureMerge:
		sb.WriteString("Resolve the merge conflicts and ensure the changes integrate properly.\n")
	case FailureDeployment:
		sb.WriteString("The deployment failed. Check logs and fix the deployment issue.\n")
	case FailureReviewRequested:
		sb.WriteString("A reviewer requested changes on this PR. Address the feedback in the review comments above.\n")
	default:
		sb.WriteString("Investigate and fix the issue described above.\n")
	}

	// Wire dependency so fix issue waits for parent to close
	if prState.IssueNumber > 0 {
		sb.WriteString(fmt.Sprintf("\nDepends on: #%d\n", prState.IssueNumber))
	}

	sb.WriteString("\n---\n*This issue was auto-generated by Pilot autopilot.*\n")

	// Machine-readable metadata for poller to parse original branch and PR number.
	// GH-1267: Include pr:N so fix sessions can use --from-pr for context resumption.
	// GH-1566: Include iteration:N to track CI fix cascade depth and enforce limits.
	if prState.BranchName != "" {
		sb.WriteString(fmt.Sprintf("\n<!-- autopilot-meta branch:%s pr:%d iteration:%d -->\n", prState.BranchName, prState.PRNumber, iteration))
	}

	return sb.String()
}

// CreateReviewIssue creates a GitHub issue for review feedback (changes requested).
// reviews are top-level review bodies, comments are line-level code annotations.
// iteration tracks how many review-fix attempts have been chained.
// Returns the issue number on success.
func (f *FeedbackLoop) CreateReviewIssue(ctx context.Context, prState *PRState, reviews []*github.PullRequestReview, comments []*github.PRReviewComment, iteration int) (int, error) {
	title := f.generateTitle(prState, FailureReviewRequested)

	var sb strings.Builder
	sb.WriteString("# Autopilot: Review Feedback\n\n")

	// Context section
	sb.WriteString("## Context\n\n")
	sb.WriteString(fmt.Sprintf("- **Original PR**: #%d\n", prState.PRNumber))
	if prState.IssueNumber > 0 {
		sb.WriteString(fmt.Sprintf("- **Original Issue**: #%d\n", prState.IssueNumber))
	}
	sb.WriteString(fmt.Sprintf("- **Failure Type**: %s\n", FailureReviewRequested))
	if prState.BranchName != "" {
		sb.WriteString(fmt.Sprintf("- **Branch**: %s\n", prState.BranchName))
	}
	sb.WriteString("\n")

	// Review feedback section
	feedback := formatReviewFeedback(reviews, comments)
	if feedback != "" {
		sb.WriteString("## Review Comments\n\n")
		sb.WriteString(feedback)
		sb.WriteString("\n")
	}

	// Task instructions
	sb.WriteString("## Task\n\n")
	sb.WriteString("A reviewer requested changes on this PR. Address the feedback in the review comments above.\n")

	if prState.IssueNumber > 0 {
		sb.WriteString(fmt.Sprintf("\nDepends on: #%d\n", prState.IssueNumber))
	}

	sb.WriteString("\n---\n*This issue was auto-generated by Pilot autopilot.*\n")

	if prState.BranchName != "" {
		sb.WriteString(fmt.Sprintf("\n<!-- autopilot-meta branch:%s pr:%d iteration:%d -->\n", prState.BranchName, prState.PRNumber, iteration))
	}

	body := sb.String()

	// AllowAllIssueRepos: owner/repo set from explicit config at construction; explicit
	// sentinel encodes intent vs a nil-means-skip default (TASK-286 / GH-3027 / TASK-347).
	issue, err := ghissue.CreatePilotIssue(ctx, f.ghClient, ghissue.AllowAllIssueRepos(), f.owner, f.repo, title, body, f.issueLabels)
	if err != nil {
		return 0, fmt.Errorf("failed to create review issue: %w", err)
	}

	f.log.Info("created review issue",
		"issue", issue.Number,
		"pr", prState.PRNumber,
	)

	return issue.Number, nil
}

// formatReviewFeedback formats review comments into collapsible <details> blocks per file.
// Top-level review bodies are grouped under "General", line-level comments are grouped by file path.
// Output is truncated to 4000 characters to avoid GitHub issue body limits.
func formatReviewFeedback(reviews []*github.PullRequestReview, comments []*github.PRReviewComment) string {
	var sb strings.Builder

	// Group top-level review bodies
	for _, r := range reviews {
		if r.Body == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("<details><summary>Review by %s (%s)</summary>\n\n", r.User.Login, r.State))
		sb.WriteString(r.Body)
		sb.WriteString("\n\n</details>\n\n")
	}

	// Group line-level comments by file
	fileComments := make(map[string][]*github.PRReviewComment)
	var fileOrder []string
	for _, c := range comments {
		if _, seen := fileComments[c.Path]; !seen {
			fileOrder = append(fileOrder, c.Path)
		}
		fileComments[c.Path] = append(fileComments[c.Path], c)
	}

	for _, path := range fileOrder {
		sb.WriteString(fmt.Sprintf("<details><summary>%s</summary>\n\n", path))
		for _, c := range fileComments[path] {
			if c.Line > 0 {
				sb.WriteString(fmt.Sprintf("**Line %d** (%s):\n", c.Line, c.User.Login))
			} else {
				sb.WriteString(fmt.Sprintf("**Comment** (%s):\n", c.User.Login))
			}
			sb.WriteString(c.Body)
			sb.WriteString("\n\n")
		}
		sb.WriteString("</details>\n\n")
	}

	result := sb.String()
	if len(result) > 4000 {
		result = result[:4000] + "\n... (truncated)\n"
	}
	return result
}
