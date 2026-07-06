---
name: sdk-ports-go-stale-vs-intree
description: studio-sdk methods ported at extraction time silently diverge from Pilot's evolving in-tree copies — GetTagForSHA shipped bounded (20 tags) in the SDK while in-tree had moved to exhaustive pagination. Before any cutover leg, diff the SDK method against CURRENT in-tree source; run the in-tree test suite against the SDK client to catch drift.
type: pitfall
---

**An extraction-era SDK port is a snapshot, not a contract.** Pilot's in-tree
`internal/adapters/github` kept evolving after the studio-sdk extraction;
methods "already present" in the SDK can be stale versions of themselves.

**Why:** During M7 4d.1 (autopilot → SDK client swap, 2026-07-06), the SDK's
`GetTagForSHA` turned out to be the old bounded `ListTags(20)` lookup while
in-tree had long since moved to exhaustive pagination (per_page=100, 50
pages). Autopilot on the SDK client would have treated anciently-tagged SHAs
as untagged and stalled release draining. **Pilot's own test suite caught it**
— `TestHandleReleasing_ExhaustiveTagDrain` failed the moment autopilot ran
against the SDK client (fixed upstream as studio-sdk v0.28.1, PR #78).
Similarly, v0.28.0 had to add typed `RateLimitError`/`AuthError` because the
SDK never carried them and autopilot's `errors.As` handling would have gone
silently dead — a behavior gap invisible to compilation.

**How to apply:**
1. When cutting any consumer over to an SDK client, don't trust "method
   exists with the right signature" — `diff` the SDK method body against the
   CURRENT in-tree implementation for every load-bearing method.
2. Run the consumer's full in-tree test suite against the SDK-backed build
   BEFORE shipping; httptest-based suites transfer for free (same wire
   format) and are exactly what catches semantic drift.
3. Watch for the invisible class: typed errors (`errors.As` targets),
   pagination bounds, retry classification — these compile fine and fail
   only behaviorally.

Related: [[poller-api-calls-deadlock-stress-suite]] (same M7 family);
TASK-368 (M7 cutover), TASK-385 (v0.27.0 surface), studio-sdk PRs #77/#78.
