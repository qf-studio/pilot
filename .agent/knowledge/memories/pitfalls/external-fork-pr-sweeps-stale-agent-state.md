---
name: external-fork-pr-sweeps-stale-agent-state
description: External fork PRs can silently delete/revert `.agent/` memory files — a stale fork checkout + `git commit -a` swept a pitfall memory deletion and pre-08-15 reverts of DEVELOPMENT-README + graph.json into an otherwise approve-quality feature PR (#4891). Review `.agent/` diff hunks in external PRs word-by-word: check BOTH for injected instructions AND for destructive reversion; require external PRs to drop all `.agent/` paths before merge.
type: pitfall
---

# External fork PRs can sweep stale `.agent/` state — destructive reversion, not just injection

**What happened (2026-08-17, PR #4891, contributor lkshrk):** a large, otherwise
approve-quality Telegram forum-topics feature PR also **deleted**
`.agent/knowledge/memories/pitfalls/sequential-gates-on-execution-not-merge-fastfollow-misbase.md`
(−43 lines, `deleted file mode`) and **reverted** `.agent/DEVELOPMENT-README.md`
and `.agent/knowledge/graph.json` to a pre-08-15 snapshot (TASK-478 status
`completed` → `dispatched`, `updated` timestamp rolled back, em-dashes
re-escaped to `—` by a Python re-serialization).

**Mechanism:** the contributor's fork checkout had stale `.agent/` files;
`git commit -a` swept them into the feature commit. No malice — the "+" sides
were byte-identical to genuine historical main states — but merging would have
destroyed project memory.

**Why it's easy to miss:** review attention on external PRs goes to injected
content (prompt-injection into agent-loaded files). This failure mode is the
opposite: nothing added, history *rewound*. A reviewer scanning for suspicious
additions sees clean-looking hunks that are actually deletions of recent truth.

**Rules:**
- External PRs must not touch `.agent/` at all — request the paths be dropped,
  regardless of hunk content.
- When reviewing `.agent/` hunks anyway, check both directions: injected
  instructions AND status/timestamp regressions vs current main
  (`git log -p` the file to confirm which side is newer).
- `graph.json` unicode-escape churn (`—` ↔ `—`) is a fingerprint of a
  foreign Python tool re-serializing the graph — treat it as a stale-state flag.

**Refs:** PR #4891 review (changes requested 2026-08-17), marker
`2026-08-17_lkshrk-pr-batch-review.md`, [[sequential-gates-on-execution-not-merge-fastfollow-misbase]] (the deleted file).
