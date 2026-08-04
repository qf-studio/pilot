// Package ghguard — GH-4671
//
// Durable/preventive half of the GH-4649 containment pair (the detective
// half, executor.sideeffect_audit's post-run search, is GH-4670 and already
// merged). GH-4649 incident: an executor session improvised `gh issue close`
// plus a `pilot-superseded` label on a SIBLING issue mid-run, entirely inside
// its own Bash tool calls — nothing upstream of the shell had a chance to
// stop it. GH-4670 can only detect that after the fact. This package is the
// policy core for actually intercepting `gh` invocations before they reach
// GitHub.
//
// Design: at subprocess spawn (see executor.setupGhGuardShim in
// ghguard_spawn.go) a per-execution directory containing a `gh` shim script
// is prepended to the child's PATH. The shim re-execs the Pilot binary as
// `pilot gh-guard -- <original argv>` (cmd/pilot/ghguard.go), which parses
// argv with this package's Classify — argv, never a shell string, so there
// is no quoting/injection surface — and either execs the REAL `gh` (resolved
// once at daemon start, see backend_factory.go) or refuses and explains why.
//
// Everything in this file is a pure function of (Identity, argv) so the
// policy is exhaustively table-testable without spawning anything (see
// policy_test.go). The rule table (allowRules below) is intentionally one
// slice of structs — extending the allowlist should be a one-line diff.
//
// Precedent: this mirrors the RepoAllowlist / ValidateTargetRepo shape from
// executor.repo_guardrail (GH-3027/TASK-286) — a narrow, consumer-side,
// data-driven allowlist with a loud, logged bypass rather than a silent one.
package ghguard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Env var names used to pass task identity and wiring from the spawning
// Pilot process (executor.setupGhGuardShim) down to the `pilot gh-guard`
// invocation (cmd/pilot/ghguard.go). Exported so both sides reference the
// same constants instead of duplicating string literals.
const (
	// EnvTaskIssue is the issue number (as a string, no "#") the running
	// execution was dispatched for. May be empty for non-issue-sourced tasks.
	EnvTaskIssue = "PILOT_TASK_ISSUE"

	// EnvTaskRepo is "owner/repo" for the task's source repo.
	EnvTaskRepo = "PILOT_TASK_REPO"

	// EnvTaskBranch is the branch the execution is working on (the PR head
	// once one exists).
	EnvTaskBranch = "PILOT_TASK_BRANCH"

	// EnvRealGh is the absolute path to the real `gh` binary, resolved once
	// at daemon start (see backend_factory.go) so the shim never has to
	// re-discover it (and risk finding itself).
	EnvRealGh = "PILOT_GH_REAL"

	// EnvShimDir is the absolute path of the per-execution shim directory
	// that was prepended to PATH. ResolveFallbackGh excludes this directory
	// when EnvRealGh is unset, so the fallback PATH search cannot recurse
	// into the shim it is trying to work around.
	EnvShimDir = "PILOT_GH_GUARD_SHIM_DIR"

	// EnvJournalPath is the absolute path of the JSONL journal file that
	// denied (and, best-effort, allowed) calls are appended to. Read by the
	// executor after the subprocess exits and surfaced as
	// BackendResult.GhGuardDenials; also the evidence trail the GH-4670
	// audit's alert channel references.
	EnvJournalPath = "PILOT_GH_GUARD_JOURNAL"
)

// Verdict is the outcome of classifying one `gh` invocation.
type Verdict string

const (
	VerdictAllow Verdict = "allow"
	VerdictDeny  Verdict = "deny"
)

// Identity is the task context the guard evaluates each `gh` call against.
// Built from the Env* vars above. Zero-value fields simply fail to satisfy
// the checks that need them (e.g. an empty TaskRepo means -R/--repo
// mismatches can never be positively confirmed, so those checks are skipped
// rather than guessed at — the default-deny allowlist below still prevents
// this being unsafe: unmatched commands don't reach an identity check at
// all, they're just denied).
type Identity struct {
	TaskIssue  string
	TaskRepo   string
	TaskBranch string
	RealGh     string
}

