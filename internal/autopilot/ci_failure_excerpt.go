package autopilot

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
)

// failedCheckExcerptBudgetChars caps the total size of the multi-check
// failing-step excerpt bundle embedded in a continuation issue body, so a
// PR with several failing checks still produces a body comfortably under
// GitHub's issue-body limit (65536 chars).
//
// GH-4460: every GH-4415 continuation (4444/4446/4449/4453) bounced at
// preflight with "provide only runner setup information" because the old
// GetFailedCheckLogs concatenated whole job logs and then hard-truncated
// the combined blob to 2000 chars from the head — on a multi-check failure
// the first check's runner-setup preamble alone could consume the entire
// budget before the actual failure line was ever reached.
const failedCheckExcerptBudgetChars = 12000

// ciExcerptSentinel prefixes a logs string that is already a pre-assembled,
// budget-capped bundle of per-check failing-step excerpts (see
// CIMonitor.GetFailedCheckExcerpts / AssembleFailureExcerptsBody), so
// FeedbackLoop.generateBody can skip the single-blob marker search in
// extractFailureExcerpt — which is designed for one raw log blob and would
// otherwise misfire against multi-check output (no --- FAIL:/panic: marker
// sits at the top level of the bundle, so it would fall through to a
// last-N-lines fallback and clobber earlier checks' excerpts).
const ciExcerptSentinel = "<!-- gh4460:step-excerpt-bundle -->\n"

// jobLogLineTimestampRe matches the leading RFC3339-nanosecond timestamp
// GitHub Actions prefixes on every raw job-log line, e.g.
// "2026-07-18T10:13:56.1234567Z Run go test ./...".
var jobLogLineTimestampRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z) `)

// FailingStepExcerpt is one failed check's extracted evidence: the tail of
// its actual failing step (not the whole job), plus a permalink fallback.
type FailingStepExcerpt struct {
	CheckName    string
	StepName     string // empty when step-level resolution wasn't possible
	Tail         string
	PermalinkURL string
	Source       string // "step", "annotations", or "job" — which fallback tier produced Tail
}

// resolveFailingStep returns the step that actually caused the job to fail,
// as opposed to a later step GitHub marks "cancelled" once the job aborts.
// A step with conclusion "failure" is the primary signal; "timed_out" is the
// fallback for jobs killed by a step-level timeout.
func resolveFailingStep(steps []ghadapter.JobStep) (ghadapter.JobStep, bool) {
	for _, s := range steps {
		if s.Conclusion == "failure" {
			return s, true
		}
	}
	for _, s := range steps {
		if s.Conclusion == "timed_out" {
			return s, true
		}
	}
	return ghadapter.JobStep{}, false
}

// repoStepsExecuted returns the count of non-synthetic (repo-defined) steps
// in steps that GitHub actually recorded an outcome for (non-empty
// Conclusion) — GH-4779 structural signal: a job that died during GitHub's
// own setup/teardown (isSyntheticStepName) before reaching any step the
// workflow itself defines has this at 0, even when the raw step array is
// non-empty (synthetic entries still populate it). Kept independent from
// resolveFailingStep above — a step-name match on the failing step is
// stronger evidence (the failing step is known), while this count also
// catches a hard runner death with no single step-level conclusion recorded
// at all.
func repoStepsExecuted(steps []ghadapter.JobStep) int {
	n := 0
	for _, s := range steps {
		if isSyntheticStepName(s.Name) {
			continue
		}
		if s.Conclusion != "" {
			n++
		}
	}
	return n
}

// sliceLogByStepWindow returns the lines of jobLog whose leading timestamp
// falls within [step.StartedAt, step.CompletedAt]. This slices a job's
// combined raw log down to one step's output without depending on GitHub
// Actions' internal ##[group] formatting, which isn't guaranteed stable
// across runner versions.
func sliceLogByStepWindow(jobLog string, step ghadapter.JobStep) (string, bool) {
	if step.StartedAt == "" || step.CompletedAt == "" {
		return "", false
	}
	start, err := time.Parse(time.RFC3339Nano, step.StartedAt)
	if err != nil {
		return "", false
	}
	end, err := time.Parse(time.RFC3339Nano, step.CompletedAt)
	if err != nil {
		return "", false
	}

	lines := strings.Split(jobLog, "\n")
	matched := make([]string, 0, len(lines))
	for _, line := range lines {
		m := jobLogLineTimestampRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, m[1])
		if err != nil {
			continue
		}
		if !ts.Before(start) && !ts.After(end) {
			matched = append(matched, line)
		}
	}
	if len(matched) == 0 {
		return "", false
	}
	return strings.Join(matched, "\n"), true
}

// tailLines returns the last n lines of s (or all of s when it has n lines
// or fewer).
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// AssembleFailureExcerptsBody renders a list of per-check failing-step
// excerpts into one self-contained block — heading, tail excerpt, and a
// permalink fallback per check — capped to maxTotalChars total. The budget
// is split evenly across excerpts up front so one oversized excerpt can't
// starve the others out of the body entirely (GH-4460).
func AssembleFailureExcerptsBody(excerpts []FailingStepExcerpt, maxTotalChars int) string {
	if len(excerpts) == 0 {
		return ""
	}

	perExcerptBudget := maxTotalChars / len(excerpts)

	var sb strings.Builder
	for i, ex := range excerpts {
		if i > 0 {
			sb.WriteString("\n\n")
		}

		heading := fmt.Sprintf("=== %s ===", ex.CheckName)
		if ex.StepName != "" {
			heading = fmt.Sprintf("=== %s \u2014 failing step: %s ===", ex.CheckName, ex.StepName)
		}
		sb.WriteString(heading)
		sb.WriteString("\n")

		tail := ex.Tail
		if strings.TrimSpace(tail) == "" {
			tail = "(no log output captured)"
		}

		permalinkLine := ""
		if ex.PermalinkURL != "" {
			permalinkLine = "\nFull log: " + ex.PermalinkURL
		}

		// Reserve room for the heading and permalink so the *total* rendered
		// block for this check — not just the tail — respects its share of
		// the budget.
		overhead := len(heading) + 1 + len(permalinkLine)
		tailBudget := perExcerptBudget - overhead
		const minTailBudget = 200 // always keep a minimum useful slice
		if tailBudget < minTailBudget {
			tailBudget = minTailBudget
		}
		sb.WriteString(truncateKeepingTail(tail, tailBudget))
		sb.WriteString(permalinkLine)
	}

	return sb.String()
}
