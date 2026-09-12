package executor

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Acceptance-evidence gate (GH-5435).
//
// Six consecutive Pilot PRs shipped with an acceptance checklist item of the
// form "paste the output of `cmd`" or "delete line L -> TestY fails" left
// unchecked and with no output pasted — the reviewer had to re-run the
// commands by hand every time. Green CI is not proof a mutation-style pin
// actually catches the regression it claims to (the TASK-460 false-success
// class); the paste is the artifact that proves the executor ran the check
// and observed the result.
//
// This file classifies each acceptance-checklist item (paste-output vs.
// mutation vs. other, unrelated items) and renders the PR-body sections.
// acceptance_evidence_run.go executes the classified items against the
// worktree. Both are pure/deterministic except for the actual command
// execution, which is injected via AcceptanceCommandRunner so this file's
// classification and rendering logic is unit-testable without a shell.

// AcceptanceItemKind classifies one acceptance-checklist item by whether it
// requires the executor to produce runnable evidence for the PR body.
type AcceptanceItemKind string

const (
	// AcceptanceItemPasteOutput matches items asking for a command's output
	// to be pasted into the PR body ("pasted into the PR body", "paste the
	// output", "terminal output").
	AcceptanceItemPasteOutput AcceptanceItemKind = "paste_output"
	// AcceptanceItemMutation matches items describing a mutation ("change
	// …", "delete line …", "remove …") that must cause a named test/package
	// to fail.
	AcceptanceItemMutation AcceptanceItemKind = "mutation"
	// AcceptanceItemOther is every acceptance item that isn't an
	// evidence-requiring paste-output or mutation item — left untouched.
	AcceptanceItemOther AcceptanceItemKind = "other"
)

// AcceptanceItem is one classified acceptance-checklist entry.
type AcceptanceItem struct {
	// Text is the acceptance item's original text (checkbox prefix
	// stripped), used verbatim as the heading in the rendered sections.
	Text string
	// Kind is the item's classification.
	Kind AcceptanceItemKind
	// Commands holds the shell commands extracted from a paste-output item
	// (inline `code span` segments). Populated only when Kind ==
	// AcceptanceItemPasteOutput.
	Commands []string
	// MutationDescription is the change to apply, verbatim (the text before
	// the "->"/"→" separator). Populated only when Kind ==
	// AcceptanceItemMutation.
	MutationDescription string
	// MutationTarget is the named test or package the mutation is expected
	// to break, extracted from the text after the separator. Populated only
	// when Kind == AcceptanceItemMutation.
	MutationTarget string
}

// RequiresEvidence reports whether an item is one the completion gate must
// verify has a produced Evidence block (paste-output and mutation items);
// "other" acceptance items are never evidence-requiring.
func (i AcceptanceItem) RequiresEvidence() bool {
	return i.Kind == AcceptanceItemPasteOutput || i.Kind == AcceptanceItemMutation
}

// pasteOutputPatternRe recognises the recognised paste-output phrasings.
// Deliberately permissive (recall-oriented): any of these phrases anywhere
// in the item text is enough to classify it as paste-output, since a false
// negative here silently drops the gate for a real evidence requirement.
var pasteOutputPatternRe = regexp.MustCompile(`(?i)pasted into the pr body|paste the output|terminal output`)

// inlineCodeRe extracts inline `code span` segments, the convention used by
// every observed paste-output acceptance item ("paste the output of `go
// test -run TestX ./pkg/`").
var inlineCodeRe = regexp.MustCompile("`([^`]+)`")

// mutationArrowRe splits a mutation-style item into its change description
// and expected outcome across a "->" or "→" separator.
var mutationArrowRe = regexp.MustCompile(`(?s)^(.*?)(?:->|→)\s*(.+)$`)

// mutationCueRe requires the description half of a mutation item to
// actually describe an edit ("change …", "delete …", "remove …") —
// otherwise an unrelated item that happens to contain an arrow would be
// misclassified.
var mutationCueRe = regexp.MustCompile(`(?i)\b(change|delete|remove)\b`)

