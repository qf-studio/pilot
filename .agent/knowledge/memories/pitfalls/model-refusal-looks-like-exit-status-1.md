---
name: model-refusal-looks-like-exit-status-1
description: A backend model refusal surfaces as "unknown: exit status 1" with empty stderr and ~0 tokens — the real cause (stop_reason refusal, category cyber) is visible ONLY in the stream recording; two identical refusals then trip the streak threshold and silently stall the task
type: pitfall
---

# A refused task looks exactly like an infrastructure failure

**Incident 2026-08-26 (pilot-cloud-infra GH-33 → GH-34).** A security-verification
harness remediation issue failed twice in 10–20s with `unknown: exit status 1`,
`stderr=""`, ~800 output tokens. It looked like an environment or repo fault.
The investigation burned time on a dirty repo root, `.pilot/workflow.yaml`
front-matter warnings, and the ledger before the truth turned up in the
**stream recording**:

```
message_delta  stop_reason="refusal"
               stop_details={type:"refusal", category:"cyber", explanation:...}
result         is_error=true  "…appears to violate our Usage Policy…"
```

The model **declined the task**. Nothing about the box was wrong.

## Two compounding traps

1. **Invisible outside the recording.** The executor folds it into `unknown`.
   Same lesson shape as the 08-23 OAuth outage (401s visible only in stream
   recordings). When a failure is fast, 0-token and stderr-empty, **read the
   recording before touching infrastructure**: find it by grepping
   `~/.pilot/recordings/*/metadata.json` for the task_id, then tail
   `stream.jsonl` — but note `task_id` collides across repos, so match
   `project_path` too.
2. **Deterministic → stalls silently.** A refusal reproduces byte-identically,
   so it trips `consecutiveIdenticalFailureThreshold=2` → `stalled` +
   `pilot-blocked` → dropped from poller candidacy with no log line. Retrying
   can never help; only the issue text can change. Fix dispatched as
   [[pilot#5232]] (classify refusals, exempt from the streak, comment on the
   issue).

## The actual cause: framing parity in the issue body

The executor's model sees **only the issue body** — not our intent, not the
repo's purpose. The original harness issue (infra#31) passed because it opened
with the authorization context: "authorized security verification of our own
AWS estate… a verification harness, not offensive tooling… every probe is an
expected-denial assertion." The remediation issue was written tersely — straight
into "three boundaries cannot fail" and how to make probes work — and read cold
as offensive tooling.

## Framing was NOT the cause — proven by a third attempt

Initial reading blamed terse framing. **That was wrong.** Three attempts:

| Attempt | Framing | Content | Result |
|---|---|---|---|
| infra#31 (original) | full authorization context | wrote ALL SIX boundary probes, 2637 lines | **PASSED** |
| infra#33/#34 | terse, then fully contextualised | probe remediation | refused ×3 |
| **infra#36** | ordinary bug-fix framing | **zero probe content** — signal-aware teardown, moving two assignments before an error check, a test fake | **REFUSED** (999 tokens) |

infra#36 contained nothing security-shaped and was still declined. So the
refusal keys on the **package and its surrounding context**
(`isolationharness`, tenant-isolation boundaries, the hostile-ticket framing
in adjacent code and docs) — not on what a given task asks for, and not on how
it is worded. Splitting the work does not help; reframing does not help.

**How to apply**: **the autonomous executor cannot work in this package at
all** — do not dispatch against it; each attempt burns two execution slots
then silently stalls. Route it to a human or an authorized path. More
generally: if a task dies fast with 0 tokens and empty stderr, **read the
stream recording before touching infrastructure**, and if it is a refusal,
stop after ONE reframing attempt — repeated rewording to get past a refusal is
circumvention, not debugging (cf.
[[guard-research-framing-parity-not-bypass]]). Note infra#31 passing means
this is narrower than "security work is refused"; what changed between #31 and
#33 is not established.
