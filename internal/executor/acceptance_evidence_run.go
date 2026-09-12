package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AcceptanceCommandRunner runs a shell command in dir and returns its
// combined stdout+stderr. Injected (rather than calling exec.Command
// directly) so pattern-detection callers can unit-test the paste-output and
// mutation harnesses with a fake double instead of a real shell.
type AcceptanceCommandRunner interface {
	RunCommand(ctx context.Context, dir, command string) (output string, err error)
}

// shellAcceptanceCommandRunner is the real AcceptanceCommandRunner, used in
// production. Commands are the issue author's own acceptance-criteria text
// (author-controlled, run inside the task's own worktree — the same trust
// boundary as every other command Pilot already executes there, e.g.
// quality gates and lint fixes).
type shellAcceptanceCommandRunner struct{}

func (shellAcceptanceCommandRunner) RunCommand(ctx context.Context, dir, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runPasteOutputItem runs every command extracted from a paste-output
// acceptance item and captures its trimmed output. An item with zero
// parsed commands, or whose every command hit a real execution error
// (binary missing, context deadline — as opposed to a non-zero exit code,
// which is a legitimate result worth pasting), is reported as
// Not-verified rather than silently dropped.
func runPasteOutputItem(ctx context.Context, runner AcceptanceCommandRunner, dir string, item AcceptanceItem) AcceptanceEvidenceResult {
	result := AcceptanceEvidenceResult{Item: item}

	if len(item.Commands) == 0 {
		result.NotVerifiedReason = "no commands parsed from acceptance item (expected an inline `command`)"
		return result
	}

	allErrored := true
	for _, command := range item.Commands {
		out, err := runner.RunCommand(ctx, dir, command)
		cr := AcceptanceCommandResult{Command: command, Output: trimOutputForEvidence(out)}
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			cr.Err = err.Error()
		} else {
			allErrored = false
		}
		result.Commands = append(result.Commands, cr)
	}

	if allErrored {
		result.NotVerifiedReason = fmt.Sprintf("execution error running %q: %s", item.Commands[0], result.Commands[0].Err)
	}
	return result
}

// runMutationItem applies a mutation-style acceptance item to a scratch
// copy of the worktree file it names, runs the target test/package,
// records the outcome, and reverts the file byte-for-byte. Only the
// deterministic "delete/remove line N in <file>" mutation shape is applied
// automatically; anything else (freeform "change X to Y" descriptions that
// don't name a parseable file+line edit) is reported as Not-verified with
// the reason, per GH-5435's "record the gap, never omit it" requirement.
//
// The file is never left mutated on disk: a read error or parse failure
// aborts before any write, and a write failure while reverting is reported
// as Not-verified (never silently passed) rather than proceeding with a
// dirty worktree.
func runMutationItem(ctx context.Context, runner AcceptanceCommandRunner, dir string, item AcceptanceItem) AcceptanceEvidenceResult {
	result := AcceptanceEvidenceResult{Item: item}

	file, line, ok := parseLineMutation(item.MutationDescription)
	if !ok {
		result.NotVerifiedReason = fmt.Sprintf(
			"mutation %q does not match a recognised deterministic edit (\"delete/remove line N in <file>\"); freeform mutations require manual verification",
			item.MutationDescription,
		)
		return result
	}

	fullPath := filepath.Join(dir, file)
	original, err := os.ReadFile(fullPath) // #nosec G304 -- fullPath is joined from the task's own worktree dir + an issue-author-supplied relative path, the same trust boundary as every other file the executor edits in that worktree.
	if err != nil {
		result.NotVerifiedReason = fmt.Sprintf("could not read %s to apply mutation: %v", file, err)
		return result
	}

	mutated, err := deleteLineFromContent(string(original), line)
	if err != nil {
		result.NotVerifiedReason = fmt.Sprintf("could not apply mutation to %s: %v", file, err)
		return result
	}

	if err := os.WriteFile(fullPath, []byte(mutated), 0o600); err != nil {
		result.NotVerifiedReason = fmt.Sprintf("could not write mutated %s: %v", file, err)
		return result
	}

	testCmd := buildMutationTestCommand(item.MutationTarget)
	output, _ := runner.RunCommand(ctx, dir, testCmd) // non-zero exit (the test failing) is the expected/success case here, not a harness error.

	// Always attempt to revert, even though the run above can't itself fail
	// the harness — never leave the mutated content on disk.
	if err := os.WriteFile(fullPath, original, 0o600); err != nil {
		result.NotVerifiedReason = fmt.Sprintf(
			"reverting mutation to %s failed: %v (worktree may be left dirty; original content was not restored)", file, err,
		)
		return result
	}

	trimmed := trimOutputForEvidence(output)
	failing := extractFailingTests(output)
	mo := &MutationOutcome{
		Description: item.MutationDescription,
		Target:      item.MutationTarget,
		Output:      trimmed,
	}
	if len(failing) == 0 {
		mo.NoTestFailed = true
	} else {
		mo.FailingTests = failing
	}
	result.MutationOutcome = mo
	return result
}

// appendAcceptanceEvidence appends the "## Evidence" / "## Not verified"
// sections to prBody when the acceptance-evidence gate is enabled and task
// has evidence-requiring acceptance criteria (GH-5435). Called once per PR-
// body-assembly site, right before the corresponding CreatePR call, with
// workDir set to the same worktree the branch was pushed from (git.ProjectPath()).
//
// Returns prBody unchanged (byte-identical) when the gate is disabled, when
// task has no acceptance criteria, or when none of them are evidence-
// requiring — this is the flag-off / no-op contract GH-5435 requires.
func (r *Runner) appendAcceptanceEvidence(ctx context.Context, task *Task, workDir, prBody string) string {
	if r == nil || task == nil || len(task.AcceptanceCriteria) == 0 {
		return prBody
	}
	if r.config != nil && !r.config.AcceptanceEvidence.IsEnabled() {
		return prBody
	}

	results := RunAcceptanceEvidence(ctx, shellAcceptanceCommandRunner{}, workDir, task.AcceptanceCriteria)
	section := RenderAcceptanceEvidenceSections(results)
	if section == "" {
		return prBody
	}
	return strings.TrimRight(prBody, "\n") + "\n\n" + section
}

// RunAcceptanceEvidence classifies criteria and runs every evidence-
// requiring item (paste-output and mutation) against dir, returning one
// AcceptanceEvidenceResult per evidence-requiring item in encounter order.
// "Other" (non-evidence-requiring) items are skipped entirely — this
// function's return value is exactly the set RenderAcceptanceEvidenceSections
// and the completion gate need.
func RunAcceptanceEvidence(ctx context.Context, runner AcceptanceCommandRunner, dir string, criteria []string) []AcceptanceEvidenceResult {
	items := ParseAcceptanceItems(criteria)
	results := make([]AcceptanceEvidenceResult, 0, len(items))
	for _, item := range items {
		switch item.Kind {
		case AcceptanceItemPasteOutput:
			results = append(results, runPasteOutputItem(ctx, runner, dir, item))
		case AcceptanceItemMutation:
			results = append(results, runMutationItem(ctx, runner, dir, item))
		}
	}
	return results
}
