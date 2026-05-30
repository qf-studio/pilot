# TASK-327: Test coverage for the Slack interaction webhook signature gate

**Wave:** 1 (S) · **Pilot** · **Severity:** HIGH (test-gap on a destructive-action gate) ·
**Audit ref:** TASK-322 §high "Slack interaction webhook has 0% test coverage"

---

## Problem

`InteractionHandler.ServeHTTP` and `verifySignature` (`internal/adapters/slack/webhook.go:106-220`) are
the entry point for Slack interactive button clicks — **PR / deployment approve & reject**. Coverage is
`0.0%` (`ServeHTTP`, `verifySignature`, `NewInteractionHandler`); no test references
`HandleInteraction`/`verifySignature`/`signingSecret`.

So the HMAC-SHA256 validation is entirely unverified: the base string `v0:%s:%s`, the `v0=` prefix, the
5-minute replay window (`abs(now-ts) > 60*5`), and the dev-mode skip when `signingSecret == ""`. A
regression that inverts the `hmac.Equal` result, mis-formats the base string, or widens the replay window
would let a **forged button click approve/merge a PR** with zero test signal. GitHub and Linear webhooks
both have signature tests; Slack is the outlier despite gating a destructive action.

## Approach

### Step 1 — `verifySignature` table test (S, ~45 min)
Cases: valid signature passes · tampered body fails · tampered signature fails · expired timestamp
(>5 min) fails · future timestamp beyond window fails · non-numeric timestamp fails · empty
`signingSecret` dev-bypass behaves as documented.

### Step 2 — `ServeHTTP` test (S, ~45 min)
- Post a real signed form payload → assert `200` + `onAction` invoked.
- Forged-signature request → assert `401` + `onAction` NOT invoked.
- `signingSecret == ""` dev-mode bypass path covered.

Build the signed payload with the same HMAC construction the handler expects; use a fake signing secret
from `internal/testutil` (never a realistic `xoxb-`/secret pattern).

## Files to modify
- `internal/adapters/slack/webhook_test.go` (new)

## Test Strategy
- Pure unit tests; no network. Target the same scenarios GitHub/Linear webhook tests cover so the three
  signature gates have parity.

## Effort
S (~1.5h). One PR. Test-only — no production code change unless a test surfaces a real bug (if so, note it
and fix in-scope).

## Out of Scope
- jira/asana/empty-secret verifier wiring — TASK-326.
