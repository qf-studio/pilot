package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// JudgeVerdict is the result of an intent judge evaluation.
type JudgeVerdict struct {
	// Passed indicates whether the diff aligns with the issue intent.
	Passed bool
	// Reason explains why the verdict was PASS or FAIL.
	Reason string
	// Confidence is the judge's confidence level (0.0-1.0).
	Confidence float64
}

// IntentJudge compares git diffs against the original issue to catch scope creep,
// missing requirements, and unrelated changes. Uses Claude Code subprocess so
// calls bill to the operator's CC subscription — no separate API key required.
// Industry research (Spotify) shows this catches ~25% of PRs that would ship wrong code.
type IntentJudge struct {
	claudeCmd        string
	model            string
	judgeTimeout     time.Duration
	preflightTimeout time.Duration
	// maxDiffChars caps the diff payload sent to the judge. GH-4407: kept
	// per-instance (not just a package constant) so it can be overridden
	// from IntentJudgeConfig.MaxDiffChars.
	maxDiffChars int
	log          *slog.Logger
	cmdRunner    func(ctx context.Context, args ...string) ([]byte, error)
}

// NewIntentJudge creates a new IntentJudge that calls Claude Code subprocess.
// claudeCmd defaults to "claude" when empty.
func NewIntentJudge(claudeCmd string) *IntentJudge {
	if claudeCmd == "" {
		claudeCmd = "claude"
	}
	j := &IntentJudge{
		claudeCmd:        claudeCmd,
		model:            "claude-haiku-4-5-20251001",
		judgeTimeout:     30 * time.Second,
		preflightTimeout: 20 * time.Second,
		maxDiffChars:     maxDiffCharsDefault,
		log:              slog.Default(),
	}
	j.cmdRunner = j.defaultCmdRunner
	return j
}

// judgeRSSSampleInterval controls how often the judge subprocess's RSS is
// polled. Short relative to judgeTimeout/preflightTimeout (20-30s) so a kill
// has a usable peak-RSS reading even on a fast failure. GH-4377.
const judgeRSSSampleInterval = 2 * time.Second

// maxJudgeStderrCaptureBytes caps the stderr buffered per judge subprocess
// invocation — a runaway `claude` process should not be able to inflate the
// daemon's own memory while we're trying to diagnose why it died. GH-4377.
const maxJudgeStderrCaptureBytes = 2000

// GH-4377 fail-open RCA: the judge subprocess died "signal: killed" 423
// times since 2026-06-18 with no way to tell whether our own judgeTimeout/
// preflightTimeout context deadline killed it or something external (OS OOM
// killer, manual kill) did. judgeFailureCause* classifies which.
const (
	judgeFailureCauseContextDeadline = "context_deadline"
	judgeFailureCauseExternalSIGKILL = "external_sigkill"
	judgeFailureCauseOther           = "other"
)

// JudgeSubprocessError wraps a failed judge subprocess invocation with the
// diagnostic context needed to tell a daemon-issued timeout kill apart from
// an externally caused one, plus the subprocess's peak RSS and stderr tail —
// previously only "signal: killed" survived to the log. GH-4377.
type JudgeSubprocessError struct {
	Err        error
	Cause      string // one of the judgeFailureCause* constants
	PeakRSSMB  int
	StderrTail string
}

func (e *JudgeSubprocessError) Error() string {
	msg := fmt.Sprintf("%v (cause=%s peak_rss_mb=%d", e.Err, e.Cause, e.PeakRSSMB)
	if e.StderrTail != "" {
		msg += fmt.Sprintf(" stderr_tail=%q", e.StderrTail)
	}
	return msg + ")"
}

func (e *JudgeSubprocessError) Unwrap() error { return e.Err }

// newJudgeSubprocessError classifies a judge subprocess failure. If ctx's
// own deadline had already fired by the time the process exited, our own
// timeout is the kill source (context_deadline) — exec.CommandContext's
// default Cancel sends SIGKILL immediately, so this looks identical to an
// external kill in the raw error text alone. Otherwise, a SIGKILL-signaled
// exit with ctx still live points at something outside our control
// (external_sigkill — most likely the OS OOM killer).
func newJudgeSubprocessError(ctx context.Context, err error, stderr string, peakRSSMB int) *JudgeSubprocessError {
	cause := judgeFailureCauseOther
	if ctx.Err() != nil {
		cause = judgeFailureCauseContextDeadline
	} else {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL {
				cause = judgeFailureCauseExternalSIGKILL
			}
		}
	}
	return &JudgeSubprocessError{
		Err:        err,
		Cause:      cause,
		PeakRSSMB:  peakRSSMB,
		StderrTail: strings.TrimSpace(stderr),
	}
}

