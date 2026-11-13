# TASK-653: One price table — the same rates for the cost estimate, the dashboard card and the replay recorder

**Status**: 🚀 **P1 DISPATCHED 2026-11-13** → [#6355](https://github.com/qf-studio/pilot/issues/6355) (`pilot` + `no-decompose`); normal review
**Created**: 2026-11-13
**Assignee**: Pilot

---

## Context

**Problem**: three places turn tokens into dollars with three tables. The executor table knows the 5.x models; the dashboard card and the replay recorder do not and price `claude-opus-5-5` at $5/$25 and `claude-sonnet-5-5` at $3/$15. The executor prices a Sonnet 5.5 cache read at $0.20 per MTok where the Anthropic price reference gives $0.10. Four runs on 2026-11-13 show the effect: the stored estimate against the cost that the CLI reported was 1.11 against 1.32 (Opus 5.5) and 1.96 against 1.55, 1.14 against 1.06, 3.90 against 3.39 (Sonnet 5.5).

**Goal**: one table in one leaf package; the three call sites use it; the estimate for a 5.x run is within a few percent of the reported cost.

**Why now**: the box routes `complex` tickets to Opus 5.5 since 2026-11-13 as a test. The comparison with Sonnet needs dollar figures that the table does not bias.

---

## Known Pitfalls & Patterns

- **PITFALL** (research 2026-11-13): `memory.CalculateTokenCost` in `internal/memory/metering.go` is a flat customer rate, not a model price. → P1 does not touch it.
- **PITFALL** (research 2026-11-13): the whole run is priced at one model name; tokens of a helper model are priced at the main rate. → out of scope for P1, stated in the leg.
- **LEARNING** (mem-309): check the source before a statement. → P1 states the source of each rate and pins each in a test.

---

## Acceptance Criteria

- [ ] One package holds every model rate; no second table stays in `internal/`.
- [ ] `claude-opus-5-5`, `claude-sonnet-5-5` and `claude-haiku-5-5` have the same rates at all three call sites.
- [ ] The 1-hour cache write is priced at 2× input when the token count is available.

---

## Implementation

### Phase 1: leg P1 (Pilot)

- [ ] **P1** one price table → [#6355](https://github.com/qf-studio/pilot/issues/6355), dispatched 11-13.

---

## Legs — pilot (daemon)

### LEG P1 — repo: qf-studio/pilot — title: fix(cost): one price table for the executor estimate, the dashboard card and the replay recorder — 5.x models everywhere, Sonnet 5.5 cache read rate, 1-hour cache write

probe: present qf-studio/pilot func[[:space:]]modelCacheReadPrice\(
probe: present qf-studio/pilot func[[:space:]]EstimateCost\(
probe: absent  qf-studio/pilot ^package[[:space:]]pricin[g]

## Context

Three places turn tokens into dollars, each with its own table:

- modelPricing, modelCacheReadPrice and estimateCostWithCache in `internal/executor/runner.go`. This is the source of the column estimated_cost_usd. It knows the 5.x models. It returns a cache read rate of $0.20 per million tokens for every name that holds sonnet-5; the comment calls that 10% of the input rate, but the input rate is $2. A cache write is priced at 1.25 times the input rate; the 1-hour cache write (2 times the input rate) is not read from the usage data.
- EstimateCost in `internal/memory/metrics.go`, used by the cost card of the dashboard (`internal/dashboard/tui.go`). It has no case for a 5.x model: claude-opus-5-5 gets $5/$25 through the general opus case and claude-sonnet-5-5 gets the default $3/$15. It prices no cache tokens.
- estimateCost in `internal/replay/recorder.go`, with the same gaps.

The file `scripts/bench/prices.json` also lists the Sonnet 5.5 cache read at 0.20. The flat rate in `internal/memory/metering.go` is a customer rate and is not part of this issue.

Rates per million tokens (Anthropic first-party list, checked 2026-11-03): claude-opus-5-5 input 4, output 20, cache read 0.20 · claude-sonnet-5-5 input 2, output 10, cache read 0.10 · claude-sonnet-5 input 2, output 10, cache read 0.20 · claude-haiku-5-5 input 0.10, output 0.50 for prompts up to 100,000 tokens. A cache write for 5 minutes costs 1.25 times the input rate; a cache write for 1 hour costs 2 times the input rate.

## Scope

1. A new leaf package, plain text: internal/pricing. It imports nothing from this repository. It holds one table and these functions: the input and output rate for a model name, the cache read rate, and a cost function that takes input, output, cache read, 5-minute cache write and 1-hour cache write token counts. Keep the present match rule (lower case, substring, the more specific name first, so that claude-opus-5-5 never falls to an older opus case; a provider prefix or a date suffix still matches). Move every rate that the executor table has today; do not drop a model.
2. Rates: add or correct the rates of Context. For a model with no explicit cache read rate, the cache read rate is 10% of its input rate. For an unknown model keep the present default and log nothing new.
3. The three call sites become thin wrappers of the package: modelPricing, modelCacheReadPrice and estimateCostWithCache in the executor; EstimateCost in the memory package; estimateCost in the replay package. Their signatures stay, so callers do not change.
4. 1-hour cache write: where the usage data of the CLI stream is parsed (`internal/executor/backend_claudecode.go`), read the 1-hour cache write count when the stream gives it (the usage object has a cache_creation object with the fields ephemeral_5m_input_tokens and ephemeral_1h_input_tokens) and pass both counts to the cost function. When the stream gives only the total, price it as today (5 minutes).
5. Correct the Sonnet 5.5 cache read value in `scripts/bench/prices.json` and the comment of the cache read function.
6. Tests in the new package, table-driven: each model of Context with its three rates; claude-opus-5-5 with a provider prefix and with a date suffix; a model with no explicit cache read rate; an unknown model; a cost with all five token kinds for one Opus 5.5 and one Sonnet 5.5 example, with the expected dollars written in the test. One test per wrapper proves that it returns the package value for claude-opus-5-5 and claude-sonnet-5-5. Update the tests that pin the old values (TestEstimateCostCacheReadRate and TestModelPricing in the executor tests, the replay recorder tests, the dashboard tests).

## Acceptance

- [ ] `go test -race -count=1 ./internal/pricing/ ./internal/executor/ ./internal/memory/ ./internal/replay/ ./internal/dashboard/ -run 'Pric|EstimateCost|ModelPricing|CacheRead|Cost'` passes; paste the output into the PR body.
- [ ] No model rate stays in a non-test Go file outside the new package (the flat customer rate of the metering code excepted); state in the PR body how you checked it.
- [ ] Give claude-sonnet-5-5 a cache read rate of 0.20 again -> a test fails.
- [ ] Remove the 5.x case from one wrapper -> its test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The flat customer rate in the metering code and the budget enforcer. A price per model inside one run (tokens of a helper model stay priced at the main rate). The tiered Haiku rate over 100,000 tokens. The price tables in the docs pages. A fetch of prices from the network.

## Refs

- Navigator TASK-653 in this repo

---

## Out of Scope

- Budget enforcement by real model cost (the enforcer uses a flat rate; a product decision).
- Docs pages with price tables.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Home of the table | executor; memory; a new leaf package | new leaf package `internal/pricing` | `executor` imports `memory` and `replay`; a leaf package gives no import cycle |
| Call sites | change every caller; keep the three functions as wrappers | wrappers | no caller changes; the tests of callers stay |
| Sonnet 5.5 cache read | 0.20 (code and bench file); 0.10 (Anthropic reference of 2026-11-03) | 0.10 | the reference names the cache read as the one price that changed from Sonnet 5 |

---

## Verify

```bash
go test -race -count=1 ./internal/pricing/ ./internal/executor/ ./internal/memory/ ./internal/replay/ ./internal/dashboard/ -run 'Pric|EstimateCost|ModelPricing|CacheRead|Cost'
```

---

## Done

- [ ] P1 merged and released.
- [ ] On the box, for ten runs after the release, `estimated_cost_usd` is within 10% of `reported_cost_usd`.

---

## Refs

- `internal/executor/runner.go` · `internal/memory/metrics.go` · `internal/replay/recorder.go`

---

**Last Updated**: 2026-11-13

## Refs

- P1 → https://github.com/qf-studio/pilot/issues/6355
