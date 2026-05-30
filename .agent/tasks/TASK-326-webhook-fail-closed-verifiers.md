# TASK-326: Wire jira/asana webhook verification + fail-closed empty-secret default

**Wave:** 1 (M) · **Pilot** · **Severity:** HIGH (security) ·
**Audit ref:** TASK-322 §"Dropped dimensions — Webhook verification matrix" + §medium repo-allowlist note

---

## Problem

The webhook signature/secret verifiers are registered but, on two adapters, never invoked — and all of
them fail **open** when no secret is configured:

| Source | Verified? | Where |
|---|---|---|
| github | yes | gateway HMAC |
| linear | yes | gateway Ed25519 |
| gitlab | yes | handler header-token |
| azuredevops | yes | handler header-secret |
| **jira** | **NO** | `VerifySignature` defined (`jira/webhook.go`) but the handler registered at `pilot.go:468` never calls it |
| **asana** | **NO** | `VerifySignature` defined (`asana/webhook.go`) but the handler at `pilot.go:506` never calls it |

Additional gap: every verifier shares a `secret == "" → return true` default (e.g. GitHub
`webhook.go:69-72,81-84` returns `true` when the secret is empty). An operator who forgets to set a
secret silently disables verification — a fail-open security default.

> Body-HMAC ordering (raw bytes destroyed by `json.NewDecoder` before the handler runs) is a separate,
> larger architectural fix — see Wave 2 F2. **Out of scope here.**

## Approach

### Step 1 — Invoke verification in jira/asana handlers (M, ~60 min)
- `internal/pilot/pilot.go` jira handler (~468): call `p.jiraWH.VerifySignature(...)` before
  `Handle(...)`; reject (log + drop) on failure.
- `internal/pilot/pilot.go` asana handler (~506): same with `p.asanaWH.VerifySignature(...)`.
- Use whatever signature material is already available on the parsed payload for these adapters; if the
  raw body is unavailable at this layer (the F2 problem), verify with what the handler currently has and
  add a `// TODO(F2): raw-body HMAC` marker rather than silently passing.

### Step 2 — Fail-closed on empty secret (M, ~60 min)
- Flip the `secret == "" → true` default to **reject** across the 8 verifiers, gated behind a single
  explicit dev-mode escape hatch (env `PILOT_ALLOW_UNSIGNED_WEBHOOKS=1`, checked once, logged loudly at
  startup). Mirror the fail-closed precedent in `executor.ValidateTargetRepo` (`repo_guardrail.go:69`).
- Keep the per-adapter sentinel-error pattern Linear already uses (caller decides) where it fits.

### Step 3 — Tests (M)
- Per adapter: valid secret passes; wrong/missing signature rejects; empty-secret rejects unless the dev
  flag is set. Add jira/asana handler-level tests asserting `Handle` is NOT reached on a bad signature.

## Files to modify
- `internal/pilot/pilot.go` (jira ~468, asana ~506 handler registration)
- `internal/adapters/{github,gitlab,jira,asana,azuredevops,...}/webhook.go` (empty-secret default)
- corresponding `*_test.go` per adapter

## Test Strategy
- Table-driven verify tests per adapter + jira/asana handler-invocation tests. Use
  `internal/testutil` fake tokens — never realistic secret patterns (push-protection).

## Effort
M (~3h). One PR.

## Out of Scope
- **F2 body-HMAC ordering** (raw-body preservation in the gateway router) — separate Wave 2 task.
- Slack interaction webhook tests — TASK-327.
