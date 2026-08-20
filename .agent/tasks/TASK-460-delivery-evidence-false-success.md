# TASK-460: Delivery-evidence audit — green CI is not proof the requested change shipped (false-success class)

**Status**: 🚀 First leg DISPATCHED 2026-08-20 — doc-vs-wire (see § Dispatched legs). Other directions still 📋 planned. Split out of TASK-459 by scope decision 2026-08-08 (founder call: separate track); not gated on TASK-459 Phases 2–4, but Phase 4's inventory hook feeds this task its site rows
**Created**: 2026-08-08
**Assignee**: Pilot (multi-leg once planned; dispatch one leg at a time)

---

## Context

**Problem**:

TASK-459 covers *false failure* — the daemon destroying correct work on misread failure signals. This task covers the mirror: **false success** — the daemon finalizing (merge, `pilot-done`, parent auto-close) on evidence that doesn't support the delivery claim. Green CI proves the diff broke nothing it tested; it does not prove the requested change shipped.

Concrete incident (`mem-151`, TASK-398/#4199, 2026-07-11): a 4-phase epic auto-collapsed to one sub-issue; the executor scoped itself to scaffolding, deferring wiring to "a subsequent subtask" the collapse never created. PR merged +840/−0 across only NEW files — helpers correct and unit-tested, `tui.go` (the stated rework target) untouched. New-file tests are trivially green, so CI passed, the PR merged, the parent auto-closed. Zero requirements shipped; every signal said success. Detection was manual (a "rework" PR with 0 deletions never editing its target file).

Structurally the same class as TASK-459's outage bugs: an irreversible action taken on decorative evidence. Mechanically different, which is why it's a separate track: TASK-459 *plumbs existing evidence* (check runs, logs, ledger status) through a typed contract in the autopilot ladder; this task must *generate new evidence* (does the diff touch the target surface? do ACs fail when the code is unwired?) and its fix territory is upstream — decomposition, executor scoping, spec quality — not `controller.go` gating.

**Second and third confirmed incidents (2026-08-19, both caught only by post-merge review agents):**
- **PR#4978** (GH-4965): merged fix was a functional no-op — `$teamId: String!` in a GraphQL `ID` position; live Linear API rejects the exact merged document pre-auth. Mock-transport tests validate no GraphQL, so all gates green. Pitfall `graphql-mock-tests-cannot-catch-schema-validation`. Fixed same day (#4985 → PR#4991, review mutation-tested the pin).
- **PR#4992** (GH-4987): merged feature is dead code — the Jira done leg hangs off autopilot PR tracking, but no path registers `pilot/JIRA-*` PRs (sdk adapter drops `OnPRCreated`; reconciler adoption filters `pilot/GH-`; external-merge path never calls it). The PR's own FEATURE-MATRIX entry claimed reachability that was checkable and false. Pitfall `merged-feature-dead-callback-not-bridged-onprcreated`. Follow-ups: pilot#4999, sdk#123/#124.
- New candidate leg from these: **reachability evidence** — for a PR whose spec says "call X when Y happens", delivery verification names the concrete production event path that invokes the new code (or an integration test that drives it end-to-end); "new code + green unit tests" is exactly the evidence shape both incidents defeated.

**Fourth confirmed incident (2026-08-19, caught by post-merge review 08-20):**
- **ui PR#113** (ui GH-112): the fix trusted a client-side `types.ts` docblock claiming `specVersion` = applied generation, over both the server DTO (`pilot-console instances/handlers.go:141` — it is the TARGET, `inst.ConfigGeneration`) and the incident evidence quoted in the issue itself ("v2 applied" at ledger GEN 2 / APPLIED 1 — only possible if the field is the target). It shipped only the `v0 → "none applied"` edge, rewrote comments AND tests to enshrine the wrong field semantics, and left the incident case reproducing. Every gate green: the tests assert self-consistent-but-wrong fixtures. New evidence shape defeated: **doc-vs-wire verification** — a fix whose correctness hinges on a field's semantics must cite the producing server code, not a consumer-side docblock. Supersession chain: console#193 (expose `appliedSpecVersion`) → ui#115 (render from it, gated).

**Fifth confirmed incident (found 2026-08-20 during operator dashboard test, shipped in UI-6/PR#61 on 08-14 — predates the class discovery):**
- **Dashboard chart empty with data on the wire**: `types.ts:383` docblock claims `byProject` keys are project *names*; producer (`dashboardapi/dto.go:46`, pinned by its own test `handlers_test.go:273`) keys by `projectDTO.Key` (connection UUID). `DashboardShippedChart.vue:76` follows the false docblock → all lookups miss → zero bars; fixtures/mockAdapter enshrine name-keys, suite green. Invisible through the 08-15 real-stack verify because the seed had zero shipped cards — first real ships (08-19) exposed it. Fix: [ui#118](https://github.com/qf-studio/pilot-console-ui/issues/118). Confirms the doc-vs-wire leg (pilot#5009) and that ui's `contract_files` must include `src/lib/api/types.ts`.

**Goal**:

Success-side finalization actions (merge, `pilot-done` label, parent/epic auto-close) require positive delivery evidence, not just green CI. Uncertainty routes to human review, not auto-close.

## Candidate directions (to be planned, not yet committed)

- **Diff-surface check**: a spec that names a target file/surface fails delivery verification if the diff never touches it (the mem-151 red flag: +N/−0 with target untouched). Cheapest first leg; pure heuristic, no AC parsing.
- **ACs observable**: spec-authoring rule + verifier — acceptance criteria must be phrased as checks that FAIL when the change is unwired (integration-seam tests, not new-helper unit tests). Builds on the spec-guard epic (2026-07-30) and the decomposition-integrity waves.
- **Collapse guard**: when an epic decomposes to a single sub-issue, verify the inherited spec still covers the whole scope; any "wire later" deferral in a collapsed epic is a hard stop (the mem-151 mechanism).
- **Evidence consumer**: whether the success-side sites consume TASK-459's `Verdict` (a delivery-verdict variant) or a separate contract — decide when planning; do not force-fit.
- **Doc-vs-wire verification** *(committed → dispatched, see below)*: a fix whose correctness hinges on a wire field's semantics must cite the code that PRODUCES the value (server handler/serializer), machine-verified — never a consumer-side docblock. Orthogonal to the four directions above: incident #4's diff touched the right files and its tests failed-when-unwired; only the semantics were inverted.

## Dispatched legs

**Doc-vs-wire (2026-08-20, founder call: full structural fix, prompt-only rejected as cosmetic). Reviewed same-day:**

- **[pilot#5009](https://github.com/qf-studio/pilot/issues/5009)** — Contract Evidence enforcement gate. Decomposed by daemon: #5010→PR#5014 config type (merged, **APPROVE-w-DEFECTS**: `ContractFiles` docblock describes producer-repo paths but executor matches CONSUMER-diff paths — silent-no-op misconfiguration trap) · #5011→PR#5015 Contents API (merged, **APPROVE-w-notes**: fail-closed on >1MB; raw path interpolation nit) · #5012→PR#5016 executor gate (**OPEN, APPROVE — ready to release**: verified genuine hard block, byte-identical to quality-gate failure path; all 4 anti-gaming rules with fetch-count-zero assertions; dedicated `--json-schema` call; ui-PR#113 synthetic fixture; one crack: diff-error skips gate fail-open) · #5013→PR#5017 wiring (**OPEN, APPROVE conditional**: 5/5 sites + grep-parity tripwire; merge AFTER #5016 — branch contains #5016's whole diff).
- **✅ ACTIVATION COMPLETE + LIVE (08-20 late)**: all #5019 legs merged (docblock PR#5023 · crack fixes PR#5026 · fetcher wiring PR#5041, reviewed APPROVE with mutation-tested tripwire) and the operator rebuild put the full gate on the box (v2.264.0-16-gd9244a40). **Remaining = operator step only**: add `contract_dependencies` to the ui project config (producer `qf-studio/pilot-console`, `contract_files` incl. `src/lib/api/types.ts`) + example block in `configs/pilot.example.yaml`. Gate no-ops until configured.
- **New candidate leg from GH-5032/PR#5043 (08-20 night): scope-completeness** — an issue closed as delivered while 3 of 4 body-required items were undelivered (review caught it; residual re-filed as #5049). Delivery evidence today checks the diff against the surface, not the diff against the FULL requirement list.
- **15:2xZ autopilot wedge + recovery**: autopilot merged **#5017 BEFORE #5016** (its stacked superset — the exact order the review forbade), stranding #5016 CONFLICTING/failed with #5012 flagged needs-manual-rebase. Parity verified (empty diff 512c40bd↔main for executor+memory) → **gate content fully live via #5017's merge `3d901e00`**; #5012 closed completed, #5016 closed superseded-by-merge (explicit do-not-retry note). #5019's decomposition: #5020→PR#5023 docblock fix MERGED · #5021 crack-fix PR#5024 was mis-based → closed-to-rearm (designed recovery) · #5022 fetcher wiring running. **New order-violation variant for the daemon-rule candidate list: stacked-SUPERSET PR merged before its base** (3rd sequencing incident today; family: sequential-gates-on-execution-not-merge + PR#76 stacked-base).
- **[console#195](https://github.com/qf-studio/pilot-console/issues/195)** → PR#196 merged, **APPROVE-w-notes** (reviewer ran the gate empirically: `--self-test` 4/4, counterfactual on PR#203's diff fails correctly — gate PROVEN live on the first post-merge DTO change; note: `--self-test` itself not in CI).
- Detection tradeoff (recorded): no auto-classification of "DTO-consuming diff" — `contract_files` is an explicit allowlist grown per incident, trading recall for zero false positives.
- Incident memory: `.agent/knowledge/memories/pitfalls/consumer-docblock-trusted-over-producer-wire-source.md`

## Inputs owed by TASK-459 Phase 4

Phase 4's inventory pass records the success-side sites (`AutoMerger.MergePR`, `pilot-done`/`LabelDone` writes, decomposed-epic parent close at `controller.go` ~:3702) with their evidence column honestly labeled **"green CI — decorative for the delivery claim"**. Those rows are this task's starting inventory; do not re-derive them.

## Out of Scope

- Everything TASK-459 owns: false-failure gating, `Verdict` plumbing in the CI-failure path, status-vocabulary parity, the destructive-calls grep gate.
- Re-litigating merged mitigations: spec-guard epic, decomposition-integrity waves 1+2.

## Refs

- Scope decision: 2026-08-08 founder call — separate track, not TASK-459 Phase 5 (recorded in `TASK-459` § Technical Decisions)
- Incident memory: `.agent/knowledge/memories/pitfalls/mem-151.md` (TASK-398/#4199 scaffold-only false completion)
- Sibling: `.agent/tasks/TASK-459-irreversible-action-audit.md` (false-failure side; Phase 4 feeds this task's inventory rows)
- Prior art: spec-guard epic (2026-07-30) · decomposition-integrity waves · re-dispatch fix TASK-399/#4203

**Last Updated**: 2026-08-20
