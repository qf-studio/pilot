# Sonnet 5.5 on the box — first cut (2026-09-28, n=2)

Box default model switched `claude-sonnet-5` → `claude-sonnet-5-5` at 20:41Z (config edit + restart). Source: `executions` table on the box (`model_name`, `duration_ms`, `tokens_total`, `estimated_cost_usd`) + PR additions via `gh`. Same day, same three repos, same issue author, all `complex`.

## Like-for-like (same-day Sonnet 5 PRs over 500 lines vs the two Sonnet 5.5 runs)

| run | model | PR lines + | minutes | lines/min | tokens/line | $/100 lines |
|---|---|---|---|---|---|---|
| GH-335 console#337 | sonnet-5 | 537 | 14.7 | 37 | 80 | 0.40 |
| GH-186 ui#187 | sonnet-5 | 648 | 13.8 | 47 | 59 | 0.50 |
| GH-341 console#342 | sonnet-5 | 821 | 17.1 | 48 | 69 | 0.49 |
| GH-518 auth#519 | sonnet-5 | 2185 | 44.2 | 49 | 55 | 0.47 |
| **GH-345 console#346** | **sonnet-5-5** | 686 | 5.2 | **132** | **29** | **0.16** |
| **GH-191 ui#192** | **sonnet-5-5** | 986 | 6.4 | **154** | **24** | **0.11** |

- Wall-clock per line of diff: **~3× faster** (≈47 → ≈140 lines/min).
- Token throughput: 3.8k tok/min vs Sonnet 5 median 2.4k (complex, 3 repos, since 09-14, n=33) — 1.6×. The rest of the speedup is fewer tokens per line (≈2.4× fewer), i.e. less exploration/retry churn, not just faster output.
- Cost per 100 lines: **~3–4× cheaper** ($0.47 → $0.13).
- Quality: both first-try CI green, both reviewed APPROVE-w-notes, no ci-fix loops, no retries. Sonnet 5 the same day: 1 of 4 big PRs needed a revision issue (#520).

## September baseline (Sonnet 5, completed, non-canary, since 09-01, n=168)
median 15.0 min · mean 19.8 · p90 32.7 · median 31.5k tokens · median $2.13 · failed 13 / 174 completed (7%).
Sonnet 5.5 (n=2): 5.8 min · 22k tokens · $1.11.

## Caveats
- n=2. Both tasks had unusually precise specs (verified ground-truth line refs). Do not generalise before ~20 runs.
- `lines_added` in `executions` is 0 for every row — the column is not populated; PR additions were fetched from GitHub instead. Candidate fix.
- Effort classifier hit a 429 on the direct API on the first 5.5 run, fell back to subprocess (7 s). Not model-related.
- Complexity classifier said `medium` for GH-345, execution recorded `complex` (workflow override). Unexplained.

## Re-cut
Re-run the same aggregation (script in the 09-28 session: tokens/min + PR additions) after a week on 5.5; compare failure rate and ci-fix loop count, not just duration.
