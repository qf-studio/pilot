// Package ghguard implements the GH-4671 gh-guard shim: a pure, allowlist
// policy that classifies a `gh` CLI invocation (argv, minus the leading
// "gh") against the task the executor session was dispatched for, and
// decides whether the real gh binary may run it.
//
// This is the preventive leg of the GH-4649 containment cluster. GH-4670
// (sideeffect_audit.go, already merged) detects after the fact that an
// executor session mutated a sibling issue; this package stops it from
// happening in the first place by intercepting every `gh` call the Claude
// Code subprocess makes via the Bash tool (see run.go for the process
// boundary, and backend_claudecode.go for how the shim is spliced onto
// PATH ahead of the real gh).
//
// Classify is a pure function: no I/O, no gh calls, no filesystem access.
// The policy table is one data-driven slice (policyTable below) mapping a
// gh subcommand path (e.g. {"issue","view"}) to a verdict, mirroring the
// RepoAllowlist chokepoint pattern in repo_guardrail.go (GH-3027).
package ghguard

import (
	"fmt"
	"strings"
)

// TaskContext identifies the task an executor session was dispatched for.
// Populated from PILOT_TASK_ISSUE / PILOT_TASK_REPO / PILOT_TASK_BRANCH env
// vars set by the spawning backend (backend_claudecode.go). Any field may be
// empty if the task isn't GitHub-sourced or the env wasn't fully wired —
// Classify treats an empty field as "cannot verify ownership" and denies
// the mutation classes that depend on it, while unconditional read rules
// remain unaffected (fail open for reads, fail closed for mutations — see
// acceptance criterion 3 on GH-4671).
type TaskContext struct {
	// Issue is the bare issue number the session was dispatched for, e.g. "4671".
	Issue string
	// Repo is "owner/repo" for the task's source repo.
	Repo string
	// Branch is the task's own branch name (the PR head branch).
	Branch string
}

// Decision is the outcome of classifying a gh invocation.
type Decision int

const (
	// Deny means the real gh binary must not run this invocation.
	Deny Decision = iota
	// Allow means the invocation may be passed through to the real gh binary.
	Allow
)

func (d Decision) String() string {
	if d == Allow {
		return "allow"
	}
	return "deny"
}

// Verdict is the result of classifying one gh invocation.
type Verdict struct {
	Decision Decision
	// Reason is a short, human-readable explanation — always set, used in
	// both the denial message printed to the Claude Code subprocess's
	// stderr and the guard journal entry (journal.go).
	Reason string
}

func allow(reason string) Verdict { return Verdict{Decision: Allow, Reason: reason} }
func deny(reason string) Verdict  { return Verdict{Decision: Deny, Reason: reason} }

// ruleKind classifies how a policyRule's verdict is determined.
type ruleKind int

const (
	// kindAllow unconditionally allows the matched subcommand — read-only
	// GitHub operations that carry no mutation risk regardless of target.
	kindAllow ruleKind = iota
	// kindDeny unconditionally denies the matched subcommand.
	kindDeny
	// kindCheck runs rule.check against the remaining argv (after the
	// matched path tokens) and the task context to decide — used for
	// mutations that are allowed only against the task's own artifacts.
	kindCheck
)

// checkFunc classifies a mutating gh subcommand against the task context.
// rest is argv with the matched subcommand path tokens stripped, so a
// checkFunc for {"issue","comment"} sees only the flags/positional args
// that follow "issue comment".
type checkFunc func(rest []string, ctx TaskContext) Verdict

// policyRule is one entry in policyTable. path is the leading subcommand
// tokens to match (1 or 2 tokens; gh's own subcommand nesting never goes
// deeper than that for anything this guard cares about).
type policyRule struct {
	path   []string
	kind   ruleKind
	reason string // used verbatim for kindDeny; used as the Allow reason for kindAllow
	check  checkFunc
}

