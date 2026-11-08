# TASK-581: Bootstrap unit economics — measure deep-research cost per repo size, set the per-repo cap and the degrade path

**Status**: 🚀 HARNESS DISPATCHED 2026-10-21 → [#5788](https://github.com/qf-studio/pilot/issues/5788); the live 4-repo run is an operator step on the founder box (spends real Opus tokens); caps below are provisional until the run lands.
**Created**: 2026-10-21
**Assignee**: Navigator plans · Pilot builds the harness · founder runs the matrix and decides D1–D5
**Parent**: [TASK-549](TASK-549-legacy-repo-navigator-bootstrap.md) (legacy-repo bootstrap) · **Sibling**: [TASK-546](archive/PILOT-CLOUD-TASKS-MOVED.md) (pricing v3, D11) · [TASK-405](archive/PILOT-CLOUD-TASKS-MOVED.md)
**Decision source**: .agent/system/project/project_pilot_cloud_positioning.md (plain text: not on main yet) § 2, item "OPEN — bootstrap unit economics"

---

## Context

**Problem**: Pilot Cloud ($499/mo, 100 shipped tickets, platform key, $300 fair-use pool) offers a free, one-time bootstrap at signup: Pilot researches the customer's repo and proposes the first ~10 tickets with `file:line` evidence. Founder call 2026-10-21: the bootstrap must be a **deep** research or it does not sell. A deep research on Opus over a large repo has an unknown cost, and a free offer with an unknown unit cost cannot go live.

What exists today (verified 2026-10-21):

- `pilot navigator bootstrap` (TASK-549 phase 2, #5679, merged) runs one session capped at 60 turns / 10 min that writes **only** `.agent/` context: README sections, an architecture note, 3–10 pattern memories, `graph.json` (`internal/executor/navigator_bootstrap.go`, `internal/executor/navigator_bootstrap_prompt.go`). It proposes **no tickets**. Its result carries input/output/cache-write/cache-read tokens, a token-based `CostUSD` and the CLI-reported `ReportedCostUSD`.
- The chat turn (`internal/executor/chat_turn.go`) is a read-only session, 40 turns / 5 min, with the research method and an issue-authoring contract in `internal/executor/chat_prompt.go` (H2 sections, `path:line` citations, every acceptance bullet names a test, ≤ ~500 changed lines per issue, `Blocked by: #N`). It drafts one issue per turn on request.
- Nothing chains the two into "research → ~10 proposed tickets", and nothing measures what that costs.
- The executor's price table is stale: `modelPricing` in `internal/executor/runner.go` prices `opus-5*` at $5/$25 and `sonnet-5*` at $3/$15, with cache reads at 10% and cache writes at 125% of input. Current list prices (claude-api skill, 2026-10): Opus 5.5 $4/$20, Sonnet 5.5 $2/$10, cache reads $0.20/MTok on both. The token estimate overstates Opus by ~25% and Sonnet by ~50%. **The measurement must record the CLI-reported USD as the primary figure** and keep the token counts so any price table can be applied later.

**Goal**: a measured cost curve for the deep bootstrap across four repo sizes, a per-repo USD cap for the free offer, a degrade path that keeps the bootstrap "deep" where it matters when the cap binds, and the break-even against the $499 plan.

**Definition — the Cloud bootstrap is three stages** (the harness measures each stage separately):

| Stage | Session | Model | Output | Caps (proposed) |
|---|---|---|---|---|
| 0 Context | existing `pilot navigator bootstrap` | box default (Sonnet 5.5) | `.agent/` README sections, architecture note, pattern memories, `graph.json` | 60 turns / 10 min (as shipped) |
| 1 Deep research | read-only session, `ChatReadOnlyTools` | **Opus 5.5** | a research brief under `.agent/system/` (plain text name: bootstrap-research.md): 15–25 candidate findings, each with ≥ 2 `path:line` refs, a size class (S/M/L) and a "why now" line; written incrementally so a cut-off still yields output | 150 turns / 20 min, or the USD cap |
| 2 Ticket drafting | read-only session, resumes nothing; input = the brief | **Sonnet 5.5** | ~10 tickets in the chat issue contract (H2 sections, `file:line` evidence, acceptance bullets with tests, `#N` deps), one file each under `.agent/tasks/` (plain text name: bootstrap/ticket-NN.md); nothing filed (positioning D2: Needs You first) | 40 turns / 5 min |

"Deep" is a measurable bar, not an adjective: see § Acceptance Criteria (coverage, evidence validity, ticket count).

---

## Known Pitfalls & Patterns

- **PITFALL** (TASK-549, `base-presence-gate-false-positives-hold-then-silent-needs-human`) — any backticked x/y.ext token in a Pilot issue body must exist on main or the dispatch gate holds. → This doc is the issue body: files to create are named in plain text; only files that exist on main are backticked.
- **PITFALL** (TASK-491, executor env scrub) — a Pilot-executor session cannot be relied on to hold a platform key or to spend money. → The Pilot issue delivers the harness plus fixture-driven tests; the live matrix is an operator step. A smoke run on the smallest repo is a documented manual step, not an acceptance bullet.
- **PITFALL** (this doc) — `estimateCostWithCache` uses stale prices. → Report CLI-reported USD first; the report tool takes a prices JSON so the table can be re-priced without a rerun.
- **PATTERN** (TASK-549 D3, TASK-546 D11) — bootstrap is priced in tickets, not dollars, and its dollars flow to the fair-use ceiling. → The cap here is an **internal** cost control; nothing in customer copy shows dollars (positioning § 3, no-dollars rule).
- **LEARNING** (`connect-means-wire-the-integration-not-create-work-in-product-repos`) — the bootstrap never opens PRs or files issues in the customer repo. → Stage 2 writes drafts to `.agent/`; the console's spec lane files them after approval.
- **PATTERN** (`scripts/canary-report.py` + `scripts/canary_report_test.py`) — bench reports are Python with a test file beside them, run via `python3 -I`. → Same shape for the bootstrap report tool.

---

## Measurement plan

### Repos (public, permissive, read-only; LOC is approximate — the script records the `scc` count)

| Class | Repo | Stack | ~LOC | Why |
|---|---|---|---|---|
| S | `jmoiron/sqlx` | Go | ~6k | one package, a floor for the fixed cost of a bootstrap |
| M | `remix-run/react-router` | TS monorepo | ~60k | the product-team / agency shape (segments 1–2) |
| L | `cli/cli` | Go | ~180k | many packages, deep command tree |
| XL | `grafana/grafana` | Go + TS | ~1.3M | the case the cap exists for; mixed stack |

Alternates if a repo turns out unsuitable (build-time generated code, vendored trees): `django/django` (L, Python), `moby/moby` (XL, Go).

### Runs

- Each repo × each stage, **2 runs** (n=2 catches a 2× variance; a third run only where the two differ by > 40%).
- Stage 1 on Opus 5.5 at effort `high` (Claude Code default is `xhigh`; record which was used). One extra Stage 1 run per repo on **Sonnet 5.5** to price the fallback.
- XL also runs with the degrade path on: `--scope <top-3 dirs by 12-month churn>` (see § Degrade path) so the matrix shows the degraded cost next to the full cost.
- Total: 4 repos × 3 stages × 2 runs + 4 Sonnet-fallback runs + 2 XL-scoped runs = **30 sessions**. Budget guard: `--max-usd` per session (default 40) and a matrix guard (default 250); the script stops when either is hit and writes what it has.

### Metrics per session (one JSON line per run, then a markdown table)

| Metric | Source |
|---|---|
| repo, class, stack, `scc` LOC and file count | script |
| stage, model, effort, turn cap, timeout | script |
| input / output / cache-write / cache-read tokens | Claude Code `result` event usage |
| USD reported (`total_cost_usd`) and USD estimated (prices JSON × tokens) | result event; report tool |
| wall time, turns used, `partial` (cut by turns/timeout) | result event + timer |
| distinct files read, top-level dirs touched / top-level dirs total (**coverage**) | tool-use events (`Read`/`Grep` paths) |
| findings (stage 1), tickets proposed (stage 2) | parse the outputs |
| `path:line` refs per ticket (**evidence density**), refs that resolve to an existing path and in-range line (**evidence validity**) | report tool checks against the clone |

### Deliverables of the Pilot issue (harness only, no live spend)

1. **Runner script** — plain text name: scripts/bench/bootstrap-cost.sh. Clones each repo shallowly (`git clone --depth 1`) into a scratch dir outside the repo, counts LOC with `scc` (falls back to `cloc`), runs Stage 0 via `pilot navigator bootstrap --project <clone>`, runs Stage 1 and Stage 2 via `claude -p --output-format stream-json --allowedTools <read-only list> --max-turns N --model <id>` with the prompt files below, and appends one JSON line per session to results/<date>.jsonl (plain text name). Flags: `--repos`, `--runs`, `--stages`, `--model-research`, `--model-draft`, `--scope <dir,...>`, `--max-usd`, `--matrix-max-usd`, `--dry-run` (prints the commands, spends nothing).
2. **Prompt files** — plain text names: scripts/bench/prompts/stage1-research.md and scripts/bench/prompts/stage2-draft.md. Stage 1 = the chat persona and research method from `internal/executor/chat_prompt.go` plus: write the brief incrementally; list top-level dirs first and record coverage; stop exploring at 80% of the turn cap and write up. Stage 2 = the chat issue contract from the same file, applied to the brief, target 10 tickets, min 5, each with ≥ 3 valid `path:line` refs. These files are the spec for the later product leg (TASK-549 phase 2b); the product lifts them verbatim.
3. **Report tool** — plain text name: scripts/bench/bootstrap_report.py (PEP 8, type hints, dataclasses) with a test file beside it, plain text name: scripts/bench/bootstrap_report_test.py. Reads the JSONL, validates refs against the clone when present, applies a prices JSON (plain text name: scripts/bench/prices.json, seeded with the list prices above and a `source`/`as_of` field), and writes the markdown table to stdout. Tests run on a fixture (plain text name: scripts/bench/testdata/bootstrap-runs.jsonl) covering a full run, a partial run, an unresolvable ref, and the matrix guard.
4. **Makefile target** `bench-bootstrap-report` that runs the report tool over the latest JSONL (`Makefile` already runs Python scripts under `python3`).
5. **Results note** — the operator pastes the table into a new file under `.agent/system/bench/` (plain text name: bootstrap-cost-<date>.md). The harness does not write into `.agent/`.

### Operator step (founder box, after the harness merges)

```bash
# on the box, platform key in env, ~1–2 h wall time, ≤ $250 by the matrix guard
scripts/bench/bootstrap-cost.sh --repos sqlx,react-router,cli,grafana --runs 2 --max-usd 40 --matrix-max-usd 250
scripts/bench/bootstrap-cost.sh --repos grafana --stages 1 --scope "pkg/api,pkg/services,public/app/features" --runs 2
make bench-bootstrap-report > /tmp/bootstrap-cost.md
```

---

## Cost model and proposed cap (provisional — replace with measured p50/p90)

Assumptions: Opus 5.5 $4 in / $20 out / $0.20 cache read / $5 cache write; Sonnet 5.5 $2 / $10 / $0.20 / $2.50. A read-only agentic session re-reads its growing context every turn, so cache reads dominate input; per turn the context is ~100–200k tokens.

| Stage | Model | Turns | Est. USD (M repo) | Est. USD (XL repo) | Driver |
|---|---|---|---|---|---|
| 0 Context | Sonnet 5.5 | ≤ 60 | $2–4 | $3–5 (turn-capped) | cache reads ~7M tok |
| 1 Deep research | Opus 5.5 | ≤ 150 | $10–20 | $30–50 | cache reads ~20M tok + thinking output |
| 2 Drafting | Sonnet 5.5 | ≤ 40 | $1–3 | $2–4 | output-heavy, small input |
| **Total** | | | **$13–27** | **$35–60** | |

Proposed, pending the run:

- **Soft cap $25 per repo** — Stage 1 receives a budget line and stops exploring when the running USD (summed from stream `result` usage, or turns as a proxy) reaches 80% of it. S/M/L repos should land under it at full depth.
- **Hard cap $40 per repo** — the session is killed; Stage 2 runs on whatever brief exists (incremental write makes that safe).
- **One bootstrap per org, ever, for free**; a rebuild costs 10 pack tickets (TASK-549 D3). The free run requires a connected GitHub App installation and a provisioned box (both already gate signup), so the free Opus endpoint is not reachable without a real tenant.

### Break-even against the $499 plan

The bootstrap is a one-time acquisition cost, not a per-ticket cost. First-month gross margin per paying tenant ≈ $499 − box (~$50) − token usage (worst case the full $300 pool) = **$150 worst case, ~$350 typical** (pool half used).

| Cap per bootstrap | Min. bootstrap→paid conversion, worst-case margin ($150) | Min. conversion, typical margin ($350) |
|---|---|---|
| $15 | 10% | 4% |
| $25 | 17% | 7% |
| $40 | 27% | 11% |
| $60 | 40% | 17% |

Reading: at a $25 cap the free bootstrap pays for itself in the first month if ≥ 1 in 6 bootstraps converts under the worst-case margin, or ≥ 1 in 14 under the typical margin. Above $40 the offer needs conversion rates no free trial reaches. **The cap is a conversion-rate bet; the measurement tells us which repo sizes fit inside it at full depth.**

---

## Degrade path when the cap binds (ordered; stop at the first that fits)

1. **Plan by size before spending** — `scc` runs first (free). S/M/L (≤ ~300k LOC): full depth. XL: go straight to step 2; the console shows "Large repo: Pilot will focus on the most active areas" before the run.
2. **Scope by directory, chosen by churn** — top-N top-level dirs by commits in the last 12 months (`git log --since` is in the read-only allowlist) plus entry points; N chosen so the scoped LOC ≤ ~300k. Stage 1 reads only those dirs, says so in the brief, and lists the dirs it skipped. The customer can pick dirs instead (console input; TASK-549 phase 4 UI).
3. **Sample within scope** — inside the chosen dirs, the research method already samples (read entry points, grep, follow references); the prompt budget line turns sampling up (fewer full-file reads, more grep) at 60% of the cap.
4. **Fewer tickets** — Stage 2 drafts as many tickets as the evidence supports, minimum 5. Under 5 valid tickets ⇒ the bootstrap is marked **partial** in the console with a "Continue on <dir>" action that costs pack tickets.
5. **Model fallback is not a degrade step** — switching Stage 1 to Sonnet mid-run loses the cache namespace and the depth claim; Sonnet Stage 1 is measured only to price a cheaper tier later.

The XL-scoped runs in the matrix test step 2 directly: the acceptance bar is that the scoped XL run meets the same evidence-validity and ticket-count bar as an L run at full depth, at ≤ the soft cap.

---

## Decisions for the founder

| # | Question | Recommendation | Why |
|---|---|---|---|
| D1 | Soft / hard cap | **$25 / $40 per repo**, re-set from p50/p90 after the run | Break-even table above; keeps S/M/L at full depth on the estimate |
| D2 | What "deep" means, as a gate | **Coverage ≥ 80% of top-level dirs (or of the scoped dirs), evidence validity ≥ 95%, ≥ 8 tickets with ≥ 3 valid refs** | A number a buyer can check beats "deep"; the matrix proves which sizes reach it |
| D3 | XL handling | **Auto-scope by churn, tell the customer, offer "pick directories"** | Keeps the free run inside the cap without a silent skim |
| D4 | Stage 1 model | **Opus 5.5**; Sonnet 5.5 only as a priced lower tier if the run shows Sonnet meets D2 at ≤ 40% of the cost | The depth claim is the sale; measure before downgrading |
| D5 | Where the free run is gated | **After GitHub App install + box provision, one per org** | Removes the free-Opus-endpoint abuse case without a card on file |

Founder answers "D1–D5 ok" or sends changes. The caps go into `executor.navigator` config (TASK-549 phase 2b) once measured.

---

## Acceptance Criteria (Pilot issue — harness)

- [ ] `--dry-run` prints every command of the full matrix (4 repos × 3 stages × 2 runs + fallback + scoped) and spends nothing; a shell test asserts the command count and that no `claude` or `pilot` binary is invoked.
- [ ] The runner writes one JSON line per session with every metric in § Metrics; a test parses a captured stream-json fixture and asserts tokens, reported USD, turns, `partial`, files read and dirs touched.
- [ ] The report tool marks a `path:line` ref invalid when the path is missing or the line exceeds the file length; the fixture includes one such ref and the test asserts evidence validity < 100% for that run.
- [ ] The matrix guard stops the runner once the summed reported USD reaches `--matrix-max-usd`; a test on the fixture asserts the stop and that the partial JSONL is still readable by the report tool.
- [ ] The report applies the prices JSON and prints both "USD reported" and "USD est." columns; a test asserts the estimate for a known token row (Opus 5.5 at $4/$20/$0.20/$5).
- [ ] Prompt files contain the research-method lines and the issue-contract lines verbatim from `internal/executor/chat_prompt.go`; a test greps both files for the "path:line" citation rule and the "Blocked by: #N" rule.
- [ ] `make bench-bootstrap-report` runs the report over the latest JSONL; `make test` includes the new Python tests (run with `python3 -I`).
- [ ] No file under `.agent/` is written by the harness; a test runs `--dry-run` from a temp copy and asserts the tree is unchanged.

## Acceptance Criteria (this task — after the operator run)

- [ ] `.agent/system/bench/` holds the dated results note with the full matrix table (30 sessions or the guard stop, stated).
- [ ] D1 caps are re-set from measured p50/p90 and written into this doc and the positioning doc's § 2 item (OPEN → decided).
- [ ] The XL-scoped runs meet the D2 bar at ≤ the soft cap, or D3 is revised.

---

## Out of Scope

- The product leg that runs Stages 1–2 on tenant boxes (TASK-549 phase 2b; this task's prompt files are its spec).
- Console UI for "pick directories" / partial bootstrap (TASK-549 phase 4).
- Fixing `modelPricing` in the executor (worth a separate `fix(executor)` issue; noted here, not bundled).
- Any customer copy; dollars never appear on the product (positioning § 3).

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|---|---|---|---|
| Where Stages 1–2 run in the bench | product command first, then bench; bench drives `claude -p` directly | **`claude -p` from the script, prompts in files** | Measures the cost now without blocking on product code; the prompt files become the product spec |
| Who spends the tokens | Pilot during the issue; operator on the box | **Operator** | Executor sessions scrub env and have a 1 h budget; a $250 matrix is a founder-approved spend |
| Primary cost figure | token estimate; CLI-reported USD | **CLI-reported, estimate beside it** | Executor price table is stale; tokens kept for re-pricing |
| Cap unit | tickets; USD; turns | **USD internally, tickets in copy** | TASK-546 D11 vocabulary stays; USD is what we actually risk |
| Degrade primary lever | fewer tickets; sampling; directory scope | **Directory scope by churn** | Keeps depth where the customer's work happens; "fewer tickets" is the visible failure, not the control |

---

## Verify

```bash
python3 -I scripts/bench/bootstrap_report_test.py
bash scripts/bench/bootstrap-cost.sh --dry-run --repos sqlx --runs 1
make bench-bootstrap-report
```

---

## Done

- [ ] Harness merged with tests; `--dry-run` matrix prints 30 commands.
- [ ] Operator run on the box, results note in `.agent/system/bench/`.
- [ ] D1–D5 decided; caps recorded here and in the positioning doc.

---

## Refs

- Decision source: .agent/system/project/project_pilot_cloud_positioning.md (plain text: not on main yet) § 2 (D1 free bootstrap, OPEN item), § 3 (no-dollars rule), § 4 (offer)
- [TASK-549](TASK-549-legacy-repo-navigator-bootstrap.md) phases 1–2, D3 (10-ticket price) · [TASK-546](archive/PILOT-CLOUD-TASKS-MOVED.md) D10/D11 · TASK-518 (archived; chat as Navigator, issue contract)
- Code: `internal/executor/navigator_bootstrap.go`, `internal/executor/navigator_bootstrap_prompt.go`, `cmd/pilot/navigator.go`, `internal/executor/chat_turn.go`, `internal/executor/chat_prompt.go`, `internal/executor/runner.go` (`modelPricing`, `estimateCostWithCache`)
- Bench shape: `scripts/canary-report.py`, `scripts/canary_report_test.py`, `.agent/system/bench/bench_cost_model.md`, `.agent/system/bench/sonnet-5-5-first-cut-2026-09-28.md`
- Pilot issue (harness): https://github.com/qf-studio/pilot/issues/5788
- Prices: claude-api skill model table (2026-10) — verify against the pricing page at run time

---

**Last Updated**: 2026-10-21