// Decision is the result of Classify: whether to allow the call, and (on
// deny) a one-line reason plus a hint of what IS allowed, so the executor
// session sees actionable stderr instead of a bare failure.
type Decision struct {
	Verdict Verdict
	Reason  string
	Allowed string // populated on deny only
}

// ruleKind distinguishes the three ways an allowlisted command is verified.
type ruleKind int

const (
	// kindRead is unconditionally allowed once the -R/--repo check (if any
	// -R/--repo was given) passes — no issue/PR number match required,
	// since read access to any issue/PR in the task's own repo is
	// legitimate context-gathering, not a mutation risk.
	kindRead ruleKind = iota

	// kindAPIRead is `gh api`: allowed only when the call is a GET (no
	// -X/--method other than GET, and no data-carrying flag).
	kindAPIRead

	// kindOwnArtifact is allowed only when the call additionally targets
	// the task's own issue/branch (see checkOwnArtifact).
	kindOwnArtifact
)

type rule struct {
	command string // "issue", "pr", "run", "auth", "api"
	sub     string // "view", "list", ... ("" for api, which has no subcommand)
	kind    ruleKind
}

// allowRules is the entire policy allowlist. Everything not matched here is
// denied by default — extending the allowlist is a one-line addition here
// plus, if it's a new kindOwnArtifact case, a branch in checkOwnArtifact.
var allowRules = []rule{
	{"issue", "view", kindRead},
	{"issue", "list", kindRead},
	{"pr", "view", kindRead},
	{"pr", "list", kindRead},
	{"pr", "checks", kindRead},
	{"pr", "diff", kindRead},
	{"run", "view", kindRead},
	{"run", "list", kindRead},
	{"auth", "status", kindRead},
	{"api", "", kindAPIRead},
	{"pr", "create", kindOwnArtifact},
	{"issue", "comment", kindOwnArtifact},
	{"pr", "comment", kindOwnArtifact},
}

// hardDenyCommands are top-level `gh` commands that are never allowed,
// regardless of flags — checked before the allowlist so the deny reason is
// specific rather than a generic "not in allowlist".
var hardDenyCommands = map[string]bool{
	"release":  true,
	"secret":   true,
	"variable": true,
	"workflow": true,
}

// hardDenyIssueSubs are `gh issue <sub>` forms that mutate issue lifecycle
// state and are never allowed, for any issue number.
var hardDenyIssueSubs = map[string]bool{
	"close":    true,
	"reopen":   true,
	"edit":     true,
	"lock":     true,
	"unlock":   true,
	"transfer": true,
	"delete":   true,
	"pin":      true,
	"unpin":    true,
	"develop":  true,
}

// valueFlags lists `gh` flags that consume the following token as their
// value (when not given in --flag=value form). This is a conservative,
// hand-maintained list covering every flag used by the commands in
// allowRules plus the common mutation flags this guard must recognize
// (-R/--repo, -X/--method, -f/-F/--input, --add-label/--remove-label,
// --head). Flags not listed here are treated as boolean (no value
// consumed) — the effect of a misclassification here is at worst an
// incorrect positional-argument split, and every code path in this package
// defaults to deny when it cannot confirm an allow, so an unrecognized flag
// biases toward safety, not away from it.
var valueFlags = map[string]bool{
	"-R": true, "--repo": true,
	"-X": true, "--method": true,
	"-f": true, "--raw-field": true,
	"-F": true, "--field": true,
	"--input": true,
	"-H":      true, "--header": true,
	"--hostname": true,
	"-q":         true, "--jq": true,
	"-t": true, "--template": true,
	"--json": true, "--limit": true,
	"-s": true, "--state": true,
	"-a": true, "--assignee": true,
	"-A": true, "--author": true,
	"-l": true, "--label": true,
	"--add-label": true, "--remove-label": true,
	"-S": true, "--search": true,
	"--title": true,
	"-b":      true, "--body": true,
	"--body-file": true,
	"-B":          true, "--base": true,
	"--head": true,
	"-m":     true, "--milestone": true,
	"-p": true, "--project": true,
	"--reviewer": true,
	"--branch":   true,
	"--workflow": true,
	"--status":   true,
}

