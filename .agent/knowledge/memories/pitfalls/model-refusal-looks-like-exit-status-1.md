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

**How to apply**: any issue about security boundaries, isolation testing,
credential handling or exfiltration paths must carry its defensive context
*in the body*: who authorizes it, whose estate it targets, and that assertions
pass on denial. This is **framing parity, not bypass** — state the true nature
of the work accurately (see [[guard-research-framing-parity-not-bypass]]). If a
security-shaped task dies fast with no output, suspect refusal before infra.
