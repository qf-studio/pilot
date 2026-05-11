---
name: Prometheus disappearing-series ghost values
description: Labeled Prometheus gauges must pre-emit zero for every defined label value, or lookback creates ghost readings when labels stop being scraped
type: pattern
originSessionId: 153408a7-5a1b-44fd-badd-ac76fc771e50
---
When exporting a Prometheus gauge with dynamic labels, the exporter must emit a line for **every defined label value on every scrape** — including `=0` for values not currently present. If a label disappears from one scrape to the next, Prometheus does NOT auto-zero it; the default lookback window (5 min) holds the last scraped value alive in queries. A label that briefly held `1` continues reading `1` for up to 5 minutes after the underlying state cleared.

**Why:** This bit us in TASK-60 follow-up (#3006, 2026-05-11, shipped v2.146.5). Grafana's `Active PRs by stage` panel showed all 4 transit stages at `=1` for a single PR. Looked like a controller-side gauge leak. `navigator-research` proved the controller was clean — the in-memory map at `internal/autopilot/metrics.go:168-177` resets every poll and is correctly keyed by PR number. Root cause was that the exporter at `internal/gateway/prometheus.go:127-132` only emitted `pilot_active_prs{stage=X}` for stages present in the map. As a PR traversed `waiting_ci → ci_passed → merging → merged` inside the lookback window, each prior stage's last scraped `=1` stayed alive. Result: ghost series, no leak.

**How to apply:** Whenever adding or reviewing a labeled Prometheus gauge in `internal/gateway/prometheus.go`:
1. Enumerate the closed set of label values (from a const block or accessor like `autopilot.AllPRStages()`)
2. After the range loop that emits present values, add a sweep that emits `0` for every defined value not in the map
3. Mirror the existing pattern at `prometheus.go:41-46` (`pilot_issues_processed_total` for `success`/`failed`/`rate_limited`) and `prometheus.go:88-92` (`pilot_approval_persist_misses_total` for `request_id`/`decision`)
4. Add a test case that snapshots with a single label set, then asserts at least one absent label appears as `=0` in the rendered output

This rule only applies to **gauges with a closed label set**. Counters are fine because they're monotonic; open-set labels (e.g. `endpoint`, `model`) can't be pre-enumerated and inherit lookback semantics by design.

If the label set is genuinely open or unbounded, prefer a counter and let consumers compute rates, or document the lookback semantics in the metric help string.