// mutationFailsRe requires the outcome half to actually assert a failure
// ("… fails", "… fail").
var mutationFailsRe = regexp.MustCompile(`(?i)\bfails?\b`)

// testNameRe extracts a Go test function name from the outcome half of a
// mutation item ("TestY fails" -> "TestY").
var testNameRe = regexp.MustCompile(`\bTest[A-Za-z0-9_]+\b`)

// packagePathRe extracts a Go package path from the outcome half of a
// mutation item ("./pkg/... fails" -> "./pkg/...").
var packagePathRe = regexp.MustCompile(`\./[A-Za-z0-9_./-]+`)

// checklistPrefixRe strips a leading "- [ ]"/"* [ ]"/"[ ]" checkbox marker
// (already-checked items are handled by the caller, which reads the
// executor's own extracted, unchecked AcceptanceCriteria list).
var checklistPrefixRe = regexp.MustCompile(`^\s*[-*]?\s*\[\s*[xX]?\s*\]\s*`)

// ClassifyAcceptanceItem classifies a single acceptance-checklist item's
// text (checkbox prefix optional — either raw issue-body text or an
// already-extracted Task.AcceptanceCriteria entry works).
func ClassifyAcceptanceItem(text string) AcceptanceItem {
	clean := strings.TrimSpace(checklistPrefixRe.ReplaceAllString(text, ""))
	item := AcceptanceItem{Text: clean, Kind: AcceptanceItemOther}

	if pasteOutputPatternRe.MatchString(clean) {
		item.Kind = AcceptanceItemPasteOutput
		item.Commands = extractInlineCommands(clean)
		return item
	}

	if desc, target, ok := extractMutation(clean); ok {
		item.Kind = AcceptanceItemMutation
		item.MutationDescription = desc
		item.MutationTarget = target
		return item
	}

	return item
}

// ParseAcceptanceItems classifies every entry in an acceptance-criteria
// list (e.g. Task.AcceptanceCriteria).
func ParseAcceptanceItems(criteria []string) []AcceptanceItem {
	items := make([]AcceptanceItem, 0, len(criteria))
	for _, c := range criteria {
		items = append(items, ClassifyAcceptanceItem(c))
	}
	return items
}

// extractInlineCommands returns the trimmed contents of every inline `code
// span` in text, in order, skipping empty spans.
func extractInlineCommands(text string) []string {
	matches := inlineCodeRe.FindAllStringSubmatch(text, -1)
	cmds := make([]string, 0, len(matches))
	for _, m := range matches {
		c := strings.TrimSpace(m[1])
		if c != "" {
			cmds = append(cmds, c)
		}
	}
	return cmds
}

// extractMutation splits a mutation-style item ("delete line 42 in foo.go
// -> TestFoo fails") into its change description and target test/package.
// ok is false when text doesn't match the mutation shape at all (no
// arrow/separator, no edit cue on the description half, or no "fails"
// assertion on the outcome half) — callers fall back to AcceptanceItemOther
// rather than guessing.
func extractMutation(text string) (desc, target string, ok bool) {
	m := mutationArrowRe.FindStringSubmatch(text)
	if m == nil {
		return "", "", false
	}
	desc = strings.TrimSpace(m[1])
	outcome := strings.TrimSpace(m[2])
	if desc == "" || !mutationCueRe.MatchString(desc) {
		return "", "", false
	}
	if !mutationFailsRe.MatchString(outcome) {
		return "", "", false
	}

	switch {
	case testNameRe.MatchString(outcome):
		target = testNameRe.FindString(outcome)
	case packagePathRe.MatchString(outcome):
		target = packagePathRe.FindString(outcome)
	default:
		target = outcome
	}
	return desc, target, true
}

