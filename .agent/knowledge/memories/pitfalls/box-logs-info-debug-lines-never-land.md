---
name: box-logs-info-debug-lines-never-land
description: The founder box daemon runs logging.level info and has never written a DEBUG line; any observability that matters (shadow-mode per-item data, flip decisions) must log at INFO or it does not exist in production (found 2026-10-01, #5556)
type: pitfall
---

# Pitfall: the box logs at info — DEBUG lines never land, so "logged at debug" means "not recorded"

## Summary
`~/.pilot/config.yaml` on the founder box sets `logging.level: info`, and `grep -c level=DEBUG daemon.log` is 0 across the whole file. A feature that records its evidence at DEBUG (PR #5549's per-item Jev shadow lines, specified by me in #5548) produces nothing in production. Treat DEBUG as developer-only; anything operators or a rollout decision depend on goes at INFO with a stable `msg` and bounded attrs.

## Context
2026-10-01: the first live gate-2 hold (GH-5553) needed the per-span line to label a low-confidence result; the line existed in code (`base_presence_classifier.go`, `r.log.Debug`) and was invisible. Filed #5556 to promote both classifiers' per-item lines to INFO. The same class: the per-denial line in the gh-guard audit (#5544) is DEBUG, so the DB event is the only record.

## Recommended Approach
When specifying a log line that feeds a decision (shadow counters, per-item reasons, suppressed counts), say INFO explicitly and add a test that captures the logger at INFO. Before trusting "it is logged", run `grep -c level=DEBUG` on the box log once.

## Related
- #5556, #5548, PR #5549, PR #5544 review note 4
- memories/pitfalls/jev-identical-questions-return-identical-answers.md
