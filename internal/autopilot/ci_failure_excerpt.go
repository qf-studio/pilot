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
	// GH-5454: the jobs API's step timestamps have whole-second granularity
	// (floored), while raw job-log lines carry nanosecond timestamps. When
	// completed_at has no fractional component, any log line stamped later
	// in that same second — which is exactly where `go test ./...`
	// typically flushes a failing package's buffered output, since it
	// prints when the package finishes and the failing package is usually
	// the last to finish — would be past `end` and silently dropped. Widen
	// the bound to cover the whole completion second so those lines are
	// still included.
	if end.Nanosecond() == 0 {
		end = end.Add(time.Second)
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

// failingTestLineRe matches a Go test failure line, with or without the GH
// Actions timestamp prefix and with any subtest indentation:
// "--- FAIL: TestName/sub (0.01s)".
var failingTestLineRe = regexp.MustCompile(`--- FAIL: (\S+)`)

// goAssertionLineRe matches a Go test assertion/log line such as
// "    quality_test.go:88: expected 2 attempts, got 1".
var goAssertionLineRe = regexp.MustCompile(`^\s*\S+\.go:\d+:`)

// failingTest is one failed test found in a CI log, with the assertion line
// that accompanied it when one was found.
type failingTest struct {
	Name      string
	Assertion string
}

// extractFailingTests returns every distinct "--- FAIL: <Name>" in logs, in
// log order, each paired with the assertion line printed immediately before
// it (go test -v order) or, failing that, immediately after it (default
// order). It scans the FULL log, not the char-capped excerpt, so a failure
// the excerpt cut off still reaches the fix issue (GH-6182: the third
// failing test of PR#6165 was missing from the body, and the pre-flight
// judge rejected the issue as vague).
func extractFailingTests(logs string) []failingTest {
	lines := strings.Split(logs, "\n")
	strip := func(s string) string {
		s = strings.TrimRight(s, "\r")
		if loc := jobLogLineTimestampRe.FindStringIndex(s); loc != nil {
			s = s[loc[1]:]
		}
		return s
	}
	assertionAt := func(i int) string {
		if i < 0 || i >= len(lines) {
			return ""
		}
		if l := strip(lines[i]); goAssertionLineRe.MatchString(l) {
			return strings.TrimSpace(l)
		}
		return ""
	}

	var out []failingTest
	seen := map[string]int{}
	for i, raw := range lines {
		m := failingTestLineRe.FindStringSubmatch(strip(raw))
		if m == nil {
			continue
		}
		assertion := assertionAt(i - 1)
		if assertion == "" {
			assertion = assertionAt(i + 1)
		}
		if idx, ok := seen[m[1]]; ok {
			if out[idx].Assertion == "" {
				out[idx].Assertion = assertion
			}
			continue
		}
		seen[m[1]] = len(out)
		out = append(out, failingTest{Name: m[1], Assertion: assertion})
	}
	return out
}

// writeFailingTestsSection renders the "## Failing tests" list for a fix
// issue body; it writes nothing when logs hold no "--- FAIL:" line.
func writeFailingTestsSection(sb *strings.Builder, logs string) {
	tests := extractFailingTests(strings.TrimPrefix(logs, ciExcerptSentinel))
	if len(tests) == 0 {
		return
	}
	sb.WriteString("## Failing tests\n\n")
	for _, t := range tests {
		if t.Assertion != "" {
			fmt.Fprintf(sb, "- `%s` — %s\n", t.Name, t.Assertion)
		} else {
			fmt.Fprintf(sb, "- `%s`\n", t.Name)
		}
	}
	sb.WriteString("\n")
}
