---
name: fresh-get-body-discarded-list-snapshot-validated
description: Both the SDK poller and Pilot's issue handler fetch a fresh GetIssue before dispatch/validation but discard its Body, validating the stale list-snapshot body instead — an issue edited seconds before a poll tick gets judged on its pre-edit content.
type: pitfall
---

**Incident (2026-07-30, pilot#4498 → fixes pilot#4624 + studio-sdk#105).** An operator fixed an issue body at 10:04Z; the guard judged the PRE-edit body at 10:05:37Z and silently re-blocked the issue. Root cause found twice in one call chain: (1) studio-sdk poller `poller.go` pre-dispatch loop fetches `fresh := GetIssue(...)`, uses it only for a done/in-progress label check, then dispatches the original list-snapshot object; (2) Pilot's `handleGithubIssueEventSDK` (cmd/pilot/handlers.go) fetches `realIssue` fresh, lifts only `.State`/`.User`, and builds the spec-validation issue from `ev.Body` — the list snapshot again.

**Why:** "we already do a fresh read here" creates false confidence — both layers fetched fresh data and threaded through only the fields the ORIGINAL author cared about (labels/state), not the field the downstream consumer validates (body). Compounding: `internal/ghbudget` installs a process-wide ETag cache on `http.DefaultTransport`, so even a genuinely fresh GET can serve a 304-synthesized stale body within GitHub's propagation window.

**How to apply:** when a staleness guard does a fresh re-fetch, audit WHICH FIELDS of the fresh object actually flow downstream — a fresh read that threads only labels is a labels-freshness guard, nothing more. For validation-then-destructive-action flows, validate the freshest obtainable body AND apply [[confirm-before-destructive-act-fresh-reads]] before the irreversible step. Related: [[learning_pilot_issue_spec_guard_headers]].
