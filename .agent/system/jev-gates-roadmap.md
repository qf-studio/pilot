# Jev gates roadmap — where a System One classifier belongs in Pilot, and where it does not

**Owner**: founder · **Created**: 2026-09-30 · **Status**: gate 1 in flight (#5509 → #5518–#5521)
**Foundation**: TASK-505 (`internal/typesafe` client, top-level `typesafe:` config, `Resolve` merge contract with six reasons, shadow mode)

Jev is TypeSafe's System One model: one HTTP call, many typed questions (choice / yes-no / score), probabilities plus a server-computed confidence, ~150 ms. It answers bounded semantic questions. It does not replace code that already knows the answer.

---

## Triage rule — when a gate gets Jev

A gate is a Jev candidate only when **all five** hold:

1. **Bounded question, fixed option set.** "Which of these three kinds?" not "what should we do?".
2. **Small text input.** One line to a few KB after a head cap. Diffs and full logs need trimming first.
3. **Regex or heuristic stays as the floor.** Jev overrides only above `min_confidence`; error, timeout, low confidence keep today's verdict. The gate can never get slower or less reliable.
4. **Production misclassification history.** At least one incident where the heuristic was wrong and something broke. No history means no evidence the tails exist.
5. **Code consumes the answer.** If a human reads it, a wrong answer costs a glance, not a PR.

A gate is **skipped** when any of these apply:

- The signal is numeric or structural (timers, counts, exit codes, HTML markers, label names).
- An LLM already answers it (intent judge, complexity, effort). Only a heuristic *fallback* path could qualify, and rarely does.
- The right fix is a **typed signal from the author or the agent** (a tag, a JSON field, a line-1 token). Classifying prose to recover a fact the writer could have stated is the wrong direction. Fix the contract first; Jev only for inputs we do not control (human-written issue bodies, CI logs).
- The consequence of a wrong answer is cosmetic.

Navigator's own typed judge (v7.7.1 live read: 4 overrides in 92 prompts) is the calibration: heuristics are right almost always on ordinary inputs; Jev earns its call in the tails. Every gate ships in shadow first and flips on counters.

---

## Verdicts

### UPGRADE — wave 1 (foundation + the three gates with real incident history)

| # | Gate | Anchor | Question → options | Floor / fallback | Why now |
|---|---|---|---|---|---|
| 1 | **Acceptance item kind** | `internal/executor/acceptance_evidence.go:155` | choice: `paste_output` / `mutation` / `other`; plus target test name picked among regex-found `Test*` tokens + `none` | regex (`ClassifyAcceptanceItem`) | Four regex-tuning incidents in a row (#5486, #5506, #5513, #5514). **In flight: TASK-505 / #5509 → #5518 client, #5519 config, #5520 classifier, #5521 docs.** |
| 2 | **Base-presence path token** | `internal/executor/dependency_detector.go:169,213` | choice per backticked `x/y.ext` span with ±1 sentence of context: `existing_prerequisite` / `to_be_created` / `not_a_repo_file` | today's rule (hold when missing) | Worst history: 165 holds across 10 tasks, silent `pilot-needs-human` (GH-257, GH-5221, GH-5241, GH-5246, GH-5509 today). Jev `to_be_created` or `not_a_repo_file` above threshold → skip that span. Safe: a false skip only removes a hold; the executor still fails loudly if a real prerequisite is missing. |
| 3 | **CI failure class** | `internal/autopilot/failure_class.go:80,242` | choice: `code` / `infra` / `infra_billing` / `unknown` on a head-capped log excerpt | existing `Verdict{source, AuthorizesDestructive}` machinery (`failure_class.go:391-461`); `unknown` = non-destructive hold | Infra read as code closed a correct PR and spawned a fix issue (#4526, #4533, #4591, #4779, #4791; pitfall memory). First cross-package consumer of `internal/typesafe` — proves the shared client. |

Order is by reuse, not by rank: 1 builds the client and seam; 2 reuses the executor seam; 3 proves the package from `internal/autopilot`.

### UPGRADE — wave 2 (evidence-gated; file only when the trigger fires)

| # | Gate | Anchor | Question → options | Trigger to file |
|---|---|---|---|---|
| 4 | **Epic-or-single-PR detection** | `internal/executor/complexity.go:206` (`detectEpic`) | choice: `single_pr_with_plan` / `epic_to_decompose` | **Do the typed fix first**: honor an explicit author marker (`[single]` in title or a `one PR` line) in code — pitfall `pilot-decomposes-parent-issues-one-pr-instructions-not-honored`, GH-4395, and #5509 today (35 checkboxes + phases → 4 sub-issues). Jev only for unmarked bodies afterwards. |
| 5 | **Criteria extraction** | `internal/acceptance/extract.go:18-31`, `internal/executor/decompose.go:587` | noul per line: is this an acceptance criterion? | A repo whose issues are not in house style (no `## Acceptance` H2, no checkboxes) shows dropped criteria. This repo's SOP already mandates the format, so no trigger here yet. |
| 6 | **Child dependency shape** | `internal/executor/dependency_detector.go:56` | choice: `none` / `verification` / `explicit` | A second TASK-402-class ordering incident. Current regex lets `add` / `fix` anywhere override the verification shape. |
| 7 | **Stderr error type** | `internal/executor/backend_claudecode.go:171` | choice: `rate_limit` / `oom` / `timeout` / `auth` / `other` | A retry-strategy incident not caught by exit code 137/139 (history: #2112, #4105, #4412 — all fixed by substrings so far). |

### SKIP — keep the heuristic, or fix the contract instead

| Gate | Anchor | Why not Jev |
|---|---|---|
| Stagnation, lane starvation, deadlock, alert cooldown/dedupe | `stagnation.go:63`, `lane_starvation.go:38`, `controller.go:8638`, `alerts/engine.go:1639` | Numeric. Timers and counters, no semantics. |
| Semver bump from commit subject | `autopilot/releaser.go:103-140` | Deterministic conventional-commit grammar. A non-conventional subject should yield `BumpNone` loudly, not a guess. |
| Intent judge PASS/FAIL/CONFIDENCE parse | `executor/intent_judge.go:251,507,537` | Parse problem, not judgment. Fix: structured output from the judge LLM. |
| Effort tier fallback | `executor/effort_classifier.go:44-48` | LLM-backed; static map fallback is adequate; no incident. |
| Complexity fallback (non-epic part) | `executor/complexity.go:113` | LLM-backed first; fallback rarely runs. The epic sub-question is gate 4 above. |
| Review-verdict parser | `memory/pr_review.go:36,49` | Contract fix already in place: verdict token must start line 1 (mem-204, #5507 re-parse). Ledger stats only; a wrong answer costs nothing downstream. |
| Chat intent | `intent/intent.go:121` | LLM classifier exists (`classifier.go:67`); heuristic is the fallback of a fallback. |
| Env-class vs code failure | `executor/runner.go:180` | Structural guard (0 tokens, no SHA/PR, <60 s) already backs the substring list (#5211, #5217). Revisit only on a streak-escalation incident. |
| `DECLINED:` marker detection | `executor/runner.go:2057` | Protocol string from our own agent. Fix: structured exit signal (`signal.go:126` already exists). |
| Infra-noise retry signatures | `executor/runner.go:210-240` | Substring on our own error strings; deterministic. |
| Contract-field regex on diffs | `executor/contract_evidence.go:212` | Input is a diff (1–20 KB), recall-biased regex is the right tool; no incident logged since GH-5009. |
| Diff-coverage section detection | `executor/acceptance_evidence_diff_coverage.go:90,139` | House-style heading; author contract, not a judgment. |
| Owner-death meta markers, reviewer trust (`[bot]` suffix) | `autopilot/owner_death.go:71-145`, `controller.go:4732` | HTML-comment markers and login suffixes are deterministic; reviewer trust wants an allowlist (TASK-493), not a classifier. |
| Telegram / Slack command and mention parsing | `telegram/commands.go:675`, `slack/events.go:153` | Deterministic prefix parsing. |
| Pre-push docs-only classify, secret-pattern check | `scripts/pre-push-classify.sh`, `scripts/check-secret-patterns.sh` | Path and pattern rules; must fail closed, never probabilistic. |
| Memory extractor patterns | `memory/extractor.go:281-595` | Low consequence; noise tolerated. |
| Build/test command detection | `quality/types.go:283`, `cmd/pilot/init_project.go:168` | Manifest presence; deterministic. |

Not sampled (assumed deterministic, unverified): `adapters/github` label and comment routing, `epic_reconcile.go`, `scope_membership.go`, `board_source_audit.go`.

---

## Sequencing and dependencies

```
#5518 typesafe pkg ─┐
#5519 config       ─┼─► #5520 classifier + seam ─► #5521 docs ─► gate 1 in shadow on the box
                    │
                    └─► gate 2 (base-presence)  needs: client + executor seam on main
                        gate 3 (CI failure)     needs: client on main; own seam in autopilot
                        console leg: fleet-supplied TYPESAFE_API_KEY (after #5519 names the env var)
```

- Each gate is its own TASK doc, authored via nav-task, filed via nav-pilot, **body pre-flighted for Rule 3b** (no backticked paths that are not on main) and **under 15 checkboxes** unless decomposition is wanted.
- No gate flips its default before its shadow counters justify it (below).

---

## Rollout protocol (same for every gate)

1. **Ship in shadow** (`shadow: true` default): Jev runs, the regex verdict is used, one info line per decision with `agreed / overrode / low_confidence / errors / latency_ms`.
2. **Read counters after ≥100 decisions or 7 days**, whichever is later. Flip candidates: `overrode` cases inspected by hand; if ≥80 % of overrides are correct and `errors` under 2 %, set `shadow: false` for that gate on the box.
3. **`min_confidence` per gate**, default 0.8 (three options: pmax ≈ 0.87). Raise it where a wrong override is destructive (gate 3: closing a PR), lower it where the fallback is merely a hold (gate 2).
4. **Kill switch**: `provider: regex` per gate, or unset `TYPESAFE_API_KEY` for all gates at once. No restart semantics beyond config reload.
5. **Privacy line**: only the text under judgment leaves the machine (checklist bullets, a backticked span with a sentence of context, a capped log excerpt). Never diffs, never command output, never repo paths the author did not write. Secret-shaped tokens redacted before send.

---

## Refs

- TASK-505 (foundation + gate 1) · #5509 → #5518, #5519, #5520, #5521
- Inventory research 2026-09-30: two nav-research passes (classifier seam; gate inventory, ~120 files matched, 14 read)
- TypeSafe docs: https://docs.typesafe.ai/api.md · https://docs.typesafe.ai/confidence.md · https://docs.typesafe.ai/concepts/use-case-map.md ("Harness Engineering: routing and guardrails")
- Navigator typed judge as reference implementation and calibration: plugin `hooks/nav_hook_lib/judge.py`, TASK-79/80, v7.7.0–v7.7.1
- Memories: `pitfalls/base-presence-gate-false-positives-hold-then-silent-needs-human`, `pitfalls/ci-infra-failure-misclassified-as-code`, `pitfalls/pilot-decomposes-parent-issues-one-pr-instructions-not-honored`, mem-203, mem-204
- SOP: `.agent/sops/onboarding/new-project-issue-authoring.md` Rule 3b, Rule 5b
