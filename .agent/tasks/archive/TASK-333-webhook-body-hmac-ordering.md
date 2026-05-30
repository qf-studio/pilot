# TASK-333: Preserve raw request body so webhook body-HMAC verification is possible

> ✅ **SHIPPED 2026-05-30 — merged manual as PR #3325** (squash `a750bb4e`). Last open Wave 0–2 item.
> Gateway (`handleJiraWebhook`/`handleAsanaWebhook`) now `io.ReadAll`s the body once, decodes from the
> buffer, and stashes `payload["_raw_body"]` (mirroring the Plane handler). `pilot.go` jira/asana handlers
> verify `VerifySignature([]byte(rawBody), sig)` over the exact bytes; dead `marshalWebhookPayload` + its
> `strings` import removed; `TODO(F2)` markers cleared. gitlab/azuredevops left as header-token/basic-auth
> (not body-HMAC). Tests: gateway raw-body preservation + decode-from-buffer; pilot body-HMAC gating
> (valid passes, bad/tampered blocked) under `-race`. CI green (one flaky `briefs` test re-run).

**Wave:** 2 (M) · **Pilot** · **Severity:** HIGH (security) · **Audit ref:** TASK-322 §"Dropped
dimensions — body-HMAC structurally impossible on jira/gitlab/asana/azuredevops"

---

## Problem

For jira/gitlab/asana/azuredevops, the signature/token is stashed into the parsed payload map, but the
gateway consumes `r.Body` via `json.NewDecoder` **first** — so the raw bytes a body-HMAC needs are already
destroyed by the time any handler runs. Body-HMAC verification is therefore structurally impossible on
those paths; `router.HandleWebhook` just forwards the parsed map and does not re-verify. (TASK-326 wired
the jira/asana verifier calls and fixed the fail-open empty-secret default, but could not do true
raw-body HMAC because the bytes are gone — this task supplies them.)

## Approach
- In the gateway HTTP webhook entrypoint, read the raw body **once** into a buffer
  (`io.ReadAll` / `httputil`), compute/verify the HMAC against the raw bytes **before** JSON decoding,
  then decode from the buffered bytes (`bytes.NewReader`). Pass both the raw bytes and the parsed map
  through to the per-adapter verifier so each can do real body-HMAC.
- Update `router.HandleWebhook` (and the per-adapter `VerifySignature` signatures) to accept the raw body.
- Keep the existing header-token verifiers (gitlab/azuredevops) working; this enables the HMAC ones.

## Files to modify
- gateway webhook HTTP handler (`internal/gateway/…` request entrypoint)
- `internal/gateway` router `HandleWebhook`
- `internal/adapters/{jira,asana,gitlab,azuredevops}/webhook.go` verify signatures (accept raw body)
- corresponding `*_test.go`

## Test Strategy
- Per adapter: a request with a valid body-HMAC over the raw bytes passes; a tampered body fails; confirm
  JSON decoding still works from the buffered bytes. Use `internal/testutil` fake secrets.

## Effort
M (~3-4h). One PR. **Coordinate with TASK-326** (webhook fail-closed) — land 326 first; this builds on it.

## Out of Scope
- Slack interaction webhook (already covered by TASK-327).