// parsedArgs is the result of a single, conservative pass over `gh` argv.
// It is never a full re-implementation of gh's flag grammar — only what
// Classify needs to decide.
type parsedArgs struct {
	command        string
	sub            string
	positional     []string
	repo           string
	repoGiven      bool
	method         string
	hasDataFlag    bool
	hasAddLabel    bool
	hasRemoveLabel bool
	head           string
	headGiven      bool
}

// parseArgs scans gh's argv (excluding the "gh" program name itself) into a
// parsedArgs. It never invokes a shell — this is a plain argv walk.
func parseArgs(args []string) parsedArgs {
	var p parsedArgs
	if len(args) == 0 {
		return p
	}
	p.command = args[0]
	i := 1
	if p.command != "api" && i < len(args) && !strings.HasPrefix(args[i], "-") {
		p.sub = args[i]
		i++
	}
	for ; i < len(args); i++ {
		tok := args[i]
		if !strings.HasPrefix(tok, "-") {
			p.positional = append(p.positional, tok)
			continue
		}
		name := tok
		val := ""
		hasVal := false
		if eq := strings.Index(tok, "="); eq >= 0 {
			name = tok[:eq]
			val = tok[eq+1:]
			hasVal = true
		}
		switch name {
		case "-R", "--repo":
			if !hasVal && i+1 < len(args) {
				i++
				val = args[i]
			}
			p.repo = val
			p.repoGiven = true
		case "-X", "--method":
			if !hasVal && i+1 < len(args) {
				i++
				val = args[i]
			}
			p.method = val
		case "-f", "--raw-field", "-F", "--field", "--input":
			p.hasDataFlag = true
			if !hasVal && i+1 < len(args) {
				i++
			}
		case "--add-label":
			p.hasAddLabel = true
			if !hasVal && i+1 < len(args) {
				i++
			}
		case "--remove-label":
			p.hasRemoveLabel = true
			if !hasVal && i+1 < len(args) {
				i++
			}
		case "--head":
			if !hasVal && i+1 < len(args) {
				i++
				val = args[i]
			}
			p.head = val
			p.headGiven = true
		default:
			if valueFlags[name] && !hasVal && i+1 < len(args) {
				i++
			}
		}
	}
	return p
}

