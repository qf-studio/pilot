---
name: consumer-docblock-trusted-over-producer-wire-source
description: ui PR#113 (ui GH-112) shipped green while inverting a wire field's semantics — the executor trusted a stale consumer-side types.ts docblock claiming specVersion = APPLIED generation over the producer source (pilot-console handlers.go:141 sends the TARGET, inst.ConfigGeneration) AND over the incident evidence quoted in the issue itself; it rewrote comments and tests to enshrine the wrong semantics, so every gate stayed green. 4th TASK-460 incident; new evidence shape "doc-vs-wire". Structural fix dispatched: pilot#5009 (Contract Evidence gate) + console#195 (raw-body CI gate). Rule — a field's semantics come from the code that PRODUCES the value, never a consumer-side comment.
type: pitfall
created: 2026-08-20
---

# Consumer docblocks are not a wire contract: cite the producer or you ship inverted semantics green

**What happened.** ui GH-112 asked the instance detail page to render the
applied config generation. The executor found a docblock in
`pilot-console-ui/src/lib/api/types.ts` claiming `specVersion` = applied
generation and built on it — ignoring both the producer
(`pilot-console/internal/instances/handlers.go:141`, which serializes
`inst.ConfigGeneration`, the TARGET) and the incident evidence quoted in the
issue body itself ("v2 applied" at ledger GEN 2 / APPLIED 1 — only possible if
the field is the target). PR#113 shipped only the trivial `v0 → "none
applied"` edge, rewrote comments AND tests to match the wrong reading, and
left the incident reproducing. CI, quality gates, intent judge, self-review:
all green, because the tests asserted self-consistent-but-wrong fixtures.

**Why every gate missed it.** The fix's correctness hinged on a field defined
in a different repo than the one being edited; nothing forced the executor to
read the producing server code instead of a same-repo comment. Self-review
and intent judge are advisory (`strings.Contains` on freeform output — no
hard gate), and no existing TASK-460 candidate direction (diff-surface, ACs
observable, collapse guard) fires here: the diff touched the right files and
the tests failed-when-unwired. This is a fifth, orthogonal evidence shape:
**doc-vs-wire**.

**Fix chain.** Supersession same-day: console#193 → PR#194 (additive
`appliedSpecVersion`, raw-body-pinned test
`handlers_test.go:299` — asserts the raw JSON body string, catches struct-tag
drift) · ui#115 → PR#117 (render from `appliedSpecVersion`, honest fixtures).
Structural class fix dispatched 2026-08-20: **pilot#5009** — machine-validated
Contract Evidence gate (per-project `contract_dependencies` config; daemon
fetches cited producer source via GitHub Contents API and hard-fails the task
when citations are missing, irrelevant, or don't verify) · **console#195** —
producer-side grep CI gate requiring raw-body assertions for `json:`-tagged
DTO changes.

**Recurrence (2026-08-20, 5th class incident, pre-dated the class discovery).**
Dashboard Shipped Activity chart shipped empty-with-data in UI-6/PR#61
(08-14): ui `types.ts:383` docblock claimed `byProject` keys are project
*names*; the producer (`dashboardapi/dto.go:46` + its pinned test) keys by
`projectDTO.Key` (connection UUID). Chart looked up by name → zero bars;
name-keyed fixtures kept the suite green. Exposed only when the first real
shipped cards landed. Fix: ui#118. Confirms `src/lib/api/types.ts` belongs in
ui's `contract_files` when pilot#5009 lands.

**Rules.**
1. A wire field's semantics come from the code that PRODUCES the value
   (server handler / serializer / DB write) — never from a consumer-side
   docblock, even in the same file as the change.
2. When issue evidence contradicts a docblock, the evidence wins; a fix that
   "corrects" tests and comments to match a doc is a red flag for enshrined
   wrong semantics.
3. Same family as [[graphql-mock-tests-cannot-catch-schema-validation]] and
   [[merged-feature-dead-callback-not-bridged-onprcreated]]: a green test
   proves what the test checks, not what the change claims (TASK-460).
