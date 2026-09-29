# Sonnet 5.5 on the box — running cut (updated 2026-09-29, n=6)

Box default model `claude-sonnet-5` → `claude-sonnet-5-5` since 2026-09-28 20:41Z. Source: `executions` on the box (`model_name`, `duration_ms`, `tokens_total`, `estimated_cost_usd`; `duration_ms` includes quality gates) + PR additions via `gh` (the `lines_added` column was empty until pilot#5477 shipped 09-28 22:55Z; rows after the next train will carry it natively).

## Like-for-like table (same day, same four repos, same issue author, all `complex`; Sonnet 5 first, then Sonnet 5.5, each by PR size)

| run | model | PR lines + | minutes | lines/min | tokens/line | $/100 lines |
|---|---|---|---|---|---|---|
| ui#179 | sonnet-5 | 20 | 3.6 | 6 | 379 | 2.35 |
| console#344 | sonnet-5 | 46 | 4.0 | 12 | 180 | 1.61 |
| ui#185 | sonnet-5 | 74 | 6.9 | 11 | 184 | 0.99 |
| console#332 | sonnet-5 | 118 | 6.4 | 18 | 104 | 0.44 |
| console#340 | sonnet-5 | 134 | 6.6 | 20 | 166 | 0.84 |
| ui#190 | sonnet-5 | 208 | 12.1 | 17 | 196 | 1.10 |
| ui#183 | sonnet-5 | 311 | 18.6 | 17 | 122 | 1.03 |
| auth#517 | sonnet-5 | 371 | 14.4 | 26 | 75 | 0.88 |
| console#337 | sonnet-5 | 537 | 14.7 | 37 | 80 | 0.40 |
| ui#187 | sonnet-5 | 648 | 13.8 | 47 | 59 | 0.50 |
| console#342 | sonnet-5 | 821 | 17.1 | 48 | 69 | 0.49 |
| auth#519 | sonnet-5 | 2185 | 44.2 | 49 | 55 | 0.47 |
| *Sonnet 5 median (n=12)* | | | *12.9* | *18* | *113* | *0.86* |
| **ui#194** | **sonnet-5-5** | 54 | 6.2 | 9 | 94 | 0.61 |
| **ui#196** | **sonnet-5-5** | 178 | 6.6 | 27 | 43 | 0.21 |
| **pilot#5478** | **sonnet-5-5** | 233 | 22.4 | 10 | 44 | 0.28 |
| **auth#522** | **sonnet-5-5** | 256 | 6.3 | 41 | 49 | 0.21 |
| **console#346** | **sonnet-5-5** | 686 | 5.2 | 132 | 29 | 0.16 |
| **ui#192** | **sonnet-5-5** | 986 | 6.4 | 154 | 24 | 0.11 |
| *Sonnet 5.5 median (n=6)* | | | *6.4* | *34* | *44* | *0.21* |

pilot#5478 runs in the pilot repo whose Go suite dominates the gate time (22 min for 233 lines); tokens/line and $/line are unaffected by that. **Sonnet 5.5: 6 of 6 first-try green, 0 failed, 0 retries, 0 ci-fix loops.** Sonnet 5 since 09-14: 7 failed of 52 (13%).

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

## Tally 2026-09-29 (running count for the week-later re-cut)

Twelve runs on `claude-sonnet-5-5` since the switch (GH-345, 191, 5477, 193, 195, 521 on 09-28; GH-5479, 347, 5481, 5484, 5485, 5486 on 09-29): 12/12 first-try green, zero ci-fix loops, 5–16 min per PR. Reviewer verdicts on the 09-29 six: 4× APPROVE-w-notes, 1× APPROVE, 1× REQUEST-CHANGES (PR#5487: tautological test + docs left inconsistent). Speed is real; the review step still catches acceptance gaps, so the cut compares defect rate after review, not merge rate. Founder verdict 09-29: "ships like a machine gun". Note the evidence gate never ran on any of these (mem-203), so PR bodies carry no self-verification for either model.
