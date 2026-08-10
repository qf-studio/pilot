# SOP: destructive autopilot actions must consume a typed, evidence-carrying Verdict

**Category:** Quality / autopilot decision-ladder correctness
**Implemented:** 2026-08-10 (TASK-459 Phase 4, GH-4823)
**Source:** TASK-459 Phases 1-4 (#4796 → PR#4802, #4811 → PR#4812, #4817 →
PR#4821, #4823 → this leg)

## The invariant

Every call site in the daemon that closes a PR, deletes a branch, spawns a
fix issue, or merges to main is *irreversible or costly-reversible by a
human* — see `.agent/system/irreversible-actions.md` for the full inventory
and its reversibility tiers. TASK-459 made these two rules structural
instead of conventional:

1. **A destructive action is authorized by a typed `Verdict`
   (`internal/autopilot/failure_class.go`) with positive evidence, not by a
   raw string/bool/nil-check.** `Verdict.AuthorizesDestructive()` requires
   both a non-`FailureClassUnknown` class *and* non-empty evidence —
   checked independently, because a bare `Verdict{}` zero value has
   `Class() == FailureClassUnknown` by construction but that alone doesn't
   rule out every degenerate-construction path (PR#4802 review finding 1).
2. **Uncertainty routes to hold, not to action.** If evidence can't be
   gathered, or classification is ambiguous, the destructive rung must be
   unreachable and the code must fall through to `escalateAndHold` (or the
   executor-level equivalent, `escalateStalledTask`) instead. "We don't
   know" is never grounds to close/delete/merge.

Not every destructive call site in the inventory uses a `Verdict` directly
— some predate the type and use an equally strong `re-read` (fetch current
state immediately before acting) or `counter` (budget exhaustion) gate
instead. The invariant that matters is **positive evidence gates the
action**, not any single implementation of it. `Verdict` is the preferred
vocabulary for CI-failure-derived evidence specifically; see the inventory's
Evidence column for which tag applies to a given site.

## How to add a new destructive call site

1. **Gate it first.** Before the call to `ClosePullRequest`/`DeleteBranch`/
   `CreateFailureIssue`/`MergePullRequest` (or a future addition to that
   family), construct or receive a `Verdict` and check
   `verdict.AuthorizesDestructive()` — or an equally strong `re-read`/
   `counter` gate if a `Verdict` genuinely doesn't fit the evidence shape.
   Route the "no" branch to `escalateAndHold`/`escalateStalledTask`, never
   silently past the gate.
2. **Add a row to `.agent/system/irreversible-actions.md`** under the
   relevant family table (§1-§8): site, subsystem, reversibility tier,
   blast radius, evidence tag, `required_checks` scoping. This table is the
   audit trail — don't skip it because the change feels small.
3. **Add the file to `scripts/check-destructive-calls.sh`'s
   `DESTRUCTIVE_CALL_ALLOWLIST`** with a comment naming the inventory family
   and what gates the call. This is a deliberate manual step, not
   automation — the point is that a new call site can't land silently; a
   human has to look at it and write down why it's safe.
4. **Construct any new `Verdict` via `NewVerdict`/`NewUnknownVerdict`**
   (`internal/autopilot/failure_class.go`), never a bare `Verdict{}`
   composite literal, even from inside package `autopilot` — unexported
   fields only stop cross-package construction, not intra-package
   (PR#4802 review finding 2). If a test genuinely needs to construct a
   zero-value or hand-crafted `Verdict` to test the type itself (see
   `TestVerdict_ZeroValue`/`TestVerdict_AuthorizesDestructive` in
   `failure_class_test.go`), that test file is the one addition
   `check-destructive-calls.sh`'s `VERDICT_LITERAL_ALLOWLIST` should ever
   need — don't add a second one; write the corpus/table-driven case into
   the existing test instead.

## Reason-string-as-protocol: the anti-pattern this also guards against

A parallel hazard TASK-459 Phase 4 closed: routing a *decision* (not just
authorizing an action) by pattern-matching a human-formatted message string
— e.g. `strings.Contains(reason, someSubstring)` deciding retry-vs-park.
This is fragile in a way `check-destructive-calls.sh` can't grep for: it
looks like ordinary control flow, not a bypass. The fix pattern (see
`scope_release.go`'s `handleScopeReleaseFailure`, TASK-459 Phase 4 task 4a)
is to carry the decision as an explicit typed value (a bool, enum, or
constant) from the site that *knows* the answer to the site that *acts* on
it, and keep the formatted string for logs/comments only — it stops being
the protocol. When adding a new routing decision fed by a message you also
show a human, ask: "if I reworded this string tomorrow, would routing
silently break?" If yes, it needs a typed carrier.

`internal/executor/dispatcher.go`'s `escalateStalledTask` idempotence key
(exact `Error`-string equality) is a narrower, deliberately-accepted
instance of the same shape — see
`gh4823_escalate_stalled_idempotence_test.go`'s doc comments for why a full
typed-key fix was judged disproportionate there (the dynamic parts of the
string are stable by construction; only a reviewed prose edit could break
it, and the blast radius is one duplicate alert). Not every reason-string
dependency needs a typed replacement — but it needs an explicit decision,
recorded in a comment or a test, about why the current form is safe enough.

## How the grep gate works

`scripts/check-destructive-calls.sh` (wired into `make check-destructive`,
`scripts/pre-push-gate.sh`, and CI's "Check Destructive-Call Gate" job) runs
two checks against tracked `*.go` files:

1. Greps production (non-`_test.go`) files for
   `\.(ClosePullRequest|DeleteBranch|CreateFailureIssue|MergePullRequest)\(`
   and fails if a match is in a file not on `DESTRUCTIVE_CALL_ALLOWLIST`.
2. Greps all files for a bare `Verdict{` composite literal (a negative
   lookbehind excludes qualified identifiers like `sdkcore.Verdict{}`, and a
   leading `\b` excludes embedded-suffix look-alikes like
   `PreFlightVerdict{}`) and fails if a match is in a file not on
   `VERDICT_LITERAL_ALLOWLIST`.

Run `./scripts/check-destructive-calls.sh --self-test` to verify the gate's
own detection logic in isolation (seeded violations caught, real allowlisted
files not false-positived) without touching repo files.

### What to do when it fires

- **If it's a genuine new destructive call site or `Verdict{}` need**:
  follow "How to add a new destructive call site" above — gate the call,
  update the inventory, update the allowlist, open the PR with all three
  changes together so a reviewer sees the evidence reasoning, not just an
  allowlist diff.
- **If it's a false positive** (e.g. a new type that happens to end in
  `Verdict` and collides with the `\bVerdict\{` pattern, or a new legitimate
  direct caller of one of the four methods for testing purposes): fix the
  script's pattern/allowlist precision rather than reflexively adding the
  offending file — the two checks are deliberately narrow-scoped (see the
  script's own header comments) to avoid training contributors to ignore
  gate failures.
- **Never bypass with `git push --no-verify`** to get past this specific
  check — the entire point is that these sites get eyes-on review.

## References

- `.agent/system/irreversible-actions.md` — the inventory (families 1-8,
  reversibility tiers, evidence tags, cross-cutting findings §9)
- `internal/autopilot/failure_class.go` — the `Verdict` type,
  `NewVerdict`/`NewUnknownVerdict`, `AuthorizesDestructive()`
- `scripts/check-destructive-calls.sh` — the grep gate
- `scripts/check-mocks.sh` — the grep-gate precedent this followed
  (TASK-441 Leg 1)
- TASK-459 Phases 1-4: #4796/PR#4802 (inventory + `Verdict`), #4811/PR#4812
  (CI-failure ladder gated), #4817/PR#4821 (executor/dispatcher/poller
  gated), #4823 (this leg — vocabulary dedup, typed routing, grep gate)