// extractStdinPrompt pulls the value following a "-p" flag out of args so it
// can be delivered via stdin instead of argv. Linux caps a single argv
// element at MAX_ARG_STRLEN (128KB, see proc(5)) — the intent judge's prompt
// embeds the full issue body and git diff, which routinely exceeds that on
// large PRs and made the subprocess fail before it even started
// ("fork/exec ...: argument list too long"). GH-4583.
//
// The returned rest slice keeps a bare "-p" flag (no value) in place —
// `claude -p` reads the prompt from stdin when invoked that way — so only
// the oversized value is removed from argv, not the flag itself.
func extractStdinPrompt(args []string) (rest []string, prompt string, ok bool) {
	for i, a := range args {
		if a == "-p" && i+1 < len(args) {
			out := make([]string, 0, len(args)-1)
			out = append(out, args[:i+1]...)
			out = append(out, args[i+2:]...)
			return out, args[i+1], true
		}
	}
	return args, "", false
}

// defaultCmdRunner executes the claude command, sampling RSS and capturing a
// bounded stderr tail so a kill can be diagnosed instead of surfacing as a
// bare "signal: killed". GH-4377.
//
// GH-4583: the prompt value passed via "-p" is routed through stdin rather
// than argv — see extractStdinPrompt — so diffs over Linux's 128KB
// MAX_ARG_STRLEN no longer fail fork/exec with "argument list too long".
func (j *IntentJudge) defaultCmdRunner(ctx context.Context, args ...string) ([]byte, error) {
	args, prompt, hasPrompt := extractStdinPrompt(args)

	cmd := exec.CommandContext(ctx, j.claudeCmd, args...)
	var stdout bytes.Buffer
	stderr := newBoundedBuffer(maxJudgeStderrCaptureBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if hasPrompt {
		cmd.Stdin = strings.NewReader(prompt)
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	rssCtx, cancelRSS := context.WithCancel(context.Background())
	rssCh := StartRSSSampler(rssCtx, cmd.Process.Pid, judgeRSSSampleInterval)

	waitErr := cmd.Wait()
	cancelRSS()
	var rss RSSSample
	if sample, ok := <-rssCh; ok {
		rss = sample
	}

	if waitErr != nil {
		return nil, newJudgeSubprocessError(ctx, waitErr, stderr.String(), rss.PeakMB)
	}
	return stdout.Bytes(), nil
}

// newIntentJudgeWithRunner creates an IntentJudge with a custom command runner for testing.
func newIntentJudgeWithRunner(runner func(ctx context.Context, args ...string) ([]byte, error)) *IntentJudge {
	j := NewIntentJudge("claude")
	j.cmdRunner = runner
	return j
}

// PreFlightDecision is the decision returned by the pre-flight intent judge.
type PreFlightDecision string

const (
	PreFlightAccept            PreFlightDecision = "accept"
	PreFlightRejectQuestion    PreFlightDecision = "reject_question"
	PreFlightRejectVague       PreFlightDecision = "reject_vague"
	PreFlightRejectConflicting PreFlightDecision = "reject_conflicting"
	PreFlightRejectStale       PreFlightDecision = "reject_stale"
	PreFlightRejectOutOfScope  PreFlightDecision = "reject_out_of_scope"
)

// PreFlightVerdict is the result of a pre-flight issue evaluation.
type PreFlightVerdict struct {
	Decision   PreFlightDecision
	Reason     string
	Confidence float64
}

// IsRejection returns true if the verdict indicates the issue should be rejected.
func (v *PreFlightVerdict) IsRejection() bool {
	return v.Decision != PreFlightAccept
}

var (
	verdictPassRegex = regexp.MustCompile(`VERDICT:\s*PASS`)
	verdictFailRegex = regexp.MustCompile(`VERDICT:\s*FAIL`)
	confidenceRegex  = regexp.MustCompile(`CONFIDENCE:\s*([0-9]*\.?[0-9]+)`)

	// maxDiffCharsDefault caps the total diff payload sent to the judge model.
	// GH-4407: raised from 8000 — that cap sliced mid-file on any diff over
	// ~150 changed lines (e.g. GH-15 at 23923 chars, GH-12 at 54240 chars),
	// and the judge then cited its own injected "...[truncated]" marker as
	// proof the implementation was missing, vetoing legitimate large PRs at
	// 0.85-0.95 confidence. Diffs still over this larger cap fall back to
	// per-file truncation (buildJudgeDiffPayload) instead of a raw cutoff.
	maxDiffCharsDefault = 32000

	// minPerFileDiffChars is the floor every file gets when a diff must be
	// truncated to fit maxDiffCharsDefault, so per-file truncation never
	// drops a file's visible content to (near) zero — which is exactly what
	// the judge misread as "this file was not touched" in GH-12.
	minPerFileDiffChars = 500

	// diffGitHeaderRegex matches the "diff --git a/X b/Y" line that begins
	// each file's section in a unified git diff.
	diffGitHeaderRegex = regexp.MustCompile(`^diff --git a/(?:.+) b/(.+)$`)

	preflightDecisionRegex   = regexp.MustCompile(`DECISION:\s*(\S+)`)
	preflightReasonRegex     = regexp.MustCompile(`REASON:\s*(.+)`)
	preflightConfidenceRegex = regexp.MustCompile(`CONFIDENCE:\s*([0-9]*\.?[0-9]+)`)

	// maxPreflightBodyChars caps the issue body sent to the preflight judge.
	// GH-4507: raised from 4000 to 32000 — matching maxDiffCharsDefault, same
	// token-budget reasoning as GH-4407. At 4000 chars, qf-studio/pilot-console#26
	// (a complete, 22,926-char spec) was falsely rejected as reject_vague: the
	// judge saw only ~17% of the issue and correctly noticed the acceptance
	// criteria it referenced weren't visible. Bodies still over this larger
	// cap are now middle-truncated (truncatePreflightBody) rather than
	// tail-cut, since acceptance criteria, scope fences, and refs live at the
	// end of our issue spec format.
	maxPreflightBodyChars = 32000
)

// truncatePreflightBody middle-truncates a body over maxPreflightBodyChars,
// preserving a head slice (context) and a tail slice (acceptance criteria,
// scope fences, refs) with an explicit omission marker in between. GH-4507:
// replaces the previous tail-cut "...[truncated]", which discarded the exact
// sections (ACs, scope, refs) that live at the end of long, fully-specified
// issues, causing the preflight judge to see only a head fragment and
// falsely reject_vague.
func truncatePreflightBody(body string) string {
	if len(body) <= maxPreflightBodyChars {
		return body
	}

	// Head gets 1/4 of the budget (context, problem statement); the rest is
	// kept from the tail, where acceptance criteria, scope fences, and refs
	// live in our issue spec format.
	headChars := maxPreflightBodyChars / 4
	tailChars := maxPreflightBodyChars - headChars
	if headChars+tailChars >= len(body) {
		return body
	}

	omitted := len(body) - headChars - tailChars
	head := body[:headChars]
	tail := body[len(body)-tailChars:]
	return fmt.Sprintf("%s\n...[truncated: %d chars omitted from middle of issue body]\n%s", head, omitted, tail)
}

const preflightJudgeSystemPrompt = `You are a pre-flight issue quality judge. Evaluate whether a GitHub issue is actionable for an autonomous AI developer.

Classify the issue as exactly one of:
- accept: clear, specific, implementable request with enough context
- reject_question: issue is asking a question rather than requesting implementation
- reject_vague: too vague or ambiguous to implement without further clarification
- reject_conflicting: contains contradictory requirements that cannot all be satisfied
- reject_stale: describes something already done or clearly outdated
- reject_out_of_scope: outside the repository's purpose or requires unavailable external resources

IMPORTANT — truncation handling: A "...[truncated: N chars omitted from middle of issue body]" marker inside the issue description means content was cut from the middle for length only — it is NOT evidence the issue is vague or missing specification. Never cite the presence of this marker itself as a reason for reject_vague, and never base reject_vague on a section (e.g. specific acceptance criteria) simply because it falls inside the omitted range. Judge actionability from the head and tail content that IS shown.

Output exactly:
DECISION: <classification>
REASON: <one sentence explanation>
CONFIDENCE: <0.0-1.0>`

const intentJudgeSystemPrompt = `You are a code review judge. Compare the git diff against the original issue title and description. Determine if the diff implements what was requested.

Check for:
1) Scope creep (changes unrelated to the issue)
2) Missing requirements (issue asks for X but diff doesn't include it)
3) Unrelated changes (refactoring or cleanup not mentioned in issue)
4) Incomplete multi-file changes (if the issue implies changes to multiple backends, adapters, or sibling files, verify ALL were updated — not just one)

IMPORTANT — truncation handling: Large diffs are prefixed with a "## Changed Files" manifest listing every file the diff touches, with add/remove line counts. That manifest is always complete and NEVER truncated, even when the diff body below it is shortened for length. A "...[truncated: N more bytes ... omitted]" marker inside a file's diff body means ONLY that content was cut to fit — it is NOT evidence that the file, a function, or a feature is missing or unimplemented. Judge scope and completeness using the Changed Files manifest, not the presence of a truncation marker. Never cite a truncation marker itself as a reason for VERDICT:FAIL.

Output exactly one of: VERDICT:PASS or VERDICT:FAIL followed by a brief reason on the next line.
Then output CONFIDENCE:X.X (0.0-1.0).`

// Judge evaluates whether a git diff aligns with the original issue intent.
func (j *IntentJudge) Judge(ctx context.Context, issueTitle, issueBody, diff string) (*JudgeVerdict, error) {
	if diff == "" {
		return nil, fmt.Errorf("empty diff")
	}

	// Truncate diff to prevent token overflow, distributing the cut across
	// files (with a full file manifest) rather than a single blind cutoff.
	maxChars := j.maxDiffChars
	if maxChars <= 0 {
		maxChars = maxDiffCharsDefault
	}
	diff = buildJudgeDiffPayload(diff, maxChars)

	prompt := fmt.Sprintf("%s\n\n## Issue Title\n%s\n\n## Issue Description\n%s\n\n## Git Diff\n```diff\n%s\n```",
		intentJudgeSystemPrompt, issueTitle, issueBody, diff)

	judgeCtx, cancel := context.WithTimeout(ctx, j.judgeTimeout)
	defer cancel()

	output, err := j.cmdRunner(judgeCtx, "--print", "-p", prompt, "--model", j.model, "--output-format", "text")
	if err != nil {
		return nil, fmt.Errorf("intent judge subprocess: %w", err)
	}

	return parseJudgeResponse(string(output))
}

// diffFileSection is one file's slice of a unified diff, delimited by
// "diff --git a/X b/Y" header lines.
type diffFileSection struct {
	path    string
	body    string
	added   int
	removed int
}

// splitDiffByFile breaks a unified diff into per-file sections so truncation
// can be distributed fairly across files instead of a single char cutoff
// that can consume the whole budget on the first file(s) and leave later
// files with zero visible content. GH-4407.
func splitDiffByFile(diff string) []diffFileSection {
	lines := strings.Split(diff, "\n")
	var sections []diffFileSection
	var cur *diffFileSection

	flush := func() {
		if cur != nil {
			cur.body = strings.TrimSuffix(cur.body, "\n")
			sections = append(sections, *cur)
		}
	}

	for _, line := range lines {
		if m := diffGitHeaderRegex.FindStringSubmatch(line); m != nil {
			flush()
			cur = &diffFileSection{path: m[1]}
		} else if cur == nil {
			// Content before any "diff --git" header (malformed/partial
			// diff) - don't drop it, buffer under a synthetic path.
			cur = &diffFileSection{path: "(diff)"}
		}
		cur.body += line + "\n"
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			// File marker lines, not content changes.
		case strings.HasPrefix(line, "+"):
			cur.added++
		case strings.HasPrefix(line, "-"):
			cur.removed++
		}
	}
	flush()
	return sections
}

// buildJudgeDiffPayload prepares the diff text sent to the judge model.
// Diffs within maxChars are returned unmodified. Diffs over the cap are
// truncated per-file — never dropping a whole file's content to zero — and
// prefixed with a complete file manifest, so the judge always has full
// visibility into diff *scope* even when it can't see every changed line.
//
// GH-4407: the previous global char-cutoff sliced mid-file on large diffs,
// and the judge then cited its own injected "...[truncated]" marker as
// proof the implementation was missing.
func buildJudgeDiffPayload(diff string, maxChars int) string {
	if len(diff) <= maxChars {
		return diff
	}

	sections := splitDiffByFile(diff)
	if len(sections) <= 1 {
		// No parseable per-file boundaries (single-file or malformed diff) -
		// fall back to a plain tail cutoff.
		return diff[:maxChars] + "\n...[truncated]"
	}

	var manifest strings.Builder
	manifest.WriteString(fmt.Sprintf("## Changed Files (%d total, full list — not truncated)\n", len(sections)))
	for _, s := range sections {
		manifest.WriteString(fmt.Sprintf("- %s (+%d/-%d)\n", s.path, s.added, s.removed))
	}
	manifest.WriteString("\n## Diff Content (per-file, may be truncated for length — see manifest above for the full file list)\n")

	budget := maxChars - manifest.Len()
	if budget < 0 {
		budget = 0
	}
	perFile := budget / len(sections)
	if perFile < minPerFileDiffChars {
		perFile = minPerFileDiffChars
	}

	var body strings.Builder
	for _, s := range sections {
		if len(s.body) <= perFile {
			body.WriteString(s.body)
			body.WriteString("\n")
			continue
		}
		omitted := len(s.body) - perFile
		body.WriteString(s.body[:perFile])
		body.WriteString(fmt.Sprintf("\n...[truncated: %d more bytes of %s omitted for length, see manifest above for full file list]\n", omitted, s.path))
	}

	return manifest.String() + body.String()
}

// JudgeIssue evaluates whether a GitHub issue is actionable before dispatching to a worker.
// Returns a PreFlightVerdict with decision, reason, and confidence.
// Empty body is treated as vague (returns reject_vague, not an error).
// Bodies over maxPreflightBodyChars are middle-truncated (see
// truncatePreflightBody) before sending.
func (j *IntentJudge) JudgeIssue(ctx context.Context, title, body, repoContext string) (*PreFlightVerdict, error) {
	body = truncatePreflightBody(body)

	userContent := fmt.Sprintf("## Issue Title\n%s\n\n## Issue Description\n%s", title, body)
	if repoContext != "" {
		userContent += fmt.Sprintf("\n\n## Repository Context\n%s", repoContext)
	}

	prompt := fmt.Sprintf("%s\n\n%s", preflightJudgeSystemPrompt, userContent)

	preflightCtx, cancel := context.WithTimeout(ctx, j.preflightTimeout)
	defer cancel()

	output, err := j.cmdRunner(preflightCtx, "--print", "-p", prompt, "--model", j.model, "--output-format", "text")
	if err != nil {
		return nil, fmt.Errorf("intent judge subprocess: %w", err)
	}

	return parsePreFlightResponse(string(output))
}

// parsePreFlightResponse extracts decision, reason, and confidence from the pre-flight judge's response.
func parsePreFlightResponse(text string) (*PreFlightVerdict, error) {
	verdict := &PreFlightVerdict{}

	m := preflightDecisionRegex.FindStringSubmatch(text)
	if len(m) < 2 {
		return nil, fmt.Errorf("no DECISION signal found in response")
	}
	decision := PreFlightDecision(strings.ToLower(strings.TrimSpace(m[1])))
	switch decision {
	case PreFlightAccept, PreFlightRejectQuestion, PreFlightRejectVague,
		PreFlightRejectConflicting, PreFlightRejectStale, PreFlightRejectOutOfScope:
		verdict.Decision = decision
	default:
		return nil, fmt.Errorf("unknown DECISION value: %q", m[1])
	}

	if rm := preflightReasonRegex.FindStringSubmatch(text); len(rm) >= 2 {
		verdict.Reason = strings.TrimSpace(rm[1])
	}

	if cm := preflightConfidenceRegex.FindStringSubmatch(text); len(cm) >= 2 {
		if c, err := strconv.ParseFloat(cm[1], 64); err == nil {
			verdict.Confidence = c
		}
	}

	return verdict, nil
}

// parseJudgeResponse extracts verdict, reason, and confidence from the judge's response.
func parseJudgeResponse(text string) (*JudgeVerdict, error) {
	verdict := &JudgeVerdict{}

	if verdictPassRegex.MatchString(text) {
		verdict.Passed = true
	} else if verdictFailRegex.MatchString(text) {
		verdict.Passed = false
	} else {
		return nil, fmt.Errorf("no VERDICT signal found in response")
	}

	// Extract confidence
	if m := confidenceRegex.FindStringSubmatch(text); len(m) >= 2 {
		if c, err := strconv.ParseFloat(m[1], 64); err == nil {
			verdict.Confidence = c
		}
	}

	// Extract reason: text between verdict line and confidence line
	lines := strings.Split(text, "\n")
	var reasonLines []string
	pastVerdict := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if verdictPassRegex.MatchString(trimmed) || verdictFailRegex.MatchString(trimmed) {
			pastVerdict = true
			continue
		}
		if confidenceRegex.MatchString(trimmed) {
			break
		}
		if pastVerdict && trimmed != "" {
			reasonLines = append(reasonLines, trimmed)
		}
	}
	verdict.Reason = strings.Join(reasonLines, " ")

	return verdict, nil
}
