package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/qf-studio/pilot/internal/typesafe"
)

// AcceptanceCommandRunner runs an evidence command in dir and returns its
// combined stdout+stderr. Injected (rather than calling exec.Command
// directly) so pattern-detection callers can unit-test the paste-output and
// mutation harnesses with a fake double instead of a real subprocess.
type AcceptanceCommandRunner interface {
	RunCommand(ctx context.Context, dir, command string) (output string, err error)
}

// acceptanceEvidenceEnvAllowlist is the exhaustive set of env var names
// forwarded into an evidence/mutation-test command's environment (GH-5437).
// PR #5436 ran commands with the daemon's full process environment — on a
// public repo, an issue-authored `env` acceptance item published every
// credential the daemon held. Everything not on this list (every API key,
// token, and cloud credential the daemon process holds) is simply absent,
// not merely hidden: cmd.Env is built from scratch, never copied from
// os.Environ().
var acceptanceEvidenceEnvAllowlist = []string{
	"PATH", "HOME", "TMPDIR", "LANG",
	"GOPATH", "GOCACHE", "GOFLAGS", "GOMODCACHE",
	"NODE_ENV", "CI",
}

// sanitizedAcceptanceEnv builds a fresh environment containing only the
// values of acceptanceEvidenceEnvAllowlist entries that are actually set in
// the daemon's own environment — never a copy of os.Environ().
func sanitizedAcceptanceEnv() []string {
	env := make([]string, 0, len(acceptanceEvidenceEnvAllowlist))
	for _, name := range acceptanceEvidenceEnvAllowlist {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// firstCommandToken returns the first whitespace-delimited token of
// command, or "" for a blank/whitespace-only command.
func firstCommandToken(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// isCommandAllowed reports whether command's first token is in allowed
// (GH-5437's command allowlist). An evidence command is only ever spawned
// when this returns true.
func isCommandAllowed(command string, allowed []string) bool {
	first := firstCommandToken(command)
	if first == "" {
		return false
	}
	for _, a := range allowed {
		if first == a {
			return true
		}
	}
	return false
}

// shellOperatorNotVerifiedReason is the Not-verified reason reported when an
// evidence command contains a shell control character or expansion sequence
// (GH-5442).
const shellOperatorNotVerifiedReason = "Not verified: shell operators are not allowed in evidence commands"

// disallowedShellOperatorBytes are the single-byte shell control characters
// GH-5442 refuses outright: `;` `&` `|` (sequencing/piping), a backtick
// (command substitution), `>` `<` (redirection), and a literal newline.
var disallowedShellOperatorBytes = []byte{';', '&', '|', '`', '>', '<', '\n'}

// disallowedShellOperatorSequences are the multi-byte expansion sequences
// GH-5442 refuses outright: `$(` (command substitution) and `${`
// (parameter expansion).
var disallowedShellOperatorSequences = []string{"$(", "${"}

// hasDisallowedShellOperators reports whether command contains any shell
// control character or expansion sequence GH-5442 refuses to run.
//
// GH-5442: isCommandAllowed only ever checked the first whitespace-delimited
// token, and the runner then handed the whole line to `sh -c` — so
// `go test ./... && echo second`, `make $(echo build)`, and
// `npm test | tee out.txt` all passed the allowlist and ran through a real
// shell, which interpreted everything after the allowlisted first token.
// This check runs before the allowlist check and before the command is ever
// tokenized, so a rejected command is never spawned.
func hasDisallowedShellOperators(command string) bool {
	for _, b := range disallowedShellOperatorBytes {
		if strings.IndexByte(command, b) >= 0 {
			return true
		}
	}
	for _, seq := range disallowedShellOperatorSequences {
		if strings.Contains(command, seq) {
			return true
		}
	}
	return hasShellCommentStart(command)
}

// hasShellCommentStart reports whether command contains a `#` that would
// start a shell comment: at the very start of command, or preceded by a
// space or tab.
func hasShellCommentStart(command string) bool {
	if strings.HasPrefix(command, "#") {
		return true
	}
	for i := 1; i < len(command); i++ {
		if command[i] != '#' {
			continue
		}
		if prev := command[i-1]; prev == ' ' || prev == '\t' {
			return true
		}
	}
	return false
}

// validateEvidenceCommand checks command against the GH-5442 shell-operator
// refusal and then the GH-5437 command allowlist, in that order, before it
// is ever tokenized or handed to the injected AcceptanceCommandRunner.
// Returns "" when command is clear to run, or the Not-verified reason to
// report instead.
func validateEvidenceCommand(command string, allowed []string) string {
	if hasDisallowedShellOperators(command) {
		return shellOperatorNotVerifiedReason
	}
	if !isCommandAllowed(command, allowed) {
		return "command not in allowlist"
	}
	return ""
}

// splitCommandFields splits command into argv fields the way a POSIX shell
// would for a simple word list: whitespace-separated tokens, with single-
// and double-quoted spans kept intact and their quotes stripped (so
// `go test -run '^TestMain$' ./...` keeps `^TestMain$` as one argument).
// Deliberately not a full shell parser: no variable/command expansion, no
// globbing, and no backslash escapes are interpreted — GH-5442 already
// refuses any command containing an expansion sequence or control character
// via hasDisallowedShellOperators before this ever runs, so the only syntax
// left to understand is quoting and whitespace.
func splitCommandFields(command string) ([]string, error) {
	var fields []string
	var current strings.Builder
	hasCurrent := false
	var quote byte

	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				current.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
			hasCurrent = true
		case c == ' ' || c == '\t':
			if hasCurrent {
				fields = append(fields, current.String())
				current.Reset()
				hasCurrent = false
			}
		default:
			current.WriteByte(c)
			hasCurrent = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote in command", quote)
	}
	if hasCurrent {
		fields = append(fields, current.String())
	}
	return fields, nil
}

// acceptanceCommandTimeoutError is returned by shellAcceptanceCommandRunner
// when a command is killed for exceeding its per-command timeout, so
// callers can report the GH-5437-specified "timed out after <d>" reason
// verbatim instead of a generic execution-error wrapper.
type acceptanceCommandTimeoutError struct {
	timeout time.Duration
}

func (e *acceptanceCommandTimeoutError) Error() string {
	return fmt.Sprintf("timed out after %s", e.timeout)
}

// shellAcceptanceCommandRunner is the real AcceptanceCommandRunner, used in
// production. Commands are the issue author's own acceptance-criteria text
// (untrusted — GH-5437 is precisely about not trusting it), so unlike
// pre-GH-5437 this runner (a) only ever receives commands the caller has
// already validated (GH-5442: no shell operators, GH-5437: allowlisted
// first token), (b) never inherits the daemon's process environment, and
// (c) enforces its own timeout independent of the task's remaining context
// budget. Its name predates GH-5442 (no shell is involved any more) but is
// kept to avoid a mechanical rename across every call site.
type shellAcceptanceCommandRunner struct {
	// timeout bounds a single command's runtime. <= 0 falls back to
	// defaultAcceptanceEvidenceCommandTimeout.
	timeout time.Duration
}

func (r shellAcceptanceCommandRunner) RunCommand(ctx context.Context, dir, command string) (string, error) {
	// GH-5442: no shell. Commands used to run through `sh -c`, so an
	// allowlisted first token ("go") with a shell operator anywhere else in
	// the line ("go test ./... && echo second") was interpreted by the
	// shell. Callers already refuse operator-bearing commands before this
	// is ever invoked (validateEvidenceCommand); splitting into argv here
	// and exec'ing directly means even a command that slipped past that
	// check would be spawned as a single literal argument list, never
	// interpreted.
	argv, err := splitCommandFields(command)
	if err != nil {
		return "", err
	}
	if len(argv) == 0 {
		return "", errors.New("empty command")
	}

	timeout := r.timeout
	if timeout <= 0 {
		timeout = defaultAcceptanceEvidenceCommandTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...) // #nosec G204 -- argv[0] was already allowlist-checked by the caller, and no shell is invoked, so no operator or expansion sequence in the remaining arguments can be interpreted.
	cmd.Dir = dir
	cmd.Env = sanitizedAcceptanceEnv()

	// GH-5437: own process group so a timeout (or outer ctx cancellation)
	// reaches every child the command forks, not just the tracked "sh" PID
	// — same fix class as GH-4503 elsewhere in this package.
	configureProcessGroup(cmd)
	cmd.Cancel = func() error {
		return killProcessGroup(cmd, syscall.SIGKILL)
	}

	out, err := cmd.CombinedOutput()
	if cctx.Err() == context.DeadlineExceeded {
		return string(out), &acceptanceCommandTimeoutError{timeout: timeout}
	}
	return string(out), err
}

// finalizeEvidenceOutput trims output to the PR-body line cap and then
// redacts it (GH-5437) — the last step before any captured output is
// embedded.
func finalizeEvidenceOutput(output string) string {
	return redactOutputForEvidence(trimOutputForEvidence(output))
}

// runPasteOutputItem runs every command extracted from a paste-output
// acceptance item and captures its trimmed, redacted output.
//
// GH-5437: every command is checked against allowedCommands before this
// function ever calls runner.RunCommand — a disallowed command aborts the
// whole item as Not-verified without spawning anything (not just the
// disallowed command; the item's other commands are skipped too, so a
// single injected `env` can't hide behind a legitimate `go test` in the
// same acceptance item).
//
// An item with zero parsed commands, or whose every allowed command hit a
// real execution error (binary missing, timeout — as opposed to a non-zero
// exit code, which is a legitimate result worth pasting), is reported as
// Not-verified rather than silently dropped.
func runPasteOutputItem(ctx context.Context, runner AcceptanceCommandRunner, dir string, item AcceptanceItem, allowedCommands []string) AcceptanceEvidenceResult {
	result := AcceptanceEvidenceResult{Item: item}

	if len(item.Commands) == 0 {
		result.NotVerifiedReason = "no commands parsed from acceptance item (expected an inline `command`)"
		return result
	}

	for _, command := range item.Commands {
		if reason := validateEvidenceCommand(command, allowedCommands); reason != "" {
			result.NotVerifiedReason = reason
			return result
		}
	}

	var lastErr error
	allErrored := true
	for _, command := range item.Commands {
		out, err := runner.RunCommand(ctx, dir, command)
		cr := AcceptanceCommandResult{Command: command, Output: finalizeEvidenceOutput(out)}
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			cr.Err = redactOutputForEvidence(err.Error())
			lastErr = err
		} else {
			allErrored = false
		}
		result.Commands = append(result.Commands, cr)
	}

	if allErrored {
		var te *acceptanceCommandTimeoutError
		if errors.As(lastErr, &te) {
			result.NotVerifiedReason = te.Error()
		} else {
			result.NotVerifiedReason = fmt.Sprintf("execution error running %q: %s", item.Commands[0], result.Commands[0].Err)
		}
	}
	return result
}

// runMutationItem applies a mutation-style acceptance item to the worktree
// file it names, runs the target test/package, records the outcome, and
// reverts the file byte-for-byte. Only the deterministic "delete/remove
// line N in <file>" mutation shape is applied automatically; anything else
// (GH-5438: classification no longer requires an edit-cue verb, so items
// like "drop …", "replace …", "swap …", "disable …", "move … -> TestX
// fails" now reach here as AcceptanceItemMutation too) is reported as
// Not-verified with reason "freeform mutation, run manually", per GH-5435's
// "record the gap, never omit it" requirement.
//
// GH-5437: the target file is resolved through resolveWorktreeConfinedPath
// before anything is read or written — a relative "..", an absolute path,
// or a symlink inside the worktree pointing outside it is refused with
// reason "path escapes worktree" and no filesystem access at all. The test
// command is subject to the same command allowlist as paste-output items
// (a go/vitest/pytest/cargo invocation from buildMutationTestCommand, chosen by
// detectMutationToolchain (GH-5625), so this only bites when an operator's
// allowed_commands excludes that runner).
// The file is never left mutated on disk: a read/parse/escape failure
// aborts before any write, and every path past the write — including a
// killed-by-timeout test run — reverts before returning.
func runMutationItem(ctx context.Context, runner AcceptanceCommandRunner, dir string, item AcceptanceItem, allowedCommands []string) AcceptanceEvidenceResult {
	result := AcceptanceEvidenceResult{Item: item}

	file, line, ok := parseLineMutation(item.MutationDescription)
	if !ok {
		// GH-5438: the item's full text (item.Text) is what
		// RenderAcceptanceEvidenceSections lists under "## Not verified" —
		// this reason is deliberately terse since the text alongside it
		// already shows the reviewer exactly what wasn't verified.
		result.NotVerifiedReason = "freeform mutation, run manually"
		return result
	}

	fullPath, err := resolveWorktreeConfinedPath(dir, file)
	if errors.Is(err, errPathEscapesWorktree) {
		result.NotVerifiedReason = "path escapes worktree"
		return result
	}
	if err != nil {
		result.NotVerifiedReason = fmt.Sprintf("could not read %s to apply mutation: %v", file, err)
		return result
	}

	original, err := os.ReadFile(fullPath) // #nosec G304 -- fullPath was confined to dir by resolveWorktreeConfinedPath above.
	if err != nil {
		result.NotVerifiedReason = fmt.Sprintf("could not read %s to apply mutation: %v", file, err)
		return result
	}

	mutated, err := deleteLineFromContent(string(original), line)
	if err != nil {
		result.NotVerifiedReason = fmt.Sprintf("could not apply mutation to %s: %v", file, err)
		return result
	}

	// GH-5625: pick the test command for the repo's toolchain before touching
	// the file, so an unsupported repo records the gap with the tree untouched.
	toolchain := detectMutationToolchain(dir)
	testCmd := buildMutationTestCommand(item.MutationTarget, toolchain, detectJSRunnerPrefix(dir))
	if testCmd == "" {
		result.NotVerifiedReason = noMutationToolchainReason
		return result
	}

	if err := os.WriteFile(fullPath, []byte(mutated), 0o600); err != nil {
		result.NotVerifiedReason = fmt.Sprintf("could not write mutated %s: %v", file, err)
		return result
	}

	var output string
	var testErr error
	if reason := validateEvidenceCommand(testCmd, allowedCommands); reason == "" {
		output, testErr = runner.RunCommand(ctx, dir, testCmd)
	} else {
		testErr = errors.New(reason)
	}

	// Always attempt to revert, whatever the run above returned — never
	// leave the mutated content on disk.
	if err := os.WriteFile(fullPath, original, 0o600); err != nil {
		result.NotVerifiedReason = fmt.Sprintf(
			"reverting mutation to %s failed: %v (worktree may be left dirty; original content was not restored)", file, err,
		)
		return result
	}

	var te *acceptanceCommandTimeoutError
	if errors.As(testErr, &te) {
		result.NotVerifiedReason = te.Error()
		return result
	}
	var exitErr *exec.ExitError
	if testErr != nil && !errors.As(testErr, &exitErr) {
		// A real harness failure (binary missing, not allowlisted) — not
		// the expected/success case of the target test failing.
		result.NotVerifiedReason = fmt.Sprintf("could not run %q: %v", testCmd, testErr)
		return result
	}

	trimmed := finalizeEvidenceOutput(output)
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
// has evidence-requiring acceptance criteria, and/or the GH-5466
// diff-coverage check (below) finds a path the issue names but the PR's
// diff doesn't touch. Called once per PR-body-assembly site, right before
// the corresponding CreatePR call, with workDir set to the same worktree
// the branch was pushed from (git.ProjectPath()).
//
// Returns prBody unchanged (byte-identical) when the gate is disabled, or
// when task has no acceptance criteria AND the diff-coverage check finds
// nothing to report — this is the flag-off / no-op contract GH-5435
// requires, extended to cover the new check without weakening it: a task
// with acceptance criteria but no named Change/Acceptance-section paths
// still produces a byte-identical result when nothing else is reported,
// since runDiffCoverageCheck below shells out to git only when
// ExtractDiffCoveragePaths finds a candidate path at all.
func (r *Runner) appendAcceptanceEvidence(ctx context.Context, task *Task, workDir, prBody string) string {
	if r == nil || task == nil {
		return prBody
	}

	var cfg *AcceptanceEvidenceConfig
	if r.config != nil {
		cfg = r.config.AcceptanceEvidence
	}
	if !cfg.IsEnabled() {
		return prBody
	}

	var results []AcceptanceEvidenceResult
	if len(task.AcceptanceCriteria) > 0 {
		var runner AcceptanceCommandRunner = shellAcceptanceCommandRunner{timeout: cfg.EffectiveCommandTimeout()}
		if r.acceptanceRunner != nil {
			runner = r.acceptanceRunner
		}
		var classifier AcceptanceClassifier = regexAcceptanceClassifier{}
		if r.acceptanceClassifier != nil {
			classifier = r.acceptanceClassifier
		}
		items, stats := classifier.Classify(ctx, task.AcceptanceCriteria)
		r.logAcceptanceClassification(task, stats)
		results = RunAcceptanceEvidenceItems(ctx, runner, workDir, items, cfg.EffectiveAllowedCommands())
	}

	// GH-5466: merged into the same "## Not verified" section as the
	// classified acceptance items above, appended after them — a reviewer
	// scanning "Not verified" shouldn't have to check two separate PR-body
	// sections for the same class of gap. Reusing
	// RenderAcceptanceEvidenceSections unchanged (rather than rendering a
	// distinct section) keeps every existing acceptance-evidence test
	// green, since that function's behavior and tests are untouched by this
	// change.
	results = append(results, runDiffCoverageCheck(ctx, r, task, workDir)...)

	section := RenderAcceptanceEvidenceSections(results)
	if section == "" {
		return prBody
	}
	return strings.TrimRight(prBody, "\n") + "\n\n" + section
}

// logAcceptanceClassification emits the one per-task info line whose counters
// (agreed / overrode / low_confidence / errors) decide the shadow-to-live flip.
// In shadow mode overrode counts would-be overrides.
func (r *Runner) logAcceptanceClassification(task *Task, stats AcceptanceClassifyStats) {
	if r == nil || r.log == nil {
		return
	}
	r.log.Info("Acceptance classification",
		slog.String("task_id", task.ID),
		slog.String("classifier", stats.Classifier),
		slog.Int("items", stats.Items),
		slog.Int("regex_only", stats.RegexOnly),
		slog.Int("agreed", stats.Agreed),
		slog.Int("overrode", stats.Overrode),
		slog.Int("low_confidence", stats.LowConfidence),
		slog.Int("errors", stats.Errors),
		slog.Bool("shadow", stats.Shadow),
		slog.Int64("latency_ms", stats.Latency.Milliseconds()),
	)
	// One info line per item so low_confidence items can be labelled. The text
	// is the redacted, capped text sent to the API, never the raw item.
	for i, d := range stats.Details {
		var reason, wouldBe typesafe.Reason
		if i < len(stats.Reasons) {
			reason = stats.Reasons[i]
		}
		if i < len(stats.WouldBe) {
			wouldBe = stats.WouldBe[i]
		}
		r.log.Info("Acceptance classification item",
			slog.String("task_id", task.ID),
			slog.Int("index", i+1),
			slog.String("regex_kind", string(d.RegexKind)),
			slog.String("jev_choice", d.JevChoice),
			slog.Float64("confidence", d.Confidence),
			slog.String("reason", string(reason)),
			slog.String("would_be", string(wouldBe)),
			slog.String("text", d.Text),
		)
	}
}

// runDiffCoverageCheck runs the GH-5466 diff-coverage check against task's
// issue body and the PR branch's actual diff, returning one synthetic
// AcceptanceEvidenceResult (NotVerifiedReason set, Item.Text the bare path)
// per uncovered path — shaped so RenderAcceptanceEvidenceSections lists
// each one under "## Not verified" exactly like a classified acceptance
// item that couldn't be verified.
//
// Returns nil without any git shellout when task.Description (the queue-time
// snapshot) names no candidate paths at all (ExtractDiffCoveragePaths
// empty) — this keeps the flag-off / no-acceptance-criteria / nil-task
// byte-identical PR-body tests git-free, since workDir there is not always
// even a git repository. The snapshot — not the live body — gates this
// short-circuit deliberately: fetching the live body first would mean
// resolveLiveIssueBody's git-remote-resolution shellout runs even for a
// workDir that was never meant to see git at all.
//
// GH-5469: once that cheap gate passes, the body actually checked against
// the diff is resolved via resolveLiveIssueBody (issue_state.go) — the same
// "live body, fall back to snapshot on fetch failure or empty body"
// resolution the dispatcher's base-presence revalidation uses (GH-5193) —
// rather than task.Description directly. Before this fix, a path an operator
// corrected or removed from the issue body after dispatch was still
// reported as "named in the issue but the PR does not modify it", the same
// class of bug GH-5193 already fixed for base-presence.
//
// Fails open on any git error (unresolvable merge-base, no such branch,
// worktree not yet pushed): a diff-coverage probe failure must never block
// or corrupt PR-body assembly, matching base_presence.go's
// "pipeline availability outranks the guard" stance for this same class of
// git/GitHub probe.
func runDiffCoverageCheck(ctx context.Context, runner *Runner, task *Task, workDir string) []AcceptanceEvidenceResult {
	if task == nil {
		return nil
	}
	paths := ExtractDiffCoveragePaths(task.Description)
	if len(paths) == 0 {
		return nil
	}

	body := task.Description
	if task.SourceAdapter == "" || task.SourceAdapter == "github" {
		resolved, _, source, err := resolveLiveIssueBody(ctx, runner, task, workDir, task.Description)
		body = resolved
		if runner != nil && runner.log != nil {
			runner.log.Debug("diff-coverage check: resolved issue body",
				slog.String("task_id", task.ID),
				slog.String("source", string(source)),
				slog.Any("fetch_error", err),
			)
		}
	}

	baseBranch := task.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	git := NewGitOperations(workDir)
	changedFiles, baseSHA, err := git.ChangedFilesAgainstOrigin(ctx, baseBranch)
	if err != nil {
		return nil
	}

	uncovered := CheckDiffCoverage(body, changedFiles, func(path string) bool {
		return git.FileExistsAtRef(ctx, baseSHA, path)
	})

	results := make([]AcceptanceEvidenceResult, 0, len(uncovered))
	for _, path := range uncovered {
		results = append(results, AcceptanceEvidenceResult{
			Item:              AcceptanceItem{Text: path, Kind: AcceptanceItemOther},
			NotVerifiedReason: DiffCoverageNotVerifiedReason(path),
		})
	}
	return results
}

// RunAcceptanceEvidence classifies criteria and runs every evidence-
// requiring item (paste-output and mutation) against dir, returning one
// AcceptanceEvidenceResult per evidence-requiring item in encounter order.
// "Other" (non-evidence-requiring) items are skipped entirely — this
// function's return value is exactly the set RenderAcceptanceEvidenceSections
// and the completion gate need.
func RunAcceptanceEvidence(ctx context.Context, runner AcceptanceCommandRunner, dir string, criteria []string, allowedCommands []string) []AcceptanceEvidenceResult {
	return RunAcceptanceEvidenceItems(ctx, runner, dir, ParseAcceptanceItems(criteria), allowedCommands)
}

// RunAcceptanceEvidenceItems is RunAcceptanceEvidence for items that were
// already classified (by the regex floor or an AcceptanceClassifier). It runs
// every evidence-requiring item against dir in encounter order and skips
// "other" items.
func RunAcceptanceEvidenceItems(ctx context.Context, runner AcceptanceCommandRunner, dir string, items []AcceptanceItem, allowedCommands []string) []AcceptanceEvidenceResult {
	results := make([]AcceptanceEvidenceResult, 0, len(items))
	for _, item := range items {
		switch item.Kind {
		case AcceptanceItemPasteOutput:
			results = append(results, runPasteOutputItem(ctx, runner, dir, item, allowedCommands))
		case AcceptanceItemMutation:
			results = append(results, runMutationItem(ctx, runner, dir, item, allowedCommands))
		}
	}
	return results
}