// policyTable is the single data-driven policy surface (acceptance
// criterion 1 on GH-4671: "one data-driven struct slice"). Two-token paths
// are matched before one-token paths, so a specific rule (e.g.
// {"issue","view"}) takes precedence over a group-level rule (e.g.
// {"issue"}, if one existed). Anything not matched here falls through to
// the default deny in Classify — this table only needs to *positively*
// list what's allowed or explicitly call out common mutations for a clear
// deny reason; every other gh subcommand (gh secret, gh variable, gh
// workflow, gh gist, gh ssh-key, ...) is denied by that default.
var policyTable = []policyRule{
	// --- Allow unconditional (read-only) ---
	{path: []string{"issue", "view"}, kind: kindAllow, reason: "read-only"},
	{path: []string{"issue", "list"}, kind: kindAllow, reason: "read-only"},
	{path: []string{"pr", "view"}, kind: kindAllow, reason: "read-only"},
	{path: []string{"pr", "list"}, kind: kindAllow, reason: "read-only"},
	{path: []string{"pr", "checks"}, kind: kindAllow, reason: "read-only"},
	{path: []string{"pr", "diff"}, kind: kindAllow, reason: "read-only"},
	{path: []string{"run", "view"}, kind: kindAllow, reason: "read-only"},
	{path: []string{"run", "list"}, kind: kindAllow, reason: "read-only"},
	{path: []string{"auth", "status"}, kind: kindAllow, reason: "read-only"},

	// --- Allow own-artifact (mutation, verified against TaskContext) ---
	{path: []string{"api"}, kind: kindCheck, check: checkAPI},
	{path: []string{"pr", "create"}, kind: kindCheck, check: checkPRCreate},
	{path: []string{"issue", "comment"}, kind: kindCheck, check: checkIssueComment},
	{path: []string{"pr", "comment"}, kind: kindCheck, check: checkPRComment},

	// --- Deny always ---
	{path: []string{"issue", "close"}, kind: kindDeny, reason: "closes issue lifecycle state"},
	{path: []string{"issue", "reopen"}, kind: kindDeny, reason: "mutates issue lifecycle state"},
	{path: []string{"issue", "edit"}, kind: kindDeny, reason: "mutates issue metadata/labels"},
	{path: []string{"issue", "lock"}, kind: kindDeny, reason: "mutates issue state"},
	{path: []string{"issue", "unlock"}, kind: kindDeny, reason: "mutates issue state"},
	{path: []string{"issue", "transfer"}, kind: kindDeny, reason: "transfers issue across repos"},
	{path: []string{"issue", "delete"}, kind: kindDeny, reason: "deletes issue"},
	{path: []string{"issue", "pin"}, kind: kindDeny, reason: "mutates issue state"},
	{path: []string{"issue", "unpin"}, kind: kindDeny, reason: "mutates issue state"},
	{path: []string{"pr", "close"}, kind: kindDeny, reason: "closes PR lifecycle state"},
	{path: []string{"pr", "reopen"}, kind: kindDeny, reason: "mutates PR lifecycle state"},
	{path: []string{"pr", "edit"}, kind: kindDeny, reason: "mutates PR metadata/labels"},
	{path: []string{"pr", "merge"}, kind: kindDeny, reason: "merges a PR — outside executor scope"},
	{path: []string{"pr", "lock"}, kind: kindDeny, reason: "mutates PR state"},
	{path: []string{"pr", "review"}, kind: kindDeny, reason: "reviews a PR — outside executor scope"},
	{path: []string{"pr", "ready"}, kind: kindDeny, reason: "mutates PR state"},
	{path: []string{"release"}, kind: kindDeny, reason: "release management is out of scope"},
	{path: []string{"repo"}, kind: kindDeny, reason: "repo management is out of scope"},
	{path: []string{"secret"}, kind: kindDeny, reason: "secret management is out of scope"},
	{path: []string{"variable"}, kind: kindDeny, reason: "variable management is out of scope"},
	{path: []string{"workflow"}, kind: kindDeny, reason: "workflow management is out of scope"},
	{path: []string{"label"}, kind: kindDeny, reason: "label management is out of scope"},
	{path: []string{"gist"}, kind: kindDeny, reason: "gist management is out of scope"},
	{path: []string{"ssh-key"}, kind: kindDeny, reason: "credential management is out of scope"},
	{path: []string{"auth", "login"}, kind: kindDeny, reason: "credential mutation is out of scope"},
	{path: []string{"auth", "logout"}, kind: kindDeny, reason: "credential mutation is out of scope"},
	{path: []string{"auth", "refresh"}, kind: kindDeny, reason: "credential mutation is out of scope"},
	{path: []string{"auth", "token"}, kind: kindDeny, reason: "credential exposure is out of scope"},
}

// repoValueFlags are the flag spellings gh uses to target a repo other than
// the one inferred from the working directory.
var repoValueFlags = []string{"-R", "--repo"}

// Classify decides whether argv (a gh invocation with the leading "gh"
// already stripped) may run, given the dispatching task's context. It never
// performs I/O and never calls gh itself.
func Classify(argv []string, ctx TaskContext) Verdict {
	if len(argv) == 0 {
		return deny("empty gh invocation")
	}

	// Global check: an explicit -R/--repo naming any repo other than the
	// task's own is denied regardless of subcommand (GH-4671 acceptance
	// criterion: "any -R/--repo other than PILOT_TASK_REPO"). Applies to
	// reads too — a foreign -R is a strong enough signal of an
	// out-of-scope operation that it isn't worth carving out an exception.
	if target, ok := extractFlagValue(argv, repoValueFlags); ok {
		if !repoMatches(target, ctx.Repo) {
			return deny(fmt.Sprintf("-R/--repo %q does not match task repo %q", target, orUnknown(ctx.Repo)))
		}
	}

	rule, path := matchRule(argv)
	if rule == nil {
		return deny(fmt.Sprintf("gh %s is not on the allowlist", strings.Join(path, " ")))
	}

	switch rule.kind {
	case kindDeny:
		return deny(rule.reason)
	case kindCheck:
		return rule.check(argv[len(path):], ctx)
	default:
		return allow(rule.reason)
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

// matchRule finds the most specific policyTable entry matching argv's
// leading subcommand tokens, preferring a two-token match (e.g.
// {"issue","view"}) over a one-token match (e.g. {"release"}). Returns the
// matched rule (nil if none) and the token path that was attempted at the
// winning specificity, for use in the default-deny message.
func matchRule(argv []string) (*policyRule, []string) {
	two := leadingTokens(argv, 2)
	if len(two) == 2 {
		if r := findRule(two); r != nil {
			return r, two
		}
	}
	one := leadingTokens(argv, 1)
	if len(one) == 1 {
		if r := findRule(one); r != nil {
			return r, one
		}
	}
	if len(two) == 2 {
		return nil, two
	}
	return nil, one
}

func findRule(path []string) *policyRule {
	for i := range policyTable {
		if pathEqual(policyTable[i].path, path) {
			return &policyTable[i]
		}
	}
	return nil
}

func pathEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// leadingTokens returns up to n leading non-flag tokens from argv. gh's
// subcommand words always precede any flags or positional arguments, so
// this reliably extracts e.g. {"issue","view"} from
// {"issue","view","123","--repo","foo/bar"}.
func leadingTokens(argv []string, n int) []string {
	out := make([]string, 0, n)
	for _, a := range argv {
		if strings.HasPrefix(a, "-") {
			break
		}
		out = append(out, a)
		if len(out) == n {
			break
		}
	}
	return out
}
