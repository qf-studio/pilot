---
name: review-before-approval-ordering
description: On approval-gated repos, PR review verdicts must complete BEFORE surfacing the approval ask — approval races the review and merges land with known-findable blockers
type: learning
---

# Review verdicts before approval asks (2026-07-13 incident)

**What happened:** studio-sdk PRs #88/#94/#95 (SyncCapable foundation) sat green behind
pre-merge approval. The session surfaced "needs your approval" to the founder and launched
the 3-PR review workflow at the same time. Founder approved; merges landed 18:38–18:39;
review verdicts arrived ~2 minutes later: #94 and #95 REQUEST_CHANGES with 5 correctness
blockers total (state name-vs-UUID round-trip, unpaginated idempotency scans, gt-vs-gte
delta semantics, cross-repo write scope). Post-merge fix issues sdk#96/#97 dispatched
same-hour; blast radius zero only because nothing consumes SyncCapable yet.

**Why:** approval and review ran concurrently; the human approval channel (Telegram/
dashboard) is faster than a review workflow. The race is structural, not accidental.

**How to apply:**
- On any approval-gated repo (studio-sdk, later pilot-console): run the review pass FIRST,
  deliver verdicts, and only then surface the approval ask — never in the same message,
  never in parallel.
- Spec-writing corollary: Pilot meets explicit ACs and guesses on implicit contracts. For
  SDK/public-surface issues, spell out round-trip symmetry (read value must be writable
  back), pagination of EVERY listing the code path touches (including internal scans), and
  boundary semantics (>= vs >) as literal acceptance criteria.
- Sibling-batch corollary: when N similar issues execute as a batch, review the FIRST PR
  before the siblings dispatch — shared defect classes (the unpaginated idem scan appeared
  in both #94 and #95) get caught once instead of merged twice.