// extractIssueOrPRNumber parses a bare number ("42") or a GitHub issue/PR
// URL ("https://github.com/o/r/issues/42", ".../pull/42") into its number.
// Returns ok=false for anything else (e.g. a branch name), which callers
// treat as "unverifiable" rather than guessing.
func extractIssueOrPRNumber(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if isAllDigits(s) {
		return s, true
	}
	if strings.Contains(s, "/issues/") || strings.Contains(s, "/pull/") {
		tail := strings.TrimRight(s, "/")
		if idx := strings.LastIndex(tail, "/"); idx >= 0 {
			tail = tail[idx+1:]
		}
		if q := strings.IndexAny(tail, "?#"); q >= 0 {
			tail = tail[:q]
		}
		if isAllDigits(tail) {
			return tail, true
		}
	}
	return "", false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Classify is the pure policy core: given the task's Identity and the `gh`
// argv (excluding the "gh" program name — e.g. []string{"issue", "view",
// "42"}), decide whether the call may proceed. Deterministic, no I/O, safe
// to call from a table test for every rule in allowRules plus every
// hard-deny case.
func Classify(id Identity, args []string) Decision {
	if len(args) == 0 {
		return deny("empty gh invocation", allowedSummary())
	}

	p := parseArgs(args)

	// Hard blocks — checked before the allowlist so the deny reason is
	// specific. These apply regardless of what rule (if any) would
	// otherwise match.
	if hardDenyCommands[p.command] {
		return deny(fmt.Sprintf("gh %s is never permitted for Pilot executor sessions", p.command), allowedSummary())
	}
	if p.command == "issue" && hardDenyIssueSubs[p.sub] {
		return deny(fmt.Sprintf("gh issue %s mutates issue lifecycle state and is never permitted", p.sub), allowedSummary())
	}
	if p.hasAddLabel || p.hasRemoveLabel {
		return deny("label mutation (--add-label/--remove-label) is never permitted", allowedSummary())
	}
	if p.repoGiven && id.TaskRepo != "" && p.repo != id.TaskRepo {
		return deny(fmt.Sprintf("-R/--repo %s does not match the task's repo %s", p.repo, id.TaskRepo), allowedSummary())
	}

	for _, r := range allowRules {
		if r.command != p.command || r.sub != p.sub {
			continue
		}
		switch r.kind {
		case kindRead:
			return Decision{Verdict: VerdictAllow, Reason: "read-only command"}
		case kindAPIRead:
			return checkAPIRead(p)
		case kindOwnArtifact:
			return checkOwnArtifact(id, p)
		}
	}

	return deny(fmt.Sprintf("gh %s is not in the allowed command set for Pilot executor sessions", strings.TrimSpace(p.command+" "+p.sub)), allowedSummary())
}

// checkAPIRead allows `gh api` only when it is unambiguously a GET: no
// explicit non-GET -X/--method, and no data-carrying flag. -f/-F/--input
// imply a mutation even when -X is left at its GET default (gh
// auto-switches to POST when data flags are present) — treating their mere
// presence as non-GET is stricter than the letter of "gh api non-GET" but
// closes that implicit-mutation gap deliberately.
func checkAPIRead(p parsedArgs) Decision {
	if p.hasDataFlag {
		return deny("gh api with -f/-F/--field/--raw-field/--input carries a request body and is treated as a mutation", allowedSummary())
	}
	method := strings.ToUpper(strings.TrimSpace(p.method))
	if method != "" && method != "GET" {
		return deny(fmt.Sprintf("gh api -X %s is not a read", p.method), allowedSummary())
	}
	return Decision{Verdict: VerdictAllow, Reason: "gh api GET"}
}

// checkOwnArtifact allows pr create / issue comment / pr comment only when
// the call's target matches the task's own branch or issue number.
func checkOwnArtifact(id Identity, p parsedArgs) Decision {
	switch p.command + " " + p.sub {
	case "pr create":
		if !p.headGiven {
			// No explicit --head: gh defaults to the current branch, which
			// in the executor's worktree is already the task branch.
			return Decision{Verdict: VerdictAllow, Reason: "pr create on current branch"}
		}
		if p.head == id.TaskBranch && id.TaskBranch != "" {
			return Decision{Verdict: VerdictAllow, Reason: "pr create targets the task's own branch"}
		}
		return deny(fmt.Sprintf("gh pr create --head %s does not match the task's branch %s", p.head, id.TaskBranch), allowedSummary())

	case "issue comment":
		if len(p.positional) == 0 {
			return deny("gh issue comment requires an issue number and none was given", allowedSummary())
		}
		num, ok := extractIssueOrPRNumber(p.positional[0])
		if ok && id.TaskIssue != "" && num == id.TaskIssue {
			return Decision{Verdict: VerdictAllow, Reason: "comment targets the task's own issue"}
		}
		return deny(fmt.Sprintf("gh issue comment targets #%s, not the task's own issue #%s", p.positional[0], id.TaskIssue), allowedSummary())

	case "pr comment":
		if len(p.positional) == 0 {
			// No explicit target: gh defaults to the PR associated with the
			// current branch, i.e. the task's own PR.
			return Decision{Verdict: VerdictAllow, Reason: "pr comment on current branch's PR"}
		}
		target := p.positional[0]
		if target == id.TaskBranch && id.TaskBranch != "" {
			return Decision{Verdict: VerdictAllow, Reason: "comment targets the task's own branch"}
		}
		// A bare number or URL target can't be verified against TaskBranch
		// without an extra `gh` lookup this guard deliberately avoids
		// (rate discipline) — deny rather than guess.
		return deny(fmt.Sprintf("gh pr comment target %q cannot be verified as the task's own PR", target), allowedSummary())
	}
	return deny("unrecognized own-artifact command", allowedSummary())
}

func deny(reason, allowed string) Decision {
	return Decision{Verdict: VerdictDeny, Reason: reason, Allowed: allowed}
}

// allowedSummary is the "what IS allowed" hint appended to every deny, so a
// refused executor session sees actionable stderr instead of a bare
// rejection.
func allowedSummary() string {
	return "allowed: gh issue view/list, gh pr view/list/checks/diff, gh run view/list, " +
		"gh auth status, gh api (GET only), gh pr create (own branch), " +
		"gh issue comment/gh pr comment (own issue/PR)"
}

// JournalEntry is one line of the JSONL guard journal written by the
// `pilot gh-guard` process (cmd/pilot/ghguard.go) for every DENY, and is
// read back by the executor after the subprocess exits (see
// executor.readGhGuardJournal in ghguard_spawn.go) to populate
// BackendResult.GhGuardDenials. This is the evidence trail the GH-4670
// audit's alert channel surfaces to a human operator.
type JournalEntry struct {
	Time      time.Time `json:"time"`
	Verdict   Verdict   `json:"verdict"`
	Reason    string    `json:"reason"`
	Args      []string  `json:"args"`
	TaskIssue string    `json:"task_issue,omitempty"`
	TaskRepo  string    `json:"task_repo,omitempty"`
}

// AppendJournal appends one entry to the JSONL journal at path, creating it
// if necessary. Best-effort evidence trail: callers should log but not fail
// the guard decision if this returns an error.
func AppendJournal(path string, entry JournalEntry) error {
	if path == "" {
		return fmt.Errorf("ghguard: empty journal path")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("ghguard: open journal: %w", err)
	}
	defer func() { _ = f.Close() }()

	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("ghguard: marshal journal entry: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("ghguard: write journal entry: %w", err)
	}
	return nil
}

// ReadJournal reads back all entries from the JSONL journal at path. A
// missing file returns (nil, nil) — the common case when a run had no
// denials. Lines that fail to parse are skipped (best-effort evidence,
// not a critical path); the journal is never allowed to fail a run.
func ReadJournal(path string) ([]JournalEntry, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("ghguard: open journal: %w", err)
	}
	defer func() { _ = f.Close() }()

	var entries []JournalEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e JournalEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return entries, fmt.Errorf("ghguard: scan journal: %w", err)
	}
	return entries, nil
}

