package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"unicode"
)

// GH-2363: After the 2nd consecutive rejection for the same title, stop
// retrying the issue and post a structured "how to fix" comment instead of
// spamming the same "PR creation refused" error comment every retry.

// titleRejectionMaxCount is the threshold at which Pilot emits the structured
// comment + escalation labels. 2 means: first failure uses normal retry path,
// second failure (same title) escalates.
const titleRejectionMaxCount = 2

// titleRejection tracks consecutive rejections for a single issue.
type titleRejection struct {
	hash  string // sha256 of the rejected title (hex)
	count int    // consecutive rejections of that exact hash
}

// titleRejectionTracker is a process-local counter keyed by task ID.
// Process restarts reset the counter — acceptable because the user still gets
// at most a handful of redundant comments before the guard re-engages.
type titleRejectionTracker struct {
	mu   sync.Mutex
	seen map[string]*titleRejection
}

func newTitleRejectionTracker() *titleRejectionTracker {
	return &titleRejectionTracker{seen: make(map[string]*titleRejection)}
}

// record returns the updated consecutive-rejection count for taskID/title.
// A different title resets the counter to 1.
func (t *titleRejectionTracker) record(taskID, title string) int {
	h := hashTitle(title)
	t.mu.Lock()
	defer t.mu.Unlock()
	cur, ok := t.seen[taskID]
	if !ok || cur.hash != h {
		t.seen[taskID] = &titleRejection{hash: h, count: 1}
		return 1
	}
	cur.count++
	return cur.count
}

// clear drops any tracked rejection for taskID. Called on successful PR
// creation so a future title change starts from a clean slate.
func (t *titleRejectionTracker) clear(taskID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.seen, taskID)
}

// recordTitleRejection wraps the record → escalate → report-progress sequence
// shared by every PR-title finalize path (direct, epic-parent,
// decomposed-parent). GH-4220 (e): only the direct path (runner.go
// executeWithOptions) wired the GH-2363 escalation — finalizeEpicBranchPR and
// finalizeDecomposedParentPR returned on titleErr without ever recording or
// escalating, so a non-conventional epic/decomposed title that survives
// normalizeTitle's auto-correct machinery (rare given the "chore" fallback,
// but possible when diff stats are unavailable) would retry forever instead
// of tripping the stop-retry guidance comment. Call this from every path that
// fails on titleErr; it is a no-op if the tracker was never configured.
func (r *Runner) recordTitleRejection(ctx context.Context, task *Task, result *ExecutionResult) {
	if r.titleRejections == nil {
		return
	}
	count := r.titleRejections.record(task.ID, task.Title)
	if count < titleRejectionMaxCount {
		return
	}
	if err := r.postTitleRejectionEscalation(ctx, task); err != nil {
		r.log.Warn("title-rejection escalation failed",
			slog.String("task_id", task.ID),
			slog.Any("error", err),
		)
		return
	}
	result.TitleRejected = true
	r.log.Info("title-rejection escalated — posted guidance comment, stopping retries",
		slog.String("task_id", task.ID),
		slog.Int("count", count),
	)
}

// clearTitleRejectionState drops any tracked rejection bookkeeping for task
// once its title has been accepted, so a future title change starts counting
// from a clean slate. Call this from every path that successfully normalizes
// a title, mirroring recordTitleRejection's coverage.
func (r *Runner) clearTitleRejectionState(task *Task) {
	if r.titleRejections != nil {
		r.titleRejections.clear(task.ID)
	}
}

func hashTitle(title string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(title)))
	return hex.EncodeToString(sum[:])
}

// suggestConventionalTitle generates a best-effort conventional-commit rewrite
// for a non-conventional title. Heuristic only — the human is expected to
// refine it. Falls back to `chore(repo): <title>` when no signal is found.
func suggestConventionalTitle(title string, labels []string) string {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return "chore: update repository"
	}
	// Strip any leading issue-id prefix the user may have typed (e.g. "GH-123: ")
	trimmed = issuePrefixRegex.ReplaceAllString(trimmed, "")

	// Prefer a label-derived prefix when available.
	if prefixed, ok := autoPrefixTitle(trimmed, labels); ok {
		return lowercaseFirstRune(prefixed)
	}

	// Heuristic: first verb in the title hints at the commit type.
	first := strings.ToLower(firstWord(trimmed))
	verbToType := map[string]string{
		"fix":       "fix",
		"fixes":     "fix",
		"fixed":     "fix",
		"bug":       "fix",
		"add":       "feat",
		"adds":      "feat",
		"added":     "feat",
		"implement": "feat",
		"introduce": "feat",
		"migrate":   "chore",
		"rename":    "refactor",
		"refactor":  "refactor",
		"cleanup":   "chore",
		"remove":    "chore",
		"update":    "chore",
		"document":  "docs",
		"docs":      "docs",
		"test":      "test",
		"tests":     "test",
	}
	kind := verbToType[first]
	if kind == "" {
		kind = "chore"
	}
	return fmt.Sprintf("%s(repo): %s", kind, lowercaseFirstRune(trimmed))
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if unicode.IsSpace(r) {
			return s[:i]
		}
	}
	return s
}

func lowercaseFirstRune(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}