// maxEvidenceOutputLines caps how many lines of a single command/test-run's
// output are embedded per fenced block in the PR body (GH-5435 spec: "cap
// per block, e.g. 60 lines").
const maxEvidenceOutputLines = 60

// trimOutputForEvidence trims output to at most maxEvidenceOutputLines
// lines, appending a truncation marker noting how many lines were dropped.
func trimOutputForEvidence(output string) string {
	output = strings.TrimRight(output, "\n")
	if output == "" {
		return ""
	}
	lines := strings.Split(output, "\n")
	if len(lines) <= maxEvidenceOutputLines {
		return strings.Join(lines, "\n")
	}
	omitted := len(lines) - maxEvidenceOutputLines
	kept := strings.Join(lines[:maxEvidenceOutputLines], "\n")
	return fmt.Sprintf("%s\n... [truncated: %d line(s) omitted]", kept, omitted)
}

// AcceptanceCommandResult is the outcome of running one paste-output
// command.
type AcceptanceCommandResult struct {
	// Command is the command that was run, verbatim.
	Command string
	// Output is the command's combined stdout+stderr, trimmed to
	// maxEvidenceOutputLines.
	Output string
	// Err is set when the command could not be executed at all (binary not
	// found, context deadline, etc.) — NOT set for a non-zero exit code,
	// which is a legitimate (and sometimes expected) command outcome that
	// still produces output worth pasting.
	Err string
}

// MutationOutcome is the outcome of applying a mutation-style acceptance
// item and running its named test/package.
type MutationOutcome struct {
	// Description is the mutation that was applied, verbatim.
	Description string
	// Target is the test name or package path that was run.
	Target string
	// Output is the trimmed combined stdout+stderr of the test run.
	Output string
	// FailingTests lists the test names the run reported as failed. Empty
	// when the mutation caused no test to fail.
	FailingTests []string
	// NoTestFailed is true when the mutation was applied and the named
	// test/package was run, but no test failed — a real finding for the
	// reviewer (GH-5435 acceptance #3: must be reported, not omitted).
	NoTestFailed bool
}

// AcceptanceEvidenceResult is the rendered outcome for one acceptance item:
// either evidence (Commands or MutationOutcome populated) or a
// NotVerifiedReason explaining why evidence could not be produced.
type AcceptanceEvidenceResult struct {
	Item              AcceptanceItem
	Commands          []AcceptanceCommandResult
	MutationOutcome   *MutationOutcome
	NotVerifiedReason string
}

// RenderAcceptanceEvidenceSections renders the "## Evidence" and
// "## Not verified" PR-body sections from a set of results. Returns "" when
// there is nothing evidence-requiring to report (results is empty, or every
// result is for a non-evidence-requiring item — which never happens since
// callers only pass evidence-requiring items in, but kept defensive).
func RenderAcceptanceEvidenceSections(results []AcceptanceEvidenceResult) string {
	var evidence, notVerified strings.Builder
	hasEvidence := false
	hasNotVerified := false

	for _, r := range results {
		if r.NotVerifiedReason != "" {
			hasNotVerified = true
			fmt.Fprintf(&notVerified, "- **%s**\n  Reason: %s\n\n", r.Item.Text, r.NotVerifiedReason)
			continue
		}

		switch r.Item.Kind {
		case AcceptanceItemPasteOutput:
			hasEvidence = true
			fmt.Fprintf(&evidence, "### %s\n\n", r.Item.Text)
			for _, c := range r.Commands {
				fmt.Fprintf(&evidence, "```\n$ %s\n%s\n```\n\n", c.Command, c.Output)
			}
		case AcceptanceItemMutation:
			hasEvidence = true
			mo := r.MutationOutcome
			fmt.Fprintf(&evidence, "### %s\n\n", r.Item.Text)
			if mo.NoTestFailed {
				fmt.Fprintf(&evidence, "Mutation applied (%s); ran `%s`; no test failed.\n\n```\n%s\n```\n\n",
					mo.Description, mo.Target, mo.Output)
			} else {
				fmt.Fprintf(&evidence, "Mutation applied (%s); failing test(s): %s\n\n```\n%s\n```\n\n",
					mo.Description, strings.Join(mo.FailingTests, ", "), mo.Output)
			}
		}
	}

	if !hasEvidence && !hasNotVerified {
		return ""
	}

	var out strings.Builder
	if hasEvidence {
		out.WriteString("## Evidence\n\n")
		out.WriteString(strings.TrimRight(evidence.String(), "\n"))
		out.WriteString("\n")
	}
	if hasNotVerified {
		if hasEvidence {
			out.WriteString("\n")
		}
		out.WriteString("## Not verified\n\n")
		out.WriteString(strings.TrimRight(notVerified.String(), "\n"))
		out.WriteString("\n")
	}
	return out.String()
}