// ResolveFallbackGh searches PATH for a `gh` binary, skipping excludeDir.
// Used only when EnvRealGh was not set (a wiring failure, not the expected
// path) so the guard's fail-open-for-reads behavior has something to exec
// without risking a self-referential loop back into the shim directory it
// is trying to work around.
func ResolveFallbackGh(excludeDir string) (string, error) {
	pathEnv := os.Getenv("PATH")
	excludeDir = filepath.Clean(excludeDir)
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		if excludeDir != "." && filepath.Clean(dir) == excludeDir {
			continue
		}
		candidate := filepath.Join(dir, "gh")
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("ghguard: no gh binary found in PATH (excluding shim dir %s)", excludeDir)
}

// IdentityFromEnv builds an Identity from the well-known env vars. The
// caller (cmd/pilot/ghguard.go) is expected to call this directly against
// os.Environ()-backed os.Getenv.
func IdentityFromEnv(getenv func(string) string) Identity {
	return Identity{
		TaskIssue:  getenv(EnvTaskIssue),
		TaskRepo:   getenv(EnvTaskRepo),
		TaskBranch: getenv(EnvTaskBranch),
		RealGh:     getenv(EnvRealGh),
	}
}

// FormatArgsForLog renders argv as a single space-joined string purely for
// human-readable log lines — never used for re-parsing or execution.
func FormatArgsForLog(args []string) string {
	return strings.Join(args, " ")
}