// buildTitleRejectionComment renders the structured "how to fix" comment
// posted on the 2nd consecutive rejection.
func buildTitleRejectionComment(issueNumber int, title string, labels []string) string {
	suggestion := suggestConventionalTitle(title, labels)
	var b strings.Builder
	fmt.Fprintf(&b, "⚠️ Pilot can't open a PR: this issue's title isn't a conventional commit.\n\n")
	fmt.Fprintf(&b, "**Current title**: `%s`\n", strings.TrimSpace(title))
	fmt.Fprintf(&b, "**Suggested rewrite**: `%s`\n\n", suggestion)
	fmt.Fprintf(&b, "To re-dispatch, run:\n")
	fmt.Fprintf(&b, "```\ngh issue edit %d --title %q --remove-label pilot-failed --remove-label pilot-title-rejected --add-label pilot-retry-ready\n```\n\n",
		issueNumber, suggestion)
	fmt.Fprintf(&b, "See the [Conventional Commits spec](https://www.conventionalcommits.org/) for the accepted format.\n")
	return b.String()
}

// postTitleRejectionEscalation posts the structured comment and adds both
// pilot-failed and pilot-title-rejected labels via the gh CLI. The latter
// is read by the GitHub poller to block auto-retry until the title changes.
//
// Only runs for GitHub-sourced tasks; other adapters will fall through to
// their normal failure path (which is already a one-shot label, not a loop).
func (r *Runner) postTitleRejectionEscalation(ctx context.Context, task *Task) error {
	if task.SourceAdapter != "" && task.SourceAdapter != "github" {
		return nil
	}
	issueNum := strings.TrimPrefix(task.ID, "GH-")
	if task.SourceIssueID != "" {
		issueNum = task.SourceIssueID
	}
	if issueNum == "" {
		return fmt.Errorf("no issue number for task %s", task.ID)
	}
	var parsed int
	if _, err := fmt.Sscanf(issueNum, "%d", &parsed); err != nil || parsed <= 0 {
		return fmt.Errorf("invalid issue number %q: %w", issueNum, err)
	}
	comment := buildTitleRejectionComment(parsed, task.Title, task.Labels)

	if err := ghIssueComment(ctx, task.ProjectPath, issueNum, comment); err != nil {
		return fmt.Errorf("post comment: %w", err)
	}
	// Both labels are best-effort; log but don't fail.
	if err := ghAddLabels(ctx, task.ProjectPath, issueNum, []string{"pilot-failed", "pilot-title-rejected"}); err != nil {
		r.log.Warn("title-rejection: failed to add labels",
			"task_id", task.ID, "issue", issueNum, "error", err)
	}
	return nil
}

func ghIssueComment(ctx context.Context, dir, issueID, body string) error {
	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", "issue", "comment", issueID, "--body", body))
	if dir != "" {
		cmd.Dir = dir
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh issue comment: %w (stderr: %s)", err, stderr.String())
	}
	return nil
}

func ghAddLabels(ctx context.Context, dir, issueID string, labels []string) error {
	return ghEditLabels(ctx, dir, issueID, labels, nil)
}

// ghEditLabels adds and removes GitHub issue labels via a single `gh issue
// edit` call. Used by stallTaskAfterRepickHardCap (dispatcher.go, GH-4454
// subtask 3) to swap pilot-failed/pilot-in-progress for pilot-blocked in one
// shot, alongside ghAddLabels' add-only callers.
//
// GH-4526: `gh issue edit` validates every label it references — both
// --add-label and --remove-label — against the repo's label set before
// applying anything, and fails the whole call atomically if any one of them
// is missing (`gh issue edit: 'pilot-failed' not found`). Hosted tenant
// repos are onboarded with zero pre-existing pilot-* labels, so the very
// first surfacing call (e.g. stallTaskAfterRepickHardCap) failed outright on
// every fresh repo. Pre-creating whatever labels this call is about to
// reference sidesteps that without a separate onboarding step.
func ghEditLabels(ctx context.Context, dir, issueID string, addLabels, removeLabels []string) error {
	ghEnsureLabels(ctx, dir, append(append([]string{}, addLabels...), removeLabels...))

	args := []string{"issue", "edit", issueID}
	for _, l := range addLabels {
		args = append(args, "--add-label", l)
	}
	for _, l := range removeLabels {
		args = append(args, "--remove-label", l)
	}
	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", args...))
	if dir != "" {
		cmd.Dir = dir
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh issue edit: %w (stderr: %s)", err, stderr.String())
	}
	return nil
}

// pilotLabelColor is applied to any pilot-* label ghEnsureLabels has to
// create on demand — a plain, unobtrusive gray. The color is cosmetic only;
// nothing in the dispatch pipeline reads it back.
const pilotLabelColor = "ededed"

// ghEnsureLabels idempotently creates any of the given labels that don't yet
// exist on the repo. `gh label create --force` updates-in-place rather than
// erroring when the label is already present, so repeat calls are no-ops.
// Best-effort: a creation failure (e.g. missing repo permissions) is not
// fatal here — the caller's own `gh issue edit` is the source of truth for
// whether the edit itself ultimately succeeded.
func ghEnsureLabels(ctx context.Context, dir string, labels []string) {
	seen := make(map[string]bool, len(labels))
	for _, l := range labels {
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", "label", "create", l, "--color", pilotLabelColor, "--force"))
		if dir != "" {
			cmd.Dir = dir
		}
		_ = cmd.Run()
	}
}