// failLineRe matches Go test's "--- FAIL: TestName" output lines.
var failLineRe = regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`)

// extractFailingTests returns the deduplicated, first-seen-order list of
// test names Go test's output reported as failed.
func extractFailingTests(output string) []string {
	matches := failLineRe.FindAllStringSubmatch(output, -1)
	seen := make(map[string]bool, len(matches))
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	return names
}

// deleteLineRe / removeLineRe extract the file and 1-indexed line number
// from a mutation description of the shape "delete line 42 in foo.go" /
// "remove line 42 from internal/foo.go" — the deterministic mutation shape
// this gate can apply and revert without LLM assistance. Freeform mutation
// descriptions that don't match either pattern report a "could not parse"
// Not-verified reason instead of guessing at an edit.
var deleteLineRe = regexp.MustCompile(`(?i)\bdelete\s+line\s+(\d+)\b(?:\s+(?:in|from|of)\s+([^\s,]+))?`)
var removeLineRe = regexp.MustCompile(`(?i)\bremove\s+line\s+(\d+)\b(?:\s+(?:in|from|of)\s+([^\s,]+))?`)

// parseLineMutation extracts the target file and 1-indexed line number from
// a mutation description, if it matches the deterministic "delete/remove
// line N in <file>" shape.
func parseLineMutation(desc string) (file string, line int, ok bool) {
	m := deleteLineRe.FindStringSubmatch(desc)
	if m == nil {
		m = removeLineRe.FindStringSubmatch(desc)
	}
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return "", 0, false
	}
	file = strings.Trim(strings.TrimSpace(m[2]), "`'\".,;:")
	if file == "" {
		return "", 0, false
	}
	return file, n, true
}

// deleteLineFromContent returns content with its 1-indexed line removed.
func deleteLineFromContent(content string, line int) (string, error) {
	// Preserve a trailing newline exactly as found, rather than
	// normalizing it away via strings.Split/Join, so the revert produces
	// byte-identical content.
	trailingNewline := strings.HasSuffix(content, "\n")
	body := content
	if trailingNewline {
		body = content[:len(content)-1]
	}
	lines := strings.Split(body, "\n")
	if line < 1 || line > len(lines) {
		return "", fmt.Errorf("line %d out of range (file has %d lines)", line, len(lines))
	}
	idx := line - 1
	lines = append(lines[:idx], lines[idx+1:]...)
	out := strings.Join(lines, "\n")
	if trailingNewline {
		out += "\n"
	}
	return out, nil
}

// buildMutationTestCommand chooses the `go test` invocation for a mutation
// item's target: a bare Go test name runs with -run anchored to that exact
// name; a package path (leading "./") runs directly; anything else falls
// back to the whole module.
func buildMutationTestCommand(target string) string {
	target = strings.TrimSpace(target)
	switch {
	case testNameRe.MatchString(target) && !strings.ContainsAny(target, "/ "):
		return fmt.Sprintf("go test -run '^%s$' ./...", target)
	case strings.HasPrefix(target, "./"):
		return "go test " + target
	default:
		return "go test ./..."
	}
}
