# Sonnet 5.5 on the box — running cut (updated 2026-09-29, n=6)

Box default model `claude-sonnet-5` → `claude-sonnet-5-5` since 2026-09-28 20:41Z. Source: `executions` on the box (`model_name`, `duration_ms`, `tokens_total`, `estimated_cost_usd`; `duration_ms` includes quality gates) + PR additions via `gh` (the `lines_added` column was empty until pilot#5477 shipped 09-28 22:55Z; rows after the next train will carry it natively).

## All Sonnet 5.5 runs so far

| run | repo | PR lines + | minutes | lines/min | tokens/line | $/100 lines | outcome |
|---|---|---|---|---|---|---|---|
| GH-345 console#346 | pilot-console | 686 | 5.2 | 132 | 29 | 0.16 | first-try green, APPROVE-w-notes |
| GH-191 ui#192 | pilot-console-ui | 986 | 6.4 | 154 | 24 | 0.11 | first-try green, APPROVE-w-notes |
| GH-5477 pilot#5478 | pilot | 233 | 22.4 | 10 | 44 | 0.28 | first-try green, APPROVE-w-notes (pilot repo: heavy Go suite dominates wall clock; classified `epic`) |
| GH-193 ui#194 | pilot-console-ui | 54 | 6.2 | 9 | 94 | 0.61 | first-try green, APPROVE-w-notes |
| GH-195 ui#196 | pilot-console-ui | 178 | 6.6 | 27 | 43 | 0.21 | first-try green, APPROVE-w-notes |
| GH-521 auth#522 | auth-service | 256 | 6.3 | 41 | 49 | 0.21 | first-try green, APPROVE-w-notes |

**6 of 6 completed, 0 failed, 0 retries, 0 ci-fix loops.** Sonnet 5 since 09-14: 7 failed of 52 (13%).

## Like-for-like by PR size (same four repos, same issue author, all `complex`)

Wall-clock has a 4–6 min floor (worktree, gates, PR) whichever model, so lines/min only separates the models once the PR is big enough. Sonnet 5 figures are same-day (09-28) runs; tokens/line uses all 15 same-day Sonnet 5 PRs.

| PR size | Sonnet 5 lines/min (median) | Sonnet 5.5 lines/min | speedup |
|---|---|---|---|
| under 100 lines | 9 (n=6) | 9 (n=1) | none — overhead-bound |
| 100–400 lines | 18 (n=5) | 27 (n=3; 41 excluding the pilot-repo run) | ~1.5–2× |
| over 500 lines | 47 (n=4) | 143 (n=2) | ~3× |

| metric | Sonnet 5 | Sonnet 5.5 | ratio |
|---|---|---|---|
| tokens per line of diff (median) | ~136 | ~44 | 3× fewer |
| $ per 100 lines (median) | ~0.90 | ~0.21 | ~4× cheaper |
| token throughput, complex runs (median) | 2.1k tok/min (n=44) | 3.8k tok/min | 1.8× |
| duration, complex runs (median) | 11.7 min (n=44) | 6.3 min (n=6) | 1.9× (mix differs) |

Reading: the win is mostly **fewer tokens per delivered line** (less exploration and retry churn), with a smaller raw-throughput gain on top. On small fixes the fixed pipeline cost hides it; on feature-sized PRs it is a clear 3×.

## Caveats
- n=6, all with unusually precise specs (verified ground-truth line refs). Reassess at ~20 runs.
- Two GH-5477 quirks, not model-related: classified `epic` for a single-file fix, and the pilot repo's own test suite makes its wall clock incomparable.
- Effort classifier hit one 429 on the direct API (first run) and fell back to subprocess in 7 s.
- The 09-28 first cut (n=2) is superseded by this table; numbers there stand.

## Re-cut
Same aggregation (script in the 09-28/29 session: `executions` rows by `model_name` + PR additions) after a week; add failure rate, ci-fix loops and review verdicts, not just duration. `lines_added` will be native after the next train.
